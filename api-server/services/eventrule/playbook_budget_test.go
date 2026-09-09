package eventrule

import (
	"testing"
	"time"

	"nudgebee/services/eventrule/playbooks"
)

func TestPlaybookBudgetExpired(t *testing.T) {
	tests := []struct {
		name     string
		deadline time.Time
		want     bool
	}{
		{
			// The inline request path passes no deadline. It must stay unbounded:
			// a zero time is in the distant past, so a naive After() check would
			// abort every playbook the moment this shipped.
			name:     "zero deadline never expires",
			deadline: time.Time{},
			want:     false,
		},
		{
			name:     "deadline in the past is expired",
			deadline: time.Now().Add(-time.Second),
			want:     true,
		},
		{
			name:     "deadline in the future is not expired",
			deadline: time.Now().Add(time.Hour),
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := playbookBudgetExpired(playbooks.PlaybookEvent{Deadline: tt.deadline})
			if got != tt.want {
				t.Errorf("playbookBudgetExpired() = %v, want %v", got, tt.want)
			}
		})
	}
}
