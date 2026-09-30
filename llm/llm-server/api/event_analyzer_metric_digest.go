package api

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"nudgebee/llm/events"
)

// Bounds on the metric digest rendered into the investigation prompt. The full
// series are in the structured evidence workspace file; the digest only has to
// show the shape (when it started, how high it went, where it ended).
const (
	maxMetricDigestSeries = 12
	maxMetricDigestBytes  = 2500
	maxMetricDigestDepth  = 8
)

// metricPoint is one sample of a digested series.
type metricPoint struct {
	at    time.Time
	value float64
}

type metricSeries struct {
	name   string
	points []metricPoint
	// labels are the series' own Prometheus labels, when it has them.
	labels map[string]any
	// alerting marks the series of the object the alert fired for, as the
	// prometheus_enricher flags it; the rule's other breaching objects are not.
	alerting bool
	// key identifies the series across cards for dedupe.
	key string
}

// renderMetricDigest summarises every metric series the enrichers attached to the
// event as one line per series: point count and span, first, min, max (with their
// times) and last value.
//
// Collected Evidence used to carry only the insight messages ("Fetch metric data on
// alarm"), never the values, while the prompt told the investigation to reuse that
// evidence rather than refetch it. Runs then reasoned about a CPU alarm without the
// series that showed saturation starting an hour earlier (R0057) or made no tool
// calls at all (R0167).
//
// Series are found structurally — any object holding equal-length `values` and
// `timestamps` arrays — so the CloudWatch `items[]` shape, the provider-agnostic
// `metric_groups` / `series_list_result` shape and the Kubernetes pod-metrics shape
// are all covered without per-provider code.
//
// A card that says what it measured (its query, and the unit when the query settles
// it) gets that stated once, with its series nested under it: rate()/sum() series
// carry no __name__, so without it the model got numbers with no metric or unit.
//
// A rule that fires per object returns every breaching object, so a card can hold
// several databases or volumes. Each series is named by the labels that tell it
// apart within its card, and the series of the object the alert fired for is
// tagged and listed first, with its card ahead of the rest, so the budget below
// never cuts it and the model does not quote another object's number.
func renderMetricDigest(ev events.InvestigateData) string {
	type digestCard struct {
		header   string
		lines    []string
		alerting bool
	}
	// The same series is often stored twice (grouped and flat), so dedupe it before
	// counting what did not fit. The key is the series' own name, labels and values,
	// not the rendered line: the labels added to tell a card's series apart depend on
	// the card, so two copies in different cards would render differently. A copy
	// flagged alerting marks every copy, whichever one is kept.
	evidenceCards := allEvidenceCards(ev)
	collected := make([][]metricSeries, 0, len(evidenceCards))
	alertingKeys := make(map[string]bool)
	for _, card := range evidenceCards {
		var series []metricSeries
		collectMetricSeries(parseEvidenceData(card.Data), "", 0, &series)
		for i := range series {
			series[i].key = seriesDedupeKey(series[i])
			alertingKeys[series[i].key] = alertingKeys[series[i].key] || series[i].alerting
		}
		collected = append(collected, series)
	}

	var cards []digestCard
	total := 0
	seen := make(map[string]bool)
	for i, card := range evidenceCards {
		series := collected[i]
		for j := range series {
			series[j].alerting = alertingKeys[series[j].key]
		}
		nameSeriesApart(series)
		sort.SliceStable(series, func(i, j int) bool { return series[i].alerting && !series[j].alerting })
		c := digestCard{header: metricCardHeader(card)}
		for _, s := range series {
			if !seen[s.key] {
				seen[s.key] = true
				c.lines = append(c.lines, formatMetricSeries(s))
				c.alerting = c.alerting || s.alerting
			}
		}
		if len(c.lines) > 0 {
			cards = append(cards, c)
			total += len(c.lines)
		}
	}
	sort.SliceStable(cards, func(i, j int) bool { return cards[i].alerting && !cards[j].alerting })

	var b strings.Builder
	rendered := 0
	for _, c := range cards {
		indent := ""
		if c.header != "" {
			indent = "  "
		}
		for i, line := range c.lines {
			header := ""
			if i == 0 {
				header = c.header
			}
			if rendered == maxMetricDigestSeries || b.Len()+len(header)+len(indent)+len(line) > maxMetricDigestBytes {
				fmt.Fprintf(&b, "- … %d more series in the structured evidence file\n", total-rendered)
				return b.String()
			}
			b.WriteString(header + indent + line)
			rendered++
		}
	}
	return b.String()
}

