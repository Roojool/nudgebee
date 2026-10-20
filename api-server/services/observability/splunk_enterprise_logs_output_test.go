package observability

import (
	"math"
	"nudgebee/services/integrations"
	"nudgebee/services/security"
	"testing"
	"time"

	"github.com/agiledragon/gomonkey/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetSplunkEnterpriseCanonicalLabels(t *testing.T) {
	out := OutputLog{
		Message:  "upstream returned 503",
		Severity: "ERROR",
		Labels: map[string]any{
			"_time":               "2026-09-25T18:57:59.754+00:00",
			"_raw":                `{"body": "upstream returned 503", "k8s.pod.name": "checkout-1"}`,
			"body":                "upstream returned 503",
			"severity_text":       "ERROR",
			"k8s.deployment.name": "checkout",
			"k8s.namespace.name":  "demo",
			"host":                "gke-node-1",
		},
	}
	eventTime, ok := parseSplunkEnterpriseTime("2026-09-25T18:57:59.754+00:00")
	require.True(t, ok)
	setSplunkEnterpriseCanonicalLabels(&out, eventTime, ok)

	// Epoch nanoseconds: the log table renders new Date(value / 1e6).
	want := time.Date(2026, 9, 25, 18, 57, 59, 754_000_000, time.UTC).UnixNano()
	assert.Equal(t, want, out.Labels["timestamp"])

	// Every alias of a shared provider field is present, so the shared normaliser has
	// nothing left to choose between.
	for canonical, want := range map[string]any{
		"body": "upstream returned 503", "message": "upstream returned 503",
		"severity": "ERROR", "level": "ERROR",
		"workload": "checkout", "app": "checkout",
		"namespace": "demo", "host": "gke-node-1", "hostname": "gke-node-1",
	} {
		assert.Equal(t, want, out.Labels[canonical], canonical)
	}
	assert.NotContains(t, out.Labels, "pod", "an alias is only added when the event carries its field")
}

// A real field of a canonical name is the event's own data and is never overwritten.
func TestSetSplunkEnterpriseCanonicalLabels_KeepsRealFields(t *testing.T) {
	out := OutputLog{Message: "the body", Labels: map[string]any{"message": "a parsed message attribute"}}
	setSplunkEnterpriseCanonicalLabels(&out, time.Time{}, false)
	assert.Equal(t, "a parsed message attribute", out.Labels["message"])
	assert.Equal(t, "the body", out.Labels["body"])
	assert.NotContains(t, out.Labels, "timestamp", "an unparseable time leaves no timestamp label")
}

func TestSplunkEnterpriseLogSource_BuildSPL_Paging(t *testing.T) {
	s := &SplunkEnterpriseLogSource{}

	spl, err := s.buildSPL(FetchLogRequest{Limit: 10, Offset: 20}, "otel_logs")
	require.NoError(t, err)
	assert.Equal(t, `search index="otel_logs" | head 30 | streamstats count AS nb_log_row | where nb_log_row>20 | fields - nb_log_row | fields *`, spl)

	_, err = s.buildSPL(FetchLogRequest{Limit: 100, Offset: splunkEnterpriseMaxLimit}, "otel_logs")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "offset + limit")
	_, err = s.buildSPL(FetchLogRequest{Limit: 10, Offset: math.MaxInt}, "otel_logs")
	require.Error(t, err, "an offset near MaxInt must not wrap past the ceiling check")

	// Newest-first is what Splunk returns; asking for it explicitly is fine.
	for _, sf := range []SortField{{ColumnName: "_time", Order: "desc"}, {ColumnName: "timestamp"}} {
		_, err = s.buildSPL(FetchLogRequest{SortFields: []SortField{sf}}, "otel_logs")
		assert.NoError(t, err, "%+v", sf)
	}
	// Anything else is refused rather than silently ignored.
	for _, sf := range []SortField{{ColumnName: "_time", Order: "asc"}, {ColumnName: "severity_text", Order: "desc"}} {
		_, err = s.buildSPL(FetchLogRequest{SortFields: []SortField{sf}}, "otel_logs")
		require.Error(t, err, "%+v", sf)
		assert.Contains(t, err.Error(), "not supported")
	}
}

