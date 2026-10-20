package observability

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// container_id is a PATH, not a name: /k8s/<namespace>/<pod>/<container>.
//
// Every caller filtered `container_id=~"<name>"`, and Prometheus anchors its regexes, so
// that required container_id to EQUAL the name. It never can, so eleven keys answered
// nothing on every provider. Measured on dev: 0 series for the name form, 696 for this
// one, and the same on container_log_messages_total (0 vs 50) and
// container_net_tcp_bytes_received_total (0 vs 21).
func TestPromContainerIDPath_MatchesThePathNotTheName(t *testing.T) {
	got := promContainerIDPath("deployment", "nudgebee", "services-server", "services-server")
	assert.Equal(t, `container_id=~"/k8s/nudgebee/services-server-.*/services-server"`, got)

	// The pod segment needs the `-`: a workload's pods are <workload>-<hash>-<id>, so
	// matching <workload>/ lands on a pod named exactly the workload, which never exists.
	assert.NotContains(t, got, `/services-server/services-server`)
	assert.Contains(t, got, `services-server-.*`)
}

// A missing part widens to `.*` rather than rendering an empty segment, which would make
// the path `/k8s//pod/c` and match nothing.
func TestPromContainerIDPath_MissingPartsWiden(t *testing.T) {
	assert.Equal(t, `container_id=~"/k8s/nudgebee/services-server-.*/.*"`,
		promContainerIDPath("deployment", "nudgebee", "services-server", ""))
	assert.Equal(t, `container_id=~"/k8s/nudgebee/.*/.*"`,
		promContainerIDPath("deployment", "nudgebee", "", ""))
	assert.Equal(t, `container_id=~"/k8s/.*/.*/.*"`,
		promContainerIDPath("deployment", "", "", ""))
}

// A regex metacharacter in a name must not become part of the pattern.
//
// The double backslash is deliberate and matches promPodNameAlternation: QuoteMeta gives
// `my\.app`, then the PromQL string escape doubles the backslash so the literal parses.
// The reverse order renders `my\.app` into the query and the parser rejects it outright
// with `unknown escape sequence`, failing every key in the same request.
func TestPromContainerIDPath_EscapesItsParts(t *testing.T) {
	got := promContainerIDPath("deployment", "ns", "my.app", "side.car")
	assert.Contains(t, got, `my\\.app-.*`)
	assert.Contains(t, got, `side\\.car`)
	assert.NotContains(t, got, `my.app-`, "an unescaped dot would match any character")
}

// The keys that were silently empty must now carry a path-shaped matcher. Asserting on the
// rendered query is the only check that would have caught the original bug: the keys built
// fine, rendered fine, and returned nothing.
func TestBuildPrometheusWorkloadQueries_ContainerIDKeysUseThePathForm(t *testing.T) {
	meta := RequestMetadata{
		Kind: "deployment", Namespace: "nudgebee",
		Name: "services-server", ContainerName: "services-server",
	}
	keys := []string{
		"container_http_latency_p99", "container_http_latency_mean",
		"container_http_request_count", "container_http_error_status_count",
		"container_error_log_count_with_workload", "container_top_http_requests",
	}
	queries := buildPrometheusWorkloadQueries(meta, keys)
	for _, k := range keys {
		q, ok := queries[k]
		require.Truef(t, ok, "%s must still build", k)
		assert.Containsf(t, q, "/k8s/", "%s must match container_id as a path", k)
		assert.NotContainsf(t, q, `container_id=~"services-server"`,
			"%s must not match a path against a bare name", k)
	}
}

// At POD scope the caller's Name IS the pod, so the segment must be exact.
//
// Appending the workload prefix there asks for a pod whose name EXTENDS a full pod name,
// which no pod has — so every pod-scoped container key would answer nothing, the same
// silent-empty this PR exists to remove. Raised in review on #39349.
func TestPromContainerIDPath_PodScopeMatchesTheExactPod(t *testing.T) {
	pod := "services-server-78fcc5b679-5scts"

	exact := promContainerIDPath("pod", "nudgebee", pod, "")
	assert.Equal(t, `container_id=~"/k8s/nudgebee/services-server-78fcc5b679-5scts/.*"`, exact)
	assert.NotContains(t, exact, pod+"-.*", "a full pod name must not be treated as a prefix")

	// An unspecified kind reaches these keys with a pod in Name, the same reading the
	// rest of the package takes, so it is exact too.
	assert.Equal(t, exact, promContainerIDPath("", "nudgebee", pod, ""))

	// A workload kind still needs the prefix: its pods are <workload>-<hash>-<id>.
	prefix := promContainerIDPath("deployment", "nudgebee", "services-server", "")
	assert.Contains(t, prefix, `services-server-.*`)
	assert.NotEqual(t, exact, prefix)
}

// The same keys asked at POD scope must carry the exact pod, not a prefix of it.
//
// A builder-level check, because the helper being right is not enough: the kind has to
// reach it from every call site. Sixteen sites pass it, and one of them forgetting would
// look exactly like the bug this PR fixes. Raised in review on #39349.
func TestBuildPrometheusWorkloadQueries_PodScopeUsesTheExactPod(t *testing.T) {
	pod := "services-server-78fcc5b679-5scts"
	meta := RequestMetadata{
		Kind: "pod", Namespace: "nudgebee",
		Name: pod, ContainerName: "services-server",
	}
	keys := []string{
		"container_http_latency_p99", "container_http_request_count",
		"container_error_log_count_with_pod", "container_top_http_requests",
	}
	queries := buildPrometheusWorkloadQueries(meta, keys)
	for _, k := range keys {
		q, ok := queries[k]
		require.Truef(t, ok, "%s must still build at pod scope", k)
		assert.Containsf(t, q, "/k8s/nudgebee/"+pod+"/",
			"%s must match the exact pod at pod scope", k)
		assert.NotContainsf(t, q, pod+"-.*",
			"%s must not treat a full pod name as a prefix", k)
	}
}
