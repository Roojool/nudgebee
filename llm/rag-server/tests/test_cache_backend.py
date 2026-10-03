"""Backend selection, fallback, and the MetadataCache port.

The behaviour that matters here is the one llm-server does NOT have: when
Redis is unreachable or starts failing, reads and writes must keep working
against process memory rather than becoming permanent misses.
"""

import time
from types import SimpleNamespace
from unittest.mock import MagicMock, patch

import pytest

from rag.core import cache_backend as cb
from utils.config import Config


@pytest.fixture(autouse=True)
def _fresh_backend():
    cb.reset_cache_backend()
    yield
    cb.reset_cache_backend()


def _redis_mock(value=None, ttl=-1, fail=None):
    """A redis client stub shaped like the one RedisBackend actually drives.

    Reads go through a pipeline (GET + TTL in one round trip), so stubbing
    client.get alone silently does nothing.
    """
    client = MagicMock()
    client.ping.return_value = True
    pipe = MagicMock()
    client.pipeline.return_value = pipe
    if fail is not None:
        pipe.execute.side_effect = fail
    else:
        pipe.execute.return_value = [value, ttl]
    return client, pipe


# --- InMemoryBackend ---------------------------------------------------------


def test_in_memory_round_trip_and_delete():
    b = cb.InMemoryBackend()
    assert b.get("missing") is None
    assert b.set("k", {"a": 1})
    assert b.get("k") == {"a": 1}
    assert b.delete("k")
    assert b.get("k") is None


def test_in_memory_entry_expires():
    b = cb.InMemoryBackend()
    b.set("k", "v", ttl_seconds=1)
    assert b.get("k") == "v"
    with patch("rag.core.cache_backend.time.time", return_value=time.time() + 2):
        assert b.get("k") is None


def test_in_memory_clear_is_prefix_scoped():
    b = cb.InMemoryBackend()
    b.set("keep:1", "a")
    b.set("drop:1", "b")
    b.clear("drop:")
    assert b.get("keep:1") == "a"
    assert b.get("drop:1") is None


# --- provider selection ------------------------------------------------------


def test_defaults_to_in_memory():
    with patch.object(Config, "cache_provider", "in_memory"):
        assert cb.get_cache_backend().name == "in_memory"


def test_unreachable_redis_falls_back_at_startup():
    """A PING failure must not leave the process without a cache."""
    client = MagicMock()
    client.ping.side_effect = ConnectionError("connection refused")
    with patch.object(Config, "cache_provider", "redis"), patch.object(Config, "cache_redis_host", "redis.test"):
        with patch("redis.Redis", return_value=client):
            backend = cb.get_cache_backend()
    assert backend.name == "in_memory"
    assert backend.set("k", "v") and backend.get("k") == "v"


def test_reachable_redis_is_used():
    client, _ = _redis_mock(value='{"a": 1}')
    with patch.object(Config, "cache_provider", "redis"), patch.object(Config, "cache_redis_host", "redis.test"):
        with patch("redis.Redis", return_value=client):
            backend = cb.get_cache_backend()
    assert backend.name == "redis+in_memory"
    assert backend.get("k") == {"a": 1}


# --- degradation after startup ----------------------------------------------


def test_redis_failing_mid_flight_degrades_to_memory():
    """This is the llm-server gap: there, a dead Redis means permanent misses."""
    client, _ = _redis_mock(fail=ConnectionError("gone"))
    with patch.object(Config, "cache_provider", "redis"), patch.object(Config, "cache_redis_host", "redis.test"):
        with patch("redis.Redis", return_value=client):
            backend = cb.get_cache_backend()
            assert backend.name == "redis+in_memory"
            for _ in range(cb._FAILURE_THRESHOLD):
                assert backend.get("k") is None  # swallowed, counted
            # Next selection retires Redis for this process.
            assert cb.get_cache_backend().name == "in_memory"


