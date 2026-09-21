package llm

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"nudgebee/services/config"
	"nudgebee/services/security"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestChatCompletion_AsyncPolling(t *testing.T) {
	convID := "test-conv-123"
	sessionID := "test-sess-456"
	expectedMsg := []string{"Hello from AI"}
	callCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		callCount++

		if r.URL.Path == "/v1/completions/chat" {
			// Initial call
			resp := llmApiResponse{
				Data: &llmAgentResponse{
					ConversationId: convID,
					SessionId:      sessionID,
					Status:         "IN_PROGRESS",
					Response:       []string{"Your request has been received and will be processed asynchronously."},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		if r.URL.Path == "/v1/completions/chat_get" {
			// Polling calls
			if callCount < 4 { // 1 initial + 2 polling "IN_PROGRESS"
				resp := llmApiResponse{
					Data: &llmAgentResponse{
						ConversationId: convID,
						SessionId:      sessionID,
						Status:         "IN_PROGRESS",
					},
				}
				_ = json.NewEncoder(w).Encode(resp)
			} else {
				// Final completion
				resp := llmApiResponse{
					Data: &llmAgentResponse{
						ConversationId: convID,
						SessionId:      sessionID,
						Status:         "COMPLETED",
						Response:       expectedMsg,
						Query:          "hi",
						AgentName:      "test-agent",
						Messages: []llmMessage{
							{
								Id:             "msg-1",
								ConversationId: convID,
								Response:       expectedMsg[0],
								Status:         "COMPLETED",
							},
						},
					},
				}
				_ = json.NewEncoder(w).Encode(resp)
			}
			return
		}

		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	// Setup config
	oldURL := config.Config.LLMServerEndpoint
	config.Config.LLMServerEndpoint = server.URL
	config.Config.ServicesServerLLMRetryAttempts = 5
	config.Config.ServicesServerLLMInitialBackoffSeconds = 1
	defer func() { config.Config.LLMServerEndpoint = oldURL }()

	sc := security.NewRequestContext(context.Background(), &security.SecurityContext{}, slog.Default(), nil, nil)

	req := ConversationApiRequest{
		Query:     "hi",
		AccountId: "acc-1",
	}

	start := time.Now()
	resp, err := ChatCompletion(sc, req)
	duration := time.Since(start)

	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, expectedMsg, resp.Response)
	assert.Equal(t, "hi", resp.Query)
	assert.Equal(t, "test-agent", resp.AgentName)
	assert.Equal(t, convID, resp.ConversationId)
	assert.Equal(t, "COMPLETED", resp.Status)
	assert.True(t, duration >= 2*time.Second, "Should have polled at least twice with 1s backoff")
	assert.Equal(t, 4, callCount, "Should have 1 initial call + 3 polling calls (2 progress, 1 complete)")
}

func TestChatCompletion_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := llmApiResponse{
			Data: &llmAgentResponse{
				ConversationId: "conv-1",
				Status:         "IN_PROGRESS",
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	oldURL := config.Config.LLMServerEndpoint
	config.Config.LLMServerEndpoint = server.URL
	config.Config.ServicesServerLLMRetryAttempts = 2
	config.Config.ServicesServerLLMInitialBackoffSeconds = 1
	defer func() { config.Config.LLMServerEndpoint = oldURL }()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	sc := security.NewRequestContext(ctx, &security.SecurityContext{}, slog.Default(), nil, nil)

	req := ConversationApiRequest{
		Query:     "hi",
		AccountId: "acc-1",
	}

	_, err := ChatCompletion(sc, req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), context.DeadlineExceeded.Error())
}

func TestChatCompletion_E2E(t *testing.T) {
	if os.Getenv("TEST_TENANT") == "" || os.Getenv("TEST_USER") == "" || os.Getenv("TEST_ACCOUNT") == "" {
		t.Skip("Skipping E2E test: missing environment variables")
	}
	reqCtx := security.NewRequestContextForUserTenant(os.Getenv("TEST_USER"), os.Getenv("TEST_TENANT"), nil, nil, nil)

	req := ConversationApiRequest{
		Query:     "hi",
		AccountId: os.Getenv("TEST_ACCOUNT"),
	}

	resp, err := ChatCompletion(reqCtx, req)
	assert.NoError(t, err)
	assert.NotEmpty(t, resp.ConversationId)
	assert.NotEmpty(t, resp.SessionId)
	assert.NotEmpty(t, resp.Status)
}

// A conversation the user stopped in Nubi (TERMINATED) or the server reaped
// (KILLED) never reaches COMPLETED. Polling on until the retry budget runs out
// costs minutes and reports a timeout that names nothing — the same defect
// #36990 describes on the automation side of this call.
func TestChatCompletion_StopsWhenConversationTerminated(t *testing.T) {
	for _, status := range []string{"TERMINATED", "KILLED"} {
		t.Run(status, func(t *testing.T) {
			callCount := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				callCount++
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/v1/completions/chat" {
					_ = json.NewEncoder(w).Encode(llmApiResponse{
						Data: &llmAgentResponse{ConversationId: "conv-1", SessionId: "sess-1", Status: "IN_PROGRESS"},
					})
					return
				}
				_ = json.NewEncoder(w).Encode(llmApiResponse{
					Data: &llmAgentResponse{ConversationId: "conv-1", SessionId: "sess-1", Status: status},
				})
			}))
			defer server.Close()

			oldURL := config.Config.LLMServerEndpoint
			config.Config.LLMServerEndpoint = server.URL
			config.Config.ServicesServerLLMRetryAttempts = 10
			config.Config.ServicesServerLLMInitialBackoffSeconds = 1
			defer func() { config.Config.LLMServerEndpoint = oldURL }()

			sc := security.NewRequestContext(context.Background(), &security.SecurityContext{}, slog.Default(), nil, nil)

			_, err := ChatCompletion(sc, ConversationApiRequest{Query: "hi", AccountId: "acc-1"})

			assert.Error(t, err)
			assert.Contains(t, err.Error(), "conversation was "+strings.ToLower(status)+" before it answered")
			assert.NotContains(t, err.Error(), "max retry attempts reached")
			assert.Equal(t, 2, callCount, "one chat + one poll: the call ends on the first poll that sees the stop")
		})
	}
}

