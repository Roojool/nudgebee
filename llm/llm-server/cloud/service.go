package cloud

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"nudgebee/llm/common"
	"nudgebee/llm/config"
)

type executeV2Response struct {
	Data   CliExecutionResult `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// ExecuteV2 runs exactly one cloud CLI process. Command exits, including
// non-zero exits and timeouts, are returned as structured results; transport,
// authentication, and executor failures are returned as errors.
func ExecuteV2(cloudCliRequest CloudExecuteCliCommandRequest) (CliExecutionResult, error) {
	headersMap := map[string]string{
		"Content-Type": "application/json",
		"Accept":       "application/json",
		"x-tenant-id":  cloudCliRequest.TenantID,
		"x-user-id":    cloudCliRequest.UserID,
	}
	if config.Config.CloudCollectorServerUrl == "" {
		return CliExecutionResult{}, errors.New("cloud: cloud collector server url not set")
	}
	if config.Config.CloudCollectorServerToken != "" {
		headersMap["X-ACTION-TOKEN"] = config.Config.CloudCollectorServerToken
	}
	resp, err := common.HttpPost(fmt.Sprintf("%s/v1/cloud/execute_cli_v2", config.Config.CloudCollectorServerUrl), common.HttpWithHeaders(headersMap), common.HttpWithJsonBody(map[string]any{
		"account_id": cloudCliRequest.AccountID,
		"command":    cloudCliRequest.Command,
	}))
	if err != nil {
		return CliExecutionResult{}, fmt.Errorf("cloud CLI executor unavailable: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.Error("cloud: failed to close v2 response body", "error", closeErr)
		}
	}()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return CliExecutionResult{}, fmt.Errorf("cloud CLI response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		message := http.StatusText(resp.StatusCode)
		var decoded executeV2Response
		if err := json.Unmarshal(body, &decoded); err == nil && len(decoded.Errors) > 0 && decoded.Errors[0].Message != "" {
			message = decoded.Errors[0].Message
		}
		return CliExecutionResult{}, fmt.Errorf("cloud CLI executor: %s", message)
	}
	var decoded executeV2Response
	if err := json.Unmarshal(body, &decoded); err != nil {
		return CliExecutionResult{}, fmt.Errorf("cloud CLI response decode: %w", err)
	}
	return decoded.Data, nil
}

func Execute(cloudCliRequest CloudExecuteCliCommandRequest) (map[string]any, error) {
	data := make(map[string]any)

	headersMap := map[string]string{
		"Content-Type": "application/json",
		"Accept":       "application/json",
	}

	if config.Config.CloudCollectorServerUrl == "" {
		return map[string]any{}, errors.New("cloud: cloud collector server url not set")
	}
	if config.Config.CloudCollectorServerToken != "" {
		headersMap["X-ACTION-TOKEN"] = config.Config.CloudCollectorServerToken
	}
	headersMap["x-tenant-id"] = cloudCliRequest.TenantID
	headersMap["x-user-id"] = cloudCliRequest.UserID

	resp, err := common.HttpPost(fmt.Sprintf("%s/v1/cloud/execute_cli", config.Config.CloudCollectorServerUrl), common.HttpWithHeaders(headersMap), common.HttpWithJsonBody(map[string]any{
		"account_id": cloudCliRequest.AccountID,
		"command":    cloudCliRequest.Command,
	}))

	if err != nil {
		slog.Error("unable to access cloud server", "error", err)
		return data, fmt.Errorf("unable to access cloud server %v", err)
	}

	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Error("cloud: failed to close response body", "error", err)
		}
	}()
	jsonBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return data, err
	}

	responseData := map[string]any{}
	err = common.UnmarshalJson(jsonBody, &responseData)
	if err != nil {
		return data, err
	}

	if resp.StatusCode != 200 {
		slog.Error("failed to fetch data from cloud", "status", resp.StatusCode, "data", string(jsonBody))
	}

	return responseData, nil
}
