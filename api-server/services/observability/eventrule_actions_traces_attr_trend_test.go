package observability

import (
	"fmt"
	"nudgebee/services/common"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func trendSpan(ts, span string, attrs map[string]string) common.OpenTelemetryTrace {
	return common.OpenTelemetryTrace{Timestamp: ts, ServiceName: "recommendation", SpanName: span, SpanAttributes: attrs}
}

// The get_product_list spans of event 07f70dc8 (OOMKilled, no error spans).
func oomEventSpans() []common.OpenTelemetryTrace {
	counts := []string{"403", "657", "1672", "2102", "31250", "48856", "364318", "364318", "1389902", "2171750", "6627756", "12944882"}
	spans := make([]common.OpenTelemetryTrace, 0, len(counts))
	for i, c := range counts {
		hit := "false"
		if i%2 == 1 {
			hit = "true"
		}
		spans = append(spans, trendSpan(fmt.Sprintf("2026-09-25 05:%02d:%02d", 8+i/4, (i%4)*15), "get_product_list", map[string]string{
			"demo.product.count":                     c,
			"demo.product.filtered.count":            "9",
			"demo.recommendation.cache_hit":          hit,
			"demo.feature_flag.recommendation_cache": "true",
		}))
	}
	return spans
}

func TestSpanAttributeTrendInsights_OOMGrowthAndConstantFlag(t *testing.T) {
	insights := spanAttributeTrendInsights(oomEventSpans())
	require.Len(t, insights, 1)
	msg := insights[0].Message
	assert.Equal(t, "High", insights[0].Severity, "the investigate page buckets only Critical/High/Info")
	assert.Contains(t, msg, "`demo.product.count`")
	assert.Contains(t, msg, "from 403 to 12944882 across 12 spans")
	assert.Contains(t, msg, "`demo.feature_flag.recommendation_cache=true`")
	assert.NotContains(t, msg, "cache_hit", "a boolean that varies is not a constant")
	assert.NotContains(t, msg, "filtered.count", "a flat numeric attribute is not a trend")
}

func TestSpanAttributeTrendInsights_IgnoresNonTrends(t *testing.T) {
	series := func(vals ...string) []common.OpenTelemetryTrace {
		out := make([]common.OpenTelemetryTrace, len(vals))
		for i, v := range vals {
			out[i] = trendSpan(fmt.Sprintf("2026-09-25 05:00:%02d", i), "op", map[string]string{"n": v})
		}
		return out
	}
	cases := map[string][]common.OpenTelemetryTrace{
		"too few samples":   series("1", "100", "10000", "1000000"),
		"a decrease":        series("10", "100", "1000", "500", "100000"),
		"growth below 10x":  series("10", "20", "40", "60", "90"),
		"non-numeric value": series("1", "10", "abc", "1000", "10000"),
		"starts at zero":    series("0", "10", "100", "1000", "10000"),
		"starts at NaN":     series("NaN", "10", "100", "1000", "10000"),
		"reaches Inf":       series("1", "10", "100", "1000", "Inf"),
	}
	for name, spans := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Empty(t, spanAttributeTrendInsights(spans))
		})
	}
}

func TestSpanAttributeTrendInsights_OrdersByTimestamp(t *testing.T) {
	spans := oomEventSpans()
	for i, j := 0, len(spans)-1; i < j; i, j = i+1, j-1 {
		spans[i], spans[j] = spans[j], spans[i]
	}
	assert.Len(t, spanAttributeTrendInsights(spans), 1, "spans arrive newest-first from most trace stores")
}

func TestSpanAttributeTrendInsights_SeparatesOperations(t *testing.T) {
	// A flat operation interleaved with a growing one would break the growing
	// series if both were read as one.
	var spans []common.OpenTelemetryTrace
	grow := 1
	for i := range 6 {
		ts := fmt.Sprintf("2026-09-25 05:00:%02d", i)
		spans = append(spans,
			trendSpan(ts, "flat", map[string]string{"n": "500"}),
			trendSpan(ts, "grow", map[string]string{"n": fmt.Sprint(grow)}))
		grow *= 10
	}
	insights := spanAttributeTrendInsights(spans)
	require.Len(t, insights, 1)
	assert.Contains(t, insights[0].Message, "`grow` spans")
}

func TestSpanAttributeTrendInsights_TimesAreThoseOfTheValues(t *testing.T) {
	// Spans without the attribute at either end must not stretch the window.
	spans := []common.OpenTelemetryTrace{trendSpan("2026-09-25 05:00:00", "op", map[string]string{"other": "x"})}
	for i, v := range []string{"100", "200", "300", "400", "1000"} {
		spans = append(spans, trendSpan(fmt.Sprintf("2026-09-25 05:01:0%d", i), "op", map[string]string{"n": v}))
	}
	spans = append(spans, trendSpan("2026-09-25 05:09:00", "op", map[string]string{"other": "x"}))
	insights := spanAttributeTrendInsights(spans)
	require.Len(t, insights, 1)
	assert.Contains(t, insights[0].Message, "(2026-09-25 05:01:00 -> 2026-09-25 05:01:04)")
}
