package observability

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"nudgebee/services/integrations"
	"nudgebee/services/query"
	"nudgebee/services/security"
	"strings"
	"testing"
	"time"

	"github.com/agiledragon/gomonkey/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openObserveBinary(field string, op query.BinaryWhereClauseType, val any) query.QueryWhereClause {
	return query.QueryWhereClause{Binary: query.BinaryWhereClause{field: {op: val}}}
}

// _ilike is a LIKE pattern, not a substring: the % wildcards the UI and the logs agent
// send must reach OpenObserve as wildcards. str_match_ignore_case took them literally
// and returned zero rows for "%error%" on a stream full of errors.
func TestOpenObserveWhere_ILikeIsAPattern(t *testing.T) {
	sql, err := buildOpenObserveSQLWhereClause(openObserveBinary("message", query.ILike, "%error%"))
	require.NoError(t, err)
	assert.Equal(t, "body ILIKE '%error%'", sql)

	sql, err = buildOpenObserveTraceWhereClause(openObserveBinary("span_name", query.ILike, "post %"))
	require.NoError(t, err)
	assert.Equal(t, "operation_name ILIKE 'post %'", sql)
}

func TestOpenObserveWhere_SetAndPatternOperators(t *testing.T) {
	cases := []struct {
		name  string
		where query.QueryWhereClause
		want  string
	}{
		{"in", openObserveBinary("workload", query.In, []any{"cart", "it's"}), "k8s_deployment_name IN ('cart', 'it''s')"},
		{"not in", openObserveBinary("workload", query.NotIn, []any{"cart"}), "k8s_deployment_name NOT IN ('cart')"},
		{"in from []string", openObserveBinary("workload", query.In, []string{"a", "b"}), "k8s_deployment_name IN ('a', 'b')"},
		{"in with a scalar", openObserveBinary("workload", query.In, "cart"), "k8s_deployment_name IN ('cart')"},
		// "IN ()" is a syntax error; the empty set has a definite answer instead.
		{"empty in matches nothing", openObserveBinary("workload", query.In, []any{}), "FALSE"},
		{"empty not in matches everything", openObserveBinary("workload", query.NotIn, []any{}), "TRUE"},
		// null is the empty set, not the string "<nil>".
		{"null in matches nothing", openObserveBinary("workload", query.In, nil), "FALSE"},
		{"null not in matches everything", openObserveBinary("workload", query.NotIn, nil), "TRUE"},
		{"not like", openObserveBinary("message", query.NLike, "%health%"), "body NOT LIKE '%health%'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql, err := buildOpenObserveSQLWhereClause(tc.where)
			require.NoError(t, err)
			assert.Equal(t, tc.want, sql)
		})
	}
}

// OpenObserve compares a quoted literal as text: on dev, duration >= '3000' matched 2447
// spans where duration >= 3000 matched 1334. A numeric value must go out bare even when
// it arrives as a string, which is how the agents send it.
func TestOpenObserveWhere_RangeComparesNumbersAsNumbers(t *testing.T) {
	cases := []struct {
		op   query.BinaryWhereClauseType
		val  any
		want string
	}{
		{query.Gt, 5, "severity > 5"},
		{query.Gte, "9", "severity >= 9"},
		{query.Lt, 2.5, "severity < 2.5"},
		{query.Lte, "17", "severity <= 17"},
		{query.Gte, "2026-09-23", "severity >= '2026-09-23'"},
		{query.Lt, "it's", "severity < 'it''s'"},
	}
	for _, tc := range cases {
		sql, err := buildOpenObserveSQLWhereClause(openObserveBinary("severity", tc.op, tc.val))
		require.NoError(t, err)
		assert.Equal(t, tc.want, sql)
	}
}

