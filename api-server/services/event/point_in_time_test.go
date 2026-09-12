package event

import (
	"testing"
	"time"
)

// An event that arrives already ended must be stored CLOSED. Cloud webhooks
// describe instants -- "ECS Task State Change", "GKE Resource Upgrade Started",
// "Kubernetes Secret Modified" -- and say so by sending ends_at == starts_at.
// Stored FIRING they assert an ongoing condition the producer never claimed,
// and no resolve ever follows for a moment that has passed: on the Rackspace
// estate 18,357 `ECS Task State Change` events sat FIRING at a median age of 52
// days, 69% of everything stuck open there.
func TestIsPointInTime(t *testing.T) {
	base := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	later := base.Add(30 * time.Minute)
	earlier := base.Add(-30 * time.Minute)

	for _, tc := range []struct {
		name        string
		findingType string
		startsAt    *time.Time
		endsAt      *time.Time
		want        bool
		why         string
	}{
		{
			name: "zero-length interval is a moment", findingType: "issue",
			startsAt: &base, endsAt: &base, want: true,
			why: "ECS Task State Change and friends send ends_at == starts_at",
		},
		{
			name: "inverted interval is still a moment", findingType: "issue",
			startsAt: &base, endsAt: &earlier, want: true,
			why: "an end before the start cannot describe an ongoing condition",
		},
		{
			name: "configuration_change with no end time", findingType: "configuration_change",
			startsAt: &base, endsAt: nil, want: true,
			why: "describes a change that already happened (#36597)",
		},
		{
			name: "real interval is NOT point in time", findingType: "issue",
			startsAt: &base, endsAt: &later, want: false,
			why: "a condition that ran and stopped; its producer owns the resolve",
		},
		{
			name: "open-ended issue is NOT point in time", findingType: "issue",
			startsAt: &base, endsAt: nil, want: false,
			why: "the ordinary firing alert, which must stay open",
		},
		{
			name: "missing starts_at is NOT point in time", findingType: "issue",
			startsAt: nil, endsAt: &base, want: false,
			why: "nothing to compare against; never guess a close",
		},
		{
			name: "no timestamps at all is NOT point in time", findingType: "issue",
			startsAt: nil, endsAt: nil, want: false,
			why: "must not panic, and must not close",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPointInTime(tc.findingType, tc.startsAt, tc.endsAt); got != tc.want {
				t.Errorf("isPointInTime = %v, want %v (%s)", got, tc.want, tc.why)
			}
		})
	}
}
