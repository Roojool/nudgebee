package queue

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"nudgebee/services/config"
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

// After resource refs are applied, what is left decides whether a build is
// queued. A refs-only message must never degrade into a tenant-wide rebuild.
func TestShouldEnqueueScope(t *testing.T) {
	refs := []KGResourceRef{{AccountID: "a", ProviderResourceID: "i-1", Op: "upsert"}}
	cases := []struct {
		name     string
		message  KGUpdateMessage
		accounts []string
		want     bool
	}{
		{"legacy tenant-only message", KGUpdateMessage{TenantID: "t"}, nil, true},
		{"account-scoped message", KGUpdateMessage{TenantID: "t", AccountIDs: []string{"a"}}, []string{"a"}, true},
		{"refs fully applied", KGUpdateMessage{TenantID: "t", Resources: refs}, nil, false},
		{"refs needing a rebuild", KGUpdateMessage{TenantID: "t", Resources: refs}, []string{"a"}, true},
		{"accounts plus applied refs", KGUpdateMessage{TenantID: "t", AccountIDs: []string{"b"}, Resources: refs}, []string{"b"}, true},
		{"drain continuation", KGUpdateMessage{TenantID: "t", Source: drainContinuationSource}, nil, false},
	}
	for _, tc := range cases {
		if got := shouldEnqueueScope(tc.message, tc.accounts); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestReferencedAndUnionAccounts(t *testing.T) {
	got := unionAccounts([]string{"a", "b"}, []string{"b", "", "c"})
	if strings.Join(got, ",") != "a,b,c" {
		t.Errorf("unionAccounts = %v, want [a b c]", got)
	}

	message := KGUpdateMessage{
		TenantID:   "t",
		AccountIDs: []string{"only"},
		Resources: []KGResourceRef{
			{AccountID: "x", ProviderResourceID: "1"},
			{ProviderResourceID: "2"},
			{AccountID: "x", ProviderResourceID: "3"},
		},
	}
	if got := referencedAccounts(message); strings.Join(got, ",") != "x,only" {
		t.Errorf("referencedAccounts = %v, want [x only]", got)
	}
}

// With per-resource updates switched off, every referenced account is rebuilt
// and the applier is never reached — dbms is nil here, so reaching it would
// panic. Refs for an account the message already rebuilds are still skipped.
func TestApplyResourceRefsDisabledRebuildsAccounts(t *testing.T) {
	prev := config.Config.KGResourcePatchEnabled
	config.Config.KGResourcePatchEnabled = false
	defer func() { config.Config.KGResourcePatchEnabled = prev }()

	message := KGUpdateMessage{
		TenantID:   "tenant",
		AccountIDs: []string{"already-rebuilding"},
		Resources: []KGResourceRef{
			{AccountID: "a", ProviderResourceID: "r1", Op: "upsert"},
			{AccountID: "b", ProviderResourceID: "r2", Op: "delete"},
			{AccountID: "a", ProviderResourceID: "r3", Op: "delete"},
			{AccountID: "already-rebuilding", ProviderResourceID: "r4", Op: "upsert"},
		},
	}
	got := applyResourceRefs(context.Background(), nil, message, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if strings.Join(got, ",") != "a,b" {
		t.Errorf("accounts to rebuild = %v, want [a b]", got)
	}
}
