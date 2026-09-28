package observability

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nudgebee/services/integrations"
	"nudgebee/services/query"
	"nudgebee/services/security"

	"github.com/agiledragon/gomonkey/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// traceReq builds a span request from a JSON where clause ("" for none).
func traceReq(t *testing.T, where string, limit, offset int, orderBy ...query.QueryOrderBy) TracesV3Request {
	t.Helper()
	req := TracesV3Request{QueryRequest: TracesQueryBuilderRequest{Limit: limit, Offset: offset, OrderBy: orderBy}}
	if where != "" {
		req.QueryRequest.Where = whereFromJSON(t, where)
	}
	return req
}

func durationExprT(t *testing.T) string {
	t.Helper()
	expr, err := splunkEnterpriseTraceDurationExpr()
	require.NoError(t, err)
	return expr
}

func TestSplunkEnterpriseTraceSPLPaging(t *testing.T) {
	t.Run("offset keeps the natural order and cuts the page with a row number", func(t *testing.T) {
		spl, err := buildSplunkEnterpriseTraceSPL(traceReq(t, "", 10, 10), "otel_traces")
		require.NoError(t, err)
		assert.Equal(t,
			`search index="otel_traces" | head 20 | streamstats count AS nb_tr_row | where nb_tr_row>10 | fields *`, spl)
		// tail takes the last N of whatever is there, so a short last page would repeat
		// rows from the page before it.
		assert.NotContains(t, spl, "tail")
		assert.NoError(t, validateSplunkEnterpriseQuery(spl))
	})

	t.Run("the default listing never sorts", func(t *testing.T) {
		for _, orderBy := range [][]query.QueryOrderBy{nil, {{Column: "timestamp", Order: query.Desc}}} {
			spl, err := buildSplunkEnterpriseTraceSPL(traceReq(t, "", 10, 0, orderBy...), "otel_traces")
			require.NoError(t, err)
			assert.Equal(t, `search index="otel_traces" | head 10 | fields *`, spl, "%v", orderBy)
		}
	})

	t.Run("a negative offset is the first page", func(t *testing.T) {
		spl, err := buildSplunkEnterpriseTraceSPL(traceReq(t, "", 10, -5), "otel_traces")
		require.NoError(t, err)
		assert.Equal(t, `search index="otel_traces" | head 10 | fields *`, spl)
	})

	t.Run("a page past the search ceiling is refused", func(t *testing.T) {
		_, err := buildSplunkEnterpriseTraceSPL(traceReq(t, "", 100, 9950), "otel_traces")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "search ceiling")
	})
}

func TestSplunkEnterpriseTraceSPLOrdering(t *testing.T) {
	expr := durationExprT(t)
	cases := []struct {
		name          string
		limit, offset int
		orderBy       []query.QueryOrderBy
		want          string
	}{
		{"duration desc evaluates the duration then sorts a bounded top-N", 5, 0,
			[]query.QueryOrderBy{{Column: "duration_ns", Order: query.Desc}},
			`search index="otel_traces" | eval nb_tr_duration=` + expr + ` | sort limit=5 -nb_tr_duration, -_time | fields *`},
		{"duration asc with an offset", 5, 5,
			[]query.QueryOrderBy{{Column: "duration_ns", Order: query.Asc}},
			`search index="otel_traces" | eval nb_tr_duration=` + expr +
				` | sort limit=10 +nb_tr_duration, -_time | streamstats count AS nb_tr_row | where nb_tr_row>5 | fields *`},
		{"timestamp asc is the oldest first", 5, 0,
			[]query.QueryOrderBy{{Column: "timestamp", Order: query.Asc}},
			`search index="otel_traces" | sort limit=5 +_time | fields *`},
		// The event enrichers fetch a trace tree this way; refusing it would empty the
		// trace evidence for every Splunk account.
		{"the enricher's trace-tree order", 50, 0,
			[]query.QueryOrderBy{{Column: "trace_id", Order: query.Asc}, {Column: "timestamp", Order: query.Asc}},
			`search index="otel_traces" | sort limit=50 +trace_id, +_time | fields *`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spl, err := buildSplunkEnterpriseTraceSPL(traceReq(t, "", tc.limit, tc.offset, tc.orderBy...), "otel_traces")
			require.NoError(t, err)
			assert.Equal(t, tc.want, spl)
			assert.NoError(t, validateSplunkEnterpriseQuery(spl))
		})
	}

	t.Run("a column Splunk cannot order by is an error, not ingest order", func(t *testing.T) {
		_, err := buildSplunkEnterpriseTraceSPL(traceReq(t, "", 5, 0, query.QueryOrderBy{Column: "span_name", Order: query.Asc}), "otel_traces")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "timestamp, duration_ns or trace_id")
	})

	t.Run("an unknown direction is an error", func(t *testing.T) {
		_, err := buildSplunkEnterpriseTraceSPL(traceReq(t, "", 5, 0, query.QueryOrderBy{Column: "duration_ns", Order: "sideways"}), "otel_traces")
		require.Error(t, err)
	})

	t.Run("a duration filter's eval is reused by the duration sort", func(t *testing.T) {
		spl, err := buildSplunkEnterpriseTraceSPL(traceReq(t, `{"_binary":{"duration_ns":{"_gte":300000000}}}`, 5, 0,
			query.QueryOrderBy{Column: "duration_ns", Order: query.Desc}), "otel_traces")
		require.NoError(t, err)
		assert.Equal(t, 1, strings.Count(spl, "| eval nb_tr_duration="), spl)
		assert.Contains(t, spl, "| where nb_tr_duration>=300000000 | sort limit=5 -nb_tr_duration, -_time")
	})
}

