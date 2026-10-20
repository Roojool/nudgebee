package observability

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The replica keys used to read the ReplicaSet family through a prefix
// (`replicaset=~"services-server.*"`, `kube_replica_set:services-server-*`). That
// matches every ReplicaSet of every workload sharing the prefix AND every revision of
// the right one, so the key answered with a sum over unrelated Deployments. Measured
// on dev: 3 for a Deployment with 1 replica, over 22 ReplicaSets spanning
// services-server and services-server-worker.
//
// These tests pin the two properties that fix it: the selector names the workload
// exactly, and it reads the controller's own object rather than its ReplicaSets.

func replicaMeta(kind string) RequestMetadata {
	return RequestMetadata{Kind: kind, Namespace: "nudgebee", Name: "services-server"}
}

func TestReplicaKeys_MatchTheWorkloadExactly(t *testing.T) {
	for _, kind := range []string{"deployment", "statefulset", "daemonset", "replicaset", ""} {
		t.Run("prometheus/"+kind, func(t *testing.T) {
			queries := buildPrometheusWorkloadQueries(replicaMeta(kind), []string{"replica_defined", "replica_ready"})
			require.Len(t, queries, 2)
			for key, q := range queries {
				assert.NotContains(t, q, `services-server.*`, "%s still prefix-matches: %s", key, q)
				assert.NotContains(t, q, "=~", "%s must not use a regex matcher: %s", key, q)
				assert.Contains(t, q, `="services-server"`, "%s: %s", key, q)
				assert.Contains(t, q, `namespace="nudgebee"`, "%s: %s", key, q)
			}
		})
		t.Run("datadog/"+kind, func(t *testing.T) {
			queries := buildDatadogWorkloadQueries(replicaMeta(kind), []string{"replica_defined", "replica_ready"})
			require.Len(t, queries, 2)
			for key, q := range queries {
				assert.NotContains(t, q, "services-server-*", "%s still glob-matches: %s", key, q)
				assert.NotContains(t, q, "*}", "%s: %s", key, q)
				assert.Contains(t, q, ":services-server}", "%s: %s", key, q)
				assert.Contains(t, q, "kube_namespace:nudgebee", "%s: %s", key, q)
			}
		})
	}
}

// One workload is one number. Summing the matches reads N× wherever two
// kube-state-metrics replicas (or two Cluster Agents) report the same object — the dev
// cluster runs two.
func TestReplicaKeys_AggregateWithMaxNotSum(t *testing.T) {
	prom := buildPrometheusWorkloadQueries(replicaMeta("deployment"), []string{"replica_defined", "replica_ready"})
	for key, q := range prom {
		assert.True(t, strings.HasPrefix(q, "max("), "%s should aggregate with max: %s", key, q)
	}
	dd := buildDatadogWorkloadQueries(replicaMeta("deployment"), []string{"replica_defined", "replica_ready"})
	for key, q := range dd {
		assert.True(t, strings.HasPrefix(q, "max:"), "%s should aggregate with max: %s", key, q)
	}
}

// The kind picks the controller family. A StatefulSet has no ReplicaSet at all, so the
// old query could only ever answer 0 for one.
func TestReplicaKeys_ReadTheControllerForTheKind(t *testing.T) {
	cases := []struct {
		kind           string
		promDefined    string
		promReady      string
		datadogDefined string
		datadogReady   string
	}{
		{"deployment", "kube_deployment_spec_replicas", "kube_deployment_status_replicas_ready",
			"kubernetes_state.deployment.replicas_desired", "kubernetes_state.deployment.replicas_ready"},
		{"statefulset", "kube_statefulset_replicas", "kube_statefulset_status_replicas_ready",
			"kubernetes_state.statefulset.replicas_desired", "kubernetes_state.statefulset.replicas_ready"},
		{"daemonset", "kube_daemonset_status_desired_number_scheduled", "kube_daemonset_status_number_ready",
			"kubernetes_state.daemonset.desired", "kubernetes_state.daemonset.ready"},
		{"replicaset", "kube_replicaset_spec_replicas", "kube_replicaset_status_ready_replicas",
			"kubernetes_state.replicaset.replicas_desired", "kubernetes_state.replicaset.replicas_ready"},
		// Anything else, including an absent kind, is a Deployment — what every caller
		// sends today.
		{"", "kube_deployment_spec_replicas", "kube_deployment_status_replicas_ready",
			"kubernetes_state.deployment.replicas_desired", "kubernetes_state.deployment.replicas_ready"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			meta := replicaMeta(tc.kind)
			prom := buildPrometheusWorkloadQueries(meta, []string{"replica_defined", "replica_ready"})
			assert.Contains(t, prom["replica_defined"], tc.promDefined)
			assert.Contains(t, prom["replica_ready"], tc.promReady)

			dd := buildDatadogWorkloadQueries(meta, []string{"replica_defined", "replica_ready"})
			assert.Contains(t, dd["replica_defined"], tc.datadogDefined)
			assert.Contains(t, dd["replica_ready"], tc.datadogReady)

			// The kind also picks the identifying label/tag, so a StatefulSet is not
			// looked up under `deployment=`.
			assert.Contains(t, dd["replica_defined"], datadogReplicaTagForKind(tc.kind)+":services-server")
		})
	}
}

func datadogReplicaTagForKind(kind string) string {
	switch kind {
	case "statefulset":
		return "kube_stateful_set"
	case "daemonset":
		return "kube_daemon_set"
	case "replicaset":
		return "kube_replica_set"
	default:
		return "kube_deployment"
	}
}

// Every provider must still answer both keys; a kind must never silently drop one.
func TestReplicaKeys_BothKeysAlwaysAnswered(t *testing.T) {
	for _, kind := range []string{"deployment", "statefulset", "daemonset", "replicaset", "pod", ""} {
		prom := buildPrometheusWorkloadQueries(replicaMeta(kind), []string{"replica_defined", "replica_ready"})
		assert.NotEmpty(t, prom["replica_defined"], "prometheus/%s", kind)
		assert.NotEmpty(t, prom["replica_ready"], "prometheus/%s", kind)

		dd := buildDatadogWorkloadQueries(replicaMeta(kind), []string{"replica_defined", "replica_ready"})
		assert.NotEmpty(t, dd["replica_defined"], "datadog/%s", kind)
		assert.NotEmpty(t, dd["replica_ready"], "datadog/%s", kind)
	}
}