func TestBuildSplunkEnterpriseLabelValuesSPL(t *testing.T) {
	spl, col, err := buildSplunkEnterpriseLabelValuesSPL("otel_logs", "k8s.pod.name", map[string]string{"namespace": "demo"})
	require.NoError(t, err)
	assert.Equal(t, "k8s.pod.name", col)
	assert.Equal(t, `search index="otel_logs" k8s.namespace.name="demo" k8s.pod.name=* | top limit=100 k8s.pod.name | fields k8s.pod.name`, spl)

	spl, col, err = buildSplunkEnterpriseLabelValuesSPL("otel_logs", "body", nil)
	require.NoError(t, err)
	assert.Equal(t, splunkEnterpriseLabelValueMessageCol, col)
	assert.Equal(t, `search index="otel_logs" | eval nb_lv_message=coalesce(body, message, log, _raw) | top limit=100 nb_lv_message | fields nb_lv_message`, spl)

	_, _, err = buildSplunkEnterpriseLabelValuesSPL("otel_logs", "k8s.pod.name", map[string]string{"a b": "x"})
	require.Error(t, err, "a current filter naming an unsafe field is rejected, not spliced in")
}

func TestExecSplunkEnterpriseSearch_RejectsInvertedWindow(t *testing.T) {
	_, err := execSplunkEnterpriseSearch(integrations.SplunkEnterpriseConfig{URL: "http://127.0.0.1:1"},
		`search index="x"`, "200.000", "100.000", 10, time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start_time must be before end_time")
}

func TestCollectSplunkEnterpriseLogLabels_KeepsSplunkSelectionFields(t *testing.T) {
	labels := collectSplunkEnterpriseLogLabels([]map[string]any{{
		"host": "n1", "source": "otel", "sourcetype": "kube:container", "index": "otel_logs",
		"punct": "x", "linecount": "1", "splunk_server": "s", "_bkt": "b", "_raw": "r",
		"k8s.pod.name": "p",
	}})
	names := make([]string, 0, len(labels))
	for _, l := range labels {
		names = append(names, l.Label)
	}
	assert.Equal(t, []string{"host", "index", "k8s.pod.name", "source", "sourcetype"}, names)
}

// The rendered SPL carries the page slice, so an offset only makes sense on it; a
// hand-written query plus an offset is refused rather than silently answered with page one.
func TestSplunkEnterpriseLogSource_OffsetNeedsRenderedSPL(t *testing.T) {
	s := &SplunkEnterpriseLogSource{}
	req := FetchLogRequest{Limit: 10, Offset: 10}
	generated, err := s.buildSPL(req, "otel_logs")
	require.NoError(t, err)
	assert.Contains(t, generated, "| head 20 | streamstats count AS nb_log_row | where nb_log_row>10")

	stub := gomonkey.ApplyFunc(integrations.GetSplunkEnterpriseConfig,
		func(*security.RequestContext, string) (integrations.SplunkEnterpriseConfig, error) {
			return integrations.SplunkEnterpriseConfig{URL: "http://127.0.0.1:1", LogIndex: "otel_logs"}, nil
		})
	defer stub.Reset()

	req.Query = `search index="otel_logs" error`
	_, err = s.queryLogRows(&security.RequestContext{}, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "offset is not supported with a hand-written query")
}

func TestSetSplunkEnterpriseCanonicalLabels_NilLabels(t *testing.T) {
	out := OutputLog{Message: "m"}
	setSplunkEnterpriseCanonicalLabels(&out, time.Unix(1, 0), true)
	assert.Equal(t, int64(1_000_000_000), out.Labels["timestamp"])
	assert.Equal(t, "m", out.Labels["body"])
}
