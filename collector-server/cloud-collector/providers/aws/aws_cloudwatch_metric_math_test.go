package aws

import (
	"context"
	"testing"
	"time"

	"nudgebee/collector/cloud/common"
	"nudgebee/collector/cloud/providers"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
)

// TestBuildMetricDataQueriesFromMetricQueries pins the fix for metric-math
// alarms: given every raw sub-metric plus the expression that computes the
// alarm's real value (Remaining = sent - current - deleted), it must build one
// MetricDataQuery per entry — MetricStat for the raw metrics, Expression for
// the formula — preserving each entry's own id (not renumbering them), since
// the expression string references those exact ids.
func TestBuildMetricDataQueriesFromMetricQueries(t *testing.T) {
	metricQueries := []providers.CloudWatchMetricEntry{
		{Id: "m1", ReturnData: false, MetricStat: &providers.CloudWatchMetricEntryStat{
			Stat: "Sum", Period: 60,
			Metric: providers.CloudWatchMetricEntryRef{Namespace: "AWS/Custom", Name: "sent"},
		}},
		{Id: "m2", ReturnData: false, MetricStat: &providers.CloudWatchMetricEntryStat{
			Stat: "Sum", Period: 60,
			Metric: providers.CloudWatchMetricEntryRef{Namespace: "AWS/Custom", Name: "current"},
		}},
		{Id: "m3", ReturnData: false, MetricStat: &providers.CloudWatchMetricEntryStat{
			Stat: "Sum", Period: 60,
			Metric: providers.CloudWatchMetricEntryRef{Namespace: "AWS/Custom", Name: "deleted"},
		}},
		{Id: "e1", Expression: "m1 - m2 - m3", Label: "Remaining", ReturnData: true},
	}

	queries, queryIdMap := buildMetricDataQueriesFromMetricQueries(metricQueries, "123456789012")

	if len(queries) != 4 {
		t.Fatalf("got %d queries, want 4", len(queries))
	}

	byId := map[string]int{}
	for i, q := range queries {
		byId[*q.Id] = i
	}

	// Raw metrics keep MetricStat, get no Expression, and keep their own id.
	for _, tc := range []struct {
		id, namespace, name, stat string
		period                    int32
	}{
		{"m1", "AWS/Custom", "sent", "Sum", 60},
		{"m2", "AWS/Custom", "current", "Sum", 60},
		{"m3", "AWS/Custom", "deleted", "Sum", 60},
	} {
		idx, ok := byId[tc.id]
		if !ok {
			t.Fatalf("no query built for id %q", tc.id)
		}
		q := queries[idx]
		if q.Expression != nil {
			t.Errorf("query %q: got Expression set, want nil (it's a raw metric)", tc.id)
		}
		if q.MetricStat == nil {
			t.Fatalf("query %q: MetricStat is nil, want populated", tc.id)
		}
		// Every entry's own alarm-time ReturnData was false (real alarms only
		// need the expression's result back for evaluation), but this is an
		// evidence-display request, not the alarm's own evaluation — we want
		// every input's data back too, so it must always be forced to true.
		if q.ReturnData == nil || !*q.ReturnData {
			t.Errorf("query %q: ReturnData = %v, want true (evidence needs every input's data, regardless of the alarm's own returnData)", tc.id, q.ReturnData)
		}
		if *q.MetricStat.Metric.Namespace != tc.namespace || *q.MetricStat.Metric.MetricName != tc.name {
			t.Errorf("query %q: namespace/name = %s/%s, want %s/%s",
				tc.id, *q.MetricStat.Metric.Namespace, *q.MetricStat.Metric.MetricName, tc.namespace, tc.name)
		}
		if *q.MetricStat.Stat != tc.stat || *q.MetricStat.Period != tc.period {
			t.Errorf("query %q: stat/period = %s/%d, want %s/%d", tc.id, *q.MetricStat.Stat, *q.MetricStat.Period, tc.stat, tc.period)
		}
	}

	// The expression keeps its own id ("e1", not renumbered), sets Expression,
	// and leaves MetricStat nil — this is the entry that was silently dropped
	// before this fix.
	idx, ok := byId["e1"]
	if !ok {
		t.Fatalf("no query built for the expression entry (id %q)", "e1")
	}
	e1 := queries[idx]
	if e1.MetricStat != nil {
		t.Errorf("e1: MetricStat set, want nil (it's an expression)")
	}
	if e1.Expression == nil || *e1.Expression != "m1 - m2 - m3" {
		t.Errorf("e1: Expression = %v, want %q", e1.Expression, "m1 - m2 - m3")
	}
	if e1.Label == nil || *e1.Label != "Remaining" {
		t.Errorf("e1: Label = %v, want %q", e1.Label, "Remaining")
	}
	if e1.ReturnData == nil || !*e1.ReturnData {
		t.Errorf("e1: ReturnData = %v, want true", e1.ReturnData)
	}

	// queryIdMap must have an entry for e1 too — without this, the
	// result-reading loop drops the expression's result as "unmapped" even
	// though the query for it was sent and CloudWatch computed it correctly.
	meta, ok := queryIdMap["e1"]
	if !ok {
		t.Fatalf("queryIdMap has no entry for e1 — its GetMetricData result would be silently dropped")
	}
	if meta.metricName != "Remaining" {
		t.Errorf("queryIdMap[e1].metricName = %q, want %q", meta.metricName, "Remaining")
	}
}

