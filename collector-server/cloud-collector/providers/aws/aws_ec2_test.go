package aws

import (
	"context"
	"fmt"
	"math"
	"nudgebee/collector/cloud/common"
	"nudgebee/collector/cloud/providers"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
)

func TestGetAvailableEc2Instances(t *testing.T) {
	t.Skip("Skipping integration test that requires AWS credentials")
	cfg, err := getAwsConfigFromAccount(context.Background(), providers.Account{
		AccountNumber: testAWSAccountNumber,
	})
	assert.Nil(t, err)

	instances, err := getAvailableEc2Instances(cfg, "us-east-1", 16, 4, "", "SUSE", "EBS Only")
	assert.Nil(t, err)
	assert.NotNil(t, instances)

	data, err := common.MarshalJson(instances)
	assert.Nil(t, err)
	fmt.Println(string(data))
	t.Log(string(data))
}

func TestPricing(t *testing.T) {
	// getEc2PricesBasedOnPriceList reads the EC2 price list from the metastore
	// Postgres DB. Skip when no DB is reachable so the suite stays hermetic;
	// the test still runs in environments (CI/dev) where the DB is up.
	if _, err := common.GetDatabaseManager(common.Metastore); err != nil {
		t.Skip("Skipping test that requires a reachable metastore database")
	}
	price, err := getEc2PricesBasedOnPriceList("us-east-1", "m4.xlarge")
	assert.Nil(t, err)
	assert.NotNil(t, price)
}

func TestParseAwsMemoryGiB(t *testing.T) {
	tests := []struct {
		input    string
		expected float64
	}{
		{"1 GiB", 1.0},
		{"16 GiB", 16.0},
		{"192 GiB", 192.0},
		{"1,952 GiB", 1952.0},
		{"24,576 GiB", 24576.0},
		{"512 MiB", 0.5},
		{"1024 MiB", 1.0},
		{"24 TiB", 24576.0},
		{"16", 16.0}, // bare number assumed GiB
		{"", 0.0},
		{"  ", 0.0},
		{"N/A", 0.0},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := parseAwsMemoryGiB(tc.input)
			assert.Equal(t, tc.expected, got)
		})
	}
}

func TestGp2ToGp3Match(t *testing.T) {
	// gp3 defaults: 3000 IOPS / 125 MiB/s. gp2 IOPS = clamp(3*GiB, 100, 16000);
	// gp2 sustains 250 MiB/s from 334 GiB. Boundaries drive this table (see spec §5).
	tests := []struct {
		name           string
		sizeGiB        float64
		wantIOPS       int
		wantThroughput int
		wantProvision  bool
	}{
		{"1 GiB clamps to gp2 min IOPS, clean win", 1, 0, 0, false},
		{"33 GiB still at gp2 min IOPS, clean win", 33, 0, 0, false},
		{"100 GiB clean win", 100, 0, 0, false},
		{"170 GiB throughput still <=128 (~gp3 125), clean win", 170, 0, 0, false},
		{"171 GiB crosses gp2 throughput tier, needs 250", 171, 0, 250, true},
		{"250 GiB mid burst band, needs throughput match", 250, 0, 250, true},
		{"333 GiB still under sustained but above tier, needs 250", 333, 0, 250, true},
		{"334 GiB sustained 250 MiB/s, throughput match", 334, 0, 250, true},
		{"999 GiB throughput match, IOPS still <= 3000", 999, 0, 250, true},
		{"1000 GiB gp2 IOPS == gp3 default, throughput match only", 1000, 0, 250, true},
		{"1001 GiB gp2 IOPS exceeds 3000, both matched", 1001, 3003, 250, true},
		{"5334 GiB gp2 IOPS maxes at 16000", 5334, 16000, 250, true},
		{"16 TiB clamps IOPS to 16000", 16384, 16000, 250, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gp2ToGp3Match(tt.sizeGiB)
			assert.Equal(t, tt.wantIOPS, got.RecommendIOPS, "RecommendIOPS")
			assert.Equal(t, tt.wantThroughput, got.RecommendThroughput, "RecommendThroughput")
			assert.Equal(t, tt.wantProvision, got.NeedsProvisioning, "NeedsProvisioning")
		})
	}
}

