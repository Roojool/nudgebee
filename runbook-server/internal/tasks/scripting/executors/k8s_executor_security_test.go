package executors_test

import (
	"errors"
	"log/slog"
	"testing"

	"nudgebee/runbook/internal/tasks/scripting/executors"
	"nudgebee/runbook/internal/tasks/testutils"

	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestKubernetesExecutorJobHasNoAmbientCredentials(t *testing.T) {
	client := fake.NewSimpleClientset()
	stopAtJob := errors.New("job spec captured")
	var job *batchv1.Job
	client.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		job = action.(ktesting.CreateAction).GetObject().(*batchv1.Job)
		return true, nil, stopAtJob
	})

	executor := &executors.KubernetesExecutor{Client: client, Namespace: "nudgebee"}
	ctx := testutils.NewTestTaskContext("tenant", "account", "user", slog.Default())
	_, err := executor.Execute(ctx, executors.ExecutionConfig{
		Script:   "echo ok",
		Language: "bash",
		Env:      map[string]string{"AWS_EC2_METADATA_DISABLED": "false"},
	})
	require.ErrorIs(t, err, stopAtJob)
	require.NotNil(t, job)
	require.Equal(t, "workflow-script", job.Labels["nudgebee.io/workload"])
	require.Equal(t, "workflow-script", job.Spec.Template.Labels["nudgebee.io/workload"])

	pod := job.Spec.Template.Spec
	require.NotNil(t, pod.AutomountServiceAccountToken)
	require.False(t, *pod.AutomountServiceAccountToken)
	require.NotNil(t, pod.EnableServiceLinks)
	require.False(t, *pod.EnableServiceLinks)
	require.False(t, pod.HostNetwork)
	require.False(t, pod.HostPID)
	require.False(t, pod.HostIPC)
	require.Len(t, pod.Containers, 1)
	var metadataDisabled string
	for _, env := range pod.Containers[0].Env {
		if env.Name == "AWS_EC2_METADATA_DISABLED" {
			metadataDisabled = env.Value
		}
	}
	require.Equal(t, "true", metadataDisabled)
	require.NotEmpty(t, pod.Containers[0].EnvFrom)
	require.NotNil(t, pod.Containers[0].EnvFrom[0].SecretRef)
	require.NotEmpty(t, pod.Containers[0].EnvFrom[0].SecretRef.Name)
}