// metricCardHeader is the line naming what a card's series measure, or "" when
// the card does not say.
func metricCardHeader(card events.InvestigateDataInsight) string {
	if card.Query == "" {
		return ""
	}
	if card.Unit != "" {
		return fmt.Sprintf("- query `%s` · unit: %s\n", card.Query, card.Unit)
	}
	return fmt.Sprintf("- query `%s`\n", card.Query)
}

// allEvidenceCards returns every evidence section that carries data.
func allEvidenceCards(ev events.InvestigateData) []events.InvestigateDataInsight {
	var out []events.InvestigateDataInsight
	for _, d := range []events.InvestigateDataInsight{
		ev.PodData, ev.NodeData, ev.Deployment, ev.AlertLabels, ev.JobInformation,
		ev.JobEvents, ev.JobPodEvents, ev.RelatedEvents, ev.ContainerMetrics,
		ev.Traces, ev.AlertData, ev.ServiceMap,
	} {
		if d.Data != nil {
			out = append(out, d)
		}
	}
	for _, list := range [][]events.InvestigateDataInsight{
		ev.PodMetrics, ev.NodeMetrics, ev.NoisyNeighbours, ev.ApiFailures,
		ev.PodEvents, ev.NodeEvents, ev.Markdowns, ev.UserActions,
		ev.RDBMSQueryData, ev.MetricsData, ev.Others,
	} {
		for _, d := range list {
			if d.Data != nil {
				out = append(out, d)
			}
		}
	}
	return out
}

// parseEvidenceData decodes evidence stored as a JSON string; anything else is
// returned as is.
func parseEvidenceData(data any) any {
	s, ok := data.(string)
	if !ok {
		return data
	}
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return nil
	}
	return decoded
}

func collectMetricSeries(node any, parentKey string, depth int, out *[]metricSeries) {
	if depth > maxMetricDigestDepth {
		return
	}
	switch v := node.(type) {
	case map[string]any:
		if s, ok := metricSeriesFrom(v, parentKey); ok {
			*out = append(*out, s)
			return
		}
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			// A container key (series_list_result, items, data) says nothing about
			// the series; keep the meaningful key above it (a metric group name).
			childKey := k
			if genericMetricKeys[k] {
				childKey = parentKey
			}
			collectMetricSeries(v[k], childKey, depth+1, out)
		}
	case []any:
		for _, item := range v {
			collectMetricSeries(item, parentKey, depth+1, out)
		}
	}
}

func metricSeriesFrom(m map[string]any, parentKey string) (metricSeries, bool) {
	values, okV := m["values"].([]any)
	stamps, okT := m["timestamps"].([]any)
	if !okV || !okT || len(values) == 0 || len(values) != len(stamps) {
		return metricSeries{}, false
	}
	points := make([]metricPoint, 0, len(values))
	for i := range values {
		value, okValue := metricNumber(values[i])
		at, okTime := metricTime(stamps[i])
		if okValue && okTime {
			points = append(points, metricPoint{at: at, value: value})
		}
	}
	if len(points) == 0 {
		return metricSeries{}, false
	}
	sort.SliceStable(points, func(i, j int) bool { return points[i].at.Before(points[j].at) })
	labels, _ := m["metric"].(map[string]any)
	alerting, _ := m["alerting"].(bool)
	return metricSeries{name: metricSeriesName(m, parentKey), points: points, labels: labels, alerting: alerting}, true
}

// seriesDedupeKey identifies a series by its name, its own labels and its values.
// The labels keep two objects apart whose values happen to match (both volumes
// flat at 0), so neither is dropped nor takes the other's alerting tag.
func seriesDedupeKey(s metricSeries) string {
	labels := make([]string, 0, len(s.labels))
	for k, v := range s.labels {
		labels = append(labels, fmt.Sprintf("%s=%v", k, v))
	}
	sort.Strings(labels)
	return formatMetricSeries(metricSeries{name: s.name, points: s.points}) + "{" + strings.Join(labels, ",") + "}"
}

// maxDistinguishingLabels bounds how many labels are added to tell a card's
// series apart.
const maxDistinguishingLabels = 3

