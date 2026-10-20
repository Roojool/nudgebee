package observability

import (
	"sort"
	"strings"

	"nudgebee/services/security"
)

// fetchOpenObserveFallbackRounds evaluates queries whose fallbacks are written as a
// top-level `or` chain, one alternative at a time.
//
// The Prometheus builders express each node key as "preferred spelling or NudgeBee-agent
// spelling or OTel spelling", which Prometheus resolves by taking the first operand that
// returns anything. OpenObserve's engine does not implement that: a binary `or` yields an
// EMPTY result rather than an error, so the whole key silently reads as no-data even when
// the first operand on its own would have matched. Verified against OpenObserve v1.0.3 --
// `sum(irate(node_cpu_seconds_total{mode!="idle", instance=~"10.0.0.11.*"}[5m]))` returns
// 27 points, and the identical expression with an `or` arm appended returns nothing.
//
// Eleven of the twenty-five node keys carry such a chain, including every headline number
// on the Utilization & Health card (cpu_usage, memory_usage, disk_total, disk_used, the
// two *_line trends and pvc_usage), which is why node-scope utilisation rendered blank on
// OpenObserve while cluster scope -- served by the candidate-probing path in
// openobserve_queries.go -- was correct.
//
// The fallback is therefore performed here: round 0 asks every key's first operand, and
// each later round re-asks only the keys still empty, with their next operand. Cost is one
// request per distinct operand depth (three today) however many keys are requested, and a
// key with no `or` costs exactly the single round it always did.
func fetchOpenObserveFallbackRounds(
	ctx *security.RequestContext,
	req GetUtilisationTrendRequest,
	integrationSource string,
	queries map[string]string,
	instant bool,
) ([]QueryResult, error) {
	alternatives := make(map[string][]string, len(queries))
	maxRounds := 0
	for key, q := range queries {
		alts := expandOpenObserveAlternatives(splitTopLevelPromQLOr(q))
		alternatives[key] = alts
		if len(alts) > maxRounds {
			maxRounds = len(alts)
		}
	}

	resolved := make(map[string]QueryResult, len(queries))

	for round := 0; round < maxRounds; round++ {
		roundQueries := make(map[string]string)
		for key, alts := range alternatives {
			if round >= len(alts) {
				continue
			}
			// Only keys nothing has answered yet go on to the next alternative.
			if prev, ok := resolved[key]; ok && queryResultHasSamples(prev) {
				continue
			}
			roundQueries[key] = alts[round]
		}
		if len(roundQueries) == 0 {
			break
		}

		res, err := FetchMetricsQuery(ctx, FetchMetricsRequest{
			AccountId:            req.AccountId,
			MetricProvider:       "openobserve",
			MetricProviderSource: integrationSource,
			Queries:              roundQueries,
			StartTime:            req.StartTime,
			EndTime:              req.EndTime,
			StepInterval:         req.StepInterval,
			Instant:              instant,
		})
		if err != nil {
			return nil, err
		}

		for _, r := range res.Results {
			// A later round replaces an earlier one only when it actually found
			// samples, so for a key every alternative missed the caller still sees
			// the first attempt's query text and error rather than the last one's.
			if prev, ok := resolved[r.QueryKey]; ok && !queryResultHasSamples(r) && !queryResultHasSamples(prev) {
				continue
			}
			resolved[r.QueryKey] = r
		}
	}

	out := make([]QueryResult, 0, len(resolved))
	for _, r := range resolved {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].QueryKey < out[j].QueryKey })
	return out, nil
}

// openObserveLowercaseFamilyAliases maps the node-exporter families that carry capitals to
// the spelling OpenObserve actually answers to.
//
// OpenObserve stores each metric as a stream and normalises the stream NAME to lower case,
// but does not lower-case the name in a query — so an instance fed node_memory_MemTotal_bytes
// only ever matches node_memory_memtotal_bytes. These are the only families in the
// utilisation set carrying capitals, which is why memory can read empty on an instance whose
// CPU and disk resolve perfectly. OpenObserveClusterCandidates already probes both spellings
// for cluster scope; this is the node-scope counterpart.
var openObserveLowercaseFamilyAliases = map[string]string{
	"node_memory_Active_bytes":       "node_memory_active_bytes",
	"node_memory_MemTotal_bytes":     "node_memory_memtotal_bytes",
	"node_memory_MemAvailable_bytes": "node_memory_memavailable_bytes",
	"node_memory_MemFree_bytes":      "node_memory_memfree_bytes",
	"node_memory_Cached_bytes":       "node_memory_cached_bytes",
	"node_memory_Buffers_bytes":      "node_memory_buffers_bytes",
}

