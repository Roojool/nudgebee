package aws

import (
	"context"
	"strings"
	"testing"
	"time"

	"nudgebee/collector/cloud/providers"
)

// cpuAlarmDetail is the verbatim EventBridge detail for event
// 3f9d39e4-9882-4527-807e-3d5e2d11e017 (alarm nudgebee-scenario-services-order-cpu).
// CloudWatch evaluated it at a 60s period against a threshold of 40 and fired on
// a 99.97% datapoint, while the runbook action queried the series at 300s and
// produced a 33.8% peak — a card that contradicted its own alarm.
const cpuAlarmDetail = `{
  "alarmName": "nudgebee-scenario-services-order-cpu",
  "configuration": {
    "description": "order host CPU - retry pressure when a dependency fails",
    "metrics": [
      {
        "id": "a4d037e1-8313-9324-eb5e-5bb61ca7cb0e",
        "metricStat": {
          "metric": {
            "dimensions": {"InstanceId": "i-0dcee3621b8456783"},
            "name": "CPUUtilization",
            "namespace": "AWS/EC2"
          },
          "period": 60,
          "stat": "Average"
        },
        "returnData": true
      }
    ]
  },
  "state": {
    "reason": "Threshold Crossed: 2 datapoints [99.97497497497497 (07/09/26 06:14:00), 65.61771561771562 (07/09/26 06:13:00)] were greater than the threshold (40.0).",
    "reasonData": "{\"version\":\"1.0\",\"queryDate\":\"2026-09-07T06:15:02.213+0000\",\"startDate\":\"2026-09-07T06:13:00.000+0000\",\"statistic\":\"Average\",\"period\":60,\"recentDatapoints\":[65.61771561771562,99.97497497497497],\"threshold\":40.0}",
    "timestamp": "2026-09-07T06:15:02.215+0000",
    "value": "ALARM"
  }
}`

func TestExtractAlarmEvaluation(t *testing.T) {
	eval := extractAlarmEvaluation(EventBridgeEvent{Detail: []byte(cpuAlarmDetail)})
	if eval.PeriodSeconds != 60 {
		t.Errorf("PeriodSeconds = %d, want 60", eval.PeriodSeconds)
	}
	if !eval.HasThreshold || eval.Threshold != 40 {
		t.Errorf("Threshold = %v (has=%v), want 40 (has=true)", eval.Threshold, eval.HasThreshold)
	}
}

// TestExtractAlarmEvaluationFallsBackToMetricStat covers alarms whose state
// carries no reasonData (composite/insufficient-data transitions).
func TestExtractAlarmEvaluationFallsBackToMetricStat(t *testing.T) {
	eval := extractAlarmEvaluation(EventBridgeEvent{Detail: []byte(realAlarmDetail)})
	if eval.PeriodSeconds != 60 {
		t.Errorf("PeriodSeconds = %d, want 60 from metricStat.period", eval.PeriodSeconds)
	}
	if eval.HasThreshold {
		t.Errorf("HasThreshold = true, want false when reasonData is absent")
	}
}

func TestExtractAlarmEvaluationNonAlarmEvent(t *testing.T) {
	eval := extractAlarmEvaluation(EventBridgeEvent{Detail: []byte(`{"foo":"bar"}`)})
	if eval != (alarmEvaluation{}) {
		t.Errorf("eval = %#v, want zero value for a non-alarm event", eval)
	}
}

// TestBuildActionEvidenceInsightDropsActionMetadata pins the fix for the evidence
// card that listed "get_cloudwatch_metric_data_on_alarm" and "Fetch metric data
// for the alarming metric" as its two insights: action metadata is not an
// insight, and action types with nothing to say emit none.
func TestBuildActionEvidenceInsightDropsActionMetadata(t *testing.T) {
	actionDef := ActionDefinition{
		Name:        "get_cloudwatch_metric_data_on_alarm",
		Type:        "aws_get_resource",
		Description: "Fetch metric data for the alarming metric",
	}
	if got := buildActionEvidenceInsight(EventBridgeEvent{}, actionDef, map[string]any{"any": "result"}); got != nil {
		t.Errorf("insight = %#v, want nil", got)
	}
}

