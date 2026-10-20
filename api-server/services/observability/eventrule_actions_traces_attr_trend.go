package observability

import (
	"fmt"
	"math"
	"nudgebee/services/common"
	"nudgebee/services/eventrule/playbooks"
	"sort"
	"strconv"
	"strings"
)

const (
	// attrTrendMinSamples is how many spans of one operation must carry the
	// attribute before a trend is reported; fewer is noise.
	attrTrendMinSamples = 5
	// attrTrendMinGrowth is how many times its first value the last value must
	// be. Per-request counters that merely tick up stay below it.
	attrTrendMinGrowth = 10
	// attrTrendMaxInsights caps the insights so a wide span schema can't flood
	// the evidence.
	attrTrendMaxInsights = 3
	// attrTrendMaxConstants caps the constant flags listed per insight.
	attrTrendMaxConstants = 3
)

// spanAttributeTrendInsights reports numeric span attributes that only grow
// across one operation's spans in the window, together with the boolean
// attributes every one of those spans carried. A failure with no error spans —
// an OOM, a slow leak — often shows only here: on event 07f70dc8 the
// recommendation spans' product count rose 403 -> 12.9M in three minutes, all
// with the cache feature flag on, while the evidence reported nothing but a
// trace path.
func spanAttributeTrendInsights(spans []common.OpenTelemetryTrace) []playbooks.PlaybookActionResponseInsight {
	type opKey struct{ service, span string }
	groups := map[opKey][]common.OpenTelemetryTrace{}
	var order []opKey
	for _, s := range spans {
		if len(s.SpanAttributes) == 0 {
			continue
		}
		k := opKey{s.ServiceName, s.SpanName}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], s)
	}

	var insights []playbooks.PlaybookActionResponseInsight
	for _, k := range order {
		group := groups[k]
		if len(group) < attrTrendMinSamples {
			continue
		}
		sort.SliceStable(group, func(i, j int) bool { return group[i].Timestamp < group[j].Timestamp })

		keys := sortedAttrKeys(group)
		consts := constantBoolAttrs(group, keys)
		service, span := k.service, k.span
		if service == "" {
			service = "unknown service"
		}
		if span == "" {
			span = "unknown"
		}
		for _, attr := range keys {
			t, ok := growingNumericAttr(group, attr)
			if !ok {
				continue
			}
			msg := fmt.Sprintf("Span attribute `%s` on `%s` spans of %s rose from %s to %s across %d spans (%s -> %s), never decreasing.",
				attr, span, service, formatAttrNumber(t.first), formatAttrNumber(t.last), t.n, t.firstAt, t.lastAt)
			if len(consts) > 0 {
				msg += " Every one of these spans also carried " + strings.Join(consts, ", ") + "."
			}
			insights = append(insights, playbooks.PlaybookActionResponseInsight{Message: msg, Severity: "High"})
			if len(insights) == attrTrendMaxInsights {
				return insights
			}
		}
	}
	return insights
}

func sortedAttrKeys(group []common.OpenTelemetryTrace) []string {
	seen := map[string]bool{}
	var keys []string
	for _, s := range group {
		for k := range s.SpanAttributes {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

type attrTrend struct {
	first, last     float64
	firstAt, lastAt string
	n               int
}

// growingNumericAttr reports whether attr is numeric on at least
// attrTrendMinSamples spans, never decreases in span order, and ends at least
// attrTrendMinGrowth times where it started. The timestamps are those of the
// spans that carried the first and last values, not of the group's ends.
func growingNumericAttr(group []common.OpenTelemetryTrace, attr string) (attrTrend, bool) {
	var t attrTrend
	for _, s := range group {
		raw, present := s.SpanAttributes[attr]
		if !present {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		// ParseFloat accepts "NaN" and "Inf"; NaN fails every comparison below
		// and would pass as a trend.
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return attrTrend{}, false
		}
		if t.n > 0 && v < t.last {
			return attrTrend{}, false
		}
		if t.n == 0 {
			t.first, t.firstAt = v, s.Timestamp
		}
		t.last, t.lastAt = v, s.Timestamp
		t.n++
	}
	if t.n < attrTrendMinSamples || t.first <= 0 || t.last < t.first*attrTrendMinGrowth {
		return attrTrend{}, false
	}
	return t, true
}

// constantBoolAttrs lists `key=value` for boolean attributes that hold the same
// value on every span of the group.
func constantBoolAttrs(group []common.OpenTelemetryTrace, keys []string) []string {
	var out []string
	for _, attr := range keys {
		val, same := "", true
		for i, s := range group {
			v, present := s.SpanAttributes[attr]
			if !present || (v != "true" && v != "false") || (i > 0 && v != val) {
				same = false
				break
			}
			val = v
		}
		if same {
			out = append(out, fmt.Sprintf("`%s=%s`", attr, val))
			if len(out) == attrTrendMaxConstants {
				break
			}
		}
	}
	return out
}

func formatAttrNumber(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
