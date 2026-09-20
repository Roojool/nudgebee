from dataclasses import dataclass, field
from datetime import datetime
from typing import Dict, Any, Optional

from apis.model.base_model import BaseDetails
from utils.datetime_utils import utc_now


@dataclass
class PodDetails(BaseDetails):
    cloud_resource_id: str
    external_id: str
    name: str

    namespace: str
    status: str
    node_name: str
    is_active: bool = True
    # kubectl's STATUS reason (CrashLoopBackOff, ImagePullBackOff, OOMKilled, ...)
    # when a container is broken, None when the pod is healthy. `status` above is
    # only the phase, which is "Running" for a crashlooping pod, so counting or
    # filtering on it hides every broken pod (#36593).
    container_status: Optional[str] = None
    workload_type: Optional[str] = None
    workload_name: Optional[str] = None
    restart_count: Dict[str, int] = field(default_factory=dict)
    creation_time: datetime = field(default_factory=utc_now)
    last_seen: datetime = field(default_factory=utc_now)
    labels: Dict[str, str] = field(default_factory=dict)
    meta: Dict[str, Any] = field(default_factory=dict)
