package llm

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"nudgebee/services/common"
	"nudgebee/services/config"
	"nudgebee/services/security"
	"strings"
	"time"
)

// stoppedConversationStatuses are the states a conversation ends in without ever
// producing an answer: TERMINATED is a user pressing Stop in Nubi, KILLED is the
// server reaping the run. Neither can become COMPLETED, so polling on is waiting
// for something that will not happen — the caller burned its whole retry budget
// before reporting a timeout that named nothing (#36990). Same terminal set the
// chat UI treats as final.
var stoppedConversationStatuses = map[string]bool{
	"TERMINATED": true,
	"KILLED":     true,
}

type llmAction struct {
	Name string `json:"name"`
}

type llmChatInputRequest struct {
	Query          string `json:"query,omitempty"`
	AccountId      string `json:"account_id"`
	ConversationId string `json:"conversation_id,omitempty"`
}

type llmChatInput struct {
	Request any `json:"request"`
}

type llmChatRequest struct {
	Action llmAction    `json:"action"`
	Input  llmChatInput `json:"input"`
}

type llmMessage struct {
	Id             string `json:"id"`
	ConversationId string `json:"conversation_id"`
	Response       string `json:"response"`
	Status         string `json:"status"`
}

type llmAgentResponse struct {
	Response       []string     `json:"response"`
	Query          string       `json:"query"`
	AgentName      string       `json:"agent_name"`
	ConversationId string       `json:"conversation_id"`
	Id             string       `json:"id"`
	SessionId      string       `json:"session_id"`
	Status         string       `json:"status"`
	Messages       []llmMessage `json:"llm_conversation_messages"`
}

