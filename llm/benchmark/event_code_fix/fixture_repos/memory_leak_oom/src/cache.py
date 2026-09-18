"""Toy subscriber cache for the event_code_fix `memory_leak_oom` scenario.

This is the bug_commit version: put() never evicts, so the cache grows
without bound as new subscriber ids come in — the root cause the scenario's
log lines (rising heap_alloc_mb over ~12 minutes, then OOMKilled) describe.
"""


class SubscriberCache:
    def __init__(self):
        self._store = {}

    def put(self, subscriber_id, payload):
        self._store[subscriber_id] = payload

    def get(self, subscriber_id):
        return self._store.get(subscriber_id)

    def size(self):
        return len(self._store)
