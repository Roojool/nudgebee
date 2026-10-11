package api

import (
	"cmp"
	"slices"
	"time"

	"nudgebee/services/observability"
	"nudgebee/services/security"
	"nudgebee/services/triage"
)

// promQLLabelProbeWindow is how far back the probe looks for a matching series.
// The probe is an instant query and only needs the label NAMES of whatever the
// expression currently matches, so the window exists to tolerate a slightly
// stale scrape, not to cover a real time range.
const promQLLabelProbeWindow = 5 * time.Minute

// promQLLabelNames answers eventrule.PromQLLabelNames from the account's own
// metrics provider.
//
// It lives here rather than in eventrule because resolving that provider is
// observability's job, and eventrule cannot import observability (observability
// imports eventrule). Passing the capability down from this composition root
// keeps the routing decision in one place instead of giving eventrule a second
// one of its own.
//
// Before this, eventrule asked relay-server directly, which reaches only the
// cluster agent's Prometheus -- so on an account whose Prometheus is connected
// directly the probe always failed and the alert action list silently collapsed
// to nudgebee-source actions only.
func promQLLabelNames(sc *security.RequestContext, accountID, query string) ([]string, error) {
	end := time.Now().UTC()
	output, err := observability.FetchMetricsQuery(sc, observability.FetchMetricsRequest{
		AccountId:      accountID,
		MetricProvider: "prometheus",
		Queries:        map[string]string{"probe": query},
		StartTime:      end.Add(-promQLLabelProbeWindow).UnixMilli(),
		EndTime:        end.UnixMilli(),
		Instant:        true,
	})
	if err != nil {
		return nil, err
	}
	for _, result := range output.Results {
		if result.QueryKey == "probe" && result.Error != nil {
			return nil, errProbeFailed(*result.Error)
		}
	}

	// Non-nil even when the expression matched nothing: the caller reads a nil
	// error as "the backend answered", and a query that legitimately matches no
	// series is an answer.
	//
	// Deduplicated because the caller only asks which label NAMES exist. The
	// topk(1, ...) form returns one series, but the bare-query fallback can match
	// thousands, and every one of them carries the same handful of names.
	names := []string{}
	seen := map[string]bool{}
	for _, labelSet := range observability.PromQLLabels(output, "probe") {
		for name := range labelSet {
			if seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	return names, nil
}

type errProbeFailed string

func (e errProbeFailed) Error() string { return "prometheus label probe: " + string(e) }

// promQLRangeSeries answers triage.PromQLRangeSeries from the account's own
// metrics provider, for the same reason promQLLabelNames exists: triage cannot
// import observability (observability reaches triage through event), so the
// capability is handed down from here.
//
// Timestamps stay in unix seconds -- what the Prometheus sources emit, and what
// the threshold tuner's step arithmetic assumes.
func promQLRangeSeries(sc *security.RequestContext, accountID, query string, start, end time.Time) ([]*triage.MetricHistory, error) {
	output, err := observability.FetchMetricsQuery(sc, observability.FetchMetricsRequest{
		AccountId:      accountID,
		MetricProvider: "prometheus",
		Queries:        map[string]string{promQLRangeSeriesKey: query},
		StartTime:      start.UnixMilli(),
		EndTime:        end.UnixMilli(),
	})
	if err != nil {
		return nil, err
	}

	series := []*triage.MetricHistory{}
	for _, result := range output.Results {
		if result.QueryKey != promQLRangeSeriesKey {
			continue
		}
		if result.Error != nil {
			return nil, errProbeFailed(*result.Error)
		}
		for _, payload := range result.Payload {
			timestamps, values := promQLSortedSamples(payload.Timestamps, payload.Values)
			if len(timestamps) == 0 {
				continue
			}
			series = append(series, &triage.MetricHistory{
				Timestamps: timestamps,
				Values:     values,
				Labels:     promQLSeriesLabels(payload.Metric),
				Step:       promQLSeriesStepSeconds(timestamps),
			})
		}
	}
	return series, nil
}

const promQLRangeSeriesKey = "threshold_query"

// promQLSeriesLabels drops the __name__ meta-label, which is not a real
// dimension and would otherwise never match an event's label set.
func promQLSeriesLabels(metric map[string]string) map[string]string {
	labels := make(map[string]string, len(metric))
	for name, value := range metric {
		if name == "__name__" {
			continue
		}
		labels[name] = value
	}
	return labels
}

// promQLSortedSamples returns the (timestamp, value) pairs in timestamp order.
//
// The agent-relayed source hands back whatever order the relay produced, which is
// not guaranteed to be ascending, and the threshold tuner reads the step off the
// first two timestamps and slices the series into baseline and firing windows. An
// unsorted series therefore yields a wrong step and mis-sized windows -- a
// silently wrong suggestion rather than an error. The relay parser this replaced
// sorted for exactly this reason.
//
// Pairs beyond the shorter of the two slices are dropped: they carry no value (or
// no timestamp) and would only mis-align the rest.
func promQLSortedSamples(timestamps []int64, values []float64) ([]int64, []float64) {
	n := min(len(timestamps), len(values))
	if n == 0 {
		return nil, nil
	}

	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	if !slices.IsSortedFunc(order, func(a, b int) int { return cmp.Compare(timestamps[a], timestamps[b]) }) {
		slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(timestamps[a], timestamps[b]) })
	}

	sortedTimestamps := make([]int64, n)
	sortedValues := make([]float64, n)
	for i, j := range order {
		sortedTimestamps[i] = timestamps[j]
		sortedValues[i] = values[j]
	}
	return sortedTimestamps, sortedValues
}

// promQLSeriesStepSeconds infers the scrape step from the first two samples,
// defaulting to 60s for a single-sample series — the same default the previous
// relay-response parser applied.
func promQLSeriesStepSeconds(timestamps []int64) int {
	if len(timestamps) > 1 {
		if step := int(timestamps[1] - timestamps[0]); step >= 1 {
			return step
		}
	}
	return 60
}
