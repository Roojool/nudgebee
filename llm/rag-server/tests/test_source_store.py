"""Storing each document's source as it arrived, and pruning old versions."""

from types import SimpleNamespace

import pytest

from rag.core.documents import source_store


class FakeClient:
    """Enough Qdrant to exercise store/prune/get without a server."""

    def __init__(self, existing=True):
        self.points = {}
        self.collections = {"parent__source"} if existing else set()
        self.created = []
        self.payload_indexes = []
        self.deleted_collections = []

    def collection_exists(self, name):
        return name in self.collections

    def create_collection(self, collection_name, **kwargs):
        self.collections.add(collection_name)
        self.created.append((collection_name, kwargs))

    def create_payload_index(self, collection_name, field_name, field_schema):
        self.payload_indexes.append((collection_name, field_name, field_schema))

    def delete_collection(self, collection_name):
        self.collections.discard(collection_name)
        self.deleted_collections.append(collection_name)

    def upsert(self, collection_name, points):
        for point in points:
            self.points[point.id] = SimpleNamespace(id=point.id, payload=dict(point.payload))

    def delete(self, collection_name, points_selector):
        # Mirror the real client: a PointIdsList carries the ids under .points.
        ids = getattr(points_selector, "points", points_selector)
        for point_id in ids:
            self.points.pop(point_id, None)

    def scroll(self, collection_name, scroll_filter=None, limit=100, offset=None, **kwargs):
        matches = list(self.points.values())
        if scroll_filter is not None:
            for condition in scroll_filter.must:
                key = condition.key
                match = condition.match
                if hasattr(match, "any"):
                    wanted = set(match.any)
                    matches = [p for p in matches if p.payload.get(key) in wanted]
                else:
                    matches = [p for p in matches if p.payload.get(key) == match.value]
        return matches[:limit], None


def doc(content, source_id="page-1", title="Rollback"):
    return {"source_id": source_id, "title": title, "url": "https://wiki/x", "content": content}


def test_source_is_stored_verbatim():
    client = FakeClient()

    source_store.store("parent", [doc("<p>raw &amp; unconverted</p>")], "run-1", client=client)

    stored = next(iter(client.points.values())).payload
    assert stored["content"] == "<p>raw &amp; unconverted</p>"
    assert stored["module"] == source_store.MODULE
    assert stored["run_id"] == "run-1"


def test_unchanged_content_does_not_add_a_version():
    client = FakeClient()

    source_store.store("parent", [doc("same")], "run-1", client=client)
    source_store.store("parent", [doc("same")], "run-2", client=client)

    # Content-addressed: the same bytes are the same point, rewritten not appended.
    assert len(client.points) == 1


def test_same_day_edits_collapse_to_the_newest():
    # The deliberate trade: a day keeps one version, so a page whose macros re-render on
    # every sync cannot fill the window. Intermediate same-day edits are not recoverable.
    client = FakeClient()

    source_store.store("parent", [doc("first")], "run-1", client=client)
    source_store.store("parent", [doc("second")], "run-2", client=client)

    assert len(client.points) == 1
    assert next(iter(client.points.values())).payload["content"] == "second"


def test_edits_on_different_days_both_survive():
    client = FakeClient()

    source_store.store("parent", [doc("first")], "run-1", client=client)
    next(iter(client.points.values())).payload["fetched_at"] = "2026-09-01T10:00:00+00:00"
    source_store.store("parent", [doc("second")], "run-2", client=client)

    assert len(client.points) == 2


def test_prune_collapses_versions_from_the_same_day():
    client = FakeClient()
    # Twelve renders, all today — the macro-churn case.
    for index in range(12):
        source_store.store("parent", [doc(f"render {index}")], f"run-{index}", client=client)

    assert len(client.points) == 1


def test_prune_keeps_ten_days_of_real_edits():
    client = FakeClient()
    for day in range(14):
        source_store.store("parent", [doc(f"edit {day}")], f"run-{day}", client=client)
        # Re-date the just-written point so each edit looks like its own day.
        newest = max(client.points.values(), key=lambda p: p.payload["fetched_at"])
        newest.payload["fetched_at"] = f"2026-09-{day + 1:02d}T10:00:00+00:00"

    assert len(client.points) == source_store.MAX_VERSIONS


