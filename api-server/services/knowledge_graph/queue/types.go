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

	// Resources names individual resources that changed. The consumer applies
	// each one to the graph directly (resourcepatch) and queues an account build
	// only for the accounts where that is not possible. A message that carries
	// only Resources and applies fully queues no build.
	Resources []KGResourceRef `json:"resources,omitempty"`
}

// KGResourceRef identifies one changed resource within an account.
type KGResourceRef struct {
	// AccountID is the cloud_accounts.id the resource belongs to. When empty the
	// message's AccountIDs must name exactly one account.
	AccountID string `json:"account_id,omitempty"`
	// ProviderResourceID identifies the resource: cloud_resourses.resourse_id,
	// its ARN or cloud_resourses.id; a k8s workload's cloud_resource_id; or, for
	// a "Node" ref, the Kubernetes node name.
	ProviderResourceID string `json:"provider_resource_id"`
	// NodeType selects how the resource is found. "Node" refs are resolved by
	// Kubernetes node name (node graph nodes carry no provider id); for every
	// other value the node is found by provider id and NodeType is only logged.
	NodeType string `json:"node_type,omitempty"`
	// Op is "upsert" or "delete". It is a hint: the applier re-reads the
	// inventory and writes what it finds.
	Op string `json:"op"`
}
