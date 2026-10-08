package observability

import (
	"encoding/json"
	"nudgebee/services/query"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func bin(field string, op query.BinaryWhereClauseType, v any) query.QueryWhereClause {
	return query.QueryWhereClause{Binary: query.BinaryWhereClause{field: {op: v}}}
}

// termOps renders each term as "key op value" strings, order-independent per term.
func termOps(terms [][]FlattenedClause) [][]string {
	out := make([][]string, len(terms))
	for i, t := range terms {
		for _, c := range t {
			b, _ := json.Marshal(c.Value)
			out[i] = append(out[i], c.Key["key"]+" "+c.Op+" "+string(b))
		}
	}
	return out
}

func TestSignozDNF(t *testing.T) {
	svcA, svcB := bin("service.name", Eq, "a"), bin("service.name", Eq, "b")
	ns := bin("k8s.namespace.name", Eq, "x")

	t.Run("OR becomes one query per branch", func(t *testing.T) {
		terms, err := signozDNF(query.QueryWhereClause{Or: []query.QueryWhereClause{svcA, svcB}})
		require.NoError(t, err)
		assert.Equal(t, [][]string{{`service.name = "a"`}, {`service.name = "b"`}}, termOps(terms))
	})

	t.Run("AND distributes over OR", func(t *testing.T) {
		where := query.QueryWhereClause{And: []query.QueryWhereClause{ns, {Or: []query.QueryWhereClause{svcA, svcB}}}}
		terms, err := signozDNF(where)
		require.NoError(t, err)
		ops := termOps(terms)
		require.Len(t, ops, 2)
		for _, term := range ops {
			assert.Contains(t, term, `k8s.namespace.name = "x"`)
		}
	})

	t.Run("NOT is pushed onto the leaves", func(t *testing.T) {
		terms, err := signozDNF(query.QueryWhereClause{Not: &ns})
		require.NoError(t, err)
		assert.Equal(t, [][]string{{`k8s.namespace.name != "x"`}}, termOps(terms))

		// NOT (a OR b) = NOT a AND NOT b: one query.
		or := query.QueryWhereClause{Or: []query.QueryWhereClause{svcA, svcB}}
		terms, err = signozDNF(query.QueryWhereClause{Not: &or})
		require.NoError(t, err)
		require.Len(t, terms, 1)
		assert.ElementsMatch(t, []string{`service.name != "a"`, `service.name != "b"`}, termOps(terms)[0])

		// NOT (a AND x) = NOT a OR NOT x: two queries.
		and := query.QueryWhereClause{And: []query.QueryWhereClause{svcA, ns}}
		terms, err = signozDNF(query.QueryWhereClause{Not: &and})
		require.NoError(t, err)
		assert.ElementsMatch(t, [][]string{{`service.name != "a"`}, {`k8s.namespace.name != "x"`}}, termOps(terms))

		// _is_null flips its value; double negation cancels.
		null := bin("user.tier", IsNull, true)
		terms, err = signozDNF(query.QueryWhereClause{Not: &null})
		require.NoError(t, err)
		assert.Equal(t, [][]string{{`user.tier exists ""`}}, func() [][]string {
			terms[0][0].Value = ""
			return termOps(terms)
		}())
		inner := query.QueryWhereClause{Not: &svcA}
		terms, err = signozDNF(query.QueryWhereClause{Not: &inner})
		require.NoError(t, err)
		assert.Equal(t, [][]string{{`service.name = "a"`}}, termOps(terms))
	})

	t.Run("a negated level still matches every stored spelling", func(t *testing.T) {
		lvl := bin(signozSeverityKey, Eq, "error")
		terms, err := signozDNF(query.QueryWhereClause{Not: &lvl})
		require.NoError(t, err)
		require.Len(t, terms, 1)
		assert.Equal(t, "nin", terms[0][0].Op)
		assert.ElementsMatch(t, []any{"error", "ERROR", "Error"}, terms[0][0].Value)
	})

	t.Run("an empty clause is one match-everything query", func(t *testing.T) {
		terms, err := signozDNF(query.QueryWhereClause{})
		require.NoError(t, err)
		require.Len(t, terms, 1)
		b, _ := json.Marshal(terms[0])
		assert.Equal(t, "[]", string(b), "items must marshal as [] for SigNoz")
	})

	t.Run("expansions beyond the cap are refused, not run", func(t *testing.T) {
		var wide []query.QueryWhereClause
		for i := 0; i < 6; i++ {
			wide = append(wide, bin("k", Eq, i))
		}
		// 6 × 6 = 36 > 25
		where := query.QueryWhereClause{And: []query.QueryWhereClause{{Or: wide}, {Or: wide}}}
		_, err := signozDNF(where)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "limit is 25")
	})

	t.Run("an operator without a complement cannot be negated", func(t *testing.T) {
		il := bin("body", ILike, "%x%")
		_, err := signozDNF(query.QueryWhereClause{Not: &il})
		require.Error(t, err)
	})
}

