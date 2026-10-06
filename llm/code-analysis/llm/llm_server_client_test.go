package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tmc/langchaingo/llms"
)

func TestLLMServerModelTransportsMessagesAndTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "workspace-token", r.Header.Get("X-Workspace-Token"))
		var request llmServerRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.Equal(t, "account-1", request.AccountID)
		require.Len(t, request.Messages, 1)
		require.Equal(t, "hello", request.Messages[0].Parts[0].Text)
		require.Len(t, request.Tools, 1)
		require.Equal(t, "read_file", request.Tools[0].Function.Name)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Choices":[{"Content":"ok","StopReason":"stop"}]}`))
	}))
	defer server.Close()

	model := &llmServerModel{baseURL: server.URL, token: "workspace-token", accountID: "account-1", client: server.Client()}
	response, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{llms.TextPart("hello")}},
	}, llms.WithTools([]llms.Tool{{Type: "function", Function: &llms.FunctionDefinition{Name: "read_file", Parameters: map[string]any{"type": "object"}}}}))

	require.NoError(t, err)
	require.Len(t, response.Choices, 1)
	require.Equal(t, "ok", response.Choices[0].Content)
}

// A reply with tool calls arrives exactly as llm-server writes it, the
// marshalled langchaingo response. Every call keeps its id, name and arguments;
// a call with no function stays without one, so the planner skips it.
func TestLLMServerModelKeepsToolCalls(t *testing.T) {
	reply, err := json.Marshal(llms.ContentResponse{Choices: []*llms.ContentChoice{{
		Content:    "reading both files",
		StopReason: "tool_calls",
		ToolCalls: []llms.ToolCall{
			{ID: "call-1", Type: "function", FunctionCall: &llms.FunctionCall{Name: "file_view", Arguments: `{"path":"main.go"}`}},
			{ID: "call-2", Type: "function", FunctionCall: &llms.FunctionCall{Name: "rg", Arguments: `{"pattern":"listen"}`}},
			{ID: "call-3", Type: "function"},
		},
	}}})
	require.NoError(t, err)

	// The decoder llm-server reads a request's tool_call part with.
	var sent struct {
		Messages []struct {
			Role  string `json:"role"`
			Parts []struct {
				Type     string `json:"type"`
				ToolCall struct {
					ToolCall struct {
						ID       string            `json:"id"`
						Function llms.FunctionCall `json:"function"`
					} `json:"tool_call"`
				} `json:"tool_call"`
			} `json:"parts"`
		} `json:"messages"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&sent))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(reply)
	}))
	defer server.Close()

	model := &llmServerModel{baseURL: server.URL, token: "workspace-token", accountID: "account-1", client: server.Client()}
	response, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		{Role: llms.ChatMessageTypeAI, Parts: []llms.ContentPart{
			llms.ToolCall{ID: "call-0", Type: "function", FunctionCall: &llms.FunctionCall{Name: "file_find", Arguments: `{"name":"*.go"}`}},
		}},
		{Role: llms.ChatMessageTypeTool, Parts: []llms.ContentPart{
			llms.ToolCallResponse{ToolCallID: "call-0", Name: "file_find", Content: "main.go"},
		}},
	})

	require.NoError(t, err)
	require.Len(t, response.Choices, 1)
	choice := response.Choices[0]
	require.Equal(t, "reading both files", choice.Content)
	require.Equal(t, "tool_calls", choice.StopReason)
	require.Len(t, choice.ToolCalls, 3)
	require.Equal(t, llms.ToolCall{ID: "call-1", Type: "function", FunctionCall: &llms.FunctionCall{Name: "file_view", Arguments: `{"path":"main.go"}`}}, choice.ToolCalls[0])
	require.Equal(t, llms.ToolCall{ID: "call-2", Type: "function", FunctionCall: &llms.FunctionCall{Name: "rg", Arguments: `{"pattern":"listen"}`}}, choice.ToolCalls[1])
	require.Nil(t, choice.ToolCalls[2].FunctionCall)

	require.Len(t, sent.Messages, 2)
	replayed := sent.Messages[0].Parts[0]
	require.Equal(t, "tool_call", replayed.Type)
	require.Equal(t, "call-0", replayed.ToolCall.ToolCall.ID)
	require.Equal(t, llms.FunctionCall{Name: "file_find", Arguments: `{"name":"*.go"}`}, replayed.ToolCall.ToolCall.Function)
}
