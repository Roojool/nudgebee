"""Re-embedding migration: what gets registered, and in what order it is rebuilt.

Two properties carry the weight here.

Registration must key on the model NAME, not the dimension. Different models
routinely share one -- bge-small-en-v1.5 and all-MiniLM-L6-v2 are both 384 -- so
a dimension check would call a swap between them "already correct" and leave
every vector silently wrong.

Rebuild ordering must stage before it deletes. Document text lives only in the
Qdrant payload, so dropping the original before the replacement holds the data
destroys it with nothing to restore from.
"""

import contextlib

import pytest
from qdrant_client.models import Distance, VectorParams

from rag.core.documents import embedding_migration as em


@pytest.fixture(autouse=True)
def _no_older_worker(monkeypatch):
    """No worker of the previous version is running unless a test says so."""

    @contextlib.contextmanager
    def _granted():
        yield lambda: True

    monkeypatch.setattr(em, "_shared_run_lock", _granted)


@pytest.fixture
def _holds_lock(monkeypatch):
    """Grant every lock. Tests of run() are about what gets rebuilt; the locking
    itself is exercised separately below."""

    @contextlib.contextmanager
    def _granted(*_a):
        yield lambda: True

    monkeypatch.setattr(em, "_exclusive_run", _granted)
    monkeypatch.setattr(em, "_collection_lock", _granted)


@pytest.fixture
def _single_run_lock(monkeypatch):
    """The migration-wide lock that per-collection locking replaced, still
    available behind RAG_EMBEDDING_MIGRATION_PER_COLLECTION_LOCK=false."""
    from utils.config import Config

    monkeypatch.setattr(Config, "embedding_migration_per_collection_lock", False)


def _staging_of(client, src):
    names = [n for n in client.colls if em._staging_source(n) == src]
    return client.colls[names[0]] if names else None


class _Coll:
    def __init__(self, dim, points, metadata=None):
        self.dim = dim
        self.points = points
        self.metadata = metadata or {}


class _FakeClient:
    """Minimal Qdrant stand-in that records the order of destructive calls."""

    def __init__(self, collections):
        self.colls = collections
        self.events = []

    def get_collections(self):
        return type("R", (), {"collections": [type("C", (), {"name": n})() for n in list(self.colls)]})()

    def get_collection(self, name):
        if name not in self.colls:
            raise KeyError(name)
        c = self.colls[name]
        params = VectorParams(size=c.dim, distance=Distance.COSINE)
        cfg = type("Cfg", (), {"params": type("P", (), {"vectors": params})(), "metadata": c.metadata})()
        return type("Info", (), {"config": cfg, "points_count": len(c.points)})()

    def create_collection(self, name, vectors_config, metadata=None):
        self.events.append(("create", name))
        self.colls[name] = _Coll(vectors_config.size, [], metadata or {})

    def delete_collection(self, name):
        self.events.append(("delete", name))
        self.colls.pop(name, None)

    def scroll(self, collection_name, limit, offset=None, with_payload=True, with_vectors=False):
        pts = self.colls[collection_name].points
        start = offset or 0
        window = pts[start : start + limit]
        return window, (start + limit if start + limit < len(pts) else None)

    def upsert(self, collection_name, points, wait=True):
        coll = self.colls[collection_name]
        incoming = {pt.id for pt in points}
        coll.points = [pt for pt in coll.points if pt.id not in incoming] + list(points)

    def retrieve(self, collection_name, ids, with_payload=False, with_vectors=False):
        wanted = set(ids)
        return [pt for pt in self.colls[collection_name].points if pt.id in wanted]

    def update_collection(self, collection_name, metadata=None):
        self.events.append(("update", collection_name))
        self.colls[collection_name].metadata.update(metadata or {})


class _Point:
    def __init__(self, pid, text, vector=None):
        self.id = pid
        self.payload = {"page_content": text}
        self.vector = vector or [0.0]


class _Embeddings:
    def __init__(self, dim, name):
        self.dim = dim
        self.model_id = name
        self.calls = 0

    def embed_query(self, _t):
        return [0.1] * self.dim

    def embed_documents(self, texts):
        self.calls += len(texts)
        return [[0.1] * self.dim for _ in texts]


def _stamped(dim, model, n=3):
    return _Coll(dim, [_Point(i, f"doc {i}") for i in range(n)], {em.MODEL_KEY: model})


def test_account_id_is_taken_from_the_collection_name():
    acct = "a2a30b02-0f67-42e5-a2ab-c658230fd798"
    assert em._account_of(f"{acct}_knowledge_base") == acct


@pytest.mark.parametrize("name", ["nudgebee_docs", "kubectl", "events", "kb_not-a-uuid"])
def test_global_collections_have_no_account(name):
    assert em._account_of(name) == ""


def test_same_model_is_not_registered():
    client = _FakeClient({"c": _stamped(384, "bge-small-en-v1.5")})
    assert em.register(client, lambda _a: _Embeddings(384, "bge-small-en-v1.5")) == []


def test_same_dimension_different_model_is_still_registered():
    """The reason the model name is stored at all: 384 != 384 is not enough."""
    client = _FakeClient({"c": _stamped(384, "all-MiniLM-L6-v2")})
    pending = em.register(client, lambda _a: _Embeddings(384, "bge-small-en-v1.5"))
    assert [p.name for p in pending] == ["c"]
    assert pending[0].stored_model == "all-MiniLM-L6-v2"


def test_unstamped_collection_at_the_expected_dimension_is_left_alone():
    """Predates the stamp; same dimension is the best evidence available, and
    rebuilding every legacy collection on a hunch costs hours."""
    client = _FakeClient({"c": _Coll(384, [_Point(1, "x")])})
    assert em.register(client, lambda _a: _Embeddings(384, "bge-small-en-v1.5")) == []


def test_unstamped_collection_at_a_different_dimension_is_registered():
    client = _FakeClient({"c": _Coll(3072, [_Point(1, "x")])})
    pending = em.register(client, lambda _a: _Embeddings(384, "bge-small-en-v1.5"))
    assert [p.name for p in pending] == ["c"]
    assert pending[0].stored_model == "unstamped"


