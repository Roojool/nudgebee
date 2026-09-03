package agents

import (
	"nudgebee/llm/agents/core"
	"nudgebee/llm/security"
	toolcore "nudgebee/llm/tools/core"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/tmc/langchaingo/llms"
)

// --- mock agent that satisfies core.NBAgent for unit tests ---

type mockTracesSubAgent struct {
	name string
}

// cannedResponse pairs a response with an optional error for the mock executor.
type cannedResponse struct {
	resp core.NBAgentResponse
	err  error
}

func (m *mockTracesSubAgent) GetName() string { return m.name }

func (m *mockTracesSubAgent) GetNameAliases() []string { return nil }

func (m *mockTracesSubAgent) GetDescription() string { return "mock" }

func (m *mockTracesSubAgent) GetSupportedTools(_ *security.RequestContext) []toolcore.NBTool {
	return nil
}

func (m *mockTracesSubAgent) GetSystemPrompt(_ *security.RequestContext, _ core.NBAgentRequest) core.NBAgentPrompt {
	return core.NBAgentPrompt{}
}

func (m *mockTracesSubAgent) GetPlannerType() core.AgentPlannerType {
	return core.AgentPlannerTypeReAct
}

// mockTracesExecutor returns an agentExecutorFunc that replays canned responses
// in order, one per call.
func mockTracesExecutor(responses []cannedResponse) agentExecutorFunc {
	callIdx := 0
	return func(_ toolcore.NbToolContext, _ core.NBAgent, _ toolcore.NBToolCallRequest) (core.NBAgentResponse, error) {
		if callIdx >= len(responses) {
			return core.NBAgentResponse{Status: core.ConversationStatusFailed}, nil
		}
		r := responses[callIdx]
		callIdx++
		return r.resp, r.err
	}
}

// --- unit tests for soft-completion detection ---

func TestIsTracesSoftCompletion(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"exact no traces", "no traces", true},
		{"mixed case", "No Traces found for the query", true},
		{"unable to retrieve", "Unable to retrieve trace data", true},
		{"real data", "Found 42 traces for service-x between 10:00 and 11:00", false},
		{"empty string", "", false},
		{"partial mismatch", "nothing found", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, isTracesSoftCompletion(tt.input))
		})
	}
}

// --- unit tests for the fallback Execute loop ---

func TestTracesFallbackExecute_FirstAgentCompletesWithData(t *testing.T) {
	ctx := security.NewRequestContextForSuperAdmin()
	agent := &fallbackTracesAgent{
		accountId: "test-account",
		agents:    []core.NBAgent{&mockTracesSubAgent{name: "a"}, &mockTracesSubAgent{name: "b"}},
		executor: mockTracesExecutor([]cannedResponse{
			{resp: core.NBAgentResponse{Response: []string{"Here are 5 traces"}, Status: core.ConversationStatusCompleted}},
		}),
	}

	resp, err := agent.Execute(ctx, core.NBAgentRequest{Query: "show traces"})
	assert.NoError(t, err)
	assert.Equal(t, core.ConversationStatusCompleted, resp.Status)
	assert.Contains(t, resp.Response[0], "Here are 5 traces")
}

func TestTracesFallbackExecute_SoftCompleteThenRealData(t *testing.T) {
	ctx := security.NewRequestContextForSuperAdmin()
	agent := &fallbackTracesAgent{
		accountId: "test-account",
		agents:    []core.NBAgent{&mockTracesSubAgent{name: "a"}, &mockTracesSubAgent{name: "b"}},
		executor: mockTracesExecutor([]cannedResponse{
			{resp: core.NBAgentResponse{Response: []string{"No traces found"}, Status: core.ConversationStatusCompleted}},
			{resp: core.NBAgentResponse{Response: []string{"Found 3 traces from Jaeger"}, Status: core.ConversationStatusCompleted}},
		}),
	}

	resp, err := agent.Execute(ctx, core.NBAgentRequest{Query: "show traces"})
	assert.NoError(t, err)
	assert.Equal(t, core.ConversationStatusCompleted, resp.Status)
	assert.Contains(t, resp.Response[0], "Found 3 traces")
}

