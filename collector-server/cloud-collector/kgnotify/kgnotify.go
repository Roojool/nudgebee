// Package kgnotify tells the api-server's knowledge-graph consumer that
// inventory changed. It depends only on common and config, so both the account
// ETL and the provider event processors can import it.
//
// Every publish is best-effort: failures are logged, never returned. The
// api-server's inventory change detector re-derives creates and deletes from
// the inventory tables every 10 minutes, so a lost message only delays the graph.
package kgnotify

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"nudgebee/collector/cloud/common"
	"nudgebee/collector/cloud/config"
)

// Op values for ResourceRef.
const (
	OpUpsert = "upsert"
	OpDelete = "delete"
)

const messageSource = "cloud-collector"

// messageExpiration drops messages the consumer could not take within two hours;
// by then the hourly tenant build has covered them.
const messageExpiration = 2 * time.Hour

// Message mirrors api-server/services/knowledge_graph/queue/types.go
// KGUpdateMessage.
type Message struct {
	TenantID      string        `json:"tenant_id"`
	Source        string        `json:"source"`
	RequestedAt   time.Time     `json:"requested_at"`
	CorrelationID string        `json:"correlation_id"`
	AccountIDs    []string      `json:"account_ids,omitempty"`
	Resources     []ResourceRef `json:"resources,omitempty"`
}

// ResourceRef mirrors KGResourceRef.
type ResourceRef struct {
	AccountID          string `json:"account_id,omitempty"`
	ProviderResourceID string `json:"provider_resource_id"`
	NodeType           string `json:"node_type,omitempty"`
	Op                 string `json:"op"`
}

// publish is swapped by tests.
var publish = func(ctx context.Context, msg Message) error {
	return common.MqPublish(
		config.Config.RabbitMqKGUpdateExchange,
		config.Config.RabbitMqKGUpdateQueue,
		msg,
		common.MqPublishWithExpiration(messageExpiration),
		common.MqPublishWithContext(ctx),
	)
}

// PublishAccountUpdate asks for one cloud account's graph to be rebuilt.
func PublishAccountUpdate(ctx context.Context, tenantID, accountID string) {
	if tenantID == "" || accountID == "" {
		slog.Warn("kg_update: account update without tenant or account id, not published",
			"tenant_id", tenantID, "account_id", accountID)
		return
	}
	send(ctx, Message{TenantID: tenantID, AccountIDs: []string{accountID}})
}

// PublishResourceUpdate reports one changed resource. The api-server applies it
// to that resource's node directly and rebuilds the account only when it cannot.
// providerResourceID is cloud_resourses.resourse_id, its ARN, or
// cloud_resourses.id.
func PublishResourceUpdate(ctx context.Context, tenantID, accountID, providerResourceID, op string) {
	if tenantID == "" || accountID == "" || providerResourceID == "" {
		slog.Warn("kg_update: resource update missing an id, not published",
			"tenant_id", tenantID, "account_id", accountID, "provider_resource_id", providerResourceID)
		return
	}
	send(ctx, Message{
		TenantID: tenantID,
		Resources: []ResourceRef{{
			AccountID:          accountID,
			ProviderResourceID: providerResourceID,
			Op:                 op,
		}},
	})
}

// OpFor maps an inventory row's active flag to the ref op.
func OpFor(active bool) string {
	if active {
		return OpUpsert
	}
	return OpDelete
}

func send(ctx context.Context, msg Message) {
	// One gate for every notification this package sends. With it off this
	// collector tells the graph nothing, but the graph does not stop moving:
	// the api-server's inventory change detector re-derives the same creates
	// and deletes from the inventory tables every 10 minutes (see the package
	// comment), so updates lag by up to that cycle instead of arriving with
	// the event. Switch that detector off too and only the hourly build is left.
	if !config.Config.KGUpdateNotificationsEnabled {
		slog.Debug("kg_update: notifications disabled, not published",
			"tenant_id", msg.TenantID, "account_ids", msg.AccountIDs, "resources", len(msg.Resources))
		return
	}

	msg.Source = messageSource
	msg.RequestedAt = time.Now().UTC()
	msg.CorrelationID = uuid.New().String()
	if err := publish(ctx, msg); err != nil {
		slog.Error("kg_update: failed to publish KG update message",
			"tenant_id", msg.TenantID, "account_ids", msg.AccountIDs, "resources", len(msg.Resources), "error", err)
		return
	}
	slog.Info("kg_update: published KG update message",
		"tenant_id", msg.TenantID, "account_ids", msg.AccountIDs, "resources", len(msg.Resources),
		"correlation_id", msg.CorrelationID)
}