def test_collection_is_created_on_disk_and_classified_by_metadata():
    client = FakeClient(existing=False)

    source_store.store("parent", [doc("x")], "run-1", client=client)

    name, kwargs = client.created[0]
    assert name == "parent__source"
    assert kwargs["on_disk_payload"] is True
    # Classified by metadata, never by the name.
    assert kwargs["metadata"]["module"] == source_store.MODULE


def test_get_by_content_hash():
    client = FakeClient()
    source_store.store("parent", [doc("findable")], "run-1", client=client)

    found = source_store.get("parent", source_store.content_hash("findable"), client=client)

    assert found["content"] == "findable"


def test_documents_for_a_run():
    client = FakeClient()
    source_store.store("parent", [doc("a", source_id="p1"), doc("b", source_id="p2")], "run-1", client=client)
    source_store.store("parent", [doc("c", source_id="p3")], "run-2", client=client)

    listed = source_store.documents_for_run("parent", "run-2", client=client)

    assert [d["source_id"] for d in listed["documents"]] == ["p3"]


def test_drop_removes_the_sibling():
    client = FakeClient()
    source_store.store("parent", [doc("x")], "run-1", client=client)

    assert source_store.drop("parent", client=client) is True
    assert client.deleted_collections == ["parent__source"]


def test_read_failures_are_not_reported_as_empty():
    # An unreachable Qdrant must not be able to say "this load captured nothing", or
    # "there is no source" — the caller renders both as normal states.
    class Broken(FakeClient):
        def collection_exists(self, name):
            raise RuntimeError("qdrant down")

    with pytest.raises(RuntimeError):
        source_store.get("parent", "hash", client=Broken())
    with pytest.raises(RuntimeError):
        source_store.documents_for_run("parent", "run-1", client=Broken())


def test_store_failure_never_fails_the_ingest():
    class Broken(FakeClient):
        def upsert(self, *a, **k):
            raise RuntimeError("qdrant down")

    assert source_store.store("parent", [doc("x")], "run-1", client=Broken()) == 0


def test_upsert_is_split_by_count_and_by_bytes():
    """One request per sync would carry every page's verbatim HTML.

    Qdrant caps the request body well below that, and store() swallows
    failures, so an unsplit upsert loses a whole space's sources silently.
    """
    import itertools

    from qdrant_client.models import PointStruct

    from rag.core.documents import source_store

    ids = itertools.count(1)

    def point(content):
        return PointStruct(id=next(ids), vector=[0.0], payload={"content": content})

    many = [point("x") for _ in range(source_store._UPSERT_MAX_POINTS * 2 + 5)]
    by_count = list(source_store._upsert_batches(many))
    assert len(by_count) == 3
    assert [len(b) for b in by_count] == [
        source_store._UPSERT_MAX_POINTS,
        source_store._UPSERT_MAX_POINTS,
        5,
    ]
    assert sum(len(b) for b in by_count) == len(many)

    big = "y" * (source_store._UPSERT_MAX_BYTES // 2 + 1)
    heavy = [point(big) for _ in range(3)]
    by_bytes = list(source_store._upsert_batches(heavy))
    assert [len(b) for b in by_bytes] == [1, 1, 1]

    # Multi-byte text is measured in bytes, not characters: one CJK character is three
    # bytes, so a character-counted budget would let a batch run 3x over the wire limit.
    per_doc_chars = source_store._UPSERT_MAX_BYTES // 3
    cjk = [point("\u65e5" * per_doc_chars) for _ in range(3)]
    by_utf8 = list(source_store._upsert_batches(cjk))
    assert [len(b) for b in by_utf8] == [1, 1, 1]
    # The same character count in ASCII is a third of the bytes, so it still fits in one.
    ascii_same_chars = [point("a" * per_doc_chars) for _ in range(3)]
    assert len(list(source_store._upsert_batches(ascii_same_chars))) == 1

    # A single document larger than the cap still goes out rather than being dropped.
    oversized = [point("z" * (source_store._UPSERT_MAX_BYTES + 10))]
    assert [len(b) for b in source_store._upsert_batches(oversized)] == [1]

    assert list(source_store._upsert_batches([])) == []
