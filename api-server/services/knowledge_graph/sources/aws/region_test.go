package aws

import (
	"reflect"
	"testing"

	"nudgebee/services/knowledge_graph/core"
)

func TestNodeRegion(t *testing.T) {
	tests := []struct {
		name string
		node *core.DbNode
		want string
	}{
		{
			name: "region property",
			node: &core.DbNode{Properties: map[string]interface{}{"region": "eu-west-2"}},
			want: "eu-west-2",
		},
		{
			name: "govcloud region is valid",
			node: &core.DbNode{Properties: map[string]interface{}{"region": "us-gov-west-1"}},
			want: "us-gov-west-1",
		},
		{
			name: "global is not a region",
			node: &core.DbNode{Properties: map[string]interface{}{"region": "global"}},
			want: "",
		},
		{
			name: "malformed region is rejected",
			node: &core.DbNode{Properties: map[string]interface{}{"region": "us-east"}},
			want: "",
		},
		{
			name: "falls back to availability zone",
			node: &core.DbNode{Properties: map[string]interface{}{"availability_zone": "ap-south-1b"}},
			want: "ap-south-1",
		},
		{
			name: "falls back to arn",
			node: &core.DbNode{Properties: map[string]interface{}{
				"arn": "arn:aws:cloudformation:eu-west-1:123456789012:stack/app/abc",
			}},
			want: "eu-west-1",
		},
		{
			name: "global arn yields no region",
			node: &core.DbNode{Properties: map[string]interface{}{"arn": "arn:aws:s3:::my-bucket"}},
			want: "",
		},
		{
			name: "falls back to unique key location segment",
			node: &core.DbNode{UniqueKey: "aws:acct-1:us-west-2:RouteTable:vpc-1:rtb-1"},
			want: "us-west-2",
		},
		{
			name: "blank unique key location segment yields no region",
			node: &core.DbNode{UniqueKey: "aws:acct-1::RouteTable:vpc-1:rtb-1"},
			want: "",
		},
		{
			name: "region property wins over availability zone",
			node: &core.DbNode{Properties: map[string]interface{}{
				"region":            "eu-central-1",
				"availability_zone": "us-east-1a",
			}},
			want: "eu-central-1",
		},
		{
			name: "nil properties",
			node: &core.DbNode{},
			want: "",
		},
		{
			name: "nil node",
			node: nil,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nodeRegion(tt.node); got != tt.want {
				t.Errorf("nodeRegion() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNodeRegions(t *testing.T) {
	nodes := []*core.DbNode{
		{Properties: map[string]interface{}{"region": "us-west-2"}},
		{Properties: map[string]interface{}{"region": "eu-west-1"}},
		{Properties: map[string]interface{}{"region": "us-west-2"}}, // duplicate
		{Properties: map[string]interface{}{"region": "global"}},    // not a region
		{Properties: map[string]interface{}{"region": ""}},          // blank
		{Properties: map[string]interface{}{"availability_zone": "ap-south-1c"}},
		{}, // no region at all
	}

	// Sorted output matters: callers issue one CLI call per region, and an
	// unsorted result would make that sequence vary between runs.
	want := []string{"ap-south-1", "eu-west-1", "us-west-2"}
	if got := nodeRegions(nodes); !reflect.DeepEqual(got, want) {
		t.Errorf("nodeRegions() = %v, want %v", got, want)
	}

	if got := nodeRegions(nil); len(got) != 0 {
		t.Errorf("nodeRegions(nil) = %v, want empty", got)
	}
}

func TestRegionFromAZ(t *testing.T) {
	tests := []struct{ az, want string }{
		{"us-east-1a", "us-east-1"},
		{"ap-southeast-2c", "ap-southeast-2"},
		{"us-gov-west-1a", "us-gov-west-1"},
		{"us-east-1", "us-east-1"},
		{"global", ""},
		{"", ""},
		// Local Zone / Wavelength AZs carry an extra segment ("us-west-2-lax-1a").
		// Recovering the parent region from those is ambiguous against four-segment
		// partition regions (us-gov-west-1), so we deliberately return "" and let
		// the caller's later fallbacks — or the skip — take over. Failing closed
		// beats guessing a region and querying the wrong one.
		{"us-west-2-lax-1a", ""},
	}

	for _, tt := range tests {
		if got := regionFromAZ(tt.az); got != tt.want {
			t.Errorf("regionFromAZ(%q) = %q, want %q", tt.az, got, tt.want)
		}
	}
}