func TestBuildSplunkEnterpriseTraceFilterDuration(t *testing.T) {
	expr := durationExprT(t)

	t.Run("a top-level duration becomes the post-stage", func(t *testing.T) {
		f, err := buildSplunkEnterpriseTraceFilter(whereFromJSON(t, `{"_binary":{"duration_ns":{"_gte":300000000,"_lte":350000000}}}`))
		require.NoError(t, err)
		assert.Equal(t, "", f.search)
		assert.Equal(t, ` | eval nb_tr_duration=`+expr+` | where nb_tr_duration>=300000000 AND nb_tr_duration<=350000000`, f.postStage)
	})

	t.Run("under the {_and: [request, defaults]} wrapping of ApplyDefaultTraceFilters", func(t *testing.T) {
		f, err := buildSplunkEnterpriseTraceFilter(whereFromJSON(t,
			`{"_and":[{"_binary":{"duration_ns":{"_gte":300000000},"workload_name":{"_eq":"ledger"}}},{"_binary":{"workload_namespace":{"_eq":"demo"}}}]}`))
		require.NoError(t, err)
		assert.Equal(t, `(service.name="ledger" AND k8s.namespace.name="demo")`, f.search)
		assert.Equal(t, ` | eval nb_tr_duration=`+expr+` | where nb_tr_duration>=300000000`, f.postStage)
	})

	t.Run("a duration a post-stage cannot honour is an explicit error", func(t *testing.T) {
		for _, where := range []string{
			`{"_or":[{"_binary":{"duration_ns":{"_gte":300000000}}},{"_binary":{"workload_name":{"_eq":"ledger"}}}]}`,
			`{"_not":{"_binary":{"duration_ns":{"_gte":300000000}}}}`,
			`{"_and":[{"_or":[{"_binary":{"duration_ns":{"_gte":1}}},{"_binary":{"workload_name":{"_eq":"a"}}}]}]}`,
		} {
			_, err := buildSplunkEnterpriseTraceFilter(whereFromJSON(t, where))
			require.Error(t, err, where)
			assert.Contains(t, err.Error(), "ANDed", where)
		}
	})

	t.Run("equality and non-numeric operands are refused", func(t *testing.T) {
		_, err := buildSplunkEnterpriseTraceFilter(whereFromJSON(t, `{"_binary":{"duration_ns":{"_eq":300000000}}}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "supports only")
		_, err = buildSplunkEnterpriseTraceFilter(whereFromJSON(t, `{"_binary":{"duration_ns":{"_gte":"slow"}}}`))
		require.Error(t, err)
	})

	t.Run("the caller's clause is not mutated", func(t *testing.T) {
		where := whereFromJSON(t, `{"_binary":{"duration_ns":{"_gte":1},"status_code":{"_eq":"Error"}}}`)
		_, err := buildSplunkEnterpriseTraceFilter(where)
		require.NoError(t, err)
		assert.Contains(t, where.Binary, "duration_ns")
		assert.Equal(t, "Error", where.Binary["status_code"][query.Eq])
	})
}

// Every builder must agree on which spans matched, or the count disagrees with the list
// and the facets offer values the list cannot show.
func TestSplunkEnterpriseTraceBuildersShareThePostStage(t *testing.T) {
	where := `{"_and":[{"_binary":{"duration_ns":{"_gte":300000000},"workload_name":{"_eq":"ledger"}}}]}`
	req := traceReq(t, where, 10, 0)
	filter, err := buildSplunkEnterpriseTraceFilter(req.QueryRequest.Where)
	require.NoError(t, err)
	require.NotEmpty(t, filter.postStage)

	list, err := buildSplunkEnterpriseTraceSPL(req, "otel_traces")
	require.NoError(t, err)
	count, err := buildSplunkEnterpriseTraceCountSPL(req, "otel_traces", false)
	require.NoError(t, err)
	dc, err := buildSplunkEnterpriseTraceCountSPL(req, "otel_traces", true)
	require.NoError(t, err)
	group, err := buildSplunkEnterpriseTraceGroupSPL(req, "otel_traces", 10)
	require.NoError(t, err)
	byTrace, err := buildSplunkEnterpriseTraceByTraceSPL(req, "otel_traces")
	require.NoError(t, err)
	facet := buildSplunkEnterpriseTraceLabelValuesSPL(filter, "otel_traces", "service.name")

	for name, spl := range map[string]string{
		"list": list, "count": count, "dc": dc, "group": group, "by-trace": byTrace, "facet": facet,
	} {
		at := strings.Index(spl, filter.postStage)
		require.NotEqual(t, -1, at, "%s must carry the post-stage: %s", name, spl)
		assert.Equal(t, 1, strings.Count(spl, filter.postStage), name)
		for _, stage := range []string{"| head", "| sort", "| stats", "| top", "| streamstats"} {
			if i := strings.Index(spl, stage); i != -1 {
				assert.Less(t, at, i, "%s: the post-stage must run before %q", name, stage)
			}
		}
		assert.NoError(t, validateSplunkEnterpriseQuery(spl), name)
	}
	assert.Equal(t, `search index="otel_traces" service.name="ledger"`+filter.postStage+` | stats count AS nb_tr_count`, count)
}

func TestSplunkEnterpriseTraceStatusEquivalence(t *testing.T) {
	errorClass := `(status.code="STATUS_CODE_ERROR" OR status.code="Error" OR status.code="2")`
	cases := []struct {
		name, where, want string
	}{
		{"the UI's Error", `{"_binary":{"status_code":{"_eq":"Error"}}}`, errorClass},
		{"the enum number, as a JSON number", `{"_binary":{"status_code":{"_eq":2}}}`, errorClass},
		{"the exporter's own spelling", `{"_binary":{"status_code":{"_eq":"STATUS_CODE_ERROR"}}}`, errorClass},
		// convertWhereClauseWithMApping has usually mapped the field already.
		{"the already-mapped field", `{"_binary":{"status.code":{"_eq":"error"}}}`, errorClass},
		{"neq negates the whole class", `{"_binary":{"status_code":{"_neq":"Ok"}}}`,
			`NOT (status.code="STATUS_CODE_OK" OR status.code="Ok" OR status.code="1")`},
		{"in widens every member", `{"_binary":{"status_code":{"_in":["Error","Unset","STATUS_CODE_ERROR"]}}}`,
			`(status.code="STATUS_CODE_ERROR" OR status.code="Error" OR status.code="2" OR ` +
				`status.code="STATUS_CODE_UNSET" OR status.code="Unset" OR status.code="0")`},
		{"an unknown value passes through", `{"_binary":{"status_code":{"_eq":"weird"}}}`, `status.code="weird"`},
		// _eq and _in on one field are ANDed; folding them into one _in would OR them.
		{"eq beside in stays an AND", `{"_binary":{"status_code":{"_eq":"Error","_in":["Ok"]}}}`,
			`(` + errorClass + ` AND (status.code="STATUS_CODE_OK" OR status.code="Ok" OR status.code="1"))`},
		{"inside an OR branch", `{"_or":[{"_binary":{"status_code":{"_eq":"Error"}}},{"_binary":{"workload_name":{"_eq":"ledger"}}}]}`,
			`(` + errorClass + ` OR service.name="ledger")`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := buildSplunkEnterpriseTraceFilter(whereFromJSON(t, tc.where))
			require.NoError(t, err)
			assert.Equal(t, tc.want, f.search)
			assert.Empty(t, f.postStage)
		})
	}
}

func TestBuildSplunkEnterpriseTraceByTraceSPL(t *testing.T) {
	root := splunkEnterpriseTraceRootExpr

	t.Run("pages on traces with streaming state only", func(t *testing.T) {
		spl, err := buildSplunkEnterpriseTraceByTraceSPL(traceReq(t, "", 10, 0), "otel_traces")
		require.NoError(t, err)
		assert.Equal(t, `search index="otel_traces"`+
			` | streamstats dc(trace_id) AS nb_tr_n`+
			` | streamstats min(nb_tr_n) AS nb_tr_rank BY trace_id`+
			` | where nb_tr_rank>0 AND nb_tr_rank<=10`+
			` | streamstats count AS nb_tr_span BY trace_id`+
			` | where nb_tr_span<=999 OR `+root+
			` | fields *`, spl)
		// eventstats buffers the whole window on the search head; a head over spans is
		// what returned fewer traces than asked.
		assert.NotContains(t, spl, "eventstats")
		assert.NotContains(t, spl, "| head")
		assert.NoError(t, validateSplunkEnterpriseQuery(spl))
	})

	t.Run("page two is ranks 11-20", func(t *testing.T) {
		spl, err := buildSplunkEnterpriseTraceByTraceSPL(traceReq(t, "", 10, 10), "otel_traces")
		require.NoError(t, err)
		assert.Contains(t, spl, "| where nb_tr_rank>10 AND nb_tr_rank<=20")
	})

	t.Run("only the natural order is accepted", func(t *testing.T) {
		_, err := buildSplunkEnterpriseTraceByTraceSPL(traceReq(t, "", 10, 0, query.QueryOrderBy{Column: "timestamp", Order: query.Desc}), "otel_traces")
		require.NoError(t, err)
		for _, ob := range []query.QueryOrderBy{
			{Column: "duration_ns", Order: query.Desc},
			{Column: "timestamp", Order: query.Asc},
		} {
			_, err := buildSplunkEnterpriseTraceByTraceSPL(traceReq(t, "", 10, 0, ob), "otel_traces")
			require.Error(t, err, "%v", ob)
			assert.Contains(t, err.Error(), "By Traces")
		}
	})

	t.Run("the per-trace cap keeps every accepted page inside the row ceiling", func(t *testing.T) {
		for limit := 1; limit <= splunkEnterpriseTraceByTraceMaxLimit; limit++ {
			spans := splunkEnterpriseTraceSpansPerTrace(limit)
			require.GreaterOrEqual(t, spans, 1, "limit %d", limit)
			// cap non-root spans plus the root, per trace.
			require.LessOrEqual(t, limit*(spans+1), splunkEnterpriseTraceMaxLimit, "limit %d", limit)
		}
	})

	t.Run("a page too large to fit the ceiling is refused", func(t *testing.T) {
		_, err := buildSplunkEnterpriseTraceByTraceSPL(TracesV3Request{
			QueryRequest: TracesQueryBuilderRequest{Limit: splunkEnterpriseTraceByTraceMaxLimit + 1},
		}, "otel_traces")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at most")
	})
}

// splunkTraceServer answers every oneshot search with the given results and messages,
// recording the form each search sent.
func splunkTraceServer(t *testing.T, results []map[string]any, messages []map[string]string) (*httptest.Server, func() []map[string]string) {
	t.Helper()
	var mu sync.Mutex
	var forms []map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		forms = append(forms, map[string]string{"search": r.Form.Get("search"), "count": r.Form.Get("count")})
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results, "messages": messages})
	}))
	t.Cleanup(server.Close)
	return server, func() []map[string]string {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]string(nil), forms...)
	}
}

func patchSplunkTraceConfig(t *testing.T, serverURL string) {
	t.Helper()
	patches := gomonkey.NewPatches()
	t.Cleanup(patches.Reset)
	patches.ApplyFunc(integrations.GetSplunkEnterpriseConfig,
		func(_ *security.RequestContext, _ string) (integrations.SplunkEnterpriseConfig, error) {
			return integrations.SplunkEnterpriseConfig{
				URL:        serverURL,
				AuthType:   integrations.SplunkEnterpriseAuthToken,
				Token:      "tok",
				TraceIndex: "otel_traces",
				App:        integrations.SplunkEnterpriseDefaultApp,
			}, nil
		})
}

// span builds one exporter-shaped row. Rows are newest-first, as Splunk streams them, so
// a trace's root (the earliest start) is the LAST of its rows.
func splunkSpanRow(traceID, spanID, parent string, startNs int64) map[string]any {
	return map[string]any{
		"trace_id": traceID, "span_id": spanID, "parent_span_id": parent,
		"service.name": "svc-" + spanID,
		"start_time":   fmt.Sprintf("%d", startNs), "end_time": fmt.Sprintf("%d", startNs+1000),
	}
}

// One trace carries more spans than the page has traces. The search must be allowed the
// row ceiling rather than `limit` rows, or that trace's root — last in the stream — and
// the whole second trace are cut off.
func TestSplunkEnterpriseQueryRootSpansByTraceFetchesWholeTraces(t *testing.T) {
	const base = int64(1787851349000000000)
	rows := []map[string]any{
		splunkSpanRow("A", "a5", "a4", base+50), splunkSpanRow("A", "a4", "a3", base+40),
		splunkSpanRow("A", "a3", "a2", base+30), splunkSpanRow("B", "b2", "b1", base+25),
		splunkSpanRow("A", "a2", "a1", base+20), splunkSpanRow("B", "b1", "", base+15),
		splunkSpanRow("A", "a1", "", base+10),
	}
	server, forms := splunkTraceServer(t, rows, nil)
	patchSplunkTraceConfig(t, server.URL)

	got, err := (&SplunkEnterpriseTraceSource{}).QueryRootSpansByTrace(mockRequestContext(), TracesV3Request{
		AccountId: "acc", QueryRequest: TracesQueryBuilderRequest{Limit: 2},
	})
	require.NoError(t, err)

	sent := forms()
	require.Len(t, sent, 1)
	assert.Equal(t, fmt.Sprintf("%d", splunkEnterpriseTraceMaxLimit), sent[0]["count"])
	assert.Contains(t, sent[0]["search"], "| where nb_tr_rank>0 AND nb_tr_rank<=2")

	require.Len(t, got, 2)
	byTrace := map[string]string{}
	for _, span := range got {
		byTrace[span.TraceID] = span.SpanID
	}
	assert.Equal(t, map[string]string{"A": "a1", "B": "b1"}, byTrace, "each trace is represented by its root")
}

// Splunk's row cap truncates without a message; a page that comes back at the ceiling
// is refused rather than served with its roots (the last rows newest-first) cut off.
func TestSplunkEnterpriseQueryRootSpansByTraceRefusesACeilingPage(t *testing.T) {
	rows := make([]map[string]any, 0, splunkEnterpriseTraceMaxLimit)
	for i := 0; i < splunkEnterpriseTraceMaxLimit; i++ {
		rows = append(rows, splunkSpanRow(fmt.Sprintf("T%d", i), fmt.Sprintf("s%d", i), "p", int64(1787851349000000000+i)))
	}
	server, _ := splunkTraceServer(t, rows, nil)
	patchSplunkTraceConfig(t, server.URL)
	_, err := (&SplunkEnterpriseTraceSource{}).QueryRootSpansByTrace(mockRequestContext(),
		TracesV3Request{AccountId: "acc", QueryRequest: TracesQueryBuilderRequest{Limit: 10}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ceiling")
}

func TestSplunkEnterpriseQueryRootSpansByTraceWarnings(t *testing.T) {
	rows := []map[string]any{splunkSpanRow("A", "a1", "", 1787851349000000000)}

	t.Run("a harmless WARN is logged, not fatal", func(t *testing.T) {
		server, _ := splunkTraceServer(t, rows, []map[string]string{{
			"type": "WARN", "text": "The limit has been reached for log messages in info.csv. 1000 messages have not been written to info.csv.",
		}})
		patchSplunkTraceConfig(t, server.URL)
		got, err := (&SplunkEnterpriseTraceSource{}).QueryRootSpansByTrace(mockRequestContext(),
			TracesV3Request{AccountId: "acc", QueryRequest: TracesQueryBuilderRequest{Limit: 10}})
		require.NoError(t, err)
		assert.Len(t, got, 1)
	})

	t.Run("a memory WARN fails the page instead of serving a wrong one", func(t *testing.T) {
		server, _ := splunkTraceServer(t, rows, []map[string]string{{
			"type": "WARN", "text": "streamstats: memory usage exceeded max_mem_usage_mb, some results may be omitted",
		}})
		patchSplunkTraceConfig(t, server.URL)
		_, err := (&SplunkEnterpriseTraceSource{}).QueryRootSpansByTrace(mockRequestContext(),
			TracesV3Request{AccountId: "acc", QueryRequest: TracesQueryBuilderRequest{Limit: 10}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "may be incomplete")
	})

	t.Run("other callers still ignore WARN", func(t *testing.T) {
		server, _ := splunkTraceServer(t, rows, []map[string]string{{"type": "WARN", "text": "memory"}})
		cfg := integrations.SplunkEnterpriseConfig{URL: server.URL, AuthType: integrations.SplunkEnterpriseAuthToken, Token: "t", App: "search"}
		got, err := execSplunkEnterpriseSearch(cfg, `search index="otel_traces"`, "1", "2", 10, 5*time.Second)
		require.NoError(t, err)
		assert.Len(t, got, 1)
		_, warnings, err := execSplunkEnterpriseSearchWithWarnings(cfg, `search index="otel_traces"`, "1", "2", 10, 5*time.Second)
		require.NoError(t, err)
		assert.Equal(t, []string{"memory"}, warnings)
	})
}

func TestValidateSplunkEnterpriseTraceQueryScope(t *testing.T) {
	for _, spl := range []string{
		`search index="otel_traces" service.name="ledger" | head 5`,
		// The implicit search, and every allowlisted transform.
		`index="otel_traces" service.name=ledger | where isnotnull(trace_id) | eval d=end_time-start_time` +
			` | stats count BY service.name | eventstats sum(count) AS total | streamstats count AS n` +
			` | timechart span=1m count | chart count BY n | sort - count | rename count AS c | table c` +
			` | fields c | head 5 | tail 5 | reverse | dedup c | fillnull value=0 | bin c span=10 | bucket c span=10` +
			` | top c | rare c | convert num(c) | search c>0`,
		// A quoted literal is a value, not a selector — nor a macro or a subsearch.
		"search index=\"otel_traces\" name=\"index=_audit\" name=\"`x`\" name=\"[1]\"",
		// A reference to the field is not a selector.
		`search index="otel_traces" | stats count BY index`,
		// Part of a longer field name.
		`search index="otel_traces" attributes.db.index=3`,
		// NOT and OR on anything but the index selector are ordinary search.
		`search index="otel_traces" NOT status.code="STATUS_CODE_OK" (service.name=a OR service.name=b)`,
		`search index="otel_traces" NOT (service.name=a OR service.name=b) | where NOT isnull(trace_id)`,
		`search service.name=a OR service.name=b index="otel_traces"`,
	} {
		assert.NoError(t, validateSplunkEnterpriseTraceQueryScope(spl, "otel_traces"), spl)
	}

	for _, spl := range []string{
		// Another index, in every spelling.
		`search index=_internal | head 1`,
		`search index="otel_traces" OR index="_internal"`,
		`search index="otel_traces" | search index=_audit`,
		`search index=otel_traces_secret`,
		`search index="otel_traces_secret"`,
		`search index IN ("otel_traces","_internal")`,
		"search index=\"otel_traces\" index IN\t(\"_internal\")",
		"search index=\"otel_traces\" index IN\n(\"_internal\")",
		`search index="otel_traces" index::_internal`,
		`search index="otel_traces" "index"=_audit`,
		`search index="otel_traces" index!=_audit`,
		`search index="otel_traces" index=*`,
		// Data read without naming an index: generating commands and non-allowlisted ones.
		`| from datamodel:Authentication`,
		`| tstats count from datamodel=X`,
		`search index="otel_traces" | from datamodel:Authentication`,
		`search index="otel_traces" | tstats count from datamodel=X`,
		`search index="otel_traces" | pivot Authentication Authentication count(Authentication) AS c`,
		`search index="otel_traces" | datamodel Authentication search`,
		`search index="otel_traces" | metadata type=hosts`,
		`search index="otel_traces" | eventcount`,
		`search index="otel_traces" | lookup users uid OUTPUT name`,
		`search index="otel_traces" | join trace_id [search index="otel_traces"]`,
		`search index="otel_traces" | appendpipe [stats count]`,
		`search index="otel_traces" | eval u=lookup("users.csv", json_object("uid", uid))`,
		`search index="otel_traces" || head 1`,
		// Subsearches and macros, even ones naming the right index.
		`search index="otel_traces" [search index="otel_traces" | fields trace_id]`,
		"search index=\"otel_traces\" `get_everything`",
		// Negation: names only the configured index, selects every other.
		`search NOT index="otel_traces"`,
		`search index="otel_traces" OR NOT index="otel_traces"`,
		`search index="otel_traces" OR NOT (x=1 OR index="otel_traces")`,
		`search index="otel_traces" OR !index="otel_traces"`,
		// The scoping selector as an OR operand also reads the role's default indexes.
		`search index="otel_traces" OR sourcetype=*`,
		`search sourcetype=* OR index="otel_traces"`,
		`search (index="otel_traces" OR sourcetype=*)`,
		// Must be scoped at all, by the quoted spelling, in the opening search.
		`search service.name="ledger"`,
		`search index=otel_traces`,
		`search service.name="ledger" | search index="otel_traces"`,
		// An unterminated quote would hide everything after it.
		`search index="otel_traces" name="x | search index=_audit`,
	} {
		assert.Error(t, validateSplunkEnterpriseTraceQueryScope(spl, "otel_traces"), spl)
	}
}

// Everything the source renders itself must pass the free-form guard too: if it did not,
// the guard would be describing a narrower language than the one the source speaks.
func TestSplunkEnterpriseTraceBuiltSPLPassesTheScopeGuard(t *testing.T) {
	where := `{"_binary":{"duration_ns":{"_gte":300000000},"status_code":{"_neq":"Ok"},"workload_name":{"_in":["a","b"]}},` +
		`"_or":[{"_binary":{"resource":{"_like":"%/api/%"}}},{"_binary":{"span_name":{"_neq":"query"}}}],` +
		`"_not":{"_binary":{"http_status_code":{"_eq":"200"}}}}`
	var spls []string
	add := func(spl string, err error) {
		t.Helper()
		require.NoError(t, err)
		spls = append(spls, spl)
	}
	for _, ob := range [][]query.QueryOrderBy{
		nil,
		{{Column: "duration_ns", Order: query.Desc}},
		{{Column: "timestamp", Order: query.Asc}},
		{{Column: "trace_id", Order: query.Asc}, {Column: "timestamp", Order: query.Asc}},
	} {
		add(buildSplunkEnterpriseTraceSPL(traceReq(t, where, 10, 10, ob...), "otel_traces"))
	}
	add(buildSplunkEnterpriseTraceCountSPL(traceReq(t, where, 0, 0), "otel_traces", false))
	add(buildSplunkEnterpriseTraceCountSPL(traceReq(t, where, 0, 0), "otel_traces", true))
	add(buildSplunkEnterpriseTraceByTraceSPL(traceReq(t, where, 10, 10), "otel_traces"))
	add(buildSplunkEnterpriseTraceGroupSPL(traceReq(t, where, 10, 10,
		query.QueryOrderBy{Column: "error_count", Order: query.Desc}), "otel_traces", 10))
	filter, err := buildSplunkEnterpriseTraceFilter(whereFromJSON(t, where))
	require.NoError(t, err)
	spls = append(spls, buildSplunkEnterpriseTraceLabelValuesSPL(filter, "otel_traces", "service.name"))

	// The heatmap and label discovery render inline; capture what they send.
	server, forms := splunkTraceServer(t, nil, nil)
	patchSplunkTraceConfig(t, server.URL)
	_, err = (&SplunkEnterpriseTraceSource{}).QueryTracesHeatmap(mockRequestContext(), TracesHeatMapRequest{AccountId: "acc", TraceId: "abc"})
	require.NoError(t, err)
	_, err = (&SplunkEnterpriseTraceSource{}).QueryLabels(mockRequestContext(), FetchTraceLabelRequest{AccountId: "acc"})
	require.NoError(t, err)
	sent := forms()
	require.Len(t, sent, 2)
	for _, f := range sent {
		spls = append(spls, f["search"])
	}

	for _, spl := range spls {
		assert.NoError(t, validateSplunkEnterpriseTraceQueryScope(spl, "otel_traces"), spl)
	}
}

func TestSplunkEnterpriseQueryTracesRefusesAnotherIndex(t *testing.T) {
	server, forms := splunkTraceServer(t, nil, nil)
	patchSplunkTraceConfig(t, server.URL)

	_, err := (&SplunkEnterpriseTraceSource{}).QueryTraces(mockRequestContext(), TracesV3Request{
		AccountId: "acc", Query: "search index=_internal | head 1",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "configured trace index")
	assert.Empty(t, forms(), "a refused query must never reach Splunk")
}

func TestSplunkEnterpriseTraceLabelValuesAreNarrowedByTheFilter(t *testing.T) {
	filter, err := buildSplunkEnterpriseTraceFilter(whereFromJSON(t, `{"_binary":{"workload_namespace":{"_in":["demo"]}}}`))
	require.NoError(t, err)
	assert.Equal(t,
		`search index="otel_traces" service.name=* (k8s.namespace.name="demo") | top limit=100 service.name | fields service.name`,
		buildSplunkEnterpriseTraceLabelValuesSPL(filter, "otel_traces", "service.name"))
}

func TestSplunkEnterpriseTraceGroupOrderAndOffset(t *testing.T) {
	tieBreak := ", +nb_tr_service, +nb_tr_namespace, +nb_tr_name, +nb_tr_resource, +nb_tr_destination, +nb_tr_http_code"

	t.Run("the default is the busiest groups first", func(t *testing.T) {
		spl, err := buildSplunkEnterpriseTraceGroupSPL(TracesV3Request{}, "otel_traces", 10)
		require.NoError(t, err)
		assert.True(t, strings.HasSuffix(spl, "| sort limit=10 -nb_tr_count"+tieBreak), spl)
	})

	cases := []struct {
		column string
		order  query.QuerySortOrder
		key    string
	}{
		{"error_count", query.Desc, "-nb_tr_errors"},
		{"count", query.Asc, "+nb_tr_count"},
		{"p99_latency", query.Desc, "-nb_tr_p99"},
		{"p95_latency", query.Desc, "-nb_tr_p95"},
		{"max_latency", query.Asc, "+nb_tr_max"},
		// The row's duration_ns IS its p99.
		{"duration_ns", query.Desc, "-nb_tr_p99"},
	}
	for _, tc := range cases {
		t.Run(tc.column, func(t *testing.T) {
			req := traceReq(t, "", 10, 10, query.QueryOrderBy{Column: tc.column, Order: tc.order})
			spl, err := buildSplunkEnterpriseTraceGroupSPL(req, "otel_traces", 10)
			require.NoError(t, err)
			assert.True(t, strings.HasSuffix(spl,
				"| sort limit=20 "+tc.key+tieBreak+" | streamstats count AS nb_tr_row | where nb_tr_row>10"), spl)
			assert.NoError(t, validateSplunkEnterpriseQuery(spl))
		})
	}

	t.Run("an unknown column is an error", func(t *testing.T) {
		_, err := buildSplunkEnterpriseTraceGroupSPL(traceReq(t, "", 10, 0, query.QueryOrderBy{Column: "span_name", Order: query.Asc}), "otel_traces", 10)
		require.Error(t, err)
	})
}

func TestSplunkEnterpriseGroupCountIgnoresPaging(t *testing.T) {
	server, forms := splunkTraceServer(t, []map[string]any{{splunkTraceCountCol: "3"}, {splunkTraceCountCol: "1"}}, nil)
	patchSplunkTraceConfig(t, server.URL)

	got, err := (&SplunkEnterpriseTraceSource{}).QueryGroupedTracesCount(mockRequestContext(),
		traceReq(t, "", 3, 3, query.QueryOrderBy{Column: "error_count", Order: query.Desc}))
	require.NoError(t, err)
	assert.Equal(t, 2, got.Count)

	sent := forms()
	require.Len(t, sent, 1)
	assert.Contains(t, sent[0]["search"], "| sort limit=100 -nb_tr_count, ")
	assert.NotContains(t, sent[0]["search"], "nb_tr_row")
}

func TestSortSplunkEnterpriseSpansByStart(t *testing.T) {
	// RFC3339Nano trims trailing zeros, so as strings ".1Z" sorts AFTER ".123Z" although
	// it started 23ms earlier — the old waterfall order.
	spans := convertSplunkEnterpriseSpans([]map[string]any{
		{"span_id": "later", "start_time": "1787851349123000000"},
		{"span_id": "earlier", "start_time": "1787851349100000000"},
		{"span_id": "ingest-only", "_time": "2026-08-27T17:22:28.000+00:00"},
	})
	require.Less(t, spans[0].Timestamp, spans[1].Timestamp, "precondition: the string order is the wrong one")

	got := sortSplunkEnterpriseSpansByStart(spans)
	ids := make([]string, len(got))
	for i, s := range got {
		ids[i] = s.SpanID
	}
	assert.Equal(t, []string{"ingest-only", "earlier", "later"}, ids)
}

// An offset near MaxInt must not wrap offset+limit negative and slip past the ceiling.
func TestSplunkEnterpriseTracePageRejectsOverflowingOffset(t *testing.T) {
	_, _, err := splunkEnterpriseTracePage(TracesQueryBuilderRequest{Limit: 10, Offset: math.MaxInt})
	require.Error(t, err)
	_, err = buildSplunkEnterpriseTraceGroupSPL(TracesV3Request{QueryRequest: TracesQueryBuilderRequest{Offset: math.MaxInt}}, "otel_traces", 10)
	require.Error(t, err)
}