// Every operator the SigNoz sources advertise must translate, and must be a token
// the UI knows (query.OperatorCatalog) — otherwise the dropdown offers an operator
// that fails, or DescribeOperators silently drops it.
func TestSignozSupportedOperatorsTranslate(t *testing.T) {
	assert.Equal(t, signozSupportedOperators, (&SignozSource{}).GetSupportedOperators())
	assert.Equal(t, signozSupportedOperators, (&SignozSaasSource{}).GetSupportedOperators())
	for _, tok := range signozSupportedOperators {
		_, inCatalog := query.OperatorCatalog[tok]
		assert.True(t, inCatalog, tok)
		var v any = "x"
		switch query.BinaryWhereClauseType(tok) {
		case In, NotIn:
			v = []any{"x"}
		case IsNull:
			v = true
		}
		_, err := signozLeaf("body", query.BinaryWhereClauseType(tok), v)
		assert.NoError(t, err, tok)
	}
	for tok, want := range map[query.BinaryWhereClauseType]string{
		query.NRegex: "nregex", HasKey: "exists", query.IContains: "contains", query.NIContains: "ncontains",
		Gte: ">=", Lt: "<", NotRegEx: "nregex", Exist: "exists", NotExist: "nexists",
	} {
		c, err := signozLeaf("body", tok, "x")
		require.NoError(t, err, tok)
		assert.Equal(t, want, c.Op, tok)
	}
	c, err := signozLeaf("body", ILike, "%Order_1234.%")
	require.NoError(t, err)
	assert.Equal(t, "regex", c.Op, "v0.51 has no ilike; regex works on every version")
	assert.Equal(t, `(?i)^.*Order.1234\..*$`, c.Value)
	_, err = signozLeaf("body", ILike, 5)
	assert.Error(t, err)

	c, err = signozLeaf("body", IsNull, false)
	require.NoError(t, err)
	assert.Equal(t, "exists", c.Op)
	_, err = signozLeaf("body", IsNull, "")
	assert.Error(t, err, "_is_null needs a boolean")
}

func TestSignozLogsQueryBody(t *testing.T) {
	req := FetchLogRequest{StartTime: 1, EndTime: 2, Limit: 50, Offset: 10}

	one := signozLogsQueryBody(req, []any{[]FlattenedClause{}})
	qs := one["compositeQuery"].(map[string]any)["builderQueries"].(map[string]any)
	require.Len(t, qs, 1)
	a := qs["A"].(map[string]any)
	assert.Equal(t, 10, a["offset"])
	assert.Equal(t, 50, a["pageSize"])
	assert.Equal(t, "AND", a["filters"].(map[string]any)["op"], "the filter op is always AND — OR is separate queries")

	two := signozLogsQueryBody(req, []any{[]FlattenedClause{}, []FlattenedClause{}})
	qs = two["compositeQuery"].(map[string]any)["builderQueries"].(map[string]any)
	require.Len(t, qs, 2)
	for _, name := range []string{"A", "B"} {
		q := qs[name].(map[string]any)
		assert.Equal(t, name, q["queryName"])
		assert.Equal(t, name, q["expression"])
		assert.Equal(t, 0, q["offset"], "a union cannot page on the server")
		assert.Equal(t, 60, q["pageSize"], "each query returns offset+limit rows")
	}
	assert.Equal(t, "A1", signozQueryName(26))
}

func signozListRow(id string, ts time.Time) any {
	return map[string]any{"timestamp": ts.UTC().Format(time.RFC3339Nano), "data": map[string]any{"id": id, "body": id}}
}

