package queue

import (
	"context"
	"time"

	"nudgebee/services/common"
	"nudgebee/services/config"

	"github.com/google/uuid"
)

// PublishKGUpdate publishes a tenant-wide KG update message. ctx carries the
// caller's trace context so the consumer continues the same distributed trace.
func PublishKGUpdate(ctx context.Context, tenantID string, source string) error {
	return PublishKGUpdateMessage(ctx, KGUpdateMessage{
		TenantID: tenantID,
		Source:   source,
	})
}

// PublishKGUpdateMessage publishes a KG update message with an explicit scope,
// filling in RequestedAt and CorrelationID when the caller left them empty.
// Use this to request an account-scoped rebuild.
func PublishKGUpdateMessage(ctx context.Context, message KGUpdateMessage) error {
	if message.RequestedAt.IsZero() {
		message.RequestedAt = time.Now().UTC()
	}
	if message.CorrelationID == "" {
		message.CorrelationID = uuid.New().String()
	}

	return common.MqPublish(
		config.Config.RabbitMqKGUpdateExchange,
		config.Config.RabbitMqKGUpdateQueue,
		message,
		common.MqPublishWithContext(ctx),
	)
}
