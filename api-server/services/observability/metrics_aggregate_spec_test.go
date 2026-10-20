package observability

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAggregate(t *testing.T) {
	for _, tc := range []struct {
		in       string
		name     string
		quantile float64
	}{
		{"", "", 0},
		{"avg", "avg", 0},
		{"sum", "sum", 0},
		{"max", "max", 0},
		{"min", "min", 0},
		{"count", "count", 0},
		{"p92", "quantile", 0.92},
		{"p99", "quantile", 0.99},
		{"p1", "quantile", 0.01},
		{"p99.9", "quantile", 0.999},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseAggregate(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.name, got.Name)
			assert.InDelta(t, tc.quantile, got.Quantile, 1e-9)
		})
	}
}

// The vocabulary is closed and the door is where it closes: a builder that had to
// interpret an unknown value would either guess or drop the key silently, which is
// how four builders behave today with unknown keys.
func TestParseAggregate_RejectsAnythingElse(t *testing.T) {
	for _, in := range []string{"p0", "p100", "p", "px", "P92", "median", "quantile", "99", "p-5", "avg()"} {
		_, err := parseAggregate(in)
		require.Errorf(t, err, "%q must be refused", in)
		assert.Contains(t, err.Error(), in, "the error names what was sent")
	}
}

// A collapse is one point per series, over the window — not a per-step rollup, which
// is what the plain keys already return.
func TestPromQLOverTime(t *testing.T) {
	expr := `max by (container, pod) (rate(container_cpu_usage_seconds_total{namespace="shop"}[75s]))`

	none, err := parseAggregate("")
	require.NoError(t, err)
	assert.Equal(t, expr, promQLOverTime(none, expr, "14d", "75s"), "no aggregate leaves the key alone")

	p92, err := parseAggregate("p92")
	require.NoError(t, err)
	assert.Equal(t,
		`quantile_over_time(0.92, (`+expr+`)[14d:75s])`,
		promQLOverTime(p92, expr, "14d", "75s"))

	max, err := parseAggregate("max")
	require.NoError(t, err)
	assert.Equal(t, `max_over_time((`+expr+`)[14d:75s])`, promQLOverTime(max, expr, "14d", "75s"))

	count, err := parseAggregate("count")
	require.NoError(t, err)
	assert.Equal(t, `count_over_time((`+expr+`)[14d:75s])`, promQLOverTime(count, expr, "14d", "75s"))
}

// The expression is parenthesised because a key's query can be an `or` fallback, and
// a subquery otherwise binds to the right-hand arm alone — which would silently
// compute the percentile of the fallback rather than of the whole expression.
func TestPromQLOverTime_ParenthesisesAnOrExpression(t *testing.T) {
	p99, err := parseAggregate("p99")
	require.NoError(t, err)
	got := promQLOverTime(p99, `A{job="kubelet"} or A`, "14d", "75s")
	assert.Equal(t, `quantile_over_time(0.99, (A{job="kubelet"} or A)[14d:75s])`, got)
}
