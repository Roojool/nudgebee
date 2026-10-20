package k8s

import (
	"errors"
	"fmt"
	"nudgebee/runbook/internal/tasks/types"
	"nudgebee/runbook/services/relay"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ResolveTargetPod turns whatever a caller has — a pod name, or a workload and
// its kind — into one concrete pod name.
//
// Tasks that act on a pod cannot assume they were handed one. Events name the
// workload, not the pod: a PodMemoryReachingLimit fires with
// subject_type=deployment and subject_name=<deployment>, and the pod only
// appears in the title text. Feeding that straight through fails with
// `pods "<deployment>" not found`.
//
// When kind is a workload, the pod is chosen deterministically — oldest first,
// ties broken by name — so repeated runs profile or restart the same pod
// rather than hopping between replicas. targetPodName pins a specific one.
func ResolveTargetPod(
	taskCtx types.TaskContext,
	accountID, namespace, kind, name, targetPodName string,
) (string, error) {
	if strings.ToLower(kind) == "pod" {
		// When kind is Pod, target_pod_name (if provided) identifies the pod;
		// otherwise fall back to name. This lets callers that also send
		// target_pod_name for workload forms keep it working when kind is Pod
		// (e.g. form auto-fill from a workload+pod selection).
		if targetPodName != "" {
			if targetPodName != name {
				taskCtx.GetLogger().Debug("target_pod_name provided with kind=Pod, using target_pod_name", "name", name, "target_pod_name", targetPodName)
			}
			return targetPodName, nil
		}
		return name, nil
	}

	requestContext := taskCtx.GetNewRequestContext()

	// It's a workload (Deployment, ReplicaSet, DaemonSet, StatefulSet, Rollout)
	// First, get the workload to extract labels
	getWorkloadCmd := fmt.Sprintf("kubectl get %s %s -n %s -o json", kind, name, namespace)
	workloadResp, err := relay.ExecuteRelayJob(requestContext, accountID, relay.RelayJobKubectl, "", getWorkloadCmd, nil)
	if err != nil {
		return "", fmt.Errorf("failed to fetch workload '%s/%s': %w", namespace, name, err)
	}
	workloadMap, err := parseKubectlResponse(workloadResp)
	if err != nil {
		return "", fmt.Errorf("failed to parse workload response: %w", err)
	}
	workloadObj := &unstructured.Unstructured{Object: workloadMap}

	labels, found, err := unstructured.NestedStringMap(workloadObj.Object, "spec", "selector", "matchLabels")
	if !found || err != nil || len(labels) == 0 {
		return "", fmt.Errorf("could not find selector labels for workload '%s/%s'", namespace, name)
	}

	labelSelector := make([]string, 0, len(labels))
	for k, v := range labels {
		labelSelector = append(labelSelector, fmt.Sprintf("%s=%s", k, v))
	}
	// Sorted so the command is stable across runs; map iteration is not.
	sort.Strings(labelSelector)
	labelSelectorStr := strings.Join(labelSelector, ",")

	// Get pods for the workload
	getPodsCmd := fmt.Sprintf("kubectl get pods -l %s -n %s -o json", labelSelectorStr, namespace)
	podsResp, err := relay.ExecuteRelayJob(requestContext, accountID, relay.RelayJobKubectl, "", getPodsCmd, nil)
	if err != nil {
		return "", fmt.Errorf("failed to fetch pods for workload '%s/%s': %w", namespace, name, err)
	}
	podsMap, err := parseKubectlResponse(podsResp)
	if err != nil {
		return "", fmt.Errorf("failed to parse pods response: %w", err)
	}
	podsObj := &unstructured.Unstructured{Object: podsMap}

	items, found, err := unstructured.NestedSlice(podsObj.Object, "items")
	if !found || err != nil || len(items) == 0 {
		return "", fmt.Errorf("no pods found for workload '%s/%s'", namespace, name)
	}

	// Implement deterministic selection: Sort pods by creationTimestamp and then by name
	sort.Slice(items, func(i, j int) bool {
		podI, okI := items[i].(map[string]any)
		podJ, okJ := items[j].(map[string]any)
		if !okI || !okJ {
			return false // Cannot compare, maintain original order
		}

		// Sort by creationTimestamp (ascending)
		tsI, _, _ := unstructured.NestedString(podI, "metadata", "creationTimestamp")
		tsJ, _, _ := unstructured.NestedString(podJ, "metadata", "creationTimestamp")
		if tsI != tsJ {
			return tsI < tsJ
		}

		// Tie-breaker: sort by name (ascending)
		nameI, _, _ := unstructured.NestedString(podI, "metadata", "name")
		nameJ, _, _ := unstructured.NestedString(podJ, "metadata", "name")
		return nameI < nameJ
	})

	if targetPodName != "" {
		for _, item := range items {
			pod, ok := item.(map[string]any)
			if !ok {
				continue
			}
			pName, _, _ := unstructured.NestedString(pod, "metadata", "name")
			if pName == targetPodName {
				return targetPodName, nil
			}
		}
		return "", fmt.Errorf("specified target pod '%s' not found for workload '%s/%s'", targetPodName, namespace, name)
	}

	firstPod, ok := items[0].(map[string]any)
	if !ok {
		return "", errors.New("failed to parse first pod in list after sorting")
	}
	podName, _, _ := unstructured.NestedString(firstPod, "metadata", "name")
	if podName == "" {
		return "", errors.New("could not determine pod name after sorting")
	}
	taskCtx.GetLogger().Info("No specific target_pod_name provided, defaulting to deterministically selected pod", "pod", podName)
	return podName, nil
}