func TestTracesFallbackExecute_AllAgentsSoftComplete(t *testing.T) {
	ctx := security.NewRequestContextForSuperAdmin()
	agent := &fallbackTracesAgent{
		accountId: "test-account",
		agents:    []core.NBAgent{&mockTracesSubAgent{name: "a"}, &mockTracesSubAgent{name: "b"}},
		executor: mockTracesExecutor([]cannedResponse{
			{resp: core.NBAgentResponse{Response: []string{"No traces available"}, Status: core.ConversationStatusCompleted}},
			{resp: core.NBAgentResponse{Response: []string{"Unable to retrieve traces"}, Status: core.ConversationStatusCompleted}},
		}),
	}

	resp, err := agent.Execute(ctx, core.NBAgentRequest{Query: "show traces"})
	assert.NoError(t, err)
	assert.Equal(t, core.ConversationStatusCompleted, resp.Status)
	assert.Contains(t, resp.Response[0], "Unable to retrieve traces")
}

func TestTracesFallbackExecute_AllAgentsFail(t *testing.T) {
	ctx := security.NewRequestContextForSuperAdmin()
	agent := &fallbackTracesAgent{
		accountId: "test-account",
		agents:    []core.NBAgent{&mockTracesSubAgent{name: "clickhouse"}, &mockTracesSubAgent{name: "jaeger"}},
		executor: mockTracesExecutor([]cannedResponse{
			{resp: core.NBAgentResponse{Response: []string{"timeout"}, Status: core.ConversationStatusFailed}},
			{resp: core.NBAgentResponse{Response: []string{"auth error"}, Status: core.ConversationStatusFailed}},
		}),
	}

	resp, err := agent.Execute(ctx, core.NBAgentRequest{Query: "show traces"})
	assert.NoError(t, err)
	assert.Equal(t, core.ConversationStatusFailed, resp.Status)
	assert.Contains(t, resp.Response[0], "All traces agents failed")
	assert.Contains(t, resp.Response[0], "clickhouse Agent Effort")
	assert.Contains(t, resp.Response[0], "jaeger Agent Effort")
}

func TestTracesFallbackExecute_WaitingPropagatedImmediately(t *testing.T) {
	ctx := security.NewRequestContextForSuperAdmin()
	agent := &fallbackTracesAgent{
		accountId: "test-account",
		agents:    []core.NBAgent{&mockTracesSubAgent{name: "a"}, &mockTracesSubAgent{name: "b"}},
		executor: mockTracesExecutor([]cannedResponse{
			{
				resp: core.NBAgentResponse{Response: []string{"waiting for confirmation"}, Status: core.ConversationStatusWaiting},
				err:  assert.AnError, // transient error should be discarded
			},
		}),
	}

	resp, err := agent.Execute(ctx, core.NBAgentRequest{Query: "show traces"})
	assert.NoError(t, err, "Waiting status should discard transient errors")
	assert.Equal(t, core.ConversationStatusWaiting, resp.Status)
}

func TestTracesFallbackExecute_CompletedEmptyResponseTreatedAsSoftCompletion(t *testing.T) {
	ctx := security.NewRequestContextForSuperAdmin()
	agent := &fallbackTracesAgent{
		accountId: "test-account",
		agents:    []core.NBAgent{&mockTracesSubAgent{name: "a"}},
		executor: mockTracesExecutor([]cannedResponse{
			{resp: core.NBAgentResponse{Response: []string{}, Status: core.ConversationStatusCompleted}},
		}),
	}

	resp, err := agent.Execute(ctx, core.NBAgentRequest{Query: "show traces"})
	assert.NoError(t, err)
	assert.Equal(t, core.ConversationStatusCompleted, resp.Status)
	assert.Contains(t, resp.Response[0], "No traces were found")
}

// --- unit tests for the executed query/provider recovered on an empty result (#37406) ---

// traceToolInvocation builds a tool observation carrying the envelope tracePayloadForResponse
// produces, which is what the fallback agent mines for the query and provider.
func traceToolInvocation(content string) core.ToolInvocation {
	return core.ToolInvocation{Response: llms.ToolCallResponse{Name: "traces_execute_v2", Content: content}}
}