type llmApiResponse struct {
	Data   *llmAgentResponse `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// ChatOption tunes a single ChatCompletion call. The zero set of options is the
// behaviour every existing caller relies on.
type ChatOption func(*chatConfig)

type chatConfig struct {
	onSubmitted func(conversationID string)
}

// WithOnSubmitted calls fn with the conversation id as soon as llm-server accepts
// the request and before polling starts. A caller that must survive its own
// process dying mid-run saves the id there, so another process can pick the run
// back up with WaitForConversation instead of starting a second one. It is not
// called when the answer came back inline (no conversation to poll).
func WithOnSubmitted(fn func(conversationID string)) ChatOption {
	return func(c *chatConfig) { c.onSubmitted = fn }
}

func llmRequestHeaders(sc *security.RequestContext, userID string) map[string]string {
	headers := map[string]string{
		"Content-Type": "application/json",
	}
	if sc.GetSecurityContext().GetTenantId() != "" {
		headers["x-tenant-id"] = sc.GetSecurityContext().GetTenantId()
	}
	if userID != "" {
		headers["x-user-id"] = userID
	}
	headers[config.Config.LLMServerTokenHeader] = config.Config.LLMServerToken
	return headers
}

// WaitForConversation polls an already-submitted conversation to completion. It
// is the second half of ChatCompletion, exposed so a process that did not submit
// the request can take over waiting for it. request needs ConversationId and
// AccountId; UserId is optional.
func WaitForConversation(sc *security.RequestContext, request ConversationApiRequest) (*ChatCompletionResponse, error) {
	if request.ConversationId == "" {
		return nil, errors.New("llm: conversation_id is required to wait for a conversation")
	}
	return pollConversation(sc, request.ConversationId, request.AccountId, llmRequestHeaders(sc, request.UserId))
}

// pollConversation polls chat_get until the conversation completes, stops, fails,
// or the retry budget runs out.
func pollConversation(sc *security.RequestContext, convId, accountID string, headers map[string]string) (*ChatCompletionResponse, error) {
	retryAttempts := config.Config.ServicesServerLLMRetryAttempts
	backoffSec := config.Config.ServicesServerLLMInitialBackoffSeconds
	if backoffSec <= 0 {
		backoffSec = 2
	}
	backoff := time.Duration(backoffSec) * time.Second

	for i := 0; i < retryAttempts; i++ {
		select {
		case <-sc.GetContext().Done():
			return nil, sc.GetContext().Err()
		case <-time.After(backoff):
		}

		getReq := llmChatRequest{
			Action: llmAction{Name: "chat"},
			Input: llmChatInput{
				Request: llmChatInputRequest{
					ConversationId: convId,
					AccountId:      accountID,
				},
			},
		}

		pollResp, err := common.HttpPost(fmt.Sprintf("%s/v1/completions/chat_get", config.Config.LLMServerEndpoint),
			common.HttpWithHeaders(headers),
			common.HttpWithJsonBody(getReq),
			common.HttpWithTimeout(30*time.Second),
			common.HttpWithContext(sc.GetContext()))

		if err != nil {
			sc.GetLogger().Error("llm.ChatCompletion: error in chat_get", "error", err, "attempt", i+1)
			continue
		}

		pollBody, err := io.ReadAll(pollResp.Body)
		if closeErr := pollResp.Body.Close(); closeErr != nil {
			sc.GetLogger().Warn("llm.ChatCompletion: error closing poll response body", "error", closeErr)
		}
		if err != nil {
			sc.GetLogger().Error("llm.ChatCompletion: unable to read body in chat_get", "error", err)
			continue
		}

		if pollResp.StatusCode != http.StatusOK {
			sc.GetLogger().Warn("llm.ChatCompletion: chat_get returned non-200", "status", pollResp.StatusCode, "body", string(pollBody))
			continue
		}

		var pollApiResponse llmApiResponse
		if err := common.UnmarshalJson(pollBody, &pollApiResponse); err != nil {
			sc.GetLogger().Error("llm.ChatCompletion: unable to unmarshal chat_get response", "error", err)
			continue
		}

		if pollApiResponse.Data == nil {
			sc.GetLogger().Error("llm.ChatCompletion: no data in chat_get response")
			continue
		}

		status := pollApiResponse.Data.Status
		if status == "COMPLETED" {
			finalResponse := pollApiResponse.Data.Response
			if len(finalResponse) == 0 && len(pollApiResponse.Data.Messages) > 0 {
				finalResponse = []string{pollApiResponse.Data.Messages[len(pollApiResponse.Data.Messages)-1].Response}
			}
			return &ChatCompletionResponse{
				Response:       finalResponse,
				Query:          pollApiResponse.Data.Query,
				AgentName:      pollApiResponse.Data.AgentName,
				ConversationId: convId,
				SessionId:      pollApiResponse.Data.SessionId,
				Status:         pollApiResponse.Data.Status,
			}, nil
		}

		if stoppedConversationStatuses[status] {
			return nil, fmt.Errorf("llm: conversation was %s before it answered", strings.ToLower(status))
		}

		if status == "FAILED" || status == "ERROR" {
			return nil, fmt.Errorf("llm: request failed with status: %s", status)
		}

		sc.GetLogger().Info("llm.ChatCompletion: request in progress", "conversation_id", convId, "attempt", i+1)
	}
	return nil, errors.New("llm: max retry attempts reached")
}

func ChatCompletion(sc *security.RequestContext, request ConversationApiRequest, opts ...ChatOption) (*ChatCompletionResponse, error) {
	var cfg chatConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	// Make the HTTP POST request to the llm-server
	llmServerURL := fmt.Sprintf("%s/v1/completions/chat", config.Config.LLMServerEndpoint)

	headers := llmRequestHeaders(sc, request.UserId)

	chatReq := llmChatRequest{
		Action: llmAction{Name: "chat"},
		Input:  llmChatInput{Request: request},
	}

	var resp *http.Response
	var err error
	maxRetries := 3
	retryInterval := 5 * time.Second

	for attempt := 0; attempt < maxRetries; attempt++ {
		resp, err = common.HttpPost(llmServerURL,
			common.HttpWithJsonBody(chatReq),
			common.HttpWithHeaders(headers),
			common.HttpWithTimeout(300*time.Second),
			common.HttpWithContext(sc.GetContext()),
		)

		if err != nil {
			sc.GetLogger().Error("llm.ChatCompletion: failed to send request", "error", err, "attempt", attempt+1)
			time.Sleep(retryInterval)
			continue
		}

		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted {
			defer func() {
				err := resp.Body.Close()
				if err != nil {
					sc.GetLogger().Error("llm.ChatCompletion: failed to close body", "error", err)
				}
			}()
			bodyBytes, err := io.ReadAll(resp.Body)
			if err != nil {
				return nil, fmt.Errorf("failed to read response body: %w", err)
			}

			var apiResponse llmApiResponse
			if err := common.UnmarshalJson(bodyBytes, &apiResponse); err != nil {
				return nil, fmt.Errorf("failed to unmarshal response: %w", err)
			}

			if len(apiResponse.Errors) > 0 {
				return nil, errors.New(apiResponse.Errors[0].Message)
			}

			if apiResponse.Data == nil {
				return nil, errors.New("llm: no data in response")
			}

			convId := apiResponse.Data.ConversationId
			if convId == "" {
				// if we got data immediately, return it
				status := apiResponse.Data.Status
				if status == "COMPLETED" {
					finalResponse := apiResponse.Data.Response
					if len(finalResponse) == 0 && len(apiResponse.Data.Messages) > 0 {
						finalResponse = []string{apiResponse.Data.Messages[len(apiResponse.Data.Messages)-1].Response}
					}
					return &ChatCompletionResponse{
						Response:       finalResponse,
						Query:          apiResponse.Data.Query,
						AgentName:      apiResponse.Data.AgentName,
						ConversationId: apiResponse.Data.ConversationId,
						SessionId:      apiResponse.Data.SessionId,
						Status:         status,
					}, nil
				}
				if stoppedConversationStatuses[status] {
					return nil, fmt.Errorf("llm: conversation was %s before it answered", strings.ToLower(status))
				}
				if status == "FAILED" || status == "ERROR" {
					return nil, fmt.Errorf("llm: initial request failed with status: %s", status)
				}
				return nil, errors.New("llm: conversation_id not found in initial response")
			}

			if cfg.onSubmitted != nil {
				cfg.onSubmitted(convId)
			}
			return pollConversation(sc, convId, request.AccountId, headers)
		}

		sc.GetLogger().Warn("llm.ChatCompletion: received non-OK status", "status_code", resp.StatusCode, "attempt", attempt+1)
		if closeErr := resp.Body.Close(); closeErr != nil {
			sc.GetLogger().Warn("llm.ChatCompletion: error closing response body", "error", closeErr)
		}
		time.Sleep(retryInterval)
	}

	return nil, fmt.Errorf("failed to get a successful response after %d attempts", maxRetries)
}
