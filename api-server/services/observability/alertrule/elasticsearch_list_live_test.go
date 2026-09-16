//go:build liveKibana

// Live verification of the Kibana rule-listing contract against a REAL Kibana,
// not an httptest fake: that _find returns the rule's `params.esQuery`, and that
// kibanaRuleToExternalRule surfaces it as the rule's Query.
//
// This is what makes a provider sync worth running for Elasticsearch. The
// webhook path can only set event_rules.expr from `{{context.conditions}}`,
// which Kibana renders per alert instance with the group name baked into the
// sentence, so the first instance to fire pins the expression. The sync is the
// only source of the rule's actual query — if Query came back empty or as the
// whole params blob, the sync would have nothing better to write.
//
// Run against a reachable Kibana, e.g. via port-forward:
//
//	kubectl -n elasticsearch port-forward svc/kibana-kb-http 5601:5601
//	KIBANA_LIVE_URL=https://localhost:5601 KIBANA_LIVE_USER=elastic \
//	  KIBANA_LIVE_PASSWORD=... KIBANA_LIVE_INSECURE=true \
//	  go test -tags liveKibana ./observability/alertrule/ -run TestLiveKibana -v
package alertrule

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func liveKibanaConfig(t *testing.T) *elasticsearchConfig {
	t.Helper()
	url := os.Getenv("KIBANA_LIVE_URL")
	if url == "" {
		t.Skip("KIBANA_LIVE_URL not set")
	}
	return &elasticsearchConfig{
		KibanaUrl:     strings.TrimRight(url, "/"),
		Username:      os.Getenv("KIBANA_LIVE_USER"),
		Password:      os.Getenv("KIBANA_LIVE_PASSWORD"),
		ApiKey:        os.Getenv("KIBANA_LIVE_API_KEY"),
		TlsSkipVerify: os.Getenv("KIBANA_LIVE_INSECURE") == "true",
	}
}

// TestLiveKibanaRuleQueryIsTheEsQuery is the assertion the sync depends on: a
// listed .es-query rule must carry its own search as Query, not an empty string
// and not the entire params object.
func TestLiveKibanaRuleQueryIsTheEsQuery(t *testing.T) {
	cfg := liveKibanaConfig(t)

	resp, err := fetchKibanaRulePage(
		cfg.KibanaUrl+"/api/alerting/rules/_find?per_page=100&page=1",
		kibanaHeaders(cfg), cfg.TlsSkipVerify)
	require.NoError(t, err)
	require.NotEmpty(t, resp.Data, "no Kibana rules to verify against")

	checked := 0
	for _, r := range resp.Data {
		if r.RuleTypeId != ".es-query" {
			continue
		}
		external := kibanaRuleToExternalRule(r)
		t.Logf("rule %q (%s)\n  Query:     %s\n  AlertType: %s\n  Duration:  %s",
			r.Name, r.Id, external.Query, external.AlertType, external.Duration)

		// The rule's own search, verbatim from params.esQuery.
		raw, ok := r.Params["esQuery"].(string)
		require.True(t, ok, "rule %q has no string params.esQuery", r.Name)
		assert.Equal(t, raw, external.Query,
			"rule %q: Query must be the rule's esQuery", r.Name)

		// Distinguish it from the params-blob fallback: a whole params object
		// would also be non-empty, and would silently look like success.
		assert.NotContains(t, external.Query, "thresholdComparator",
			"rule %q: Query fell back to the whole params object", r.Name)
		var parsed map[string]any
		assert.NoError(t, json.Unmarshal([]byte(external.Query), &parsed),
			"rule %q: Query should be the JSON query body", r.Name)
		assert.Contains(t, parsed, "query", "rule %q: Query lacks a `query` key", r.Name)

		// An .es-query rule counts documents, and the sync writes the rule's
		// evaluation interval as duration — both feed the same event_rules row.
		assert.Equal(t, "log", external.AlertType, "rule %q", r.Name)
		assert.NotEmpty(t, external.Duration, "rule %q: duration should carry the schedule interval", r.Name)

		checked++
	}
	require.Positive(t, checked, "no .es-query rules found to verify")
	t.Logf("verified %d .es-query rule(s)", checked)
}
