package playbooks

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func jobPod(name, namespace string, labels map[string]any, failed bool) map[string]any {
	md := map[string]any{"name": name, "namespace": namespace}
	if labels != nil {
		md["labels"] = labels
	}
	pod := map[string]any{"metadata": md, "status": map[string]any{"phase": "Running"}}
	if failed {
		pod["status"] = map[string]any{"phase": "Failed"}
	}
	return pod
}

// The agent answers get_resource with the cluster's pods regardless of the
// `namespace` and `label_selector` we send, so the caller does the filtering.
// Without it the first failing pod anywhere in the cluster was captioned as the
// Job's own output — 98% of job_failure findings on dev, 99.6% on prod.
func TestPodsOwnedByJob_RejectsForeignPods(t *testing.T) {
	list := []any{
		jobPod("memory-hog-7b565497b8-hbq8q", "demo", map[string]any{"app": "memory-hog"}, true),
		jobPod("nightly-report-29836280-xd896", "benchmark-scenarios", map[string]any{"job-name": "nightly-report-29836280"}, true),
		jobPod("nightly-report-29836288-q74hb", "benchmark-scenarios", map[string]any{"job-name": "nightly-report-29836288"}, true),
	}

	owned := podsOwnedByJob(list, "nightly-report-29836288", "benchmark-scenarios")

	assert.Len(t, owned, 1)
	assert.Equal(t, "nightly-report-29836288-q74hb", pickFailedPodName(owned))
}

func TestPodsOwnedByJob_NoOwnedPodYieldsNothing(t *testing.T) {
	list := []any{
		jobPod("catalog-api-84555d69cb-9wmk4", "nudgebee", map[string]any{"app": "catalog-api"}, true),
	}

	owned := podsOwnedByJob(list, "postgres-migration-job", "nudgebee")

	assert.Empty(t, owned)
	// An arbitrary failing pod is worse evidence than none: pickFailedPodName's
	// last-resort "first pod" must have nothing to fall back to.
	assert.Equal(t, "", pickFailedPodName(owned))
}

func TestPodsOwnedByJob_OwnerReferenceAndNamePrefix(t *testing.T) {
	byOwnerRef := map[string]any{
		"metadata": map[string]any{
			"name":      "java-api-checker-9d27c",
			"namespace": "app-12",
			"ownerReferences": []any{
				map[string]any{"kind": "Job", "name": "java-api-checker"},
			},
		},
		"status": map[string]any{"phase": "Failed"},
	}
	byName := jobPod("java-api-checker-abcde", "app-12", nil, true)
	// Anchored on the hyphen: a longer Job name that merely starts with ours.
	notOurs := jobPod("java-api-checker-extra-xyz12", "app-12",
		map[string]any{"job-name": "java-api-checker-extra"}, true)

	owned := podsOwnedByJob([]any{byOwnerRef, byName, notOurs}, "java-api-checker", "app-12")

	assert.Len(t, owned, 2)
}

func TestPodsOwnedByJob_SkipsOtherNamespaces(t *testing.T) {
	// Same generated name in two namespaces — only the event's own one counts.
	list := []any{
		jobPod("nightly-report-29836288-q74hb", "iteration-test", nil, true),
		jobPod("nightly-report-29836288-abcde", "benchmark-scenarios", nil, true),
	}

	owned := podsOwnedByJob(list, "nightly-report-29836288", "benchmark-scenarios")

	assert.Len(t, owned, 1)
	assert.Equal(t, "nightly-report-29836288-abcde", pickFailedPodName(owned))
}

// A pod whose metadata carries no namespace cannot be shown to belong to the
// event's namespace, and this action exists to stop foreign pods being captioned
// as the Job's own output — so it is skipped rather than given the benefit of the
// doubt.
func TestPodsOwnedByJob_PodWithoutANamespaceIsNotAssumedLocal(t *testing.T) {
	pod := jobPod("nightly-report-29836288-q74hb", "", map[string]any{"job-name": "nightly-report-29836288"}, true)

	owned := podsOwnedByJob([]any{pod}, "nightly-report-29836288", "benchmark-scenarios")

	assert.Empty(t, owned)
	// With no namespace asked for, the same pod is kept — the name still ties it
	// to the Job.
	assert.Len(t, podsOwnedByJob([]any{pod}, "nightly-report-29836288", ""), 1)
}
