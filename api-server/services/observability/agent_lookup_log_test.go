package observability

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"nudgebee/services/account"
)

// TestLogAgentLookupFailureLevel: an account with no live agent is expected during
// capability discovery (WARN); any other lookup failure stays ERROR.
func TestLogAgentLookupFailureLevel(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantLevel string
	}{
		{"no agent", fmt.Errorf("no agent connected for account acc-1: %w", account.ErrAgentNotConnected), "level=WARN"},
		{"sentinel", account.ErrAgentNotConnected, "level=WARN"},
		{"missing account id", errors.New("accountId is required"), "level=ERROR"},
		{"database error", errors.New("pq: connection refused"), "level=ERROR"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			logAgentLookupFailure(slog.New(slog.NewTextHandler(&buf, nil)), "acc-1", c.err)
			if !strings.Contains(buf.String(), c.wantLevel) {
				t.Errorf("log = %q, want %s", buf.String(), c.wantLevel)
			}
		})
	}
}
