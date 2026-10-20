package query

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nudgebee/services/security"
)

const (
	fast = "fast" // no CTE at all
	lean = "lean" // ROW_NUMBER() window, no cloud_resourses join
	full = "full" // window + both joins + display projection
)

// route classifies which of the three recommendation_groupings_v2 subquery
// shapes a request takes, by inspecting the generated SQL.
func route(t *testing.T, request QueryRequest) string {
	t.Helper()
	request.Table = "recommendation_groupings_v2"
	sql, err := GenerateSqlQuery(security.NewRequestContextForSuperAdmin(nil, nil, nil), "", request, table_metadata["recommendation_groupings_v2"])
	require.NoError(t, err)
	switch {
	case strings.Contains(sql, "cloud_resourses"):
		return full
	case strings.Contains(sql, "ROW_NUMBER"):
		return lean
	default:
		return fast
	}
}

// The recommendation_groupings_v2 DefGenerator picks one of three subquery
// shapes from the requested columns. Getting that choice wrong is silent at
// compile time and fails at runtime with `column "<x>" does not exist`, so the
// routing is pinned here: any column whose Def is only available from the
// cloud_resourses / cloud_accounts joins MUST appear in joinRequiringCols.
func TestRecommendationGroupingsRouting(t *testing.T) {
	tests := []struct {
		name    string
		request QueryRequest
		want    string
	}{
		{"count only takes the fast path", QueryRequest{Columns: cols("count")}, fast},
		{"savings roll-up needs the window but no join", QueryRequest{Columns: cols("count", "sum_estimated_savings")}, lean},
		{"is_primary_recommendation in where needs the window", QueryRequest{
			Columns: cols("count"),
			Where:   QueryWhereClause{Binary: BinaryWhereClause{"is_primary_recommendation": {Eq: true}}},
		}, lean},
		{"a display column forces the join", QueryRequest{Columns: cols("count", "sum_estimated_savings", "resource_cloud_service")}, full},
		{"resource_names aggregates resource_name, so it forces the join", QueryRequest{
			Columns: cols("vuln_id", "count", "resource_names"),
			GroupBy: []string{"vuln_id"},
		}, full},
		{"a namespace-scoped role's injected filter forces the join", QueryRequest{
			Columns: cols("count", "sum_estimated_savings"),
			Where: QueryWhereClause{And: []QueryWhereClause{
				{Binary: BinaryWhereClause{"resource_k8s_namespace": {In: []string{"default"}}}},
			}},
		}, full},
		{"grouping by a display column forces the join", QueryRequest{
			Columns: cols("count", "resource_region"),
			GroupBy: []string{"resource_region"},
		}, full},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, route(t, tc.request))
		})
	}
}

func TestRecommendationGroupings_VMPackageFieldsUseVulnerabilityJoin(t *testing.T) {
	request := QueryRequest{
		Table:   "recommendation_groupings_v2",
		Columns: cols("package_name", "recommendation", "count"),
		GroupBy: []string{"package_name"},
	}
	sql, err := GenerateSqlQuery(security.NewRequestContextForSuperAdmin(nil, nil, nil), "", request, table_metadata["recommendation_groupings_v2"])
	require.NoError(t, err)
	assert.Contains(t, sql, "LEFT JOIN vulnerabilities v ON v.id = r.vulnerability_id")
	assert.Contains(t, sql, "r1.v_package_name")
	assert.Contains(t, sql, "r1.recommendation - 'package_version'")
}

// See the DefGenerator comment: severity pushdown is only safe for
// rule_name='image_scan'.
func TestRecommendationGroupings_SeverityPushdownGatedToImageScanOnly(t *testing.T) {
	displayCols := cols("count", "resource_cloud_service", "severity")

	pushedDown := QueryRequest{
		Table:   "recommendation_groupings_v2",
		Columns: displayCols,
		Where: QueryWhereClause{Binary: BinaryWhereClause{
			"rule_name": {Eq: "image_scan"},
			"severity":  {Eq: "High"},
		}},
	}
	sql, err := GenerateSqlQuery(security.NewRequestContextForSuperAdmin(nil, nil, nil), "", pushedDown, table_metadata["recommendation_groupings_v2"])
	require.NoError(t, err)
	assert.Contains(t, sql, "r.severity = 'High'", "rule_name='image_scan' should push severity into the CTE")

	notPushedCases := []struct {
		name       string
		ruleFilter map[BinaryWhereClauseType]any
	}{
		{"rule_name IN list including other rules", map[BinaryWhereClauseType]any{In: []any{"image_scan", "vm_package_vulnerability"}}},
		{"rule_name = vm_package_vulnerability", map[BinaryWhereClauseType]any{Eq: "vm_package_vulnerability"}},
		{"no rule_name filter at all", nil},
	}
	for _, tc := range notPushedCases {
		t.Run(tc.name, func(t *testing.T) {
			binary := BinaryWhereClause{"severity": {Eq: "High"}}
			if tc.ruleFilter != nil {
				binary["rule_name"] = tc.ruleFilter
			}
			request := QueryRequest{
				Table:   "recommendation_groupings_v2",
				Columns: displayCols,
				Where:   QueryWhereClause{Binary: binary},
			}
			sql, err := GenerateSqlQuery(security.NewRequestContextForSuperAdmin(nil, nil, nil), "", request, table_metadata["recommendation_groupings_v2"])
			require.NoError(t, err)
			assert.NotContains(t, sql, "r.severity", "severity must stay out of the CTE unless rule_name is exactly 'image_scan'")
		})
	}
}