func TestBuildActionEvidenceInsightSummarisesMetricSeries(t *testing.T) {
	base := time.Date(2026, 9, 7, 6, 10, 0, 0, time.UTC)
	result := providers.QueryMetricsResponse{
		Items: []providers.MetricItem{{
			Name:       "CPUUtilization",
			Statistics: "Average",
			ResourceId: "i-0dcee3621b8456783",
			Values:     []float64{0.92, 1.08, 65.62, 99.97},
			Timestamps: []time.Time{base, base.Add(time.Minute), base.Add(3 * time.Minute), base.Add(4 * time.Minute)},
		}},
		StartDate: base.Add(-time.Hour),
		EndDate:   base.Add(5 * time.Minute),
	}

	lines := buildActionEvidenceInsight(
		EventBridgeEvent{Detail: []byte(cpuAlarmDetail)},
		ActionDefinition{Name: "get_cloudwatch_metric_data_on_alarm", Type: "aws_get_metric"},
		result,
	)
	if len(lines) != 2 {
		t.Fatalf("lines = %#v, want 2", lines)
	}
	for _, want := range []string{"CPUUtilization (Average)", "99.97", "2026-09-07T06:14:00Z", "4 datapoints"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("lines[0] = %q, want it to contain %q", lines[0], want)
		}
	}
	for _, want := range []string{"threshold 40.00", "0.92 to 99.97"} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("lines[1] = %q, want it to contain %q", lines[1], want)
		}
	}
}

// metricMathAlarmDetail is a math alarm whose real value is an expression
// (Remaining = sent - current - deleted) over three raw inputs that were
// never themselves compared to the threshold — only the returnData:true
// entry (e1, labeled "Remaining") was.
const metricMathAlarmDetail = `{
  "alarmName": "nb-demo-remaining",
  "configuration": {
    "metrics": [
      {"id": "m1", "metricStat": {"stat": "Sum", "period": 60, "metric": {"namespace": "AWS/Custom", "name": "sent"}}, "returnData": false},
      {"id": "m2", "metricStat": {"stat": "Sum", "period": 60, "metric": {"namespace": "AWS/Custom", "name": "current"}}, "returnData": false},
      {"id": "m3", "metricStat": {"stat": "Sum", "period": 60, "metric": {"namespace": "AWS/Custom", "name": "deleted"}}, "returnData": false},
      {"id": "e1", "expression": "m1 - m2 - m3", "label": "Remaining", "returnData": true}
    ]
  },
  "state": {
    "reason": "Threshold Crossed",
    "reasonData": "{\"version\":\"1.0\",\"period\":60,\"threshold\":50.0}",
    "value": "ALARM"
  }
}`

// TestMetricSeriesInsightScopesThresholdToExpressionSeries pins the fix for a
// metric-math alarm's evidence: with four returned series (three raw inputs
// plus the expression's computed result), the threshold comparison line must
// only follow "Remaining" — the series CloudWatch actually evaluated — not
// the three unrelated raw inputs, which were never compared to it.
func TestMetricSeriesInsightScopesThresholdToExpressionSeries(t *testing.T) {
	base := time.Date(2026, 9, 16, 4, 30, 0, 0, time.UTC)
	mkItem := func(name string, values ...float64) providers.MetricItem {
		return providers.MetricItem{Name: name, Values: values, Timestamps: []time.Time{base, base.Add(time.Minute)}}
	}
	result := providers.QueryMetricsResponse{
		Items: []providers.MetricItem{
			mkItem("sent", 500, 480),
			mkItem("current", 300, 290),
			mkItem("deleted", 100, 90),
			mkItem("Remaining", 100, 100),
		},
		StartDate: base.Add(-time.Hour),
		EndDate:   base,
	}

	lines := buildActionEvidenceInsight(
		EventBridgeEvent{Detail: []byte(metricMathAlarmDetail)},
		ActionDefinition{Name: "get_cloudwatch_metric_data_on_alarm", Type: "aws_get_metric"},
		result,
	)

	thresholdLines := 0
	for i, line := range lines {
		if !strings.Contains(line, "Alarm threshold") {
			continue
		}
		thresholdLines++
		if !strings.HasPrefix(lines[i-1], "Remaining ") {
			t.Errorf("threshold line %q follows %q, want it to follow the Remaining series", line, lines[i-1])
		}
	}
	if thresholdLines != 1 {
		t.Fatalf("got %d threshold lines, want exactly 1 (attached only to Remaining): %#v", thresholdLines, lines)
	}
}

