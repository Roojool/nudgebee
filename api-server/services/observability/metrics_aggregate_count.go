package observability

import (
	"math"
	"time"
)

// `count` answers a coverage question, not a storage question.
//
// Its caller is a data floor: rightsizing refuses to size a container it has not
// watched for long enough, and `points_required` is written as a number of steps —
// 50 at the 75s step the loaders ask for, which is "about an hour of observed data".
// Prometheus answers that literally, because count_over_time((expr)[14d:75s]) counts
// the step-wide intervals the series had data in.
//
// No other provider stores data on the caller's step. Datadog picks a rollup from the
// window (measured on a live account: 2-hour buckets over 14 days), and Elasticsearch
// holds one document per collection period, usually 10s. Counting what each provider
// happens to return therefore made the same floor mean "four days" on Datadog and
// "eight minutes" on Elasticsearch, both from the same setting.
//
// So `count` is defined in the request's own unit — **how many Step-wide intervals of
// this series had data** — and each provider converts its readings into it. That is
// the same conversion the Python Datadog service arrived at independently (#38889);
// doing it here means one definition instead of one threshold per provider.
//
// The conversion is deliberately lossy in one direction only: a provider whose
// readings are COARSER than the step reports the time those readings cover, not the
// steps it could have resolved. A pod observed in eight 2-hour buckets covers 16
// hours, and that is the true answer to "how long have we watched it" even though
// Prometheus would have had 768 points for the same span.

// stepEquivalentCount converts readings into Step-wide intervals.
//
// Each reading stands for samplePeriod seconds of observation, so N readings cover
// N*samplePeriod seconds, which is N*samplePeriod/step intervals.
//
// With either duration unknown the readings are returned unchanged. That is the
// honest fallback rather than a guess: it is exactly the number the provider
// measured, and it is what a single reading with nothing to compare against means.
func stepEquivalentCount(readings float64, samplePeriodSeconds, stepSeconds float64) float64 {
	// NaN is checked explicitly because every comparison against it is false: a NaN
	// period would slip past `<= 0` and turn a real count into NaN, which marshals to
	// null and reads downstream as "no coverage" rather than as a bad conversion.
	if readings <= 0 || samplePeriodSeconds <= 0 || stepSeconds <= 0 ||
		math.IsNaN(readings) || math.IsNaN(samplePeriodSeconds) || math.IsNaN(stepSeconds) ||
		math.IsInf(samplePeriodSeconds, 0) || math.IsInf(stepSeconds, 0) {
		return readings
	}
	return readings * samplePeriodSeconds / stepSeconds
}

// aggregateStepSeconds is the caller's step, in seconds.
//
// Step is carried as a PromQL duration literal because that is what every other
// consumer of it needs. Returns 0 when it is absent or unparseable, which
// stepEquivalentCount reads as "do not convert".
func aggregateStepSeconds(meta RequestMetadata) float64 {
	if meta.Step == "" {
		return 0
	}
	d, err := time.ParseDuration(meta.Step)
	if err != nil || d <= 0 {
		return 0
	}
	return d.Seconds()
}
