package observability

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time proof the source satisfies both the required interface and the optional
// playbook one. The optional assertion matters most: PlaybookQueryGenerator is looked up
// by type assertion at runtime, so dropping a method would silently fall back to PromQL
// generation that Splunk cannot execute, with no compile error to catch it.
var (
	_ MetricSource           = (*SplunkEnterpriseMetricSource)(nil)
	_ PlaybookQueryGenerator = (*SplunkEnterpriseMetricSource)(nil)
	// Also looked up by type assertion (wrapAggregator): without it metrics_get_query
	// wraps the rendered SPL in PromQL, `sum(| mstats sum(...) ...)`.
	_ MetricAggregateWrapper = (*SplunkEnterpriseMetricSource)(nil)
)

func TestBuildSplunkMStatsQuery(t *testing.T) {
	tests := []struct {
		name    string
		item    QueryItem
		labels  map[string]string
		span    int
		instant bool
		want    string
	}{
		{
			name: "canonical label is mapped to the OTel dimension and grouped",
			item: QueryItem{
				Metric:        "k8s.pod.cpu.usage",
				LabelMatchers: []LabelMatcher{{Label: "namespace", Operator: "_eq", Value: "nudgebee"}},
			},
			span: 300,
			want: `| mstats avg(k8s.pod.cpu.usage) AS nb_value WHERE index="otel_metrics" AND k8s.namespace.name="nudgebee" span=300s BY k8s.namespace.name`,
		},
		{
			name: "aggregate operator is honoured",
			item: QueryItem{Metric: "k8s.pod.memory.usage", AggregateOperator: "sum"},
			span: 300,
			want: `| mstats sum(k8s.pod.memory.usage) AS nb_value WHERE index="otel_metrics" span=300s`,
		},
		{
			name:    "instant query omits span",
			item:    QueryItem{Metric: "k8s.pod.cpu.usage"},
			instant: true,
			want:    `| mstats avg(k8s.pod.cpu.usage) AS nb_value WHERE index="otel_metrics"`,
		},
		{
			name: "absent span falls back to the default rather than emitting span=0s",
			item: QueryItem{Metric: "k8s.pod.cpu.usage"},
			span: 0,
			want: `| mstats avg(k8s.pod.cpu.usage) AS nb_value WHERE index="otel_metrics" span=60s`,
		},
		{
			name:   "eq labels and matchers combine, sorted for stability",
			item:   QueryItem{Metric: "cpu", LabelMatchers: []LabelMatcher{{Label: "pod", Operator: "_neq", Value: "x"}}},
			labels: map[string]string{"namespace": "demo"},
			span:   60,
			want:   `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" AND k8s.namespace.name="demo" AND k8s.pod.name!="x" span=60s BY k8s.namespace.name, k8s.pod.name`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildSplunkMStatsQuery(tt.item, tt.labels, "otel_metrics", tt.span, tt.instant)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// escapeSplunkString turns a literal `*` into `\*`. A pod-name prefix therefore cannot
// carry its wildcard in the value — it would be escaped and match nothing. Verified
// against Splunk 10.4.2: the unescaped form returned 28 rows, the escaped form none.
func TestSplunkMetricPrefixOperatorKeepsWildcardUnescaped(t *testing.T) {
	got, err := buildSplunkWorkloadCPUQuery("k8s.pod.cpu.usage", "api-server", "nudgebee", "otel_metrics")
	require.NoError(t, err)
	assert.Contains(t, got, `k8s.pod.name="api-server-*"`)
	assert.NotContains(t, got, `\*`, "the trailing wildcard must not be escaped")
}

func TestBuildSplunkMStatsQueryRejectsUnsafeInput(t *testing.T) {
	_, err := buildSplunkMStatsQuery(QueryItem{Metric: `cpu" | delete`}, nil, "otel_metrics", 60, false)
	assert.Error(t, err, "a metric name that could close the aggregation must be rejected")

	_, err = buildSplunkMStatsQuery(QueryItem{Metric: "cpu"}, nil, `bad" index`, 60, false)
	assert.Error(t, err, "an unsafe index name must be rejected")

	_, err = buildSplunkMStatsQuery(
		QueryItem{Metric: "cpu", LabelMatchers: []LabelMatcher{{Label: `bad" dim`, Operator: "_eq", Value: "x"}}},
		nil, "otel_metrics", 60, false)
	assert.Error(t, err, "an unsafe dimension name must be rejected")
}

func TestValidateSplunkEnterpriseMetricQuery(t *testing.T) {
	const index = "otel_metrics"

	t.Run("accepts the generated shapes", func(t *testing.T) {
		for _, spl := range []string{
			`| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" span=60s`,
			`| mcatalog values(metric_name) WHERE index="otel_metrics"`,
			`| mcatalog values(_dims) WHERE index="otel_metrics"`,
		} {
			assert.NoError(t, validateSplunkEnterpriseMetricQuery(spl, index), spl)
		}
	})

	t.Run("requires a leading generating command", func(t *testing.T) {
		err := validateSplunkEnterpriseMetricQuery(`search index="otel_metrics" | head 10`, index)
		assert.Error(t, err)
	})

	t.Run("only mstats and mcatalog may open the query", func(t *testing.T) {
		err := validateSplunkEnterpriseMetricQuery(`| inputlookup creds WHERE index="otel_metrics"`, index)
		assert.Error(t, err)
	})

	// The whole point of the tightened index rule: a leading pipe lets the query pick its
	// own index, so an unscoped one could read anything on the server.
	t.Run("must be scoped to the configured index", func(t *testing.T) {
		err := validateSplunkEnterpriseMetricQuery(`| mstats avg(cpu) AS nb_value WHERE index="secrets" span=60s`, index)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "configured metrics index")
	})

	t.Run("a prefix of the configured index does not satisfy the scope check", func(t *testing.T) {
		err := validateSplunkEnterpriseMetricQuery(`| mstats avg(cpu) AS nb_value WHERE index="otel_metrics_secret" span=60s`, index)
		assert.Error(t, err)
	})

	t.Run("disallowed commands are still rejected downstream", func(t *testing.T) {
		err := validateSplunkEnterpriseMetricQuery(`| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" | outputlookup stolen.csv`, index)
		assert.Error(t, err)
	})

	t.Run("empty is rejected", func(t *testing.T) {
		assert.Error(t, validateSplunkEnterpriseMetricQuery("   ", index))
	})
}

// Each of these passed the guard before: the opening-command check tested segment 0,
// which is always "" for a query starting with "|", and the index check only required
// index="otel_metrics" to appear somewhere.
func TestValidateSplunkEnterpriseMetricQueryRejectsBypasses(t *testing.T) {
	const index = "otel_metrics"
	tests := []struct {
		name    string
		spl     string
		wantErr string
	}{
		{"tstats opening command", `| tstats count WHERE index="otel_metrics" BY sourcetype`, "may only begin with"},
		{"metadata opening command", `| metadata type=sourcetypes index="otel_metrics"`, "may only begin with"},
		{"eventcount opening command", `| eventcount summarize=false index="otel_metrics"`, "may only begin with"},
		{"makeresults opening command", `| makeresults | eval x="index=\"otel_metrics\""`, "may only begin with"},
		{"doubled leading pipe", `|| tstats count WHERE index="otel_metrics"`, "may only begin with"},
		{"later search names another index", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" | search index=_internal`, "may only read the configured metrics index"},
		{"bare index with a shared prefix", `| mstats avg(cpu) AS nb_value WHERE index=otel_metrics_secret`, "may only read the configured metrics index"},
		{"a second index alongside the configured one", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" OR index=otel_metrics_secret`, "may only read the configured metrics index"},
		{"append subsearch naming another index", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" | append [search index=_audit]`, `command "append" is not permitted after mstats`},
		{"append subsearch naming no index reads the role's defaults", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" | append [search sourcetype=*]`, `command "append" is not permitted after mstats`},
		{"subsearch as a filter", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" | search [search index="otel_metrics" | fields k8s.pod.name]`, "subsearches ([ ... ]) are not permitted in a metrics query"},
		{"subsearch inside WHERE", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" AND [search sourcetype=* | fields host]`, "subsearches ([ ... ]) are not permitted in a metrics query"},
		{"macro as a later command", "| mstats avg(cpu) AS nb_value WHERE index=\"otel_metrics\" | `leak_macro`", "is not permitted after mstats"},
		{"macro inside eval", "| mstats avg(cpu) AS nb_value WHERE index=\"otel_metrics\" | eval x=`leak_macro`", "search macros (`...`) are not permitted in a metrics query"},
		// Commands that read data without naming an index take their scope from the
		// server (a data model, a lookup, a saved search), so no index check sees them.
		{"tstats over a data model (review repro)", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" | tstats count from datamodel=Authentication`, `command "tstats" is not permitted after mstats in a metrics query`},
		{"from a data model (review repro)", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" | from datamodel:Authentication`, `command "from" is not permitted after mstats in a metrics query`},
		{"pivot", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" | pivot Authentication Authentication count(Authentication) AS n`, `command "pivot" is not permitted after mstats`},
		{"datamodel", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" | datamodel Authentication search`, `command "datamodel" is not permitted after mstats`},
		{"lookup command", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" BY k8s.pod.name | lookup users.csv user AS k8s.pod.name`, `command "lookup" is not permitted after mstats`},
		{"join", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" BY k8s.pod.name | join k8s.pod.name [search index="otel_metrics"]`, `command "join" is not permitted after mstats`},
		{"appendpipe", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" | appendpipe [stats count]`, `command "appendpipe" is not permitted after mstats`},
		{"a second mstats", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" | mstats append=t avg(mem) AS nb_value WHERE index="otel_metrics"`, `command "mstats" is not permitted after mstats`},
		{"metadata after mcatalog", `| mcatalog values(metric_name) WHERE index="otel_metrics" | metadata type=hosts`, `command "metadata" is not permitted after mcatalog`},
		{"eventcount", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" | eventcount summarize=false`, `command "eventcount" is not permitted after mstats`},
		{"the denylist still answers first", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" | outputlookup stolen.csv`, "this integration may only read"},
		{"eval lookup() reads a lookup table", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" | eval pw=lookup("users.csv", json_object("user", "admin"), json_array("password"))`, "lookup() function is not permitted"},
		{"macro inside WHERE", "| mstats avg(cpu) AS nb_value WHERE index=\"otel_metrics\" AND `scope`", "search macros (`...`) are not permitted in a metrics query"},
		{"OR NOT the configured index", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" OR NOT index="otel_metrics"`, "negated index selector"},
		{"lower-case not", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" OR not index="otel_metrics"`, "negated index selector"},
		{"NOT without a space before the group", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" OR NOT(index="otel_metrics")`, "negated index selector"},
		{"NOT over a group containing the selector", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" OR NOT (k8s.pod.name="x" OR (index="otel_metrics"))`, "negated index selector"},
		{"bang negation", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" | where !(index="otel_metrics")`, "negated index selector"},
		{"negated indexed-field syntax", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" | search NOT index::otel_metrics`, "negated index selector"},
		{"spaces around the operator", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" | search index = _audit`, "may only read the configured metrics index"},
		{"double equals", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" | search index==_audit`, "may only read the configured metrics index"},
		{"indexed-field syntax", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" | search index::_internal`, "may only read the configured metrics index"},
		{"quoted field name", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" | search "index"=_internal`, "may only read the configured metrics index"},
		{"single-quoted field name", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" | where 'index'="_audit"`, "may only read the configured metrics index"},
		{"upper case", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" | search INDEX=_audit`, "may only read the configured metrics index"},
		{"wildcard", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics*"`, "may only read the configured metrics index"},
		{"negated", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" | search index!=otel_metrics`, "may only read the configured metrics index"},
		{"IN list", `| mstats avg(cpu) AS nb_value WHERE index IN ("otel_metrics", "_metrics")`, "may only read the configured metrics index"},
		// IN followed by any whitespace, not only a space, is still the IN operator.
		{"IN list after a tab", "| mstats avg(cpu) AS nb_value WHERE index=\"otel_metrics\" OR index IN\t(\"_metrics\")", "may only read the configured metrics index"},
		{"IN list after a newline", "| mstats avg(cpu) AS nb_value WHERE index=\"otel_metrics\" OR index IN\n(\"_metrics\")", "may only read the configured metrics index"},
		{"comparison", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" | search index>_a`, "may only read the configured metrics index"},
		{"apostrophes cannot hide a selector", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" | eval a=don't | search index=_audit | eval b='x`, "may only read the configured metrics index"},
		{"configured literal only inside a string", `| mstats avg(cpu) AS nb_value WHERE metric_name="cpu" | eval x="index=\"otel_metrics\""`, "must be scoped to the configured metrics index"},
		{"configured index only in bare form", `| mstats avg(cpu) AS nb_value WHERE index=otel_metrics`, "must be scoped to the configured metrics index"},
		{"unterminated quote", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" | search "x index=_audit`, "unterminated quote"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSplunkEnterpriseMetricQuery(tt.spl, index)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// The shapes real callers send must keep passing: builder output (with and without the
// newest-first sort), the raw SPL the UI Code tab and the llm splunk_metrics_query tool
// send, and every catalog query this source issues.
func TestValidateSplunkEnterpriseMetricQueryAcceptsRealShapes(t *testing.T) {
	const index = "otel_metrics"
	builder, err := buildSplunkMStatsQuery(QueryItem{
		Metric:        "k8s.pod.cpu.usage",
		LabelMatchers: []LabelMatcher{{Label: "workload", Operator: "_like", Value: "%check%"}},
	}, map[string]string{"namespace": "nudgebee"}, index, 3600, false)
	require.NoError(t, err)
	sorted, _ := splunkEnterpriseExecutableSPL(builder)
	workload, err := buildSplunkWorkloadCPUQuery("k8s.pod.cpu.usage", "api-server", "nudgebee", index)
	require.NoError(t, err)

	for _, spl := range []string{
		builder,
		sorted,
		workload,
		`| mstats avg(k8s.pod.cpu.usage) AS nb_value WHERE index="otel_metrics" AND k8s.namespace.name="default" span=1m BY k8s.pod.name`,
		`| mstats avg(k8s.pod.cpu.usage) AS nb_value WHERE index="otel_metrics" span=60s BY k8s.pod.name | sort 0 - _time`,
		`| mstats avg(k8s.pod.cpu.usage) AS nb_value WHERE index = "otel_metrics" BY index`,
		`| mstats avg(k8s.pod.cpu.usage) AS nb_value WHERE index="otel_metrics" AND k8s.pod.name="index=_audit" | search index::otel_metrics`,
		`| mstats max(k8s.pod.memory.usage) AS nb_value WHERE index="otel_metrics" AND my.index.name="x" BY k8s.pod.name | eval nb_value=nb_value/1024`,
		`| mcatalog values(metric_name) WHERE index="otel_metrics"`,
		`| mcatalog values(_dims) WHERE index="otel_metrics" AND metric_name="k8s.pod.*"`,
		`| mcatalog values(k8s.deployment.name) WHERE index="otel_metrics" AND metric_name="k8s.node.cpu.usage"`,
		`| mcatalog values(metric_name) WHERE index="otel_metrics" AND k8s.namespace.name="nudgebee" AND k8s.pod.name="api-server-*"`,
		// Negating a dimension is fine; only a negated index selector widens the scope.
		`| mstats avg(k8s.pod.cpu.usage) AS nb_value WHERE index="otel_metrics" AND NOT k8s.namespace.name="kube-system" span=60s`,
		`| mstats avg(k8s.pod.cpu.usage) AS nb_value WHERE NOT (k8s.pod.name="a") AND index="otel_metrics" AND (k8s.pod.name="b" OR k8s.pod.name="c")`,
		// Brackets and backticks inside a quoted value are text, not syntax.
		"| mstats avg(k8s.pod.cpu.usage) AS nb_value WHERE index=\"otel_metrics\" AND k8s.pod.name=\"a[b]`c`\"",
		// The UI Code-tab placeholder (QueryModeSwitcher.jsx) and the llm agent's prompt
		// examples (agent_splunk_enterprise_metrics.go).
		`| mstats avg(k8s.pod.cpu.usage) WHERE index="otel_metrics" AND k8s.namespace.name="prod" span=1m BY k8s.pod.name`,
		`| mstats avg(k8s.pod.cpu.usage) AS nb_value WHERE index="otel_metrics" AND k8s.namespace.name="nudgebee" AND k8s.pod.name="api-server*" span=1m BY k8s.pod.name`,
		`| mstats max(k8s.pod.memory.usage) AS nb_value WHERE index="otel_metrics" AND k8s.namespace.name="demo" BY k8s.pod.name`,
		// Value-only transforms after the opening command.
		`| mstats avg(k8s.pod.cpu.usage) AS nb_value WHERE index="otel_metrics" span=5m BY k8s.pod.name | where nb_value > 0.5 | eval nb_value=round(nb_value*1000, 1) | sort 0 - nb_value | head 10`,
		`| mstats avg(k8s.pod.cpu.usage) AS v WHERE index="otel_metrics" span=1m BY k8s.pod.name | stats max(v) AS nb_value BY k8s.pod.name | rename k8s.pod.name AS pod | table pod nb_value`,
		`| mstats avg(k8s.pod.cpu.usage) AS nb_value WHERE index="otel_metrics" span=1m BY k8s.pod.name | eventstats avg(nb_value) AS mean | streamstats count | fillnull value=0 | dedup k8s.pod.name | reverse | tail 5`,
		`| mstats avg(k8s.pod.cpu.usage) AS v WHERE index="otel_metrics" span=1m BY k8s.pod.name | timechart span=5m avg(v) AS nb_value BY k8s.pod.name`,
		`| mstats avg(k8s.pod.cpu.usage) AS v WHERE index="otel_metrics" span=1m BY k8s.pod.name | bin _time span=10m | chart avg(v) AS nb_value OVER _time BY k8s.pod.name`,
		`| mstats count(k8s.pod.cpu.usage) AS nb_value WHERE index="otel_metrics" BY k8s.pod.name | top limit=3 k8s.pod.name | rare k8s.pod.name | convert num(nb_value) | fields - percent | bucket _time span=1h | search k8s.pod.name="api*"`,
	} {
		assert.NoError(t, validateSplunkEnterpriseMetricQuery(spl, index), spl)
	}
}

func TestConvertSplunkMetricRows(t *testing.T) {
	t.Run("groups rows into one series per dimension set", func(t *testing.T) {
		rows := []map[string]any{
			{"_time": "2026-08-27T15:20:00.000+00:00", "k8s.namespace.name": "nudgebee", "nb_value": "1.5"},
			{"_time": "2026-08-27T15:25:00.000+00:00", "k8s.namespace.name": "nudgebee", "nb_value": "2.5"},
			{"_time": "2026-08-27T15:20:00.000+00:00", "k8s.namespace.name": "demo", "nb_value": "9"},
		}
		got := convertSplunkMetricRows("cpu", "spl", rows, 1700000000)
		require.Len(t, got.Payload, 2)

		byNamespace := map[string]Result{}
		for _, r := range got.Payload {
			byNamespace[r.Metric["k8s.namespace.name"]] = r
		}
		assert.Equal(t, []float64{1.5, 2.5}, byNamespace["nudgebee"].Values)
		assert.Len(t, byNamespace["nudgebee"].Timestamps, 2)
		assert.Equal(t, []float64{9}, byNamespace["demo"].Values)
	})

	// Stamped with the window's end, not "now": an instant value over last week's window
	// describes last week.
	t.Run("instant rows carry no _time and are stamped with the window end", func(t *testing.T) {
		rows := []map[string]any{{"k8s.deployment.name": "checkout", "nb_value": "1.38"}}
		got := convertSplunkMetricRows("cpu", "spl", rows, 1700000000)
		require.Len(t, got.Payload, 1)
		assert.Equal(t, []float64{1.38}, got.Payload[0].Values)
		assert.Equal(t, []int64{1700000000000}, got.Payload[0].Timestamps)
	})

	// A null bucket must not become a zero: zero is a measurement, absence is not.
	t.Run("non-numeric values are skipped rather than zeroed", func(t *testing.T) {
		rows := []map[string]any{
			{"_time": "2026-08-27T15:20:00.000+00:00", "nb_value": "1"},
			{"_time": "2026-08-27T15:25:00.000+00:00", "nb_value": ""},
			{"_time": "2026-08-27T15:30:00.000+00:00", "nb_value": "3"},
		}
		got := convertSplunkMetricRows("cpu", "spl", rows, 1700000000)
		require.Len(t, got.Payload, 1)
		assert.Equal(t, []float64{1, 3}, got.Payload[0].Values)
	})

	t.Run("internal columns are not leaked as labels", func(t *testing.T) {
		rows := []map[string]any{
			{"_time": "2026-08-27T15:20:00.000+00:00", "_span": "60", "k8s.namespace.name": "demo", "nb_value": "1"},
		}
		got := convertSplunkMetricRows("cpu", "spl", rows, 1700000000)
		require.Len(t, got.Payload, 1)
		assert.Equal(t, map[string]string{"k8s.namespace.name": "demo"}, got.Payload[0].Metric)
	})

	t.Run("rows with no value column report why the payload is empty", func(t *testing.T) {
		rows := []map[string]any{{"_time": "2026-08-27T15:20:00.000+00:00", "other": "x"}}
		got := convertSplunkMetricRows("cpu", "spl", rows, 1700000000)
		assert.Empty(t, got.Payload)
		require.NotNil(t, got.DocsMatched)
		assert.Equal(t, int64(1), *got.DocsMatched)
		assert.NotEmpty(t, got.Note)
	})
}

// mcatalog returns a single row whose column is an array. Joining it with commas (as the
// display formatter does) would split any value containing a comma in two.
func TestSplunkEnterpriseCatalogValues(t *testing.T) {
	rows := []map[string]any{
		{"values(metric_name)": []any{"k8s.pod.cpu.usage", "k8s.pod.memory.usage", "k8s.pod.cpu.usage"}},
	}
	got := splunkEnterpriseCatalogValues(rows, "values(metric_name)")
	assert.Equal(t, []string{"k8s.pod.cpu.usage", "k8s.pod.memory.usage"}, got, "duplicates collapse, order is stable")

	assert.Empty(t, splunkEnterpriseCatalogValues(rows, "values(missing)"))
}

func TestRankSplunkCPUMetricNames(t *testing.T) {
	metrics := []OutputMetrics{
		{Metric: "k8s.pod.memory.usage"},
		{Metric: "container.cpu.usage"},
		{Metric: "k8s.pod.network.rx"},
		{Metric: "k8s.pod.cpu.usage"},
	}
	got := rankSplunkCPUMetricNames(metrics)

	require.NotEmpty(t, got)
	assert.Equal(t, "k8s.pod.cpu.usage", got[0], "the most specific spelling must be probed first")
	assert.Contains(t, got, "container.cpu.usage")
	assert.NotContains(t, got, "k8s.pod.memory.usage", "non-CPU metrics must not be probed")
	assert.NotContains(t, got, "k8s.pod.network.rx")
}

func TestRankSplunkCPUMetricNamesEmptyWhenNothingMatches(t *testing.T) {
	got := rankSplunkCPUMetricNames([]OutputMetrics{{Metric: "disk.io"}, {Metric: "net.rx"}})
	assert.Empty(t, got, "no CPU-like metric means discovery must fail loudly, not probe everything")
}

// Every function on the right was run against Splunk 10.4 over otel_metrics and returned a
// value. The old mapping sent all of the rate/stddev/percentile/latest ones to avg.
func TestSplunkEnterpriseAggregation(t *testing.T) {
	for op, want := range map[string]string{
		"":       "avg",
		"avg":    "avg",
		"sum":    "sum",
		"MAX":    "max",
		"min":    "min",
		"count":  "count",
		"rate":   "rate",
		"stddev": "stdev",
		"stdvar": "var",
		"p50":    "perc50",
		"p90":    "perc90",
		"p95":    "perc95",
		"p99":    "perc99",
		"median": "median",
		"latest": "latest",
		"last":   "latest",
	} {
		got, err := splunkEnterpriseAggregation(op)
		require.NoError(t, err, op)
		assert.Equal(t, want, got, op)
	}

	// An operator mstats cannot express must fail, not quietly become an average.
	for _, op := range []string{"group", "topk", "p75", "bogus"} {
		_, err := splunkEnterpriseAggregation(op)
		assert.Error(t, err, op)
	}
}

func TestBuildSplunkMStatsQueryAggregations(t *testing.T) {
	got, err := buildSplunkMStatsQuery(QueryItem{Metric: "k8s.pod.cpu.usage", AggregateOperator: "p95"}, nil, "otel_metrics", 300, false)
	require.NoError(t, err)
	assert.Equal(t, `| mstats perc95(k8s.pod.cpu.usage) AS nb_value WHERE index="otel_metrics" span=300s`, got)

	_, err = buildSplunkMStatsQuery(QueryItem{Metric: "k8s.pod.cpu.usage", AggregateOperator: "group"}, nil, "otel_metrics", 300, false)
	assert.Error(t, err, "an unmappable operator must fail the build, not render avg")
}

func TestSplunkEnterpriseMetricTimestampMs(t *testing.T) {
	for _, raw := range []any{"2023-11-14T22:13:20.000+00:00", "2023-11-14T22:13:20Z", "1700000000", float64(1700000000)} {
		assert.Equal(t, int64(1700000000000), splunkEnterpriseRowTimestampMs(map[string]any{"_time": raw}, 1))
	}
	assert.Equal(t, int64(42), splunkEnterpriseRowTimestampMs(map[string]any{}, 42), "no _time falls back to the caller's timestamp")
	assert.Equal(t, int64(42), splunkEnterpriseRowTimestampMs(map[string]any{"_time": "garbage"}, 42))
}

// The UI never sends step_interval, so the window alone decides the span. Each case's
// bucket count stays at or under 1,000 per series.
func TestSplunkEnterpriseSpanSeconds(t *testing.T) {
	const day = int64(24 * 3600 * 1000)
	const hour = int64(3600 * 1000)
	end := int64(1790000000000)

	tests := []struct {
		name   string
		window int64
		want   int
	}{
		{"1 hour keeps the one-minute floor", hour, 60},
		{"16h40m is exactly 1,000 one-minute buckets", 1000 * 60 * 1000, 60},
		{"1 day", day, 300},
		{"7 days", 7 * day, 900},
		{"14 days", 14 * day, 1800},
		{"30 days", 30 * day, 3600},
		{"31 days", 31 * day, 3600},
		{"90 days", 90 * day, 10800},
		{"5 years is capped at one day", 5 * 365 * day, 86400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, splunkEnterpriseSpanSeconds(0, end-tt.window, end))
		})
	}

	t.Run("an explicit step wins over the window", func(t *testing.T) {
		assert.Equal(t, 120, splunkEnterpriseSpanSeconds(120, end-31*day, end))
	})

	t.Run("the derived span is what the SPL carries", func(t *testing.T) {
		spl, err := buildSplunkMStatsQuery(QueryItem{Metric: "k8s.pod.cpu.usage"}, nil, "otel_metrics",
			splunkEnterpriseSpanSeconds(0, end-31*day, end), false)
		require.NoError(t, err)
		assert.Equal(t, `| mstats avg(k8s.pod.cpu.usage) AS nb_value WHERE index="otel_metrics" span=3600s`, spl)
	})
}

func TestSplunkMetricLikeAndRegexMatchers(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		// '%' was kept literally before, so `*%check%*` matched nothing.
		{"substring pattern", "%check%", `k8s.deployment.name="*check*"`},
		{"prefix pattern", "check%", `k8s.deployment.name="check*"`},
		{"suffix pattern", "%out", `k8s.deployment.name="*out"`},
		{"bare word keeps the substring match", "check", `k8s.deployment.name="*check*"`},
		{"underscore stays literal", "api_%", `k8s.deployment.name="api_*"`},
		// '*' is a Splunk wildcard with no escape inside a quoted term (#39334), so a '*' in
		// the value passes through as one.
		{"a star in the value stays a wildcard", "a*%", `k8s.deployment.name="a**"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildSplunkMetricMatcher(LabelMatcher{Label: "workload", Operator: "_like", Value: tt.value})
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("_contains is a plain substring", func(t *testing.T) {
		got, err := buildSplunkMetricMatcher(LabelMatcher{Label: "workload", Operator: "_contains", Value: "check"})
		require.NoError(t, err)
		assert.Equal(t, `k8s.deployment.name="*check*"`, got)
	})

	// Rendering `a|b` as a wildcard term matched nothing and said nothing.
	for _, op := range []string{"=~", "_regex"} {
		t.Run(op+" is refused", func(t *testing.T) {
			_, err := buildSplunkMetricMatcher(LabelMatcher{Label: "workload", Operator: op, Value: "a|b"})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "no regex operator")
		})
	}
}

// The wildcard guard belongs to the aggregation only: catalog lookups (FetchMetricsLabels)
// share isSafeSplunkMetricName and must keep accepting a name pattern.
func TestSplunkMetricWildcardGuardScope(t *testing.T) {
	_, err := buildSplunkMStatsQuery(QueryItem{Metric: "k8s.pod.*"}, nil, "otel_metrics", 60, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wildcard")

	assert.True(t, isSafeSplunkMetricName("k8s.pod.*"), "catalog lookups must still accept a metric-name pattern")
}

func TestSplunkEnterpriseExecutableSPL(t *testing.T) {
	const index = "otel_metrics"
	tests := []struct {
		name        string
		spl         string
		want        string
		newestFirst bool
	}{
		{
			name:        "builder-rendered mstats",
			spl:         `| mstats avg(k8s.pod.cpu.usage) AS nb_value WHERE index="otel_metrics" span=3600s`,
			want:        `| mstats avg(k8s.pod.cpu.usage) AS nb_value WHERE index="otel_metrics" span=3600s | sort 0 - _time`,
			newestFirst: true,
		},
		{
			name:        "raw mstats with its own pipeline and trailing whitespace",
			spl:         "  | mstats avg(k8s.pod.cpu.usage) AS v WHERE index=\"otel_metrics\" span=60s BY k8s.pod.name | eval v=v*100 \n",
			want:        `  | mstats avg(k8s.pod.cpu.usage) AS v WHERE index="otel_metrics" span=60s BY k8s.pod.name | eval v=v*100 | sort 0 - _time`,
			newestFirst: true,
		},
		{
			name: "mcatalog has no _time and is left alone",
			spl:  `| mcatalog values(metric_name) WHERE index="otel_metrics"`,
			want: `| mcatalog values(metric_name) WHERE index="otel_metrics"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, newestFirst := splunkEnterpriseExecutableSPL(tt.spl)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.newestFirst, newestFirst)
			assert.NoError(t, validateSplunkEnterpriseMetricQuery(got, index), "the appended sort must pass the metric guard")
		})
	}
}

func TestSplunkEnterpriseMetricResult(t *testing.T) {
	t.Run("newest-first rows are returned oldest first per series", func(t *testing.T) {
		rows := []map[string]any{
			{"_time": "2026-08-27T15:30:00.000+00:00", "k8s.pod.name": "a", "nb_value": "3"},
			{"_time": "2026-08-27T15:30:00.000+00:00", "k8s.pod.name": "b", "nb_value": "30"},
			{"_time": "2026-08-27T15:25:00.000+00:00", "k8s.pod.name": "a", "nb_value": "2"},
			{"_time": "2026-08-27T15:20:00.000+00:00", "k8s.pod.name": "a", "nb_value": "1"},
			{"_time": "2026-08-27T15:20:00.000+00:00", "k8s.pod.name": "b", "nb_value": "10"},
		}
		got := splunkEnterpriseMetricResult("cpu", "spl", rows, true, 1)
		require.Len(t, got.Payload, 2)
		byPod := map[string]Result{}
		for _, r := range got.Payload {
			byPod[r.Metric["k8s.pod.name"]] = r
		}
		assert.Equal(t, []float64{1, 2, 3}, byPod["a"].Values)
		assert.True(t, byPod["a"].Timestamps[0] < byPod["a"].Timestamps[2], "timestamps must ascend")
		assert.Equal(t, []float64{10, 30}, byPod["b"].Values)
		assert.Empty(t, got.Note, "an uncapped result carries no truncation note")
	})

	t.Run("instant rows keep Splunk's order", func(t *testing.T) {
		rows := []map[string]any{
			{"k8s.pod.name": "top", "nb_value": "9"},
			{"k8s.pod.name": "second", "nb_value": "5"},
		}
		got := splunkEnterpriseMetricResult("cpu", "spl", rows, true, 1)
		require.Len(t, got.Payload, 2)
		assert.Equal(t, "top", got.Payload[0].Metric["k8s.pod.name"])
	})

	// A full page means the cap dropped rows. Before this, a 31-day query ended weeks
	// early with nothing on the result to say so.
	t.Run("a capped result says it was truncated", func(t *testing.T) {
		rows := make([]map[string]any, splunkEnterpriseMetricMaxRows)
		for i := range rows {
			rows[i] = map[string]any{"_time": float64(1700000000 - i*60), "nb_value": "1"}
		}
		got := splunkEnterpriseMetricResult("cpu", "spl", rows, true, 1)
		require.Len(t, got.Payload, 1)
		assert.Contains(t, got.Note, "truncated to the most recent 20000 rows")
		ts := got.Payload[0].Timestamps
		assert.Equal(t, int64(1700000000000), ts[len(ts)-1], "the newest point is kept and drawn last")
	})

	t.Run("a capped mcatalog result is not described as newest-first", func(t *testing.T) {
		rows := make([]map[string]any, splunkEnterpriseMetricMaxRows)
		for i := range rows {
			rows[i] = map[string]any{"_time": float64(1700000000 + i*60), "nb_value": "1"}
		}
		got := splunkEnterpriseMetricResult("cpu", "spl", rows, false, 1)
		assert.Empty(t, got.Note)
	})
}

// A raw `| mcatalog` through metrics_query used to run and come back empty with a note
// blaming the value column. It is answered with the actions that serve catalog questions.
func TestSplunkEnterpriseRawCatalogResult(t *testing.T) {
	spl := `| mcatalog values(metric_name) WHERE index="otel_metrics"`
	got, ok := splunkEnterpriseRawCatalogResult("q", spl)
	require.True(t, ok)
	require.NotNil(t, got.Error)
	assert.Contains(t, *got.Error, "catalog searches are not time series")
	assert.Contains(t, *got.Error, "metrics_list_names")
	assert.Equal(t, *got.Error, got.Note, "the Note carries the same answer for Payload-only readers")
	assert.Equal(t, spl, got.Query)
	assert.Empty(t, got.Payload)

	_, ok = splunkEnterpriseRawCatalogResult("q", "  | MCATALOG values(_dims) WHERE index=\"otel_metrics\"")
	assert.True(t, ok, "matched on the opening command, case-insensitively")

	_, ok = splunkEnterpriseRawCatalogResult("q", `| mstats avg(cpu) AS nb_value WHERE index="otel_metrics" span=60s`)
	assert.False(t, ok, "mstats runs as before")

	// The source's own catalog SPL still passes the shared guard.
	assert.NoError(t, validateSplunkEnterpriseMetricQuery(spl, "otel_metrics"))
}

// metrics_get_query used to fall back to the PromQL wrap for this source.
func TestSplunkEnterpriseWrapAggregateIsIdentity(t *testing.T) {
	spl := `| mstats sum(k8s.pod.cpu.usage) AS nb_value WHERE index="otel_metrics" span=60s`
	got, err := wrapAggregator(&SplunkEnterpriseMetricSource{}, spl, "sum")
	require.NoError(t, err)
	assert.Equal(t, spl, got)
}
