"""Keep each document's source exactly as it arrived, so a user can see what was indexed.

The index holds converted, chunked text — useful for retrieval, and not what anyone means
by "show me the page". This store keeps the original alongside it.

It is a sibling Qdrant collection, one per indexed collection, never the indexed collection
itself, for three reasons: ``handle_updated_documents`` deletes any point not in the current
run's id list, so a source point living there would be wiped on every scrape;
``list_collection_documents`` scrolls the indexed collection and would show source as an
extra row; and a source point would otherwise sit in the same vector index as real chunks.

Identified by collection metadata (``module``), never by its name — that is how every other
collection in this service is classified.

Versions are content-addressed, so an unchanged page writes nothing new, and pruning is
day-collapsed: the newest version per calendar day, then the newest ``MAX_VERSIONS`` of
those. Ten syncs of a page whose macros re-render every time collapse to one version; ten
real edits across ten days all survive.
"""

import hashlib
import logging
import uuid
from datetime import datetime, timezone
from typing import Any, Dict, Iterator, List, Optional

from qdrant_client.models import (
    Distance,
    FieldCondition,
    Filter,
    MatchAny,
    MatchValue,
    PointIdsList,
    PointStruct,
    VectorParams,
)

logger = logging.getLogger(__name__)

# Collection-level metadata marking a collection as source storage. Search-side
# filtering selects on this; nothing reads the collection's name.
MODULE = "kb_source"

MAX_VERSIONS = 10

_VECTOR_SIZE = 1
_PLACEHOLDER_VECTOR = [0.0]
_SCROLL_BATCH = 100

# Bounds a prune: a runaway scroll on the ingest path is worse than a version left behind.
_MAX_PRUNE_BATCHES = 50

# One upsert per sync would be tens of megabytes on a large space; Qdrant caps the
# request body well below that.
_UPSERT_MAX_POINTS = 64
_UPSERT_MAX_BYTES = 8 * 1024 * 1024


def source_collection_name(parent_collection: str) -> str:
    """The sibling collection for an indexed collection.

    Derived from the parent so it inherits whatever keying the parent already uses —
    per-integration for scrapes, per-KB for uploads — and so dropping it needs nothing but
    the name being dropped. The suffix is a uniqueness device, not a classification: that
    is what MODULE is for.
    """
    return f"{parent_collection}__source"


def content_hash(content: str) -> str:
    return hashlib.sha256((content or "").encode("utf-8")).hexdigest()


def _point_id(parent_collection: str, source_id: str, digest: str) -> str:
    """A stable point id for one document version.

    Qdrant accepts only an unsigned integer or a UUID as a point id, so the hash is folded
    into one rather than used as a hex string — which is still deterministic, so the same
    document version always lands on the same point and re-storing it overwrites rather
    than duplicating.
    """
    digest_hex = hashlib.sha256(f"{parent_collection}|{source_id}|{digest}".encode("utf-8")).hexdigest()
    return str(uuid.UUID(hex=digest_hex[:32]))


def _client():
    from rag.qdrant.client import get_qdrant_client

    return get_qdrant_client()


def ensure_collection(client, name: str) -> None:
    if client.collection_exists(name):
        return
    client.create_collection(
        collection_name=name,
        vectors_config=VectorParams(size=_VECTOR_SIZE, distance=Distance.COSINE),
        # Qdrant defaults this to False, and this repo's on-disk work covers vectors and
        # HNSW only — so payloads are RAM-resident, and source payloads are the largest
        # thing we store. It cannot be changed after creation.
        on_disk_payload=True,
        metadata={"module": MODULE},
    )
    logger.info("source_store: created %s", name)


def _upsert_batches(points: List[PointStruct]) -> Iterator[List[PointStruct]]:
    """Split an upsert so one sync cannot exceed the server's request size limit.

    A space's pages are stored verbatim, so a few hundred of them is tens of
    megabytes in one request — and ``store`` swallows failures, so the whole
    sync's sources would go missing without a word.
    """
    batch: List[PointStruct] = []
    size = 0
    for point in points:
        # Encoded, not len(): the cap is a network byte budget, and one CJK character
        # is three bytes, so counting characters would let a batch run 3x over.
        content_len = len(str((point.payload or {}).get("content") or "").encode("utf-8"))
        if batch and (len(batch) >= _UPSERT_MAX_POINTS or size + content_len > _UPSERT_MAX_BYTES):
            yield batch
            batch, size = [], 0
        batch.append(point)
        size += content_len
    if batch:
        yield batch


def store(parent_collection: str, documents: List[Dict[str, Any]], run_id: str, client=None) -> int:
    """Keep the source of each document, and prune that document's old versions.

    ``documents`` are ``{source_id, title, url, content, content_type}``. Returns how many
    points were written — an unchanged document rewrites its existing point rather than
    adding one.
    """
    if not documents:
        return 0
    name = source_collection_name(parent_collection)
    fetched_at = datetime.now(timezone.utc).isoformat()

    points = []
    for document in documents:
        content = document.get("content") or ""
        if not content:
            continue
        digest = content_hash(content)
        source_id = str(document.get("source_id") or digest)
        points.append(
            PointStruct(
                id=_point_id(parent_collection, source_id, digest),
                vector=_PLACEHOLDER_VECTOR,
                payload={
                    "module": MODULE,
                    "source_id": source_id,
                    "title": document.get("title") or "",
                    "url": document.get("url") or "",
                    "content": content,
                    "content_type": document.get("content_type") or "text/html",
                    "content_hash": digest,
                    "fetched_at": fetched_at,
                    "run_id": run_id,
                },
            )
        )
    if not points:
        return 0

    try:
        client = client or _client()
        ensure_collection(client, name)
        for batch in _upsert_batches(points):
            client.upsert(collection_name=name, points=batch)
        prune(parent_collection, sorted({str((p.payload or {}).get("source_id")) for p in points}), client=client)
    except Exception as exc:  # losing the source must never fail an ingest
        logger.warning("source_store: store failed for %s: %s", parent_collection, exc)
        return 0
    return len(points)


