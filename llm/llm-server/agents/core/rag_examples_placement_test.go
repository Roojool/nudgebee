package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"nudgebee/llm/config"
	"nudgebee/llm/security"
	toolcore "nudgebee/llm/tools/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tmc/langchaingo/prompts"
)

// stubRAGServer answers every search with one example derived from the query,
// so different queries retrieve different examples (as rag-server does).
func stubRAGServer(t *testing.T) *int32 {
	t.Helper()
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode([]map[string]any{{"document": "example for " + req.Query}})
	}))
	t.Cleanup(server.Close)
	prev := config.Config.RAGServerUrl
	config.Config.RAGServerUrl = server.URL
	t.Cleanup(func() { config.Config.RAGServerUrl = prev })
	return &calls
}

// RAG examples depend on the query, so for the ReAct planners they must stay out
// of the system prompt (a stable cacheable prefix) and ride in the human message.
func TestRAGExamples_KeptOutOfReActSystemPrompt(t *testing.T) {
	calls := stubRAGServer(t)
	p := NBAgentPrompt{Role: "an events analyst", Rag: NBAgentPromptRag{Module: "events"}}
	render := func(q string, prompt NBAgentPrompt) string {
		s, err := GetPromptTemplate(prompt, NBAgentRequest{Query: q, AccountId: "acc-1"}, AgentPlannerTypeReAct3).Format(map[string]any{})
		require.NoError(t, err)
		return s
	}

	// The executor renders the ReAct system prompt with Rag cleared.
	systemPrompt := p
	systemPrompt.Rag = NBAgentPromptRag{}
	a, b := render("pod crashloop", systemPrompt), render("disk pressure", systemPrompt)
	assert.Equal(t, a, b, "system prompt must not vary with the query")
	assert.NotContains(t, a, "<rag_examples>")
	assert.Zero(t, atomic.LoadInt32(calls), "no RAG lookup when the examples go to the human message")

	assert.Contains(t, render("pod crashloop", p), "example for pod crashloop", "GetPromptTemplate callers keep the examples")

	examples := renderRAGExamples(p, NBAgentRequest{Query: "pod crashloop", AccountId: "acc-1"}, AgentPlannerTypeReAct3)
	assert.Contains(t, examples, "<rag_examples>")
	assert.Contains(t, examples, "example for pod crashloop")
}

func TestRAGExamples_RenderedInHumanMessage(t *testing.T) {
	const examples = "<rag_examples>\nexample for pod crashloop</rag_examples>"

	t.Run("react4", func(t *testing.T) {
		planner := &NBReActPlanner4{request: NBAgentRequest{AgentId: "a-1", AccountId: "acc-1", RAGExamples: examples}}
		assert.Contains(t, planner.humanText("why is pod-a crashing?"), examples)
	})

	t.Run("react3", func(t *testing.T) {
		req := NBAgentRequest{AgentId: "a-1", AccountId: "acc-1", RAGExamples: examples}
		tmpl, _, err := reActCreatePrompt3(security.NewRequestContextForSuperAdmin(), "agent prompt", []toolcore.NBTool{}, "", []prompts.MessageFormatter{}, req, &MockAgent{})
		require.NoError(t, err)
		value, err := tmpl.FormatPrompt(map[string]any{"input": "why is pod-a crashing?", "scratchpad": "", "notebook": ""})
		require.NoError(t, err)
		messages := value.Messages()
		assert.NotContains(t, messages[0].GetContent(), examples)
		assert.Contains(t, messages[len(messages)-1].GetContent(), examples)
	})
}