def test_a_staging_collection_is_registered_under_its_source_name_not_its_own():
    """Staging is never a migration target in its own right. A lone one means
    the original was already dropped, so the source is registered for restore
    -- earlier this returned nothing and the data stayed orphaned."""
    client = _FakeClient({f"c{em.STAGING_SUFFIX}": _Coll(3072, [_Point(1, "x")])})
    pending = em.register(client, lambda _a: _Embeddings(384, "m"))

    assert [p.name for p in pending] == ["c"]
    assert all(not p.name.endswith(em.STAGING_SUFFIX) for p in pending)


def test_original_is_only_deleted_after_staging_holds_the_data():
    client = _FakeClient({"c": _Coll(3072, [_Point(i, f"doc {i}") for i in range(5)])})
    p = em.register(client, lambda _a: _Embeddings(384, "new"))[0]
    em.rebuild(client, p, lambda: _Embeddings(384, "new"))

    staged = next(i for i, (op, n) in enumerate(client.events) if op == "create" and em._staging_source(n) == "c")
    dropped = client.events.index(("delete", "c"))
    assert staged < dropped, "staging must exist before the original is dropped"


def test_rebuilt_collection_keeps_every_point_and_records_the_model():
    client = _FakeClient({"c": _Coll(3072, [_Point(i, f"doc {i}") for i in range(7)])})
    p = em.register(client, lambda _a: _Embeddings(384, "bge-small-en-v1.5"))[0]
    em.rebuild(client, p, lambda: _Embeddings(384, "bge-small-en-v1.5"))

    assert len(client.colls["c"].points) == 7
    assert client.colls["c"].dim == 384
    assert client.colls["c"].metadata[em.MODEL_KEY] == "bge-small-en-v1.5"
    assert _staging_of(client, "c") is None, "staging should be cleaned up"


def test_rebuild_keeps_the_metadata_that_decides_which_searches_reach_it():
    """module/account/tenant_id select which collections a search covers. A
    rebuild that recreated the collection without them would leave the data
    intact and unreachable, which is worse than losing it loudly."""
    original = {
        "module": "knowledge_base",
        "account": "a2a30b02-0f67-42e5-a2ab-c658230fd798",
        "tenant_id": "890cad87-c452-4aa7-b84a-742cee0454a1",
        "source": "confluence",
    }
    client = _FakeClient({"c": _Coll(3072, [_Point(1, "x")], dict(original))})
    p = em.register(client, lambda _a: _Embeddings(384, "bge-small-en-v1.5"))[0]
    em.rebuild(client, p, lambda: _Embeddings(384, "bge-small-en-v1.5"))

    after = client.colls["c"].metadata
    for k, v in original.items():
        assert after.get(k) == v, f"{k} was dropped by the rebuild"
    assert after[em.MODEL_KEY] == "bge-small-en-v1.5"


def test_rebuilt_collection_is_not_registered_again():
    client = _FakeClient({"c": _Coll(3072, [_Point(1, "x")])})
    embs = _Embeddings(384, "bge-small-en-v1.5")
    em.rebuild(client, em.register(client, lambda _a: embs)[0], lambda: embs)
    assert em.register(client, lambda _a: _Embeddings(384, "bge-small-en-v1.5")) == []


def test_interrupted_run_reuses_staging_instead_of_re_embedding():
    staged = [_Point(i, f"doc {i}", vector=[0.1] * 384) for i in range(4)]
    client = _FakeClient(
        {
            "c": _Coll(3072, [_Point(i, f"doc {i}") for i in range(4)]),
            f"c{em.STAGING_SUFFIX}": _Coll(384, staged, {em.MODEL_KEY: "new"}),
        }
    )
    p = em.register(client, lambda _a: _Embeddings(384, "new"))[0]
    emb = _Embeddings(384, "new")
    em.rebuild(client, p, lambda: emb)
    assert emb.calls == 0, "a complete staging collection must not be re-embedded"


def test_each_account_is_measured_against_its_own_model(_holds_lock):
    """An account with its own embeddings integration keeps its own model;
    rebuilding it with the deployment default would strand its queries."""
    a = "11111111-1111-1111-1111-111111111111"
    b = "22222222-2222-2222-2222-222222222222"
    client = _FakeClient(
        {
            f"{a}_knowledge_base": _stamped(3072, "gemini-embedding-001"),
            f"{b}_knowledge_base": _stamped(3072, "gemini-embedding-001"),
        }
    )
    models = {a: _Embeddings(384, "bge-small-en-v1.5"), b: _Embeddings(3072, "gemini-embedding-001")}
    summary = em.run(client, lambda acct: models[acct])
    assert summary["registered"] == 1, "only the account that changed model is rebuilt"
    assert summary["rebuilt"] == 1
    assert models[b].calls == 0, "the unchanged account pays nothing"


def test_one_failing_collection_does_not_strand_the_others(_holds_lock):
    class _Boom(_FakeClient):
        def scroll(self, collection_name, **kw):
            if collection_name == "bad":
                raise RuntimeError("qdrant is unhappy")
            return super().scroll(collection_name, **kw)

    client = _Boom({"bad": _Coll(3072, [_Point(1, "x")]), "good": _Coll(3072, [_Point(2, "y")])})
    summary = em.run(client, lambda _a: _Embeddings(384, "new"))
    assert summary["failed"] == 1
    assert summary["rebuilt"] == 1


def test_run_on_a_clean_store_does_nothing(_holds_lock):
    client = _FakeClient({"c": _stamped(384, "m")})
    assert em.run(client, lambda _a: _Embeddings(384, "m")) == {"registered": 0, "rebuilt": 0, "failed": 0}


def test_background_run_never_raises_into_the_caller():
    """A daemon thread that propagates would take nothing down but would leave
    an unhandled-exception traceback and no retry."""

    class _Broken(_FakeClient):
        def get_collections(self):
            raise RuntimeError("qdrant unreachable at startup")

    thread = em.start_background(_Broken({}), lambda _a: _Embeddings(384, "m"), retry_delay=0)
    thread.join(timeout=10)
    assert not thread.is_alive(), "the worker must exit rather than hang"


