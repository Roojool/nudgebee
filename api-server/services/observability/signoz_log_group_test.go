package observability

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// signozGroupSeries is one log-group series as SigNoz returns it, labelled with
// whatever keys the query grouped by.
func signozGroupSeries(nsKey, podKey string) map[string]any {
	return map[string]any{
		"labels": map[string]any{
			"severity_text": "ERROR",
			nsKey:           "payments",
			podKey:          "checkout-7d4b9c6f8-xk2p9",
		},
		"points": []any{map[string]any{"timestamp": float64(1700000000000), "value": float64(4)}},
	}
}

func signozGroupResult(series map[string]any) map[string]any {
	return map[string]any{"result": []any{map[string]any{"queryName": "A", "series": []any{series}}}}
}

// Current SigNoz labels series with the dotted OTel keys we now group by; a
// legacy response labelled with the underscored keys must parse identically.
func TestSignozLogGroupParse_DottedAndLegacyKeysAgree(t *testing.T) {
	agent := &SignozSource{}
	for name, keys := range map[string][2]string{
		"current (dotted)":     {signozNamespaceKey, signozPodKey},
		"legacy (underscored)": {signozLegacyNamespaceKey, signozLegacyPodKey},
	} {
		t.Run(name, func(t *testing.T) {
			series := signozGroupSeries(keys[0], keys[1])

			saas, err := parseSignozSaasLogGroupResponse(map[string]any{"data": signozGroupResult(series)}, 1700000000000)
			require.NoError(t, err)
			require.Len(t, saas.Groups, 1)

			relayed, err := agent.parseSignozLogGroupResponse(map[string]any{"data": map[string]any{"data": signozGroupResult(series)}}, 1700000000000)
			require.NoError(t, err)
			require.Len(t, relayed.Groups, 1)

			for _, g := range []LogGroup{saas.Groups[0], relayed.Groups[0]} {
				assert.Equal(t, "payments", g.Namespace)
				assert.Equal(t, "ERROR", g.Level)
				assert.NotEmpty(t, g.Workload)
				assert.Equal(t, "/k8s/payments/"+g.Workload, g.ContainerID)
			}
		})
	}
}

// Filtering by the underscored key matches no row on current SigNoz, which
// empties the whole log-group result — both builders must use the dotted keys.
func TestSignozLogGroupFilters_UseDottedKeys(t *testing.T) {
	req := FetchLogGroupRequest{Request: map[string]any{"selectedNamespace": "payments", "selectedWorkload": "checkout"}}

	for name, filters := range map[string][]map[string]any{
		"user source":  buildSignozSaasLogGroupFilters(req),
		"agent source": (&SignozSource{}).buildLogGroupFilters(req),
	} {
		t.Run(name, func(t *testing.T) {
			keys := map[string]any{}
			for _, f := range filters {
				if k, ok := f["key"].(map[string]string); ok {
					keys[k["key"]] = f["value"]
				}
			}
			assert.Equal(t, "payments", keys[signozNamespaceKey])
			assert.Equal(t, "checkout", keys[signozPodKey])
			assert.NotContains(t, keys, signozLegacyNamespaceKey)
			assert.NotContains(t, keys, signozLegacyPodKey)
		})
	}
}

// The series shape SigNoz v0.145 returned for the log-group query (captured live):
// points under `values`, each value a numeric STRING. Reading `points` as floats
// gave every group a count of 0, which the Log Groups page then hid.
func TestSignozSeriesPoints_ValuesWithStringNumbers(t *testing.T) {
	series := map[string]any{
		"labels": map[string]any{"k8s.pod.name": "gateway-6c7d8e9f0-zz9yy"},
		"values": []any{
			map[string]any{"timestamp": float64(1791453600000), "value": "1"},
			map[string]any{"timestamp": float64(1791457200000), "value": "2"},
		},
	}
	// Hourly bucket starts 10:00 and 11:00 (ms) for a query ending 11:13:20.
	ts, vals := signozSeriesPoints(series, 1791458000000)
	assert.Equal(t, []int64{1791457200, 1791458000}, ts,
		"Unix seconds, at each bucket's END (11:00), and the last one capped at the query end")
	assert.Equal(t, []float64{1, 2}, vals)

	// Numeric values and the legacy `points` key are read the same way; with no end
	// time the bucket end is not capped.
	ts, vals = signozSeriesPoints(map[string]any{"points": []any{map[string]any{"timestamp": float64(1791453600000), "value": float64(3)}}}, 0)
	assert.Equal(t, []int64{1791457200}, ts)
	assert.Equal(t, []float64{3}, vals)

	// A non-numeric value is skipped rather than counted as 0.
	_, vals = signozSeriesPoints(map[string]any{"values": []any{map[string]any{"timestamp": float64(1791453600000), "value": "n/a"}}}, 0)
	assert.Empty(t, vals)
}

