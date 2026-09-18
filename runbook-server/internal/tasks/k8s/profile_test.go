package k8s

import (
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"nudgebee/runbook/config"
	"nudgebee/runbook/internal/tasks/testutils"
	"nudgebee/runbook/internal/tasks/types"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProfileTask_Schemas(t *testing.T) {
	task := &ProfileTask{}

	in := task.InputSchema()
	require.NotNil(t, in)
	assert.Contains(t, in.Properties, "namespace")
	assert.Contains(t, in.Properties, "pod_name")
	assert.Equal(t, "cpu", in.Properties["profile_type"].Default)
	// Language is optional on purpose: the agent resolves it from the
	// node-agent's container_application_type metric.
	assert.False(t, in.Properties["application_language"].Required)

	out := task.OutputSchema()
	require.NotNil(t, out)
	assert.Contains(t, out.Properties, "profile_task_id")
	assert.Contains(t, out.Properties, "profile_tool")
	// The artefact itself is deliberately absent — it is stored against the
	// pod, and a flamegraph would bloat every workflow payload.
	assert.NotContains(t, out.Properties, "contents_base64")
	assert.Equal(t, types.PropertyTypeString, out.Properties["status"].Type)
}

// Test_base64DecodedSize — size_bytes describes the artefact, so it has to be
// the decoded length; the encoded string is a third larger.
func Test_base64DecodedSize(t *testing.T) {
	cases := []struct {
		name    string
		encoded string
		want    int
		wantOK  bool
	}{
		{"no padding", base64.StdEncoding.EncodeToString([]byte("abcdef")), 6, true},
		{"one pad byte", base64.StdEncoding.EncodeToString([]byte("abcde")), 5, true},
		{"two pad bytes", base64.StdEncoding.EncodeToString([]byte("abcd")), 4, true},
		{"empty", "", 0, false},
		{"not base64-shaped", "AAA", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := base64DecodedSize(tc.encoded)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestProfileTask_Execute_Validation(t *testing.T) {
	task := &ProfileTask{}
	taskCtx := testutils.NewTestTaskContext(os.Getenv("TEST_TENANT_ID"), os.Getenv("TEST_K8S_ACCOUNT_ID"), os.Getenv("TEST_USER_ID"), slog.Default())

	cases := []struct {
		name   string
		params map[string]any
		expect string
	}{
		{"missing namespace", map[string]any{"pod_name": "web-0"}, "namespace and pod_name are required"},
		{"missing pod", map[string]any{"namespace": "shop"}, "namespace and pod_name are required"},
		{"bad namespace", map[string]any{"namespace": "shop;rm -rf /", "pod_name": "web-0"}, "invalid namespace format"},
		{"bad pod", map[string]any{"namespace": "shop", "pod_name": "web 0"}, "invalid pod_name format"},
		{"bad profile type", map[string]any{"namespace": "shop", "pod_name": "web-0", "profile_type": "allocations"}, "profile_type must be cpu or memory"},
		{"duration over the cap", map[string]any{"namespace": "shop", "pod_name": "web-0", "profile_duration": 900}, "at most 600s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := task.Execute(taskCtx, tc.params)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.expect)
		})
	}
}

// TestProfileTask_Execute_SubmitsAndPolls covers the whole happy path against a
// stubbed services-server: one submit, then polling until the agent reports a
// terminal state.
func TestProfileTask_Execute_SubmitsAndPolls(t *testing.T) {
	var submits, polls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/rpc/application", r.URL.Path)

		var envelope struct {
			Action struct {
				Name string `json:"name"`
			} `json:"action"`
			Input struct {
				Request map[string]any `json:"request"`
			} `json:"input"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&envelope))

		w.Header().Set("Content-Type", "application/json")
		switch envelope.Action.Name {
		case profileSubmitAction:
			atomic.AddInt32(&submits, 1)
			assert.Equal(t, "shop", envelope.Input.Request["namespace"])
			assert.Equal(t, "web-0", envelope.Input.Request["pod_name"])
			assert.Equal(t, "cpu", envelope.Input.Request["profile_type"])
			// Not named by the caller, so it must not be sent — the agent
			// detects it.
			assert.NotContains(t, envelope.Input.Request, "application_language")
			_, _ = w.Write([]byte(`{"data":{"profile_task_id":"prof-1","status":"TODO"}}`))
		case profileStatusAction:
			if atomic.AddInt32(&polls, 1) < 2 {
				_, _ = w.Write([]byte(`{"data":{"status":"PROCESSING"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":{"status":"COMPLETED","base64_profile":{"lang":"java","profile_tool":"async-profiler","filename":"/tmp/agent-flamegraph-1.html.gz","contents_base64":"AAAA"}}}`))
		default:
			t.Fatalf("unexpected action %q", envelope.Action.Name)
		}
	}))
	defer server.Close()

	withServiceEndpoint(t, server.URL)
	withFastPolling(t)

	task := &ProfileTask{}
	taskCtx := testutils.NewTestTaskContext("tenant", "account", "user", slog.Default())
	result, err := task.Execute(taskCtx, map[string]any{
		"namespace": "shop",
		"pod_name":  "web-0",
		// Templated parameters arrive as strings.
		"profile_duration": "30",
	})
	require.NoError(t, err)

	out, ok := result.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "COMPLETED", out["status"])
	assert.Equal(t, "prof-1", out["profile_task_id"])
	// What the agent actually did, not what was asked for.
	assert.Equal(t, "java", out["lang"])
	assert.Equal(t, "async-profiler", out["profile_tool"])
	assert.NotContains(t, out, "contents_base64")
	// "AAAA" is four base64 characters, so three bytes of artefact.
	assert.Equal(t, 3, out["size_bytes"])
	assert.Equal(t, int32(1), atomic.LoadInt32(&submits))
	assert.Equal(t, int32(2), atomic.LoadInt32(&polls))
}

// TestProfileTask_Execute_ReportsAgentFailure — a failed profile has to fail the
// task with the agent's reason, not pass quietly with nothing captured.
func TestProfileTask_Execute_ReportsAgentFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			Action struct {
				Name string `json:"name"`
			} `json:"action"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&envelope))
		w.Header().Set("Content-Type", "application/json")
		if envelope.Action.Name == profileSubmitAction {
			_, _ = w.Write([]byte(`{"data":{"profile_task_id":"prof-2","status":"TODO"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"status":"FAILED","message":"could not determine the application language"}}`))
	}))
	defer server.Close()

	withServiceEndpoint(t, server.URL)
	withFastPolling(t)

	task := &ProfileTask{}
	taskCtx := testutils.NewTestTaskContext("tenant", "account", "user", slog.Default())
	_, err := task.Execute(taskCtx, map[string]any{"namespace": "shop", "pod_name": "web-0"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not determine the application language")
}

func TestProfileTask_Execute_DryRunDoesNotProfile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("a dry run must not reach services-server")
	}))
	defer server.Close()
	withServiceEndpoint(t, server.URL)

	task := &ProfileTask{}
	taskCtx := testutils.NewTestTaskContext("tenant", "account", "user", slog.Default())
	taskCtx.(*testutils.MockTaskContext).DryRun = true

	result, err := task.Execute(taskCtx, map[string]any{"namespace": "shop", "pod_name": "web-0"})
	require.NoError(t, err)
	out, ok := result.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, out["dry_run"])
}

// withFastPolling keeps the polling tests instant; the production interval is
// paced for the agent's 2-minute queue tick.
func withFastPolling(t *testing.T) {
	t.Helper()
	previous := profilePollInterval
	profilePollInterval = time.Millisecond
	t.Cleanup(func() { profilePollInterval = previous })
}

func withServiceEndpoint(t *testing.T, url string) {
	t.Helper()
	previous := config.Config.ServiceEndpoint
	config.Config.ServiceEndpoint = url
	t.Cleanup(func() { config.Config.ServiceEndpoint = previous })
}