// _timestamp holds epoch microseconds. The logs agent bounds it with ISO strings; quoted,
// those compare as text and matched 0 of 1129 rows on dev — a silent empty answer.
func TestOpenObserveWhere_TimestampRangeUsesMicros(t *testing.T) {
	sql, err := buildOpenObserveSQLWhereClause(query.QueryWhereClause{And: []query.QueryWhereClause{
		openObserveBinary("timestamp", query.Gte, "2026-09-25T18:18:00Z"),
		openObserveBinary("timestamp", query.Lt, "2026-09-25T18:48:00.5+05:30"),
	}})
	require.NoError(t, err)
	assert.Equal(t, "(_timestamp >= 1790360280000000 AND _timestamp < 1790342280500000)", sql)

	// Only the timestamp column is converted; any other string stays a quoted literal.
	sql, err = buildOpenObserveSQLWhereClause(openObserveBinary("host", query.Gte, "2026-09-25T18:18:00Z"))
	require.NoError(t, err)
	assert.Equal(t, "host_name >= '2026-09-25T18:18:00Z'", sql)
}

// Every key on one clause is a constraint. The workload traces tab sends _binary and
// _or side by side; the builder used to return after _binary, silently dropping the
// caller-or-callee restriction and widening the result.
func TestOpenObserveWhere_AppliesEveryKeyOnAClause(t *testing.T) {
	where := query.QueryWhereClause{
		Binary: query.BinaryWhereClause{"status_code": {query.Eq: "2"}},
		Or: []query.QueryWhereClause{
			{Binary: query.BinaryWhereClause{"workload_name": {query.In: []any{"cart"}}}},
			{Binary: query.BinaryWhereClause{"destination_workload_name": {query.In: []any{"cart"}}}},
		},
		Not: &query.QueryWhereClause{Binary: query.BinaryWhereClause{"span_name": {query.Eq: "GET /healthz"}}},
	}
	sql, err := buildOpenObserveTraceWhereClause(where)
	require.NoError(t, err)
	assert.Equal(t,
		"(status_code = '2' AND (service_name IN ('cart') OR net_peer_name IN ('cart')) AND NOT (operation_name = 'GET /healthz'))",
		sql)
}

// duration_ns is nanoseconds; OpenObserve's duration column is microseconds. A rename
// alone would turn "slower than 5 ms" into "slower than 5 s".
func TestOpenObserveTraceWhere_DurationIsRescaledToMicros(t *testing.T) {
	sql, err := buildOpenObserveTraceWhereClause(openObserveBinary("duration_ns", query.Gte, 5_000_000))
	require.NoError(t, err)
	assert.Equal(t, "duration >= 5000", sql)

	sql, err = buildOpenObserveTraceWhereClause(openObserveBinary("duration_ns", query.Lt, "2000000"))
	require.NoError(t, err)
	assert.Equal(t, "duration < 2000", sql)

	// Sub-microsecond remainders are kept, not truncated. On dev, ">= 1.5" returned the
	// same 4088 spans as ">= 2", while a truncated ">= 1" also admitted the 1 µs spans.
	sql, err = buildOpenObserveTraceWhereClause(openObserveBinary("duration_ns", query.Gte, 1500))
	require.NoError(t, err)
	assert.Equal(t, "duration >= 1.5", sql)

	sql, err = buildOpenObserveTraceWhereClause(openObserveBinary("duration_ns", query.Lt, 2.5e6))
	require.NoError(t, err)
	assert.Equal(t, "duration < 2500", sql)
}

// Lists are rescaled element by element. They used to pass through untouched, so on dev
// "duration_ns in [13000]" returned 0 spans instead of 34, and not_in returned 4132, not 4098.
func TestOpenObserveTraceWhere_DurationListsAreRescaled(t *testing.T) {
	sql, err := buildOpenObserveTraceWhereClause(openObserveBinary("duration_ns", query.In, []any{13000, "1500", 2e6}))
	require.NoError(t, err)
	assert.Equal(t, "duration IN ('13', '1.5', '2000')", sql)

	sql, err = buildOpenObserveTraceWhereClause(openObserveBinary("duration_ns", query.NotIn, []string{"13000"}))
	require.NoError(t, err)
	assert.Equal(t, "duration NOT IN ('13')", sql)

	sql, err = buildOpenObserveTraceWhereClause(openObserveBinary("duration_ns", query.In, []any{}))
	require.NoError(t, err)
	assert.Equal(t, "FALSE", sql)
}