// labeledSingleMetricAlarmDetail is a plain single-metric alarm whose
// returnData:true entry carries BOTH a metricStat and a custom label — a
// case AWS allows (Label isn't exclusive to expressions). Pins the fix for a
// mismatch where extractAlarmValueName checked metricStat.metric.name before
// label, while buildMetricDataQueriesFromMetricQueries names the fetched
// series after label first — so the threshold line failed to match the
// returned item.Name for any alarm like this one.
const labeledSingleMetricAlarmDetail = `{
  "alarmName": "nb-demo-labeled",
  "configuration": {
    "metrics": [
      {"id": "m1", "metricStat": {"stat": "Average", "period": 60, "metric": {"namespace": "AWS/EC2", "name": "CPUUtilization"}}, "label": "Order Host CPU", "returnData": true}
    ]
  },
  "state": {
    "reason": "Threshold Crossed",
    "reasonData": "{\"version\":\"1.0\",\"period\":60,\"threshold\":40.0}",
    "value": "ALARM"
  }
}`

// TestMetricSeriesInsightMatchesLabeledSingleMetricSeries pins the
// extractAlarmValueName/buildMetricDataQueriesFromMetricQueries precedence
// fix: the fetched series is named after the alarm's custom label (matching
// what the query-builder does), so the threshold line must attach to that
// same label, not the raw metric name underneath it.
func TestMetricSeriesInsightMatchesLabeledSingleMetricSeries(t *testing.T) {
	base := time.Date(2026, 9, 16, 4, 30, 0, 0, time.UTC)
	result := providers.QueryMetricsResponse{
		Items: []providers.MetricItem{
			{Name: "Order Host CPU", Values: []float64{20, 45}, Timestamps: []time.Time{base, base.Add(time.Minute)}},
		},
		StartDate: base.Add(-time.Hour),
		EndDate:   base,
	}

	lines := buildActionEvidenceInsight(
		EventBridgeEvent{Detail: []byte(labeledSingleMetricAlarmDetail)},
		ActionDefinition{Name: "get_cloudwatch_metric_data_on_alarm", Type: "aws_get_metric"},
		result,
	)

	found := false
	for _, line := range lines {
		if strings.Contains(line, "Alarm threshold") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no threshold line found — extractAlarmValueName likely returned %q (the raw metric name) instead of the label, so it never matched item.Name %q: %#v",
			"CPUUtilization", "Order Host CPU", lines)
	}
}

func TestMetricSeriesInsightEmptySeries(t *testing.T) {
	result := providers.QueryMetricsResponse{
		Items:     []providers.MetricItem{{Name: "CPUUtilization", Statistics: "Average"}},
		StartDate: time.Date(2026, 9, 7, 5, 15, 2, 0, time.UTC),
		EndDate:   time.Date(2026, 9, 7, 6, 20, 2, 0, time.UTC),
	}
	lines := metricSeriesInsight(result, alarmEvaluation{}, "")
	if len(lines) != 1 || !strings.Contains(lines[0], "no datapoints") {
		t.Errorf("lines = %#v, want a single no-datapoints line", lines)
	}
}

