import logging
import time
from typing import Dict, Optional

from rag.core.types import Document

from rag.core.utils.db_query import get_live_kb_collection_names, get_tenant_id_for_account
from rag.qdrant.client import list_collections_optimized
from rag.core.llm import local_reranker
from rag.core.llm import ranking
from rag.core.monitoring.audit import RetrievalTrace, TraceCandidate
from utils.config import Config

logger = logging.getLogger(__name__)

# Collection-name shapes owned by an ``llm_knowledgebases`` row. Anything else
# (``nudgebee_docs``, per-module collections, …) is not KB-backed and is never
# subject to the live-KB check.
_MANUAL_KB_PREFIX = "kb_"
_INTEGRATION_KB_SUFFIX = "_knowledge_base"


def _is_kb_backed_collection(collection, tenant_id):
    """True when a collection is attributable to an ``llm_knowledgebases`` row.

    Only attributable collections may be dropped — an unrecognised one is kept,
    so we never silently stop searching data we cannot account for.

    ``<uuid>_knowledge_base`` is shared by three writers, and two of them are
    *not* integration KBs:

    - the tenant-level user KB, ``<tenant_id>_knowledge_base``
      (``loaders.account.load_tenant_knowledge_base_docs``); and
    - legacy per-account collections renamed from ``<account_id>_docs`` by
      ``module_retag_migration``, which carry an ``account`` tag because they
      predate tenant scoping.

    Both are excluded here; what remains under that suffix is integration-owned.
    """
    name = collection.name
    if name.startswith(_MANUAL_KB_PREFIX):
        return True
    if not name.endswith(_INTEGRATION_KB_SUFFIX):
        return False
    owner = name[: -len(_INTEGRATION_KB_SUFFIX)]
    if tenant_id and owner == str(tenant_id):
        return False
    return not (collection.metadata or {}).get("account")


def _drop_dead_kb_collections(collections, account_id, tenant_id):
    """Return the names of ``collections``, minus the dead knowledge-base ones.

    A collection is dropped only when it is KB-attributable *and* absent from
    the account's live set — i.e. its knowledge base was archived, or its
    integration was disabled or deleted (deleting an integration cascades the
    KB row away, stranding the vector collection). Without this, every search
    keeps reading collections whose integration no longer exists, which
    multiplies duplicate copies of the same page across the result set.

    Storage cleanup is a separate concern: dropping at read time means
    correctness does not wait on a Qdrant sweep.
    """
    kb_backed = {collection.name for collection in collections if _is_kb_backed_collection(collection, tenant_id)}
    if not kb_backed:
        return [collection.name for collection in collections]

    live_names = get_live_kb_collection_names(account_id, tenant_id)
    if live_names is None:
        logger.warning(
            "Could not resolve live knowledge bases for account %s / tenant %s - " "excluding %d KB-backed collections",
            account_id,
            tenant_id,
            len(kb_backed),
        )
        return [collection.name for collection in collections if collection.name not in kb_backed]

    kept, dropped = [], []
    for collection in collections:
        if collection.name in kb_backed and collection.name not in live_names:
            dropped.append(collection.name)
        else:
            kept.append(collection.name)

    if dropped:
        logger.info(f"Skipping {len(dropped)} collections with no live knowledge base: {dropped}")
    return kept


def _filter_collections_for_module_and_account(
    collections, module, account_id, collection_name, tenant_id=None, restrict_to_collection=False
):
    """Filter collections by module + visibility scope.

    A collection is included when its ``module`` tag matches AND at least one
    visibility predicate holds:

    - ``account`` matches the requested ``account_id`` (legacy per-account KBs).
    - ``account`` equals ``"global"`` (shared resources like product docs).
    - ``tenant_id`` matches the requested ``tenant_id`` (tenant-scoped
      integration collections — Confluence/ServiceNow landed here after the
      per-tenant scrape refactor).

    The tenant path lets tenant-level integrations surface for every cloud
    account in the tenant without listing every account on the collection.

    Visible is not the same as live: a matched collection is dropped again by
    ``_drop_dead_kb_collections`` when its knowledge base is unavailable. An
    explicitly requested collection may bypass the module tag, but never the
    visibility scope or live-KB gate.

    ``restrict_to_collection`` inverts that last rule: instead of ADDING the
    named collection to the discovered set, it narrows the search down to it.
    The narrowing intersects with what was already visible, so a caller can
    only restrict to a collection it could already have searched — naming
    another tenant's collection yields an empty search, never a targeted read.
    """
    matched = []
    for collection in collections:
        metadata = collection.metadata
        if not metadata:
            continue

        explicit = collection.name == collection_name and not restrict_to_collection
        if metadata.get("module") != module and not explicit:
            continue

        is_matching_account = metadata.get("account") == account_id if account_id else False
        is_global_account = metadata.get("account") == "global"
        is_matching_tenant = (
            tenant_id is not None and metadata.get("tenant_id") is not None and metadata["tenant_id"] == str(tenant_id)
        )

        if is_matching_account or is_global_account or is_matching_tenant:
            matched.append(collection)

    collection_names = _drop_dead_kb_collections(matched, account_id, tenant_id)
    if collection_name and restrict_to_collection:
        if collection_name in collection_names:
            return [collection_name]
        logger.info(
            "Restricted search requested for collection %s, which is not visible to account %s / tenant %s - "
            "returning no collections",
            collection_name,
            account_id,
            tenant_id,
        )
        return []
    return collection_names


