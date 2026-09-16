package queue

import "time"

// KGUpdateMessage represents a message requesting a KG update for a tenant.
//
// Scope fields are optional and additive: a message with none of them set means
// "rebuild this tenant exactly as the cron does", which is what every publisher
// sent before account-scoped builds existed. Set AccountIDs to rebuild only those
// cloud accounts.
type KGUpdateMessage struct {
	TenantID      string    `json:"tenant_id"`      // Required: tenant to update
	Source        string    `json:"source"`         // What triggered: "cron", "webhook", "manual"
	RequestedAt   time.Time `json:"requested_at"`   // When requested
	CorrelationID string    `json:"correlation_id"` // For tracing

	// AccountIDs restricts the rebuild to these cloud accounts. Empty means every
	// account in the tenant's filter (the historical behaviour).
	AccountIDs []string `json:"account_ids,omitempty"`
	// Sources restricts which static sources run. Empty means the tenant filter's.
	Sources []string `json:"sources,omitempty"`
	// FlowSources restricts which flow sources run. Empty means the tenant
	// filter's for a tenant-wide build, and none at all for an account-scoped one.
	FlowSources []string `json:"flow_sources,omitempty"`

	// Resources names individual resources that changed. Reserved for the
	// resource-level fast path; ignored today, but declared now so collectors can
	// start populating it without a second message-shape migration.
	Resources []KGResourceRef `json:"resources,omitempty"`
}

// KGResourceRef identifies one changed resource within an account.
// Reserved: no consumer acts on it yet.
type KGResourceRef struct {
	// ProviderResourceID is the collector's own identifier for the resource —
	// cloud_resourses.resourse_id / an ARN / a k8s cloud_resource_id.
	ProviderResourceID string `json:"provider_resource_id"`
	// NodeType optionally narrows the reverse lookup to one KG node type.
	NodeType string `json:"node_type,omitempty"`
	// Op is "upsert" or "delete".
	Op string `json:"op"`
}