def test_migration_defaults_on_for_ondevice_and_off_for_a_chosen_provider(monkeypatch):
    """On-device is the default provider, so inheriting it means the stored
    vectors came from something else and only a rebuild restores retrieval.
    A provider someone chose was chosen against known data, so it stays opt-in.
    """
    from utils.config import Config

    monkeypatch.delenv("RAG_EMBEDDING_MIGRATION_ENABLED", raising=False)

    monkeypatch.setattr(Config, "embeddings_provider", "ondevice")
    assert em.is_enabled() is True

    # Secret values arrive base64-decoded and have carried trailing newlines
    # before; a stray one must not silently turn the rebuild off.
    monkeypatch.setattr(Config, "embeddings_provider", "ondevice\n")
    assert em.is_enabled() is True

    monkeypatch.setattr(Config, "embeddings_provider", "bedrock")
    assert em.is_enabled() is False


def test_explicit_migration_setting_overrides_the_provider_default(monkeypatch):
    from utils.config import Config

    monkeypatch.setattr(Config, "embeddings_provider", "ondevice")
    monkeypatch.setenv("RAG_EMBEDDING_MIGRATION_ENABLED", "false")
    assert em.is_enabled() is False

    monkeypatch.setattr(Config, "embeddings_provider", "bedrock")
    monkeypatch.setenv("RAG_EMBEDDING_MIGRATION_ENABLED", "true")
    assert em.is_enabled() is True


def test_a_second_replica_rebuilds_nothing_while_another_holds_the_lock(monkeypatch, _single_run_lock):
    """Replicas share one Qdrant. Two rebuilding the same collection would have
    one delete the original while the other is still reading it."""

    @contextlib.contextmanager
    def _denied():
        yield None

    monkeypatch.setattr(em, "_exclusive_run", _denied)
    client = _FakeClient({"c": _Coll(3072, [_Point(1, "x")])})
    summary = em.run(client, lambda _a: _Embeddings(384, "new"))

    assert summary["rebuilt"] == 0
    assert summary["skipped_locked"] == 1
    assert client.events == [], "a replica without the lock must not touch any collection"


def test_a_database_failure_is_raised_not_reported_as_a_healthy_skip():
    """ "Another replica has it" and "the database is down" must not look the
    same. Collapsing them lets a blip read as a successful skip, and with the
    provider already switched every search then returns nothing -- permanently,
    because nothing retries."""

    class _DeadEngine:
        def connect(self):
            raise RuntimeError("database unreachable")

    mods = __import__("sys").modules
    saved = mods.get("rag.core.utils.db_query")
    mods["rag.core.utils.db_query"] = type("m", (), {"engine": _DeadEngine()})
    try:
        client = _FakeClient({"c": _Coll(3072, [_Point(1, "x")])})
        with pytest.raises(em.MigrationUnavailable):
            em.run(client, lambda _a: _Embeddings(384, "new"))
        assert client.events == [], "an unguarded run must not start"
    finally:
        if saved is not None:
            mods["rag.core.utils.db_query"] = saved
        else:
            mods.pop("rag.core.utils.db_query", None)


def test_a_short_embedding_batch_stops_the_collection_instead_of_dropping_points(_holds_lock):
    """zip() would pair payloads with the wrong vectors or silently drop them,
    and the original is deleted once staging looks complete."""

    class _ShortEmbeddings(_Embeddings):
        def embed_documents(self, texts):
            return [[0.1] * self.dim for _ in texts][:-1]  # one short

    client = _FakeClient({"c": _Coll(3072, [_Point(i, f"doc {i}") for i in range(3)])})
    summary = em.run(client, lambda _a: _ShortEmbeddings(384, "new"))

    assert summary["rebuilt"] == 0
    assert summary["failed"] == 1
    assert "c" in client.colls, "the original must survive a failed rebuild"
    assert len(client.colls["c"].points) == 3


def test_losing_the_lock_mid_run_stops_before_the_next_collection(monkeypatch, _single_run_lock):
    """A dropped connection releases the advisory lock, so another replica may
    already be working. Continuing would be the very race the lock prevents."""
    done = {"n": 0}
    real_rebuild = em.rebuild

    def counting_rebuild(client, p, load_embeddings, still_held=lambda: True):
        result = real_rebuild(client, p, load_embeddings, still_held)
        done["n"] += 1
        return result

    monkeypatch.setattr(em, "rebuild", counting_rebuild)

    @contextlib.contextmanager
    def _lock_dies_after_one_collection():
        yield lambda: done["n"] < 1

    monkeypatch.setattr(em, "_exclusive_run", _lock_dies_after_one_collection)
    client = _FakeClient(
        {
            "a": _Coll(3072, [_Point(1, "x")]),
            "b": _Coll(3072, [_Point(2, "y")]),
        }
    )
    summary = em.run(client, lambda _a: _Embeddings(384, "new"))

    assert summary["rebuilt"] == 1
    assert summary["stopped_lock_lost"] == 1
    assert client.colls["b"].dim == 3072, "the second collection must be left untouched"


def test_an_error_inside_the_lock_is_not_masked_by_the_context_manager():
    """Handling acquisition around the yield would catch exceptions thrown back
    in from the body and yield twice, raising "generator didn't stop after
    throw()" and hiding the real failure."""

    class _Result:
        def scalar(self):
            return True

    class _Conn:
        def execution_options(self, **_kw):
            return self

        def execute(self, *_a, **_kw):
            return _Result()

        def close(self):
            pass

    mods = __import__("sys").modules
    saved = mods.get("rag.core.utils.db_query")
    mods["rag.core.utils.db_query"] = type("m", (), {"engine": type("e", (), {"connect": lambda self: _Conn()})()})
    try:
        with pytest.raises(ValueError, match="boom"):
            with em._exclusive_run() as held:
                assert held is not None
                raise ValueError("boom")
    finally:
        if saved is not None:
            mods["rag.core.utils.db_query"] = saved
        else:
            mods.pop("rag.core.utils.db_query", None)


