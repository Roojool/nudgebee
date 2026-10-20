package observability

import (
	"fmt"
	"nudgebee/services/integrations"
	"nudgebee/services/security"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
)

const (
	// splunkEnterpriseSeriesBudget bounds the whole sweep. metrics_list_series runs
	// under observabilityMetadataActionTimeout (30s) and each catalog search may take
	// splunkEnterpriseMetricSearchTimeout (30s) on its own, so a sweep that just fires
	// every candidate can outlive the handler and return a bare failure. Stop short of
	// it and report what was found, flagged truncated.
	splunkEnterpriseSeriesBudget = 25 * time.Second
	// splunkEnterpriseSeriesMinSearch is the least time worth starting a search with.
	// Below this the request is almost certain to be killed mid-flight, so the budget
	// is better spent returning what is already in hand.
	splunkEnterpriseSeriesMinSearch = 2 * time.Second
)

// FetchMetricSeries implements the optional MetricSeriesSource capability for Splunk
// Enterprise: "which metric families have series for workload W in namespace N".
//
// mcatalog is the direct analogue of Prometheus's /label/__name__/values?match[]= — it
// answers the question server-side in one search per label convention. Verified against
// Splunk 10.x with an OTel-shaped metrics index:
//
//   - Dimension filters work in the WHERE clause, and dotted OTel dimension names
//     (k8s.namespace.name) need no quoting.
//   - A wildcard value (k8s.pod.name="checkout*") filters correctly, which is what the
//     pod-prefix convention needs.
//   - Filtering on a dimension the metric does not carry returns an empty result rather
//     than an error. That is why this needs none of the schema pruning the OpenObserve
//     source does, where an absent column fails the whole statement.
//
// The WHERE clause is rendered by buildSplunkMetricMatcher, the same helper that builds
// mstats filters, so discovery and the follow-up query cannot disagree about dimension
// spelling, value escaping or what "prefix" means. Each match reports the dimensions its
// WHERE clause actually filtered on (k8s.namespace.name, k8s.pod.name, …) rather than
// the canonical convention names: the agent writes its follow-up mstats from those
// labels, and no Splunk series carries a dimension called `pod` or `workload`.
func (s *SplunkEnterpriseMetricSource) FetchMetricSeries(ctx *security.RequestContext, req FetchMetricSeriesRequest) (MetricSeriesResult, error) {
	cfg, index, err := splunkEnterpriseMetricContext(ctx, req.AccountId)
	if err != nil {
		return MetricSeriesResult{}, err
	}
	if req.Workload == "" {
		return MetricSeriesResult{}, fmt.Errorf("splunk enterprise: workload is required for series-match")
	}

	limit := req.Limit
	if limit <= 0 {
		limit = seriesMatchDefaultLimit
	}
	if limit > seriesMatchMaxLimit {
		limit = seriesMatchMaxLimit
	}

	start, end := req.StartTime, req.EndTime
	if end <= 0 {
		end = time.Now().Unix()
	}
	if start <= 0 {
		start = end - int64(seriesMatchDefaultLookback.Seconds())
	}

	deadline := time.Now().Add(splunkEnterpriseSeriesBudget)

	candidates := splunkEnterpriseSeriesCandidates()
	families := make([][]string, len(candidates))
	truncated := make([]bool, len(candidates))
	errs := make([]error, len(candidates))
	probed := make([]bool, len(candidates))

	// Several canonical conventions collapse onto one Splunk dimension — `app` and
	// `workload` both map to k8s.deployment.name — and would otherwise cost a duplicate
	// search and report the same families twice. The rendered WHERE clause is the
	// natural identity for that: two candidates that filter identically produce the same
	// string, so the first one to claim it answers for both.
	claimed := map[string]bool{}

	eg := new(errgroup.Group)
	eg.SetLimit(seriesMatchMaxConcurrency)
	for i, candidate := range candidates {
		where, werr := splunkEnterpriseSeriesWhere(candidate, index, req.Namespace, req.Workload)
		if werr != nil {
			// An unsafe dimension or value is this candidate's problem alone; the other
			// conventions can still answer.
			ctx.GetLogger().Warn("splunk series-match: skipping candidate",
				"namespace_label", candidate.NamespaceLabel, "workload_label", candidate.WorkloadLabel,
				"account_id", req.AccountId, "error", werr)
			continue
		}
		if claimed[where] {
			continue
		}
		claimed[where] = true
		probed[i] = true

		eg.Go(func() error {
			found, stopped, ferr := s.splunkEnterpriseSeriesFamilies(ctx, cfg, index, where, start, end, deadline)
			if len(found) >= limit {
				found = found[:limit]
				stopped = true
			}
			families[i], truncated[i], errs[i] = found, stopped, ferr
			return nil
		})
	}
	_ = eg.Wait() // goroutines never return an error; per-candidate errors live in errs.

	probedCount, failures := 0, 0
	var firstErr error
	for i, candidate := range candidates {
		if !probed[i] {
			continue
		}
		probedCount++
		if errs[i] == nil {
			continue
		}
		failures++
		// This convention was never actually searched, so the result no longer covers
		// every convention. Flagging it truncated is what keeps an empty answer from
		// reading as "this workload emits no metrics" when one search merely failed.
		truncated[i] = true
		if firstErr == nil {
			firstErr = errs[i]
		}
		ctx.GetLogger().Warn("splunk series-match: candidate lookup failed",
			"namespace_label", candidate.NamespaceLabel, "workload_label", candidate.WorkloadLabel,
			"account_id", req.AccountId, "error", errs[i])
	}

	// Every probed candidate failing means Splunk is unreachable or rejecting searches,
	// not that the workload has no metrics — surfacing it beats a misleading empty.
	if probedCount > 0 && failures == probedCount {
		return MetricSeriesResult{}, firstErr
	}

	return assembleSeriesResult(candidates, families, truncated), nil
}

