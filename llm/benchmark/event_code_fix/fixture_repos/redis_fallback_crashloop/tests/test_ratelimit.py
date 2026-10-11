from src.ratelimit import MemStore, RedisUnavailable, build


class FakeRedisStore:
    pass


def _failing_redis_factory():
    raise RedisUnavailable("connection refused")


def test_build_uses_redis_when_available():
    limiter = build(lambda: FakeRedisStore())
    assert isinstance(limiter.counters, FakeRedisStore)


def test_build_falls_back_to_memstore_when_redis_unavailable():
    """FAIL_TO_PASS: build() must not crash when Redis is unreachable at
    boot — it should fall back to an in-memory store instead.

    Fails at bug_commit — the RedisUnavailable exception propagates
    uncaught. Passes once build() catches it and falls back to MemStore.
    """
    limiter = build(_failing_redis_factory)
    assert isinstance(limiter.counters, MemStore)