def test_a_crash_during_the_final_copy_is_picked_up_again(_holds_lock):
    """The dangerous window: the rebuilt collection exists and is already
    stamped with the new model, but holds only some of the points. Trusting the
    stamp would skip it forever and the missing points are unrecoverable."""
    client = _FakeClient(
        {
            # what a crash mid-copy leaves behind
            "c": _Coll(384, [_Point(1, "x")], {em.MODEL_KEY: "bge-small-en-v1.5"}),
            f"c{em.STAGING_SUFFIX}": _Coll(384, [_Point(i, f"doc {i}") for i in range(4)]),
        }
    )
    pending = em.register(client, lambda _a: _Embeddings(384, "bge-small-en-v1.5"))

    assert [p.name for p in pending] == ["c"], "leftover staging must force a rebuild"


def test_an_account_whose_provider_is_down_is_asked_once_not_once_per_collection(_holds_lock):
    acct = "33333333-3333-3333-3333-333333333333"
    client = _FakeClient({f"{acct}_{m}": _Coll(3072, [_Point(1, "x")]) for m in ("a", "b", "c")})
    calls = {"n": 0}

    def _down(_account):
        calls["n"] += 1
        raise RuntimeError("provider unreachable")

    assert em.register(client, _down) == []
    assert calls["n"] == 1, "a dead provider must be asked once per account, not once per collection"


@pytest.mark.parametrize("payload", [None, "not a dict", 42, []])
def test_a_payload_of_an_unexpected_shape_yields_no_text_rather_than_raising(payload):
    """Payloads written by older ingests are read here; a surprise should skip
    the point, not abort the rebuild."""
    assert em._text_of(payload) == ""


def test_non_string_page_content_is_coerced_rather_than_crashing():
    assert em._text_of({"page_content": 12345}) == "12345"


def test_staging_whose_original_was_already_deleted_is_restored(_holds_lock):
    """The narrowest and worst window: the run died between dropping the
    original and recreating it, so staging holds the only copy. Iterating
    active collections alone would never look at it and the data is gone."""
    client = _FakeClient(
        {
            f"c{em.STAGING_SUFFIX}": _Coll(
                384,
                [_Point(i, f"doc {i}", vector=[0.1] * 384) for i in range(4)],
                {"module": "knowledge_base", em.MODEL_KEY: "bge-small-en-v1.5"},
            )
        }
    )
    pending = em.register(client, lambda _a: _Embeddings(384, "bge-small-en-v1.5"))
    assert [p.name for p in pending] == ["c"], "an orphaned staging must be registered for restore"

    em.rebuild(client, pending[0], lambda: _Embeddings(384, "bge-small-en-v1.5"))

    assert "c" in client.colls, "the collection must be restored"
    assert len(client.colls["c"].points) == 4
    assert client.colls["c"].metadata["module"] == "knowledge_base"
    assert _staging_of(client, "c") is None


def test_restoring_an_orphan_does_not_re_embed(_holds_lock):
    """Staging already holds finished vectors; paying for them twice would turn
    a crash into hours of extra work."""
    client = _FakeClient(
        {
            f"c{em.STAGING_SUFFIX}": _Coll(
                384, [_Point(i, f"doc {i}", vector=[0.1] * 384) for i in range(3)], {em.MODEL_KEY: "m"}
            )
        }
    )
    p = em.register(client, lambda _a: _Embeddings(384, "m"))[0]
    emb = _Embeddings(384, "m")
    em.rebuild(client, p, lambda: emb)
    assert emb.calls == 0


def test_losing_the_lock_mid_rebuild_stops_before_destroying_the_original():
    """Checking only between collections leaves the worst window open: one
    collection can take hours, and the swap deletes the original."""
    client = _FakeClient({"c": _Coll(3072, [_Point(i, f"doc {i}") for i in range(3)])})
    p = em.register(client, lambda _a: _Embeddings(384, "new"))[0]

    with pytest.raises(RuntimeError, match="migration lock lost"):
        em.rebuild(client, p, lambda: _Embeddings(384, "new"), still_held=lambda: False)

    assert "c" in client.colls, "the original must survive"
    assert client.colls["c"].dim == 3072
    assert len(client.colls["c"].points) == 3


def test_the_lock_is_rechecked_immediately_before_the_swap():
    """Staging can take hours to fill. The check that matters is the one taken
    just before the original is deleted, not the one taken before it started."""
    client = _FakeClient({"c": _Coll(3072, [_Point(i, f"doc {i}") for i in range(3)])})
    p = em.register(client, lambda _a: _Embeddings(384, "new"))[0]

    def lock_dies_once_staging_is_full():
        staged = _staging_of(client, "c")
        return not (staged and len(staged.points) >= 3)

    with pytest.raises(RuntimeError, match="migration lock lost"):
        em.rebuild(client, p, lambda: _Embeddings(384, "new"), still_held=lock_dies_once_staging_is_full)

    assert "c" in client.colls, "the original must not be deleted after the lock is lost"
    assert client.colls["c"].dim == 3072
    assert len(client.colls["c"].points) == 3


def test_losing_the_lock_on_the_last_collection_is_reported_as_a_stop_not_a_failure(monkeypatch, _single_run_lock):
    """The pre-loop check only runs before the *next* collection, so a lock
    lost on the final one would otherwise be recorded as a failed rebuild with
    a misleading traceback, and the stop would never be reported at all."""
    lost = {"yes": False}

    @contextlib.contextmanager
    def _lock_dies_during_the_only_collection():
        def held():
            staged = _staging_of(client, "c")
            if staged and len(staged.points) >= 2:
                lost["yes"] = True
            return not lost["yes"]

        yield held

    monkeypatch.setattr(em, "_exclusive_run", _lock_dies_during_the_only_collection)
    client = _FakeClient({"c": _Coll(3072, [_Point(i, f"doc {i}") for i in range(2)])})
    summary = em.run(client, lambda _a: _Embeddings(384, "new"))

    assert summary["stopped_lock_lost"] == 1
    assert summary["failed"] == 0, "a deliberate stop must not be counted as a failed collection"
    assert summary["rebuilt"] == 0
    assert "c" in client.colls, "the original must survive"


