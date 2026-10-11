"""Toy service startup for the event_code_fix `startup_ordering_crashloop`
scenario, mined from a real merged fix (PR #37133).

This is the bug_commit version: a slow synchronous setup task runs BEFORE the
health server starts listening, so a readiness-probe deadline shorter than
the setup time always fails — the root cause the scenario's log lines
(repeated "connection refused" on the health port, then SIGTERM) describe.
"""

import time


class App:
    def __init__(self, slow_setup_seconds: float = 0):
        self._slow_setup_seconds = slow_setup_seconds
        self._health_ready = False

    def run(self):
        self._slow_setup()
        self._start_health_server()

    def _slow_setup(self):
        time.sleep(self._slow_setup_seconds)

    def _start_health_server(self):
        self._health_ready = True

    def is_healthy(self):
        return self._health_ready