def test_data_written_while_redis_was_up_survives_it_going_down():
    """The reason writes go to both tiers.

    A Redis-only cache does not degrade when Redis dies, it empties -- and a
    caller that reads a miss as "does not exist" then returns a wrong answer.
    Caught live: every /collections/<name> returned 404 for a collection that
    existed, because the local tier had never been written to.
    """
    client = MagicMock()
    client.ping.return_value = True
    stored = {}
    client.set.side_effect = lambda k, v, ex=None: stored.__setitem__(k, v)
    client.get.side_effect = lambda k: stored.get(k)

    with patch.object(Config, "cache_provider", "redis"), patch.object(Config, "cache_redis_host", "redis.test"):
        with patch("redis.Redis", return_value=client):
            backend = cb.get_cache_backend()
            backend.set("coll", {"points_count": 88})
            assert backend.get("coll") == {"points_count": 88}

            # Redis dies.
            client.get.side_effect = ConnectionError("gone")
            client.set.side_effect = ConnectionError("gone")

            # Still served, from the local tier, with the real value.
            assert backend.get("coll") == {"points_count": 88}
            for _ in range(cb._FAILURE_THRESHOLD):
                backend.get("coll")
            assert cb.get_cache_backend().get("coll") == {"points_count": 88}


def test_reads_are_served_locally_without_touching_redis():
    """L1/L2: a key this process has seen costs no network round trip.

    Metadata is read per document during processing, so reading Redis first
    would put a hop on every lookup for data the local tier already holds.
    """
    client = MagicMock()
    client.ping.return_value = True
    fallback = cb.InMemoryBackend()
    fallback.set("k", {"from": "memory"})
    tier = cb.TieredBackend(cb.RedisBackend(client), fallback)

    assert tier.get("k") == {"from": "memory"}
    client.get.assert_not_called()


def test_a_local_miss_falls_through_to_redis_and_warms_l1():
    """The cold-start path: nothing local, so take the shared copy and keep it."""
    client, pipe = _redis_mock(value='{"from": "redis"}')
    fallback = cb.InMemoryBackend()
    tier = cb.TieredBackend(cb.RedisBackend(client), fallback)

    assert tier.get("k") == {"from": "redis"}
    assert pipe.execute.call_count == 1
    assert fallback.get("k") == {"from": "redis"}, "L1 warmed"

    pipe.execute.reset_mock()
    assert tier.get("k") == {"from": "redis"}
    pipe.execute.assert_not_called(), "second read is local"


def test_clearing_a_prefix_deletes_in_batches():
    """One DEL over the whole keyspace holds it all in memory and blocks Redis."""
    client = MagicMock()
    client.ping.return_value = True
    client.scan_iter.return_value = iter([f"rag:p{i}" for i in range(1250)])
    cb.RedisBackend(client).clear("p")

    assert client.delete.call_count == 3, "1250 keys at 500 per batch"
    assert [len(c.args) for c in client.delete.call_args_list] == [500, 500, 250]


def test_redis_errors_never_escape():
    client = MagicMock()
    client.ping.return_value = True
    for op in ("get", "set", "delete", "scan_iter"):
        getattr(client, op).side_effect = RuntimeError("boom")
    backend = cb.RedisBackend(client)
    assert backend.get("k") is None
    assert backend.set("k", "v") is False
    assert backend.delete("k") is False
    assert backend.clear("p") is False


def test_transient_error_does_not_retire_redis():
    """One blip shouldn't cost the shared cache for the rest of the process."""
    client, pipe = _redis_mock(fail=ConnectionError("blip"))
    backend = cb.RedisBackend(client)
    backend.get("k")
    assert backend.healthy()
    pipe.execute.side_effect = None
    pipe.execute.return_value = ['"ok"', -1]
    assert backend.get("k") == "ok"
    assert backend.healthy()


# --- MetadataCache over the backend -----------------------------------------


def test_metadata_cache_works_on_either_backend():
    from rag.core.metadata_cache import MetadataCache

    for provider in ("in_memory", "redis_down"):
        cb.reset_cache_backend()
        if provider == "redis_down":
            client = MagicMock()
            client.ping.side_effect = ConnectionError("refused")
            ctx = patch("redis.Redis", return_value=client)
            prov = "redis"
        else:
            ctx = patch("redis.Redis")
            prov = "in_memory"

        with patch.object(Config, "cache_provider", prov), ctx:
            c = MetadataCache()
            c.set("coll", {"module": "events"}, 5)
            got = c.get("coll")
            assert got["metadata"] == {"module": "events"}, provider
            assert got["points_count"] == 5, provider
            assert c.exists("coll"), provider
            assert c.stats()["total_cached"] == 1, provider
            c.invalidate("coll")
            assert c.get("coll") is None, provider