func TestSignozFetchLogRows(t *testing.T) {
	base := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	r1, r2, r3 := signozListRow("1", base.Add(1*time.Second)), signozListRow("2", base.Add(2*time.Second)), signozListRow("3", base.Add(3*time.Second))
	or := query.QueryWhereClause{Or: []query.QueryWhereClause{bin("service.name", Eq, "a"), bin("service.name", Eq, "b")}}

	t.Run("one request, rows merged, deduped and newest first", func(t *testing.T) {
		calls := 0
		rows, err := signozFetchLogRows(FetchLogRequest{QueryRequest: LogsQueryBuilderRequest{Where: or}}, func(body map[string]any) ([]any, error) {
			calls++
			return []any{
				map[string]any{"queryName": "A", "list": []any{r1, r3}},
				map[string]any{"queryName": "B", "list": []any{r3, r2}},
			}, nil
		})
		require.NoError(t, err)
		assert.Equal(t, 1, calls)
		assert.Equal(t, []any{r3, r2, r1}, rows)
	})

	t.Run("offset and limit page the merged rows", func(t *testing.T) {
		rows, err := signozFetchLogRows(FetchLogRequest{Offset: 1, Limit: 1, QueryRequest: LogsQueryBuilderRequest{Where: or}}, func(map[string]any) ([]any, error) {
			return []any{map[string]any{"list": []any{r1, r3}}, map[string]any{"list": []any{r2}}}, nil
		})
		require.NoError(t, err)
		assert.Equal(t, []any{r2}, rows)
	})

	t.Run("a server that answers fewer queries gets one request per branch", func(t *testing.T) {
		var sent []int
		rows, err := signozFetchLogRows(FetchLogRequest{QueryRequest: LogsQueryBuilderRequest{Where: or}}, func(body map[string]any) ([]any, error) {
			n := len(body["compositeQuery"].(map[string]any)["builderQueries"].(map[string]any))
			sent = append(sent, n)
			if n > 1 {
				return []any{map[string]any{"list": []any{r1}}}, nil
			}
			return []any{map[string]any{"list": []any{signozListRow(map[int]string{2: "2", 3: "3"}[len(sent)], base.Add(time.Duration(len(sent))*time.Second))}}}, nil
		})
		require.NoError(t, err)
		assert.Equal(t, []int{2, 1, 1}, sent)
		assert.Len(t, rows, 2)
	})

	t.Run("an OR filter cannot sort by another column", func(t *testing.T) {
		_, err := signozFetchLogRows(FetchLogRequest{SortFields: []SortField{{ColumnName: "severity_number", Order: "desc"}}, QueryRequest: LogsQueryBuilderRequest{Where: or}}, func(map[string]any) ([]any, error) {
			t.Fatal("must not query")
			return nil, nil
		})
		assert.ErrorContains(t, err, "severity_number")
	})

	t.Run("equal timestamps are ordered by id, whatever order the branches return", func(t *testing.T) {
		a, b := signozListRow("a", base), signozListRow("b", base)
		for _, lists := range [][]any{{map[string]any{"list": []any{a}}, map[string]any{"list": []any{b}}}, {map[string]any{"list": []any{b}}, map[string]any{"list": []any{a}}}} {
			rows, err := signozFetchLogRows(FetchLogRequest{QueryRequest: LogsQueryBuilderRequest{Where: or}}, func(map[string]any) ([]any, error) { return lists, nil })
			require.NoError(t, err)
			assert.Equal(t, []any{a, b}, rows)
		}
	})

	t.Run("a single query keeps today's path", func(t *testing.T) {
		rows, err := signozFetchLogRows(FetchLogRequest{QueryRequest: LogsQueryBuilderRequest{Where: bin("service.name", Eq, "a")}}, func(map[string]any) ([]any, error) {
			return []any{map[string]any{"list": []any{r1, r2}}}, nil
		})
		require.NoError(t, err)
		assert.Equal(t, []any{r1, r2}, rows, "server order is kept for a single query")
	})
}

