package observability

import (
	"encoding/json"
	"nudgebee/services/query"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// whereFromJSON builds a clause the way it arrives over the wire, so the cases below read
// like the requests the UI and the logs agent actually send.
func whereFromJSON(t *testing.T, raw string) query.QueryWhereClause {
	t.Helper()
	var where query.QueryWhereClause
	require.NoError(t, json.Unmarshal([]byte(raw), &where))
	return where
}

func TestBuildSplunkEnterpriseFilter_Logs(t *testing.T) {
	cases := []struct {
		name  string
		where string
		want  string
	}{
		// Siblings of a _binary used to be dropped: the filter came back wider than asked.
		{"binary with sibling _or",
			`{"_binary":{"namespace":{"_eq":"demo"}},"_or":[{"_binary":{"severity":{"_eq":"FATAL"}}},{"_binary":{"severity":{"_eq":"WARN"}}}]}`,
			`(k8s.namespace.name="demo" AND (severity_text="FATAL" OR severity_text="WARN"))`},
		{"binary with sibling _and",
			`{"_binary":{"namespace":{"_eq":"demo"}},"_and":[{"_binary":{"severity":{"_eq":"FATAL"}}}]}`,
			`(k8s.namespace.name="demo" AND severity_text="FATAL")`},
		{"binary with sibling _not",
			`{"_binary":{"namespace":{"_eq":"demo"}},"_not":{"_binary":{"severity":{"_eq":"INFO"}}}}`,
			`(k8s.namespace.name="demo" AND NOT (severity_text="INFO"))`},
		// SPL binds OR tighter than AND, so a multi-field group inside an OR must be
		// parenthesised or it regroups as demo AND (FATAL OR nudgebee) AND FATAL.
		{"or of multi-field groups",
			`{"_or":[{"_binary":{"namespace":{"_eq":"demo"},"severity":{"_eq":"FATAL"}}},{"_binary":{"namespace":{"_eq":"nudgebee"},"severity":{"_eq":"FATAL"}}}]}`,
			`((k8s.namespace.name="demo" AND severity_text="FATAL") OR (k8s.namespace.name="nudgebee" AND severity_text="FATAL"))`},
		{"two operators on one field", `{"_binary":{"namespace":{"_eq":"demo","_neq":"nudgebee"}}}`,
			`(k8s.namespace.name="demo" AND NOT k8s.namespace.name="nudgebee")`},
		// SQL LIKE patterns: '%' is a wildcard and the match is anchored.
		{"ilike substring pattern", `{"_binary":{"pod":{"_ilike":"%postgres%"}}}`, `k8s.pod.name="*postgres*"`},
		{"ilike prefix pattern", `{"_binary":{"pod":{"_ilike":"panic%"}}}`, `k8s.pod.name="panic*"`},
		{"like suffix pattern", `{"_binary":{"pod":{"_like":"%exceeded"}}}`, `k8s.pod.name="*exceeded"`},
		// A bare word keeps the substring match callers have always had.
		{"ilike bare word", `{"_binary":{"pod":{"_ilike":"POSTGRES"}}}`, `k8s.pod.name="*POSTGRES*"`},
		{"like bare word", `{"_binary":{"pod":{"_like":"postgres"}}}`, `k8s.pod.name="*postgres*"`},
		{"nlike", `{"_binary":{"pod":{"_nlike":"%postgres%"}}}`, `NOT k8s.pod.name="*postgres*"`},
		{"like keeps underscore literal", `{"_binary":{"pod":{"_like":"api_%"}}}`, `k8s.pod.name="api_*"`},
		{"icontains", `{"_binary":{"pod":{"_icontains":"Postgres"}}}`, `k8s.pod.name="*Postgres*"`},
		{"nicontains", `{"_binary":{"pod":{"_nicontains":"postgres"}}}`, `NOT k8s.pod.name="*postgres*"`},
		{"in", `{"_binary":{"container":{"_in":["app","ledger"]}}}`, `(k8s.container.name="app" OR k8s.container.name="ledger")`},
		{"not in", `{"_binary":{"container":{"_not_in":["app","ledger"]}}}`, `NOT (k8s.container.name="app" OR k8s.container.name="ledger")`},
		{"gt number", `{"_binary":{"severity_number":{"_gt":13}}}`, `severity_number>13`},
		{"gte numeric string", `{"_binary":{"severity_number":{"_gte":"17"}}}`, `severity_number>=17`},
		{"lt decimal", `{"_binary":{"severity_number":{"_lt":9.5}}}`, `severity_number<9.5`},
		{"lte", `{"_binary":{"severity_number":{"_lte":9}}}`, `severity_number<=9`},
		// '*' is a wildcard in Splunk and passes through unchanged.
		{"asterisk passes through as wildcard", `{"_binary":{"workload":{"_eq":"check*"}}}`, `k8s.deployment.name="check*"`},
		// An empty branch is "no restriction", which makes the whole OR unrestricted.
		{"or with an unrestricted branch", `{"_or":[{"_binary":{"namespace":{"_eq":"demo"}}},{}]}`, ``},
		{"empty clause", `{}`, ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildSplunkEnterpriseWhereClause(whereFromJSON(t, tc.where))
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestBuildSplunkEnterpriseFilter_RejectsMalformedOperands(t *testing.T) {
	cases := []struct {
		name, where, want string
	}{
		{"in without a list", `{"_binary":{"container":{"_in":"app"}}}`, "needs a list"},
		{"in with an empty list", `{"_binary":{"container":{"_in":[]}}}`, "at least one value"},
		{"comparison with text", `{"_binary":{"severity_number":{"_gt":"high"}}}`, "needs a number"},
		{"unsupported operator", `{"_binary":{"pod":{"_regex":"a.*"}}}`, "unsupported binary operator"},
		{"unsafe field nested in an OR", `{"_or":[{"_binary":{"a b":{"_eq":"x"}}}]}`, "unsafe field name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildSplunkEnterpriseWhereClause(whereFromJSON(t, tc.where))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// The quote is still escaped inside every operator that quotes a value, so no operand
// can break out of its literal.
func TestBuildSplunkEnterpriseFilter_EscapesQuotesInEveryQuotedOperator(t *testing.T) {
	for _, op := range []string{"_eq", "_neq", "_contains", "_icontains", "_nicontains", "_like", "_ilike", "_nlike"} {
		got, err := buildSplunkEnterpriseWhereClause(query.QueryWhereClause{
			Binary: query.BinaryWhereClause{"pod": {query.BinaryWhereClauseType(op): `x" OR index="*`}},
		})
		require.NoError(t, err, op)
		assert.Contains(t, got, `x\" OR index=\"`, op)
		assert.NoError(t, validateSplunkEnterpriseQuery(`search index="main" `+got), op)
	}
	got, err := buildSplunkEnterpriseWhereClause(query.QueryWhereClause{
		Binary: query.BinaryWhereClause{"pod": {query.In: []any{`a"b`}}},
	})
	require.NoError(t, err)
	assert.Equal(t, `(k8s.pod.name="a\"b")`, got)
}

func TestBuildSplunkEnterpriseFilter_Traces(t *testing.T) {
	cases := []struct {
		name, where, want string
	}{
		// The unsupported-field strip runs on nested groups too: a caller-OR-callee search
		// keeps its caller side instead of the callee side matching nothing.
		{"unsupported field nested in an OR branch is skipped",
			`{"_or":[{"_binary":{"workload_name":{"_eq":"ledger"},"workload_namespace":{"_eq":"demo"}}},{"_binary":{"destination_workload_name":{"_eq":"ledger"},"destination_workload_namespace":{"_eq":"demo"}}}]}`,
			`((service.name="ledger" AND k8s.namespace.name="demo") OR attributes.net.peer.name="ledger")`},
		{"branch with only an unsupported field is skipped",
			`{"_or":[{"_binary":{"workload_name":{"_eq":"ledger"}}},{"_binary":{"destination_workload_namespace":{"_eq":"demo"}}}]}`,
			`service.name="ledger"`},
		{"destination_name maps to the peer name", `{"_binary":{"destination_name":{"_eq":"ledger"}}}`, `attributes.net.peer.name="ledger"`},
		// Every Splunk span is an OTel span, so trace_source=otel is a tautology.
		{"trace_source otel alone is no restriction", `{"_binary":{"trace_source":{"_eq":"otel"}}}`, ``},
		{"trace_source otel beside a filter", `{"_binary":{"trace_source":{"_eq":"OTEL"},"destination_workload_name":{"_eq":"payments"}}}`,
			`attributes.net.peer.name="payments"`},
		// The UI's operators.
		{"in on workload", `{"_binary":{"workload_name":{"_in":["ledger","payments"]}}}`, `(service.name="ledger" OR service.name="payments")`},
		{"like on resource", `{"_binary":{"resource":{"_like":"%/api/%"}}}`, `attributes.http.target="*/api/*"`},
		{"gte on http status", `{"_binary":{"http_status_code":{"_gte":500}}}`, `attributes.http.status_code>=500`},
		// The trace group view's HTTP-status filter: a _binary with a sibling _and.
		{"group view status filter",
			`{"_binary":{"span_name":{"_neq":"query"}},"_and":[{"_binary":{"http_status_code":{"_eq":"503"}}}]}`,
			`(NOT name="query" AND attributes.http.status_code="503")`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildSplunkEnterpriseTraceWhereClause(whereFromJSON(t, tc.where))
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestBuildSplunkEnterpriseFilter_TracesRefuseWhatCannotBeHonoured(t *testing.T) {
	cases := []struct {
		name, where, want string
	}{
		// Dropping the only predicate would render "no restriction" and match every span.
		{"only an unsupported field", `{"_binary":{"destination_workload_namespace":{"_eq":"demo"}}}`, "no field this schema can express"},
		{"every OR branch unsupported", `{"_or":[{"_binary":{"destination_workload_namespace":{"_eq":"a"}}},{"_binary":{"destination_workload_namespace":{"_eq":"b"}}}]}`, "no field this schema can express"},
		{"unsupported field under an AND", `{"_and":[{"_binary":{"workload_name":{"_eq":"x"}}},{"_binary":{"destination_workload_namespace":{"_eq":"demo"}}}]}`, "no field this schema can express"},
		{"unsupported field under a NOT", `{"_not":{"_binary":{"destination_workload_namespace":{"_eq":"demo"}}}}`, "no field this schema can express"},
		// The same node carrying a sibling group: the siblings are ANDed with the emptied
		// group, so dropping it would widen the result just as at the top level.
		{"emptied group beside a sibling _and", `{"_binary":{"destination_workload_namespace":{"_eq":"demo"}},"_and":[{"_binary":{"workload_name":{"_eq":"ledger"}}}]}`, "no field this schema can express"},
		{"emptied group beside a sibling _or", `{"_binary":{"destination_workload_namespace":{"_eq":"demo"}},"_or":[{"_binary":{"workload_name":{"_eq":"a"}}},{"_binary":{"workload_name":{"_eq":"b"}}}]}`, "no field this schema can express"},
		{"emptied group beside a sibling _not", `{"_binary":{"destination_workload_namespace":{"_eq":"demo"}},"_not":{"_binary":{"workload_name":{"_eq":"ledger"}}}}`, "no field this schema can express"},
		{"trace_source that cannot match", `{"_binary":{"trace_source":{"_eq":"ebpf"}}}`, "cannot match"},
		{"trace_source negated", `{"_binary":{"trace_source":{"_neq":"otel"}}}`, "cannot match"},
		{"duration is not a search field", `{"_binary":{"duration_ns":{"_gte":300000000}}}`, "duration_ns is not supported"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildSplunkEnterpriseTraceWhereClause(whereFromJSON(t, tc.where))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// Go callers pass numbers of any width; each must render, not fail the type switch.
func TestSplunkEnterpriseFilterNumber_AcceptsEveryIntegerWidth(t *testing.T) {
	for _, v := range []any{int(5), int8(5), int16(5), int32(5), int64(5), uint(5), uint8(5), uint16(5), uint32(5), uint64(5)} {
		got, err := splunkEnterpriseFilterNumber(query.Gt, v)
		require.NoError(t, err, "%T", v)
		assert.Equal(t, "5", got, "%T", v)
	}
}

func TestSplunkEnterpriseTraceSource_AdvertisedOperatorsBuild(t *testing.T) {
	for _, op := range (&SplunkEnterpriseTraceSource{}).GetSupportedOperators() {
		_, err := buildSplunkEnterpriseTraceWhereClause(query.QueryWhereClause{
			Binary: query.BinaryWhereClause{"workload_name": {query.BinaryWhereClauseType(op): splunkOperandFor(op)}},
		})
		assert.NoError(t, err, "advertised operator %q must be supported by the builder", op)
	}
}

// A filter on the log line resolves the displayed message field, in display order, and
// reaches _raw only for events with none of the named fields (unstructured sourcetypes).
func TestBuildSplunkEnterpriseFilter_MessageField(t *testing.T) {
	msg := func(term func(string) string) string {
		return "(" + term("body") +
			" OR (NOT body=* AND " + term("message") + ")" +
			" OR (NOT body=* AND NOT message=* AND " + term("log") + ")" +
			" OR (NOT body=* AND NOT message=* AND NOT log=* AND " + term("_raw") + "))"
	}
	cases := []struct {
		name, where, want string
	}{
		{"canonical body, exact", `{"_binary":{"body":{"_eq":"upstream returned 503"}}}`,
			msg(func(f string) string { return f + `="upstream returned 503"` })},
		{"canonical message, prefix pattern", `{"_binary":{"message":{"_ilike":"panic%"}}}`,
			msg(func(f string) string { return f + `="panic*"` })},
		{"v1 tool token _body, substring", `{"_binary":{"_body":{"_contains":"postgres"}}}`,
			msg(func(f string) string { return f + `="*postgres*"` })},
		{"negation negates the whole expression", `{"_binary":{"body":{"_nicontains":"postgres"}}}`,
			"NOT " + msg(func(f string) string { return f + `="*postgres*"` })},
		{"not in", `{"_binary":{"body":{"_not_in":["a","b"]}}}`,
			"NOT " + msg(func(f string) string { return `(` + f + `="a" OR ` + f + `="b")` })},
		// An explicit _raw filter is left alone: the caller asked for the raw event.
		{"explicit _raw is literal", `{"_binary":{"_raw":{"_contains":"x"}}}`, `_raw="*x*"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildSplunkEnterpriseWhereClause(whereFromJSON(t, tc.where))
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestBuildSplunkEnterpriseFilter_HostIsSplunksHostField(t *testing.T) {
	for _, label := range []string{"host", "hostname"} {
		got, err := buildSplunkEnterpriseWhereClause(whereFromJSON(t, `{"_binary":{"`+label+`":{"_eq":"gke-node-1"}}}`))
		require.NoError(t, err)
		assert.Equal(t, `host="gke-node-1"`, got, label)
	}
}