// A pattern on nanoseconds cannot be expressed against the microsecond column; rescaling
// the pattern silently returned 0 rows, so it is refused instead.
func TestOpenObserveTraceWhere_DurationRejectsPatternOperators(t *testing.T) {
	for _, op := range []query.BinaryWhereClauseType{query.Contains, query.Like, query.NLike, query.ILike} {
		_, err := buildOpenObserveTraceWhereClause(openObserveBinary("duration_ns", op, "1"))
		assert.Error(t, err, "duration_ns %s must be refused", op)
	}
}

func TestOpenObserveTraceWhere_DurationRewriteDoesNotMutateCaller(t *testing.T) {
	binary := query.BinaryWhereClause{"duration_ns": {query.Gte: 5_000_000}, "workload_name": {query.Eq: "cart"}}
	_, err := buildOpenObserveTraceWhereClause(query.QueryWhereClause{Binary: binary})
	require.NoError(t, err)
	assert.Equal(t, 5_000_000, binary["duration_ns"][query.Gte], "other providers in a fan-out read the same clause")
	assert.NotContains(t, binary, "duration")
}

// The exact filter trace.ts builds for a workload's Traces tab — it 400'd on _in.
func TestOpenObserveTraceWhere_WorkloadTracesTabRequest(t *testing.T) {
	where := query.QueryWhereClause{Binary: query.BinaryWhereClause{
		"workload_name":      {query.In: []any{"cart"}},
		"workload_namespace": {query.In: []any{"demo"}},
		"http_status_code":   {query.In: []any{"200", "500"}},
		"duration_ns":        {query.Gte: 1_000_000},
		"resource":           {query.Like: "%/cart%"},
	}}
	sql, err := buildOpenObserveTraceWhereClause(where)
	require.NoError(t, err)
	for _, want := range []string{
		"service_name IN ('cart')",
		"service_k8s_namespace_name IN ('demo')",
		"http_status_code IN ('200', '500')",
		"duration >= 1000",
		"http_target LIKE '%/cart%'",
	} {
		assert.Contains(t, sql, want)
	}
}

func TestOpenObserveSources_AdvertiseTheSameOperators(t *testing.T) {
	assert.Equal(t, (&OpenObserveLogSource{}).GetSupportedOperators(), (&OpenObserveTraceSource{}).GetSupportedOperators(),
		"both sources render through buildOpenObserveBinaryClause")
}

func TestOpenObserveSources_AdvertiseEveryOperatorTheBuilderRenders(t *testing.T) {
	for _, ops := range [][]string{
		(&OpenObserveLogSource{}).GetSupportedOperators(),
		(&OpenObserveTraceSource{}).GetSupportedOperators(),
	} {
		for _, op := range ops {
			var val any = []any{"x"}
			if op == string(query.IsNull) {
				val = true
			}
			_, err := buildOpenObserveSQLWhereClause(openObserveBinary("workload", query.BinaryWhereClauseType(op), val))
			assert.NoError(t, err, "advertised operator %s must render", op)
		}
	}
}

// The log-row detail renders labels.timestamp as epoch nanoseconds; the raw microsecond
// _timestamp aliased into it rendered every OpenObserve row as January 1970.
func TestOpenObserveLogs_TimestampLabelIsNanoseconds(t *testing.T) {
	const micros = int64(1790363408906341)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// The shipper's own "timestamp" field must not win: hit keys iterate in random
		// order, so a raw value copied after the derived one would flip row to row.
		_ = json.NewEncoder(w).Encode(openObserveSearchResponse{Hits: []map[string]any{
			{"_timestamp": micros, "timestamp": "1970-01-01T00:00:00Z", "body": "hello", "k8s_namespace_name": "ns"},
		}})
	}))
	defer server.Close()
	patches := gomonkey.ApplyFunc(integrations.GetOpenObserveConfigs,
		func(_ *security.RequestContext, _ string) (integrations.OpenObserveConfig, error) {
			return integrations.OpenObserveConfig{
				URL: server.URL, OrgID: "test-org", Username: "user", Password: "pass",
				LogStream: integrations.OpenObserveDefaultLogStream,
			}, nil
		})
	defer patches.Reset()

	logs, err := (&OpenObserveLogSource{}).QueryLogs(mockRequestContext(), FetchLogRequest{
		AccountId: "acct-1", LogProvider: "openobserve", StartTime: 1790363000000, EndTime: 1790364000000,
	})
	require.NoError(t, err)
	require.Len(t, logs, 1)

	normalizeOutputLogLabels(logs, openObserveLogLabelMapping)
	assert.Equal(t, micros*1000, logs[0].Labels["timestamp"])
	assert.Equal(t, "2026-09-25T19:10:08.906341Z", logs[0].Timestamp)
}