def test_a_reconnected_session_is_treated_as_having_lost_the_lock(monkeypatch):
    """A pool that transparently reconnects answers queries perfectly well
    while holding no advisory lock. A liveness ping reads that as success; only
    asking Postgres who holds the lock catches it."""
    state = {"held": True}

    class _Result:
        def __init__(self, v):
            self.v = v

        def scalar(self):
            return self.v

    class _Conn:
        def execution_options(self, **_kw):
            return self

        def execute(self, stmt, params=None):
            sql = str(stmt)
            if "pg_try_advisory_lock" in sql:
                return _Result(True)
            if "pg_locks" in sql:
                return _Result(state["held"])
            return _Result(None)

        def close(self):
            pass

    monkeypatch.setitem(
        __import__("sys").modules,
        "rag.core.utils.db_query",
        type("m", (), {"engine": type("e", (), {"connect": lambda self: _Conn()})()}),
    )

    with em._exclusive_run() as still_held:
        assert still_held is not None and still_held(), "the lock starts out held"
        state["held"] = False  # the pool silently reconnected
        assert not still_held(), "a session that no longer holds the lock must report lost"


def test_restoring_an_orphan_never_touches_the_embedding_provider(_holds_lock):
    """Staging already holds finished vectors. Recovery is exactly when the
    provider is most likely to be the thing that is broken, so asking it for a
    model that will not be used would fail the restore for no reason."""
    client = _FakeClient(
        {
            f"c{em.STAGING_SUFFIX}": _Coll(
                384, [_Point(i, f"doc {i}", vector=[0.1] * 384) for i in range(3)], {em.MODEL_KEY: "m"}
            )
        }
    )
    p = em.register(client, lambda _a: _Embeddings(384, "m"))[0]

    def _provider_is_down():
        raise RuntimeError("provider unreachable")

    em.rebuild(client, p, _provider_is_down)

    assert len(client.colls["c"].points) == 3, "the restore must complete without the provider"


def test_resuming_complete_staging_never_touches_the_embedding_provider(_holds_lock):
    client = _FakeClient(
        {
            "c": _Coll(3072, [_Point(i, f"doc {i}") for i in range(2)]),
            f"c{em.STAGING_SUFFIX}": _Coll(
                384, [_Point(i, f"doc {i}", vector=[0.1] * 384) for i in range(2)], {em.MODEL_KEY: "new"}
            ),
        }
    )
    p = em.register(client, lambda _a: _Embeddings(384, "new"))[0]

    def _provider_is_down():
        raise RuntimeError("provider unreachable")

    em.rebuild(client, p, _provider_is_down)

    assert len(client.colls["c"].points) == 2


def test_the_background_worker_retries_a_transient_failure(monkeypatch):
    """Running once means a rolling update that briefly takes Postgres or
    Qdrant away abandons the rebuild for the life of the pod."""
    attempts = {"n": 0}

    def flaky(_client, _embeddings_for):
        attempts["n"] += 1
        if attempts["n"] < 3:
            raise RuntimeError("qdrant unreachable")
        return {"registered": 0, "rebuilt": 0, "failed": 0}

    monkeypatch.setattr(em, "run", flaky)
    em.start_background(_FakeClient({}), lambda _a: None, retry_delay=0).join(timeout=5)

    assert attempts["n"] == 3, "it must keep trying until a run succeeds"


def test_the_background_worker_gives_up_rather_than_retrying_forever(monkeypatch):
    attempts = {"n": 0}

    def always_broken(_client, _embeddings_for):
        attempts["n"] += 1
        raise RuntimeError("still unreachable")

    monkeypatch.setattr(em, "run", always_broken)
    thread = em.start_background(_FakeClient({}), lambda _a: None, retry_delay=0)
    thread.join(timeout=5)

    assert not thread.is_alive()
    assert attempts["n"] == em.RETRY_ATTEMPTS


def test_a_run_skipped_because_another_replica_holds_the_lock_is_not_retried(monkeypatch):
    """That is a healthy outcome — the other replica is doing the work."""
    attempts = {"n": 0}

    def skipped(_client, _embeddings_for):
        attempts["n"] += 1
        return {"registered": 0, "rebuilt": 0, "failed": 0, "skipped_locked": 1}

    monkeypatch.setattr(em, "run", skipped)
    em.start_background(_FakeClient({}), lambda _a: None, retry_delay=0).join(timeout=5)

    assert attempts["n"] == 1


def test_a_failed_collection_is_retried_rather_than_left_until_the_next_restart(monkeypatch):
    """A run that finishes with failures is not a finished run: those
    collections are unsearchable, and waiting for a restart is not a plan."""
    calls = {"n": 0}

    def one_failure_then_clean(_client, _embeddings_for):
        calls["n"] += 1
        if calls["n"] == 1:
            return {"registered": 1, "rebuilt": 0, "failed": 1}
        return {"registered": 0, "rebuilt": 0, "failed": 0}

    monkeypatch.setattr(em, "run", one_failure_then_clean)
    em.start_background(_FakeClient({}), lambda _a: None, retry_delay=0).join(timeout=5)

    assert calls["n"] == 2, "a run reporting failures must be retried"


def test_a_staging_cleanup_failure_does_not_undo_a_completed_rebuild(_holds_lock):
    """By this point the collection is rebuilt and correct. Failing the whole
    migration over a leftover temporary collection would retry finished work."""

    class _CleanupFails(_FakeClient):
        def delete_collection(self, name):
            if em._staging_source(name) and name in self.colls and self.colls[name].points:
                raise RuntimeError("qdrant timeout on cleanup")
            return super().delete_collection(name)

    client = _CleanupFails({"c": _Coll(3072, [_Point(i, f"doc {i}") for i in range(3)])})
    p = em.register(client, lambda _a: _Embeddings(384, "new"))[0]

    em.rebuild(client, p, lambda: _Embeddings(384, "new"))

    assert client.colls["c"].dim == 384, "the rebuild must stand"
    assert len(client.colls["c"].points) == 3


