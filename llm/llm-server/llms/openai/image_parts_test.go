package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tmc/langchaingo/llms"
)

var pngBytes = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0xff}

func TestExtractToolParts_ImageBytesBecomeDataURLImage(t *testing.T) {
	t.Parallel()

	msg := &ChatMessage{MultiContent: []llms.ContentPart{
		llms.TextContent{Text: "what is this?"},
		llms.ImageURLContent{URL: "https://example.com/a.png"},
		llms.BinaryContent{MIMEType: "image/png", Data: pngBytes},
	}}

	got, calls := ExtractToolParts(msg)

	require.Len(t, got, 3)
	assert.Empty(t, calls)
	assert.Equal(t, llms.TextContent{Text: "what is this?"}, got[0])
	assert.Equal(t, llms.ImageURLContent{URL: "https://example.com/a.png"}, got[1], "a URL image is left as it was")
	assert.Equal(t, llms.ImageURLContent{URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)}, got[2])
}

// visionServer behaves like vLLM's chat endpoint for content parts: it knows text and image_url and
// answers 400 "Unknown part type" for anything else. sentImages returns the image URLs it received.
func visionServer(t *testing.T) (server *httptest.Server, sentImages func() []string) {
	t.Helper()
	var (
		mu   sync.Mutex
		sent []string
	)
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Not require: FailNow must not be called from the handler's goroutine.
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var req struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Errorf("decode request body: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for _, m := range req.Messages {
			var parts []map[string]any
			if json.Unmarshal(m.Content, &parts) != nil {
				continue // plain string content
			}
			for _, p := range parts {
				switch p["type"] {
				case "text":
				case "image_url":
					mu.Lock()
					sent = append(sent, p["image_url"].(map[string]any)["url"].(string))
					mu.Unlock()
				default:
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = fmt.Fprintf(w, `{"error":{"message":"Unknown part type: %v","type":"BadRequestError","code":400}}`, p["type"])
					return
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"a riddle"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	return server, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), sent...)
	}
}

// The failure seen in production: an attached image reached an OpenAI-compatible server as
// {"type":"binary"} and the request was rejected with a 400.
func TestGenerateContent_ImageIsSentAsImageURL(t *testing.T) {
	t.Parallel()

	server, sentImages := visionServer(t)
	defer server.Close()

	llm, err := New(WithToken("test-key"), WithBaseURL(server.URL), WithModel("test-model"))
	require.NoError(t, err)

	resp, err := llm.GenerateContent(context.Background(), []llms.MessageContent{{
		Role: llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{
			llms.TextContent{Text: "help me solve this riddle"},
			llms.BinaryContent{MIMEType: "image/png", Data: pngBytes},
		},
	}})

	require.NoError(t, err, "the server must accept the image part")
	require.Len(t, resp.Choices, 1)
	assert.Equal(t, "a riddle", resp.Choices[0].Content)
	sent := sentImages()
	require.Len(t, sent, 1)
	assert.Equal(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(pngBytes), sent[0])
}