def _versions(client, name: str, source_ids: List[str]) -> List[Any]:
    """Every stored version of the given documents, oldest data left behind.

    One scroll for the whole batch rather than one per document: a sync writes hundreds of
    pages, and a scroll each would be hundreds of round trips on the ingest path.

    ``with_payload`` names only the two fields pruning needs. Source payloads hold the raw
    page, so fetching them whole to decide what to delete would pull the entire collection
    through memory — the one thing this store is designed not to do.
    """
    found: List[Any] = []
    offset = None
    for _ in range(_MAX_PRUNE_BATCHES):
        points, offset = client.scroll(
            collection_name=name,
            scroll_filter=Filter(must=[FieldCondition(key="source_id", match=MatchAny(any=source_ids))]),
            limit=_SCROLL_BATCH,
            offset=offset,
            with_payload=["source_id", "fetched_at"],
            with_vectors=False,
        )
        found.extend(points)
        if not points or offset is None:
            break
    return found


def prune(parent_collection: str, source_ids: List[str], client=None) -> int:
    """Keep each document's newest version per day, then the newest MAX_VERSIONS of those.

    Batched across documents: one scroll to read the positions, one delete to remove what
    is stale, however many documents the run touched.
    """
    if not source_ids:
        return 0
    name = source_collection_name(parent_collection)
    try:
        client = client or _client()
        points = _versions(client, name, source_ids)
    except Exception as exc:
        logger.warning("source_store: prune could not read %s: %s", name, exc)
        return 0

    by_source: Dict[str, List[Any]] = {}
    for point in points:
        payload = point.payload if isinstance(point.payload, dict) else {}
        by_source.setdefault(str(payload.get("source_id") or ""), []).append(point)

    stale: List[Any] = []
    for versions in by_source.values():
        if len(versions) <= 1:
            continue
        ordered = sorted(
            versions,
            key=lambda p: str((p.payload if isinstance(p.payload, dict) else {}).get("fetched_at") or ""),
            reverse=True,
        )
        keep, seen_days = set(), set()
        for point in ordered:
            day = str((point.payload if isinstance(point.payload, dict) else {}).get("fetched_at") or "")[:10]
            if day in seen_days:
                continue
            seen_days.add(day)
            keep.add(point.id)
            if len(keep) >= MAX_VERSIONS:
                break
        stale.extend(p.id for p in versions if p.id not in keep)

    if not stale:
        return 0
    try:
        client.delete(collection_name=name, points_selector=PointIdsList(points=stale))
    except Exception as exc:
        logger.warning("source_store: prune could not delete from %s: %s", name, exc)
        return 0
    logger.info("source_store: pruned %d old versions across %d documents", len(stale), len(by_source))
    return len(stale)


def get(parent_collection: str, digest: str, client=None) -> Optional[Dict[str, Any]]:
    """One stored version, by content hash."""
    name = source_collection_name(parent_collection)
    client = client or _client()
    if not client.collection_exists(name):
        return None
    # Deliberately not swallowed: a read that turns an outage into "no source" is
    # indistinguishable from a version that was pruned, and the caller renders the
    # second as a normal state. A write may fail quietly; a read may not.
    points, _ = client.scroll(
        collection_name=name,
        scroll_filter=Filter(must=[FieldCondition(key="content_hash", match=MatchValue(value=digest))]),
        limit=1,
        with_payload=True,
        with_vectors=False,
    )
    return (points[0].payload or {}) if points else None


def documents_for_run(parent_collection: str, run_id: str, limit: int = 100, offset=None, client=None) -> Dict:
    """What a load captured: one entry per document, paginated by Qdrant.

    A list rather than a stored manifest, so it pages natively and nothing has to be
    rewritten whole on every run. Failures are not here — a page that failed produces no
    point — and are recorded on the run row instead.
    """
    name = source_collection_name(parent_collection)
    client = client or _client()
    if not client.collection_exists(name):
        return {"documents": [], "next_offset": None}
    # Not swallowed, for the same reason as get: "this load captured nothing" is a
    # statement about the load, and an unreachable Qdrant must not be able to make it.
    points, next_offset = client.scroll(
        collection_name=name,
        scroll_filter=Filter(must=[FieldCondition(key="run_id", match=MatchValue(value=run_id))]),
        limit=limit,
        offset=offset,
        with_payload=True,
        with_vectors=False,
    )

    documents = [
        {
            "source_id": (p.payload or {}).get("source_id"),
            "title": (p.payload or {}).get("title"),
            "url": (p.payload or {}).get("url"),
            "content_hash": (p.payload or {}).get("content_hash"),
            "fetched_at": (p.payload or {}).get("fetched_at"),
        }
        for p in points
    ]
    # Qdrant's cursor is a point id, which may come back as an int or a UUID object;
    # llm-server decodes it into *string, so normalise here rather than at each caller.
    return {"documents": documents, "next_offset": str(next_offset) if next_offset is not None else None}


def drop(parent_collection: str, client=None) -> bool:
    """Delete a collection's source alongside the collection itself, so nothing derived
    outlives a deleted knowledge base."""
    name = source_collection_name(parent_collection)
    try:
        client = client or _client()
        if not client.collection_exists(name):
            return False
        client.delete_collection(collection_name=name)
        logger.info("source_store: dropped %s", name)
        return True
    except Exception as exc:
        logger.warning("source_store: could not drop %s: %s", name, exc)
        return False
