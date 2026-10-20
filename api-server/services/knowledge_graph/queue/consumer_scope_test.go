package queue

import (
	"reflect"
	"testing"
)

// The tenant filter decides what a tenant is ALLOWED to build; the claim decides
// what was asked for this time. Intersecting the two is what stops an
// account-scoped request from widening a restricted filter.
func TestIntersectScope(t *testing.T) {
	tests := []struct {
		name          string
		filterValues  []string
		requestValues []string
		want          []string
	}{
		{
			name:          "request narrows an unrestricted filter",
			filterValues:  nil,
			requestValues: []string{"acct-a"},
			want:          []string{"acct-a"},
		},
		{
			name:          "request narrows a broader filter",
			filterValues:  []string{"acct-a", "acct-b"},
			requestValues: []string{"acct-b"},
			want:          []string{"acct-b"},
		},
		{
			name:          "request for an account the filter excludes yields nothing",
			filterValues:  []string{"acct-a"},
			requestValues: []string{"acct-z"},
			want:          []string{},
		},
		{
			name:          "partial overlap keeps only the overlap",
			filterValues:  []string{"acct-a", "acct-b"},
			requestValues: []string{"acct-b", "acct-z"},
			want:          []string{"acct-b"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := intersectScope(tc.filterValues, tc.requestValues)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("intersectScope(%v, %v) = %v, want %v",
					tc.filterValues, tc.requestValues, got, tc.want)
			}
		})
	}
}

// A tenant filter that restricts which sources may be built must not be widened
// by what a request asks for. The RPC action accepts a caller-supplied `sources`
// list, so treating a non-overlapping request as "build what was asked" would let
// any caller build a source the tenant deliberately excluded.
func TestIntersectScope_NonOverlappingRequestYieldsEmptyNotTheRequest(t *testing.T) {
	got := intersectScope([]string{"aws"}, []string{"gcp"})
	if len(got) != 0 {
		t.Errorf("intersectScope([aws], [gcp]) = %v, want empty so the caller skips the filter", got)
	}
}
