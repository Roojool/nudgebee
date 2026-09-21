"""Every scan builds its own thread pool; none of it may outlive the scan.

Worker threads park on their work queue rather than exiting, so a pool that is merely
dropped keeps its threads -- and the scan's object graph with them -- alive for the life of
the gunicorn worker. Measured on dev: a pod that consumed three scans sat at 16 threads and
~250MB per worker against a pod that consumed none at 12 threads and 149MB, and the extra
threads never came back.

Two things here are load-bearing and easy to delete by accident:

1. The services are kept alive in `built`. Without that the local goes out of scope, CPython
   collects the executor, its weakref callback drains the workers, and the *unfixed* code
   passes this test. Production is the case where something still holds the reference.
2. The fakes submit real concurrent work. ThreadPoolExecutor spawns no threads at
   construction -- one per submitted task -- so a fake that does nothing proves nothing.
"""

import asyncio
import threading
from types import SimpleNamespace
from unittest.mock import MagicMock, patch

import pytest

import server.recommendation.volume_rightsizing as volume
from server.recommendation import vertical_rightsizing as vertical
from server.recommendation.vertical_rightsizing.services.service import RecommendationService

VERTICAL_PREFIX = "rightsizing"
VOLUME_PREFIX = "pvc-rightsize"
CLUSTER_ENGINE = (
    "server.recommendation.vertical_rightsizing.services.nudgebee_cluster_service.DatabaseEngine.get_engine"
)
VOLUME_ENGINE = "server.recommendation.volume_rightsizing.DatabaseEngine.get_engine"


def _live(prefix: str) -> list[str]:
    """Pool threads currently alive, by name.

    Scoped to the pool's own thread_name_prefix and compared against a snapshot, so the
    process's other threads (gRPC, OpenTelemetry, kombu, numpy) cannot move the result.
    """
    return sorted(t.name for t in threading.enumerate() if t.name.startswith(prefix))


async def _use_the_pool(executor) -> None:
    """Submit the way a real scan does: two calls in flight at once.

    A Barrier rather than a sleep, because one worker thread can service two sleeps in
    sequence and the pool would never grow to two. The 5s timeout fails loudly as
    BrokenBarrierError instead of hanging the suite if max_workers ever drops below 2.
    """
    loop = asyncio.get_running_loop()
    barrier = threading.Barrier(2)
    await asyncio.gather(
        loop.run_in_executor(executor, barrier.wait, 5),
        loop.run_in_executor(executor, barrier.wait, 5),
    )


def _keep_every_instance(cls, built: list):
    """Patch __init__ so the test holds a reference to every service the code under test builds.

    This reproduces production, where the executor is not collected, and is what makes the
    test fail against the unfixed code.
    """
    real_init = cls.__init__

    def spy_init(self, *args, **kwargs):
        real_init(self, *args, **kwargs)
        built.append(self)

    return patch.object(cls, "__init__", spy_init)


class TestVerticalRightsizingReleasesItsPool:
    def test_a_completed_scan_leaves_no_worker_threads_behind(self):
        async def fake_collect(self):
            await _use_the_pool(self._executor)
            return SimpleNamespace(scans=[])

        built: list = []
        before = _live(VERTICAL_PREFIX)

        with _keep_every_instance(RecommendationService, built), patch.object(
            RecommendationService, "_collect_result", fake_collect
        ), patch(CLUSTER_ENGINE, return_value=MagicMock()):
            for _ in range(3):
                asyncio.run(vertical.generate_recommendations("acct", None, tenant_id="tenant-1"))

        assert len(built) == 3, "the entry point should build one service per scan"
        assert all(svc._executor._shutdown for svc in built), "every pool must be shut down explicitly"
        assert _live(VERTICAL_PREFIX) == before, "worker threads outlived their scan"

    def test_a_failing_scan_still_releases_its_pool(self):
        """The scans that leaked in production are the ones that raise.

        The dev scan that permanently added a thread aborted in 0.35s with "No objects
        available to scan" -- it had already built the pool. This is why close() belongs in
        a finally and not after the await.
        """

        async def fake_collect_then_fail(self):
            await _use_the_pool(self._executor)
            raise RuntimeError("No objects available to scan.")

        built: list = []
        before = _live(VERTICAL_PREFIX)

        with _keep_every_instance(RecommendationService, built), patch.object(
            RecommendationService, "_collect_result", fake_collect_then_fail
        ), patch(CLUSTER_ENGINE, return_value=MagicMock()):
            for _ in range(3):
                with pytest.raises(RuntimeError):
                    asyncio.run(vertical.generate_recommendations("acct", None, tenant_id="tenant-1"))

        assert len(built) == 3
        assert all(svc._executor._shutdown for svc in built), "a failed scan must still shut its pool down"
        assert _live(VERTICAL_PREFIX) == before, "worker threads outlived a failed scan"


class TestVolumeRightsizingReleasesItsPool:
    def test_a_completed_scan_leaves_no_worker_threads_behind(self):
        async def fake_generate(self, namespace_filter=None, max_recommendations=None):
            await _use_the_pool(self._executor)
            return SimpleNamespace(database_stored=False)

        built: list = []
        before = _live(VOLUME_PREFIX)

        with _keep_every_instance(volume.VolumeRightsizingService, built), patch.object(
            volume.VolumeRightsizingService, "generate_recommendations", fake_generate
        ), patch(VOLUME_ENGINE, return_value=MagicMock()):
            for _ in range(3):
                asyncio.run(
                    volume.generate_volume_rightsizing_recommendations(
                        tenant_id="tenant-1", account_id="acct", persist_recommendation=False
                    )
                )

        assert len(built) == 3
        assert all(svc._executor._shutdown for svc in built), "every pool must be shut down explicitly"
        assert _live(VOLUME_PREFIX) == before, "worker threads outlived their scan"

    def test_a_failing_scan_still_releases_its_pool(self):
        async def fake_generate_then_fail(self, namespace_filter=None, max_recommendations=None):
            await _use_the_pool(self._executor)
            raise RuntimeError("provider unavailable")

        built: list = []
        before = _live(VOLUME_PREFIX)

        with _keep_every_instance(volume.VolumeRightsizingService, built), patch.object(
            volume.VolumeRightsizingService, "generate_recommendations", fake_generate_then_fail
        ), patch(VOLUME_ENGINE, return_value=MagicMock()):
            for _ in range(3):
                with pytest.raises(RuntimeError):
                    asyncio.run(
                        volume.generate_volume_rightsizing_recommendations(
                            tenant_id="tenant-1", account_id="acct", persist_recommendation=False
                        )
                    )

        assert len(built) == 3
        assert all(svc._executor._shutdown for svc in built), "a failed scan must still shut its pool down"
        assert _live(VOLUME_PREFIX) == before, "worker threads outlived a failed scan"
