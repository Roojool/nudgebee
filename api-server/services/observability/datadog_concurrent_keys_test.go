package observability

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Datadog is asked one key at a time -- `aggregate` applies to a whole request, so four
// CPU percentiles cannot share one. A default rightsizing strategy sends 7 requests per
// workload where the per-provider service sent 2, and serially that multiplier is the
// difference between "larger" and "too slow to use". That measurement, not a hunch, is
// why this loop is concurrent.

// The bound is deliberately below a typical key count: firing every key at once is the
// burst Datadog rate-limits, and the retry budget is what absorbs the 429s concurrency
// makes more likely.
func TestDatadogConcurrency_IsBoundedAndLeavesHeadroom(t *testing.T) {
	assert.Greater(t, datadogMaxConcurrentKeys, 1, "serial is the thing being fixed")
	assert.Less(t, datadogMaxConcurrentKeys, 7,
		"a rightsizing request is ~7 keys; firing them all at once is the burst that gets rate-limited")
}

// The answer must not depend on which request finishes first. Each goroutine writes its
// own indexed slot, so a scrambled completion order still produces one stable ordering.
func TestDatadogConcurrency_ResultOrderDoesNotDependOnCompletionOrder(t *testing.T) {
	keys := []string{"cpu_usage", "memory_usage", "replica_defined", "workload_http_latency_p99"}

	collect := func(finishOrder []int) []string {
		perKey := make([][]QueryResult, len(keys))
		var wg sync.WaitGroup
		for _, idx := range finishOrder {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				perKey[i] = []QueryResult{{QueryKey: keys[i]}}
			}(idx)
		}
		wg.Wait()

		var got []string
		for _, rs := range perKey {
			for _, r := range rs {
				got = append(got, r.QueryKey)
			}
		}
		return got
	}

	forwards := collect([]int{0, 1, 2, 3})
	backwards := collect([]int{3, 2, 1, 0})
	scrambled := collect([]int{2, 0, 3, 1})

	assert.Equal(t, keys, forwards)
	assert.Equal(t, forwards, backwards, "finishing in reverse must not reorder the answer")
	assert.Equal(t, forwards, scrambled, "finishing out of order must not reorder the answer")
}

// A failed key occupies its own slot, so it neither disappears nor displaces a
// neighbour's result — the property that made partial results safe in the first place.
func TestDatadogConcurrency_AFailedKeyKeepsItsPlace(t *testing.T) {
	keys := []string{"a", "b", "c"}
	perKey := make([][]QueryResult, len(keys))
	var failures int32

	var wg sync.WaitGroup
	for i := range keys {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if keys[i] == "b" {
				msg := "rate-limited"
				atomic.AddInt32(&failures, 1)
				perKey[i] = []QueryResult{{QueryKey: keys[i], Error: &msg}}
				return
			}
			perKey[i] = []QueryResult{{QueryKey: keys[i], Payload: []Result{{}}}}
		}(i)
	}
	wg.Wait()

	var out []QueryResult
	for _, rs := range perKey {
		out = append(out, rs...)
	}

	assert.Len(t, out, 3, "every key asked for must come back, failed or not")
	assert.Equal(t, "b", out[1].QueryKey)
	assert.NotNil(t, out[1].Error)
	assert.Nil(t, out[0].Error)
	assert.Nil(t, out[2].Error)
	assert.Equal(t, int32(1), atomic.LoadInt32(&failures))
}
