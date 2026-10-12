package aws

import (
	"compress/gzip"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"runtime"
	"testing"

	"nudgebee/collector/cloud/providers"

	"github.com/stretchr/testify/require"
)

func openCostReportFixture(t *testing.T) io.Reader {
	t.Helper()
	f, err := os.Open("testdata/aws_cost_report.csv.gz")
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	gzr, err := gzip.NewReader(f)
	require.NoError(t, err)
	return gzr
}

// Streaming a DAILY report hands over the same line items, in the same order,
// as parsing it whole — in full batches except the last.
func TestStreamAwsBillingReport_MatchesWholeParse(t *testing.T) {
	want, err := readAwsBillingReport(openCostReportFixture(t), "DAILY")
	require.NoError(t, err)
	require.Greater(t, len(want), providers.UsageReportBatchSize, "fixture must span several batches")

	got := []providers.UsageReportItem{}
	batches := []int{}
	err = streamAwsBillingReport(openCostReportFixture(t), "DAILY", func(batch []providers.UsageReportItem) error {
		batches = append(batches, len(batch))
		got = append(got, batch...)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, want, got)
	for _, n := range batches[:len(batches)-1] {
		require.Equal(t, providers.UsageReportBatchSize, n)
	}
}

func TestStreamAwsBillingReport_StopsOnEmitError(t *testing.T) {
	calls := 0
	err := streamAwsBillingReport(openCostReportFixture(t), "DAILY", func([]providers.UsageReportItem) error {
		calls++
		return fmt.Errorf("sink down")
	})
	require.EqualError(t, err, "sink down")
	require.Equal(t, 1, calls)
}

// syntheticCostReport writes a CUR of rows lines built from the fixture's
// header and first data row, each with its own resource id.
func syntheticCostReport(t *testing.T, rows int) io.Reader {
	t.Helper()
	r := csv.NewReader(openCostReportFixture(t))
	header, err := r.Read()
	require.NoError(t, err)
	template, err := r.Read()
	require.NoError(t, err)
	resourceCol := -1
	for i, h := range header {
		if h == "lineItem/ResourceId" {
			resourceCol = i
		}
	}
	require.GreaterOrEqual(t, resourceCol, 0)

	pr, pw := io.Pipe()
	go func() {
		w := csv.NewWriter(pw)
		_ = w.Write(header)
		row := append([]string(nil), template...)
		for i := 0; i < rows; i++ {
			row[resourceCol] = fmt.Sprintf("arn:aws:ec2:us-east-1:123456789012:snapshot/snap-%017x", i)
			_ = w.Write(row)
		}
		w.Flush()
		_ = pw.CloseWithError(w.Error())
	}()
	return pr
}

// peakLiveHeapWhileStreaming streams a synthetic report and returns the largest
// live heap seen at a batch boundary, after a GC.
func peakLiveHeapWhileStreaming(t *testing.T, rows int) uint64 {
	t.Helper()
	var peak uint64
	var stats runtime.MemStats
	err := streamAwsBillingReport(syntheticCostReport(t, rows), "DAILY", func(batch []providers.UsageReportItem) error {
		runtime.GC()
		runtime.ReadMemStats(&stats)
		peak = max(peak, stats.HeapAlloc)
		return nil
	})
	require.NoError(t, err)
	return peak
}

// Guard for #38972: the cost sync's memory must not grow with the size of the
// bill. Streaming 10x the lines must not raise the peak live heap beyond noise;
// holding the report (the pre-#38972 shape) would raise it ~10x.
func TestStreamAwsBillingReport_HeapStaysFlat(t *testing.T) {
	if testing.Short() {
		t.Skip("streams 200k synthetic lines")
	}
	small := peakLiveHeapWhileStreaming(t, 20_000)
	large := peakLiveHeapWhileStreaming(t, 200_000)
	t.Logf("peak live heap: 20k lines %d KiB, 200k lines %d KiB", small>>10, large>>10)
	require.Less(t, large, small+16<<20, "peak live heap grew with the number of lines")
}
