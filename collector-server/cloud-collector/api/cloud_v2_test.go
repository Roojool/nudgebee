package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"nudgebee/collector/cloud/providers"
	"nudgebee/collector/cloud/security"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecuteCliV2ReturnsNonZeroExitAsProcessResult(t *testing.T) {
	gin.SetMode(gin.TestMode)
	original := executeCliCommandV2
	t.Cleanup(func() { executeCliCommandV2 = original })
	executeCliCommandV2 = func(_ *security.RequestContext, accountID, command string) (providers.CliExecutionResult, error) {
		assert.Equal(t, "account", accountID)
		assert.Equal(t, "aws s3 ls", command)
		return providers.CliExecutionResult{Stdout: "partial", Stderr: "denied", ExitCode: 7}, nil
	}

	router := gin.New()
	handleCloudProviderApis(router, nil, nil, slog.Default())
	body, err := json.Marshal(map[string]any{"account_id": "account", "command": "aws s3 ls"})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/v1/cloud/execute_cli_v2", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-tenant-id", "tenant")
	req.Header.Set("x-user-id", "user")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Data providers.CliExecutionResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, 7, response.Data.ExitCode)
	assert.Equal(t, "partial", response.Data.Stdout)
	assert.Equal(t, "denied", response.Data.Stderr)
}
