package agents

import (
	"strings"
	"testing"

	"nudgebee/llm/agents/core"
	"nudgebee/llm/tools"
)

func TestElasticSearchMetricsAgent_PreservesEmptyResultEvidence(t *testing.T) {
	tests := []struct {
		name    string
		result  string
		want    []string
		notWant []string
	}{
		{
			name:    "missing field",
			result:  `{"results":[{"query":"{\"term\":{\"missing.field\":\"x\"}}","payload":[],"docs_matched":0,"note":"The query references fields that do not exist: missing.field."}]}`,
			want:    []string{"missing.field", "do not exist", "0"},
			notWant: []string{"Result: No data found (empty payload)"},
		},
		{
			name:    "documents matched but extraction failed",
			result:  `{"results":[{"query":"{\"match_all\":{}}","payload":[],"docs_matched":7,"note":"The query matched 7 documents but no metric series could be extracted. This is NOT an absence of data."}]}`,
			want:    []string{"7", "no metric series could be extracted", "NOT an absence of data"},
			notWant: []string{"Result: No data found (empty payload)"},
		},
		{
			name:    "zero matches without field validation",
			result:  `{"results":[{"query":"{\"term\":{\"known.field\":\"absent\"}}","payload":[],"docs_matched":0}]}`,
			want:    []string{"reports 0 matching documents", "does not establish"},
			notWant: []string{"no metric series could be extracted", "do not exist"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := (ElasticSearchMetricsAgent{}).UpdateToolResponseForPlanner(
				core.NBAgentPlannerToolAction{Tool: tools.ToolESMetricsQuery}, tc.result,
			)
			for _, s := range tc.want {
				if !strings.Contains(got, s) {
					t.Errorf("response missing %q: %s", s, got)
				}
			}
			for _, s := range tc.notWant {
				if strings.Contains(got, s) {
					t.Errorf("response contains misleading %q: %s", s, got)
				}
			}
		})
	}
}

func TestElasticSearchMetricsAgent_PreservesWarningWithNonemptySeries(t *testing.T) {
	result := `{"results":[{"query":"{\"must_not\":[{\"exists\":{\"field\":\"missing.field\"}}]}","payload":[{"metric":{"__name__":"CPUUtilization"},"values":[10],"timestamps":[100],"num_points":1}],"total_series":20,"docs_matched":12,"note":"The query references fields that do not exist: missing.field.","capped_notice":"Showing 1 of 20 series."}]}`
	got := (ElasticSearchMetricsAgent{}).UpdateToolResponseForPlanner(
		core.NBAgentPlannerToolAction{Tool: tools.ToolESMetricsQuery}, result,
	)
	for _, want := range []string{"Series count: 20 (showing 1)", "reports 12 matching documents", "count may be a lower bound", "missing.field", "Showing 1 of 20 series"} {
		if !strings.Contains(got, want) {
			t.Errorf("response missing %q: %s", want, got)
		}
	}
}

func TestElasticSearchMetricsAgent_DoesNotPresentMatchLimitAsExact(t *testing.T) {
	result := `{"results":[{"query":"{\"query\":{\"match_all\":{}}}","payload":[],"docs_matched":10000}]}`
	got := (ElasticSearchMetricsAgent{}).UpdateToolResponseForPlanner(
		core.NBAgentPlannerToolAction{Tool: tools.ToolESMetricsQuery}, result,
	)
	if !strings.Contains(got, "reports 10000 matching documents (the count may be a lower bound)") {
		t.Errorf("response presented Elasticsearch's 10000 threshold as exact: %s", got)
	}
}

func TestElasticSearchMetricsAgent_PromptKeepsUserPathsAndPriorMatches(t *testing.T) {
	prompt := (ElasticSearchMetricsAgent{}).buildSystemPrompt(nil, []string{"metrics-*"}, "", nil)
	body := strings.Join(prompt.Instructions, "\n") + "\n" + strings.Join(prompt.Constraints, "\n")
	for _, want := range []string{
		"use it verbatim in the first query",
		"not evidence that the source index uses those labels as field paths",
		"a later zero does not erase previously observed matches",
		"with no user-supplied exact field path",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}