def _collection_scope(metadata):
    """Classify a collection from its own metadata, not from its name.

    Qdrant collection metadata is the authority on what a collection holds:

    - ``account == "global"``     -> shared content with no knowledge-base row
      (NudgeBee product docs). Callers must not expect to attribute it to a KB.
    - ``source == "user_kb"``     -> a manual knowledge base
    - ``source in (confluence, servicenow, ...)`` -> a synced integration KB
    - ``module``                  -> which subsystem owns it (knowledge_base,
      prometheus, logs, ...), so non-KB agent collections are distinguishable.

    Returned verbatim to the caller so llm-server does not have to re-derive any
    of this by parsing collection names or matching document text.
    """
    metadata = metadata or {}
    scope = "account"
    if metadata.get("account") == "global":
        scope = "global"
    elif metadata.get("tenant_id"):
        scope = "tenant"
    return {
        "collection_scope": scope,
        "collection_module": metadata.get("module") or "",
        "collection_source": metadata.get("source") or "",
    }


def _collection_classification(collection, tenant_id):
    """Full classification of a collection: scope plus whether it is KB-backed.

    ``collection_kb_backed`` is the field callers must branch on. It reuses
    ``_is_kb_backed_collection`` - the same predicate ``_drop_dead_kb_collections``
    trusts - so "does an llm_knowledgebases row exist for this?" has ONE answer in
    the system. Three kinds of collection are NOT KB-backed and can never be
    attributed to a knowledge base: the global product-docs collection, the
    tenant-level user KB, and legacy per-account collections renamed from
    ``<account_id>_docs``. Callers that demand a KB row for those drop content
    that is legitimately un-owned.
    """
    out = _collection_scope(collection.metadata or {})
    out["collection_kb_backed"] = _is_kb_backed_collection(collection, tenant_id)
    return out


def _retrieve_documents_from_collections(
    collection_names,
    query,
    min_results,
    account_id,
    metadata_filter: Optional[Dict] = None,
    collection_metadata: Optional[Dict] = None,
):
    """
    Retrieves documents from collections by directly calling search logic.
    Works with Qdrant.
    """
    if not collection_names:
        return []

    logger.info(f"Searching {len(collection_names)} collections directly")
    from rag.search.search_logic import search_collections

    serializable_results = search_collections(collection_names, query, account_id, min_results, metadata_filter)

    # Reconstruct the Document objects
    all_docs = []
    collection_metadata = collection_metadata or {}
    for item in serializable_results:
        md = item["metadata"] or {}
        # Attach the owning collection's classification so the caller can tell a
        # global collection from a KB-backed one without guessing.
        source_collection = md.get("collection")
        if source_collection in collection_metadata:
            md = {**md, **collection_metadata[source_collection]}
        doc = Document(page_content=item["page_content"], metadata=md)
        score = item["score"]
        all_docs.append((doc, score))

    logger.info(f"Search complete: retrieved {len(all_docs)} total documents.")
    return all_docs


def _scorer_version() -> str:
    """Describe the scoring configuration, derived from it rather than declared.

    Composed from the settings that actually decide a score, so changing any of
    them changes this string on its own. A hand-maintained version label goes
    stale silently and makes two different configurations indistinguishable in
    the trace, which is the one thing the trace exists to prevent.
    """
    return (
        f"emb={Config.embeddings_model_id};"
        f"rr={Config.reranker_model}/{Config.reranker_max_length}/{Config.reranker_max_doc_chars};"
        f"score=w{Config.ranked_similarity_weight:.2f};"
        f"limit={Config.ranked_limit}"
    )


