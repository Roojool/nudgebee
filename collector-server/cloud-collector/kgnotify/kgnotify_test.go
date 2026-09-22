package kgnotify

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"nudgebee/collector/cloud/config"
)

// enableNotifications turns the gate on for one test and restores it after.
func enableNotifications(t *testing.T) {
	t.Helper()
	prev := config.Config.KGUpdateNotificationsEnabled
	config.Config.KGUpdateNotificationsEnabled = true
	t.Cleanup(func() { config.Config.KGUpdateNotificationsEnabled = prev })
}

// Nothing reaches the broker unless the environment turns notifications on.
func TestNotificationsGate(t *testing.T) {
	prev := config.Config.KGUpdateNotificationsEnabled
	t.Cleanup(func() { config.Config.KGUpdateNotificationsEnabled = prev })

	sent := &[]Message{}
	prevPublish := publish
	publish = func(_ context.Context, msg Message) error {
		*sent = append(*sent, msg)
		return nil
	}
	t.Cleanup(func() { publish = prevPublish })

	config.Config.KGUpdateNotificationsEnabled = false
	PublishAccountUpdate(context.Background(), "tenant", "account")
	PublishResourceUpdate(context.Background(), "tenant", "account", "i-0abc", OpUpsert)
	if len(*sent) != 0 {
		t.Fatalf("published %d messages with notifications disabled, want 0", len(*sent))
	}

	config.Config.KGUpdateNotificationsEnabled = true
	PublishAccountUpdate(context.Background(), "tenant", "account")
	if len(*sent) != 1 {
		t.Fatalf("published %d messages with notifications enabled, want 1", len(*sent))
	}
}

// capture swaps the publisher and opens the notification gate, which is off by
// default; TestNotificationsGate covers the closed case.
func capture(t *testing.T, err error) *[]Message {
	t.Helper()
	enableNotifications(t)
	sent := &[]Message{}
	prev := publish
	publish = func(_ context.Context, msg Message) error {
		*sent = append(*sent, msg)
		return err
	}
	t.Cleanup(func() { publish = prev })
	return sent
}

func TestPublishAccountUpdate(t *testing.T) {
	sent := capture(t, nil)
	PublishAccountUpdate(context.Background(), "tenant", "account")

	if len(*sent) != 1 {
		t.Fatalf("published %d messages, want 1", len(*sent))
	}
	msg := (*sent)[0]
	if msg.TenantID != "tenant" || len(msg.AccountIDs) != 1 || msg.AccountIDs[0] != "account" || len(msg.Resources) != 0 {
		t.Errorf("unexpected message %+v", msg)
	}
	if msg.Source != "cloud-collector" || msg.CorrelationID == "" || msg.RequestedAt.IsZero() {
		t.Errorf("provenance not stamped: %+v", msg)
	}
}

// The wire shape must match the api-server's KGUpdateMessage / KGResourceRef.
func TestPublishResourceUpdate_WireShape(t *testing.T) {
	sent := capture(t, nil)
	PublishResourceUpdate(context.Background(), "tenant", "account", "i-0abc", OpDelete)

	if len(*sent) != 1 {
		t.Fatalf("published %d messages, want 1", len(*sent))
	}
	raw, err := json.Marshal((*sent)[0])
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if _, has := wire["account_ids"]; has {
		t.Error("a resource update must not carry account_ids: the consumer would rebuild the account")
	}
	refs, _ := wire["resources"].([]any)
	if len(refs) != 1 {
		t.Fatalf("resources = %v", wire["resources"])
	}
	ref := refs[0].(map[string]any)
	if ref["account_id"] != "account" || ref["provider_resource_id"] != "i-0abc" || ref["op"] != "delete" {
		t.Errorf("ref = %v", ref)
	}
}

func TestPublish_SkipsMissingIDsAndSwallowsErrors(t *testing.T) {
	sent := capture(t, errors.New("broker down"))
	PublishAccountUpdate(context.Background(), "", "account")
	PublishResourceUpdate(context.Background(), "tenant", "account", "", OpUpsert)
	if len(*sent) != 0 {
		t.Errorf("published %d messages with missing ids", len(*sent))
	}
	PublishAccountUpdate(context.Background(), "tenant", "account") // must not panic or return
	if len(*sent) != 1 {
		t.Errorf("attempted %d publishes, want 1", len(*sent))
	}
}

func TestOpFor(t *testing.T) {
	if OpFor(true) != OpUpsert || OpFor(false) != OpDelete {
		t.Error("OpFor mapping wrong")
	}
}
