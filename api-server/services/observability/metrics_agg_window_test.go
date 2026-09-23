package observability

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The step and the rate window are one decision, not two.
//
// They were set together by promAggWindow and then only the step was overridden by
// an explicit request step, so a caller asking for 75s sampling still had its usage
// measured over 300s. On dev that returned a p99 CPU 26% below the query it replaces,
// which is the number rightsizing sizes a container on.
func TestResolveAggWindows_ExplicitStepMovesTheRateWindowWithIt(t *testing.T) {
	const day = 24 * 3600 * 1000
	end := int64(1_700_000_000_000)
	start := end - 14*day

	t.Run("no explicit step keeps the derived pair", func(t *testing.T) {
		wantRange, wantStep, wantRate := promAggWindow(start, end)
		gotRange, gotStep, gotRate := resolveAggWindows(start, end, 0)
		assert.Equal(t, wantRange, gotRange)
		assert.Equal(t, wantStep, gotStep)
		assert.Equal(t, wantRate, gotRate, "the derived pair must be left exactly as it was")
	})

	t.Run("an explicit step sets both", func(t *testing.T) {
		_, step, rate := resolveAggWindows(start, end, 75)
		assert.Equal(t, "75s", step)
		assert.Equal(t, "75s", rate,
			"a usage figure is the rise between two readings divided by the gap; "+
				"honouring the step but not the gap answers a different question")
	})

	// The floor exists to stop a DERIVED step asking for a rate window narrower than
	// a scrape interval. An explicit step is the caller saying how finely it samples,
	// so it is not floored -- pinned here so the floor is not "helpfully" restored.
	t.Run("an explicit step is not floored", func(t *testing.T) {
		for _, step := range []int{15, 30, 75, 120} {
			_, gotStep, gotRate := resolveAggWindows(start, end, step)
			assert.Equal(t, gotStep, gotRate, "step %ds", step)
		}
	})

	t.Run("the range window never follows the step", func(t *testing.T) {
		gotRange, _, _ := resolveAggWindows(start, end, 75)
		assert.Equal(t, "1209600s", gotRange, "14 days, whatever the sampling interval")
	})
}
