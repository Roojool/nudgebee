package observability

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"nudgebee/services/common"
	"nudgebee/services/integrations"
	"nudgebee/services/query"
	"nudgebee/services/security"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SplunkEnterpriseLogSource implements LogSource for Splunk Enterprise / Splunk Cloud
// Platform, querying SPL through the search REST API.
//
// Not to be confused with SplunkLogSource, which targets Splunk Observability Cloud
// (Log Observer) over the SignalFx API.
type SplunkEnterpriseLogSource struct{}

// splunkEnterpriseLogLabelMapping maps canonical field names to the Splunk field names
// produced by the Splunk Distribution of the OpenTelemetry Collector, which is the
// dominant ingestion path for Kubernetes logs into Splunk Enterprise. That collector
// forwards OTel resource attributes verbatim through HEC, so the dots survive
// (k8s.pod.name, not k8s_pod_name as OpenObserve would flatten it).
//
// A deployment using Splunk Connect for Kubernetes or a hand-rolled HEC pipeline lands
// different names; those can be remapped per-account through the label-mapping override
// rather than by editing this table.
//
// body/message name the OTel `body` field, which is what the collector writes and what the
// Message column displays. They are not mapped to _raw: on a structured event _raw is the
// whole JSON line, so an exact or prefix match on it can never hit and a substring match
// also hits the event's own metadata (a "checkout" search matched every event whose pod
// was named checkout). A filter on the message field is expanded by the builder to the
// same precedence display uses (splunkEnterpriseMessageFields), so unstructured
// sourcetypes, where only _raw exists, still match.
var splunkEnterpriseLogLabelMapping = map[string]string{
	"timestamp": "_time",
	"body":      "body",
	"message":   "body",
	"namespace": "k8s.namespace.name",
	"pod":       "k8s.pod.name",
	"container": "k8s.container.name",
	"node":      "k8s.node.name",
	"workload":  "k8s.deployment.name",
	"app":       "k8s.deployment.name",
	"cluster":   "k8s.cluster.name",
	// Splunk's own indexed `host` field, which the HEC exporter populates from the node.
	// OTel's host.name attribute is not written as a field on HEC log events.
	"host":     "host",
	"hostname": "host",
	"service":  "service.name",
	"severity": "severity_text",
	"level":    "severity_text",
}

// splunkEnterpriseSeverityFields are the field names checked, in order, for a log level.
// The OTel collector emits severity_text; hand-rolled pipelines commonly use one of the
// others.
var splunkEnterpriseSeverityFields = []string{"severity_text", "severity", "level", "log_level"}

// splunkEnterpriseMessageFields are the field names checked, in order, for the
// human-readable log line.
//
// _raw is the LAST resort, not the first. For structured HEC data — which is what the
// OpenTelemetry Collector writes, and the dominant ingestion path here — _raw is the
// entire event JSON, so using it directly renders every row in the Message column as
// `{"body": "...", "severity_text": "ERROR", "k8s.namespace.name": ...}` rather than the
// log line. That is unreadable in the table, and it also poisons anything downstream that
// treats Message as prose: the pattern hash behind log grouping would fold each event's
// k8s metadata into the pattern and split one recurring error into one group per pod.
// Only a genuinely unstructured sourcetype, where none of the named fields exist, falls
// through to _raw — and there _raw IS the log line. Same precedence the Log Groups
// aggregation resolves with coalesce().
var splunkEnterpriseMessageFields = []string{"body", "message", "log", "_raw"}

// splunkEnterpriseDefaultLimit matches the other log sources' default page size.
const splunkEnterpriseDefaultLimit = 100

// splunkEnterpriseMaxLimit caps how many events a single search returns. Splunk itself
// would happily stream far more; the ceiling exists so one query cannot exhaust the
// API server's memory decoding the response.
const splunkEnterpriseMaxLimit = 10000

// splunkEnterpriseSearchTimeout bounds a single oneshot search. Oneshot blocks until the
// search finishes, so this is the real ceiling on how long a slow SPL query ties up a
// request - longer than the OpenObserve equivalent because Splunk searches over a wide
// window are routinely slower than a columnar scan.
const splunkEnterpriseSearchTimeout = 60 * time.Second

// splunkEnterpriseDisallowedCommands are SPL commands rejected in a caller-supplied
// query. They either write (delete, collect, outputlookup and friends), execute
// (script, runshell), exfiltrate (sendemail, sendalert, rest), or run a search other
// than the one being validated (savedsearch, map, loadjob) — the last group matters
// because the guard can only reason about the SPL in front of it.
//
// This is defence in depth, not the primary control: the integration's Splunk account
// should be a dedicated role holding only the "search" capability with srchIndexesAllowed
// scoped to the configured indexes, which stops all of these at the server. The guard
// exists because a misconfigured deployment - a shared admin token pasted into the form -
// is common enough that a code-mode query box should not be a remote shell.
var splunkEnterpriseDisallowedCommands = map[string]bool{
	"delete":        true,
	"collect":       true,
	"mcollect":      true,
	"meventcollect": true,
	"tscollect":     true,
	"summaryindex":  true,
	"outputlookup":  true,
	"outputcsv":     true,
	"outputtext":    true,
	"sendemail":     true,
	"sendalert":     true,
	"script":        true,
	"runshell":      true,
	"rest":          true,
	"savedsearch":   true,
	"map":           true,
	"loadjob":       true,
	// Read counterparts of outputlookup/outputcsv. A LEADING `| inputlookup` is already
	// refused by the generating-command guard, but a subsearch is not:
	// `search index="otel_logs" [ | inputcsv secrets.csv ]` passed before this entry.
	// Both pull data from lookup tables and CSVs on the search head, outside the
	// configured index scope entirely.
	"inputlookup": true,
	"inputcsv":    true,
}