// See the DefGenerator comment: status/rule_name pushdown is only safe on the
// fast path, or for rule_name='image_scan'.
func TestRecommendationGroupings_StatusAndRuleNamePushdownGatedToWindowSafety(t *testing.T) {
	displayCols := cols("count", "resource_cloud_service", "status", "rule_name")

	windowedCases := []struct {
		name   string
		where  BinaryWhereClause
		wantIn string // a literal expected to survive somewhere (outer WHERE)
	}{
		{"status filter on a windowed request", BinaryWhereClause{"status": {Eq: "Archive"}}, "Archive"},
		{"rule_name filter (not image_scan) on a windowed request", BinaryWhereClause{"rule_name": {Eq: "vm_package_vulnerability"}}, "vm_package_vulnerability"},
	}
	for _, tc := range windowedCases {
		t.Run(tc.name, func(t *testing.T) {
			// Not route()+GenerateSqlQuery: Where.Binary is a map, so route()'s
			// call would consume the filter before this one runs.
			request := QueryRequest{
				Table:   "recommendation_groupings_v2",
				Columns: displayCols,
				Where:   QueryWhereClause{Binary: tc.where},
			}
			sql, err := GenerateSqlQuery(security.NewRequestContextForSuperAdmin(nil, nil, nil), "", request, table_metadata["recommendation_groupings_v2"])
			require.NoError(t, err)
			require.Contains(t, sql, "ROW_NUMBER", "expected this request shape to take a windowed (lean/full) path")
			// (bare "r.status" also appears in the window's own ORDER BY tie-break)
			assert.NotContains(t, sql, "r.status = 'Archive'", "status must stay out of the CTE's WHERE on a windowed path")
			assert.NotContains(t, sql, "r.rule_name", "rule_name must stay out of the CTE on a windowed path unless it's exactly 'image_scan'")
			assert.Contains(t, sql, tc.wantIn, "the filter must still be applied somewhere (the outer WHERE), not silently dropped")
		})
	}

	t.Run("rule_name='image_scan' is still pushed on a windowed request", func(t *testing.T) {
		request := QueryRequest{
			Table:   "recommendation_groupings_v2",
			Columns: displayCols,
			Where:   QueryWhereClause{Binary: BinaryWhereClause{"rule_name": {Eq: "image_scan"}}},
		}
		sql, err := GenerateSqlQuery(security.NewRequestContextForSuperAdmin(nil, nil, nil), "", request, table_metadata["recommendation_groupings_v2"])
		require.NoError(t, err)
		require.Contains(t, sql, "ROW_NUMBER", "expected this request shape to take a windowed (lean/full) path")
		assert.Contains(t, sql, "r.rule_name = 'image_scan'")
	})

	t.Run("status and rule_name both push down on the fast path", func(t *testing.T) {
		request := QueryRequest{
			Table:   "recommendation_groupings_v2",
			Columns: cols("count"),
			Where: QueryWhereClause{Binary: BinaryWhereClause{
				"status":    {Eq: "Archive"},
				"rule_name": {Eq: "vm_package_vulnerability"},
				"severity":  {Eq: "High"},
			}},
		}
		sql, err := GenerateSqlQuery(security.NewRequestContextForSuperAdmin(nil, nil, nil), "", request, table_metadata["recommendation_groupings_v2"])
		require.NoError(t, err)
		require.NotContains(t, sql, "ROW_NUMBER", "expected this request shape to take the fast path")
		assert.Contains(t, sql, "r.status = 'Archive'")
		assert.Contains(t, sql, "r.rule_name = 'vm_package_vulnerability'")
		assert.Contains(t, sql, "r.severity = 'High'")
	})
}

// Regression test for a missing-comma bug in the fast path's SELECT list: when
// a request needs the vulnerabilities join (package_name/vuln_id/recommendation)
// but no join- or window-requiring column, the fast path used to splice vulnCols
// between `r.*,` and `TRUE AS is_primary_recommendation` with no separating
// comma, producing a Postgres syntax error ("Unable to execute query" at the API
// boundary — the real cause was masked). This is exactly the request shape the VM
// dashboard's "Most affected packages" / "Most common vulnerabilities" panels send.
func TestRecommendationGroupings_FastPathVulnJoinHasNoMissingComma(t *testing.T) {
	tests := []struct {
		name    string
		request QueryRequest
	}{
		{"group by package_name", QueryRequest{
			Columns: cols("package_name", "count", "max_severity_weight"),
			GroupBy: []string{"package_name"},
		}},
		{"group by vuln_id", QueryRequest{
			Columns: cols("vuln_id", "count", "max_severity_weight"),
			GroupBy: []string{"vuln_id"},
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, fast, route(t, tc.request), "expected this request shape to take the fast path")

			sql, err := GenerateSqlQuery(security.NewRequestContextForSuperAdmin(nil, nil, nil), "", tc.request, table_metadata["recommendation_groupings_v2"])
			require.NoError(t, err)
			assert.Regexp(t, `,\s*TRUE AS is_primary_recommendation`, sql, "TRUE AS is_primary_recommendation must be preceded by a comma")
		})
	}
}
