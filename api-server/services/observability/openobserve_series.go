package observability

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"nudgebee/services/common"
	"nudgebee/services/integrations"
	"nudgebee/services/security"
	"sort"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
)

const (
	// openObserveSeriesTimeout bounds one candidate lookup. Each is a single grouped
	// read over a pruned set of metric streams, not a full series scan.
	openObserveSeriesTimeout = 20 * time.Second
	// openObserveSeriesMaxStreamsPerQuery caps how many streams are unioned into one
	// SQL statement. The statement is built by concatenation, so an org with a large
	// metric catalog would otherwise produce a query too long for the engine to parse.
	openObserveSeriesMaxStreamsPerQuery = 40
	// openObserveSeriesMaxStreams bounds the total streams probed per candidate, so a
	// catalog of thousands cannot turn one discovery call into an unbounded sweep.
	openObserveSeriesMaxStreams = 400
	// openObserveSeriesBudget bounds the whole sweep. The metrics_list_series action
	// gives the handler 30s (observabilityMetadataActionTimeout) and reports a bare
	// failure when it expires, so stop short of that and return what was found flagged
	// truncated — a partial catalogue is worth more to the agent than an error.
	openObserveSeriesBudget = 25 * time.Second
)

// FetchMetricSeries implements the optional MetricSeriesSource capability for
// OpenObserve: "which metric families have series for workload W in namespace N".
//
// It does NOT go through the Prometheus shim the way the Prometheus and CubeAPM
// sources do. Verified against OpenObserve v1.0.3:
//
//   - /label/__name__/values IGNORES the match[] selector (it returns the family for a
//     literal name match regardless of the labels alongside it, and an empty list for a
//     __name__ regex), so the label-values primitive those sources are built on cannot
//     answer this at all — it would report every family as belonging to every workload.
//   - /series DOES filter correctly, but only accepts a selector that starts with a
//     literal metric name, and rejects a second match[] with "duplicate field", so it
//     would cost one HTTP call per metric family.
//
// The native route answers it in one call per label convention instead: each metric is
// its own stream, so the stream schemas say which families could carry the candidate's
// labels, and a grouped read over just those says which actually do.
func (s *OpenObserveMetricSource) FetchMetricSeries(ctx *security.RequestContext, req FetchMetricSeriesRequest) (MetricSeriesResult, error) {
	cfg, err := integrations.GetOpenObserveConfigs(ctx, req.AccountId)
	if err != nil {
		return MetricSeriesResult{}, fmt.Errorf("failed to get OpenObserve configs: %w", err)
	}
	if req.Workload == "" {
		return MetricSeriesResult{}, fmt.Errorf("openobserve: workload is required for series-match")
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

	// One budget for the whole call, started before the schema fetch because that spends
	// from it too.
	deadline := time.Now().Add(openObserveSeriesBudget)

	// One call for the whole catalog: stream name -> its columns. Pruning against this
	// is what keeps the sweep bounded, and it is also required for correctness — a
	// stream that lacks the candidate's column fails the whole statement with
	// "unknown field", taking the streams that would have matched down with it.
	schemas, err := fetchOpenObserveMetricStreamSchemas(cfg)
	if err != nil {
		return MetricSeriesResult{}, err
	}

	// Probe every candidate concurrently, bounded the same way the Prometheus source
	// bounds its sweep. Sequentially these are up to len(streams)/chunk blocking searches
	// per candidate, which overruns the action deadline on a mid-sized catalogue long
	// before the per-request timeout ever trips. Each goroutine owns its own index, and
	// records its outcome rather than returning it, so one candidate failing never
	// cancels the siblings that did find families.
	families := make([][]string, len(SeriesMatchCandidates))
	truncated := make([]bool, len(SeriesMatchCandidates))
	errs := make([]error, len(SeriesMatchCandidates))
	probed := make([]bool, len(SeriesMatchCandidates))

	eg := new(errgroup.Group)
	eg.SetLimit(seriesMatchMaxConcurrency)
	for i, candidate := range SeriesMatchCandidates {
		streams := openObserveCandidateStreams(schemas, candidate, req.Namespace)
		if len(streams) == 0 {
			continue
		}
		probed[i] = true

		capped := false
		if len(streams) > openObserveSeriesMaxStreams {
			streams = streams[:openObserveSeriesMaxStreams]
			capped = true
		}

		eg.Go(func() error {
			found, stopped, ferr := openObserveSeriesFamilies(cfg, candidate, req.Namespace, req.Workload, streams, start, end, limit, deadline)
			if len(found) >= limit {
				found = found[:limit]
				stopped = true
			}
			families[i], truncated[i], errs[i] = found, capped || stopped, ferr
			return nil
		})
	}
	_ = eg.Wait() // goroutines never return an error; per-candidate errors live in errs.

	probedCount, failures := 0, 0
	var firstErr error
	for i, candidate := range SeriesMatchCandidates {
		if !probed[i] {
			continue
		}
		probedCount++
		if errs[i] == nil {
			continue
		}
		failures++
		if firstErr == nil {
			firstErr = errs[i]
		}
		ctx.GetLogger().Warn("openobserve series-match: candidate lookup failed",
			"namespace_label", candidate.NamespaceLabel, "workload_label", candidate.WorkloadLabel,
			"account_id", req.AccountId, "error", errs[i])
	}

	// Every probed candidate failing means the backend is unreachable, not that the
	// workload has no metrics — surfacing it beats returning a misleading empty.
	if probedCount > 0 && failures == probedCount {
		return MetricSeriesResult{}, firstErr
	}

	return assembleSeriesResult(SeriesMatchCandidates, families, truncated), nil
}

// openObserveCandidateStreams returns the metric streams whose schema carries the
// columns this candidate filters on, sorted for deterministic query construction.
func openObserveCandidateStreams(schemas map[string]map[string]struct{}, c seriesMatchCandidate, namespace string) []string {
	if !isSafeIdentifier(c.WorkloadLabel) {
		return nil
	}
	if namespace != "" && !isSafeIdentifier(c.NamespaceLabel) {
		return nil
	}

	out := make([]string, 0, len(schemas))
	for stream, cols := range schemas {
		if !integrations.IsSafeOpenObserveStreamName(stream) {
			continue
		}
		if _, ok := cols[c.WorkloadLabel]; !ok {
			continue
		}
		if namespace != "" {
			if _, ok := cols[c.NamespaceLabel]; !ok {
				continue
			}
		}
		out = append(out, stream)
	}
	sort.Strings(out)
	return out
}

// openObserveSeriesFamilies asks, for one candidate label convention, which of the
// pruned streams actually hold a row for this workload. Each UNION ALL branch is a
// grouped read that yields at most one row per stream.
//
// It reports whether it stopped early — because the limit was already met or the shared
// deadline ran out — so the caller can flag the result truncated rather than pass a
// partial catalogue off as the whole one.
func openObserveSeriesFamilies(cfg integrations.OpenObserveConfig, c seriesMatchCandidate, namespace, workload string, streams []string, start, end int64, limit int, deadline time.Time) ([]string, bool, error) {
	seen := map[string]struct{}{}
	out := []string{}
	stopped := false

	for chunk := 0; chunk < len(streams); chunk += openObserveSeriesMaxStreamsPerQuery {
		// Enough families already: the remaining chunks cannot change the answer the
		// caller asked for, so do not spend requests on them.
		if len(out) >= limit {
			stopped = true
			break
		}
		// Out of budget: returning the families found so far, flagged truncated, beats
		// letting the action deadline expire and handing the caller a bare failure.
		remaining := time.Until(deadline)
		if remaining <= 0 {
			stopped = true
			break
		}
		timeout := openObserveSearchTimeout
		if remaining < timeout {
			timeout = remaining
		}

		upper := chunk + openObserveSeriesMaxStreamsPerQuery
		if upper > len(streams) {
			upper = len(streams)
		}

		branches := make([]string, 0, upper-chunk)
		for _, stream := range streams[chunk:upper] {
			branches = append(branches, fmt.Sprintf(`SELECT __name__ FROM "%s" WHERE %s GROUP BY __name__`,
				stream, openObserveSeriesWhere(c, namespace, workload)))
		}

		names, err := openObserveMetricSearch(cfg, strings.Join(branches, " UNION ALL "), start*1000, end*1000, "__name__", timeout)
		if err != nil {
			return nil, false, err
		}
		for _, n := range names {
			if n == "" {
				continue
			}
			if _, dup := seen[n]; dup {
				continue
			}
			seen[n] = struct{}{}
			out = append(out, n)
		}
	}

	sort.Strings(out)
	return out, stopped, nil
}

// openObserveSeriesWhere renders the WHERE clause for one candidate convention.
func openObserveSeriesWhere(c seriesMatchCandidate, namespace, workload string) string {
	var where []string
	if namespace != "" {
		where = append(where, fmt.Sprintf("%s = '%s'", c.NamespaceLabel, escapeOpenObserveSQLLiteral(namespace)))
	}
	if c.MatchType == matchTypePrefix {
		// Pod names carry a replica-hash suffix, so the workload is a PREFIX of the pod.
		// ESCAPE is explicit because a workload may legitimately contain _ or %, which
		// LIKE would otherwise read as wildcards and silently widen the match.
		where = append(where, fmt.Sprintf(`%s LIKE '%s%%' ESCAPE '\'`, c.WorkloadLabel, escapeOpenObserveSQLLike(workload)))
	} else {
		where = append(where, fmt.Sprintf("%s = '%s'", c.WorkloadLabel, escapeOpenObserveSQLLiteral(workload)))
	}
	return strings.Join(where, " AND ")
}

// escapeOpenObserveSQLLiteral makes a value safe inside a single-quoted SQL literal.
func escapeOpenObserveSQLLiteral(v string) string {
	return strings.ReplaceAll(v, "'", "''")
}

// escapeOpenObserveSQLLike escapes a value used as a LIKE prefix, so wildcards in the
// workload name match literally rather than widening the filter.
func escapeOpenObserveSQLLike(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, "%", `\%`)
	v = strings.ReplaceAll(v, "_", `\_`)
	return escapeOpenObserveSQLLiteral(v)
}

