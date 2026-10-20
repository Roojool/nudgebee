package aws

import (
	"encoding/json"
	"testing"

	"nudgebee/collector/cloud/providers"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func int32Value(p *int32) any {
	if p == nil {
		return nil
	}
	return *p
}

// Widening the window must keep the alarm's "M of N" ratio; the old code turned a
// 3-of-3 alarm into 3-of-10, which fires on far sparser breaches.
func TestScaleEvaluationWindow(t *testing.T) {
	tests := []struct {
		name     string
		period   int32
		oldEval  int32
		oldDP    *int32
		minutes  int
		wantEval int32
		wantDP   any
	}{
		{"3 of 3 to 10 periods", 60, 3, aws.Int32(3), 10, 10, int32(10)},
		{"2 of 4 to 8 periods keeps half", 60, 4, aws.Int32(2), 8, 8, int32(4)},
		{"1 of 3 rounds up", 60, 3, aws.Int32(1), 4, 4, int32(2)},
		{"all-N stays all-N", 300, 1, nil, 15, 3, nil},
		{"shorter than one period", 300, 2, aws.Int32(2), 1, 1, int32(1)},
		{"narrowing keeps ratio", 60, 10, aws.Int32(5), 4, 4, int32(2)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			eval, dp := scaleEvaluationWindow(tc.period, tc.oldEval, tc.oldDP, tc.minutes)
			assert.Equal(t, tc.wantEval, *eval)
			assert.Equal(t, tc.wantDP, int32Value(dp))
		})
	}
}

func TestApplyCloudWatchAlarmChange(t *testing.T) {
	base := func() *cloudwatch.PutMetricAlarmInput {
		return &cloudwatch.PutMetricAlarmInput{
			Threshold:         aws.Float64(80),
			Period:            aws.Int32(60),
			EvaluationPeriods: aws.Int32(3),
			DatapointsToAlarm: aws.Int32(3),
		}
	}

	t.Run("duration only keeps the threshold", func(t *testing.T) {
		in := base()
		require.NoError(t, applyCloudWatchAlarmChange(in, providers.AlertRuleChange{DurationMinutes: 10}))
		assert.Equal(t, 80.0, *in.Threshold)
		assert.Equal(t, int32(10), *in.EvaluationPeriods)
		assert.Equal(t, int32(10), *in.DatapointsToAlarm)
	})

	t.Run("anomaly band alarm has no threshold to set", func(t *testing.T) {
		in := base()
		in.ThresholdMetricId = aws.String("ad1")
		v := 5.0
		err := applyCloudWatchAlarmChange(in, providers.AlertRuleChange{Threshold: &v})
		assert.ErrorIs(t, err, providers.ErrAlertRuleChangeUnsupported)
	})

	t.Run("metric-math window is refused, not silently skipped", func(t *testing.T) {
		in := base()
		in.Period = nil
		err := applyCloudWatchAlarmChange(in, providers.AlertRuleChange{DurationMinutes: 10})
		assert.ErrorIs(t, err, providers.ErrAlertRuleChangeUnsupported)
	})

	t.Run("restore puts back the exact previous window", func(t *testing.T) {
		in := base()
		in.EvaluationPeriods = aws.Int32(10)
		in.DatapointsToAlarm = aws.Int32(10)
		in.Threshold = aws.Float64(95)
		prev := cloudWatchAlarmWindow(&cwtypesMetricAlarm{Threshold: aws.Float64(80), EvaluationPeriods: aws.Int32(3), DatapointsToAlarm: aws.Int32(2)})
		require.NoError(t, applyCloudWatchAlarmChange(in, providers.AlertRuleChange{Restore: roundTrip(t, prev)}))
		assert.Equal(t, 80.0, *in.Threshold)
		assert.Equal(t, int32(3), *in.EvaluationPeriods)
		assert.Equal(t, int32(2), *in.DatapointsToAlarm)
	})

	t.Run("restore of an all-N alarm clears DatapointsToAlarm", func(t *testing.T) {
		in := base()
		prev := cloudWatchAlarmWindow(&cwtypesMetricAlarm{Threshold: aws.Float64(80), EvaluationPeriods: aws.Int32(1)})
		require.NoError(t, applyCloudWatchAlarmChange(in, providers.AlertRuleChange{Restore: roundTrip(t, prev)}))
		assert.Equal(t, int32(1), *in.EvaluationPeriods)
		assert.Nil(t, in.DatapointsToAlarm)
	})
}

type cwtypesMetricAlarm = types.MetricAlarm

// roundTrip passes "previous" through JSON, the way it travels via api-server.
func roundTrip(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(m)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(b, &out))
	return out
}
