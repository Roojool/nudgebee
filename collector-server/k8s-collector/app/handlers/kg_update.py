import contextvars
import logging
import uuid
from contextlib import contextmanager
from datetime import datetime, timezone
from typing import Any, Dict, Iterable, List, Optional, Tuple

from config import Configs
from rabbitmq.rabbitmq_client import publish_message

logger = logging.getLogger(__name__)

# 2 hours in milliseconds, matching the api-server publisher TTL
KG_UPDATE_MESSAGE_TTL_MS = 2 * 60 * 60 * 1000

# Kinds whose deletion is not reported per resource. They churn constantly, the
# api-server's inventory detector ignores them for the same reason, and the
# hourly graph build removes them.
_UNREPORTED_KINDS = frozenset({"Pod", "Job"})

# Graph node types of the refs this module sends. Workloads are addressed by
# their k8s_workloads cloud_resource_id, Kubernetes nodes by name (the api-server
# resolves "Node" refs by name because node graph nodes carry no provider id).
WORKLOAD_NODE_TYPE = "Workload"
K8S_NODE_NODE_TYPE = "Node"

# (resource id, node type) pairs deactivated while processing one discovery message.
_deleted_resources: contextvars.ContextVar[Optional[List[Tuple[str, str]]]] = contextvars.ContextVar(
    "kg_deleted_resources", default=None
)


def publish_kg_update(
    tenant_id: str,
    cloud_account_id: Optional[str] = None,
    resources: Optional[List[dict]] = None,
) -> None:
    """Publish a knowledge-graph update message.

    With resources, the api-server updates just those resources' nodes; otherwise
    the message asks for cloud_account_id's graph to be rebuilt. Creates and
    rollouts are not published from here: the api-server detects them from the
    inventory tables every 10 minutes.

    Failures are logged but never raised -- KG updates are best-effort and must not
    break the discovery pipeline.
    """
    now = datetime.now(timezone.utc)
    correlation_id = str(uuid.uuid4())
    message: Dict[str, Any] = {
        "tenant_id": tenant_id,
        "source": "k8s-collector",
        "requested_at": now.isoformat(),
        "correlation_id": correlation_id,
    }
    if resources:
        message["resources"] = resources
    elif cloud_account_id:
        message["account_ids"] = [cloud_account_id]

    try:
        publish_message(
            exchange_name=Configs.RABBIT_MQ_KG_UPDATE_EXCHANGE,
            routing_key=Configs.RABBIT_MQ_KG_UPDATE_QUEUE,
            message=message,
            exchange_type="direct",
            message_ttl=KG_UPDATE_MESSAGE_TTL_MS,
        )
        logger.info(
            "kg_update: published KG update message for tenant %s account %s (%d resources, correlation_id=%s)",
            tenant_id,
            cloud_account_id,
            len(resources or []),
            correlation_id,
        )
    except Exception:
        logger.exception("kg_update: failed to publish KG update message for tenant %s", tenant_id)


def record_deleted_workloads(rows: Iterable) -> None:
    """Record workloads a discovery pass just deactivated.

    rows are (cloud_resource_id, kind) tuples from an UPDATE ... RETURNING.
    Outside a deletion_scope this does nothing.
    """
    deleted = _deleted_resources.get()
    if deleted is None:
        return
    for row in rows or []:
        cloud_resource_id, kind = row[0], row[1]
        if kind not in _UNREPORTED_KINDS:
            deleted.append((str(cloud_resource_id), WORKLOAD_NODE_TYPE))


def record_deleted_nodes(rows: Iterable) -> None:
    """Record Kubernetes nodes a discovery pass just deactivated.

    rows are (name,) tuples from an UPDATE k8s_nodes ... RETURNING name.
    Outside a deletion_scope this does nothing.
    """
    deleted = _deleted_resources.get()
    if deleted is None:
        return
    for row in rows or []:
        name = row[0]
        if name:
            deleted.append((str(name), K8S_NODE_NODE_TYPE))


@contextmanager
def deletion_scope(tenant_id: str, cloud_account_id: str):
    """Collect workload and node deletions during one discovery message and, on
    exit, publish them as per-resource delete refs in one message."""
    deleted: List[Tuple[str, str]] = []
    token = _deleted_resources.set(deleted)
    try:
        yield
    finally:
        _deleted_resources.reset(token)
        if deleted:
            publish_kg_update(
                tenant_id,
                cloud_account_id,
                resources=[
                    {
                        "account_id": cloud_account_id,
                        "provider_resource_id": resource_id,
                        "node_type": node_type,
                        "op": "delete",
                    }
                    for resource_id, node_type in dict.fromkeys(deleted)
                ],
            )
