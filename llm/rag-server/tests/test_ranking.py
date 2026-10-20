"""Ranking behaviour that the shortlist depends on.

Each test here corresponds to something that was measured rather than assumed:
normalisation is what makes the blend do anything at all, grouping is what stops
long documents crowding the shortlist, and the absence of filtering is what lets
the caller decide relevance with context this layer does not have.
"""

import pytest

from rag.core.llm.ranking import normalise
from rag.core.llm.ranking import parent_key
from rag.core.llm.ranking import rank_documents


class FakeDocument:
    def __init__(self, content, parent_id=None):
        self.page_content = content
        self.metadata = {"parent_id": parent_id} if parent_id else {}


def chunk(content, parent_id, similarity, reranker_score):
    return (FakeDocument(content, parent_id), similarity, reranker_score)


class TestNormalise:
    def test_maps_range_to_unit_interval(self):
        assert normalise([0.85, 0.90, 0.95]) == pytest.approx([0.0, 0.5, 1.0])

    def test_flat_input_is_neutral_not_a_division_by_zero(self):
        # Retrieval routinely returns near-identical similarities; the old
        # weighted score divided by that spread and got a constant.
        assert normalise([0.87, 0.87, 0.87]) == [0.5, 0.5, 0.5]

    def test_empty(self):
        assert normalise([]) == []

    def test_narrow_band_still_separates(self):
        # The whole point: similarity spans ~0.85-0.90 in practice, and without
        # normalising, that range contributes almost nothing to a weighted sum.
        out = normalise([0.870, 0.872, 0.874])
        assert out[0] == 0.0 and out[-1] == 1.0


class TestParentKey:
    def test_prefers_parent_id(self):
        assert parent_key(FakeDocument("some chunk", "doc-1")) == "doc-1"

    def test_falls_back_to_content_for_unstamped_chunks(self):
        # Chunks indexed before parent_id existed must stay addressable rather
        # than collapsing into a single shared bucket.
        a = parent_key(FakeDocument("alpha text"))
        b = parent_key(FakeDocument("beta text"))
        assert a and b and a != b

    def test_empty_content_without_parent_is_unusable(self):
        assert parent_key(FakeDocument("")) is None


class TestRankDocuments:
    def test_groups_chunks_of_one_document_into_a_single_slot(self):
        ranked = rank_documents(
            [
                chunk("a1", "doc-a", 0.90, 0.10),
                chunk("a2", "doc-a", 0.89, 0.90),
                chunk("a3", "doc-a", 0.88, 0.50),
                chunk("b1", "doc-b", 0.87, 0.80),
            ],
            limit=10,
        )
        assert [r.parent_id for r in ranked] == ["doc-a", "doc-b"]
        assert ranked[0].n_chunks == 3

    def test_document_is_represented_by_its_best_chunk(self):
        ranked = rank_documents(
            [
                chunk("weak", "doc-a", 0.86, 0.10),
                chunk("strong", "doc-a", 0.90, 0.99),
                chunk("other", "doc-b", 0.85, 0.20),
            ],
            limit=10,
        )
        best = {r.parent_id: r for r in ranked}
        assert best["doc-a"].document.page_content == "strong"

    def test_tie_breaks_on_the_reranker_not_retrieval_order(self):
        # With two candidates, normalisation pins them to 0 and 1 on both
        # signals, so a disagreement blends to exactly 0.5 either way. Without a
        # tie-break the chunk that merely came back first would represent the
        # document.
        ranked = rank_documents(
            [
                chunk("weak", "doc-a", 0.90, 0.10),
                chunk("strong", "doc-a", 0.85, 0.99),
            ],
            limit=10,
        )
        assert ranked[0].document.page_content == "strong"

    def test_limit_counts_documents_not_chunks(self):
        # Ten chunks of two documents must not consume ten shortlist slots —
        # this is what lifted answer-present from 86% to 100% at ten slots.
        chunks = [chunk(f"a{i}", "doc-a", 0.9, 0.5) for i in range(5)]
        chunks += [chunk(f"b{i}", "doc-b", 0.9, 0.5) for i in range(5)]
        assert len(rank_documents(chunks, limit=3)) == 2

    def test_returns_everything_ranked_and_filters_nothing(self):
        # No threshold here by design: a per-query normalised score cannot carry
        # an absolute relevance decision.
        ranked = rank_documents(
            [chunk("junk", "doc-a", 0.10, 0.0001), chunk("junk2", "doc-b", 0.11, 0.0002)],
            limit=10,
        )
        assert len(ranked) == 2

    def test_similarity_can_change_the_order(self):
        # Equal cross-encoder scores, different similarity — the blend has to
        # break the tie, otherwise adding similarity back was pointless.
        ranked = rank_documents(
            [chunk("low sim", "doc-a", 0.10, 0.5), chunk("high sim", "doc-b", 0.99, 0.5)],
            limit=10,
        )
        assert ranked[0].parent_id == "doc-b"

    def test_weight_of_one_ignores_the_reranker(self):
        ranked = rank_documents(
            [chunk("a", "doc-a", 0.99, 0.0), chunk("b", "doc-b", 0.10, 1.0)],
            limit=10,
            similarity_weight=1.0,
        )
        assert ranked[0].parent_id == "doc-a"

    def test_weight_of_zero_ignores_similarity(self):
        ranked = rank_documents(
            [chunk("a", "doc-a", 0.99, 0.0), chunk("b", "doc-b", 0.10, 1.0)],
            limit=10,
            similarity_weight=0.0,
        )
        assert ranked[0].parent_id == "doc-b"

    def test_empty_input(self):
        assert rank_documents([], limit=10) == []

    def test_chunk_without_identity_is_skipped_not_crashed(self):
        ranked = rank_documents(
            [chunk("", None, 0.9, 0.9), chunk("real", "doc-a", 0.8, 0.8)],
            limit=10,
        )
        assert [r.parent_id for r in ranked] == ["doc-a"]

    def test_rank_in_records_pre_scoring_position(self):
        ranked = rank_documents(
            [chunk("first", "doc-a", 0.99, 0.1), chunk("second", "doc-b", 0.98, 0.9)],
            limit=10,
        )
        by_parent = {r.parent_id: r for r in ranked}
        assert by_parent["doc-a"].rank_in == 0
        assert by_parent["doc-b"].rank_in == 1