# --- per-collection locking: several workers share one store ---


def test_a_collection_another_worker_holds_is_skipped_and_the_rest_are_rebuilt(monkeypatch):
    """Locks are per collection, so a busy collection costs the next worker
    nothing but that collection."""

    @contextlib.contextmanager
    def _one_is_taken(name):
        yield None if name == "a" else (lambda: True)

    monkeypatch.setattr(em, "_collection_lock", _one_is_taken)
    client = _FakeClient({"a": _Coll(3072, [_Point(1, "x")]), "b": _Coll(3072, [_Point(2, "y")])})
    summary = em.run(client, lambda _a: _Embeddings(384, "new"))

    assert summary["skipped_locked"] == 1
    assert summary["rebuilt"] == 1
    assert client.colls["a"].dim == 3072, "a collection another worker holds must not be touched"
    assert client.colls["b"].dim == 384


def test_a_collection_finished_by_another_worker_after_registration_is_left_alone():
    """Registration is a snapshot. Rebuilding a collection someone else has
    already finished would re-embed it for nothing, and swap it while that
    worker's searches are using it."""
    client = _FakeClient({"c": _Coll(3072, [_Point(1, "x")])})
    p = em.register(client, lambda _a: _Embeddings(384, "new"))[0]
    client.colls["c"] = _stamped(384, "new", n=1)
    client.events.clear()
    emb = _Embeddings(384, "new")

    assert em.rebuild(client, p, lambda: emb) == "already"
    assert client.events == [] and emb.calls == 0


def test_a_lock_lost_mid_rebuild_stops_that_collection_and_the_next_still_runs(monkeypatch):
    """A dropped connection releases one collection's lock. That collection
    stops before its original is touched; the others have locks of their own."""

    @contextlib.contextmanager
    def _lost_for_a(name):
        yield (lambda: False) if name == "a" else (lambda: True)

    monkeypatch.setattr(em, "_collection_lock", _lost_for_a)
    client = _FakeClient({"a": _Coll(3072, [_Point(1, "x")]), "b": _Coll(3072, [_Point(2, "y")])})
    summary = em.run(client, lambda _a: _Embeddings(384, "new"))

    assert summary["stopped_lock_lost"] == 1
    assert summary["failed"] == 0
    assert summary["rebuilt"] == 1
    assert client.colls["a"].dim == 3072 and len(client.colls["a"].points) == 1


def test_each_worker_stages_into_its_own_copy():
    client = _FakeClient({"c": _Coll(3072, [_Point(1, "x")])})
    p = em.register(client, lambda _a: _Embeddings(384, "new"))[0]
    em.rebuild(client, p, lambda: _Embeddings(384, "new"), worker="w1")

    created = [n for op, n in client.events if op == "create" and em._staging_source(n) == "c"]
    assert created == [f"c{em.STAGING_SUFFIX}__w1"]


def test_a_write_landing_during_the_rebuild_survives_the_swap(_holds_lock):
    """Writes keep going to the original while it is re-embedded. Without the
    catch-up pass they are dropped with the original at the swap."""

    class _WriteDuringRebuild(_FakeClient):
        written = False

        def upsert(self, collection_name, points, wait=True):
            super().upsert(collection_name, points, wait)
            if em._staging_source(collection_name) == "c" and not self.written:
                self.written = True
                self.colls["c"].points.append(_Point(99, "written mid-rebuild"))

    client = _WriteDuringRebuild({"c": _Coll(3072, [_Point(i, f"doc {i}") for i in range(3)])})
    em.run(client, lambda _a: _Embeddings(384, "new"))

    assert 99 in {pt.id for pt in client.colls["c"].points}
    assert len(client.colls["c"].points) == 4


def test_a_staging_copy_is_marked_complete_before_the_original_is_dropped():
    client = _FakeClient({"c": _Coll(3072, [_Point(i, f"doc {i}") for i in range(2)])})
    p = em.register(client, lambda _a: _Embeddings(384, "new"))[0]
    em.rebuild(client, p, lambda: _Embeddings(384, "new"))

    marked = next(i for i, (op, n) in enumerate(client.events) if op == "update" and em._staging_source(n) == "c")
    assert marked < client.events.index(("delete", "c"))


def test_an_orphan_is_never_restored_from_a_copy_not_known_complete(_holds_lock):
    """A worker that lost its lock can leave a partial copy of its own. Once
    the original is gone, restoring from that copy would make the loss
    permanent; only a copy marked complete may stand in for it."""
    partial = f"c{em.STAGING_SUFFIX}__stale"
    client = _FakeClient({partial: _Coll(384, [_Point(1, "x", vector=[0.1] * 384)], {em.MODEL_KEY: "m"})})

    assert em.register(client, lambda _a: _Embeddings(384, "m")) == []
    assert partial in client.colls, "the partial copy is all that is left; it must not be dropped"


def test_an_orphan_is_restored_from_a_copy_marked_complete(_holds_lock):
    done = f"c{em.STAGING_SUFFIX}__w1"
    client = _FakeClient(
        {
            done: _Coll(
                384,
                [_Point(i, f"doc {i}", vector=[0.1] * 384) for i in range(3)],
                {em.MODEL_KEY: "m", em.COMPLETE_KEY: 3},
            ),
            f"c{em.STAGING_SUFFIX}__stale": _Coll(384, [_Point(0, "doc 0", vector=[0.1] * 384)], {em.MODEL_KEY: "m"}),
        }
    )
    summary = em.run(client, lambda _a: _Embeddings(384, "m"))

    assert summary["rebuilt"] == 1
    assert len(client.colls["c"].points) == 3
    assert em.COMPLETE_KEY not in client.colls["c"].metadata
    assert _staging_of(client, "c") is None, "every copy is cleaned up once the collection is whole"


def test_shared_collections_are_rebuilt_before_per_account_ones_then_smallest_first():
    acct = "44444444-4444-4444-4444-444444444444"
    client = _FakeClient(
        {
            f"{acct}_big": _Coll(3072, [_Point(i, "x") for i in range(5)]),
            f"{acct}_small": _Coll(3072, [_Point(1, "x")]),
            "nudgebee_docs": _Coll(3072, [_Point(i, "x") for i in range(9)]),
        }
    )
    pending = em.register(client, lambda _a: _Embeddings(384, "new"))
    assert [p.name for p in pending] == ["nudgebee_docs", f"{acct}_small", f"{acct}_big"]


