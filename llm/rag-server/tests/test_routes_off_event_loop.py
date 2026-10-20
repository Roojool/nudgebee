import ast
import importlib
import inspect
import pkgutil
import textwrap

import pytest

import controllers

# Handlers that are `async def`, await nothing, and still belong on the event loop
# because they do no I/O. /health answers the pod's liveness and readiness probes, so
# it must never queue behind a thread pool that slow requests have filled.
NON_BLOCKING = {
    "controllers.health.health_check",
    "controllers.cache_controller.get_cache_stats",
    "controllers.cache_controller.clear_cache",
    "controllers.migration_controller.get_qdrant_migration_status",
}

MODULES = sorted(name for _, name, _ in pkgutil.iter_modules(controllers.__path__, "controllers."))


def awaits_something(fn):
    tree = ast.parse(textwrap.dedent(inspect.getsource(fn)))
    return any(isinstance(node, (ast.Await, ast.AsyncFor, ast.AsyncWith)) for node in ast.walk(tree))


@pytest.mark.parametrize("module", MODULES)
def test_blocking_routes_run_off_the_event_loop(module):
    # An `async def` route that awaits nothing runs its blocking calls (Qdrant, the
    # database) on the event loop, so this single-worker server serves nothing else
    # until it returns. Plain `def` runs it in FastAPI's thread pool instead.
    router = getattr(importlib.import_module(module), "router", None)
    if router is None:
        return
    offenders = []
    for route in router.routes:
        endpoint = getattr(route, "endpoint", None)
        if not inspect.iscoroutinefunction(endpoint) or awaits_something(endpoint):
            continue
        name = f"{endpoint.__module__}.{endpoint.__name__}"
        if name not in NON_BLOCKING:
            offenders.append(name)
    assert offenders == []
