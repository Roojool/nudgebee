package alertrule

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"nudgebee/services/config"
	"nudgebee/services/security"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The enable switch and the revert values must reach cloud-collector as their own
// fields, and the replaced values must come back to the caller.
func TestCloudAlertRuleSource_UpdateSendsSwitchAndRestore(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/cloud/update_alert_rule", r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &got))
		_, _ = w.Write([]byte(`{"data":{"external_rule_id":"projects/p/alertPolicies/1","status":"updated","previous":{"enabled":true,"threshold":80}},"errors":[]}`))
	}))
	defer srv.Close()
	prevURL := config.Config.CloudCollectorServerUrl
	config.Config.CloudCollectorServerUrl = srv.URL
	defer func() { config.Config.CloudCollectorServerUrl = prevURL }()

	ctx := security.NewRequestContext(context.Background(), &security.SecurityContext{}, slog.Default(), nil, nil)
	off := false
	res, err := (&CloudAlertRuleSource{}).UpdateAlertRule(ctx, "projects/p/alertPolicies/1", AlertRuleConfig{
		AccountId:  "acc",
		Enabled:    true,
		SetEnabled: &off,
		Restore:    map[string]any{"threshold": 80.0},
	})
	require.NoError(t, err)
	assert.Equal(t, false, got["set_enabled"])
	assert.Equal(t, map[string]any{"threshold": 80.0}, got["restore"])
	assert.Equal(t, map[string]any{"enabled": true, "threshold": 80.0}, res.Previous)
}

func TestCloudAlertRuleSource_UpdateOmitsUnsetFields(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &got))
		// An older collector returns no previous values.
		_, _ = w.Write([]byte(`{"data":{"status":"updated"}}`))
	}))
	defer srv.Close()
	prevURL := config.Config.CloudCollectorServerUrl
	config.Config.CloudCollectorServerUrl = srv.URL
	defer func() { config.Config.CloudCollectorServerUrl = prevURL }()

	ctx := security.NewRequestContext(context.Background(), &security.SecurityContext{}, slog.Default(), nil, nil)
	res, err := (&CloudAlertRuleSource{}).UpdateAlertRule(ctx, "arn:aws:cloudwatch:us-east-1:1:alarm:A", AlertRuleConfig{
		ProviderConfig: map[string]interface{}{"threshold": 5.0},
	})
	require.NoError(t, err)
	assert.NotContains(t, got, "set_enabled")
	assert.NotContains(t, got, "restore")
	assert.Nil(t, res.Previous)
}
