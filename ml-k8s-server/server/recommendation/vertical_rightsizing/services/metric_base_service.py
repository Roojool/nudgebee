import abc
import datetime
from concurrent.futures import ThreadPoolExecutor
from typing import Optional, Dict, Any

from server.recommendation.vertical_rightsizing.models.result import PodsTimeData
from server.recommendation.vertical_rightsizing.models.objects import K8sObjectData
from server.recommendation.vertical_rightsizing.models.replicas import ReplicaStats
from server.recommendation.vertical_rightsizing.metrics.base import PrometheusMetric
from server.recommendation.vertical_rightsizing.models.config import Config


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
    async def get_cluster_summary(self) -> Dict[str, Any]:
        pass

    @abc.abstractmethod
    async def gather_data(
        self,
        object: K8sObjectData,
        LoaderClass: type[PrometheusMetric],
        period: datetime.timedelta,
        step: datetime.timedelta = datetime.timedelta(minutes=30),
    ) -> PodsTimeData:
        pass

    async def load_replica_stats(self, object: K8sObjectData, period: datetime.timedelta) -> Optional[ReplicaStats]:
        """Replica count over the window for the savings multiplier; None means fall back to the live count."""
        return None

    def get_prometheus_cluster_label(self) -> str:
        return " __CLUSTER__ "