func TestFormatMetricValue(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{0, "0"},
		{0.0004521, "0.000452"},
		{0.9219575222252228, "0.92"},
		{99.97497497497497, "99.97"},
		{1048576.4, "1048576"},
	}
	for _, tt := range tests {
		if got := formatMetricValue(tt.in); got != tt.want {
			t.Errorf("formatMetricValue(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// captureMetricsAPI records the QueryMetricsRequest the action builds so the
// period narrowing can be asserted on the request that would reach CloudWatch.
type captureMetricsAPI struct {
	awsProviderAPI
	got      providers.QueryMetricsRequest
	response providers.QueryMetricsResponse
}

func (c *captureMetricsAPI) QueryMetrices(_ providers.CloudProviderContext, _ providers.Account, filter providers.QueryMetricsRequest) (providers.QueryMetricsResponse, error) {
	c.got = filter
	return c.response, nil
}

// TestAlarmActionNarrowsPeriodToAlarmEvaluation drives the aws_get_metric action
// with the runbook's declared period_seconds: 300 against the 60s alarm that
// produced event 3f9d39e4-9882-4527-807e-3d5e2d11e017. Querying at 300s averaged
// the breaching 99.97% minute into a 33.8% bucket, below the alarm's own 40%
// threshold.
func TestAlarmActionNarrowsPeriodToAlarmEvaluation(t *testing.T) {
	api := &captureMetricsAPI{}
	processor := NewTemplatedEventBridgeProcessor(EventRuleSet{}, api)
	pCtx := providers.NewCloudProviderContext(context.Background())

	ebEvent := EventBridgeEvent{
		ID:         "alarm-period",
		Source:     "aws.cloudwatch",
		Region:     "us-east-1",
		DetailType: "CloudWatch Alarm State Change",
		Time:       time.Date(2026, 9, 7, 6, 15, 2, 0, time.UTC),
		Detail:     []byte(cpuAlarmDetail),
	}
	actionDef := ActionDefinition{
		Name: "get_cloudwatch_metric_data_on_alarm",
		Type: "aws_get_metric",
		Params: map[string]any{
			"namespace":         "AWS/EC2",
			"metric_name":       "CPUUtilization",
			"period_seconds":    300,
			"statistic":         "Average",
			"start_time_offset": "-1h",
			"end_time_offset":   "+5m",
		},
	}

	if _, err := processor.executeAction(pCtx, providers.Account{}, ebEvent, actionDef, processor.prepareTemplateData(ebEvent)); err != nil {
		t.Fatalf("executeAction: %v", err)
	}
	if api.got.Step != 60*time.Second {
		t.Errorf("Step = %v, want 60s (the alarm's evaluation period), not the rule's 300s", api.got.Step)
	}
}

// TestAlarmActionKeepsRulePeriodWhenAlarmIsCoarser guards the narrowing against
// widening: a 300s alarm must not drag a rule that asked for 60s up to 300s.
func TestAlarmActionKeepsRulePeriodWhenAlarmIsCoarser(t *testing.T) {
	coarseAlarm := strings.Replace(cpuAlarmDetail, `\"period\":60`, `\"period\":300`, 1)
	coarseAlarm = strings.Replace(coarseAlarm, `"period": 60`, `"period": 300`, 1)

	api := &captureMetricsAPI{}
	processor := NewTemplatedEventBridgeProcessor(EventRuleSet{}, api)
	pCtx := providers.NewCloudProviderContext(context.Background())

	ebEvent := EventBridgeEvent{
		ID:         "alarm-period-coarse",
		Source:     "aws.cloudwatch",
		Region:     "us-east-1",
		DetailType: "CloudWatch Alarm State Change",
		Time:       time.Date(2026, 9, 7, 6, 15, 2, 0, time.UTC),
		Detail:     []byte(coarseAlarm),
	}
	actionDef := ActionDefinition{
		Name: "get_cloudwatch_metric_data_on_alarm",
		Type: "aws_get_metric",
		Params: map[string]any{
			"namespace":         "AWS/EC2",
			"metric_name":       "CPUUtilization",
			"period_seconds":    60,
			"statistic":         "Average",
			"start_time_offset": "-1h",
			"end_time_offset":   "+5m",
		},
	}

	if _, err := processor.executeAction(pCtx, providers.Account{}, ebEvent, actionDef, processor.prepareTemplateData(ebEvent)); err != nil {
		t.Fatalf("executeAction: %v", err)
	}
	if api.got.Step != 60*time.Second {
		t.Errorf("Step = %v, want the rule's 60s to be preserved", api.got.Step)
	}
}