func TestExpandCanonicalLabelKeys(t *testing.T) {
	src := &SignozSaasSource{}

	t.Run("app matches every workload kind", func(t *testing.T) {
		out, written := expandCanonicalLabelKeys(bin("app", Eq, "redis"), src, signozLogLabelMapping)
		require.Len(t, out.And, 1)
		require.Len(t, out.And[0].Or, len(signozWorkloadKeys))
		assert.Len(t, written, len(signozWorkloadKeys), "value validation skips every key the expansion wrote")
		for i, k := range signozWorkloadKeys {
			assert.Equal(t, "redis", out.And[0].Or[i].Binary[k][Eq])
		}
		terms, err := signozDNF(convertWhereClauseWithMApping(out, signozLogLabelMapping))
		require.NoError(t, err)
		assert.Len(t, terms, len(signozWorkloadKeys))
	})

	t.Run("a negative condition must hold for every kind", func(t *testing.T) {
		out, _ := expandCanonicalLabelKeys(bin("workload", Nq, "redis"), src, signozLogLabelMapping)
		require.Len(t, out.And, 1)
		assert.Len(t, out.And[0].And, len(signozWorkloadKeys))
		terms, err := signozDNF(convertWhereClauseWithMApping(out, signozLogLabelMapping))
		require.NoError(t, err)
		assert.Len(t, terms, 1, "app != x is one query")
	})

	t.Run("explicit keys, other labels and account overrides are untouched", func(t *testing.T) {
		where := query.QueryWhereClause{And: []query.QueryWhereClause{bin("k8s.deployment.name", Eq, "x"), bin("namespace", Eq, "y")}}
		out, written := expandCanonicalLabelKeys(where, src, signozLogLabelMapping)
		assert.Equal(t, where.And, out.And)
		assert.Empty(t, written)

		override := map[string]string{"app": "app"}
		out, written = expandCanonicalLabelKeys(bin("app", Eq, "redis"), src, override)
		assert.Equal(t, bin("app", Eq, "redis").Binary, out.Binary)
		assert.Empty(t, written)

		out, _ = expandCanonicalLabelKeys(bin("app", Eq, "redis"), &LokiSource{}, map[string]string{"app": "k8s.deployment.name"})
		assert.Equal(t, bin("app", Eq, "redis"), out)
	})

	t.Run("app and workload naming the same value cost one expansion", func(t *testing.T) {
		where := query.QueryWhereClause{And: []query.QueryWhereClause{bin("app", Eq, "redis"), bin("workload", Eq, "redis"), bin("namespace", Eq, "y")}}
		out, _ := expandCanonicalLabelKeys(where, src, signozLogLabelMapping)
		terms, err := signozDNF(convertWhereClauseWithMApping(out, signozLogLabelMapping))
		require.NoError(t, err)
		assert.Len(t, terms, len(signozWorkloadKeys), "not 25")

		same := query.QueryWhereClause{Binary: query.BinaryWhereClause{"app": {Eq: "redis"}, "workload": {Eq: "redis"}}}
		out, _ = expandCanonicalLabelKeys(same, src, signozLogLabelMapping)
		assert.Len(t, out.And, 1)
	})

	t.Run("validation skips the extra kinds", func(t *testing.T) {
		keys := expandedSecondaryKeys(src)
		assert.True(t, keys["k8s.statefulset.name"])
		assert.False(t, keys["k8s.deployment.name"], "the mapped key is still validated")
	})
}

func TestSignozRawQueryToWhereClause(t *testing.T) {
	item := func(key, op string, v any) any {
		return map[string]any{"key": map[string]any{"key": key}, "op": op, "value": v}
	}
	list := func(items ...any) map[string]any {
		return map[string]any{"dataSource": "logs", "aggregateOperator": "noop", "filters": map[string]any{"op": "AND", "items": items}}
	}
	raw := func(qs map[string]any) map[string]any {
		return map[string]any{"compositeQuery": map[string]any{"panelType": "list", "builderQueries": qs}, "start": float64(1000), "end": float64(2000)}
	}

	where, ok := signozRawQueryToWhereClause(raw(map[string]any{"A": list(item("service.name", "=", "a"), item("http.status_code", ">=", "500"))}))
	require.True(t, ok)
	terms, err := signozDNF(where)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{`service.name = "a"`, `http.status_code >= "500"`}, termOps(terms)[0])

	where, ok = signozRawQueryToWhereClause(raw(map[string]any{
		"A": list(item("service.name", "=", "a")),
		"B": list(item("service.name", "=", "b")),
		"C": map[string]any{"disabled": true},
	}))
	require.True(t, ok)
	assert.Len(t, where.Or, 2, "several list queries are ORed; disabled ones skipped")

	for name, q := range map[string]map[string]any{
		"aggregate":      {"A": map[string]any{"dataSource": "logs", "aggregateOperator": "count", "filters": map[string]any{"items": []any{item("a", "=", "b")}}}},
		"OR-ed items":    {"A": map[string]any{"filters": map[string]any{"op": "OR", "items": []any{item("a", "=", "b")}}}},
		"traces":         {"A": map[string]any{"dataSource": "traces", "filters": map[string]any{"items": []any{item("a", "=", "b")}}}},
		"unknown op":     {"A": list(item("a", "has", "b"))},
		"typed numeric":  {"A": list(map[string]any{"key": map[string]any{"key": "http.status_code", "dataType": "float64"}, "op": "=", "value": 500})},
		"unreadable key": {"A": list(map[string]any{"key": "", "op": "=", "value": "x"})},
		"no builder qry": {},
	} {
		_, ok := signozRawQueryToWhereClause(raw(q))
		assert.False(t, ok, name)
	}

	start, end := signozRawQueryWindow(raw(nil), time.Unix(0, 0), time.Unix(9, 0))
	assert.Equal(t, int64(1000), start.UnixMilli())
	assert.Equal(t, int64(2000), end.UnixMilli())
	start, end = signozRawQueryWindow(map[string]any{}, time.Unix(1, 0), time.Unix(9, 0))
	assert.Equal(t, time.Unix(1, 0), start)
	assert.Equal(t, time.Unix(9, 0), end)
}
