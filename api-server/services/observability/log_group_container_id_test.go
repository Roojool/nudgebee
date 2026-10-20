package observability

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A log group's container_id is /k8s/<namespace>/<workload>. The UI reads a four-segment
// id as Prometheus's /k8s/<namespace>/<pod>/<container> and trims a pod suffix off the
// third segment, so a provider that appends the container to a workload id gets its
// workload shortened (cloud-collector-server became "cloud") and the +/- Logs drill-down
// then filters on a name that matches nothing. OpenObserve, CubeAPM, New Relic and Splunk
// Enterprise all shipped that shape before it was caught. Only Prometheus emits the pod
// form, and it copies the id from its metric rather than formatting one.
func TestLogGroupContainerIDIsNamespaceAndWorkload(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	// Correctly-shaped builders seen. A floor on it keeps the scan honest: if it stops
	// finding the providers (wrong directory, renamed files, a builder refactored off
	// the one-line shape), it would otherwise pass while checking nothing.
	wellFormed := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			// Case-insensitive so the providers that build a local containerID variable
			// (Loki, Elasticsearch, Dynatrace, ...) are covered as well as group.ContainerID.
			if !strings.Contains(strings.ToLower(line), "containerid") {
				continue
			}
			switch {
			case strings.Contains(line, `"/k8s/%s/%s/%s"`):
				t.Errorf("%s:%d builds a four-segment log-group container_id; emit /k8s/<namespace>/<workload>", f, i+1)
			case strings.Contains(line, `"/k8s/%s/%s"`):
				wellFormed++
			}
		}
	}
	// Fourteen lines build the id today; ten leaves room to consolidate a few.
	if wellFormed < 10 {
		t.Fatalf("found only %d log-group container_id builders; the scan no longer sees the providers", wellFormed)
	}
}