// fetchOpenObserveMetricStreamSchemas lists every metric stream with its columns in a
// single call, so candidate pruning costs one request rather than one per family.
func fetchOpenObserveMetricStreamSchemas(cfg integrations.OpenObserveConfig) (map[string]map[string]struct{}, error) {
	endpoint := fmt.Sprintf("%s/api/%s/streams?type=metrics&fetchSchema=true", cfg.URL, neturl.PathEscape(cfg.OrgID))

	resp, err := common.HttpGet(endpoint,
		common.HttpWithHeaders(map[string]string{
			"Authorization": openObserveAuthHeader(cfg.Username, cfg.Password),
		}),
		common.HttpWithTimeout(openObserveSeriesTimeout),
	)
	if err != nil {
		return nil, fmt.Errorf("OpenObserve stream listing failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("OpenObserve stream listing returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload struct {
		List []struct {
			Name   string `json:"name"`
			Schema []struct {
				Name string `json:"name"`
			} `json:"schema"`
		} `json:"list"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("failed to decode OpenObserve stream listing: %w", err)
	}

	schemas := make(map[string]map[string]struct{}, len(payload.List))
	for _, stream := range payload.List {
		if stream.Name == "" {
			continue
		}
		cols := make(map[string]struct{}, len(stream.Schema))
		for _, f := range stream.Schema {
			cols[f.Name] = struct{}{}
		}
		schemas[stream.Name] = cols
	}
	return schemas, nil
}
