package recommendation

import (
	"testing"
	"time"
)

func f(v float64) *float64 { return &v }

func containerRec(resource string, allocated, recommended float64) ContainerRecommendation {
	var c ContainerRecommendation
	c.Resource = resource
	c.Allocated.Request = f(allocated)
	c.Recommended.Request = f(recommended)
	return c
}

func TestPodRightSizingRowsCarryWorkloadSavingsOnce(t *testing.T) {
	recMap := map[string][]ContainerRecommendation{
		"sidecar": {containerRec("cpu", 0.5, 0.1)},
		"app":     {containerRec("cpu", 2, 1), containerRec("memory", 2048*1024*1024, 1024*1024*1024)},
	}
	rows := podRightSizingRows(PodRightSizingRow{WorkloadName: "api", UpdatedAt: time.Unix(0, 0).UTC()}, recMap, 40)
	if len(rows) != 2 {
		t.Fatalf("expected one row per container, got %d", len(rows))
	}

	first := rows[0].(*PodRightSizingRow)
	second := rows[1].(*PodRightSizingRow)
	if first.PodName != "app" || second.PodName != "sidecar" {
		t.Fatalf("containers must be in name order, got %q then %q", first.PodName, second.PodName)
	}
	if first.WorkloadSavings == nil || *first.WorkloadSavings != 40 {
		t.Fatalf("first container row must carry the workload savings, got %v", first.WorkloadSavings)
	}
	if second.WorkloadSavings != nil {
		t.Fatalf("later container rows must leave the savings blank, got %v", *second.WorkloadSavings)
	}

	if got := first.ToStringSlice()[10]; got != "40.00" {
		t.Fatalf("savings column on the first row = %q", got)
	}
	if got := second.ToStringSlice()[10]; got != "" {
		t.Fatalf("savings column on the second row = %q", got)
	}
	if first.MemoryRequest != 2048 || first.MemoryRecommended != 1024 || first.MemorySavings != 50 {
		t.Fatalf("memory columns: request %v recommended %v savings %v", first.MemoryRequest, first.MemoryRecommended, first.MemorySavings)
	}
	if second.CPUSavings != 80 {
		t.Fatalf("cpu savings on the sidecar = %v", second.CPUSavings)
	}
}

func TestPodRightSizingRowsColumnsMatchHeader(t *testing.T) {
	cols := (&PodRightSizingExporter{}).GetColumns()
	rows := podRightSizingRows(PodRightSizingRow{}, map[string][]ContainerRecommendation{"app": {}}, 0)
	if got := len(rows[0].ToStringSlice()); got != len(cols) {
		t.Fatalf("row has %d cells for %d columns", got, len(cols))
	}
	if cols[10].Name != "Workload Monthly Savings ($)" {
		t.Fatalf("savings header = %q", cols[10].Name)
	}
}