def test_collection_lock_keys_are_stable_and_distinct():
    """Every worker must ask Postgres for the same key for the same collection;
    Python's own str hash is salted per process and would not be."""
    assert em._collection_lock_key("nudgebee_docs") == em._collection_lock_key("nudgebee_docs")
    assert em._collection_lock_key("a") != em._collection_lock_key("b")
    assert em._collection_lock_key("a") != em.LOCK_KEY
    assert 0 < em._collection_lock_key("a") < 2**63


def test_a_narrowed_run_does_not_look_up_collections_outside_it(_holds_lock):
    """A worker pointed at a few collections must not resolve every account's
    model across the whole store before starting."""
    acct = "55555555-5555-5555-5555-555555555555"
    client = _FakeClient({"wanted": _Coll(3072, [_Point(1, "x")]), f"{acct}_other": _Coll(3072, [_Point(2, "y")])})
    asked = []

    def embeddings_for(account):
        asked.append(account)
        return _Embeddings(384, "new")

    summary = em.run(client, embeddings_for, lambda name: name == "wanted")

    assert summary["rebuilt"] == 1
    assert acct not in asked
    assert client.colls[f"{acct}_other"].dim == 3072


def test_writes_that_keep_arriving_right_up_to_the_swap_all_survive(_holds_lock):
    """One catch-up pass leaves a gap between its end and the delete; in a
    live run against dev three of forty writes fell into it."""

    class _Busy(_FakeClient):
        writes = 0

        def get_collection(self, name):
            if name == "c" and self.writes < 6 and any(em._staging_source(n) == "c" for n in self.colls):
                self.writes += 1
                self.colls["c"].points.append(_Point(1000 + self.writes, f"late write {self.writes}"))
            return super().get_collection(name)

    client = _Busy({"c": _Coll(3072, [_Point(i, f"doc {i}") for i in range(3)])})
    em.run(client, lambda _a: _Embeddings(384, "new"))

    assert len(client.colls["c"].points) == 3 + client.writes
    assert client.writes == 6


def test_repeated_text_in_a_collection_is_embedded_once():
    """Prod recommendation collections hold about five copies of each text;
    paying for every copy is most of the cost of rebuilding them."""
    pts = [_Point(i, f"doc {i % 3}") for i in range(12)]
    client = _FakeClient({"c": _Coll(3072, pts)})
    p = em.register(client, lambda _a: _Embeddings(384, "new"))[0]
    emb = _Embeddings(384, "new")
    em.rebuild(client, p, lambda: emb)

    assert emb.calls == 3
    assert len(client.colls["c"].points) == 12, "every point keeps its own entry"


def test_a_complete_copy_wins_over_a_stale_partial_one_after_a_crash_mid_copy_back(_holds_lock):
    """A crash during copy-back leaves the original half restored, so its count
    proves nothing; a stale worker's partial copy must not stand in for the
    complete one, which would then be deleted as a leftover."""
    client = _FakeClient(
        {
            "c": _Coll(384, [_Point(i, f"doc {i}", vector=[0.1] * 384) for i in range(2)], {em.MODEL_KEY: "new"}),
            f"c{em.STAGING_SUFFIX}__zombie": _Coll(
                384, [_Point(i, f"doc {i}", vector=[0.1] * 384) for i in range(3)], {em.MODEL_KEY: "new"}
            ),
            f"c{em.STAGING_SUFFIX}__a": _Coll(
                384,
                [_Point(i, f"doc {i}", vector=[0.1] * 384) for i in range(6)],
                {em.MODEL_KEY: "new", em.COMPLETE_KEY: 6},
            ),
        }
    )
    em.run(client, lambda _a: _Embeddings(384, "new"))

    assert len(client.colls["c"].points) == 6
    assert _staging_of(client, "c") is None


def test_a_half_restored_collection_from_the_previous_version_is_finished_from_its_copy(_holds_lock):
    """The previous version names its copy without a worker suffix and never
    marks it, but only dropped the original once that copy was full."""
    client = _FakeClient(
        {
            "c": _Coll(384, [_Point(1, "doc 1", vector=[0.1] * 384)], {em.MODEL_KEY: "new"}),
            f"c{em.STAGING_SUFFIX}": _Coll(
                384, [_Point(i, f"doc {i}", vector=[0.1] * 384) for i in range(4)], {em.MODEL_KEY: "new"}
            ),
        }
    )
    emb = _Embeddings(384, "new")
    p = em.register(client, lambda _a: emb)[0]
    em.rebuild(client, p, lambda: emb)

    assert len(client.colls["c"].points) == 4
    assert emb.calls == 0


def test_a_half_restored_collection_with_no_known_complete_copy_is_left_alone(_holds_lock):
    """Rebuilding from the half-restored original would make the loss permanent;
    better to stop and leave every copy for a person to look at."""
    partial = f"c{em.STAGING_SUFFIX}__zombie"
    client = _FakeClient(
        {
            "c": _Coll(384, [_Point(1, "doc 1", vector=[0.1] * 384)], {em.MODEL_KEY: "new"}),
            partial: _Coll(384, [_Point(i, f"doc {i}", vector=[0.1] * 384) for i in range(2)]),
        }
    )
    summary = em.run(client, lambda _a: _Embeddings(384, "new"))

    assert summary["failed"] == 1
    assert partial in client.colls and "c" in client.colls


def test_nothing_runs_while_a_worker_of_the_previous_version_holds_the_migration_lock(monkeypatch):
    """The previous version takes the migration-wide lock exclusively and knows
    nothing of per-collection locks; during a rollout both may be alive."""

    @contextlib.contextmanager
    def _held_by_older_worker():
        yield None

    monkeypatch.setattr(em, "_shared_run_lock", _held_by_older_worker)
    client = _FakeClient({"c": _Coll(3072, [_Point(1, "x")])})

    with pytest.raises(RuntimeError, match="older migration worker"):
        em.run(client, lambda _a: _Embeddings(384, "new"))
    assert client.events == []


