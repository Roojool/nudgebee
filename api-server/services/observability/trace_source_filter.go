package observability

import (
	"fmt"
	"strings"

	"nudgebee/services/query"
	"nudgebee/services/security"
)

// trace_source is a synthetic trace field: "otel" for spans an OpenTelemetry exporter
// sent, "ebpf" for spans the nudgebee node agent captured. The agent's ClickHouse store
// derives it per span (query/metadata.go traceSourceExpr). Callers send it as an ordinary
// filter — the service map on every query, the Traces tab's Trace Source dropdown — but
// most providers have no such field: every span they hold is OpenTelemetry.
//
// Those providers declare it in allProviderCaps (TraceSource: "otel"), and the dispatch
// layer evaluates the filter itself instead of sending a field the backend lacks. Before
// this, each provider had to neutralise it on its own; the ones that did not (the three
// Elasticsearch readers) sent term{trace_source: otel}, matched nothing, and rendered an
// empty service map.
const traceSourceField = "trace_source"

// traceSourceOutcome is a filter's truth value once trace_source is evaluated: it always
// holds (no restriction left), never holds, or still depends on the remaining fields.
type traceSourceOutcome int

const (
	traceSourceDepends traceSourceOutcome = iota
	traceSourceAlways
	traceSourceNever
)

// resolveTraceSourceFilter evaluates every trace_source predicate in where against the
// provider's declared constant and removes it. matchesNothing reports that the whole
// clause can never hold (e.g. trace_source=ebpf on a provider with no eBPF spans): the
// caller must return an empty result without querying the provider.
//
// A provider without a declared constant — one that evaluates the field itself or passes
// it through — gets where back unchanged, as does a clause with no trace_source in it.
// Called after the label mapping, so a mapping that renamed trace_source to a real field
// reaches the provider as that field.
func resolveTraceSourceFilter(provider string, where query.QueryWhereClause) (query.QueryWhereClause, bool, error) {
	constant := allProviderCaps[provider].TraceSource
	if constant == "" || !whereHasField(where, traceSourceField) {
		return where, false, nil
	}
	resolved, outcome, err := evaluateTraceSource(where, constant)
	if err != nil {
		return where, false, err
	}
	return resolved, outcome == traceSourceNever, nil
}

// evaluateTraceSource evaluates trace_source through the clause tree. A clause is
// Binary ∧ (∧ And) ∧ (∨ Or) ∧ ¬Not, and each part keeps its three-valued outcome, so
// ¬(trace_source=ebpf ∧ x=1) evaluates to always rather than to ¬(x=1).
func evaluateTraceSource(w query.QueryWhereClause, constant string) (query.QueryWhereClause, traceSourceOutcome, error) {
	out := query.QueryWhereClause{}
	never := false

	for field, ops := range w.Binary {
		if field != traceSourceField {
			if out.Binary == nil {
				out.Binary = query.BinaryWhereClause{}
			}
			out.Binary[field] = ops
			continue
		}
		holds, err := traceSourceHolds(ops, constant)
		if err != nil {
			return w, traceSourceDepends, err
		}
		never = never || !holds
	}

	for _, child := range w.And {
		c, outcome, err := evaluateTraceSource(child, constant)
		if err != nil {
			return w, traceSourceDepends, err
		}
		switch outcome {
		case traceSourceNever:
			never = true
		case traceSourceDepends:
			out.And = append(out.And, c)
		}
	}

	if len(w.Or) > 0 {
		var kept []query.QueryWhereClause
		anyAlways := false
		for _, child := range w.Or {
			c, outcome, err := evaluateTraceSource(child, constant)
			if err != nil {
				return w, traceSourceDepends, err
			}
			switch outcome {
			case traceSourceAlways:
				anyAlways = true
			case traceSourceDepends:
				kept = append(kept, c)
			}
		}
		switch {
		case anyAlways:
			// One branch always holds, so the disjunction does: no restriction.
		case len(kept) == 0:
			never = true
		default:
			out.Or = kept
		}
	}

	if w.Not != nil {
		c, outcome, err := evaluateTraceSource(*w.Not, constant)
		if err != nil {
			return w, traceSourceDepends, err
		}
		switch outcome {
		case traceSourceAlways:
			never = true
		case traceSourceDepends:
			out.Not = &c
		}
	}

	switch {
	case never:
		return query.QueryWhereClause{}, traceSourceNever, nil
	case len(out.Binary) == 0 && len(out.And) == 0 && len(out.Or) == 0 && out.Not == nil:
		return out, traceSourceAlways, nil
	default:
		return out, traceSourceDepends, nil
	}
}

// traceSourceHolds evaluates one trace_source predicate against the constant every span
// of the provider carries. Values compare case-insensitively.
func traceSourceHolds(ops map[query.BinaryWhereClauseType]any, constant string) (bool, error) {
	holds := true
	for op, raw := range ops {
		found := false
		for _, v := range traceSourceValues(raw) {
			found = found || strings.EqualFold(strings.TrimSpace(v), constant)
		}
		switch op {
		case query.Eq, query.In:
			holds = holds && found
		case query.Nq, query.NotIn:
			holds = holds && !found
		default:
			return false, fmt.Errorf("trace_source supports %s, %s, %s and %s, not %s",
				query.Eq, query.Nq, query.In, query.NotIn, op)
		}
	}
	return holds, nil
}

// traceSourceValues flattens a filter value: a scalar, a list, or a comma-separated
// string (the shape list operators arrive in from some callers).
func traceSourceValues(raw any) []string {
	switch v := raw.(type) {
	case nil:
		return nil
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			out = append(out, fmt.Sprintf("%v", item))
		}
		return out
	case string:
		return strings.Split(v, ",")
	default:
		return []string{fmt.Sprintf("%v", v)}
	}
}

// whereHasField reports whether field appears anywhere in the clause tree.
func whereHasField(w query.QueryWhereClause, field string) bool {
	if _, ok := w.Binary[field]; ok {
		return true
	}
	for _, c := range w.And {
		if whereHasField(c, field) {
			return true
		}
	}
	for _, c := range w.Or {
		if whereHasField(c, field) {
			return true
		}
	}
	return w.Not != nil && whereHasField(*w.Not, field)
}

// applyTraceSourceFilter resolves trace_source in *where for the provider, logging when
// the filter rules out every span. It returns the message to show for that case, or ""
// when the query should run.
func applyTraceSourceFilter(ctx *security.RequestContext, accountID, provider string, where *query.QueryWhereClause) (string, error) {
	resolved, matchesNothing, err := resolveTraceSourceFilter(provider, *where)
	if err != nil {
		return "", err
	}
	*where = resolved
	if !matchesNothing {
		return "", nil
	}
	ctx.GetLogger().Info("trace_source_filter_matches_nothing", "account_id", accountID, "provider", provider)
	return fmt.Sprintf("%s holds no eBPF spans: every span it stores has trace_source=%s, so this trace_source filter matches nothing",
		provider, allProviderCaps[provider].TraceSource), nil
}
