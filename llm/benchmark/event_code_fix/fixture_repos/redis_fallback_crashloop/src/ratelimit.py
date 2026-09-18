"""Toy rate limiter builder for the event_code_fix `redis_fallback_crashloop`
scenario, mined from a real merged fix (PR #38409).

This is the bug_commit version: build() re-raises when the Redis-backed
counter store can't connect, instead of falling back to an in-memory store —
the root cause the scenario's log lines (Redis connection refused at boot,
immediate exit, restart loop) describe.
"""


class RedisUnavailable(Exception):
    pass


class MemStore:
    pass


class Limiter:
    def __init__(self, counters):
        self.counters = counters


def new_store_from_config(redis_client_factory):
    return redis_client_factory()


def build(redis_client_factory):
    counters = new_store_from_config(redis_client_factory)
    return Limiter(counters)
