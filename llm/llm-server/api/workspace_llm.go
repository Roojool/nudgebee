package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/tmc/langchaingo/llms"
	"nudgebee/llm/agents"
	"nudgebee/llm/agents/core"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// workspaceLLMRequest is deliberately provider-neutral. Workspace processes
// send the same message/tool representation regardless of the configured
// provider; llm-server owns provider resolution, caching, retries and usage.
type workspaceLLMRequest struct {
	AccountID      string                `json:"account_id"`
	UserID         string                `json:"user_id"`
	ConversationID string                `json:"conversation_id"`
	MessageID      string                `json:"message_id"`
	Messages       []workspaceLLMMessage `json:"messages"`
	Tools          []workspaceLLMTool    `json:"tools,omitempty"`
}

type workspaceLLMMessage struct {
	Role  string                    `json:"role"`
	Parts []workspaceLLMMessagePart `json:"parts"`
}

type workspaceLLMMessagePart struct {
	Type         string                 `json:"type"`
	Text         string                 `json:"text,omitempty"`
	ToolCall     *workspaceLLMToolCall  `json:"tool_call,omitempty"`
	ToolResponse *llms.ToolCallResponse `json:"tool_response,omitempty"`
}

// workspaceLLMToolCall is a tool call as a workspace sends it, in langchaingo's
// JSON envelope: {"type":"tool_call","tool_call":{"id":…,"type":…,"function":{…}}}.
// It is decoded here rather than by langchaingo, whose ToolCall decoder drops
// the function name and arguments.
type workspaceLLMToolCall struct {
	ToolCall struct {
		ID       string             `json:"id"`
		Type     string             `json:"type"`
		Function *llms.FunctionCall `json:"function"`
	} `json:"tool_call"`
}

type workspaceLLMTool struct {
	Type     string               `json:"type"`
	Function workspaceLLMFunction `json:"function"`
}

type workspaceLLMFunction struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters,omitempty"`
}

func handleWorkspaceLLM(r *gin.Engine, tracer trace.Tracer, meter metric.Meter) {
	r.Group("/api/v1/workspace").POST("/llm/generate", func(c *gin.Context) {
		var req workspaceLLMRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if req.AccountID == "" || len(req.Messages) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "account_id and messages are required"})
			return
		}

		ctx, err := authorizeWorkspaceRequest(c, req.AccountID, tracer, meter)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
			return
		}

		messages, err := workspaceLLMMessages(req.Messages)
		if err != nil {
			returnBadWorkspaceLLM(c, err.Error())
			return
		}

		tools := make([]llms.Tool, 0, len(req.Tools))
		for _, tool := range req.Tools {
			tools = append(tools, llms.Tool{Type: tool.Type, Function: &llms.FunctionDefinition{
				Name: tool.Function.Name, Description: tool.Function.Description, Parameters: tool.Function.Parameters,
			}})
		}
		options := make([]llms.CallOption, 0, 1)
		if len(tools) > 0 {
			options = append(options, llms.WithTools(tools))
		}
		response, err := core.GenerateAndTrackLLMContent(ctx, req.UserID, req.AccountID, req.ConversationID, req.MessageID, agents.AgentCodeAnalyzer, false, messages, false, options...)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, response)
	})
}

// workspaceLLMMessages converts a workspace's messages into the form the
// provider clients take. A workspace sends all tool results of one step in a
// single tool message; each result becomes its own tool message here, the
// layout llm-server's own planner sends, because the OpenAI-compatible clients
// accept exactly one result per tool message.
func workspaceLLMMessages(wire []workspaceLLMMessage) ([]llms.MessageContent, error) {
	messages := make([]llms.MessageContent, 0, len(wire))
	for _, message := range wire {
		role := llms.ChatMessageType(message.Role)
		parts := make([]llms.ContentPart, 0, len(message.Parts))
		for _, part := range message.Parts {
			switch part.Type {
			case "text":
				parts = append(parts, llms.TextPart(part.Text))
			case "tool_call":
				if part.ToolCall == nil || part.ToolCall.ToolCall.Function == nil {
					return nil, errors.New("tool_call part is missing its function")
				}
				call := part.ToolCall.ToolCall
				parts = append(parts, llms.ToolCall{ID: call.ID, Type: call.Type, FunctionCall: call.Function})
			case "tool_response":
				if part.ToolResponse == nil {
					return nil, errors.New("tool_response part is missing tool_response")
				}
				parts = append(parts, *part.ToolResponse)
			default:
				return nil, fmt.Errorf("unsupported message part type %q", part.Type)
			}
		}
		if role == llms.ChatMessageTypeTool && len(parts) > 1 {
			for _, part := range parts {
				messages = append(messages, llms.MessageContent{Role: role, Parts: []llms.ContentPart{part}})
			}
			continue
		}
		messages = append(messages, llms.MessageContent{Role: role, Parts: parts})
	}
	return messages, nil
}

func returnBadWorkspaceLLM(c *gin.Context, message string) {
	c.JSON(http.StatusBadRequest, gin.H{"error": message})
}
