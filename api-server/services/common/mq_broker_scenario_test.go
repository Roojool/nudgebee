package common

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"nudgebee/services/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Live-broker reproduction of the dev outage: RabbitMQ was recreated, the
// consumer never re-declared its queue, and every publish to the exchange was
// discarded by the broker while MqPublish reported success — so the webhook
// handler's fallback never ran and the payload was lost.
//
// Needs a real broker; skipped when NB_TEST_RABBIT_HOST is unset:
//
//	docker run -d --name nb-mq-test -p 5673:5672 rabbitmq:3-management
//	NB_TEST_RABBIT_HOST=localhost NB_TEST_RABBIT_PORT=5673 go test ./common/ -run TestMqScenario -v
func requireLiveBroker(t *testing.T) {
	t.Helper()
	host := os.Getenv("NB_TEST_RABBIT_HOST")
	if host == "" {
		t.Skip("NB_TEST_RABBIT_HOST not set; skipping live-broker scenario")
	}
	port, err := strconv.Atoi(os.Getenv("NB_TEST_RABBIT_PORT"))
	require.NoError(t, err, "NB_TEST_RABBIT_PORT must be set alongside NB_TEST_RABBIT_HOST")

	config.Config.RabbitMqHost = host
	config.Config.RabbitMqPort = port
	config.Config.RabbitMqUsername = envOrDefault("NB_TEST_RABBIT_USER", "guest")
	config.Config.RabbitMqPassword = envOrDefault("NB_TEST_RABBIT_PASSWORD", "guest")
	config.Config.ServerRole = config.ServerRoleWorker

	MqClose()
	t.Cleanup(MqClose)
	require.NotNil(t, getConnection(), "could not reach the test broker")
}

// TestMqScenarioUnroutablePublishIsReported is the regression: publishing to an
// exchange whose queue was never declared must fail, not silently succeed.
func TestMqScenarioUnroutablePublishIsReported(t *testing.T) {
	requireLiveBroker(t)

	exchange := fmt.Sprintf("scenario_exchange_%d", time.Now().UnixNano())
	routingKey := "webhook_process"

	// No consumer has ever run, so the exchange exists (the publisher declares
	// it) but nothing is bound to it — exactly the state dev was left in.
	err := MqPublish(exchange, routingKey, map[string]string{"webhook_row_id": "row-1"},
		MqPublishWithMandatory())

	require.Error(t, err, "publish into an exchange with no queue must not report success")
	assert.ErrorIs(t, err, ErrMqUnroutable)
}

// TestMqScenarioPlainPublishSilentlyDrops pins the behaviour that caused the
// outage, so the value of the mandatory flag stays visible: without it the same
// publish into an exchange with no queue reports success and the payload is gone.
func TestMqScenarioPlainPublishSilentlyDrops(t *testing.T) {
	requireLiveBroker(t)

	exchange := fmt.Sprintf("scenario_exchange_%d", time.Now().UnixNano())

	err := MqPublish(exchange, "webhook_process", map[string]string{"webhook_row_id": "row-0"})

	assert.NoError(t, err, "documents the silent drop: the broker discards the message and confirms it")
}

// TestMqScenarioRoutablePublishStillSucceeds pins the other half: once the
// consumer has declared its queue, the same publish delivers normally and the
// caller sees no error (and pays only the grace window).
func TestMqScenarioRoutablePublishStillSucceeds(t *testing.T) {
	requireLiveBroker(t)

	exchange := fmt.Sprintf("scenario_exchange_%d", time.Now().UnixNano())
	queue := fmt.Sprintf("scenario_queue_%d", time.Now().UnixNano())

	delivered := make(chan string, 1)
	require.NoError(t, MqConsume(exchange, queue, queue, 1, func(_ context.Context, data []byte) error {
		delivered <- string(data)
		return nil
	}))
	waitForQueueDeclared(t, queue)

	require.NoError(t, MqPublish(exchange, queue, map[string]string{"webhook_row_id": "row-2"},
		MqPublishWithMandatory()))

	select {
	case body := <-delivered:
		assert.Contains(t, body, "row-2")
	case <-time.After(10 * time.Second):
		t.Fatal("message was never delivered to the consumer")
	}
}

// TestMqScenarioConsumerSurvivesBrokerDownAtStartup covers the second half of
// the outage: the worker pod restarted while the broker was being recreated.
// MqConsume used to return ErrRbmqNoConn and never register the consumer again,
// which is why the queue stayed missing for hours.
func TestMqScenarioConsumerSurvivesBrokerDownAtStartup(t *testing.T) {
	// Point at a port nothing is listening on — the pod-starts-before-broker case.
	config.Config.RabbitMqHost = "127.0.0.1"
	config.Config.RabbitMqPort = 1 // reserved, refuses connections
	config.Config.RabbitMqUsername = "guest"
	config.Config.RabbitMqPassword = "guest"
	config.Config.ServerRole = config.ServerRoleWorker
	MqClose()
	t.Cleanup(MqClose)

	err := MqConsume("scenario_dead_exchange", "scenario_dead_queue", "scenario_dead_queue", 1,
		func(_ context.Context, _ []byte) error { return nil })

	assert.NoError(t, err, "a broker that is down at startup must not kill the consumer permanently")
}

// waitForQueueDeclared waits until the consumer goroutine has registered, which
// is when its queue and binding exist on the broker.
func waitForQueueDeclared(t *testing.T, queue string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		rbmqConsumersMux.Lock()
		_, ok := rbmqConsumers[queue]
		rbmqConsumersMux.Unlock()
		if ok {
			time.Sleep(200 * time.Millisecond) // let the binding settle
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("consumer for %s never registered", queue)
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
