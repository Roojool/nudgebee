package azureclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"nudgebee/llm/common"
)

const (
	defaultEmbeddingModel = "text-embedding-ada-002"
)

type embeddingPayload struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingResponsePayload struct {
	Object string `json:"object"`
	Data   []struct {
		Object    string    `json:"object"`
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Model string `json:"model"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

// nolint:lll
func (c *Client) createEmbedding(ctx context.Context, payload *embeddingPayload) (*embeddingResponsePayload, error) {
	if c.baseURL == "" {
		return nil, errors.New("base URL is empty")
	}
	if c.Model == "" {
		payload.Model = c.EmbeddingModel
	}
	if payload.Model == "" {
		payload.Model = defaultEmbeddingModel
	}

	payloadBytes, err := common.MarshalJson(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.buildURL("/embeddings"), bytes.NewReader(payloadBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	c.setHeaders(req)

	r, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer func() {
		if err := r.Body.Close(); err != nil {
			// Log the error, but don't return it as it's a defer call
			fmt.Printf("Error closing response body: %v\n", err)
		}
	}()

	if r.StatusCode != http.StatusOK {
		// Decode is best-effort: on failure the error carries the status code
		// alone. Either way the response headers ride along, so a 429 records
		// whether the limit was on requests or tokens and how much was left.
		var errResp errorMessage
		if err := json.NewDecoder(r.Body).Decode(&errResp); err != nil {
			return nil, common.NewProviderHTTPError(r.StatusCode, r.Header, "")
		}

		return nil, common.NewProviderHTTPError(r.StatusCode, r.Header, errResp.Error.Message)
	}

	var response embeddingResponsePayload

	if err := json.NewDecoder(r.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &response, nil
}
