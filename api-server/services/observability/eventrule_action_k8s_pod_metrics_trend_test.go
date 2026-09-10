package observability

import (
	"fmt"
	"nudgebee/services/eventrule/playbooks"
	"strings"
	"testing"
)

// memorySeries builds n samples one minute apart, each stepMiB larger than the
// last, in the wire shape the enricher produces: byte values as strings,
// timestamps as float seconds.
func memorySeries(container string, limitMiB float64, n int, startMiB, stepMiB float64) podMetricEntry {
	entry := podMetricEntry{
		Metric: map[string]any{
			"container": container,
			"limits":    map[string]any{"memory": limitMiB * bytesPerMiB, "cpu": 0},
		},
	}
	for i := 0; i < n; i++ {
		entry.Timestamps = append(entry.Timestamps, float64(1757505600+i*60))
		entry.Values = append(entry.Values, fmt.Sprintf("%.0f", (startMiB+float64(i)*stepMiB)*bytesPerMiB))
	}
	return entry
}

func appendSamples(entry podMetricEntry, valuesMiB ...float64) podMetricEntry {
	next := entry.Timestamps[len(entry.Timestamps)-1]
	for i, v := range valuesMiB {
		entry.Timestamps = append(entry.Timestamps, next+float64((i+1)*60))
		entry.Values = append(entry.Values, fmt.Sprintf("%.0f", v*bytesPerMiB))
	}
	return entry
}

func onlyInsight(t *testing.T, entry podMetricEntry) playbooks.PlaybookActionResponseInsight {
	t.Helper()
	insights := memoryTrendInsights([]podMetricEntry{entry}, "memory")
	if len(insights) != 1 {
		t.Fatalf("expected 1 insight; got %d", len(insights))
	}
	return insights[0]
}

// The distinction the OOMKilled event cannot make on its own: a working set
// that climbs and never falls back is retaining something, so a bigger limit
// only moves the deadline.
func TestMemoryTrend_UnboundedGrowth(t *testing.T) {
	got := onlyInsight(t, memorySeries("api", 192, 30, 30, 5))
	if !strings.Contains(got.Message, "without ever falling back") {
		t.Errorf("monotonic growth must be called out; got %q", got.Message)
	}
	if !strings.Contains(got.Message, "192MiB limit") {
		t.Errorf("the limit belongs in the summary; got %q", got.Message)
	}
	if !strings.Contains(got.Message, "before changing the limit") {
		t.Errorf("summary should push back on the reflexive limit bump; got %q", got.Message)
	}
	if !strings.Contains(got.Message, "Container api") {
		t.Errorf("the container should be named; got %q", got.Message)
	}
	if got.Severity != "High" {
		t.Errorf("severity = %q; unbounded retention changes the recommended action", got.Severity)
	}
}

func TestMemoryTrend_SawtoothIsNotUnbounded(t *testing.T) {
	// Rises, gives memory back, rises again — a workload that does release.
	entry := appendSamples(memorySeries("api", 192, 10, 40, 5), 45, 50, 55, 60, 65)
	got := onlyInsight(t, entry)
	if strings.Contains(got.Message, "without ever falling back") {
		t.Errorf("a series that falls back must not read as unbounded; got %q", got.Message)
	}
	if !strings.Contains(got.Message, "does release memory") {
		t.Errorf("expected the release reading; got %q", got.Message)
	}
	if got.Severity != "Info" {
		t.Errorf("severity = %q; a workload that releases memory is not the alarming case", got.Severity)
	}
}

// A container that OOMs and restarts drops to near zero. That is the kill, not
// the workload releasing memory under load — reading it as a sawtooth inverts
// the conclusion.
func TestMemoryTrend_RestartDropIsNotRelease(t *testing.T) {
	entry := appendSamples(memorySeries("api", 192, 20, 30, 8), 4, 12, 20, 28, 36)
	got := onlyInsight(t, entry)
	if strings.Contains(got.Message, "does release memory") {
		t.Errorf("an OOM restart must not read as releasing memory; got %q", got.Message)
	}
}

// A slow release — a GC handing memory back over several scrapes — never
// drops 10% between two adjacent samples. Comparing against the previous
// sample read this as unbroken growth, which is the one conclusion that
// must not be produced by accident: it is what says "no limit is large
// enough".
func TestMemoryTrend_GradualDeclineIsARelease(t *testing.T) {
	entry := memorySeries("api", 512, 12, 200, 0) // flat at the peak
	entry = appendSamples(entry, 195, 190, 185, 180, 175, 170, 165, 160)
	got := onlyInsight(t, entry)
	if strings.Contains(got.Message, "without ever falling back") {
		t.Errorf("a steady decline is a release, not unbounded growth; got %q", got.Message)
	}
	if !strings.Contains(got.Message, "does release memory") {
		t.Errorf("expected the release reading; got %q", got.Message)
	}
}

