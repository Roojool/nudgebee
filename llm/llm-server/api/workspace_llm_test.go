package api

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tmc/langchaingo/llms"
)

// workspaceWirePart is a message part as a workspace encodes it: langchaingo's
// own JSON for the tool call and the tool result.
type workspaceWirePart struct {
	Type         string                 `json:"type"`
	Text         string                 `json:"text,omitempty"`
	ToolCall     *llms.ToolCall         `json:"tool_call,omitempty"`
	ToolResponse *llms.ToolCallResponse `json:"tool_response,omitempty"`
}

type workspaceWireMessage struct {
	Role  string              `json:"role"`
	Parts []workspaceWirePart `json:"parts"`
}

func decodeWorkspaceWire(t *testing.T, wire []workspaceWireMessage) []workspaceLLMMessage {
	t.Helper()
	body, err := json.Marshal(map[string]any{"account_id": "acct-1", "messages": wire})
	require.NoError(t, err)
	var req workspaceLLMRequest
	require.NoError(t, json.Unmarshal(body, &req))
	return req.Messages
}

// A step's history reaches the provider client intact: each tool call keeps its
// id, name and arguments, and each tool result is its own tool message.
func TestWorkspaceLLMMessagesKeepToolCallsAndSplitResults(t *testing.T) {
	viewCall := llms.ToolCall{ID: "call-1", Type: "function", FunctionCall: &llms.FunctionCall{Name: "file_view", Arguments: `{"path":"main.go"}`}}
	searchCall := llms.ToolCall{ID: "call-2", Type: "function", FunctionCall: &llms.FunctionCall{Name: "rg", Arguments: `{"pattern":"listen"}`}}
	viewResult := llms.ToolCallResponse{ToolCallID: "call-1", Name: "file_view", Content: "package main"}
	searchResult := llms.ToolCallResponse{ToolCallID: "call-2", Name: "rg", Content: "main.go:12"}
	lastResult := llms.ToolCallResponse{ToolCallID: "call-3", Name: "file_find", Content: "main.go"}

	messages, err := workspaceLLMMessages(decodeWorkspaceWire(t, []workspaceWireMessage{
		{Role: "human", Parts: []workspaceWirePart{{Type: "text", Text: "where does it listen?"}}},
		{Role: "ai", Parts: []workspaceWirePart{
			{Type: "text", Text: "reading both"},
			{Type: "tool_call", ToolCall: &viewCall},
			{Type: "tool_call", ToolCall: &searchCall},
		}},
		{Role: "tool", Parts: []workspaceWirePart{
			{Type: "tool_response", ToolResponse: &viewResult},
			{Type: "tool_response", ToolResponse: &searchResult},
		}},
		{Role: "tool", Parts: []workspaceWirePart{{Type: "tool_response", ToolResponse: &lastResult}}},
	}))

	require.NoError(t, err)
	assert.Equal(t, []llms.MessageContent{
		{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{llms.TextPart("where does it listen?")}},
		{Role: llms.ChatMessageTypeAI, Parts: []llms.ContentPart{llms.TextPart("reading both"), viewCall, searchCall}},
		{Role: llms.ChatMessageTypeTool, Parts: []llms.ContentPart{viewResult}},
		{Role: llms.ChatMessageTypeTool, Parts: []llms.ContentPart{searchResult}},
		{Role: llms.ChatMessageTypeTool, Parts: []llms.ContentPart{lastResult}},
	}, messages)
}

func TestWorkspaceLLMMessagesRejectMalformedParts(t *testing.T) {
	tests := []struct {
		name    string
		part    workspaceWirePart
		wantErr string
	}{
		{name: "tool call without a function", part: workspaceWirePart{Type: "tool_call", ToolCall: &llms.ToolCall{ID: "call-1", Type: "function"}}, wantErr: "tool_call part is missing its function"},
		{name: "tool call part without a tool call", part: workspaceWirePart{Type: "tool_call"}, wantErr: "tool_call part is missing its function"},
		{name: "tool response part without a response", part: workspaceWirePart{Type: "tool_response"}, wantErr: "tool_response part is missing tool_response"},
		{name: "unknown part type", part: workspaceWirePart{Type: "image"}, wantErr: `unsupported message part type "image"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := workspaceLLMMessages(decodeWorkspaceWire(t, []workspaceWireMessage{{Role: "ai", Parts: []workspaceWirePart{tt.part}}}))

			assert.EqualError(t, err, tt.wantErr)
		})
	}
}