// A field the span schema cannot express has no values — an empty dropdown, not a 400
// that takes the whole filter request down with it.
func TestOpenObserveTraceLabelValues_UnsupportedFieldIsEmpty(t *testing.T) {
	patches := gomonkey.ApplyFunc(integrations.GetOpenObserveConfigs,
		func(_ *security.RequestContext, _ string) (integrations.OpenObserveConfig, error) {
			return integrations.OpenObserveConfig{URL: "http://127.0.0.1:1", OrgID: "o", TraceStream: "default"}, nil
		})
	defer patches.Reset()

	out, err := (&OpenObserveTraceSource{}).GetLabelValues(mockRequestContext(), TracesV3LabelValuesRequest{
		AccountId: "acct-1", Label: "destination_workload_namespace",
	})
	require.NoError(t, err)
	assert.Empty(t, out.Values)
}

// The builder's exists / !exists chips send _is_null; roots are also selected with it.
func TestOpenObserveWhere_IsNull(t *testing.T) {
	sql, err := buildOpenObserveSQLWhereClause(openObserveBinary("pod", query.IsNull, true))
	require.NoError(t, err)
	assert.Equal(t, "k8s_pod_name IS NULL", sql)
	sql, err = buildOpenObserveSQLWhereClause(openObserveBinary("pod", query.IsNull, "false"))
	require.NoError(t, err)
	assert.Equal(t, "k8s_pod_name IS NOT NULL", sql)
	sql, err = buildOpenObserveSQLWhereClause(openObserveBinary("pod", query.IsNull, ""))
	require.NoError(t, err)
	assert.Equal(t, "k8s_pod_name IS NULL", sql, "the value-less is-null chip sends an empty value")
	_, err = buildOpenObserveSQLWhereClause(openObserveBinary("pod", query.IsNull, "maybe"))
	assert.Error(t, err)
}

// The Ok/Error filter sends status names; the column holds numeric OTel codes. On dev
// "Error" returned 0 spans instead of 10.
func TestOpenObserveTraceWhere_StatusNamesBecomeCodes(t *testing.T) {
	cases := map[any]string{
		"STATUS_CODE_ERROR": "status_code = '2'",
		"STATUS_CODE_UNSET": "status_code = '0'",
		"ok":                "status_code = '1'",
		"2":                 "status_code = '2'",
	}
	for in, want := range cases {
		sql, err := buildOpenObserveTraceWhereClause(openObserveBinary("status_code", query.Eq, in))
		require.NoError(t, err)
		assert.Equal(t, want, sql, "input %v", in)
	}
	sql, err := buildOpenObserveTraceWhereClause(openObserveBinary("status_code", query.In, []any{"STATUS_CODE_OK", "STATUS_CODE_UNSET"}))
	require.NoError(t, err)
	assert.Equal(t, "status_code IN ('1', '0')", sql)
}

