package observability

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

const esDay = 24 * 3600 * 1000

// A search is never built with more date_histogram buckets than the budget.
//
// Elasticsearch refuses a search that would build more than search.max_buckets, and
// takes 42s to 188s to say so — past the 30s HTTP client timeout, so api-server sees
// a timeout and never the refusal, while Elasticsearch keeps building buckets for a
// caller that has hung up. Detecting the refusal is impossible; not asking is the
// only thing left.
func TestESUtilStepSeconds_NeverExceedsTheBucketBudget(t *testing.T) {
	end := int64(1_700_000_000_000)

	for _, window := range []int64{1, 3, 7, 14, 30, 90} {
		start := end - window*esDay
		rangeSec := window * 24 * 3600

		// Explicit steps only. The derived form keeps its own long-standing ceiling.
		for _, explicit := range []string{"15s", "60s", "75s", "300s", "1800s"} {
			meta := RequestMetadata{Step: explicit}
			step := esUtilStepSeconds(meta, start, end)

			assert.Positivef(t, step, "window=%dd step=%q", window, explicit)
			buckets := rangeSec / step
			assert.LessOrEqualf(t, buckets, int64(esUtilHistogramBucketBudget),
				"window=%dd explicit=%q produced %d buckets", window, explicit, buckets)
		}
	}
}

// A request that already fits is returned untouched, so no existing query text moves.
func TestESUtilFitBuckets_LeavesAFittingStepAlone(t *testing.T) {
	// esUtilFitBuckets is only ever reached with an explicit step; the derived path
	// does not call it. These are the fitting cases, which it must return unchanged.
	for _, tc := range []struct{ step, rangeSec int64 }{
		{75, 600},   // volume rightsizing's current/capacity: 8 buckets
		{75, 86400}, // one day at 75s: 1,152 buckets, measured to answer in 7.5s
		{300, 14 * 86400},
		{1800, 90 * 86400},
	} {
		if tc.rangeSec/tc.step > esUtilHistogramBucketBudget {
			continue
		}
		assert.Equalf(t, tc.step, esUtilFitBuckets(tc.step, tc.rangeSec),
			"step=%d over %ds is %d buckets and must not move",
			tc.step, tc.rangeSec, tc.rangeSec/tc.step)
	}
}

// The step only ever grows. A finer step would be a different, denser query than the
// caller asked for, and would defeat the budget it is meant to satisfy.
func TestESUtilFitBuckets_OnlyWidens(t *testing.T) {
	for _, window := range []int64{600, 86400, 7 * 86400, 30 * 86400} {
		for _, step := range []int64{1, 15, 75, 300, 3600} {
			got := esUtilFitBuckets(step, window)
			assert.GreaterOrEqualf(t, got, step,
				"step=%d over %ds was narrowed to %d", step, window, got)
		}
	}
}

// The shapes this was found with, named so a regression is recognisable.
func TestESUtilStepSeconds_TheWindowsThatUsedToTimeOut(t *testing.T) {
	end := int64(1_700_000_000_000)
	for _, tc := range []struct {
		window   int64
		wasStuck bool
	}{
		{1, false}, // answered in 7.5s
		{3, true},  // refused after 42s, seen as a timeout
		{7, true},  // refused after 188s
		{14, true},
	} {
		step := esUtilStepSeconds(RequestMetadata{Step: "75s"}, end-tc.window*esDay, end)
		buckets := (tc.window * 24 * 3600) / step
		assert.LessOrEqualf(t, buckets, int64(esUtilHistogramBucketBudget),
			"%dd at a requested 75s", tc.window)
		if tc.wasStuck {
			assert.Greaterf(t, step, int64(75),
				"%dd at 75s asked for %d buckets and had to be widened",
				tc.window, (tc.window*24*3600)/75)
		}
		t.Logf("  %2dd requested 75s -> %4ds  (%d buckets)", tc.window, step, buckets)
	}
}