def test_metadata_cache_invalidate_all_clears_only_its_prefix():
    from rag.core.metadata_cache import MetadataCache

    with patch.object(Config, "cache_provider", "in_memory"):
        backend = cb.get_cache_backend()
        backend.set("unrelated:key", "keep")
        c = MetadataCache()
        c.set("a", {}, 0)
        c.set("b", {}, 0)
        c.invalidate()
        assert c.get("a") is None and c.get("b") is None
        assert backend.get("unrelated:key") == "keep"


def test_bulk_writes_use_one_round_trip():
    """A few hundred individual SETs dominated the refresh over a real link.

    Measured against dev Redis: 110s per-key versus 18s batched, for the same
    324 collections. This pins the pipeline so it cannot quietly go back.
    """
    client = MagicMock()
    client.ping.return_value = True
    pipe = MagicMock()
    client.pipeline.return_value = pipe

    backend = cb.RedisBackend(client)
    assert backend.set_many({f"k{i}": {"v": i} for i in range(300)})

    client.pipeline.assert_called_once()
    assert pipe.set.call_count == 300, "every key queued"
    pipe.execute.assert_called_once(), "but only one round trip"
    client.set.assert_not_called()


def test_bulk_write_failure_is_swallowed_like_any_other():
    client = MagicMock()
    client.ping.return_value = True
    client.pipeline.side_effect = ConnectionError("gone")
    assert cb.RedisBackend(client).set_many({"a": 1}) is False


def test_bulk_write_reaches_both_tiers():
    client = MagicMock()
    client.ping.return_value = True
    fallback = cb.InMemoryBackend()
    tier = cb.TieredBackend(cb.RedisBackend(client), fallback)
    tier.set_many({"a": {"x": 1}, "b": {"x": 2}})
    # Local tier populated, so a later Redis outage still serves these.
    assert fallback.get("a") == {"x": 1} and fallback.get("b") == {"x": 2}


def test_the_two_backends_agree_on_whether_a_caller_can_mutate_the_cache():
    """Redis deserialises JSON per read, so it always hands back a fresh object.

    In-memory used to hand back the stored reference, so the same code could
    corrupt the cache on one provider and not the other -- the worst shape for
    a bug, since the providers are meant to be interchangeable.
    """
    b = cb.InMemoryBackend()
    b.set("k", {"metadata": {"module": "events"}})

    got = b.get("k")
    got["metadata"]["module"] = "CORRUPTED"
    assert b.get("k") == {"metadata": {"module": "events"}}, "read must not expose the stored object"

    original = {"metadata": {"module": "events"}}
    b.set("k2", original)
    original["metadata"]["module"] = "CORRUPTED"
    assert b.get("k2") == {"metadata": {"module": "events"}}, "write must not alias the caller's object"


def test_local_only_writes_skip_redis():
    """Data that just came from Redis should not be written straight back."""
    client = MagicMock()
    client.ping.return_value = True
    fallback = cb.InMemoryBackend()
    tier = cb.TieredBackend(cb.RedisBackend(client), fallback)

    tier.set_many({"a": 1, "b": 2}, local_only=True)
    client.pipeline.assert_not_called()
    assert fallback.get("a") == 1 and fallback.get("b") == 2

    tier.set_many({"c": 3})
    client.pipeline.assert_called_once()


def test_redis_requested_without_a_host_says_so_and_still_serves():
    """Misconfiguration must be distinguishable from an outage, not fatal.

    Without the explicit check the only clue was "Error 61 connecting to
    :6379", which looks like Redis being down. Crashing instead would turn a
    cache misconfiguration into an outage, and this service treats the shared
    cache as optional throughout.
    """
    with patch.object(Config, "cache_provider", "redis"):
        with patch.object(Config, "cache_redis_host", ""):
            with patch("redis.Redis") as ctor:
                backend = cb.get_cache_backend()

    ctor.assert_not_called(), "no point dialling an empty host"
    assert backend.name == "in_memory"
    assert backend.set("k", "v") and backend.get("k") == "v", "still a working cache"