// "By Traces" selects root spans (NULL parent) in the query, on top of the caller's filter.
func TestOpenObserveTraces_RootSpansSelectedInQuery(t *testing.T) {
	req := TracesV3Request{QueryRequest: TracesQueryBuilderRequest{Limit: 10,
		Where: query.QueryWhereClause{Binary: query.BinaryWhereClause{"workload_name": {query.Eq: "cart"}}}}}
	sql, err := (&OpenObserveTraceSource{}).buildSQL(withOpenObserveRootSpans(req), "default")
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "default" WHERE (service_name = 'cart' AND reference_parent_span_id IS NULL) ORDER BY _timestamp DESC LIMIT 10`, sql)
	assert.Len(t, req.QueryRequest.Where.Binary, 1, "the caller's request is not mutated")
}

// Destination workload, destination and resource were empty on most dev spans although the
// span carried the callee (net_peer_name, upstream_cluster, rpc_service) and gRPC method.
func TestOpenObserveTraceRows_FillDestinationAndResource(t *testing.T) {
	rows := parseOpenObserveTraceHits([]map[string]any{
		{"trace_id": "t1", "service_name": "checkout", "net_peer_name": "cart", "server_address": "cart"},
		{"trace_id": "t2", "service_name": "frontend-proxy", "upstream_cluster": "frontend"},
		{"trace_id": "t3", "service_name": "cart", "grpc_method": "/oteldemo.CartService/GetCart", "rpc_service": "oteldemo.CartService"},
		{"trace_id": "t4", "service_name": "web", "http_target": "/api/cart", "rpc_method": "ignored"},
	})
	require.Len(t, rows, 4)
	assert.Equal(t, "cart", rows[0].DestinationWorkload, "destination workload comes from the column its filter targets")
	assert.Equal(t, "cart", rows[0].DestinationName)
	assert.Equal(t, "frontend", rows[1].DestinationName, "Envoy names the callee in upstream_cluster")
	assert.Empty(t, rows[1].DestinationWorkload)
	assert.Equal(t, "/oteldemo.CartService/GetCart", rows[2].Resource)
	assert.Equal(t, "oteldemo.CartService", rows[2].DestinationName)
	assert.Equal(t, "/api/cart", rows[3].Resource, "an HTTP target still wins over the RPC method")
}

// OpenObserve rejects a search with no time range; the traces screen asks for status_code
// values without one, so that dropdown failed on every load.
func TestOpenObserveTraceLabelValues_DefaultsMissingWindow(t *testing.T) {
	var got openObserveSearchRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(openObserveSearchResponse{Hits: []map[string]any{{"status_code": 2}}})
	}))
	defer server.Close()
	patches := gomonkey.ApplyFunc(integrations.GetOpenObserveConfigs,
		func(_ *security.RequestContext, _ string) (integrations.OpenObserveConfig, error) {
			return integrations.OpenObserveConfig{URL: server.URL, OrgID: "o", TraceStream: "default"}, nil
		})
	defer patches.Reset()

	out, err := (&OpenObserveTraceSource{}).GetLabelValues(mockRequestContext(), TracesV3LabelValuesRequest{AccountId: "a", Label: "status_code"})
	require.NoError(t, err)
	assert.Equal(t, []string{"2"}, out.Values)
	assert.Greater(t, got.Query.StartTime, int64(0))
	assert.Equal(t, int64(24*time.Hour/time.Microsecond), got.Query.EndTime-got.Query.StartTime)
}

// The traces screen's Logs tab filters on trace_id. A stdout-tailing collector has no such
// column — the id is inside the message — so the tab showed no logs for a trace whose id was
// in a log body. Missing correlation columns become a body match; real columns are kept.
func TestOpenObserveLogs_CorrelationIDsFallBackToBody(t *testing.T) {
	where := query.QueryWhereClause{And: []query.QueryWhereClause{
		{Binary: query.BinaryWhereClause{"namespace": {query.Eq: "nudgebee"}, "trace_id": {query.Eq: "6b257ff5"}}},
		{Binary: query.BinaryWhereClause{"span_id": {query.In: []any{"a1", "b2"}}}},
	}}
	got := rewriteCorrelationToBody(where, map[string]bool{"trace_id": true, "span_id": true})
	sql, err := buildOpenObserveSQLWhereClause(got)
	require.NoError(t, err)
	assert.Equal(t, "((k8s_namespace_name = 'nudgebee' AND str_match(body, '6b257ff5')) AND (str_match(body, 'a1') OR str_match(body, 'b2')))", sql)
	assert.Contains(t, where.And[0].Binary, "trace_id", "the caller's clause is not mutated")

	kept := rewriteCorrelationToBody(where, map[string]bool{"span_id": true})
	sql, err = buildOpenObserveSQLWhereClause(kept)
	require.NoError(t, err)
	assert.Contains(t, sql, "trace_id = '6b257ff5'", "a stream with a real trace_id column keeps it")
}

func TestOpenObserveLogs_CorrelationRewriteUsesTheStreamSchema(t *testing.T) {
	var sqls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/schema") {
			_, _ = w.Write([]byte(`{"schema":[{"name":"_timestamp","type":"Int64"},{"name":"body","type":"Utf8"}]}`))
			return
		}
		var req openObserveSearchRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		sqls = append(sqls, req.Query.SQL)
		_ = json.NewEncoder(w).Encode(openObserveSearchResponse{})
	}))
	defer server.Close()
	patches := gomonkey.ApplyFunc(integrations.GetOpenObserveConfigs,
		func(_ *security.RequestContext, _ string) (integrations.OpenObserveConfig, error) {
			return integrations.OpenObserveConfig{URL: server.URL, OrgID: "o", LogStream: "default"}, nil
		})
	defer patches.Reset()

	_, err := (&OpenObserveLogSource{}).QueryLogs(mockRequestContext(), FetchLogRequest{AccountId: "a", StartTime: 1, EndTime: 2,
		QueryRequest: LogsQueryBuilderRequest{Where: openObserveBinary("trace_id", query.Eq, "6b257ff5")}})
	require.NoError(t, err)
	require.Len(t, sqls, 1)
	assert.Contains(t, sqls[0], "str_match(body, '6b257ff5')")
}

// The Log Groups drill-down filters by the workload a group reports, which maps to the
// deployment column — empty on every pod not owned by a Deployment. Those records fall back
// to a pod-name match; records that do carry a deployment still have to match it.
func TestOpenObserveLogs_WorkloadFilterFallsBackToPodName(t *testing.T) {
	where := query.QueryWhereClause{And: []query.QueryWhereClause{
		{Binary: query.BinaryWhereClause{"k8s_namespace_name": {query.Eq: "nudgebee"}}},
		{Binary: query.BinaryWhereClause{"k8s_deployment_name": {query.Eq: "runbook-script-5c3e"}}},
	}}
	got := rewriteWorkloadWithPodFallback(where, "k8s_deployment_name", "k8s_pod_name", true)
	sql, err := buildOpenObserveSQLWhereClause(got)
	require.NoError(t, err)
	assert.Equal(t, "(k8s_namespace_name = 'nudgebee' AND (k8s_deployment_name = 'runbook-script-5c3e' OR "+
		"((k8s_deployment_name IS NULL OR k8s_deployment_name = '') AND "+
		"(k8s_pod_name = 'runbook-script-5c3e' OR k8s_pod_name LIKE 'runbook-script-5c3e-%'))))", sql)
	assert.Contains(t, where.And[1].Binary, "k8s_deployment_name", "the caller's clause is not mutated")

	// No deployment column on the stream: referencing it would fail the query.
	got = rewriteWorkloadWithPodFallback(openObserveBinary("k8s_deployment_name", query.In, []any{"a", "b"}), "k8s_deployment_name", "k8s_pod_name", false)
	sql, err = buildOpenObserveSQLWhereClause(got)
	require.NoError(t, err)
	assert.Equal(t, "((k8s_pod_name = 'a' OR k8s_pod_name LIKE 'a-%') OR (k8s_pod_name = 'b' OR k8s_pod_name LIKE 'b-%'))", sql)

	// Only positive matches are widened.
	got = rewriteWorkloadWithPodFallback(openObserveBinary("k8s_deployment_name", query.Nq, "a"), "k8s_deployment_name", "k8s_pod_name", true)
	sql, err = buildOpenObserveSQLWhereClause(got)
	require.NoError(t, err)
	assert.Equal(t, "k8s_deployment_name != 'a'", sql)
}

// The where clause reaches the source already rewritten through the account's effective
// label mapping, so the fallback has to find the workload and pod columns through that same
// mapping — an account that remaps them for a Fluent Bit shipper must get it too.
func TestOpenObserveLogs_WorkloadFallbackUsesTheStreamSchema(t *testing.T) {
	otel := map[string]string{"app": "k8s_deployment_name", "pod": "k8s_pod_name"}
	fluentBit := map[string]string{"app": "kubernetes_deployment_name", "pod": "kubernetes_pod_name"}
	for name, tc := range map[string]struct {
		mapping map[string]string
		schema  string
		want    string
	}{
		"pod column present": {otel, `{"name":"k8s_pod_name","type":"Utf8"},{"name":"k8s_deployment_name","type":"Utf8"}`, "k8s_pod_name LIKE 'relay-%'"},
		"no pod column":      {otel, `{"name":"k8s_deployment_name","type":"Utf8"}`, "WHERE k8s_deployment_name = 'relay' ORDER BY"},
		"remapped columns":   {fluentBit, `{"name":"kubernetes_pod_name","type":"Utf8"},{"name":"kubernetes_deployment_name","type":"Utf8"}`, "kubernetes_pod_name LIKE 'relay-%'"},
	} {
		t.Run(name, func(t *testing.T) {
			var sqls []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(r.URL.Path, "/schema") {
					_, _ = w.Write([]byte(`{"schema":[{"name":"body","type":"Utf8"},` + tc.schema + `]}`))
					return
				}
				var req openObserveSearchRequest
				_ = json.NewDecoder(r.Body).Decode(&req)
				sqls = append(sqls, req.Query.SQL)
				_ = json.NewEncoder(w).Encode(openObserveSearchResponse{})
			}))
			defer server.Close()
			patches := gomonkey.ApplyFunc(integrations.GetOpenObserveConfigs,
				func(_ *security.RequestContext, _ string) (integrations.OpenObserveConfig, error) {
					return integrations.OpenObserveConfig{URL: server.URL, OrgID: "o", LogStream: "default"}, nil
				})
			defer patches.Reset()
			patches.ApplyFunc(resolveLogLabelMapping, func(_ *security.RequestContext, _ string, _ LogSource, _ *labelMappingOverride) ResolvedLabelMapping {
				return ResolvedLabelMapping{Effective: tc.mapping}
			})

			ctx := security.NewRequestContext(context.Background(), security.NewSecurityContextForSuperAdmin(), slog.Default(), nil, nil)
			_, err := (&OpenObserveLogSource{}).QueryLogs(ctx, FetchLogRequest{AccountId: "a", StartTime: 1, EndTime: 2,
				QueryRequest: LogsQueryBuilderRequest{Where: openObserveBinary(tc.mapping["app"], query.Eq, "relay")}})
			require.NoError(t, err)
			require.Len(t, sqls, 1)
			assert.Contains(t, sqls[0], tc.want)
		})
	}
}

// The service map filters every query on trace_source = otel; OpenObserve has no such column,
// so each query failed and the map was empty. The predicate is evaluated against the one
// source OpenObserve spans come from.
func TestOpenObserveTraceWhere_TraceSourceIsEvaluated(t *testing.T) {
	cases := []struct {
		where query.QueryWhereClause
		want  string
	}{
		{query.QueryWhereClause{Binary: query.BinaryWhereClause{"trace_source": {query.Eq: "otel"}, "workload_name": {query.Eq: "cart"}}}, "service_name = 'cart'"},
		{openObserveBinary("trace_source", query.Eq, "OTEL"), "TRUE"},
		{openObserveBinary("trace_source", query.Eq, "ebpf"), "FALSE"},
		{openObserveBinary("trace_source", query.In, []any{"ebpf", "otel"}), "TRUE"},
		{openObserveBinary("trace_source", query.NotIn, []any{"otel"}), "FALSE"},
		{openObserveBinary("trace_source", query.Nq, "ebpf"), "TRUE"},
	}
	for _, tc := range cases {
		sql, err := buildOpenObserveTraceWhereClause(tc.where)
		require.NoError(t, err)
		assert.Equal(t, tc.want, sql)
	}
	_, err := buildOpenObserveTraceWhereClause(openObserveBinary("trace_source", query.Like, "o%"))
	assert.Error(t, err)
}

// A trace query with no limit is an internal caller (the service map) that wants the whole
// window; it used to get 100 spans and draw a map with most edges missing.
func TestOpenObserveTraces_NoLimitMeansTheCap(t *testing.T) {
	sql, err := (&OpenObserveTraceSource{}).buildSQL(TracesV3Request{}, "default")
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "default" ORDER BY _timestamp DESC LIMIT 10000`, sql)
	sql, err = (&OpenObserveTraceSource{}).buildSQL(TracesV3Request{QueryRequest: TracesQueryBuilderRequest{Limit: 25}}, "default")
	require.NoError(t, err)
	assert.Contains(t, sql, "LIMIT 25")
}
