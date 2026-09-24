"""Remember what a vision model said about an image, so it is asked once.

A knowledge base is re-indexed whenever it is retriggered, and re-extracting a
document produces the same figures every time. Without somewhere to keep the
descriptions, every retrigger pays for the same vision calls again — same
images, same answers, new bill.

The store is a Qdrant collection rather than a table because there is nothing
relational about it and the descriptions are already the kind of thing this
service keeps. It lives apart from the knowledge base's own collection on
purpose: that one is dropped and rebuilt on every update, which is exactly when
the cache has to survive.

The key is the image's bytes, plus the model and prompt that described them:

  * bytes, so a model's wording can never drift between runs — it is asked once
    and the answer is reused verbatim, which is also what keeps a chunk's text
    (and therefore its document id) stable across re-indexes;
  * model and prompt version, so improving either one re-describes rather than
    serving descriptions from the old one forever.

Every failure here is swallowed: a cache that is down costs tokens, and must
never cost an ingest.
"""

import hashlib
import logging
import uuid
from typing import Any, Dict, Iterable, List

from qdrant_client.models import (
    Distance,
    FieldCondition,
    Filter,
    MatchValue,
    PointStruct,
    VectorParams,
)

logger = logging.getLogger(__name__)

COLLECTION_NAME = "kb_figure_descriptions"

# Collection-level metadata marking this as cache storage rather than an embedding
# index, so a collection sweep can tell the two apart. Nothing reads the name.
MODULE = "kb_figures"

# Bump when FIGURE_PROMPT changes in a way that should re-describe existing
# figures. Part of the key, so an old description is never served for a new
# prompt — and so bumping it is a deliberate act rather than a silent one.
PROMPT_VERSION = "1"

# Qdrant requires a vector; nothing here is ever searched by similarity.
_VECTOR_SIZE = 1
_PLACEHOLDER_VECTOR = [0.0]


def cache_key(account_id: str, content_hash: str, model: str) -> str:
    """Stable point id for one (account, image, model, prompt) combination.

    Folded into a UUID because Qdrant accepts only an unsigned integer or a UUID as a point
    id — deterministic either way, so the same image described by the same model and prompt
    always resolves to the same point.
    """
    raw = "|".join([account_id or "", content_hash or "", model or "", PROMPT_VERSION])
    return str(uuid.UUID(hex=hashlib.sha256(raw.encode("utf-8")).hexdigest()[:32]))


def _client():
    from rag.qdrant.client import get_qdrant_client

    return get_qdrant_client()


def _ensure_collection(client) -> None:
    if client.collection_exists(COLLECTION_NAME):
        _heal_wrong_dimension(client)
    else:
        _create(client)
    # After either path, so a collection that predates the index — or one the heal
    # declined to touch because it holds points — gets one too. Creating an index that
    # already exists is a no-op, so this is one cheap call per ingest.
    _ensure_payload_index(client)


def _ensure_payload_index(client) -> None:
    """Index the field the only filtered read matches on.

    ``forget_collection`` deletes by ``parent_collection`` when a knowledge base goes
    away. Unindexed, Qdrant scans every payload to find them.
    """
    try:
        client.create_payload_index(
            collection_name=COLLECTION_NAME, field_name="parent_collection", field_schema="keyword"
        )
    except Exception:
        # A slower cleanup is not worth failing an ingest over.
        logger.warning("figure_cache: could not ensure the parent_collection index", exc_info=True)


def _create(client) -> None:
    client.create_collection(
        collection_name=COLLECTION_NAME,
        vectors_config=VectorParams(size=_VECTOR_SIZE, distance=Distance.COSINE),
        metadata={"module": MODULE},
    )
    logger.info("figure_cache: created %s", COLLECTION_NAME)


