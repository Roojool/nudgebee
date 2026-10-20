package observability

import "sort"

// canonicaliseUtilisationLabels renames a provider's own identity labels to the
// canonical names, in place, on results from the UTILISATION path only.
//
// Three rules, each of which exists because ignoring it breaks something real:
//
//  1. Utilisation keys only. The free-form metrics_query path runs a query the USER
//     wrote, and five UI renderers build legends or table columns from whatever
//     labels come back; rewriting those would rewrite the user's own output. This
//     function is therefore called from FetchMetricUtilisation and its early-return
//     arms, never from FetchMetricsQuery.
//
//  2. Identity dimensions only. canonicalLabelExempt keeps destination_* out: those
//     name the callee of a request, not the object queried.
//
//  3. Never overwrite. If the canonical name is already present with a different
//     value, the provider's own value stays and nothing is written. A provider that
//     emits a real `workload` label (the eBPF paths do) must not have it replaced by
//     a tag that happens to map onto the same name.
//
// A provider absent from canonicalMetricLabels already speaks canonically, so this
// is a no-op for Prometheus, Elasticsearch, SolarWinds, CubeAPM and OpenObserve.
func canonicaliseUtilisationLabels(results []QueryResult, provider string) {
	mapping := canonicalMetricLabels[provider]
	if len(mapping) == 0 {
		return
	}
	for i := range results {
		for j := range results[i].Payload {
			canonicaliseSeriesLabels(results[i].Payload[j].Metric, provider, mapping)
		}
	}
}

func canonicaliseSeriesLabels(metric map[string]string, provider string, mapping map[string]string) {
	if len(metric) == 0 {
		return
	}
	// Collected first rather than written during the walk: renaming while ranging
	// over the same map is undefined, and a rename can otherwise land on a key the
	// loop has yet to visit.
	type rename struct{ from, to, value string }
	var pending []rename
	for label, value := range metric {
		canonical, mapped := mapping[label]
		if !mapped || canonicalLabelExempt(label) {
			continue
		}
		if existing, taken := metric[canonical]; taken && existing != value {
			// Rule 3: the provider already answered this question differently.
			continue
		}
		pending = append(pending, rename{from: label, to: canonical, value: value})
	}
	// Two labels can map to the same canonical name with different values — a
	// CronJob-owned pod on Datadog carries both `kube_cronjob` and `kube_job`. Map
	// iteration order is randomised, so applying them as they came would give the
	// same series a different `workload` on different requests. Highest-precedence
	// source wins; the others keep their own names, so the ambiguity stays visible
	// rather than being resolved silently.
	sort.SliceStable(pending, func(i, j int) bool {
		if pending[i].to != pending[j].to {
			return pending[i].to < pending[j].to
		}
		ri, rj := canonicalSourceRank(provider, pending[i].from), canonicalSourceRank(provider, pending[j].from)
		if ri != rj {
			return ri < rj
		}
		return pending[i].from < pending[j].from
	})

	written := make(map[string]bool, len(pending))
	for _, r := range pending {
		if written[r.to] {
			continue
		}
		delete(metric, r.from)
		metric[r.to] = r.value
		written[r.to] = true
	}
}
