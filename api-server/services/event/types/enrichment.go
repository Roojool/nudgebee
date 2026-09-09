package types

// EventEnrichmentMessage is the job carried on the event_enrich queue.
//
// It lives here rather than in event/queue because both the producer (package
// event) and the consumer (package event/queue) need it, and event/queue already
// imports event — so event cannot import event/queue back.
//
// The event row is already committed by the time this is published; the consumer
// re-reads it, runs the playbook, merges the evidence in, and only then hands the
// event on to event_post_process.
type EventEnrichmentMessage struct {
	EventID   string `json:"event_id"`
	TenantID  string `json:"tenant_id"`
	AccountID string `json:"account_id"`
	// Attempt counts self-driven retries. A broker requeue redelivers identical
	// bytes, so a counter can only be carried by republishing the message.
	Attempt int `json:"attempt,omitempty"`
}