// expandOpenObserveAlternatives interleaves a lower-cased-family variant after each
// alternative that names a mixed-case family, preserving the builder's preference order:
// node-exporter spelling, the same family as OpenObserve stores it, then the fallbacks the
// builder already supplied.
func expandOpenObserveAlternatives(alts []string) []string {
	out := make([]string, 0, len(alts))
	for _, a := range alts {
		out = append(out, a)
		if lowered, changed := openObserveLowercaseFamilyVariant(a); changed {
			out = append(out, lowered)
		}
	}
	return out
}

// openObserveLowercaseFamilyVariant rewrites known mixed-case family names to the stored
// spelling, reporting whether anything changed. Only metric names are substituted, so a
// label value that happens to contain one is unaffected in practice: these names do not
// appear as matcher values in the utilisation queries.
func openObserveLowercaseFamilyVariant(expr string) (string, bool) {
	out := expr
	changed := false
	for mixed, lower := range openObserveLowercaseFamilyAliases {
		if strings.Contains(out, mixed) {
			out = strings.ReplaceAll(out, mixed, lower)
			changed = true
		}
	}
	return out, changed
}

// queryResultHasSamples reports whether a result carries at least one data point, which is
// what decides whether the next fallback alternative is worth asking for.
func queryResultHasSamples(r QueryResult) bool {
	if r.Error != nil {
		return false
	}
	for _, s := range r.Payload {
		if len(s.Values) > 0 {
			return true
		}
	}
	return false
}

// splitTopLevelPromQLOr splits a PromQL expression on its top-level `or` operators,
// ignoring any that appear inside parentheses, braces, brackets or a quoted literal. An
// expression with no top-level `or` comes back as a single element, so callers need no
// special case for it.
func splitTopLevelPromQLOr(expr string) []string {
	const doubleQuote = '"'
	const singleQuote = '\''
	const backQuote = '`'
	const backslash = '\\'

	runes := []rune(expr)
	var (
		parts []string
		depth int
		quote rune
		start int
	)

	for i := 0; i < len(runes); i++ {
		c := runes[i]

		if quote != 0 {
			switch c {
			case backslash:
				i++ // skip the escaped rune
			case quote:
				quote = 0
			}
			continue
		}

		switch c {
		case doubleQuote, singleQuote, backQuote:
			quote = c
			continue
		case '(', '{', '[':
			depth++
			continue
		case ')', '}', ']':
			depth--
			continue
		}

		if depth != 0 {
			continue
		}
		if c != 'o' && c != 'O' {
			continue
		}
		if i+1 >= len(runes) || (runes[i+1] != 'r' && runes[i+1] != 'R') {
			continue
		}
		// Must be a standalone operator, not the start or middle of an identifier
		// such as `order_total` or `ratio_or_x`.
		if i > 0 && isPromQLIdentRune(runes[i-1]) {
			continue
		}
		after := i + 2
		if after < len(runes) && isPromQLIdentRune(runes[after]) {
			continue
		}

		if seg := strings.TrimSpace(string(runes[start:i])); seg != "" {
			parts = append(parts, seg)
		}
		start = after
		i = after - 1
	}

	if seg := strings.TrimSpace(string(runes[start:])); seg != "" {
		parts = append(parts, seg)
	}
	if len(parts) == 0 {
		return []string{strings.TrimSpace(expr)}
	}
	return parts
}

// isPromQLIdentRune reports whether a rune can appear inside a PromQL identifier, so `or`
// is recognised as an operator only when it stands alone.
func isPromQLIdentRune(r rune) bool {
	return r == '_' || r == ':' ||
		(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}