func TestExecutedTraceQueryProvider(t *testing.T) {
	t.Run("reads the query and provider out of the envelope", func(t *testing.T) {
		query, provider := executedTraceQueryProvider([]core.ToolInvocation{
			traceToolInvocation(`{"traces":[],"metadata":{"query":"{namespace=\"nope\"}","provider":"jaeger"}}`),
		})
		assert.Equal(t, `{namespace="nope"}`, query)
		assert.Equal(t, "jaeger", provider)
	})

	t.Run("the newest trace observation wins", func(t *testing.T) {
		query, provider := executedTraceQueryProvider([]core.ToolInvocation{
			traceToolInvocation(`{"traces":[],"metadata":{"query":"first","provider":"clickhouse"}}`),
			traceToolInvocation(`{"traces":[],"metadata":{"query":"second","provider":"jaeger"}}`),
		})
		assert.Equal(t, "second", query)
		assert.Equal(t, "jaeger", provider)
	})

	t.Run("observations without the envelope are skipped", func(t *testing.T) {
		query, provider := executedTraceQueryProvider([]core.ToolInvocation{
			traceToolInvocation(`{"traces":[],"metadata":{"query":"real","provider":"clickhouse"}}`),
			traceToolInvocation(`not json at all`),
			traceToolInvocation(`{"skills":["k8s"]}`),
		})
		assert.Equal(t, "real", query)
		assert.Equal(t, "clickhouse", provider)
	})

	t.Run("nothing to recover", func(t *testing.T) {
		query, provider := executedTraceQueryProvider(nil)
		assert.Empty(t, query)
		assert.Empty(t, provider)
	})
}

func TestAppendExecutedTraceContext(t *testing.T) {
	t.Run("names the provider and the query", func(t *testing.T) {
		got := appendExecutedTraceContext("No traces were found for the specified criteria.", []core.ToolInvocation{
			traceToolInvocation(`{"traces":[],"metadata":{"query":"{namespace=\"nope\"}","provider":"jaeger"}}`),
		})
		assert.Contains(t, got, "No traces were found for the specified criteria.")
		assert.Contains(t, got, "provider: jaeger")
		assert.Contains(t, got, `executed query: {namespace="nope"}`)
	})

	t.Run("keeps the message untouched when nothing was recovered", func(t *testing.T) {
		const msg = "No traces were found for the specified criteria."
		assert.Equal(t, msg, appendExecutedTraceContext(msg, []core.ToolInvocation{
			traceToolInvocation(`[{"service_name":"checkout"}]`),
		}))
	})

	t.Run("names the provider alone when the query is absent", func(t *testing.T) {
		got := appendExecutedTraceContext("No traces found.", []core.ToolInvocation{
			traceToolInvocation(`{"traces":[],"metadata":{"provider":"clickhouse"}}`),
		})
		assert.Equal(t, "No traces found. (provider: clickhouse)", got)
	})

	t.Run("echoes a long query verbatim", func(t *testing.T) {
		long := strings.Repeat("x", 4000)
		got := appendExecutedTraceContext("No traces found.", []core.ToolInvocation{
			traceToolInvocation(`{"traces":[],"metadata":{"query":"` + long + `","provider":"clickhouse"}}`),
		})
		assert.Contains(t, got, "executed query: "+long, "the query must be pasteable, so it is never clipped")
		assert.NotContains(t, got, "(truncated)")
	})

	t.Run("keeps a multi-byte query intact", func(t *testing.T) {
		long := strings.Repeat("é", 2000)
		got := appendExecutedTraceContext("No traces found.", []core.ToolInvocation{
			traceToolInvocation(`{"traces":[],"metadata":{"query":"` + long + `","provider":"clickhouse"}}`),
		})
		assert.Contains(t, got, "executed query: "+long)
		assert.True(t, utf8.ValidString(got))
	})
}

// The ticket's case: every sub-agent came up empty, so the user gets the collapsed message —
// which must still say which query ran against which backend.
func TestTracesFallbackExecute_AllSoftCompleteNamesQueryAndProvider(t *testing.T) {
	ctx := security.NewRequestContextForSuperAdmin()
	steps := []core.ToolInvocation{
		traceToolInvocation(`{"traces":[],"metadata":{"query":"SELECT * FROM otel_traces WHERE ServiceNamespace = 'nope'","provider":"clickhouse"}}`),
	}
	agent := &fallbackTracesAgent{
		accountId: "test-account",
		agents:    []core.NBAgent{&mockTracesSubAgent{name: "a"}},
		executor: mockTracesExecutor([]cannedResponse{
			{resp: core.NBAgentResponse{
				Response:          []string{"No traces were found for the specified criteria."},
				Status:            core.ConversationStatusCompleted,
				AgentStepResponse: steps,
			}},
		}),
	}

	resp, err := agent.Execute(ctx, core.NBAgentRequest{Query: "show traces"})
	assert.NoError(t, err)
	assert.Equal(t, core.ConversationStatusCompleted, resp.Status, "an empty result stays a soft completion, not a failure")
	assert.Contains(t, resp.Response[0], "No traces were found")
	assert.Contains(t, resp.Response[0], "provider: clickhouse")
	assert.Contains(t, resp.Response[0], "ServiceNamespace = 'nope'")
}

