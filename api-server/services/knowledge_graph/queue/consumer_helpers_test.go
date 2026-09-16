package queue

import (
	"errors"
	"strings"
	"testing"
)

// A failed build must surface as an error so its request is recorded 'failed'.
func TestAggregateFilterErrors(t *testing.T) {
	if err := aggregateFilterErrors(nil, 3); err != nil {
		t.Errorf("no failures: got %v, want nil", err)
	}

	cause := errors.New("save failed")
	err := aggregateFilterErrors([]error{cause}, 2)
	if err == nil {
		t.Fatal("one failure: got nil, want an error")
	}
	if !strings.Contains(err.Error(), "1 of 2 filters failed") {
		t.Errorf("error = %q, want the failure count", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Error("aggregated error does not wrap the filter's own error")
	}
}

// A drain continuation carries no scope; enqueueing it would create a tenant-wide
// rebuild request nobody asked for.
func TestShouldEnqueue(t *testing.T) {
	if shouldEnqueue(KGUpdateMessage{TenantID: "t", Source: drainContinuationSource}) {
		t.Error("drain continuation must not be enqueued as a request")
	}
	for _, source := range []string{"cron", "manual", "k8s-collector", "cloud-collector", ""} {
		if !shouldEnqueue(KGUpdateMessage{TenantID: "t", Source: source}) {
			t.Errorf("source %q must be enqueued", source)
		}
	}
}