func TestSignozLogGroupParse_CountsFromValuesShape(t *testing.T) {
	series := map[string]any{
		"labels": map[string]any{"severity_text": "ERROR", signozNamespaceKey: "payments", signozPodKey: "checkout-7d4b9c6f8-xk2p9"},
		"values": []any{
			map[string]any{"timestamp": float64(1791453600000), "value": "1"},
			map[string]any{"timestamp": float64(1791457200000), "value": "1"},
		},
	}
	saas, err := parseSignozSaasLogGroupResponse(map[string]any{"data": signozGroupResult(series)}, 1791458000000)
	require.NoError(t, err)
	relayed, err := (&SignozSource{}).parseSignozLogGroupResponse(map[string]any{"data": map[string]any{"data": signozGroupResult(series)}}, 1791458000000)
	require.NoError(t, err)

	for _, out := range []LogGroupOutput{saas, relayed} {
		require.Len(t, out.Groups, 1)
		assert.Equal(t, int64(2), out.Groups[0].Count)
		assert.Equal(t, []float64{1, 1}, out.Groups[0].Values)
		assert.Equal(t, []int64{1791457200, 1791458000}, out.Groups[0].Timestamps, "Unix seconds at bucket ends, last capped at the query end")
	}
}

// A canonical label is looked up under its SigNoz attribute as a resource attribute
// (the caller's tag type described the canonical label); a native key passes as-is.
func TestSignozLabelValueParams(t *testing.T) {
	key, tagType := signozLabelValueParams("service", "tag")
	assert.Equal(t, "service.name", key)
	assert.Equal(t, "resource", tagType)

	key, tagType = signozLabelValueParams("level", "tag")
	assert.Equal(t, "severity_text", key)
	assert.Equal(t, "resource", tagType)

	key, tagType = signozLabelValueParams("http.method", "tag")
	assert.Equal(t, "http.method", key)
	assert.Equal(t, "tag", tagType)
}

// Points missing a timestamp or value, or carrying NaN/Inf, are skipped: a 0
// timestamp would plot at 1970 and int64(NaN) is a garbage count.
func TestSignozSeriesPoints_SkipsUnusablePoints(t *testing.T) {
	ts, vals := signozSeriesPoints(map[string]any{"values": []any{
		map[string]any{"value": "3"},
		map[string]any{"timestamp": float64(1791446400000), "value": "NaN"},
		map[string]any{"timestamp": float64(1791450000000), "value": "+Inf"},
		map[string]any{"timestamp": float64(1791453600000), "value": "2"},
	}}, 0)
	assert.Equal(t, []int64{1791457200}, ts)
	assert.Equal(t, []float64{2}, vals)
}

// Several canonical names read the same SigNoz key (app and workload both read
// k8s.deployment.name). Every one of them must be aliased on every log — the old
// single-valued reverse map picked one at random per request.
func TestNormalizeOutputLogLabels_AliasesEveryCanonicalSharingAKey(t *testing.T) {
	for i := 0; i < 20; i++ {
		logs := []OutputLog{{Labels: map[string]any{"k8s.deployment.name": "checkout", "host.name": "n1", "severity_text": "ERROR"}}}
		normalizeOutputLogLabels(logs, signozLogLabelMapping)
		for _, canonical := range []string{"app", "workload"} {
			assert.Equal(t, "checkout", logs[0].Labels[canonical], "canonical %q", canonical)
		}
		assert.Equal(t, "n1", logs[0].Labels["host"])
		assert.Equal(t, "n1", logs[0].Labels["hostname"])
		assert.Equal(t, "ERROR", logs[0].Labels["level"])
		assert.Equal(t, "ERROR", logs[0].Labels["severity"])
	}
}

// The Log Groups page multiplies every timestamp by 1000, so FetchLogGroup enforces
// Unix seconds for every provider: whatever precision a parser emits, the page gets
// seconds — a slipped unit no longer reaches the screen as 1970 (or year 58000).
func TestLogGroupTimestampSeconds(t *testing.T) {
	const sec = int64(1791457200)
	for name, in := range map[string]int64{
		"seconds":      sec,
		"milliseconds": sec * 1e3,
		"microseconds": sec * 1e6,
		"nanoseconds":  sec * 1e9,
	} {
		assert.Equal(t, sec, logGroupTimestampSeconds(in), name)
	}

	out := LogGroupOutput{Groups: []LogGroup{{Timestamps: []int64{sec * 1e3, sec}}, {Timestamps: []int64{sec * 1e9}}}}
	normalizeLogGroupTimestamps(&out)
	assert.Equal(t, []int64{sec, sec}, out.Groups[0].Timestamps)
	assert.Equal(t, []int64{sec}, out.Groups[1].Timestamps)
}