def _ranked_documents(query, similar_docs_flat, no_of_results, token_usage, trace):
    """Score every candidate, group chunks into documents, return the best N.

    No filtering: a per-query normalised blend cannot carry an absolute
    relevance decision, because its top candidate scores 1.0 whether or not
    anything relevant was found. The caller decides.
    """
    pool_size = max(no_of_results * 2, Config.reranking_max_docs)
    pool = similar_docs_flat[:pool_size]

    started = time.monotonic()
    scores, ok = local_reranker.score_documents(query, pool)
    score_ms = int((time.monotonic() - started) * 1000)
    if not ok:
        # Same contract as the threshold path: an unscored list is not a ranked
        # one, and returning it unranked is what previously put six irrelevant
        # documents into a prompt.
        logger.warning("Reranking unavailable — returning no documents rather than unranked ones")
        if trace is not None:
            trace.fallback_reason = "rerank_unavailable"
            trace.n_retrieved = len(pool)
            trace.n_injected = 0
            trace.score_ms = score_ms
        return [], token_usage, trace

    scored_chunks = [(doc, sim, scores[i]) for i, (doc, sim) in enumerate(pool)]
    ranked = ranking.rank_documents(
        scored_chunks, limit=Config.ranked_limit, similarity_weight=Config.ranked_similarity_weight
    )

    if trace is not None:
        trace.scorer_version = _scorer_version()
        trace.requested_k = no_of_results
        trace.n_retrieved = len(pool)
        trace.n_injected = len(ranked)
        trace.score_ms = score_ms
        blended = ranking.blend(scored_chunks, Config.ranked_similarity_weight)
        # The chunk that won each returned document's slot, by identity — several
        # chunks of one document can tie on score, and only one of them was
        # actually sent.
        winners = {id(r.document) for r in ranked}
        order = {r.parent_id: i for i, r in enumerate(ranked)}
        for index, (doc, sim, rr) in enumerate(scored_chunks):
            key = ranking.parent_key(doc)
            title, url = _candidate_identity(doc)
            if id(doc) in winners:
                # This chunk represented its document in the response.
                status, rank_out = "returned", order.get(key)
            elif key in order:
                # Its document was returned, but through a better chunk of the
                # same document. Distinct from being dropped on relevance: the
                # text did reach the caller, this passage did not.
                status, rank_out = "dropped_sibling", None
            else:
                status, rank_out = "dropped_rank", None
            trace.candidates.append(
                TraceCandidate(
                    rank_in=index,
                    rank_out=rank_out,
                    ref=key,
                    chunk=doc.page_content or "",
                    status=status,
                    sim=sim,
                    rr=rr,
                    # Recorded for every candidate, not only the survivors, so
                    # "how close was what we dropped" is answerable.
                    weighted=blended[index],
                    title=title,
                    url=url,
                )
            )

    logger.info(
        f"Ranked {len(pool)} chunks into {len(ranked)} documents " f"(limit={Config.ranked_limit}, score_ms={score_ms})"
    )
    return [(_with_retrieval_metadata(r, rank), r.weighted) for rank, r in enumerate(ranked)], token_usage, trace


def _with_retrieval_metadata(ranked, rank):
    """Attach the per-signal scores to the document the caller receives.

    The caller needs the similarity and cross-encoder scores separately, not only
    the blend they were combined into — it is judging these candidates and the
    two signals disagree in ways the single number hides. Carrying them on the
    document's metadata keeps the ``(document, score)`` shape every existing
    caller unpacks, rather than forking the return type by mode.

    The metadata dict is copied rather than mutated: it comes from the Qdrant
    payload, and the collection metadata cache hands out shared objects.
    """
    document = ranked.document
    metadata = dict(document.metadata or {})
    metadata["retrieval"] = {
        "rank": rank,
        "parent_id": ranked.parent_id,
        "similarity": round(float(ranked.similarity), 4),
        "reranker_score": round(float(ranked.reranker_score), 4),
        "weighted": round(float(ranked.weighted), 4),
        "n_chunks": ranked.n_chunks,
    }
    document.metadata = metadata
    return document


def _candidate_identity(document) -> tuple[str | None, str | None]:
    """The document's title and link, for naming it to a reader."""
    metadata = getattr(document, "metadata", None) or {}
    title = metadata.get("title") or None
    url = metadata.get("url") or None
    return (str(title) if title else None, str(url) if url else None)


