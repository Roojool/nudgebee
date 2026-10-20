import pytest

from server.resource_models.k8s_resources import K8sContainer


@pytest.mark.parametrize(
    "container",
    [
        {"name": "nginx", "image": "nginx:alpine"},
        {"name": "nginx", "image": "nginx:alpine", "resources": {}},
    ],
)
def test_container_without_resources_has_no_limits(container):
    parsed = K8sContainer(**container)
    assert parsed.resources.limits.memory is None
    assert parsed.resources.limits.cpu is None
    assert parsed.resources.requests.memory is None
    assert parsed.resources.requests.cpu is None


def test_container_with_only_requests_parses_requests():
    parsed = K8sContainer(name="app", image="app:1", resources={"requests": {"cpu": "250m", "memory": "128Mi"}})
    assert parsed.resources.requests.cpu == 0.25
    assert parsed.resources.requests.memory == 128 * 1024**2
    assert parsed.resources.limits.cpu is None
    assert parsed.resources.limits.memory is None
