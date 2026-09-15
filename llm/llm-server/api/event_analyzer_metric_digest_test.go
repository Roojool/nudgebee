package api

import (
	"strings"
	"testing"

	"nudgebee/llm/events"
)

func TestRenderMetricDigest(t *testing.T) {
	t.Run("cloudwatch items shape", func(t *testing.T) {
		ev := events.InvestigateData{Others: []events.InvestigateDataInsight{{
			Data: `{"items":[{"name":"CPUUtilization","statistics":"Average","resource_id":"janus",` +
				`"values":[81.67,98.63,92.64],"timestamps":["2026-09-14T09:34:00Z","2026-09-14T10:09:00Z","2026-09-14T10:29:00Z"]}]}`,
		}}}
		got := renderMetricDigest(ev)
		for _, want := range []string{"CPUUtilization Average janus", "3 points 09-14 09:34→09-14 10:29", "first 81.670", "max 98.630 @09-14 10:09", "last 92.640"} {
			if !strings.Contains(got, want) {
				t.Errorf("digest missing %q:\n%s", want, got)
			}
		}
	})

	t.Run("metric_groups shape", func(t *testing.T) {
		ev := events.InvestigateData{MetricsData: []events.InvestigateDataInsight{{
			Data: `{"result_type":"matrix","metric_groups":{"CPUUtilization (Maximum)":{"series_list_result":[` +
				`{"metric":{"name":"CPUUtilization","statistics":"Maximum","resource_id":"billing-server"},` +
				`"values":[98.7,100.1],"timestamps":[1789382040,1789382100]}]}}}`,
		}}}
		got := renderMetricDigest(ev)
		if !strings.Contains(got, "CPUUtilization Maximum billing-server") || !strings.Contains(got, "2 points") {
			t.Errorf("unexpected digest:\n%s", got)
		}
	})

	t.Run("series repeated at top level (aws evidence) are rendered once, oldest first", func(t *testing.T) {
		series := `{"metric":{"name":"CPUUtilization","statistics":"Maximum","resource_id":"billing-server"},"values":[99.8,97.1,98.7],"timestamps":[1789381980,1789381920,1789377840]}`
		ev := events.InvestigateData{MetricsData: []events.InvestigateDataInsight{{
			Data: `{"metric_groups":{"CPUUtilization (Maximum)":{"series_list_result":[` + series + `]}},"series_list_result":[` + series + `]}`,
		}}}
		got := renderMetricDigest(ev)
		if strings.Count(got, "CPUUtilization") != 1 {
			t.Errorf("expected the repeated series once, got:\n%s", got)
		}
		if !strings.Contains(got, "first 98.700") || !strings.Contains(got, "last 99.800") {
			t.Errorf("expected points sorted by time (first 98.7 at the earliest timestamp), got:\n%s", got)
		}
	})

	t.Run("kubernetes pod metrics with string values and unix seconds", func(t *testing.T) {
		ev := events.InvestigateData{PodMetrics: []events.InvestigateDataInsight{{
			Data: `{"data":{"memory_usage":{"series_list_result":[{"metric":{"__name__":"container_memory_working_set_bytes","pod":"services-server-6pl88"},` +
				`"values":["91922432","355463168","57880576"],"timestamps":[1787408040,1787410470,1787411640]}]}}}`,
		}}}
		got := renderMetricDigest(ev)
		if !strings.Contains(got, "memory_usage container_memory_working_set_bytes services-server-6pl88") || !strings.Contains(got, "max 355.46e6") {
			t.Errorf("unexpected digest:\n%s", got)
		}
	})

	t.Run("non-metric evidence and mismatched arrays are ignored", func(t *testing.T) {
		ev := events.InvestigateData{
			Others: []events.InvestigateDataInsight{
				{Data: `{"data":[{"labels":{"@ptr":"x"},"message":"GET /health 499"}]}`},
				{Data: `{"values":[1,2],"timestamps":["2026-09-14T09:34:00Z"]}`},
				{Data: "plain text"},
			},
		}
		if got := renderMetricDigest(ev); got != "" {
			t.Errorf("expected no digest, got:\n%s", got)
		}
	})

	t.Run("series count is bounded", func(t *testing.T) {
		var list []events.InvestigateDataInsight
		for i := 0; i < maxMetricDigestSeries+5; i++ {
			list = append(list, events.InvestigateDataInsight{Data: map[string]any{
				"name": "m" + strings.Repeat("x", i), "values": []any{float64(i)}, "timestamps": []any{"2026-09-14T09:34:00Z"},
			}})
		}
		got := renderMetricDigest(events.InvestigateData{MetricsData: list})
		if lines := strings.Count(got, "\n- ") + 1; lines > maxMetricDigestSeries+1 {
			t.Errorf("expected at most %d lines, got %d:\n%s", maxMetricDigestSeries+1, lines, got)
		}
		if !strings.Contains(got, "- … 5 more series in the structured evidence file") {
			t.Errorf("expected overflow note, got:\n%s", got)
		}
	})

	t.Run("digest appears in the investigation evidence context", func(t *testing.T) {
		ev := events.InvestigateData{Others: []events.InvestigateDataInsight{{
			Insight: []events.Insight{{Message: "Fetch metric data on alarm"}},
			Data:    `{"items":[{"name":"WriteIOPS","values":[7047.9],"timestamps":["2026-09-13T01:10:00Z"]}]}`,
		}}}
		got := buildInvestigationEvidenceContext(ev)
		if !strings.Contains(got, "### Metric Values") || !strings.Contains(got, "WriteIOPS") {
			t.Errorf("expected metric values section, got:\n%s", got)
		}
	})
}
