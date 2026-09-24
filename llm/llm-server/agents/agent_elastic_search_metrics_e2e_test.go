//go:build e2e

package agents

import (
	"encoding/json"
	"fmt"
	"nudgebee/llm/agents/core"
	"nudgebee/llm/security"
	"nudgebee/llm/tools"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestElasticSearchMetricsAgent_Execute(t *testing.T) {
	sc := security.NewRequestContextForSuperAdmin()
	accountId := os.Getenv("TEST_ES_METRICS_ACCOUNT")
	userId := os.Getenv("TEST_USER")

	if accountId == "" || userId == "" {
		t.Skip("skipping: TEST_ES_METRICS_ACCOUNT and TEST_USER must be set")
	}

	testCases := []struct {
		Query         string
		ExpectedField string
		ExpectedIndex string
		ExpectedDSL   string
	}{
		// {
		// 	Query: "What metrics are available in the demo namespace over the last hour? Summarize the metric names and their latest values",
		// },
		{
			Query: "Show the latest CPU usage for up to 3 pods in the nudgebee namespace over the last 24 hours. One metrics query is enough; report only the series returned.",
		},
		{
			Query:         "Use the exact Elasticsearch index metrics-kubernetes.* and field path kubernetes.pod.cpu.usage.nanocores to show CPU usage for pods in namespace nudgebee over the last 24 hours. Report only observed series.",
			ExpectedField: "kubernetes.pod.cpu.usage.nanocores",
		},
		{
			Query: `Execute this exact Elasticsearch query against index metrics-kubernetes.*. Do not change the DSL or substitute field paths. Report only matching observations from this query.
{"_source":["@timestamp","kubernetes.namespace","kubernetes.pod.name","kubernetes.pod.cpu.usage.nanocores"],"query":{"bool":{"filter":[{"exists":{"field":"kubernetes.pod.cpu.usage.nanocores"}},{"term":{"kubernetes.namespace":"nudgebee"}},{"range":{"@timestamp":{"gte":"now-24h"}}}]}},"size":10}`,
			ExpectedField: "kubernetes.pod.cpu.usage.nanocores",
			ExpectedIndex: "metrics-kubernetes.*",
			ExpectedDSL:   `{"_source":["@timestamp","kubernetes.namespace","kubernetes.pod.name","kubernetes.pod.cpu.usage.nanocores"],"query":{"bool":{"filter":[{"exists":{"field":"kubernetes.pod.cpu.usage.nanocores"}},{"term":{"kubernetes.namespace":"nudgebee"}},{"range":{"@timestamp":{"gte":"now-24h"}}}]}},"size":10}`,
		},
		// {
		// 	Query: "Show me the memory usage trend for the llm-server in nudgebee namespace over the last 7 days",
		// },
	}

	for i, tc := range testCases {
		t.Run(tc.Query, func(t *testing.T) {
			sessionId := fmt.Sprintf("ut-es-metrics-%d", i+1)
			_ = core.DeleteConversationBySession(sessionId, accountId, userId)

			agent, ok := core.GetNBAgent(sc, ElasticSearchMetricsAgentName, accountId, "")
			assert.True(t, ok)
			assert.NotNil(t, agent)

			resp, err := core.HandleConversationSessionRequest(sc, agent, userId, accountId, sessionId, tc.Query)
			require.NoError(t, err, "an E2E run with configured services must complete")

			require.NotNil(t, resp)
			require.Equal(t, core.ConversationStatusCompleted, resp.Status, "agent must finish the turn")
			assert.Equal(t, ElasticSearchMetricsAgentName, resp.AgentName)
			assert.Greater(t, len(resp.Response), 0)
			queryCount, successfulQueries, matchedDocs := 0, 0, 0
			fieldInSuccessfulQuery := false
			firstQueryInput := ""
			for _, invocation := range resp.AgentStepResponse {
				if invocation.Call.FunctionCall == nil || invocation.Call.FunctionCall.Name != tools.ToolESMetricsQuery {
					continue
				}
				queryCount++
				if firstQueryInput == "" {
					firstQueryInput = invocation.Call.FunctionCall.Arguments
				}
				if !invocation.Failed {
					successfulQueries++
					fieldInSuccessfulQuery = fieldInSuccessfulQuery || strings.Contains(invocation.Call.FunctionCall.Arguments, tc.ExpectedField)
					var result struct {
						Results []struct {
							DocsMatched int `json:"docs_matched"`
						} `json:"results"`
					}
					if json.Unmarshal([]byte(invocation.Response.Content), &result) == nil {
						for _, item := range result.Results {
							matchedDocs += item.DocsMatched
						}
					}
				}
			}
			require.Positive(t, queryCount, "agent must execute a metrics query")
			require.Positive(t, successfulQueries, "a response after only failed metrics queries is not an E2E success")
			if tc.ExpectedField != "" {
				require.True(t, fieldInSuccessfulQuery, "a successful ES query must retain the user's exact field path")
			}
			if tc.ExpectedDSL != "" {
				var actual struct {
					Index string          `json:"index"`
					Query json.RawMessage `json:"query"`
				}
				require.NoError(t, json.Unmarshal([]byte(firstQueryInput), &actual))
				require.Equal(t, tc.ExpectedIndex, actual.Index)
				require.JSONEq(t, tc.ExpectedDSL, string(actual.Query), "first ES query must match user DSL")
				require.Positive(t, matchedDocs, "the exact query must match documents")
			}
		})
	}
}
