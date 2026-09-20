package cloud

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"nudgebee/llm/config"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecuteV2PreservesProcessResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/cloud/execute_cli_v2", r.URL.Path)
		assert.Equal(t, "tenant", r.Header.Get("x-tenant-id"))
		assert.Equal(t, "user", r.Header.Get("x-user-id"))
		assert.Equal(t, "service-token", r.Header.Get("X-ACTION-TOKEN"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "account", body["account_id"])
		assert.Equal(t, "aws s3 ls", body["command"])
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"stdout": "partial", "stderr": "denied", "exit_code": 7, "timed_out": false,
		}})
	}))
	defer server.Close()

	originalURL := config.Config.CloudCollectorServerUrl
	originalToken := config.Config.CloudCollectorServerToken
	config.Config.CloudCollectorServerUrl = server.URL
	config.Config.CloudCollectorServerToken = "service-token"
	t.Cleanup(func() {
		config.Config.CloudCollectorServerUrl = originalURL
		config.Config.CloudCollectorServerToken = originalToken
	})

	result, err := ExecuteV2(CloudExecuteCliCommandRequest{
		AccountID: "account", TenantID: "tenant", UserID: "user", Command: "aws s3 ls",
	})
	require.NoError(t, err)
	assert.Equal(t, CliExecutionResult{Stdout: "partial", Stderr: "denied", ExitCode: 7}, result)
}

func TestExecuteV2ReturnsExecutorFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]any{{"message": "executor busy"}}})
	}))
	defer server.Close()
	originalURL := config.Config.CloudCollectorServerUrl
	config.Config.CloudCollectorServerUrl = server.URL
	t.Cleanup(func() { config.Config.CloudCollectorServerUrl = originalURL })

	_, err := ExecuteV2(CloudExecuteCliCommandRequest{AccountID: "a", TenantID: "t", UserID: "u", Command: "aws s3 ls"})
	require.ErrorContains(t, err, "executor busy")
}

func TestExecuteV2FallsBackToHTTPStatusForNonJSONFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream unavailable"))
	}))
	defer server.Close()
	originalURL := config.Config.CloudCollectorServerUrl
	config.Config.CloudCollectorServerUrl = server.URL
	t.Cleanup(func() { config.Config.CloudCollectorServerUrl = originalURL })

	_, err := ExecuteV2(CloudExecuteCliCommandRequest{AccountID: "a", TenantID: "t", UserID: "u", Command: "aws s3 ls"})
	require.EqualError(t, err, "cloud CLI executor: Bad Gateway")
}