// splunkEnterpriseSeriesCandidates is the shared candidate table restated in the
// dimension names Splunk stores, with the conventions that have an OTel dimension first.
//
// It is not the shared table as-is for two reasons. The result reports the labels each
// family was found under, so they have to be the dimensions the WHERE clause used. And
// the sweep runs at bounded concurrency under one budget, while the shared table leads
// with eBPF and scrape conventions (destination_workload_name, job, statefulset, …) that
// an OTel-shaped index rarely carries — in table order, the conventions most likely to
// answer queue behind the ones least likely to. Those are kept rather than pruned,
// because Prometheus-receiver metrics forwarded into Splunk keep their raw label names;
// they are just searched last.
//
// The shared table is only read, never reordered in place: the Prometheus and
// OpenObserve sources iterate it too.
func splunkEnterpriseSeriesCandidates() []seriesMatchCandidate {
	mapped := make([]seriesMatchCandidate, 0, len(SeriesMatchCandidates))
	var raw []seriesMatchCandidate
	for _, c := range SeriesMatchCandidates {
		resolved := seriesMatchCandidate{
			NamespaceLabel: splunkEnterpriseMetricDimension(c.NamespaceLabel),
			WorkloadLabel:  splunkEnterpriseMetricDimension(c.WorkloadLabel),
			MatchType:      c.MatchType,
		}
		if _, ok := splunkEnterpriseMetricLabelMapping[c.WorkloadLabel]; ok {
			mapped = append(mapped, resolved)
		} else {
			raw = append(raw, resolved)
		}
	}
	return append(mapped, raw...)
}

// splunkEnterpriseMetricDimension is the Splunk dimension a canonical label is stored
// under, or the label itself when it has no OTel spelling.
func splunkEnterpriseMetricDimension(label string) string {
	if mapped, ok := splunkEnterpriseMetricLabelMapping[label]; ok {
		return mapped
	}
	return label
}

// splunkEnterpriseSeriesWhere renders the mcatalog WHERE clause for one candidate.
//
// It goes through buildSplunkMetricMatcher rather than formatting the comparison here,
// so the canonical->OTel dimension mapping, the value escaping and the prefix wildcard
// are the same ones the mstats query builder applies. A family discovered under a
// convention is therefore reachable by a query filtered on that convention.
func splunkEnterpriseSeriesWhere(c seriesMatchCandidate, index, namespace, workload string) (string, error) {
	parts := []string{fmt.Sprintf(`index="%s"`, index)}

	if namespace != "" {
		nsMatcher, err := buildSplunkMetricMatcher(LabelMatcher{
			Label: c.NamespaceLabel, Value: namespace, Operator: "_eq",
		})
		if err != nil {
			return "", err
		}
		parts = append(parts, nsMatcher)
	}

	// Pod names carry a replica-hash suffix, so the workload is a PREFIX of the pod.
	// _prefix is what appends the trailing wildcard.
	operator := "_eq"
	if c.MatchType == matchTypePrefix {
		operator = "_prefix"
	}
	wlMatcher, err := buildSplunkMetricMatcher(LabelMatcher{
		Label: c.WorkloadLabel, Value: workload, Operator: operator,
	})
	if err != nil {
		return "", err
	}
	parts = append(parts, wlMatcher)

	return strings.Join(parts, " AND "), nil
}

// splunkEnterpriseSeriesFamilies runs one candidate's catalog search and returns the
// metric families it matched, plus whether the shared budget cut it short.
func (s *SplunkEnterpriseMetricSource) splunkEnterpriseSeriesFamilies(
	ctx *security.RequestContext,
	cfg integrations.SplunkEnterpriseConfig,
	index, where string,
	start, end int64,
	deadline time.Time,
) ([]string, bool, error) {
	remaining := time.Until(deadline)
	if remaining < splunkEnterpriseSeriesMinSearch {
		return nil, true, nil
	}
	timeout := splunkEnterpriseMetricSearchTimeout
	if remaining < timeout {
		timeout = remaining
	}

	spl := fmt.Sprintf(`| mcatalog values(metric_name) WHERE %s`, where)
	if err := validateSplunkEnterpriseMetricQuery(spl, index); err != nil {
		return nil, false, err
	}

	startTime, endTime := splunkEnterpriseTimeRangeSeconds(start*1000, end*1000, time.Now())
	ctx.GetLogger().Debug("splunk enterprise series-match query", "spl", spl)

	rows, err := execSplunkEnterpriseSearch(cfg, spl, startTime, endTime,
		splunkEnterpriseCatalogLimit, timeout)
	if err != nil {
		return nil, false, err
	}

	return splunkEnterpriseCatalogValues(rows, "values(metric_name)"), false, nil
}