func (s *SplunkEnterpriseLogSource) GetSupportedOperators() []string {
	return splunkEnterpriseFilterOperators
}

func (s *SplunkEnterpriseLogSource) GetLabelMapping() map[string]string {
	return splunkEnterpriseLogLabelMapping
}

func (s *SplunkEnterpriseLogSource) GetIgnoredQueryRequestKeys() []string {
	return []string{}
}

// splunkEnterpriseSearchResponse is the envelope a oneshot search returns under
// output_mode=json.
type splunkEnterpriseSearchResponse struct {
	Messages []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"messages"`
	Results []map[string]any `json:"results"`
}

// escapeSplunkString escapes a value for a double-quoted SPL literal.
//
// '*' is deliberately left alone. Inside a quoted search term Splunk treats it as a
// wildcard and honours no escape for it - `name="chec\*"` still matches "checkout" on
// Splunk 10.x - so there is no way to match a literal asterisk, and emitting `\*` only
// suggested otherwise. Kubernetes names cannot contain '*', so a caller that sends one
// means a wildcard, and that is what it gets. Order is load-bearing - backslash first,
// or the escape introduced for the quote would itself be escaped.
func escapeSplunkString(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	// A newline cannot break out of a quoted literal, but it does corrupt the
	// form-encoded search parameter and produces an opaque Splunk parse error.
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	return value
}

// isSafeSplunkFieldName reports whether a field name is safe to interpolate into SPL as a
// bare token. Splunk field names routinely carry dots (k8s.pod.name), colons (OTel scope
// names) and hyphens, none of which can terminate the token, but anything outside this
// set - quotes, spaces, pipes, parentheses - could.
func isSafeSplunkFieldName(name string) bool {
	if name == "" || len(name) > 255 {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') &&
			r != '_' && r != '.' && r != ':' && r != '-' {
			return false
		}
	}
	return true
}

// splunkEnterpriseSplitPipeline splits an SPL string on pipes that are outside a quoted
// literal, so a pipe inside a search term is not mistaken for a command boundary.
func splunkEnterpriseSplitPipeline(spl string) []string {
	var segments []string
	var current strings.Builder
	inQuotes := false
	escaped := false

	for _, r := range spl {
		switch {
		case escaped:
			escaped = false
			current.WriteRune(r)
		case r == '\\':
			escaped = true
			current.WriteRune(r)
		case r == '"':
			inQuotes = !inQuotes
			current.WriteRune(r)
		case r == '|' && !inQuotes:
			segments = append(segments, current.String())
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}
	segments = append(segments, current.String())
	return segments
}

// validateSplunkEnterpriseQuery rejects an SPL query that could do more than read.
//
// Two rules. A query may not begin with a pipe, because a leading pipe means a generating
// command that ignores the index scope entirely. And no segment may invoke a command from
// splunkEnterpriseDisallowedCommands.
func validateSplunkEnterpriseQuery(spl string) error {
	trimmed := strings.TrimSpace(spl)
	if trimmed == "" {
		return fmt.Errorf("empty splunk query")
	}
	if strings.HasPrefix(trimmed, "|") {
		return fmt.Errorf("splunk query may not start with a generating command (a leading '|'): it would bypass the configured index scope")
	}

	for i, segment := range splunkEnterpriseSplitPipeline(trimmed) {
		fields := strings.Fields(strings.TrimSpace(segment))
		if len(fields) == 0 {
			continue
		}
		command := strings.ToLower(strings.TrimPrefix(fields[0], "|"))
		// The first segment is an implicit or explicit `search`; its first token is a
		// command only when it is spelled out.
		if i == 0 && command != "search" {
			continue
		}
		if splunkEnterpriseDisallowedCommands[command] {
			return fmt.Errorf("splunk command %q is not permitted: this integration may only read", command)
		}
	}
	return nil
}

// splunkEnterpriseFilterOperators are the operators buildSplunkEnterpriseBinaryClause
// renders. Logs and traces advertise exactly this list, so a capability check can never
// promise an operator the builder then rejects.
var splunkEnterpriseFilterOperators = []string{
	string(query.Eq), string(query.Nq),
	string(query.Contains), string(query.IContains), string(query.NIContains),
	string(query.Like), string(query.ILike), string(query.NLike),
	string(query.In), string(query.NotIn),
	string(query.Gt), string(query.Gte), string(query.Lt), string(query.Lte),
}

// errSplunkEnterpriseGroupInexpressible marks a filter group whose every predicate named
// a field the schema cannot express. It is not a user error in itself: inside an OR the
// branch is skipped, and only where skipping would widen the result (AND, NOT, top level)
// does it surface as one.
var errSplunkEnterpriseGroupInexpressible = errors.New("filter group has no field this schema can express")

// splunkEnterpriseFilterSchema is what differs between the log and trace filter builds:
// the canonical->Splunk field mapping, and an optional hook that rewrites each binary
// group before it is rendered (the trace source drops fields a span cannot express).
type splunkEnterpriseFilterSchema struct {
	mapping map[string]string
	// prepare returns the group to render and how many predicates it removed because the
	// schema cannot express them. A predicate removed because it is always true (not
	// counted) leaves an empty group that means "no restriction".
	prepare func(query.BinaryWhereClause) (query.BinaryWhereClause, int, error)
	// messageKeys are field names that mean "the log line"; a predicate on one is rendered
	// against messageFields in display precedence (renderSplunkEnterpriseMessagePredicate)
	// rather than against a single field.
	messageKeys   map[string]bool
	messageFields []string
}

var splunkEnterpriseLogFilterSchema = splunkEnterpriseFilterSchema{
	mapping:       splunkEnterpriseLogLabelMapping,
	messageKeys:   splunkEnterpriseLogMessageKeys,
	messageFields: splunkEnterpriseMessageFields,
}

// splunkEnterpriseLogMessageKeys are the field names, as they reach the builder after
// label mapping, that mean "the log line": `body` (canonical body/message map to it) and
// `_body`, the token the v1 logs tool documents for free-text search. An account-level
// label-mapping override that points body/message at another field is honoured as
// written - the admin has named the one field the message lives in - so the expansion
// applies only to the provider-default mapping.
var splunkEnterpriseLogMessageKeys = map[string]bool{"body": true, "message": true, "_body": true}

// splunkEnterpriseLikePattern renders a LIKE value as the body of a quoted SPL term.
//
// A value carrying SQL '%' wildcards is a pattern: '%' becomes Splunk's '*' and the match
// is anchored, so "panic%" is a prefix and "%x%" a substring. Keeping '%' literally (the
// old behaviour) made every such pattern match nothing, and '%' is what the logs agent
// writes. A bare word keeps the substring match callers have always had. '_' stays
// literal: Splunk has no single-character wildcard, and '_' is far more common as a
// literal in field values than as an intended wildcard.
func splunkEnterpriseLikePattern(value string) string {
	escaped := escapeSplunkString(value)
	if strings.Contains(value, "%") {
		return strings.ReplaceAll(escaped, "%", "*")
	}
	return "*" + escaped + "*"
}

// splunkEnterpriseListValues reads an _in / _not_in operand, which arrives from JSON as
// []any (or []string from Go callers).
func splunkEnterpriseListValues(op query.BinaryWhereClauseType, v any) ([]string, error) {
	var out []string
	switch list := v.(type) {
	case []any:
		for _, item := range list {
			out = append(out, fmt.Sprintf("%v", item))
		}
	case []string:
		out = append(out, list...)
	default:
		return nil, fmt.Errorf("operator %s needs a list of values, got %T", op, v)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("operator %s needs at least one value", op)
	}
	return out, nil
}

// splunkEnterpriseFilterNumber reads the operand of a numeric comparison. SPL compares
// numerically only when both sides are numbers, so a non-numeric operand is rejected
// rather than silently compared as text.
func splunkEnterpriseFilterNumber(op query.BinaryWhereClauseType, v any) (string, error) {
	switch n := v.(type) {
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64), nil
	case float32:
		return strconv.FormatFloat(float64(n), 'f', -1, 32), nil
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", n), nil
	case json.Number:
		if _, err := n.Float64(); err != nil {
			return "", fmt.Errorf("operator %s needs a number, got %q", op, n)
		}
		return n.String(), nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		if err != nil {
			return "", fmt.Errorf("operator %s needs a number, got %q", op, n)
		}
		return strconv.FormatFloat(f, 'f', -1, 64), nil
	default:
		return "", fmt.Errorf("operator %s needs a number, got %T", op, v)
	}
}

// renderSplunkEnterprisePredicate renders one field/operator pair as a single SPL term
// that is safe to join with AND or OR without further parentheses.
//
// Splunk term matching is case-insensitive by default, so _eq, _like and _contains are
// already the case-insensitive variants; there is no case-sensitive form to offer.
func renderSplunkEnterprisePredicate(col string, op query.BinaryWhereClauseType, v any) (string, error) {
	switch op {
	case query.Eq:
		return fmt.Sprintf(`%s="%s"`, col, escapeSplunkString(fmt.Sprintf("%v", v))), nil
	case query.Nq:
		return fmt.Sprintf(`NOT %s="%s"`, col, escapeSplunkString(fmt.Sprintf("%v", v))), nil
	case query.Contains, query.IContains:
		return fmt.Sprintf(`%s="*%s*"`, col, escapeSplunkString(fmt.Sprintf("%v", v))), nil
	case query.NIContains:
		return fmt.Sprintf(`NOT %s="*%s*"`, col, escapeSplunkString(fmt.Sprintf("%v", v))), nil
	case query.Like, query.ILike:
		return fmt.Sprintf(`%s="%s"`, col, splunkEnterpriseLikePattern(fmt.Sprintf("%v", v))), nil
	case query.NLike:
		return fmt.Sprintf(`NOT %s="%s"`, col, splunkEnterpriseLikePattern(fmt.Sprintf("%v", v))), nil
	case query.In, query.NotIn:
		values, err := splunkEnterpriseListValues(op, v)
		if err != nil {
			return "", err
		}
		terms := make([]string, len(values))
		for i, value := range values {
			terms[i] = fmt.Sprintf(`%s="%s"`, col, escapeSplunkString(value))
		}
		joined := "(" + strings.Join(terms, " OR ") + ")"
		if op == query.NotIn {
			return "NOT " + joined, nil
		}
		return joined, nil
	case query.Gt, query.Gte, query.Lt, query.Lte:
		n, err := splunkEnterpriseFilterNumber(op, v)
		if err != nil {
			return "", err
		}
		symbol := map[query.BinaryWhereClauseType]string{query.Gt: ">", query.Gte: ">=", query.Lt: "<", query.Lte: "<="}[op]
		return col + symbol + n, nil
	default:
		return "", fmt.Errorf("unsupported binary operator for Splunk: %s", op)
	}
}

// splunkEnterprisePositiveOperator pairs each negated operator with the operator it negates.
var splunkEnterprisePositiveOperator = map[query.BinaryWhereClauseType]query.BinaryWhereClauseType{
	query.Nq: query.Eq, query.NIContains: query.IContains, query.NLike: query.Like, query.NotIn: query.In,
}

// renderSplunkEnterpriseMessagePredicate renders a predicate on the log line against the
// first message field the event actually carries, in the order display resolves the
// Message column - so a filter matches exactly the text the user sees:
//
//	(body=V OR (NOT body=* AND message=V) OR ... OR (NOT body=* AND ... AND _raw=V))
//
// _raw is reached only by events with none of the named fields, i.e. unstructured
// sourcetypes, where _raw IS the log line. A negated operator negates the whole
// expression, so "does not contain X" means the displayed line does not contain X.
func renderSplunkEnterpriseMessagePredicate(fields []string, op query.BinaryWhereClauseType, v any) (string, error) {
	positive, negated := splunkEnterprisePositiveOperator[op]
	if !negated {
		positive = op
	}
	branches := make([]string, 0, len(fields))
	for i, field := range fields {
		term, err := renderSplunkEnterprisePredicate(field, positive, v)
		if err != nil {
			return "", err
		}
		guards := make([]string, 0, i+1)
		for _, earlier := range fields[:i] {
			guards = append(guards, "NOT "+earlier+"=*")
		}
		guards = append(guards, term)
		branches = append(branches, joinSplunkEnterpriseParts(guards, " AND "))
	}
	expr := "(" + strings.Join(branches, " OR ") + ")"
	if negated {
		return "NOT " + expr, nil
	}
	return expr, nil
}

// buildSplunkEnterpriseBinaryClause renders one binary group's predicates, joined by AND
// and parenthesised whenever there is more than one. The parentheses are load-bearing:
// SPL binds OR tighter than AND, so an unwrapped `a AND b` inside an OR would regroup as
// a AND (b OR ...) and change what matches.
func buildSplunkEnterpriseBinaryClause(binary query.BinaryWhereClause, schema splunkEnterpriseFilterSchema) (string, error) {
	// Field iteration order over a map is random; sorting keeps the generated SPL stable
	// so it is diffable in logs and assertable in tests.
	fields := make([]string, 0, len(binary))
	for field := range binary {
		fields = append(fields, field)
	}
	sort.Strings(fields)

	var parts []string
	for _, field := range fields {
		col := field
		if mapped, ok := schema.mapping[col]; ok {
			col = mapped
		}
		if schema.messageKeys[col] && len(schema.messageFields) > 0 {
			ops := binary[field]
			opNames := make([]string, 0, len(ops))
			for op := range ops {
				opNames = append(opNames, string(op))
			}
			sort.Strings(opNames)
			for _, opName := range opNames {
				part, err := renderSplunkEnterpriseMessagePredicate(schema.messageFields, query.BinaryWhereClauseType(opName), ops[query.BinaryWhereClauseType(opName)])
				if err != nil {
					return "", err
				}
				parts = append(parts, part)
			}
			continue
		}
		if !isSafeSplunkFieldName(col) {
			return "", fmt.Errorf("invalid or unsafe field name: %q", col)
		}

		ops := binary[field]
		opNames := make([]string, 0, len(ops))
		for op := range ops {
			opNames = append(opNames, string(op))
		}
		sort.Strings(opNames)

		for _, opName := range opNames {
			op := query.BinaryWhereClauseType(opName)
			part, err := renderSplunkEnterprisePredicate(col, op, ops[op])
			if err != nil {
				return "", err
			}
			parts = append(parts, part)
		}
	}
	return joinSplunkEnterpriseParts(parts, " AND "), nil
}

// joinSplunkEnterpriseParts joins rendered terms, parenthesising any multi-term result
// so it can be nested anywhere without changing precedence.
func joinSplunkEnterpriseParts(parts []string, sep string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	default:
		return "(" + strings.Join(parts, sep) + ")"
	}
}

// buildSplunkEnterpriseWhereClause renders a log filter, using the log label mapping.
func buildSplunkEnterpriseWhereClause(where query.QueryWhereClause) (string, error) {
	return buildSplunkEnterpriseFilter(where, splunkEnterpriseLogFilterSchema)
}

// buildSplunkEnterpriseFilter renders a filter tree. Every part a clause carries -
// _binary, _and, _or and _not - is ANDed together, as in the Hasura boolean expressions
// the clause is modelled on. (Returning as soon as _binary was non-empty silently
// dropped any sibling group, so filters came back wider than asked.)
//
// An empty result means "no restriction". A group whose predicates were all
// inexpressible for the schema returns errSplunkEnterpriseGroupInexpressible, which only
// an OR may absorb.
func buildSplunkEnterpriseFilter(where query.QueryWhereClause, schema splunkEnterpriseFilterSchema) (string, error) {
	var parts []string

	if len(where.Binary) > 0 {
		binary, dropped := where.Binary, 0
		if schema.prepare != nil {
			var err error
			if binary, dropped, err = schema.prepare(where.Binary); err != nil {
				return "", err
			}
		}
		// Whatever else the node carries, its siblings are ANDed with this group, so
		// rendering nothing for it would drop the restriction and widen the result.
		// Only an OR branch may absorb the error (buildSplunkEnterpriseOr).
		if len(binary) == 0 && dropped > 0 {
			return "", errSplunkEnterpriseGroupInexpressible
		}
		if len(binary) > 0 {
			part, err := buildSplunkEnterpriseBinaryClause(binary, schema)
			if err != nil {
				return "", err
			}
			parts = append(parts, part)
		}
	}

	for _, c := range where.And {
		part, err := buildSplunkEnterpriseFilter(c, schema)
		if err != nil {
			return "", err
		}
		if part != "" {
			parts = append(parts, part)
		}
	}

	if len(where.Or) > 0 {
		part, err := buildSplunkEnterpriseOr(where.Or, schema)
		if err != nil {
			return "", err
		}
		if part != "" {
			parts = append(parts, part)
		}
	}

	if where.Not != nil {
		notPart, err := buildSplunkEnterpriseFilter(*where.Not, schema)
		if err != nil {
			return "", err
		}
		if notPart != "" {
			parts = append(parts, fmt.Sprintf("NOT (%s)", notPart))
		}
	}

	return joinSplunkEnterpriseParts(parts, " AND "), nil
}

// buildSplunkEnterpriseOr renders an OR group. An inexpressible branch is skipped - the
// other branches can still answer, which is how a caller-OR-callee search keeps its
// caller side when the callee side names a field Splunk lacks. A branch with no
// restriction at all makes the whole OR unrestricted.
func buildSplunkEnterpriseOr(branches []query.QueryWhereClause, schema splunkEnterpriseFilterSchema) (string, error) {
	var parts []string
	skipped := 0
	for _, c := range branches {
		part, err := buildSplunkEnterpriseFilter(c, schema)
		if errors.Is(err, errSplunkEnterpriseGroupInexpressible) {
			skipped++
			continue
		}
		if err != nil {
			return "", err
		}
		if part == "" {
			return "", nil
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 && skipped > 0 {
		return "", errSplunkEnterpriseGroupInexpressible
	}
	return joinSplunkEnterpriseParts(parts, " OR "), nil
}

// splunkEnterpriseLimit clamps a requested page size into the supported range.
func splunkEnterpriseLimit(requested int) int {
	if requested <= 0 {
		return splunkEnterpriseDefaultLimit
	}
	if requested > splunkEnterpriseMaxLimit {
		return splunkEnterpriseMaxLimit
	}
	return requested
}

// splunkEnterpriseLogRowCol numbers a page's rows so an offset can drop the earlier ones.
const splunkEnterpriseLogRowCol = "nb_log_row"

// checkSplunkEnterpriseLogSort accepts only the order Splunk returns events in: newest
// first. Oldest-first would need `tail`, which cannot emit until the whole window is on
// the search head and, past limits.conf max_count, silently returns the tail of the
// capped set rather than the oldest events. Rejecting it beats a slow, sometimes-wrong
// page; no caller sends a sort today.
func checkSplunkEnterpriseLogSort(sortFields []SortField) error {
	for _, f := range sortFields {
		col := strings.ToLower(strings.TrimSpace(f.ColumnName))
		order := strings.ToLower(strings.TrimSpace(f.Order))
		if (col == "_time" || col == "timestamp") && (order == "" || order == "desc") {
			continue
		}
		return fmt.Errorf("splunk enterprise: sorting logs by %s %s is not supported - events are returned newest first", f.ColumnName, f.Order)
	}
	return nil
}

func (s *SplunkEnterpriseLogSource) buildSPL(req FetchLogRequest, index string) (string, error) {
	// Callers pass a config-resolved index, which GetSplunkEnterpriseConfig has already
	// validated. Re-checked here so the function is safe for any caller, not just that one.
	if !integrations.IsSafeSplunkIndexName(index) {
		return "", fmt.Errorf("invalid or unsafe index name: %q", index)
	}

	whereClause, err := buildSplunkEnterpriseWhereClause(req.QueryRequest.Where)
	if err != nil {
		return "", err
	}

	// Splunk returns events newest-first, so `head` yields the most recent N - the same
	// result as the ORDER BY _timestamp DESC LIMIT n the SQL-backed sources emit.
	if err := checkSplunkEnterpriseLogSort(req.SortFields); err != nil {
		return "", err
	}

	spl := fmt.Sprintf(`search index="%s"`, index)
	if whereClause != "" {
		spl += " " + whereClause
	}
	limit := splunkEnterpriseLimit(req.Limit)
	if req.Offset > 0 {
		// A page is the newest offset+limit events with the first offset dropped, by row
		// number. (`tail limit` looks equivalent but is wrong on a short last page: with
		// 25 events, offset 20 and limit 10 it returns rows 16-25, repeating the previous
		// page, where 21-25 are due.)
		// Compared by subtraction so an offset near MaxInt cannot wrap the sum negative.
		if req.Offset > splunkEnterpriseMaxLimit-limit {
			return "", fmt.Errorf("splunk enterprise: offset + limit may not exceed %d", splunkEnterpriseMaxLimit)
		}
		spl += fmt.Sprintf(" | head %d | streamstats count AS %s | where %s>%d | fields - %s",
			req.Offset+limit, splunkEnterpriseLogRowCol, splunkEnterpriseLogRowCol, req.Offset, splunkEnterpriseLogRowCol)
	} else {
		spl += fmt.Sprintf(" | head %d", limit)
	}
	// Splunk only returns a field if the search REFERENCES it. Without this, a search
	// that filters on nothing comes back with just the default fields (_time, host,
	// index, linecount, source, sourcetype, splunk_server) and every mapped label is
	// silently missing: severity renders blank, the k8s.* labels never populate, and
	// QueryLabels - which derives the label set from returned rows - can only offer
	// Splunk internals like _bkt and _cd, which build filters that match nothing.
	//
	// `fields *` asks for every extracted field rather than a fixed list because the
	// label mapping is per-deployment and overridable; naming columns here would
	// silently drop any field an operator remapped.
	spl += " | fields *"

	return spl, nil
}

func (s *SplunkEnterpriseLogSource) GetQuery(ctx *security.RequestContext, req FetchLogRequest) (string, error) {
	cfg, err := integrations.GetSplunkEnterpriseConfig(ctx, req.AccountId)
	if err != nil {
		return "", fmt.Errorf("failed to get Splunk Enterprise config: %w", err)
	}
	return s.buildSPL(req, cfg.LogIndex)
}

// splunkEnterpriseTimeRangeSeconds normalizes the request window (milliseconds) to the
// fractional epoch seconds Splunk's earliest_time/latest_time accept, defaulting to the
// last hour. Several callers - notably the label-value dropdown - omit the window
// entirely, and an unbounded Splunk search is far more expensive than an unbounded
// columnar scan.
func splunkEnterpriseTimeRangeSeconds(startMs, endMs int64, now time.Time) (string, string) {
	if endMs <= 0 {
		endMs = now.UnixMilli()
	}
	if startMs <= 0 {
		startMs = now.Add(-time.Hour).UnixMilli()
	}
	format := func(ms int64) string {
		return strconv.FormatFloat(float64(ms)/1000.0, 'f', 3, 64)
	}
	return format(startMs), format(endMs)
}

// runSplunkEnterpriseSearch executes an SPL query as a oneshot job and returns the decoded
// results.
//
// Oneshot rather than the create/poll/fetch job lifecycle: it blocks until the search
// finishes and returns results in the same response, which collapses three round trips
// into one and leaves no server-side job to reap. The tradeoff is that a slow search
// holds the request open, which splunkEnterpriseSearchTimeout bounds.
func runSplunkEnterpriseSearch(
	cfg integrations.SplunkEnterpriseConfig,
	spl string,
	startTime, endTime string,
	count int,
	timeout time.Duration,
) ([]map[string]any, error) {
	if err := validateSplunkEnterpriseQuery(spl); err != nil {
		return nil, err
	}
	return execSplunkEnterpriseSearch(cfg, spl, startTime, endTime, count, timeout)
}

// execSplunkEnterpriseSearch is the transport half of runSplunkEnterpriseSearch, with no
// validation of its own. Split out because metric queries are legitimately generating
// commands (`| mstats`, `| mcatalog`) that validateSplunkEnterpriseQuery must keep
// rejecting for log queries; they carry their own validator instead. Every caller is
// therefore responsible for validating BEFORE calling this.
func execSplunkEnterpriseSearch(
	cfg integrations.SplunkEnterpriseConfig,
	spl string,
	startTime, endTime string,
	count int,
	timeout time.Duration,
) ([]map[string]any, error) {
	// Every Splunk source (logs, traces, metrics) ends up here, so this is the one place
	// an inverted window is caught; Splunk would otherwise reject it with its own FATAL
	// message, which reads like a query error rather than a bad time range.
	if s, sErr := strconv.ParseFloat(startTime, 64); sErr == nil {
		if e, eErr := strconv.ParseFloat(endTime, 64); eErr == nil && s > e {
			return nil, fmt.Errorf("start_time must be before end_time")
		}
	}

	form := map[string]string{
		"search":        spl,
		"exec_mode":     "oneshot",
		"output_mode":   "json",
		"earliest_time": startTime,
		"latest_time":   endTime,
		"count":         strconv.Itoa(count),
	}

	headers := cfg.AuthHeaders()
	headers["Content-Type"] = "application/x-www-form-urlencoded"

	opts := []common.HttpOption{
		common.HttpWithHeaders(headers),
		common.HttpWithFormUrlEncodedBody(form),
		common.HttpWithTimeout(timeout),
	}
	if cfg.InsecureSkipVerify {
		opts = append(opts, common.HttpWithInsecureSkipVerify())
	}

	resp, err := common.HttpPost(cfg.SearchEndpoint(), opts...)
	if err != nil {
		return nil, fmt.Errorf("splunk search request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		var errorBody map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&errorBody)
		return nil, fmt.Errorf("splunk search failed with status %d: %v", resp.StatusCode, errorBody)
	}

	// Numbers stay as their literal text. Splunk returns every field as a JSON string
	// today, but a future field typed as a large integer (an epoch in nanoseconds, say)
	// would lose its low digits to float64 before any formatting ran.
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	var searchResp splunkEnterpriseSearchResponse
	if err := dec.Decode(&searchResp); err != nil {
		return nil, fmt.Errorf("failed to decode Splunk response: %w", err)
	}

	// A malformed search is reported inside a 200 response rather than as an HTTP error,
	// so the envelope has to be inspected or the caller sees "no logs" for what is
	// actually a broken query.
	for _, message := range searchResp.Messages {
		switch strings.ToUpper(message.Type) {
		case "FATAL", "ERROR":
			return nil, fmt.Errorf("splunk search error: %s", message.Text)
		}
	}

	return searchResp.Results, nil
}

// formatSplunkValue renders a decoded JSON cell as a plain string.
//
// Splunk returns multivalue fields as arrays; those are joined rather than dropped so a
// pod with two values for a field still shows both.
func formatSplunkValue(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return value
	case json.Number:
		return value.String()
	case bool:
		return strconv.FormatBool(value)
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			parts = append(parts, formatSplunkValue(item))
		}
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprintf("%v", v)
	}
}

// splunkEnterpriseTimestamp normalizes Splunk's _time into the RFC3339Nano UTC string the
// other log sources emit. Splunk renders _time as ISO 8601 with a numeric offset
// (2026-08-26T10:11:12.000+00:00); anything unparseable is passed through so a custom
// time format still reaches the UI rather than becoming an empty cell.
func splunkEnterpriseTimestamp(raw string) string {
	if parsed, ok := parseSplunkEnterpriseTime(raw); ok {
		return parsed.UTC().Format(time.RFC3339Nano)
	}
	return raw
}

// parseSplunkEnterpriseTime parses a Splunk _time value in the layouts Splunk emits.
func parseSplunkEnterpriseTime(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999-0700"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func (s *SplunkEnterpriseLogSource) QueryLogs(ctx *security.RequestContext, req FetchLogRequest) ([]OutputLog, error) {
	results, err := s.queryLogRows(ctx, req)
	if err != nil {
		return nil, err
	}

	outputs := make([]OutputLog, 0, len(results))
	for _, result := range results {
		out := OutputLog{Labels: make(map[string]any, len(result))}

		for k, v := range result {
			out.Labels[k] = v
		}

		rawTime := formatSplunkValue(result["_time"])
		parsedTime, timeOK := parseSplunkEnterpriseTime(rawTime)
		out.Timestamp = rawTime
		if timeOK {
			out.Timestamp = parsedTime.UTC().Format(time.RFC3339Nano)
		}
		out.Message = firstSplunkValue(result, splunkEnterpriseMessageFields)

		for _, field := range splunkEnterpriseSeverityFields {
			if severity := formatSplunkValue(result[field]); severity != "" {
				out.Severity = severity
				break
			}
		}

		setSplunkEnterpriseCanonicalLabels(&out, parsedTime, timeOK)
		outputs = append(outputs, out)
	}

	return outputs, nil
}

// setSplunkEnterpriseCanonicalLabels fills the canonical label names the UI and the logs
// agent read, deterministically and only where the event has no field of that name.
//
// The shared normaliser keeps ONE canonical name per provider field, picked by map
// iteration order, and this mapping points several canonical names at one field
// (severity/level, workload/app, body/message, host/hostname) - so which alias appeared
// changed from call to call, and `message` was sometimes the whole _raw JSON. Filling
// them all here leaves the normaliser nothing to choose. `timestamp` is epoch
// nanoseconds, the unit the log table divides by 1e6 to render; an ISO string there
// renders as Invalid Date.
func setSplunkEnterpriseCanonicalLabels(out *OutputLog, eventTime time.Time, hasTime bool) {
	if out.Labels == nil {
		out.Labels = make(map[string]any)
	}
	fill := func(key string, value any) {
		if _, exists := out.Labels[key]; !exists && value != nil && value != "" {
			out.Labels[key] = value
		}
	}
	if hasTime {
		out.Labels["timestamp"] = eventTime.UnixNano()
	}
	for canonical, field := range splunkEnterpriseLogLabelMapping {
		switch canonical {
		case "timestamp":
			continue
		case "body", "message":
			fill(canonical, out.Message)
		case "severity", "level":
			fill(canonical, out.Severity)
		default:
			fill(canonical, out.Labels[field])
		}
	}
}

// queryLogRows runs the log search and returns Splunk's rows as-is. QueryLabels reads
// these rather than QueryLogs' decorated output, so the label list offers the fields the
// events really carry, not the canonical aliases QueryLogs adds.
func (s *SplunkEnterpriseLogSource) queryLogRows(ctx *security.RequestContext, req FetchLogRequest) ([]map[string]any, error) {
	cfg, err := integrations.GetSplunkEnterpriseConfig(ctx, req.AccountId)
	if err != nil {
		return nil, fmt.Errorf("failed to get Splunk Enterprise config: %w", err)
	}

	// FetchLogs populates Query from GetQuery when the caller supplied only a where
	// clause, so by this point Query holds either that generated SPL or a query the user
	// typed in code mode. Running it either way is what the Loki and Signoz sources do;
	// validateSplunkEnterpriseQuery inside runSplunkEnterpriseSearch is what makes the
	// second case safe. Note that a hand-written query bypasses the account's default log
	// filters, which are applied to the where clause rather than to the rendered query -
	// the same tradeoff those sources make.
	spl := strings.TrimSpace(req.Query)
	if spl == "" || req.Offset > 0 {
		generated, genErr := s.buildSPL(req, cfg.LogIndex)
		if genErr != nil {
			return nil, genErr
		}
		switch {
		case spl == "":
			spl = generated
		case spl != generated:
			// Paging lives in the SPL buildSPL renders (head + row number). A hand-written
			// query has no such slice, so an offset on it would silently return page one;
			// the caller can page its own SPL instead.
			return nil, fmt.Errorf("splunk enterprise: offset is not supported with a hand-written query - add head/tail to the SPL instead")
		}
	}

	startTime, endTime := splunkEnterpriseTimeRangeSeconds(req.StartTime, req.EndTime, time.Now())

	// The rendered SPL already slices the page, so Splunk never needs to return more
	// than one page of rows.
	return runSplunkEnterpriseSearch(
		cfg, spl, startTime, endTime, splunkEnterpriseLimit(req.Limit), splunkEnterpriseSearchTimeout,
	)
}

func buildSplunkEnterpriseLabelSampleRequest(req FetchLogLabelRequest, now time.Time) FetchLogRequest {
	startTime, endTime := req.StartTime, req.EndTime
	if startTime == 0 && endTime == 0 {
		endTime = now.UnixMilli()
		startTime = now.Add(-time.Hour).UnixMilli()
	}

	return FetchLogRequest{
		AccountId:         req.AccountId,
		LogProvider:       req.LogProvider,
		LogProviderSource: req.LogProviderSource,
		StartTime:         startTime,
		EndTime:           endTime,
		Limit:             splunkEnterpriseDefaultLimit,
	}
}

// splunkEnterpriseLogInternalFields are fields Splunk adds to every event that no filter
// can usefully target. host, source, sourcetype and index are deliberately NOT here: they
// are Splunk's primary log-selection fields, and host is what the canonical host label
// maps to. Every _-prefixed field is internal too (see isSplunkEnterpriseLogInternalField).
var splunkEnterpriseLogInternalFields = map[string]bool{
	"punct": true, "linecount": true, "splunk_server": true, "splunk_server_group": true,
	"eventtype": true, "tag": true, "tag::eventtype": true,
}

func isSplunkEnterpriseLogInternalField(name string) bool {
	return strings.HasPrefix(name, "_") || splunkEnterpriseLogInternalFields[name]
}

func collectSplunkEnterpriseLogLabels(rows []map[string]any) []OutputLogLabel {
	labelSet := make(map[string]struct{})
	for _, row := range rows {
		for name := range row {
			if isSplunkEnterpriseLogInternalField(name) {
				continue
			}
			labelSet[name] = struct{}{}
		}
	}

	labelNames := make([]string, 0, len(labelSet))
	for name := range labelSet {
		labelNames = append(labelNames, name)
	}
	sort.Strings(labelNames)

	labels := make([]OutputLogLabel, 0, len(labelNames))
	for _, name := range labelNames {
		labels = append(labels, OutputLogLabel{Label: name, Attributes: make(map[string]any)})
	}
	return labels
}

// QueryLabels reports the field names present on a sample of recent events.
//
// Splunk can answer this directly with `| fieldsummary`, but that command reports every
// field the search-time extractions produce over the sampled window - which for a raw
// sourcetype is effectively unbounded. Sampling events and taking the union of their keys
// gives the same answer for structured (HEC/OTel) data at a bounded cost, and matches
// what the other schemaless sources do.
func (s *SplunkEnterpriseLogSource) QueryLabels(ctx *security.RequestContext, req FetchLogLabelRequest) ([]OutputLogLabel, error) {
	fetchReq := buildSplunkEnterpriseLabelSampleRequest(req, time.Now())

	rows, err := s.queryLogRows(ctx, fetchReq)
	if err != nil {
		return nil, err
	}

	return collectSplunkEnterpriseLogLabels(rows), nil
}

// splunkEnterpriseLabelValueLimit caps how many distinct values the dropdown offers.
const splunkEnterpriseLabelValueLimit = 100

// splunkEnterpriseLabelValueMessageCol holds the resolved log line when the requested
// label is the message field.
const splunkEnterpriseLabelValueMessageCol = "nb_lv_message"

// buildSplunkEnterpriseLabelValuesSPL renders the value lookup for one label, narrowed by
// the filters already applied in the UI so a cascading dropdown offers only values that
// still match (pods of the selected namespace, not of every namespace). Returns the SPL
// and the result column holding the value.
//
// `top` ranks by frequency and caps server-side, so the most useful values surface first
// even when the field has a long tail. The trailing `fields` drops top's count and
// percent columns. The message field resolves the displayed line (coalesce, in display
// order) rather than one field, which on structured events would be the whole _raw JSON.
func buildSplunkEnterpriseLabelValuesSPL(index, col string, currentFilters map[string]string) (string, string, error) {
	filters := query.BinaryWhereClause{}
	for label, value := range currentFilters {
		if strings.TrimSpace(label) == "" || value == "" {
			continue
		}
		filters[label] = map[query.BinaryWhereClauseType]any{query.Eq: value}
	}
	filterClause, err := buildSplunkEnterpriseWhereClause(query.QueryWhereClause{Binary: filters})
	if err != nil {
		return "", "", err
	}
	search := fmt.Sprintf(`search index="%s"`, index)
	if filterClause != "" {
		search += " " + filterClause
	}

	if splunkEnterpriseLogMessageKeys[col] {
		valueCol := splunkEnterpriseLabelValueMessageCol
		return fmt.Sprintf(`%s | eval %s=coalesce(%s) | top limit=%d %s | fields %s`,
			search, valueCol, strings.Join(splunkEnterpriseMessageFields, ", "),
			splunkEnterpriseLabelValueLimit, valueCol, valueCol), valueCol, nil
	}
	return fmt.Sprintf(`%s %s=* | top limit=%d %s | fields %s`,
		search, col, splunkEnterpriseLabelValueLimit, col, col), col, nil
}

func (s *SplunkEnterpriseLogSource) QueryLabelValues(ctx *security.RequestContext, req FetchLogLabelValuesRequest) ([]OutputLogLabelValue, error) {
	cfg, err := integrations.GetSplunkEnterpriseConfig(ctx, req.AccountId)
	if err != nil {
		return nil, fmt.Errorf("failed to get Splunk Enterprise config: %w", err)
	}

	col := req.LabelName
	if mapped, ok := splunkEnterpriseLogLabelMapping[col]; ok {
		col = mapped
	}
	if !isSafeSplunkFieldName(col) {
		return nil, fmt.Errorf("invalid or unsafe label name: %q", col)
	}
	if !integrations.IsSafeSplunkIndexName(cfg.LogIndex) {
		return nil, fmt.Errorf("invalid or unsafe index name: %q", cfg.LogIndex)
	}

	spl, valueCol, err := buildSplunkEnterpriseLabelValuesSPL(cfg.LogIndex, col, req.CurrentFilters)
	if err != nil {
		return nil, err
	}

	startTime, endTime := splunkEnterpriseTimeRangeSeconds(req.StartTime, req.EndTime, time.Now())

	results, err := runSplunkEnterpriseSearch(
		cfg, spl, startTime, endTime, splunkEnterpriseLabelValueLimit, 30*time.Second,
	)
	if err != nil {
		return nil, err
	}

	values := make([]OutputLogLabelValue, 0, len(results))
	for _, result := range results {
		strVal := formatSplunkValue(result[valueCol])
		if strVal == "" {
			continue
		}
		values = append(values, OutputLogLabelValue{
			Value:      strVal,
			Attributes: make(map[string]any),
		})
	}

	return values, nil
}