// nameSeriesApart appends to each series' name the labels whose values differ
// across the card's series and are not in the name already. The name is built
// from a fixed set of labels, which leaves out the one a per-object rule varies
// by (datname, deployment, persistentvolumeclaim, queue), so two volumes in one
// namespace came out under the same name with different numbers.
func nameSeriesApart(series []metricSeries) {
	values := map[string]map[string]bool{}
	for _, s := range series {
		for k, v := range s.labels {
			if str, ok := v.(string); ok {
				if values[k] == nil {
					values[k] = map[string]bool{}
				}
				values[k][str] = true
			}
		}
	}
	var keys []string
	for k, vs := range values {
		if len(vs) > 1 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for i := range series {
		var extra []string
		for _, k := range keys {
			v, _ := series[i].labels[k].(string)
			if v == "" || strings.Contains(series[i].name, v) {
				continue
			}
			if len(extra) == maxDistinguishingLabels {
				break
			}
			extra = append(extra, k+"="+v)
		}
		if len(extra) > 0 {
			series[i].name += " " + strings.Join(extra, " ")
		}
	}
}

// genericMetricKeys are container keys that never name a series.
var genericMetricKeys = map[string]bool{"": true, "data": true, "items": true, "series_list_result": true, "metric_groups": true, "result": true, "results": true, "payload": true}

// metricSeriesName builds a readable name from the series' own labels, falling back
// to the key it was found under.
func metricSeriesName(m map[string]any, parentKey string) string {
	labels := m
	if metric, ok := m["metric"].(map[string]any); ok {
		labels = metric
	}
	var parts []string
	for _, k := range []string{"__name__", "name", "statistics", "stat", "resource_id", "namespace", "pod", "container", "instance"} {
		if s, ok := labels[k].(string); ok && s != "" {
			parts = append(parts, s)
		} else if s, ok := m[k].(string); ok && s != "" {
			parts = append(parts, s)
		}
	}
	if !genericMetricKeys[parentKey] && (len(parts) == 0 || !strings.Contains(parentKey, parts[0])) {
		parts = append([]string{parentKey}, parts...)
	}
	if len(parts) == 0 {
		return "series"
	}
	return strings.Join(dedupeStrings(parts), " ")
}

func formatMetricSeries(s metricSeries) string {
	first, last := s.points[0], s.points[len(s.points)-1]
	lo, hi := first, first
	for _, p := range s.points {
		if p.value < lo.value {
			lo = p
		}
		if p.value > hi.value {
			hi = p
		}
	}
	name := s.name
	if s.alerting {
		name += " [the object this alert fired for]"
	}
	return fmt.Sprintf("- %s: %d points %s→%s UTC · first %s · min %s @%s · max %s @%s · last %s\n",
		name, len(s.points), metricClock(first.at), metricClock(last.at),
		metricValue(first.value), metricValue(lo.value), metricClock(lo.at),
		metricValue(hi.value), metricClock(hi.at), metricValue(last.value))
}

func metricNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, !math.IsNaN(n) && !math.IsInf(n, 0)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	return 0, false
}

// metricTime accepts RFC3339 strings and unix timestamps in seconds or
// milliseconds, as numbers or numeric strings.
func metricTime(v any) (time.Time, bool) {
	switch t := v.(type) {
	case string:
		if parsed, err := time.Parse(time.RFC3339, t); err == nil {
			return parsed.UTC(), true
		}
		if f, err := strconv.ParseFloat(t, 64); err == nil {
			return unixTime(f), true
		}
	case float64:
		return unixTime(t), true
	}
	return time.Time{}, false
}

func unixTime(f float64) time.Time {
	if f > 1e12 {
		return time.UnixMilli(int64(f)).UTC()
	}
	return time.Unix(int64(f), 0).UTC()
}

func metricClock(t time.Time) string {
	return t.Format("01-02 15:04")
}

// metricValue renders large byte-sized values compactly and small ratios with
// enough precision to compare against a threshold.
func metricValue(v float64) string {
	abs := math.Abs(v)
	switch {
	case abs >= 1e9:
		return strconv.FormatFloat(v/1e9, 'f', 2, 64) + "e9"
	case abs >= 1e6:
		return strconv.FormatFloat(v/1e6, 'f', 2, 64) + "e6"
	case abs >= 100:
		return strconv.FormatFloat(v, 'f', 1, 64)
	default:
		return strconv.FormatFloat(v, 'f', 3, 64)
	}
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
