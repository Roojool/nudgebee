import abc
import datetime
from concurrent.futures import ThreadPoolExecutor
from typing import Optional, Dict, List

from server.recommendation.vertical_rightsizing.models.result import PodsTimeData
from server.recommendation.vertical_rightsizing.models.objects import K8sObjectData, PodData
from server.recommendation.vertical_rightsizing.models.replicas import ReplicaStats
from server.recommendation.vertical_rightsizing.models.config import Config
from server.recommendation.vertical_rightsizing.strategy.strategies import BaseStrategy


class MetricsService(abc.ABC):
    def __init__(
        self,
        config: Config,
        cluster: Optional[str] = None,
        executor: Optional[ThreadPoolExecutor] = None,
    ) -> None:
        self.cluster = cluster or "default"
        self.executor = executor
        self._config = config

    @abc.abstractmethod
    def check_connection(self):
        pass

    @classmethod
    def name(cls) -> str:
        classname = cls.__name__
        return classname.replace("MetricsService", "") if classname != MetricsService.__name__ else classname

    @abc.abstractmethod
    async def load_pods(self, object: K8sObjectData, period: datetime.timedelta) -> List[PodData]:
        """Which pods the workload HAD over the window, live or since deleted."""

    @abc.abstractmethod
    async def gather_data(
        self,
        object: K8sObjectData,
        strategy: BaseStrategy,
        period: datetime.timedelta,
        *,
        step: datetime.timedelta = datetime.timedelta(minutes=30),
    ) -> Dict[str, PodsTimeData]:
        """Every metric the strategy declared, keyed by metric name then by pod."""

    async def load_replica_stats(self, object: K8sObjectData, period: datetime.timedelta) -> Optional[ReplicaStats]:
        """Replica count over the window for the savings multiplier; None means fall back to the live count."""
        return None

    def get_prometheus_cluster_label(self) -> str:
        return " __CLUSTER__ "