func TestGp2ToGp3Match_RecommendedConfigNeverBelowGp2(t *testing.T) {
	// When a match config is emitted, it must equal (not undershoot) the gp2's
	// size-derived performance, so applying gp3 with it is never a downgrade.
	for size := 1.0; size <= 16384; size += 13 {
		match := gp2ToGp3Match(size)
		if match.RecommendIOPS > 0 {
			wantIOPS := int(math.Round(3 * size))
			if wantIOPS > gp2MaxIOPS {
				wantIOPS = gp2MaxIOPS
			}
			assert.Equal(t, wantIOPS, match.RecommendIOPS, "size %.0f GiB: recommended IOPS must equal gp2 baseline", size)
			assert.GreaterOrEqual(t, match.RecommendIOPS, gp3DefaultIOPS, "size %.0f GiB: recommended IOPS below gp3 default", size)
		}
		if match.RecommendThroughput > 0 {
			assert.Equal(t, gp2MaxThroughput, match.RecommendThroughput, "size %.0f GiB", size)
			assert.Greater(t, match.RecommendThroughput, gp3DefaultThroughput, "size %.0f GiB: recommended throughput not above gp3 default", size)
		}
	}
}

func TestEbsStoragePricePerGBMonth(t *testing.T) {
	// An unattached volume's saving is what it bills, which depends on its type:
	// pricing every type at the gp2 rate overstated gp3 by 25% and magnetic by 2x.
	tests := []struct {
		volumeType string
		want       float64
	}{
		{"gp2", 0.10},
		{"gp3", 0.08},
		{"io1", 0.125},
		{"io2", 0.125},
		{"st1", 0.045},
		{"sc1", 0.015},
		{"standard", 0.05},
		{"", 0.10},
		{"some-future-type", 0.10},
	}
	for _, tt := range tests {
		t.Run(tt.volumeType, func(t *testing.T) {
			assert.Equal(t, tt.want, ebsStoragePricePerGBMonth(tt.volumeType))
		})
	}
}

func TestEbsModifyParams(t *testing.T) {
	// In production the Data map is JSON round-tripped, so numbers arrive as float64;
	// ebsModifyParams must read those (a plain .(int) assertion would silently miss them).
	tests := []struct {
		name           string
		data           map[string]any
		wantIOPS       int32
		wantThroughput int32
	}{
		{"both present as float64 (JSON/DB path)", map[string]any{"recommended_iops": 6000.0, "recommended_throughput": 250.0}, 6000, 250},
		{"both present as int (in-memory path)", map[string]any{"recommended_iops": 6000, "recommended_throughput": 250}, 6000, 250},
		{"only throughput (334-1000 GiB volume)", map[string]any{"recommended_throughput": 250.0}, 0, 250},
		{"absent (small volume uses gp3 defaults)", map[string]any{}, 0, 0},
		{"zero values ignored", map[string]any{"recommended_iops": 0.0, "recommended_throughput": 0.0}, 0, 0},
		{"wrong type ignored", map[string]any{"recommended_iops": "6000"}, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			iops, throughput := ebsModifyParams(tt.data)
			assert.Equal(t, tt.wantIOPS, iops, "iops")
			assert.Equal(t, tt.wantThroughput, throughput, "throughput")
		})
	}
}

func TestEbsModificationInProgress(t *testing.T) {
	tests := []struct {
		name   string
		states []types.VolumeModificationState
		want   bool
	}{
		{"no modifications", nil, false},
		{"modifying is in progress", []types.VolumeModificationState{types.VolumeModificationStateModifying}, true},
		{"optimizing is in progress", []types.VolumeModificationState{types.VolumeModificationStateOptimizing}, true},
		{"completed does not block", []types.VolumeModificationState{types.VolumeModificationStateCompleted}, false},
		{"failed does not block", []types.VolumeModificationState{types.VolumeModificationStateFailed}, false},
		{"a prior completed plus a current modifying blocks", []types.VolumeModificationState{types.VolumeModificationStateCompleted, types.VolumeModificationStateModifying}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mods := make([]types.VolumeModification, len(tt.states))
			for i, s := range tt.states {
				mods[i] = types.VolumeModification{ModificationState: s}
			}
			assert.Equal(t, tt.want, ebsModificationInProgress(mods))
		})
	}
}
