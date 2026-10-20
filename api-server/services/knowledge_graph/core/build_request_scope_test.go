package core

import (
	"reflect"
	"testing"
)

// Scope union semantics: "empty means everything", so one tenant-wide request
// swallows every narrower one queued alongside it. Getting this backwards would
// either build too little (missing an account someone asked for) or silently
// promote an account-scoped request into a full tenant rebuild.
func TestScopeAccumulator(t *testing.T) {
	tests := []struct {
		name  string
		input [][]string
		want  []string
	}{
		{
			name:  "union of disjoint scopes",
			input: [][]string{{"a"}, {"b"}},
			want:  []string{"a", "b"},
		},
		{
			name:  "duplicate scopes collapse",
			input: [][]string{{"a"}, {"a"}},
			want:  []string{"a"},
		},
		{
			name:  "an unbounded request widens everything",
			input: [][]string{{"a"}, {}},
			want:  nil,
		},
		{
			name:  "unbounded first still wins",
			input: [][]string{{}, {"a"}},
			want:  nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			acc := newScopeAccumulator()
			for _, in := range tc.input {
				acc.add(in)
			}
			if got := acc.result(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("result = %v, want %v", got, tc.want)
			}
		})
	}
}

// Two requests for the same accounts in a different order must compare equal as
// JSONB, otherwise the pending-duplicate check never fires and a burst of
// identical events piles up rows.
func TestNormalizeScopeList(t *testing.T) {
	got := normalizeScopeList([]string{"b", "a", "b", ""})
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("normalizeScopeList = %v, want %v", got, want)
	}

	if got := normalizeScopeList(nil); !reflect.DeepEqual(got, []string{}) {
		t.Errorf("normalizeScopeList(nil) = %v, want empty slice (JSONB '[]')", got)
	}
}

func TestBuildClaimPartial(t *testing.T) {
	if (&BuildClaim{}).Partial() {
		t.Error("a claim with no account ids must be tenant-wide")
	}
	if !(&BuildClaim{AccountIDs: []string{"a"}}).Partial() {
		t.Error("a claim with account ids must be partial")
	}
	var nilClaim *BuildClaim
	if nilClaim.Partial() {
		t.Error("a nil claim must not report partial")
	}
}

func TestIntervalString(t *testing.T) {
	if got := intervalString(30 * 60 * 1e9); got != "1800 seconds" {
		t.Errorf("intervalString(30m) = %q, want \"1800 seconds\"", got)
	}
	if got := intervalString(0); got != "1 seconds" {
		t.Errorf("intervalString(0) = %q, want a floor of 1 second", got)
	}
}