def _heal_wrong_dimension(client) -> None:
    """Recreate a cache collection that something rebuilt at an embedder's dimension.

    The cache keeps descriptions behind a 1-dimension placeholder vector. A sweep that
    treats it as an embedding collection leaves it at 384 or 1536, every upsert then fails
    on vector size, and ``store`` swallows that — so the cache stays empty and every
    ingest re-describes every figure at full price. Only an empty collection is replaced:
    points at another dimension were never written by this module.
    """
    try:
        info = client.get_collection(COLLECTION_NAME)
        params = info.config.params.vectors
        size = getattr(params, "size", None)
        # No `size is None` escape: a named-vector collection reports None here, and
        # this module only ever creates a single unnamed vector — so that shape is
        # just as unwritable as a wrong dimension and wants the same repair.
        if size == _VECTOR_SIZE:
            return
        points = info.points_count or 0
        if points:
            logger.error(
                "figure_cache: %s has vector size %s (expected %s) and %d point(s); leaving it alone. "
                "Descriptions cannot be cached until it is rebuilt.",
                COLLECTION_NAME,
                size,
                _VECTOR_SIZE,
                points,
            )
            return
        client.delete_collection(collection_name=COLLECTION_NAME)
        logger.warning(
            "figure_cache: %s was empty at vector size %s; recreating at %s", COLLECTION_NAME, size, _VECTOR_SIZE
        )
        _create(client)
    except Exception:
        # Drop-and-recreate cannot be atomic in Qdrant. Reporting a failed heal here beats
        # it surfacing later as an unexplained missing collection; the next store retries.
        logger.error("figure_cache: could not heal %s", COLLECTION_NAME, exc_info=True)


def load(account_id: str, content_hashes: Iterable[str], model: str) -> Dict[str, Dict[str, str]]:
    """Descriptions already stored for these images, keyed by content hash."""
    keys = {cache_key(account_id, digest, model): digest for digest in content_hashes if digest}
    if not keys:
        return {}

    found: Dict[str, Dict[str, str]] = {}
    try:
        client = _client()
        if not client.collection_exists(COLLECTION_NAME):
            return {}
        # The cache key IS the point id, so this is a direct fetch rather than a filtered
        # scan with pagination.
        points = client.retrieve(
            collection_name=COLLECTION_NAME,
            ids=list(keys),
            with_payload=True,
            with_vectors=False,
        )
        for point in points:
            payload = point.payload if isinstance(point.payload, dict) else {}
            digest = payload.get("content_hash")
            if digest:
                found[str(digest)] = {
                    "summary": payload.get("summary") or "",
                    "transcription": payload.get("transcription") or "",
                }
    except Exception as exc:  # a cache miss only costs tokens
        logger.warning("figure_cache: lookup failed, describing everything: %s", exc)
        return {}

    logger.info("figure_cache: %d of %d figures already described", len(found), len(keys))
    return found


def forget_collection(parent_collection: str, client=None) -> int:
    """Drop the descriptions cached for one knowledge base's figures.

    Called when its collection is deleted. The cache is keyed on image bytes rather than on
    a knowledge base, so without this the descriptions of a deleted KB's figures would stay
    behind, keyed by hashes nothing references.
    """
    try:
        client = client or _client()
        if not client.collection_exists(COLLECTION_NAME):
            return 0
        client.delete(
            collection_name=COLLECTION_NAME,
            points_selector=Filter(
                must=[FieldCondition(key="parent_collection", match=MatchValue(value=parent_collection))]
            ),
        )
        logger.info("figure_cache: forgot descriptions for %s", parent_collection)
        return 1
    except Exception as exc:  # cleanup must never fail a delete
        logger.warning("figure_cache: could not forget %s: %s", parent_collection, exc)
        return 0


def store(account_id: str, figures: List[Dict[str, Any]], model: str, parent_collection: str = "") -> None:
    """Keep the descriptions just produced, so the next load reuses them."""
    points = []
    seen = set()
    for figure in figures:
        digest = figure.get("content_hash")
        summary = figure.get("summary") or ""
        transcription = figure.get("transcription") or ""
        if not digest or (not summary and not transcription) or digest in seen:
            continue
        seen.add(digest)
        points.append(
            PointStruct(
                id=cache_key(account_id, digest, model),
                vector=_PLACEHOLDER_VECTOR,
                payload={
                    "key": cache_key(account_id, digest, model),
                    "account_id": account_id,
                    # Recorded so the cache can be cleared when this knowledge base is
                    # deleted; the key itself is the image, not the knowledge base.
                    "parent_collection": parent_collection,
                    "content_hash": digest,
                    "model": model,
                    "prompt_version": PROMPT_VERSION,
                    "summary": summary,
                    "transcription": transcription,
                },
            )
        )
    if not points:
        return

    try:
        client = _client()
        _ensure_collection(client)
        client.upsert(collection_name=COLLECTION_NAME, points=points)
        logger.info("figure_cache: stored %d descriptions", len(points))
    except Exception as exc:  # failing to cache must not fail the ingest
        logger.warning("figure_cache: store failed: %s", exc)
