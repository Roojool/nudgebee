package observability

import (
	"fmt"
	"math"
	"nudgebee/services/eventrule/playbooks"
	"strconv"
	"strings"
	"time"
)

// The OOMKilled event says a threshold was crossed. It cannot say whether the
// limit is too small or the workload is retaining memory without bound, and
// that distinction is the whole difference between "raise the limit" and "no
// limit is large enough". It lives entirely in the shape of the curve.
//
// The memory series is already fetched for these events — k8s_pod_memory_metric_enricher
// auto-executes on any pod-subject finding, which every OOMKilled finding is —
// and it is already rendered as a chart. What was missing is the one sentence
// that reads it, which is what a reader (or a model) acts on; nobody eyeballs
// thirty points and concludes "unbounded retention".
const (
	// memoryTrendMinSamples is the fewest points worth characterising. Below
	// this, "grew" is noise dressed as a finding.
	memoryTrendMinSamples = 5

	// memoryTrendDropRatio: a sample this far below the running peak counts
	// as memory being given back. Measured against the peak, not the previous
	// sample: a slow release (a GC returning memory to the OS over several
	// scrapes) never drops 10% between two adjacent samples, so a
	// predecessor comparison reads a steady decline as unbroken growth —
	// the one conclusion that must not be produced by accident, since it is
	// what says "no limit is large enough".
	memoryTrendDropRatio = 0.9

	// memoryTrendRestartFloor: a fall below this fraction of the peak is a
	// container restart resetting the working set — that is the OOMKill
	// itself, not the workload releasing memory under load. Reading it as a
	// release inverts the conclusion.
	memoryTrendRestartFloor = 0.1

	bytesPerMiB = 1024 * 1024
)

// memoryTrendInsights characterises each container's memory working set.
//
// Deliberately mechanical — first/last/peak and whether the series ever fell
// back — rather than a verdict. It reports what the numbers do; concluding
// "a bigger limit will not help" is the reader's job, and one they can only do
// with the shape stated.
func memoryTrendInsights(entries []podMetricEntry, resourceType string) []playbooks.PlaybookActionResponseInsight {
	// Gate on the concrete resource type the query used, not the caller's
	// (possibly empty) original: the cpu variant of this action runs over the
	// same code path, and describing CPU cores as a working set would be wrong.
	if !strings.EqualFold(resourceType, "memory") {
		return nil
	}
	var insights []playbooks.PlaybookActionResponseInsight
	for _, entry := range entries {
		if insight, ok := memoryTrendInsight(entry); ok {
			insights = append(insights, insight)
		}
	}
	return insights
}

func memoryTrendInsight(entry podMetricEntry) (playbooks.PlaybookActionResponseInsight, bool) {
	samples := parseSampleMiB(entry.Values)
	if len(samples) < memoryTrendMinSamples {
		return playbooks.PlaybookActionResponseInsight{}, false
	}

	first, last := samples[0], samples[len(samples)-1]
	peak, dropped := first, false
	prev := first
	for _, mb := range samples[1:] {
		if mb > peak {
			peak = mb
		}
		// Three conditions, each ruling out a different false reading:
		//   mb < prev             — still falling. Without it, a container
		//                           climbing back after an OOM restart sits
		//                           below the old peak and reads as a release.
		//   mb < peak*ratio       — meaningfully below the high-water mark,
		//                           not scrape jitter.
		//   mb > peak*floor       — not the restart itself, which drops the
		//                           working set to near zero.
		if mb < prev && mb < peak*memoryTrendDropRatio && mb > peak*memoryTrendRestartFloor {
			dropped = true
		}
		prev = mb
	}

	// The series is a sum: every provider's utilisation query wraps in sum()
	// with no `by (pod)`, so a multi-replica workload — or a single one caught
	// mid-restart with two incarnations alive — returns the total across pods,
	// and the limits come back summed to match. Say so, rather than implying
	// one container's curve.
	subject := "Working set"
	if container, ok := entry.Metric["container"].(string); ok && container != "" {
		subject = fmt.Sprintf("Container %s: working set across the workload's pods", container)
	}
	summary := fmt.Sprintf("%s went %.0fMiB → %.0fMiB (peak %.0fMiB)", subject, first, last, peak)
	if span := seriesSpan(entry.Timestamps); span > 0 {
		summary += fmt.Sprintf(" over %s", span.Round(time.Minute))
	}
	if limitMiB := limitMiB(entry.Metric); limitMiB > 0 {
		// Summed to match the series, so it is the workload's combined ceiling,
		// not the per-container limit an operator would recognise from the spec.
		summary += fmt.Sprintf(", against a combined %.0fMiB limit", limitMiB)
	}

	switch {
	case !dropped && last > first:
		return playbooks.PlaybookActionResponseInsight{
			Message: summary + ". It grew without ever falling back, so the working set is being " +
				"retained rather than churned — find what sets that retention before changing the limit.",
			Severity: "High",
		}, true
	case dropped:
		// Deliberately not "the workload releases memory". On a total summed
		// across pods a fall can simply be a pod leaving — restarted, evicted,
		// scaled in — which says nothing about any container giving memory
		// back. Reading it as a release inverts the conclusion on precisely the
		// case this insight exists for: a container that grew until it was
		// OOMKilled drops out of the sum at the moment it dies.
		return playbooks.PlaybookActionResponseInsight{
			Message: summary + ". The total fell back within the window — either a container released " +
				"memory or a pod left the set, which this series cannot tell apart.",
			Severity: "Info",
		}, true
	default:
		return playbooks.PlaybookActionResponseInsight{Message: summary + ".", Severity: "Info"}, true
	}
}

// parseSampleMiB converts Prometheus sample values (bytes, sent as strings so
// NaN/Inf survive JSON) to MiB. Unparseable samples are dropped rather than
// treated as zero: a phantom 0 in a memory series reads as "the workload
// released everything", which is a lie.
//
// NaN and Inf are dropped for a sharper reason: ParseFloat accepts both, and
// every comparison against NaN is false. One NaN sample would silently defeat
// the peak and fall-back detection this whole file is built on, and could be
// rendered to the reader as "NaN MiB".
func parseSampleMiB(values []string) []float64 {
	out := make([]float64, 0, len(values))
	for _, v := range values {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			continue
		}
		out = append(out, f/bytesPerMiB)
	}
	return out
}

func seriesSpan(timestamps []float64) time.Duration {
	if len(timestamps) < 2 {
		return 0
	}
	seconds := timestamps[len(timestamps)-1] - timestamps[0]
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// limitMiB reads the container memory limit the enricher already attached to
// the series. Returns 0 when unset — getResourceValues seeds the map with a
// literal 0, so absence and "no limit" look the same here, and both mean the
// summary simply omits the comparison.
func limitMiB(metric map[string]any) float64 {
	limits, _ := metric["limits"].(map[string]any)
	if limits == nil {
		return 0
	}
	switch v := limits["memory"].(type) {
	case float64:
		return v / bytesPerMiB
	case int:
		return float64(v) / bytesPerMiB
	default:
		return 0
	}
}