def test_warming_l1_carries_the_remaining_ttl():
    """Without this, a key that expires in Redis lives in memory forever.

    The shared snapshot has a 30-minute TTL. Copied into L1 with no expiry,
    this process would keep serving its first adopted snapshot and never see
    an update that every other replica picks up.
    """
    client = MagicMock()
    client.ping.return_value = True
    pipe = MagicMock()
    client.pipeline.return_value = pipe
    pipe.execute.return_value = ['{"v": 1}', 900]  # value, 900s left

    fallback = cb.InMemoryBackend()
    tier = cb.TieredBackend(cb.RedisBackend(client), fallback)

    assert tier.get("snap") == {"v": 1}
    assert "snap" in fallback._expiry, "L1 copy must expire too"
    assert 890 < fallback._expiry["snap"] - time.time() <= 900


def test_a_key_with_no_expiry_in_redis_gets_none_in_l1():
    """TTL -1 means no expiry; -2 means gone. Neither is a real TTL."""
    client = MagicMock()
    client.ping.return_value = True
    pipe = MagicMock()
    client.pipeline.return_value = pipe
    pipe.execute.return_value = ['{"v": 1}', -1]

    fallback = cb.InMemoryBackend()
    tier = cb.TieredBackend(cb.RedisBackend(client), fallback)
    assert tier.get("k") == {"v": 1}
    assert "k" not in fallback._expiry


def test_bad_float_config_degrades_instead_of_crashing():
    """Consistent with _int_env: a typo in a cache setting is not fatal."""
    from utils.config import _float_env

    with patch.dict("os.environ", {"X": "abc"}):
        assert _float_env("X", 2.0) == 2.0
    for bad in ("nan", "inf", "-inf"):
        with patch.dict("os.environ", {"X": bad}):
            assert _float_env("X", 2.0) == 2.0, bad
    with patch.dict("os.environ", {"X": "3.5"}):
        assert _float_env("X", 2.0) == 3.5


def test_a_corrupt_value_is_dropped_without_blaming_redis():
    """A bad value is a data problem; Redis answered fine.

    This never tripped the breaker -- the success is recorded the moment
    the pipeline returns, which resets the counter before decoding -- but
    the read should record no failure at all, not one it then forgets.
    """
    client, _ = _redis_mock(value="{not json", ttl=30)
    backend = cb.RedisBackend(client)

    for _ in range(cb._FAILURE_THRESHOLD + 1):
        assert backend.get("k") is None, "undecodable value reads as a miss"
    assert backend._failures == 0, "a decode error is not a Redis failure"
    assert backend.healthy()


def test_unserialisable_writes_do_not_retire_a_healthy_redis():
    """Writing a value json cannot encode must not count against Redis.

    Before the split these raised inside the same block that records
    backend health, so a caller passing a bad value repeatedly retired a
    perfectly healthy server and sent every later read to the local tier.
    """
    client, _ = _redis_mock()
    backend = cb.RedisBackend(client)

    for _ in range(cb._FAILURE_THRESHOLD + 2):
        assert backend.set("k", {"fn": lambda: 1}) is False
        assert backend.set_many({"a": object()}) is False

    assert backend._failures == 0, "serialisation errors are not Redis failures"
    assert backend.healthy(), "a healthy Redis must not be retired by bad input"
    client.set.assert_not_called(), "nothing unserialisable should reach Redis"


def test_add_publishes_with_the_lock_released():
    """The first add used to publish inside the lock; the others did not."""
    from rag.core import cache as cache_mod

    c = cache_mod.CollectionListCache(ttl_seconds=60)
    held = []
    original = c._publish_shared_snapshot

    def record_then_publish():
        # RLock is reentrant, so acquire() always succeeds here -- what we
        # care about is whether anyone else is locked out, which _is_locked
        # on the underlying lock reports.
        held.append(c._lock._is_owned())
        return original()

    c._publish_shared_snapshot = record_then_publish
    c.add(SimpleNamespace(name="a", metadata={}, points_count=1))
    c.add(SimpleNamespace(name="b", metadata={}, points_count=1))

    assert held == [False, False], f"publish ran while holding the cache lock: {held}"