// TestBuildMetricDataQueriesUnlabeledExpressionFallsBackToId pins the fix for
// an expression entry with no Label (AWS doesn't require one) — without a
// fallback, its series would surface in the evidence with an empty name.
func TestBuildMetricDataQueriesUnlabeledExpressionFallsBackToId(t *testing.T) {
	metricQueries := []providers.CloudWatchMetricEntry{
		{Id: "e1", Expression: "m1 - m2 - m3", ReturnData: true}, // no Label
	}

	_, queryIdMap := buildMetricDataQueriesFromMetricQueries(metricQueries, "123456789012")

	meta, ok := queryIdMap["e1"]
	if !ok {
		t.Fatalf("no queryIdMap entry for e1")
	}
	if meta.metricName != "e1" {
		t.Errorf("queryIdMap[e1].metricName = %q, want %q (the id, since there's no Label to fall back to)", meta.metricName, "e1")
	}
}

// TestBuildMetricDataQueriesSetsPeriodForMetricsInsightsExpression pins the
// fix for a Metrics Insights (SELECT ...) alarm: that kind of expression has
// no MetricStat to inherit a period from, so CloudWatch requires Period set
// directly on the MetricDataQuery. A plain metric-math expression (referencing
// other entries' ids) must NOT get this set — it derives timing from them.
func TestBuildMetricDataQueriesSetsPeriodForMetricsInsightsExpression(t *testing.T) {
	metricQueries := []providers.CloudWatchMetricEntry{
		{Id: "q1", Expression: `SELECT AVG(CPUUtilization) FROM "AWS/EC2"`, Period: 300, ReturnData: true},
		{Id: "e1", Expression: "m1 - m2 - m3", ReturnData: true}, // math expression, no Period in the source data
	}

	queries, _ := buildMetricDataQueriesFromMetricQueries(metricQueries, "123456789012")

	byId := map[string]types.MetricDataQuery{}
	for _, q := range queries {
		byId[*q.Id] = q
	}

	if byId["q1"].Period == nil || *byId["q1"].Period != 300 {
		t.Errorf("q1 (Metrics Insights): Period = %v, want 300", byId["q1"].Period)
	}
	if byId["e1"].Period != nil {
		t.Errorf("e1 (metric math): Period = %v, want nil — it derives timing from the entries it references", *byId["e1"].Period)
	}
}

// TestCombineMetricItemsAcrossResourcesZeroesUnrecognisedStatistics documents
// the exact mechanism behind the critical evidence-fetch bug this test suite
// guards against: this function looks up an aggregation function by the
// item's Statistics, and an expression's result has none (it's a computed
// value, not an Average/Sum) — so calling this on metric-math results zeroes
// them out. This is why getAwsCloudwatchMetrics must skip calling it entirely
// when filter.MetricQueries is set (see the len(filter.MetricQueries) == 0
// guard around the call site) rather than ever reaching this code.
func TestCombineMetricItemsAcrossResourcesZeroesUnrecognisedStatistics(t *testing.T) {
	pCtx := providers.NewCloudProviderContext(context.Background())
	base := time.Date(2026, 9, 16, 4, 30, 0, 0, time.UTC)

	items := []providers.MetricItem{
		{Name: "Remaining", Statistics: "", Values: []float64{100, 150}, Timestamps: []time.Time{base, base.Add(time.Minute)}},
	}

	got := combineMetricItemsAcrossResources(pCtx, items, "us-east-1", "")

	if len(got) != 1 {
		t.Fatalf("got %d items, want 1", len(got))
	}
	for _, v := range got[0].Values {
		if v != 0 {
			t.Fatalf("combineMetricItemsAcrossResources did NOT zero out the unrecognised-statistics series (values=%v) — "+
				"if this function's behavior changed, double check the len(filter.MetricQueries)==0 guard in "+
				"getAwsCloudwatchMetrics is still needed", got[0].Values)
		}
	}
}

// TestDecodeMetricsJsonIntoCloudWatchMetricEntry proves the yaml-rendered
// metrics_json string (JSON, camelCase field names, exactly as CloudWatch
// emits them) decodes cleanly into []providers.CloudWatchMetricEntry.
func TestDecodeMetricsJsonIntoCloudWatchMetricEntry(t *testing.T) {
	metricsJSON := `[{"id":"m1","metricStat":{"metric":{"name":"sent","namespace":"AWS/Custom"},"period":60,"stat":"Sum"},"returnData":false},{"id":"m2","metricStat":{"metric":{"name":"current","namespace":"AWS/Custom"},"period":60,"stat":"Sum"},"returnData":false},{"id":"m3","metricStat":{"metric":{"name":"deleted","namespace":"AWS/Custom"},"period":60,"stat":"Sum"},"returnData":false},{"expression":"m1 - m2 - m3","id":"e1","label":"Remaining","returnData":true}]`

	var entries []providers.CloudWatchMetricEntry
	if err := common.UnmarshalJson([]byte(metricsJSON), &entries); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if len(entries) != 4 {
		t.Fatalf("got %d entries, want 4", len(entries))
	}
	if entries[3].Expression != "m1 - m2 - m3" || entries[3].Label != "Remaining" {
		t.Errorf("entries[3] = %+v, want the Remaining expression", entries[3])
	}
	if entries[0].MetricStat == nil || entries[0].MetricStat.Metric.Name != "sent" {
		t.Errorf("entries[0] = %+v, want a raw metric named sent", entries[0])
	}
}
