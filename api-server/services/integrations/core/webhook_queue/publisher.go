package webhook_queue

import (
	"context"
	"time"

	"nudgebee/services/common"
	"nudgebee/services/config"
)

// PublishWebhookProcess publishes a webhook row ID for async processing.
// ctx carries the ingest request's trace context so the consumer continues
// the same distributed trace.
func PublishWebhookProcess(ctx context.Context, webhookRowID string) error {
	message := WebhookProcessMessage{
		WebhookRowID: webhookRowID,
	}

	// Mandatory: if the consumer is down and its queue was never declared, the
	// broker would otherwise discard the message and confirm the publish, and
	// the webhook row would sit at processing_status='pending' forever. With
	// this, MqPublish returns ErrMqUnroutable and the caller processes the
	// webhook synchronously instead.
	return common.MqPublish(
		config.Config.RabbitMqWebhookProcessExchange,
		config.Config.RabbitMqWebhookProcessQueue,
		message,
		common.MqPublishWithExpiration(1*time.Hour),
		common.MqPublishWithContext(ctx),
		common.MqPublishWithMandatory(),
	)
}
