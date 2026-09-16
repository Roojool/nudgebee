package gcloud

import (
	"testing"
	"time"

	"nudgebee/collector/cloud/providers"

	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func thresholdCondition(value float64, d time.Duration) *monitoringpb.AlertPolicy_Condition {
	return &monitoringpb.AlertPolicy_Condition{
		Condition: &monitoringpb.AlertPolicy_Condition_ConditionThreshold{
			ConditionThreshold: &monitoringpb.AlertPolicy_Condition_MetricThreshold{
				ThresholdValue: value,
				Duration:       durationpb.New(d),
			},
		},
	}
}

func testPolicy(conds ...*monitoringpb.AlertPolicy_Condition) *monitoringpb.AlertPolicy {
	return &monitoringpb.AlertPolicy{Conditions: conds, Enabled: wrapperspb.Bool(true)}
}

func TestApplyGCPPolicyChange(t *testing.T) {
	t.Run("disable touches only the enabled field", func(t *testing.T) {
		p := testPolicy(thresholdCondition(80, time.Minute))
		off := false
		prev, paths, err := applyGCPPolicyChange(p, providers.AlertRuleChange{Enabled: &off})
		require.NoError(t, err)
		assert.Equal(t, []string{"enabled"}, paths)
		assert.False(t, p.GetEnabled().GetValue())
		assert.Equal(t, true, prev["enabled"])
		assert.Equal(t, 80.0, p.GetConditions()[0].GetConditionThreshold().GetThresholdValue())
	})

	t.Run("threshold and duration on agreeing conditions", func(t *testing.T) {
		p := testPolicy(thresholdCondition(80, time.Minute), thresholdCondition(80, time.Minute))
		v := 90.0
		prev, paths, err := applyGCPPolicyChange(p, providers.AlertRuleChange{Threshold: &v, DurationMinutes: 5})
		require.NoError(t, err)
		assert.Equal(t, []string{"conditions"}, paths)
		for _, c := range p.GetConditions() {
			assert.Equal(t, 90.0, c.GetConditionThreshold().GetThresholdValue())
			assert.Equal(t, 5*time.Minute, c.GetConditionThreshold().GetDuration().AsDuration())
		}
		assert.Equal(t, 80.0, prev["threshold"])
		assert.Equal(t, 60.0, prev["duration_seconds"])
	})

	t.Run("conditions with different thresholds are refused", func(t *testing.T) {
		p := testPolicy(thresholdCondition(80, time.Minute), thresholdCondition(95, time.Minute))
		v := 90.0
		_, _, err := applyGCPPolicyChange(p, providers.AlertRuleChange{Threshold: &v})
		assert.ErrorIs(t, err, providers.ErrAlertRuleChangeUnsupported)
		assert.Equal(t, 80.0, p.GetConditions()[0].GetConditionThreshold().GetThresholdValue(), "nothing is mutated on refusal")
	})

	t.Run("no threshold condition", func(t *testing.T) {
		p := testPolicy(&monitoringpb.AlertPolicy_Condition{})
		v := 1.0
		_, _, err := applyGCPPolicyChange(p, providers.AlertRuleChange{Threshold: &v})
		assert.ErrorIs(t, err, providers.ErrAlertRuleChangeUnsupported)
	})

	t.Run("restore", func(t *testing.T) {
		p := testPolicy(thresholdCondition(90, 5*time.Minute))
		p.Enabled = wrapperspb.Bool(false)
		// Whole numbers built in Go (not via JSON) must restore too.
		_, paths, err := applyGCPPolicyChange(p, providers.AlertRuleChange{Restore: map[string]any{
			"threshold": 80, "duration_seconds": 90, "enabled": true,
		}})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"conditions", "enabled"}, paths)
		assert.Equal(t, 80.0, p.GetConditions()[0].GetConditionThreshold().GetThresholdValue())
		assert.Equal(t, 90*time.Second, p.GetConditions()[0].GetConditionThreshold().GetDuration().AsDuration())
		assert.True(t, p.GetEnabled().GetValue())
	})

	t.Run("empty change", func(t *testing.T) {
		_, _, err := applyGCPPolicyChange(testPolicy(thresholdCondition(1, 0)), providers.AlertRuleChange{})
		assert.ErrorIs(t, err, providers.ErrAlertRuleChangeUnsupported)
	})
}
