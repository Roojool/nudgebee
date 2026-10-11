from src.cache import SubscriberCache


def test_cache_returns_active_subscriber():
    cache = SubscriberCache()
    cache.put("sub-1", {"name": "Ada"})
    assert cache.get("sub-1") == {"name": "Ada"}


def test_cache_evicts_stale_subscribers():
    """FAIL_TO_PASS: the cache must not grow without bound.

    Fails at bug_commit (put() never evicts, so size() keeps climbing with
    every new subscriber id). Passes once put() enforces a bounded size or
    TTL-based eviction policy.
    """
    cache = SubscriberCache()
    for i in range(10_000):
        cache.put(f"sub-{i}", {"n": i})
    assert cache.size() <= 1000
