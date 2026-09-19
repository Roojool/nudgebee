package changedetect

import (
	"strings"
	"testing"
	"time"
)

func ip(i int) *int { return &i }

func tp(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return &t
}

func TestDecideCloud(t *testing.T) {
	prev := &Marks{CloudActiveCount: ip(10), CloudCreatedMark: tp("2026-09-17T10:00:00Z")}
	cases := []struct {
		name  string
		prev  *Marks
		probe CloudProbe
		want  CloudDecision
	}{
		{"first sighting publishes nothing", nil, CloudProbe{ActiveCount: 10}, CloudDecision{First: true}},
		{"no new rows", prev, CloudProbe{ActiveCount: 9, CreatedMax: tp("2026-09-17T10:00:00Z")}, CloudDecision{}},
		{"new rows are read", prev, CloudProbe{ActiveCount: 11, CreatedMax: tp("2026-09-17T10:05:00Z")},
			CloudDecision{CheckCreatedAfter: tp("2026-09-17T10:00:00Z")}},
		{"first rows of an empty account", &Marks{CloudActiveCount: ip(0)}, CloudProbe{ActiveCount: 1, CreatedMax: tp("2026-09-17T10:00:00Z")},
			CloudDecision{CheckCreatedAfter: &time.Time{}}},
	}
	for _, tc := range cases {
		got := DecideCloud(tc.prev, tc.probe)
		if got.First != tc.want.First || !sameTime(got.CheckCreatedAfter, tc.want.CheckCreatedAfter) {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestDecideCountChange(t *testing.T) {
	cases := []struct {
		name               string
		prev, cur, newRows int
		want               CountChange
	}{
		{"unchanged", 10, 10, 0, CountExplained},
		{"only creations", 10, 12, 2, CountExplained},
		{"a deletion", 10, 9, 0, CountRemoved},
		{"a creation and a deletion in one window", 10, 10, 1, CountRemoved},
		{"a row came back", 10, 11, 0, CountUnexplained},
		{"a creation and a re-activation", 10, 12, 1, CountUnexplained},
	}
	for _, tc := range cases {
		if got := DecideCountChange(tc.prev, tc.cur, tc.newRows); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestDecideK8s(t *testing.T) {
	prev := &Marks{
		K8sActiveCount: ip(5), K8sCreatedMark: tp("2026-09-17T09:00:00Z"), K8sDeployedMark: tp("2026-09-17T09:30:00Z"),
		NodeCount: ip(3), NodeCreatedMark: tp("2026-09-17T08:00:00Z"),
	}
	same := K8sProbe{WorkloadCount: 5, CreatedMax: tp("2026-09-17T09:00:00Z"), DeployedMax: tp("2026-09-17T09:30:00Z"), NodeCount: 3, NodeCreated: tp("2026-09-17T08:00:00Z")}
	with := func(f func(p *K8sProbe)) K8sProbe {
		p := same
		f(&p)
		return p
	}
	cases := []struct {
		name  string
		prev  *Marks
		probe K8sProbe
		want  K8sDecision
	}{
		{"first sighting", nil, same, K8sDecision{First: true}},
		{"marks predating node probing count as first", &Marks{K8sActiveCount: ip(5)}, same, K8sDecision{First: true}},
		{"unchanged", prev, same, K8sDecision{}},
		{"workload deleted", prev, with(func(p *K8sProbe) { p.WorkloadCount = 4 }), K8sDecision{WorkloadsRemoved: true}},
		{"workload created", prev, with(func(p *K8sProbe) { p.CreatedMax = tp("2026-09-17T09:10:00Z") }), K8sDecision{Rebuild: true}},
		{"workload came back", prev, with(func(p *K8sProbe) { p.WorkloadCount = 6 }), K8sDecision{Rebuild: true}},
		{"workload created and another deleted", prev, with(func(p *K8sProbe) { p.CreatedMax = tp("2026-09-17T09:10:00Z") }), K8sDecision{Rebuild: true}},
		{"node replaced (same count)", prev, with(func(p *K8sProbe) { p.NodeCreated = tp("2026-09-17T09:40:00Z") }), K8sDecision{Rebuild: true}},
		{"node removed", prev, with(func(p *K8sProbe) { p.NodeCount = 2 }), K8sDecision{NodesRemoved: true}},
		{"rollout becomes refs", prev, with(func(p *K8sProbe) { p.DeployedMax = tp("2026-09-17T09:45:00Z") }),
			K8sDecision{RefsDeployedAfter: tp("2026-09-17T09:30:00Z")}},
		{"deletion and rollout together", prev, with(func(p *K8sProbe) {
			p.WorkloadCount = 4
			p.DeployedMax = tp("2026-09-17T09:45:00Z")
		}), K8sDecision{WorkloadsRemoved: true, RefsDeployedAfter: tp("2026-09-17T09:30:00Z")}},
	}
	for _, tc := range cases {
		got := DecideK8s(tc.prev, tc.probe)
		if got.First != tc.want.First || got.Rebuild != tc.want.Rebuild ||
			got.WorkloadsRemoved != tc.want.WorkloadsRemoved || got.NodesRemoved != tc.want.NodesRemoved ||
			!sameTime(got.RefsDeployedAfter, tc.want.RefsDeployedAfter) {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func TestLaterMark(t *testing.T) {
	early, late := tp("2026-09-17T09:00:00Z"), tp("2026-09-17T10:00:00Z")
	if got := laterMark(late, nil); got != late {
		t.Error("a missing reading must not clear the mark")
	}
	if got := laterMark(late, early); got != late {
		t.Error("an earlier reading must not move the mark back")
	}
	if got := laterMark(early, late); got != late {
		t.Error("a later reading must advance the mark")
	}
	if got := laterMark(nil, early); got != early {
		t.Error("first reading must set the mark")
	}
}

func TestLeastRecentlyProbedFirst(t *testing.T) {
	t0 := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	probedAt := map[string]time.Time{
		"b": t0.Add(2 * time.Minute),
		"c": t0,
		"d": t0, // same time as c: ties go by id
	}
	got := leastRecentlyProbedFirst([]string{"d", "b", "a", "c", "e"}, probedAt)
	want := []string{"a", "e", "c", "d", "b"} // never probed (a, e) first, then oldest
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// A run that runs out of time must not keep starting from the same tenants.
// Simulate the detector's loop with a budget of 3 tenants per run over 10
// tenants, stamping the ones it probes with the run's time the way saveMarks
// does, and require every tenant to be reached within ceil(10/3) runs.
func TestProbeOrderReachesEveryTenant(t *testing.T) {
	tenants := []string{"t0", "t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8", "t9"}
	const budget = 3
	probedAt := map[string]time.Time{}
	lastRun := map[string]int{}
	start := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)

	runs := (len(tenants) + budget - 1) / budget
	for run := 1; run <= runs; run++ {
		now := start.Add(time.Duration(run) * 10 * time.Minute)
		for _, id := range leastRecentlyProbedFirst(tenants, probedAt)[:budget] {
			probedAt[id] = now
			lastRun[id] = run
		}
	}
	for _, id := range tenants {
		if _, ok := lastRun[id]; !ok {
			t.Errorf("tenant %s was never probed in %d runs of %d", id, runs, budget)
		}
	}

	// And in steady state no tenant waits longer than that between probes.
	for run := runs + 1; run <= runs*5; run++ {
		now := start.Add(time.Duration(run) * 10 * time.Minute)
		for _, id := range leastRecentlyProbedFirst(tenants, probedAt)[:budget] {
			if gap := run - lastRun[id]; gap > runs {
				t.Errorf("run %d: tenant %s waited %d runs, want at most %d", run, id, gap, runs)
			}
			probedAt[id] = now
			lastRun[id] = run
		}
	}
}
