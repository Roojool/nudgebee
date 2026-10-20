package triage

import (
	"testing"

	"nudgebee/services/eventrule"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func revertSuggestion(recommendation string, previous *float64) *ApplySuggestionRow {
	return &ApplySuggestionRow{
		AlertName:          "HighCPU",
		Source:             "prometheus",
		CurrentThreshold:   80,
		RecommendationType: recommendation,
		PreviousThreshold:  previous,
	}
}

func TestPlanRevert(t *testing.T) {
	prev := 80.0
	promRule := &SourceRule{ID: "r1", Source: "prometheus", Expr: `cpu_usage > 95`, Duration: "5m"}

	t.Run("a disable is undone by switching the rule back on", func(t *testing.T) {
		// Quality-only disables carry no numeric threshold; a threshold rewrite
		// would have written 0.
		plan, err := planRevert(revertSuggestion("disable", nil), promRule, map[string]any{"threshold": 1.0})
		require.NoError(t, err)
		assert.True(t, plan.reEnable)
		assert.Nil(t, plan.rewritten.Restore)
	})

	t.Run("a recorded cloud apply is restored exactly, without new values", func(t *testing.T) {
		previous := map[string]any{"threshold": 70.0, "evaluation_periods": 3.0}
		cloudRule := &SourceRule{ID: "r2", Source: "AWS_CloudWatch_Alarm", Expr: "{}"}
		plan, err := planRevert(revertSuggestion("tune_both", &prev), cloudRule, previous)
		require.NoError(t, err)
		assert.False(t, plan.reEnable)
		assert.Equal(t, previous, plan.rewritten.Restore)
		assert.Empty(t, plan.rewritten.ProviderConfig, "cloud-collector rejects a restore mixed with new values")
		assert.Empty(t, plan.rewritten.Duration)
		assert.Equal(t, 70.0, plan.appliedThreshold)
		assert.Equal(t, "tune_both", plan.sug.RecommendationType)
	})

	t.Run("a restore without a threshold reports the stored previous one", func(t *testing.T) {
		cloudRule := &SourceRule{ID: "r2", Source: "GCP_Metric_Alert", Expr: "{}"}
		plan, err := planRevert(revertSuggestion("increase_duration", &prev), cloudRule, map[string]any{"duration_seconds": 60.0})
		require.NoError(t, err)
		assert.Equal(t, 80.0, plan.appliedThreshold)
	})

	t.Run("without a record the previous threshold is written back", func(t *testing.T) {
		plan, err := planRevert(revertSuggestion("tune_threshold", &prev), promRule, nil)
		require.NoError(t, err)
		assert.False(t, plan.reEnable)
		assert.Nil(t, plan.rewritten.Restore)
		assert.Equal(t, "cpu_usage > 80", plan.rewritten.Expr)
		assert.Equal(t, "tune_threshold", plan.sug.RecommendationType)
		assert.Equal(t, 80.0, plan.appliedThreshold)
	})

	t.Run("a duration change restores threshold and duration", func(t *testing.T) {
		d := 5
		sug := revertSuggestion("increase_duration", &prev)
		sug.PreviousDuration = &d
		plan, err := planRevert(sug, promRule, nil)
		require.NoError(t, err)
		assert.Equal(t, "tune_both", plan.sug.RecommendationType)
		assert.Equal(t, "increase_duration", sug.RecommendationType, "the caller's suggestion is not mutated")
	})

	t.Run("nothing recorded at all", func(t *testing.T) {
		_, err := planRevert(revertSuggestion("tune_threshold", nil), promRule, nil)
		assert.ErrorContains(t, err, "no previous threshold")
	})
}

func TestCloudUpdateConfig(t *testing.T) {
	sug := revertSuggestion("tune_threshold", nil)
	rule := &SourceRule{AlertType: "metric"}
	awsByName := &eventrule.RuleTarget{Kind: eventrule.TargetAWS, Enabled: true,
		Locator: eventrule.RuleLocator{AWSRegion: "eu-west-1", AWSAlarmName: "HighCPU"}}

	t.Run("revert of an alarm known by name keeps the region, adds no values", func(t *testing.T) {
		restore := map[string]any{"threshold": 80.0}
		cfg := cloudUpdateConfig(awsByName, sug, rule, &RewrittenRule{Restore: restore}, "")
		assert.Equal(t, restore, cfg.Restore)
		// region only locates the alarm; cloud-collector's restore check looks at
		// threshold / duration_minutes / set_enabled.
		assert.Equal(t, map[string]interface{}{"region": "eu-west-1"}, cfg.ProviderConfig)
		assert.Equal(t, "HighCPU", cfg.Name)
	})

	t.Run("apply carries the new values", func(t *testing.T) {
		cfg := cloudUpdateConfig(awsByName, sug, rule, &RewrittenRule{ProviderConfig: map[string]interface{}{"threshold": 90.0}}, "")
		assert.Nil(t, cfg.Restore)
		assert.Equal(t, map[string]interface{}{"threshold": 90.0, "region": "eu-west-1"}, cfg.ProviderConfig)
	})

	t.Run("an ARN needs no region", func(t *testing.T) {
		cfg := cloudUpdateConfig(awsByName, sug, rule, &RewrittenRule{Restore: map[string]any{"threshold": 80.0}}, "arn:aws:cloudwatch:eu-west-1:1:alarm:HighCPU")
		assert.Empty(t, cfg.ProviderConfig)
	})
}