func TestTracesFallbackExecute_EmptyResponsePlaceholderNamesQueryAndProvider(t *testing.T) {
	ctx := security.NewRequestContextForSuperAdmin()
	agent := &fallbackTracesAgent{
		accountId: "test-account",
		agents:    []core.NBAgent{&mockTracesSubAgent{name: "a"}},
		executor: mockTracesExecutor([]cannedResponse{
			{resp: core.NBAgentResponse{
				Response:          []string{},
				Status:            core.ConversationStatusCompleted,
				AgentStepResponse: []core.ToolInvocation{traceToolInvocation(`{"traces":[],"metadata":{"query":"{service=\"x\"}","provider":"jaeger"}}`)},
			}},
		}),
	}

	resp, err := agent.Execute(ctx, core.NBAgentRequest{Query: "show traces"})
	assert.NoError(t, err)
	assert.Equal(t, core.ConversationStatusCompleted, resp.Status)
	assert.Contains(t, resp.Response[0], "No traces were found")
	assert.Contains(t, resp.Response[0], "provider: jaeger")
}

// Without recoverable metadata (an older services-server, or the Datadog / cloud-CLI paths) the
// message must read exactly as it did before.
func TestTracesFallbackExecute_AllSoftCompleteWithoutMetadataIsUnchanged(t *testing.T) {
	ctx := security.NewRequestContextForSuperAdmin()
	agent := &fallbackTracesAgent{
		accountId: "test-account",
		agents:    []core.NBAgent{&mockTracesSubAgent{name: "a"}},
		executor: mockTracesExecutor([]cannedResponse{
			{resp: core.NBAgentResponse{
				Response:          []string{"No traces were found for the specified criteria."},
				Status:            core.ConversationStatusCompleted,
				AgentStepResponse: []core.ToolInvocation{traceToolInvocation(`[]`)},
			}},
		}),
	}

	resp, err := agent.Execute(ctx, core.NBAgentRequest{Query: "show traces"})
	assert.NoError(t, err)
	assert.Equal(t, "No traces were found for the specified criteria.", resp.Response[0])
}

// A run that found data must not gain the annotation — it returns before the collapse branch.
func TestTracesFallbackExecute_RealDataIsNotAnnotated(t *testing.T) {
	ctx := security.NewRequestContextForSuperAdmin()
	agent := &fallbackTracesAgent{
		accountId: "test-account",
		agents:    []core.NBAgent{&mockTracesSubAgent{name: "a"}},
		executor: mockTracesExecutor([]cannedResponse{
			{resp: core.NBAgentResponse{
				Response:          []string{"Found 3 traces for checkout."},
				Status:            core.ConversationStatusCompleted,
				AgentStepResponse: []core.ToolInvocation{traceToolInvocation(`{"traces":[{"service_name":"checkout"}],"metadata":{"query":"q","provider":"jaeger"}}`)},
			}},
		}),
	}

	resp, err := agent.Execute(ctx, core.NBAgentRequest{Query: "show traces"})
	assert.NoError(t, err)
	assert.Equal(t, "Found 3 traces for checkout.", resp.Response[0])
}

// An outright failure must stay a failure: the annotation is only for clean empty results, so a
// provider error still surfaces as the error rather than as attributable no-data (#36948).
func TestTracesFallbackExecute_FailureIsNotAnnotated(t *testing.T) {
	ctx := security.NewRequestContextForSuperAdmin()
	agent := &fallbackTracesAgent{
		accountId: "test-account",
		agents:    []core.NBAgent{&mockTracesSubAgent{name: "a"}},
		executor: mockTracesExecutor([]cannedResponse{
			{resp: core.NBAgentResponse{
				Response:          []string{"clickhouse: connection refused"},
				Status:            core.ConversationStatusFailed,
				AgentStepResponse: []core.ToolInvocation{traceToolInvocation(`{"traces":[],"metadata":{"query":"q","provider":"clickhouse"}}`)},
			}},
		}),
	}

	resp, err := agent.Execute(ctx, core.NBAgentRequest{Query: "show traces"})
	assert.NoError(t, err)
	assert.Equal(t, core.ConversationStatusFailed, resp.Status)
	assert.NotContains(t, resp.Response[0], "provider: clickhouse")
}