// newSubmitThenCompleteServer answers the submit call with an in-progress
// conversation and the first chat_get with COMPLETED. submits counts how many
// times a run was submitted, which is what the re-attach path must keep at zero.
func newSubmitThenCompleteServer(t *testing.T, convID string, submits *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/completions/chat":
			*submits++
			_ = json.NewEncoder(w).Encode(llmApiResponse{Data: &llmAgentResponse{ConversationId: convID, Status: "IN_PROGRESS"}})
		case "/v1/completions/chat_get":
			_ = json.NewEncoder(w).Encode(llmApiResponse{Data: &llmAgentResponse{
				ConversationId: convID, Status: "COMPLETED", Response: []string{"done"},
			}})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
}

func useTestLLMServer(t *testing.T, url string) *security.RequestContext {
	t.Helper()
	oldURL := config.Config.LLMServerEndpoint
	config.Config.LLMServerEndpoint = url
	config.Config.ServicesServerLLMRetryAttempts = 3
	config.Config.ServicesServerLLMInitialBackoffSeconds = 1
	t.Cleanup(func() { config.Config.LLMServerEndpoint = oldURL })
	return security.NewRequestContext(context.Background(), &security.SecurityContext{}, slog.Default(), nil, nil)
}

func TestChatCompletion_OnSubmittedReceivesConversationIDBeforePolling(t *testing.T) {
	submits := 0
	server := newSubmitThenCompleteServer(t, "conv-submitted", &submits)
	defer server.Close()
	sc := useTestLLMServer(t, server.URL)

	var got string
	resp, err := ChatCompletion(sc, ConversationApiRequest{Query: "hi", AccountId: "acc-1"},
		WithOnSubmitted(func(id string) { got = id }))

	assert.NoError(t, err)
	assert.Equal(t, "conv-submitted", got, "the id must reach the caller so another process can re-attach")
	assert.Equal(t, "conv-submitted", resp.ConversationId)
	assert.Equal(t, 1, submits)
}

func TestWaitForConversation_PollsWithoutSubmitting(t *testing.T) {
	submits := 0
	server := newSubmitThenCompleteServer(t, "conv-orphaned", &submits)
	defer server.Close()
	sc := useTestLLMServer(t, server.URL)

	resp, err := WaitForConversation(sc, ConversationApiRequest{ConversationId: "conv-orphaned", AccountId: "acc-1"})

	assert.NoError(t, err)
	assert.Equal(t, []string{"done"}, resp.Response)
	assert.Equal(t, 0, submits, "re-attaching must not start a second agent run")
}

func TestWaitForConversation_RequiresConversationID(t *testing.T) {
	sc := security.NewRequestContext(context.Background(), &security.SecurityContext{}, slog.Default(), nil, nil)
	_, err := WaitForConversation(sc, ConversationApiRequest{AccountId: "acc-1"})
	assert.Error(t, err)
}
