from rag.core.documents import processing
from rag.core.types import Document
from utils.config import Config


def test_worst_case_chunk_size_never_exceeds_the_reranker_document_window():
    # Live-tested regression guard: a fixed 700-char limit was correct against
    # the reranker config in place when this fix was first written, then
    # silently went stale when RAG_RERANKER_MAX_DOC_CHARS was independently
    # set to 500 — content past that point was hard-sliced out of every
    # rerank call before scoring, dropping a real chunk below threshold
    # entirely. _CHUNK_CHAR_LIMIT is derived from Config.reranker_max_doc_chars
    # specifically so this can't drift out of sync again; this test pins that
    # relationship rather than a specific number.
    worst_case_chunk_size = processing._CHUNK_CHAR_LIMIT + processing._CHUNK_OVERLAP_CHARS
    assert worst_case_chunk_size <= Config.reranker_max_doc_chars


def test_short_document_passes_through_unchanged():
    doc = Document(page_content="short content", metadata={"shape": "narrative_reference"})
    result = processing._chunk_long_documents([doc])

    assert len(result) == 1
    assert result[0] is doc
    assert "chunk_index" not in result[0].metadata


def test_long_document_splits_into_multiple_chunks_under_the_limit():
    paragraphs = [f"Paragraph {i} " + ("x" * 200) for i in range(10)]
    content = "\n\n".join(paragraphs)
    doc = Document(page_content=content, metadata={"shape": "deterministic_procedure", "capabilities": ["k8s.cli"]})

    result = processing._chunk_long_documents([doc])

    assert len(result) > 1
    for chunk in result:
        assert len(chunk.page_content) <= processing._CHUNK_CHAR_LIMIT + processing._CHUNK_OVERLAP_CHARS


def test_metadata_is_copied_onto_every_chunk_not_shared():
    content = "\n\n".join(f"Paragraph {i} " + ("x" * 200) for i in range(10))
    doc = Document(page_content=content, metadata={"shape": "deterministic_procedure", "capabilities": ["k8s.cli"]})

    result = processing._chunk_long_documents([doc])

    assert len(result) > 1
    for i, chunk in enumerate(result):
        assert chunk.metadata["shape"] == "deterministic_procedure"
        assert chunk.metadata["capabilities"] == ["k8s.cli"]
        assert chunk.metadata["chunk_index"] == i
        assert chunk.metadata["chunk_count"] == len(result)

    # Mutating one chunk's metadata must not bleed into another (real dict copy,
    # not a shared reference to the original doc.metadata).
    result[0].metadata["capabilities"].append("mutated")
    assert "mutated" not in result[1].metadata["capabilities"]


def test_adjacent_chunks_overlap_so_a_boundary_never_strands_a_sentence():
    paragraphs = [f"Paragraph {i} " + ("x" * 200) for i in range(10)]
    content = "\n\n".join(paragraphs)
    doc = Document(page_content=content, metadata={})

    result = processing._chunk_long_documents([doc])

    assert len(result) > 1
    tail_of_first = result[0].page_content[-processing._CHUNK_OVERLAP_CHARS :]
    assert tail_of_first in result[1].page_content


def test_a_single_oversized_paragraph_is_hard_sliced_not_left_oversized():
    # No blank-line breaks at all — one giant paragraph well past the limit.
    content = "x" * (processing._CHUNK_CHAR_LIMIT * 3)
    doc = Document(page_content=content, metadata={})

    result = processing._chunk_long_documents([doc])

    assert len(result) > 1
    for chunk in result:
        assert len(chunk.page_content) <= processing._CHUNK_CHAR_LIMIT + processing._CHUNK_OVERLAP_CHARS


def test_a_flush_no_longer_than_the_overlap_is_not_duplicated_as_its_own_chunk():
    # A short accumulated flush is fully re-carried into the next chunk's
    # overlap prefix, so appending it separately would only produce a
    # redundant near-duplicate chunk with no content this test can't already
    # find preserved elsewhere in the output.
    result = processing._split_on_paragraphs("AAAAA\n\n" + "B" * 30, limit=20, overlap=10)

    assert "AAAAA" not in result, "the short flush must not survive as its own standalone chunk"
    assert any("AAAAA" in chunk for chunk in result), "its content must still appear, carried into the next chunk"


