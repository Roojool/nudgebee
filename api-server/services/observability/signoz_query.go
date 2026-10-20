package observability

import (
	"fmt"
	"nudgebee/services/common"
	"sort"
	"strings"
	"time"
)

// signozLogTerms returns the filter items of each builder query a log fetch runs:
// the caller's raw items JSON as one query, or one query per AND-term of the
// where-clause (signozDNF).
func signozLogTerms(req FetchLogRequest) ([]any, error) {
	if req.Query != "" {
		var parsed any
		if err := common.UnmarshalJson([]byte(req.Query), &parsed); err != nil {
			return nil, fmt.Errorf("failed to parse Query JSON for signoz: %w", err)
		}
		return []any{parsed}, nil
	}
	terms, err := signozDNF(req.QueryRequest.Where)
	if err != nil {
		return nil, fmt.Errorf("failed to flatten query: %w", err)
	}
	items := make([]any, len(terms))
	for i, t := range terms {
		items[i] = t
	}
	return items, nil
}

// signozQueryName names the i-th builder query: A, B, … Z, then A1, B1, ….
func signozQueryName(i int) string {
	name := string(rune('A' + i%26))
	if i >= 26 {
		name += fmt.Sprint(i / 26)
	}
	return name
}

// signozLogsQueryBody is the query_range body for a logs list with one builder
// query per entry of terms. A single query pages on the server. Several queries
// each return the first offset+limit rows and signozMergeRows pages the merged
// set, since no server-side offset applies to a union.
func signozLogsQueryBody(req FetchLogRequest, terms []any) map[string]any {
	orderBy := []map[string]any{{"columnName": "timestamp", "order": "desc"}}
	if len(req.SortFields) > 0 {
		orderBy = make([]map[string]any, len(req.SortFields))
		for i, sf := range req.SortFields {
			orderBy[i] = map[string]any{"columnName": sf.ColumnName, "order": sf.Order}
		}
	}
	limit := 1000
	if req.Limit > 0 {
		limit = req.Limit
	}
	offset := max(req.Offset, 0)
	steps := 60
	if req.StepInterval > 0 {
		steps = req.StepInterval
	}
	pageSize := limit
	if len(terms) > 1 {
		pageSize, offset = offset+limit, 0
	}

	builderQueries := make(map[string]any, len(terms))
	for i, items := range terms {
		name := signozQueryName(i)
		builderQueries[name] = map[string]any{
			"dataSource":         "logs",
			"queryName":          name,
			"aggregateOperator":  "noop",
			"aggregateAttribute": map[string]any{},
			"timeAggregation":    "rate",
			"spaceAggregation":   "sum",
			"functions":          []any{},
			"filters":            map[string]any{"op": "AND", "items": items},
			"expression":         name,
			"disabled":           false,
			"stepInterval":       steps,
			"having":             []any{},
			"limit":              nil,
			"orderBy":            orderBy,
			"groupBy":            []any{},
			"legend":             "",
			"reduceTo":           "avg",
			"offset":             offset,
			"pageSize":           pageSize,
		}
	}
	return map[string]any{
		"start":     req.StartTime,
		"end":       req.EndTime,
		"step":      60,
		"variables": map[string]any{},
		"compositeQuery": map[string]any{
			"queryType":      "builder",
			"panelType":      "list",
			"fillGaps":       false,
			"builderQueries": builderQueries,
		},
	}
}

// signozResultLists returns the row list of every query in a query_range result.
func signozResultLists(result []any) [][]any {
	var lists [][]any
	for _, r := range result {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if list, ok := m["list"].([]any); ok {
			lists = append(lists, list)
		} else if _, present := m["list"]; present {
			lists = append(lists, nil) // the query ran and matched nothing
		}
	}
	return lists
}

// signozFetchLogRows runs a log fetch through do, which sends one query_range body
// and returns its data.result. Several AND-terms go out as one request with one
// builder query each (measured on v0.145: each query returns its own list). If the
// server returns fewer lists than queries it was sent, each term is re-sent alone.
func signozFetchLogRows(req FetchLogRequest, do func(body map[string]any) ([]any, error)) ([]any, error) {
	terms, err := signozLogTerms(req)
	if err != nil {
		return nil, err
	}
	if len(terms) > 1 {
		for _, sf := range req.SortFields {
			if sf.ColumnName != "timestamp" {
				return nil, fmt.Errorf("sorting by %q is not supported with an OR filter on SigNoz (rows from the OR branches are merged by timestamp)", sf.ColumnName)
			}
		}
	}
	result, err := do(signozLogsQueryBody(req, terms))
	if err != nil {
		return nil, err
	}
	lists := signozResultLists(result)
	if len(terms) == 1 {
		if len(lists) == 0 {
			return nil, nil
		}
		return lists[0], nil
	}
	if len(lists) < len(terms) {
		lists = lists[:0]
		for _, t := range terms {
			single := req
			single.Offset, single.Limit = 0, max(req.Offset, 0)+signozLimit(req)
			r, err := do(signozLogsQueryBody(single, []any{t}))
			if err != nil {
				return nil, err
			}
			lists = append(lists, signozResultLists(r)...)
		}
	}
	return signozMergeRows(lists, req), nil
}

func signozLimit(req FetchLogRequest) int {
	if req.Limit > 0 {
		return req.Limit
	}
	return 1000
}

// signozMergeRows unions the rows of several queries: a row matched by two OR
// branches appears once (by its SigNoz id), the union is ordered by timestamp as
// the request asked (newest first by default), then offset and limit apply.
func signozMergeRows(lists [][]any, req FetchLogRequest) []any {
	seen := map[string]bool{}
	var rows []any
	for _, list := range lists {
		for _, row := range list {
			if id := signozRowID(row); id != "" {
				if seen[id] {
					continue
				}
				seen[id] = true
			}
			rows = append(rows, row)
		}
	}
	ascending := len(req.SortFields) > 0 && req.SortFields[0].ColumnName == "timestamp" && strings.EqualFold(req.SortFields[0].Order, "asc")
	// The id breaks timestamp ties, so the order — and the page an offset picks —
	// does not depend on the order the OR branches came back in.
	sort.Slice(rows, func(i, j int) bool {
		ti, tj := signozRowTime(rows[i]), signozRowTime(rows[j])
		if ti == tj {
			return signozRowID(rows[i]) < signozRowID(rows[j])
		}
		if ascending {
			return ti < tj
		}
		return ti > tj
	})
	offset := min(max(req.Offset, 0), len(rows))
	end := min(offset+signozLimit(req), len(rows))
	return rows[offset:end]
}

func signozRowID(row any) string {
	m, _ := row.(map[string]any)
	data, _ := m["data"].(map[string]any)
	id, _ := data["id"].(string)
	return id
}

// signozRowTime is a row's timestamp in Unix nanoseconds: SigNoz sends an RFC 3339
// string in list rows; a numeric value is taken as already in nanoseconds.
func signozRowTime(row any) int64 {
	m, _ := row.(map[string]any)
	switch ts := m["timestamp"].(type) {
	case string:
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			return t.UnixNano()
		}
	case float64:
		return int64(ts)
	}
	return 0
}