def get_matching_documents(
    query: str,
    no_of_results: int,
    account_id: str,
    module: Optional[str] = None,
    collection_name: Optional[str] = None,
    metadata_filter: Optional[Dict] = None,
    use_reranking: bool = False,
    tenant_id: Optional[str] = None,
    restrict_to_collection: bool = False,
    trace: Optional[RetrievalTrace] = None,
):
    """
    Returns:
        tuple: (documents, token_usage_metadata, trace) where token_usage_metadata
        contains LLM usage info and trace records every candidate that was scored,
        returned or dropped. trace is None when the caller did not ask for one.
    """
    # Resolve tenant_id lazily from the account when the caller didn't supply
    # one — keeps tenant-scoped integration collections (Confluence, ServiceNow)
    # reachable from legacy callers that only know about cloud accounts.
    if tenant_id is None and account_id:
        tenant_id = get_tenant_id_for_account(account_id)

    logger.info(
        f"Getting matching documents for module {module}, account {account_id}, tenant {tenant_id} "
        f"with query: {query} and k: {no_of_results}"
    )

    token_usage: dict = {}

    if trace is not None:
        # Set here rather than in the ranking helper so it survives every exit —
        # the no-collections, empty, reranker-down and exception paths all
        # returned rows with no k at all, which reads as "no search happened".
        trace.requested_k = no_of_results

    try:
        # Find relevant collections
        logger.info(f"Listing collections for module {module}, account {account_id}, tenant {tenant_id}")
        collections = list_collections_optimized()
        collection_names = _filter_collections_for_module_and_account(
            collections,
            module,
            account_id,
            collection_name,
            tenant_id=tenant_id,
            restrict_to_collection=restrict_to_collection,
        )
        logger.info(
            f"Found {len(collection_names)} collections for module {module}, account {account_id}, "
            f"tenant {tenant_id}, collections: {collection_names}"
        )
        if not collection_names:
            # No collection was visible to this account/tenant. Distinct from a
            # search that ran and found nothing, and the trace has to say which:
            # this is usually a scoping or eligibility problem, not a relevance one.
            if trace is not None:
                trace.fallback_reason = "no_collections"
                trace.n_retrieved = 0
                trace.n_injected = 0
            return [], {}, trace

        # Retrieve documents from collections using multiprocessing
        logger.info(f"Retrieving documents from collections: {collection_names}")
        similar_docs_flat = _retrieve_documents_from_collections(
            collection_names,
            query,
            no_of_results,
            account_id,
            metadata_filter,
            collection_metadata={c.name: _collection_classification(c, tenant_id) for c in collections},
        )
        if not similar_docs_flat:
            # Collections were searched and returned nothing — an unreachable
            # Qdrant looks identical to an empty index from here, so record that
            # retrieval came back empty rather than leaving a blank row.
            if trace is not None:
                trace.fallback_reason = "rag_empty"
                trace.n_retrieved = 0
                trace.n_injected = 0
            return [], {}, trace

        # Sort by relevance score
        similar_docs_flat.sort(key=lambda x: x[1], reverse=True)

        if use_reranking and Config.ranked_mode:
            return _ranked_documents(query, similar_docs_flat, no_of_results, token_usage, trace)

        if use_reranking:
            # Cap docs sent to the reranker to bound latency.
            max_docs_for_reranking = max(no_of_results * 2, Config.reranking_max_docs)
            docs_for_reranking = similar_docs_flat[:max_docs_for_reranking]
            if len(similar_docs_flat) > max_docs_for_reranking:
                logger.info(f"Capping docs for reranking: {len(similar_docs_flat)} -> {max_docs_for_reranking}")

            docs_reranked, ok = local_reranker.rerank(query, docs_for_reranking)
            if not ok:
                # Fail closed. Returning the unranked list here is what shipped
                # six irrelevant documents into a prompt: retrieval always
                # yields its nearest k, and without a working relevance score
                # nothing downstream can tell a good hit from a bad one.
                logger.warning("Reranking unavailable — returning no documents rather than unranked ones")
                if trace is not None:
                    trace.fallback_reason = "rerank_unavailable"
                    trace.n_retrieved = len(similar_docs_flat)
                    trace.n_injected = 0
                return [], token_usage, trace
        else:
            logger.info("Skipping reranking (use_reranking=False)")
            docs_reranked = similar_docs_flat

        returned = docs_reranked[:no_of_results]
        if trace is not None:
            # The unranked path serves callers that ask for no reranking, and it
            # was writing a row carrying only the query — no counts, no
            # candidates, indistinguishable from a search that never ran.
            trace.n_retrieved = len(similar_docs_flat)
            trace.n_injected = len(returned)
            kept = {id(doc) for doc, _ in returned}
            for index, (doc, sim) in enumerate(docs_reranked):
                title, url = _candidate_identity(doc)
                trace.candidates.append(
                    TraceCandidate(
                        rank_in=index,
                        rank_out=index if id(doc) in kept else None,
                        ref=ranking.parent_key(doc),
                        chunk=doc.page_content or "",
                        status="returned" if id(doc) in kept else "dropped_rank",
                        sim=sim,
                        title=title,
                        url=url,
                    )
                )
        logger.info(f"Request complete - Returned {len(returned)} docs")
        return returned, token_usage, trace
    except Exception as e:
        logger.error(f"Error in get_matching_documents: {str(e)}")
        if trace is not None:
            trace.fallback_reason = "error"
        return [], token_usage, trace
