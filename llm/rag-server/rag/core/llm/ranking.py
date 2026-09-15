"""Rank retrieved chunks into a shortlist of documents.

Retrieval returns chunks; a caller wants documents. Three things happen here, in
this order, and each was measured against 300 real questions before it was
written:

Blend. The cross-encoder alone puts the answer first for 43% of answerable
questions; blending the retrieval similarity back in raises that to 45% and
top-3 from 65% to 71%. The blend only works on *normalised* scores — raw
similarity spans roughly 0.85-0.90 across a query's candidates, so an unnormalised
weighted sum is the cross-encoder plus a near-constant, which is what the
reranker this replaced was doing.

Group. Several chunks of one document compete with each other for shortlist
slots, and a document chunked into more pieces gets more entries purely for
being long. Grouping by parent and keeping each document's best chunk removes
both effects: at ten slots it lifts the answer-present rate from 86% to 100% on
documentation questions, and it cuts the downstream judge's work from ten calls
to about six.

No threshold. Deciding relevance needs a score that means the same thing across
queries, and a per-query normalised blend does not — the top document always
scores 1.0 whether or not anything is relevant. That decision belongs to the
caller, which has the conversation to judge against.
"""

from dataclasses import dataclass
from typing import Dict
from typing import List
from typing import Optional
from typing import Tuple


@dataclass
class RankedDocument:
    """One document on the shortlist, with the chunk that earned its place."""

    document: object  # rag.core.types.Document — the best-scoring chunk
    parent_id: Optional[str]
    similarity: float
    reranker_score: float
    weighted: float
    n_chunks: int  # chunks of this document among the candidates
    rank_in: int  # where its best chunk sat after retrieval


def normalise(values: List[float]) -> List[float]:
    """Min-max to 0-1 within one query's candidates.

    A flat set maps to 0.5 rather than dividing by zero: with nothing to
    separate, every candidate is equally (un)distinguished.
    """
    if not values:
        return []
    low, high = min(values), max(values)
    if high - low < 1e-9:
        return [0.5] * len(values)
    return [(v - low) / (high - low) for v in values]


def parent_key(document) -> Optional[str]:
    """Identify the document a chunk came from.

    ``parent_id`` is written by ingest. Chunks indexed before that fall back to
    a prefix of their own text, which groups nothing but keeps them addressable
    rather than collapsing every legacy chunk into one bucket.
    """
    metadata = getattr(document, "metadata", None) or {}
    parent = metadata.get("parent_id")
    if parent:
        return str(parent)
    content = getattr(document, "page_content", "") or ""
    return content[:200] or None


def blend(
    scored_chunks: List[Tuple[object, float, float]],
    similarity_weight: float = 0.5,
) -> List[float]:
    """The blended score for every chunk, in the order given.

    Exposed separately from ``rank_documents`` so a caller recording what was
    dropped can record how close it came. Without it, everything that did not
    make the shortlist has no score at all, and "why was this dropped" is
    unanswerable for exactly the candidates the question is about.
    """
    if not scored_chunks:
        return []
    similarities = normalise([s for _, s, _ in scored_chunks])
    reranked = normalise([r for _, _, r in scored_chunks])
    rerank_weight = 1.0 - similarity_weight
    return [similarity_weight * similarities[i] + rerank_weight * reranked[i] for i in range(len(scored_chunks))]


def rank_documents(
    scored_chunks: List[Tuple[object, float, float]],
    limit: int,
    similarity_weight: float = 0.5,
) -> List[RankedDocument]:
    """Shortlist ``limit`` distinct documents from scored chunks.

    ``scored_chunks`` is ``(document, similarity, reranker_score)`` in retrieval
    order. Returns the best chunk of each of the top ``limit`` documents,
    ordered by blended score. No filtering: everything retrieved is ranked, and
    what to keep is the caller's decision.
    """
    if not scored_chunks:
        return []

    blended = blend(scored_chunks, similarity_weight)

    best: Dict[str, RankedDocument] = {}
    counts: Dict[str, int] = {}
    for index, (document, similarity, reranker_score) in enumerate(scored_chunks):
        key = parent_key(document)
        if key is None:
            continue
        counts[key] = counts.get(key, 0) + 1
        weighted = blended[index]
        current = best.get(key)
        # Best chunk wins the document's slot. Mean dilutes a single strong
        # passage with its weaker siblings, and sum rewards length; both scored
        # worse than max over the same 123 answerable questions.
        #
        # Ties break on the cross-encoder score. They are not hypothetical: with
        # few candidates, min-max normalisation pins the extremes to 0 and 1, so
        # two chunks that disagree between the signals blend to exactly 0.5 and
        # would otherwise be settled by retrieval order. The cross-encoder is
        # the better-separating of the two signals, so it decides.
        if current is None or (weighted, reranker_score) > (current.weighted, current.reranker_score):
            best[key] = RankedDocument(
                document=document,
                parent_id=key,
                similarity=similarity,
                reranker_score=reranker_score,
                weighted=weighted,
                n_chunks=0,  # filled below, once every chunk has been seen
                rank_in=index,
            )

    for key, ranked in best.items():
        ranked.n_chunks = counts[key]

    return sorted(best.values(), key=lambda r: r.weighted, reverse=True)[: max(0, limit)]