def test_split_on_paragraphs_terminates_when_overlap_is_not_smaller_than_limit():
    # A misconfiguration (overlap >= limit) must not spin forever: max(1, ...)
    # guarantees the hard-slice loop always advances by at least one
    # character instead of re-slicing the same string from index 0 forever.
    result = processing._split_on_paragraphs("x" * 50, limit=10, overlap=20)

    assert "".join(result).count("x") >= 50
    for chunk in result:
        assert len(chunk) <= 10


def test_real_test_runbook_chunks_fit_the_reranker_budget():
    # The exact document used in live end-to-end testing this session — the
    # regression guard for the bug this chunking fix was built for.
    content = (
        "# Signup Service Debugging Runbook\n\n"
        "## Overview\nWhen the signup service is experiencing issues, the problem is often "
        "related to the payments service dependency. This runbook provides systematic "
        "debugging steps.\n\n"
        "## Step 1: Check Signup Service Status\n```bash\nkubectl get pods -l app=signup-service\n"
        "kubectl describe pod <signup-pod-name>\nkubectl logs <signup-pod-name>\n```\n\n"
        "Look for:\n- Pod status and restart count\n- Error messages in logs\n"
        "- Connection timeouts to payments service\n\n"
        "## Step 2: Verify Payments Service Health\n**CRITICAL**: Always check the payments "
        "service first when signup issues occur.\n\n```bash\nkubectl get pods -l app=payments-service\n"
        "kubectl describe pod <payments-pod-name>\nkubectl logs <payments-pod-name>\n```\n\n"
        "## Common Root Causes\n1. **Invalid Stripe API Key**: Payments service fails to "
        "initialize, causing signup to fail\n2. **Payments Service Crashlooping**: Due to "
        "database connectivity or API key issues\n3. **Network Issues**: Service-to-service "
        "communication problems\n4. **Resource Constraints**: Payments service running out of "
        "memory/CPU\n\n"
        "## Resolution Steps\n1. **Fix Stripe API Key**: Update the STRIPE_API_KEY environment "
        "variable\n2. **Restart Payments Service**: `kubectl rollout restart deployment/payments-service`"
    )
    doc = Document(page_content=content, metadata={"shape": "deterministic_procedure"})

    result = processing._chunk_long_documents([doc])

    assert len(result) > 1
    # The actual failure mode: "Common Root Causes" must land in some chunk
    # short enough for the reranker to see in full, not truncated out.
    assert any("Common Root Causes" in c.page_content for c in result)
    for chunk in result:
        assert len(chunk.page_content) <= processing._CHUNK_CHAR_LIMIT + processing._CHUNK_OVERLAP_CHARS


def test_chunks_share_a_parent_id_so_the_kb_listing_shows_one_row_per_document():
    from rag.core.documents.collection import _is_listed_point

    content = "\n\n".join(f"Paragraph {i} " + ("x" * 200) for i in range(10))
    chunks = processing._chunk_long_documents([Document(page_content=content, metadata={"title": "Runbook"})])

    assert len(chunks) > 1
    assert len({c.metadata["parent_id"] for c in chunks}) == 1
    listed = [c for c in chunks if _is_listed_point({"page_content": c.page_content, "metadata": c.metadata})]
    assert len(listed) == 1
    assert listed[0].metadata["chunk_index"] == 0


def test_a_caller_supplied_parent_id_is_kept():
    content = "\n\n".join(f"Paragraph {i} " + ("x" * 200) for i in range(10))
    doc = Document(page_content=content, metadata={"parent_id": "caller-owned"})

    assert {c.metadata["parent_id"] for c in processing._chunk_long_documents([doc])} == {"caller-owned"}
