package reports

import (
	"strings"
	"testing"

	"nudgebee/services/query"
)

// The report reads its rules from one list. Both consumers must use it, and
// every entry must be spelled the way producers write rule_name — a hyphenated
// legacy spelling matches no row and silently empties that rule from the
// report.
func TestOptimizationRuleNames(t *testing.T) {
	if len(optimizationRuleNames) == 0 {
		t.Fatal("the report would cover no rules at all")
	}

	seen := map[string]bool{}
	for _, name := range optimizationRuleNames {
		if strings.Contains(name, "-") {
			t.Errorf("%q is a legacy hyphenated spelling and matches no row; producers write snake_case", name)
		}
		if seen[name] {
			t.Errorf("%q is listed twice", name)
		}
		seen[name] = true
	}
}

// Spelled out so that renaming a rule is a deliberate, reviewed edit rather
// than a one-character slip that silently empties the report. Each name is the
// rule_name its producer writes.
func TestOptimizationRuleNamesAreTheOnesProducersWrite(t *testing.T) {
	want := []string{
		"pod_right_sizing",
		"unused_pvc",
		"pv_rightsize",
		"replica_right_sizing",
		"abandoned_resource",
		"eks_cluster_upgrade",
	}

	if len(optimizationRuleNames) != len(want) {
		t.Fatalf("report covers %d rules, expected %d: %q", len(optimizationRuleNames), len(want), optimizationRuleNames)
	}
	for i, name := range want {
		if optimizationRuleNames[i] != name {
			t.Errorf("rule %d is %q, expected %q", i, optimizationRuleNames[i], name)
		}
	}
}

func TestPrepareRecommendationQueryFiltersOnTheSharedRuleList(t *testing.T) {
	q := prepareRecommendationQuery("tenant", nil)

	var filtered []string
	for _, clause := range q.Where.And {
		if in, ok := clause.Binary["rule_name"][query.In]; ok {
			names, ok := in.([]string)
			if !ok {
				t.Fatalf("rule_name filter is %T, not a string slice", in)
			}
			filtered = names
		}
	}

	if filtered == nil {
		t.Fatal("the query does not filter on rule_name at all")
	}
	if len(filtered) != len(optimizationRuleNames) {
		t.Fatalf("query filters on %d rules, the shared list has %d", len(filtered), len(optimizationRuleNames))
	}
	for i, name := range optimizationRuleNames {
		if filtered[i] != name {
			t.Errorf("rule %d: query filters on %q, the shared list says %q", i, filtered[i], name)
		}
	}
}