def test_the_text_cache_is_bounded(monkeypatch):
    monkeypatch.setattr(em, "EMBED_CACHE_MAX", 4)
    cache = {}
    emb = _Embeddings(384, "new")
    for i in range(5):
        em._embed_unique(emb, [f"t{i}-{j}" for j in range(3)], cache, "c")
    assert len(cache) <= 4
    assert "t4-2" in cache, "the most recent text stays cached"


def test_writes_that_never_pause_do_not_hold_the_swap_off_forever(_holds_lock, monkeypatch):
    monkeypatch.setattr(em, "DRAIN_MAX_PASSES", 3)

    class _Firehose(_FakeClient):
        n = 0

        def get_collection(self, name):
            if name == "c" and any(em._staging_source(k) == "c" for k in self.colls):
                self.n += 1
                self.colls["c"].points.append(_Point(5000 + self.n, f"write {self.n}"))
            return super().get_collection(name)

    client = _Firehose({"c": _Coll(3072, [_Point(1, "x")])})
    summary = em.run(client, lambda _a: _Embeddings(384, "new"))

    assert summary["rebuilt"] == 1
    assert client.colls["c"].dim == 384


def test_the_staging_name_follows_the_process_that_builds_it(monkeypatch):
    """A pre-forking server imports the module once and forks; a worker id
    fixed at import would give every child the parent's pid and one shared
    staging name."""
    monkeypatch.setattr(em.os, "getpid", lambda: 4242)
    client = _FakeClient({"c": _Coll(3072, [_Point(1, "x")])})
    p = em.register(client, lambda _a: _Embeddings(384, "new"))[0]
    em.rebuild(client, p, lambda: _Embeddings(384, "new"))

    created = [n for op, n in client.events if op == "create" and em._staging_source(n) == "c"]
    assert len(created) == 1 and created[0].endswith("-4242")


def test_catch_up_passes_read_ids_not_payloads(_holds_lock):
    """Nearly every point is already staged by then; reading every payload of
    a large collection on each drain pass is the expensive part."""

    class _Recording(_FakeClient):
        def scroll(self, collection_name, limit, offset=None, with_payload=True, with_vectors=False):
            self.events.append(("scroll", collection_name, with_payload))
            return super().scroll(collection_name, limit, offset, with_payload, with_vectors)

    client = _Recording({"c": _Coll(3072, [_Point(i, f"doc {i}") for i in range(5)])})
    em.run(client, lambda _a: _Embeddings(384, "new"))

    marked = next(i for i, e in enumerate(client.events) if e[0] == "update")
    after = [e for e in client.events[marked:] if e[0] == "scroll" and e[1] == "c"]
    assert after, "the drain runs after the copy is marked complete"
    assert all(e[2] is False for e in after)


def test_a_complete_copy_built_for_another_model_is_not_reused(_holds_lock):
    """A crashed run toward another model (the model switched again, or an
    account's embeddings integration changed) can leave a complete copy behind.
    Swapping it in would stamp old vectors with the new model: wrong vectors,
    no error."""
    stale = f"c{em.STAGING_SUFFIX}__old"
    client = _FakeClient(
        {
            "c": _Coll(3072, [_Point(i, f"doc {i}") for i in range(3)]),
            stale: _Coll(
                384,
                [_Point(i, f"doc {i}", vector=[0.9] * 384) for i in range(3)],
                {em.MODEL_KEY: "older", em.COMPLETE_KEY: 3},
            ),
        }
    )
    emb = _Embeddings(384, "new")
    summary = em.run(client, lambda _a: emb)

    assert summary["rebuilt"] == 1
    assert emb.calls == 3, "the collection is re-embedded with this model, not copied from the stale one"
    assert all(pt.vector == [0.1] * 384 for pt in client.colls["c"].points)
    assert _staging_of(client, "c") is None


def test_a_complete_copy_at_another_dimension_is_not_reused(_holds_lock):
    client = _FakeClient(
        {
            "c": _Coll(3072, [_Point(1, "x")]),
            f"c{em.STAGING_SUFFIX}__old": _Coll(
                768, [_Point(1, "x", vector=[0.9] * 768)], {em.MODEL_KEY: "new", em.COMPLETE_KEY: 1}
            ),
        }
    )
    em.run(client, lambda _a: _Embeddings(384, "new"))
    assert client.colls["c"].dim == 384 and len(client.colls["c"].points) == 1


def test_losing_the_shared_lock_stops_the_run(monkeypatch):
    """If the shared migration lock's connection drops, a worker of the previous
    version can take it exclusively and rebuild with no per-collection locks."""

    @contextlib.contextmanager
    def _shared_then_lost():
        yield lambda: False

    monkeypatch.setattr(em, "_shared_run_lock", _shared_then_lost)

    @contextlib.contextmanager
    def _granted(_name):
        yield lambda: True

    monkeypatch.setattr(em, "_collection_lock", _granted)
    client = _FakeClient({"c": _Coll(3072, [_Point(1, "x")])})
    summary = em.run(client, lambda _a: _Embeddings(384, "new"))

    assert summary["rebuilt"] == 0
    assert summary["stopped_lock_lost"] == 1
    assert client.colls["c"].dim == 3072


def test_the_shared_lock_is_checked_with_every_collection_lock_check(monkeypatch):
    checks = {"shared": 0}

    @contextlib.contextmanager
    def _counting_shared():
        def held():
            checks["shared"] += 1
            return True

        yield held

    monkeypatch.setattr(em, "_shared_run_lock", _counting_shared)

    @contextlib.contextmanager
    def _granted(_name):
        yield lambda: True

    monkeypatch.setattr(em, "_collection_lock", _granted)
    em.run(_FakeClient({"c": _Coll(3072, [_Point(i, "x") for i in range(3)])}), lambda _a: _Embeddings(384, "new"))
    assert checks["shared"] > 2, "checked before the collection and at every lock check inside the rebuild"
