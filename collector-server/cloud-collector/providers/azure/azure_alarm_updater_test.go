package azure

import (
	"testing"

	"nudgebee/collector/cloud/providers"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/monitor/armmonitor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testAlertProps(thresholds ...float64) *armmonitor.MetricAlertProperties {
	criteria := &armmonitor.MetricAlertSingleResourceMultipleMetricCriteria{}
	for _, v := range thresholds {
		criteria.AllOf = append(criteria.AllOf, &armmonitor.MetricCriteria{Threshold: to.Ptr(v)})
	}
	return &armmonitor.MetricAlertProperties{
		Criteria:   criteria,
		Enabled:    to.Ptr(true),
		WindowSize: to.Ptr("PT5M"),
	}
}

func TestAzureWindowSize(t *testing.T) {
	for minutes, want := range map[int]string{1: "PT1M", 4: "PT5M", 5: "PT5M", 20: "PT30M", 61: "PT6H", 1440: "P1D"} {
		got, err := azureWindowSize(minutes)
		require.NoError(t, err)
		assert.Equal(t, want, got, "minutes=%d", minutes)
	}
	_, err := azureWindowSize(1441)
	assert.ErrorIs(t, err, providers.ErrAlertRuleChangeUnsupported)
}

func TestApplyAzureAlertChange(t *testing.T) {
	t.Run("disable only", func(t *testing.T) {
		p := testAlertProps(80)
		off := false
		prev, err := applyAzureAlertChange(p, providers.AlertRuleChange{Enabled: &off})
		require.NoError(t, err)
		assert.False(t, *p.Enabled)
		assert.Equal(t, 80.0, *azureStaticCriteria(p.Criteria)[0].Threshold)
		assert.Equal(t, map[string]any{"enabled": true, "window_size": "PT5M", "threshold": 80.0}, prev)
	})

	t.Run("threshold and rounded window", func(t *testing.T) {
		p := testAlertProps(80, 80)
		v := 90.0
		_, err := applyAzureAlertChange(p, providers.AlertRuleChange{Threshold: &v, DurationMinutes: 10})
		require.NoError(t, err)
		for _, c := range azureStaticCriteria(p.Criteria) {
			assert.Equal(t, 90.0, *c.Threshold)
		}
		assert.Equal(t, "PT15M", *p.WindowSize)
	})

	t.Run("criteria with different thresholds are refused", func(t *testing.T) {
		p := testAlertProps(80, 95)
		v := 90.0
		_, err := applyAzureAlertChange(p, providers.AlertRuleChange{Threshold: &v})
		assert.ErrorIs(t, err, providers.ErrAlertRuleChangeUnsupported)
		assert.Equal(t, 80.0, *azureStaticCriteria(p.Criteria)[0].Threshold)
	})

	t.Run("restore", func(t *testing.T) {
		p := testAlertProps(90)
		p.Enabled = to.Ptr(false)
		p.WindowSize = to.Ptr("PT15M")
		_, err := applyAzureAlertChange(p, providers.AlertRuleChange{Restore: map[string]any{"threshold": 80, "window_size": "PT5M", "enabled": true}})
		require.NoError(t, err)
		assert.Equal(t, 80.0, *azureStaticCriteria(p.Criteria)[0].Threshold)
		assert.Equal(t, "PT5M", *p.WindowSize)
		assert.True(t, *p.Enabled)
	})

	t.Run("no static criterion", func(t *testing.T) {
		p := &armmonitor.MetricAlertProperties{Criteria: &armmonitor.MetricAlertMultipleResourceMultipleMetricCriteria{
			AllOf: []armmonitor.MultiMetricCriteriaClassification{&armmonitor.DynamicMetricCriteria{}},
		}}
		v := 1.0
		_, err := applyAzureAlertChange(p, providers.AlertRuleChange{Threshold: &v})
		assert.ErrorIs(t, err, providers.ErrAlertRuleChangeUnsupported)
	})
}
