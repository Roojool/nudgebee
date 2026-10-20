package workspace

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// isPodInTerminalState's reason string is the only diagnostic a poller has
// for why a workspace pod stopped serving a run — pinned here so a pod's own
// kubelet-reported cause (Evicted, DiskPressure, ...) survives into it
// instead of collapsing to just the phase (#38994).
func TestIsPodInTerminalState_AppendsKubeletReason(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pod      *corev1.Pod
		terminal bool
		want     string
	}{
		{
			name: "failed phase without a reason falls back to the phase alone",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase: corev1.PodFailed,
			}},
			terminal: true,
			want:     "pod has failed (Failed phase)",
		},
		{
			name: "failed phase with an eviction reason and message",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase:   corev1.PodFailed,
				Reason:  "Evicted",
				Message: "Pod ephemeral local storage usage exceeds the total limit of containers 5Gi.",
			}},
			terminal: true,
			want:     "pod has failed (Failed phase): Evicted (Pod ephemeral local storage usage exceeds the total limit of containers 5Gi.)",
		},
		{
			name: "succeeded phase with a reason but no message",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase:  corev1.PodSucceeded,
				Reason: "Completed",
			}},
			terminal: true,
			want:     "pod has completed (Succeeded phase): Completed",
		},
		{
			name:     "running pod is not terminal",
			pod:      &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodRunning}},
			terminal: false,
			want:     "",
		},
		{
			name: "terminating pod carries the reason too",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: &metav1.Time{}},
				Status:     corev1.PodStatus{Reason: "NodeShutdown"},
			},
			terminal: true,
			want:     "pod is terminating: NodeShutdown",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			terminal, reason := isPodInTerminalState(tc.pod)
			assert.Equal(t, tc.terminal, terminal)
			assert.Equal(t, tc.want, reason)
		})
	}
}
