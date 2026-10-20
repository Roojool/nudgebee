"""The Load History ledger reads in documents, not chunks.

`67ccd3f5ca` put `_chunk_long_documents` ahead of embedding, so both figures
behind the modal's **Documents** column silently changed units — dev shows
`0 / 151` for every sync up to 2026-09-22 and `2693 / 2857` from 2026-09-23.
These pin them back to source documents.
"""

from rag.core.embeddings.tracker import EmbeddingTracker


class _Embeddings:
    model = "test-model"


def _tracker():
    return EmbeddingTracker(account_id="acct", collection_name="c", expected_document_count=3)


def test_chunks_of_one_document_count_as_one_document():
    tracker = _tracker()
    for index in range(19):
        tracker.record(
            doc_id=f"chunk-{index}",
            text="body",
            embedding_model=_Embeddings(),
            document_key="page-1",
        )

    assert tracker.total_docs == 19, "the chunk counter still measures embedded units"
    assert len(tracker._document_keys) == 1, "but the ledger reports one document"


def test_unchunked_documents_key_on_their_own_id():
    tracker = _tracker()
    for doc_id in ("a", "b", "c"):
        tracker.record(doc_id=doc_id, text="body", embedding_model=_Embeddings(), document_key=None)

    assert len(tracker._document_keys) == 3


def test_mixed_chunked_and_unchunked():
    tracker = _tracker()
    for index in range(5):
        tracker.record(doc_id=f"long-{index}", text="body", embedding_model=_Embeddings(), document_key="long")
    tracker.record(doc_id="short", text="body", embedding_model=_Embeddings(), document_key=None)

    assert tracker.total_docs == 6
    assert len(tracker._document_keys) == 2


def test_the_pipeline_keys_each_chunk_on_its_parent(monkeypatch):
    """The embedding path must hand the parent through, or the set degrades to chunks."""
    from rag.core.documents import processing
    from rag.core.types import Document

    monkeypatch.setattr(processing, "add_documents_to_vector_store", lambda **kwargs: None)

    recorded = []

    class SpyTracker:
        def record(self, *, doc_id, text, embedding_model, usage_metadata=None, document_key=None):
            recorded.append((doc_id, document_key))

    batch = [
        Document(page_content="first half", metadata={"parent_id": "page-1", "chunk_index": 0}),
        Document(page_content="second half", metadata={"parent_id": "page-1", "chunk_index": 1}),
        Document(page_content="a short page", metadata={}),
    ]
    processing._create_embeddings_batch(batch, _Embeddings(), "coll", {}, tracker=SpyTracker())

    assert [key for _, key in recorded[:2]] == ["page-1", "page-1"], "both chunks key on their page"
    doc_id, key = recorded[2]
    assert key == doc_id, "an unchunked document keys on its own id"


def test_expected_count_is_taken_before_chunking(monkeypatch):
    """The submitted figure is the document count handed in, not the chunk count."""
    from rag.core.documents import processing
    from rag.core.types import Document

    captured = {}

    class SpyTracker:
        def __init__(self, *args, **kwargs):
            captured.update(kwargs)

        def start_timer(self):
            # Nothing past construction is under test.
            raise RuntimeError("stop after construction")

    monkeypatch.setattr(processing, "EmbeddingTracker", SpyTracker)

    # Three documents well over the chunk limit, so chunking would inflate the count.
    docs = [Document(page_content="paragraph. " * 500, metadata={}) for _ in range(3)]
    assert len(processing._chunk_long_documents(docs)) > 3, "fixture must actually chunk"

    try:
        processing.process_documents(
            documents=docs,
            embeddings=_Embeddings(),
            account_id=None,
            module="knowledge_base",
            collection_name="coll",
            # Supplying a store takes the branch that skips collection setup,
            # so the test never reaches for Qdrant.
            vector_store=object(),
        )
    except RuntimeError:
        pass

    assert captured["expected_document_count"] == 3, "submitted count is documents, not chunks"


class _FakeTxn:
    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return False


class _FakeConnection(_FakeTxn):
    def __init__(self, captured):
        self.captured = captured

    def begin(self):
        return _FakeTxn()

    def execute(self, query, params):
        self.captured.append(params)


class _FakeEngine:
    def __init__(self, captured):
        self.captured = captured

    def connect(self):
        return _FakeConnection(self.captured)


def test_persist_submits_documents_and_clears_state(monkeypatch):
    """The value bound to document_count is documents; persisting resets the run."""
    from rag.core.embeddings import tracker as tracker_module

    captured = []
    monkeypatch.setattr(tracker_module, "engine", _FakeEngine(captured))

    tracker = _tracker()
    tracker.start_timer()
    # Two chunks of one page, then a second page: three embedded units, two documents.
    for doc_id, key in (("c0", "page-1"), ("c1", "page-1"), ("c2", "page-2")):
        tracker.record(doc_id=doc_id, text="body", embedding_model=_Embeddings(), document_key=key)

    tracker.persist()

    assert captured[0]["document_count"] == 2, "the column takes documents, not chunks"
    assert tracker.total_tokens == 0
    assert tracker._document_keys == set(), "a later run must not inherit these keys"


def test_fixture_matches_what_dev_showed():
    """151 pages chunking to 2857 units must report 151, not 2857."""
    tracker = _tracker()
    pages = 151
    per_page = 2857 // pages
    for page in range(pages):
        for chunk in range(per_page):
            tracker.record(
                doc_id=f"p{page}-c{chunk}",
                text="body",
                embedding_model=_Embeddings(),
                document_key=f"page-{page}",
            )
    assert len(tracker._document_keys) == pages
    assert tracker.total_docs == pages * per_page
