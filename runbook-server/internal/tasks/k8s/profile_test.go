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
	// The artefact comes back so downstream tasks can use it, bounded by size.
	assert.Contains(t, out.Properties, "file")
	assert.Contains(t, out.Properties, "contents_omitted")
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
		{"bad kind", map[string]any{"namespace": "shop", "pod_name": "web-0", "kind": "Deploy;ment"}, "invalid kind format"},
		{"bad target pod", map[string]any{"namespace": "shop", "pod_name": "web", "kind": "Deployment", "target_pod_name": "a b"}, "invalid target_pod_name format"},
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

// TestProfileTask_Execute_AcceptsNameAsAlias — the task shipped with pod_name
// and the live automation still sends it; `name` is the shape the rest of the
// k8s tasks use, so both have to work.
func TestProfileTask_Execute_AcceptsNameAsAlias(t *testing.T) {
	task := &ProfileTask{}
	taskCtx := testutils.NewTestTaskContext("tenant", "account", "user", slog.Default())
	taskCtx.(*testutils.MockTaskContext).DryRun = true

	for _, key := range []string{"pod_name", "name"} {
		t.Run(key, func(t *testing.T) {
			out, err := task.Execute(taskCtx, map[string]any{"namespace": "shop", key: "web-0"})
			require.NoError(t, err)
			req := out.(map[string]any)["request"].(map[string]any)
			assert.Equal(t, "web-0", req["pod_name"])
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
			// base64_profile wraps the agent response, whose own data is the
			// file block — the shape services-server actually returns.
			_, _ = w.Write([]byte(`{"data":{"status":"COMPLETED","base64_profile":{"data":{"success":true,"task_processing_duration":42,"data":{"lang":"java","profile_tool":"async-profiler","filename":"/tmp/agent-flamegraph-1.html.gz","contents_base64":"AAAA"}}}}}`))
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
	// "AAAA" is four base64 characters, so three bytes of artefact.
	assert.Equal(t, 3, out["size_bytes"])
	// Small enough to carry, so the bytes come back shaped for the profile card.
	file, ok := out["file"].(map[string]any)
	require.True(t, ok, "the profile itself must come back — a handle alone is unusable downstream")
	assert.Equal(t, "AAAA", file["data"])
	assert.Equal(t, "gz", file["type"])
	assert.Equal(t, "/tmp/agent-flamegraph-1.html.gz", file["filename"])
	assert.NotContains(t, out, "contents_omitted")
	// The logs card renders any .gz evidence unless the action name marks it
	// as a profile, so an unlabelled profile shows up there as a binary blob.
	info, ok := file["additional_info"].(map[string]any)
	require.True(t, ok, "the profile must be labelled or the logs card claims it")
	assert.Equal(t, "pod_profiler", info["actual_action_name"])
	assert.Equal(t, int32(1), atomic.LoadInt32(&submits))
	assert.Equal(t, int32(2), atomic.LoadInt32(&polls))
}

// TestProfileTask_Execute_ReportsAgentFailure — a failed profile has to fail the
// task with the agent's reason, not pass quietly with nothing captured.
// Test_profileDetails — the metadata the task reports sits two wrappers deep.
// Reading base64_profile directly finds only the envelope, and the task then
// reports a profile with no language, tool or size.
func Test_profileDetails(t *testing.T) {
	fileBlock := map[string]any{"lang": "go", "profile_tool": "pprof", "filename": "/tmp/agent-heapdump-1.pprof.gz"}

	t.Run("double-wrapped, as services-server returns it", func(t *testing.T) {
		status := map[string]any{"base64_profile": map[string]any{
			"data": map[string]any{"success": true, "data": fileBlock},
		}}
		assert.Equal(t, fileBlock, profileDetails(status))
	})

	t.Run("flattened by one layer", func(t *testing.T) {
		status := map[string]any{"base64_profile": map[string]any{"data": fileBlock}}
		assert.Equal(t, fileBlock, profileDetails(status))
	})

	t.Run("not wrapped at all", func(t *testing.T) {
		status := map[string]any{"base64_profile": fileBlock}
		assert.Equal(t, fileBlock, profileDetails(status))
	})

	t.Run("no profile block", func(t *testing.T) {
		assert.Nil(t, profileDetails(map[string]any{"status": "COMPLETED"}))
	})

	// A wrapper is worse than nothing: the caller reads no fields out of it and
	// reports a profile with no language, tool or bytes.
	t.Run("wrappers all the way down returns nil, not a wrapper", func(t *testing.T) {
		status := map[string]any{"base64_profile": map[string]any{
			"data": map[string]any{"success": true, "task_processing_duration": 42},
		}}
		assert.Nil(t, profileDetails(status))
	})

	// The block is identified by what the caller consumes, so it survives one
	// of the two fields going missing.
	t.Run("block without a filename is still the block", func(t *testing.T) {
		block := map[string]any{"lang": "go", "contents_base64": "AAAA"}
		status := map[string]any{"base64_profile": map[string]any{"data": map[string]any{"data": block}}}
		assert.Equal(t, block, profileDetails(status))
	})
}

// TestProfileResult_OversizedProfileIsNotInlined — a jcmd heapdump is a whole
// JVM heap; inlining it would breach Temporal's payload ceiling and kill the
// run, so it degrades to an explanation rather than silently vanishing.
func TestProfileResult_OversizedProfileIsNotInlined(t *testing.T) {
	huge := base64.StdEncoding.EncodeToString(make([]byte, maxInlineProfileBytes+1))
	status := map[string]any{"base64_profile": map[string]any{"data": map[string]any{"data": map[string]any{
		"lang": "java", "profile_tool": "jcmd", "filename": "/tmp/agent-heapdump-1.hprof.gz",
		"contents_base64": huge,
	}}}}

	out := profileResult("prof-9", map[string]any{"namespace": "shop", "pod_name": "web-0"}, status)

	assert.NotContains(t, out, "file")
	assert.Contains(t, out["contents_omitted"], "Profiler tab")
	// The metadata still has to be there — that is what the evidence table renders.
	assert.Equal(t, "jcmd", out["profile_tool"])
	assert.Equal(t, maxInlineProfileBytes+1, out["size_bytes"])
}

// profileStub serves the submit/status pair, and records what — if anything —
// was posted to /rpc/event.
func profileStub(t *testing.T, contents string) (*httptest.Server, *map[string]any) {
	t.Helper()
	var attached map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		w.Header().Set("Content-Type", "application/json")

		if r.URL.Path == "/rpc/event" {
			attached = body
			_, _ = w.Write([]byte(`{"data":{"event_id":"evt-1","added":1}}`))
			return
		}
		if body["action"].(map[string]any)["name"].(string) == profileSubmitAction {
			_, _ = w.Write([]byte(`{"data":{"profile_task_id":"prof-att","status":"TODO"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"status":"COMPLETED","base64_profile":{"data":{"data":{` +
			`"lang":"go","profile_tool":"pprof","filename":"/tmp/agent-heapdump-1-1.pprof.gz",` +
			`"contents_base64":"` + contents + `"}}}}}`))
	}))
	return server, &attached
}

// TestProfileTask_Execute_AttachesOversizedProfileToEvent is the whole point of
// event_id: a profile too large to inline still reaches the event. The 947KB Go
// heap profile that motivated this went over maxInlineProfileBytes, so `file`
// was dropped and the workflow attached an empty string.
func TestProfileTask_Execute_AttachesOversizedProfileToEvent(t *testing.T) {
	huge := base64.StdEncoding.EncodeToString(make([]byte, maxInlineProfileBytes+1))
	server, attached := profileStub(t, huge)
	defer server.Close()

	withServiceEndpoint(t, server.URL)
	withFastPolling(t)

	task := &ProfileTask{}
	taskCtx := testutils.NewTestTaskContext("tenant", "account", "user", slog.Default())
	result, err := task.Execute(taskCtx, map[string]any{
		"namespace": "shop", "pod_name": "web-0",
		"profile_type": "memory", "event_id": "evt-1",
	})
	require.NoError(t, err)

	out := result.(map[string]any)
	// The inline cap still applies to the task output — that is what rides the
	// Temporal payload, and it must not grow just because we also attached.
	assert.NotContains(t, out, "file")
	assert.Contains(t, out, "contents_omitted")
	// But the profile is on the event regardless of size.
	assert.Equal(t, true, out["evidence_attached"])
	assert.Equal(t, "evt-1", out["event_id"])

	require.NotNil(t, *attached, "an oversized profile must still reach the evidence writer")
	input := (*attached)["input"].(map[string]any)
	assert.Equal(t, "evt-1", input["event_id"])

	evidences := input["evidences"].([]any)
	require.Len(t, evidences, 1)
	evidence := evidences[0].(map[string]any)
	// The fields the Investigate page's profile card selects and reads.
	assert.Equal(t, "gz", evidence["type"])
	assert.Equal(t, "/tmp/agent-heapdump-1-1.pprof.gz", evidence["filename"])
	assert.Equal(t, huge, evidence["data"])
	// Required, or the logs card claims the profile and renders it as a log.
	assert.Equal(t, profilerActionName,
		evidence["additional_info"].(map[string]any)["actual_action_name"])

	// Scopes api-server's replace-my-own-output rule to this task, so a second
	// capture in the same workflow does not delete this one.
	assert.Equal(t, taskCtx.GetTaskID(),
		input["source_workflow"].(map[string]any)["task_id"])
}

// TestProfileTask_Execute_AttachFailureIsLoud — "captured but not attached" is
// the silent failure event_id exists to end, so it must surface as a task error.
func TestProfileTask_Execute_AttachFailureIsLoud(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		w.Header().Set("Content-Type", "application/json")

		if r.URL.Path == "/rpc/event" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"message":"event evt-gone not found"}`))
			return
		}
		if body["action"].(map[string]any)["name"].(string) == profileSubmitAction {
			_, _ = w.Write([]byte(`{"data":{"profile_task_id":"prof-4","status":"TODO"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"status":"COMPLETED","base64_profile":{"data":{"data":{` +
			`"lang":"go","profile_tool":"pprof","filename":"/tmp/x.pprof.gz","contents_base64":"AAAA"}}}}}`))
	}))
	defer server.Close()

	withServiceEndpoint(t, server.URL)
	withFastPolling(t)

	task := &ProfileTask{}
	taskCtx := testutils.NewTestTaskContext("tenant", "account", "user", slog.Default())
	_, err := task.Execute(taskCtx, map[string]any{
		"namespace": "shop", "pod_name": "web-0", "event_id": "evt-gone",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not be attached to event evt-gone")
}

// TestProfileTask_Execute_NoEventIdDoesNotAttach — without event_id the task
// behaves exactly as it did before: capture, return, attach nothing.
func TestProfileTask_Execute_NoEventIdDoesNotAttach(t *testing.T) {
	server, attached := profileStub(t, "AAAA")
	defer server.Close()

	withServiceEndpoint(t, server.URL)
	withFastPolling(t)

	task := &ProfileTask{}
	taskCtx := testutils.NewTestTaskContext("tenant", "account", "user", slog.Default())
	result, err := task.Execute(taskCtx, map[string]any{"namespace": "shop", "pod_name": "web-0"})
	require.NoError(t, err)

	out := result.(map[string]any)
	assert.NotContains(t, out, "evidence_attached")
	// Small enough to inline, so the existing output path is untouched.
	assert.Contains(t, out, "file")
	assert.Nil(t, *attached, "nothing may be attached when no event_id was given")
}

// Test_profileEvidence — both routes out of this task build the element here, so
// an attached profile renders identically to a forwarded one.
func Test_profileEvidence(t *testing.T) {
	evidence := profileEvidence("/tmp/agent-flamegraph-1.svg", "Zm9v")

	assert.Equal(t, "svg", evidence["type"])
	assert.Equal(t, "/tmp/agent-flamegraph-1.svg", evidence["filename"])
	assert.Equal(t, "Zm9v", evidence["data"])
	assert.Equal(t, profilerActionName,
		evidence["additional_info"].(map[string]any)["actual_action_name"])
}

func Test_profileFileType(t *testing.T) {
	assert.Equal(t, "gz", profileFileType("/tmp/agent-flamegraph-1.html.gz"))
	assert.Equal(t, "svg", profileFileType("/tmp/agent-flamegraph-1.svg"))
	assert.Equal(t, "gz", profileFileType(""))
}

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
