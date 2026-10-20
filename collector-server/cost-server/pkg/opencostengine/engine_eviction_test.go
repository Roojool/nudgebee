package opencostengine

import (
	"sync"
	"testing"
	"time"

	"github.com/opencost/opencost/core/pkg/clusters"
	"github.com/opencost/opencost/pkg/costmodel"
)

// Eviction contract for the per-cluster engine cache.
//
// The map used to grow for the life of the pod, and each entry left a goroutine
// polling that customer's Prometheus through the relay every 5 minutes. The
// sweeper here is what ends that, so what it must and must not evict is pinned.

// fakeClusterMap satisfies clusters.ClusterMap and records StopRefresh, standing
// in for upstream's *prom.PrometheusClusterMap.
type fakeClusterMap struct {
	mu      sync.Mutex
	stopped int
}

func (f *fakeClusterMap) StopRefresh() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped++
}

func (f *fakeClusterMap) stopCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}

func (f *fakeClusterMap) GetClusterIDs() []string                        { return nil }
func (f *fakeClusterMap) AsMap() map[string]*clusters.ClusterInfo        { return nil }
func (f *fakeClusterMap) InfoFor(clusterID string) *clusters.ClusterInfo { return nil }
func (f *fakeClusterMap) NameFor(clusterID string) string                { return clusterID }
func (f *fakeClusterMap) NameIDFor(clusterID string) string              { return clusterID }

// seedAccess installs an entry directly, bypassing the real constructor (which
// needs a database, a relay and a Prometheus).
func seedAccess(t *testing.T, clusterID string, lastUsed time.Time, inFlight int) *fakeClusterMap {
	t.Helper()
	cm := &fakeClusterMap{}
	accessMu.Lock()
	accesses[clusterID] = &accessEntry{
		access:   &costmodel.Accesses{ClusterMap: cm},
		lastUsed: lastUsed,
		inFlight: inFlight,
	}
	accessMu.Unlock()
	t.Cleanup(func() {
		accessMu.Lock()
		delete(accesses, clusterID)
		accessMu.Unlock()
	})
	return cm
}

func accessCount() int {
	accessMu.Lock()
	defer accessMu.Unlock()
	return len(accesses)
}

func TestEvictIdleAccesses_DropsIdleEntriesAndStopsTheirRefreshLoop(t *testing.T) {
	now := time.Now()
	cm := seedAccess(t, "cluster-idle", now.Add(-2*accessIdleTTL), 0)

	if got := evictIdleAccesses(now); got != 1 {
		t.Fatalf("evicted = %d, want 1", got)
	}
	if accessCount() != 0 {
		t.Errorf("entry survived eviction")
	}
	// The important half: dropping the map entry alone would leave the refresh
	// goroutine running and holding the whole engine reachable.
	if got := cm.stopCount(); got != 1 {
		t.Errorf("StopRefresh called %d times, want 1", got)
	}
}

func TestEvictIdleAccesses_KeepsRecentlyUsedEntries(t *testing.T) {
	now := time.Now()
	cm := seedAccess(t, "cluster-warm", now.Add(-accessIdleTTL/2), 0)

	if got := evictIdleAccesses(now); got != 0 {
		t.Fatalf("evicted = %d, want 0", got)
	}
	if accessCount() != 1 {
		t.Errorf("recently used entry was dropped")
	}
	if got := cm.stopCount(); got != 0 {
		t.Errorf("StopRefresh called %d times on a live entry, want 0", got)
	}
}

func TestEvictIdleAccesses_WillNotEvictWhileARequestIsRunning(t *testing.T) {
	// A long allocation query over a wide window can easily outlive the TTL.
	// Evicting underneath it stops the cluster map refresh it is reading from.
	now := time.Now()
	cm := seedAccess(t, "cluster-busy", now.Add(-10*accessIdleTTL), 1)

	if got := evictIdleAccesses(now); got != 0 {
		t.Fatalf("evicted = %d, want 0 (request still in flight)", got)
	}
	if got := cm.stopCount(); got != 0 {
		t.Errorf("StopRefresh called %d times under a live request, want 0", got)
	}

	// Once the request finishes the entry becomes evictable — but only after it
	// has then sat idle, since releasing refreshes lastUsed.
	releaseAccess("cluster-busy")
	if got := evictIdleAccesses(time.Now()); got != 0 {
		t.Errorf("evicted = %d immediately after release, want 0", got)
	}
	if got := evictIdleAccesses(time.Now().Add(2 * accessIdleTTL)); got != 1 {
		t.Errorf("entry never became evictable after its TTL elapsed")
	}
}

func TestReleaseAccess_DoesNotUnderflowInFlight(t *testing.T) {
	// Defensive: a double release must not drive the counter negative, which
	// would make the entry permanently unevictable.
	now := time.Now()
	seedAccess(t, "cluster-double-release", now.Add(-2*accessIdleTTL), 1)

	releaseAccess("cluster-double-release")
	releaseAccess("cluster-double-release")

	accessMu.Lock()
	inFlight := accesses["cluster-double-release"].inFlight
	accessMu.Unlock()
	if inFlight != 0 {
		t.Errorf("inFlight = %d, want 0", inFlight)
	}
	if got := evictIdleAccesses(time.Now().Add(2 * accessIdleTTL)); got != 1 {
		t.Errorf("entry was not evictable after a double release")
	}
}

func TestReleaseAccess_UnknownClusterIsANoop(t *testing.T) {
	releaseAccess("never-seen") // must not panic
}
