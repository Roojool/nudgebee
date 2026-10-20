package account

import (
	"testing"

	"nudgebee/collector/cloud/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildEventRuleRows(t *testing.T) {
	rules := []providers.EventRule{
		{Name: "cpu-high", Source: "AWS_CloudWatch_Alarm", ExternalID: "arn:aws:cloudwatch:us-east-1:1:alarm:cpu-high", Disabled: true},
		{Name: "cpu-high", Source: "AWS_CloudWatch_Alarm", ExternalID: "arn:aws:cloudwatch:eu-west-1:1:alarm:cpu-high"},
		{Name: "disk-full", Source: "GCP_Metric_Alert", ExternalID: "projects/p/alertPolicies/7", Disabled: true},
		{Name: "no-id", Source: "Azure_Monitor_Alert"},
		// Same name, different source: a different row.
		{Name: "cpu-high", Source: "Azure_Monitor_Alert", ExternalID: "/subscriptions/s/x"},
	}
	rows, errs := buildEventRuleRows(rules, "acc", "ten", "now")
	require.Empty(t, errs)
	require.Len(t, rows, 4)

	byKey := map[string]map[string]any{}
	for _, r := range rows {
		byKey[r["source"].(string)+"/"+r["alert"].(string)] = r
		assert.Equal(t, "provider", r["origin"])
		assert.Equal(t, "acc", r["account_id"])
		assert.Equal(t, "ten", r["tenant_id"])
	}

	collapsed := byKey["AWS_CloudWatch_Alarm/cpu-high"]
	assert.Equal(t, true, collapsed["ambiguous"])
	assert.Nil(t, collapsed["external_rule_id"], "an ambiguous row must not point at one arbitrary alarm")
	assert.Equal(t, true, collapsed["enabled"], "live if any collapsed alarm is live")

	disabled := byKey["GCP_Metric_Alert/disk-full"]
	assert.Equal(t, false, disabled["enabled"])
	assert.Equal(t, false, disabled["ambiguous"])
	assert.Equal(t, "projects/p/alertPolicies/7", disabled["external_rule_id"])

	assert.Nil(t, byKey["Azure_Monitor_Alert/no-id"]["external_rule_id"])
	assert.Equal(t, "/subscriptions/s/x", byKey["Azure_Monitor_Alert/cpu-high"]["external_rule_id"])
}

func TestUpsertEventRulesSQL(t *testing.T) {
	assert.Contains(t, upsertEventRulesSQL, "enabled = excluded.enabled")
	assert.Contains(t, upsertEventRulesSQL, "ambiguous = excluded.ambiguous")
	assert.NotContains(t, upsertEventRulesLegacySQL, "ambiguous")
	assert.NotContains(t, upsertEventRulesLegacySQL, "origin")
	assert.Contains(t, upsertEventRulesLegacySQL, "external_rule_id = excluded.external_rule_id")
}

func TestAlertRuleChangeFromRequest(t *testing.T) {
	// Duration-only suggestions send no threshold; that is a valid change.
	c := alertRuleChangeFromRequest(UpdateAlertRuleRequest{ProviderConfig: map[string]interface{}{"duration_minutes": 10.0}})
	assert.Nil(t, c.Threshold)
	assert.Equal(t, 10, c.DurationMinutes)
	assert.False(t, c.IsEmpty())

	// enabled=true alone (what every threshold edit used to send) changes nothing.
	assert.True(t, alertRuleChangeFromRequest(UpdateAlertRuleRequest{Enabled: true}).IsEmpty())

	off := false
	c = alertRuleChangeFromRequest(UpdateAlertRuleRequest{SetEnabled: &off, ProviderConfig: map[string]interface{}{"threshold": "5"}})
	require.NotNil(t, c.Threshold)
	assert.Equal(t, 5.0, *c.Threshold)
	assert.False(t, *c.Enabled)
}

func TestAlertRuleChangeValidate(t *testing.T) {
	assert.ErrorIs(t, providers.AlertRuleChange{}.Validate(), providers.ErrAlertRuleChangeUnsupported)
	v := 95.0
	restore := map[string]any{"threshold": 80.0}
	// A restore with an explicit value would silently drop one of them.
	assert.ErrorIs(t, providers.AlertRuleChange{Threshold: &v, Restore: restore}.Validate(), providers.ErrAlertRuleChangeUnsupported)
	assert.ErrorIs(t, providers.AlertRuleChange{DurationMinutes: 5, Restore: restore}.Validate(), providers.ErrAlertRuleChangeUnsupported)
	assert.NoError(t, providers.AlertRuleChange{Restore: restore}.Validate())
	assert.NoError(t, providers.AlertRuleChange{Threshold: &v, DurationMinutes: 5}.Validate())
}