// The mirror case, and why the drop is not measured against the peak
// alone: after an OOM restart the container climbs back from near zero, so
// every sample sits below the old peak while the workload is doing the
// opposite of releasing.
func TestMemoryTrend_ClimbingBackAfterRestartIsNotARelease(t *testing.T) {
	entry := memorySeries("api", 192, 20, 30, 8) // climbs to ~182MiB
	entry = appendSamples(entry, 4, 12, 20, 28, 36, 44, 52, 60)
	got := onlyInsight(t, entry)
	if strings.Contains(got.Message, "does release memory") {
		t.Errorf("recovery after an OOM restart must not read as a release; got %q", got.Message)
	}
}

func TestMemoryTrend_TooFewSamplesSaysNothing(t *testing.T) {
	if insights := memoryTrendInsights([]podMetricEntry{memorySeries("api", 192, 2, 30, 5)}, "memory"); len(insights) != 0 {
		t.Errorf("two points cannot characterise a trend; got %v", insights)
	}
}

// The cpu variant of this action runs the same code path. Describing CPU cores
// as a working set would be wrong, and emitting from both variants would double
// every insight.
func TestMemoryTrend_OnlyForMemorySeries(t *testing.T) {
	for _, resourceType := range []string{"CPU", "cpu", ""} {
		if insights := memoryTrendInsights([]podMetricEntry{memorySeries("api", 192, 30, 30, 5)}, resourceType); len(insights) != 0 {
			t.Errorf("resource_type %q must not produce a memory trend; got %v", resourceType, insights)
		}
	}
}

// The OOMKilled pod whose container is already gone: the enricher seeds entries
// from requests/limits alone, with no samples at all.
func TestMemoryTrend_EmptySeriesIsSkipped(t *testing.T) {
	entry := podMetricEntry{Metric: map[string]any{"container": "api"}, Timestamps: []float64{}, Values: []string{}}
	if insights := memoryTrendInsights([]podMetricEntry{entry}, "memory"); len(insights) != 0 {
		t.Errorf("expected no insight for an empty series; got %v", insights)
	}
}

// Prometheus sends values as strings precisely so NaN and Inf survive JSON,
// and ParseFloat accepts both. Every comparison against NaN is false, so a
// single NaN sample would silently defeat the peak and fall-back detection
// this file is built on — and could be rendered to the reader as "NaN MiB".
func TestMemoryTrend_NaNAndInfAreDropped(t *testing.T) {
	entry := memorySeries("api", 192, 30, 30, 5)
	entry.Values[3] = "NaN"
	entry.Values[7] = "+Inf"
	got := onlyInsight(t, entry)
	if strings.Contains(got.Message, "NaN") || strings.Contains(got.Message, "Inf") {
		t.Errorf("NaN/Inf must never reach the summary; got %q", got.Message)
	}
	// The remaining samples still describe a curve that only grows.
	if !strings.Contains(got.Message, "without ever falling back") {
		t.Errorf("dropping NaN/Inf must not change the reading; got %q", got.Message)
	}
}

func TestMemoryTrend_UnparseableSamplesAreDroppedNotZeroed(t *testing.T) {
	entry := memorySeries("api", 192, 10, 100, 5)
	entry.Values[4] = "not-a-number"
	got := onlyInsight(t, entry)
	// A phantom 0 would read as the workload releasing everything.
	if strings.Contains(got.Message, "does release memory") {
		t.Errorf("a dropped sample must not read as a memory release; got %q", got.Message)
	}
}

func TestMemoryTrend_NoLimitOmitsTheComparison(t *testing.T) {
	entry := memorySeries("api", 0, 30, 30, 5)
	got := onlyInsight(t, entry)
	if strings.Contains(got.Message, "limit") && !strings.Contains(got.Message, "before changing the limit") {
		t.Errorf("no limit set — the summary must not claim one; got %q", got.Message)
	}
}

func TestMemoryTrend_OneInsightPerContainer(t *testing.T) {
	entries := []podMetricEntry{
		memorySeries("api", 192, 30, 30, 5),
		memorySeries("sidecar", 64, 30, 10, 1),
	}
	insights := memoryTrendInsights(entries, "memory")
	if len(insights) != 2 {
		t.Fatalf("expected one insight per container; got %d", len(insights))
	}
	if !strings.Contains(insights[0].Message, "api") || !strings.Contains(insights[1].Message, "sidecar") {
		t.Errorf("insights should name their containers; got %v", insights)
	}
}
