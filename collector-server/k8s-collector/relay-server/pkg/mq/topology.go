// mq/topology.go
package mq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"nudgebee/relay-server/pkg/config"
	"strings"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// TenantEnsurer is here for testability.
type TenantEnsurer interface {
	EnsureTenant(ctx context.Context, id string) error
	EnsureTenantForAgentType(ctx context.Context, id, agentType string) error
}

// Topology manages your primary exchange and per-tenant queues + DLQs.
type Topology struct {
	connMgr     *ConnectionManager
	exchange    string
	dlxExchange string
	declared    sync.Map // queue name → *declaration
	ttlMs       int32    // x-message-ttl in milliseconds

	// consumerProbes caches HasConsumers answers per queue name
	// (queue name → consumerProbe) so the /request hot path does not open a
	// broker channel per call.
	consumerProbes sync.Map
	now            func() time.Time
}

// consumerProbe is one cached HasConsumers answer.
type consumerProbe struct {
	has bool
	at  time.Time
}

// consumerProbeTTL bounds how stale a cached HasConsumers answer may be. Short
// on purpose: it only has to absorb bursts of requests to the same agent, and
// a longer window would keep routing to a queue whose agent just left (or keep
// avoiding one whose agent just arrived) for longer than a caller would wait.
const consumerProbeTTL = 5 * time.Second

// ConsumerProber reports whether a queue currently has at least one consumer.
// It is what lets the publish path prefer a per-agent queue only when the
// owning agent's session is actually consuming it.
type ConsumerProber interface {
	HasConsumers(ctx context.Context, queue string) (bool, error)
}

// NewTopology declares the DLX and primary exchange exactly once.
// No context needed at startup; uses background internally.
func NewTopology(connMgr *ConnectionManager, cfg *config.Config) (*Topology, error) {
	// load TTL from env
	ttl := int32(cfg.RabbitMQ.MessageTTL.Milliseconds())
	exchange := cfg.RabbitMQ.ExchangeName

	dlx := exchange + ".dlx"

	// Create topology first to use retry mechanism
	topology := &Topology{
		connMgr:     connMgr,
		exchange:    exchange,
		dlxExchange: dlx,
		ttlMs:       ttl,
	}

	// one-time exchange declarations with retry
	err := topology.retryRabbitMQOperation(context.Background(), func(ch *amqp.Channel) error {
		if err := ch.ExchangeDeclare(dlx, "direct", true, false, false, false, nil); err != nil {
			return fmt.Errorf("declare DLX %q: %w", dlx, err)
		}
		if err := ch.ExchangeDeclare(exchange, "direct", true, false, false, false, nil); err != nil {
			return fmt.Errorf("declare exchange %q: %w", exchange, err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("topology init: %w", err)
	}

	return topology, nil
}

// EnsureTenant idempotently declares the per-tenant queue + its DLQ for k8s agents.
func (t *Topology) EnsureTenant(ctx context.Context, id string) error {
	return t.EnsureTenantForAgentType(ctx, id, "k8s")
}

// retryRabbitMQOperation retries RabbitMQ operations with exponential backoff
// when encountering connection/channel errors, with proper context cancellation support
func (t *Topology) retryRabbitMQOperation(ctx context.Context, operation func(*amqp.Channel) error) error {
	const maxRetries = 3
	const baseDelay = 100 * time.Millisecond

	for attempt := 0; attempt < maxRetries; attempt++ {
		ch, err := t.connMgr.GetChannel(ctx)
		if err != nil {
			// GetChannel already handles retries internally and only fails when context is cancelled
			// Return immediately as this is a terminal condition
			return fmt.Errorf("failed to get channel: %w", err)
		}
		defer ch.Close() // nolint:errcheck

		err = operation(ch)
		if err == nil {
			return nil
		}

		// Check if it's a connection/channel error that we should retry
		if isRetryableError(err) {
			if attempt == maxRetries-1 {
				return fmt.Errorf("operation failed after %d attempts: %w", maxRetries, err)
			}
			slog.Warn("RabbitMQ operation failed, retrying", "attempt", attempt+1, "err", err)
			if err := t.contextAwareWait(ctx, baseDelay*time.Duration(1<<attempt)); err != nil {
				return err // Context cancelled
			}
			continue
		}

		// Non-retryable error, return immediately
		return err
	}

	return fmt.Errorf("operation failed after %d attempts", maxRetries)
}

// contextAwareWait waits for the specified duration or until context is cancelled
func (t *Topology) contextAwareWait(ctx context.Context, duration time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(duration):
		return nil
	}
}

// isRetryableError checks if an error is retryable (connection/channel issues)
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// Check for AMQP connection/channel errors
	if amqpErr, ok := err.(*amqp.Error); ok {
		switch amqpErr.Code {
		case 406: // PRECONDITION_FAILED
			// Check for message size errors - these are NOT retryable
			if strings.Contains(amqpErr.Reason, "message size") && strings.Contains(amqpErr.Reason, "larger than configured max size") {
				slog.Error("Message too large for RabbitMQ", "reason", amqpErr.Reason, "error_code", amqpErr.Code)
				return false
			}
			// Channel closure due to precondition failures are retryable
			if amqpErr.Reason == "channel/connection is not open" {
				return true
			}
			// TTL/parameter mismatches might be retryable in some cases
			if strings.Contains(amqpErr.Reason, "inequivalent arg") {
				return true
			}
			return false
		case 504: // CHANNEL_ERROR or CONNECTION_FORCED
			return true
		case 320: // CONNECTION_FORCED
			return true
		case 502: // COMMAND_INVALID (can happen during connection issues)
			return true
		}
	}

	// Check for common connection error strings
	errStr := err.Error()
	if errStr == "channel/connection is not open" ||
		errStr == "connection is not open" ||
		errStr == "channel is not open" {
		return true
	}

	// Handle the specific error message format from RabbitMQ
	if amqpErr, ok := err.(*amqp.Error); ok && amqpErr.Reason == "channel/connection is not open" {
		return true
	}

	return false
}

// ForgetTenant drops the "already declared" record for a tenant queue, so the
// next EnsureTenantForAgentType really talks to the broker instead of trusting
// the cache.
//
// The cache assumes the broker still holds what we once declared, which stops
// being true the moment the broker loses its definitions — a RabbitMQ that comes
// back without its queues leaves consumers retrying a queue that no longer
// exists, and nothing re-creates it, because the cache says it is already there.
// Callers that see the broker disagree with the cache must call this first.
func (t *Topology) ForgetTenant(id, agentType string) {
	t.ForgetQueue(RelayQueueName(id, agentType))
}

// ForgetQueue is ForgetTenant for an explicit queue name, used for per-agent
// proxy queues (see RelayAgentQueueName).
func (t *Topology) ForgetQueue(qName string) {
	t.declared.Delete(qName)
}

// declaration is an in-flight or finished attempt to declare one tenant's
// queue. Concurrent callers for the same queue wait on it and share its result,
// rather than the second caller assuming success because the first merely
// started.
//
// err is written before done is closed and read only after, so the close is the
// happens-before edge that makes it safe without a second lock.
type declaration struct {
	done chan struct{}
	err  error
}

// EnsureTenantForAgentType idempotently declares the per-tenant queue for a specific agent type.
// For k8s agents, uses the standard queue name. For other types (e.g. proxy), appends the type suffix.
func (t *Topology) EnsureTenantForAgentType(ctx context.Context, id, agentType string) error {
	return t.EnsureQueue(ctx, RelayQueueName(id, agentType))
}

// EnsureQueue idempotently declares a relay request queue (and its DLQ) by
// explicit name. EnsureTenantForAgentType is the per-account form; this one is
// also used for per-agent proxy queues (see RelayAgentQueueName).
func (t *Topology) EnsureQueue(ctx context.Context, qName string) error {
	return t.ensureQueue(ctx, qName, 0)
}

// agentQueueExpiry is x-expires on per-agent queues and their DLQs: the
// broker deletes them once they have gone this long with no consumer (and no
// redeclare). Per-agent queues are named after an agent id, so without it
// every agent that ever connected — including deleted and re-registered ones
// — would leave two queues behind forever. A live session's consumer keeps
// its queues from expiring, and the next session re-declares them.
const agentQueueExpiry = 24 * time.Hour

// EnsureAgentQueue is EnsureQueue for a per-agent queue (RelayAgentQueueName):
// the queue and its DLQ are declared with x-expires = agentQueueExpiry.
func (t *Topology) EnsureAgentQueue(ctx context.Context, qName string) error {
	return t.ensureQueue(ctx, qName, int32(agentQueueExpiry.Milliseconds()))
}

// ForgetConsumerProbe drops the cached HasConsumers answer for qName. A
// session calls it for its per-agent queue when it ends, so this replica stops
// routing to that queue at once instead of for up to consumerProbeTTL.
func (t *Topology) ForgetConsumerProbe(qName string) {
	t.consumerProbes.Delete(qName)
}

// ensureQueue declares qName (and its DLQ); expiresMs > 0 adds x-expires.
func (t *Topology) ensureQueue(ctx context.Context, qName string, expiresMs int32) error {

	// Keyed on the queue name, so callers for the same tenant collapse onto one
	// declare. This matters because the proxy config-push and resync handlers
	// can run against the same queue a proxy agent is registering on: telling
	// the second caller "declared" while the first is still talking to the
	// broker would let it publish into an exchange with nothing bound yet, and
	// that message is discarded without an error.
	mine := &declaration{done: make(chan struct{})}
	actual, loaded := t.declared.LoadOrStore(qName, mine)
	if loaded {
		theirs := actual.(*declaration)
		select {
		case <-theirs.done:
			return theirs.err
		case <-ctx.Done():
			// Wait on our own deadline, never inherit theirs: the declaring
			// caller may be a long-lived session while we are a short HTTP
			// request, and blocking past our own deadline would be worse than
			// reporting that we could not confirm the queue.
			return ctx.Err()
		}
	}

	// The entry above is staked before the declare actually runs, so a failed
	// declare has to retract it. Leaving it behind marks a queue that does not
	// exist as declared, and every later call short-circuits to success — the
	// tenant is then permanently unroutable until the process restarts.
	mine.err = t.declareTenant(ctx, qName, qName+".dlq", expiresMs)
	if mine.err != nil {
		t.declared.Delete(qName)
	}
	close(mine.done)
	return mine.err
}

// declareTenant declares a relay request queue and its DLQ. expiresMs > 0
// sets x-expires on both. A queue that already exists with different
// arguments (TTL or x-expires) fails the declare with 406 PRECONDITION_FAILED
// and is deleted and re-declared below.
func (t *Topology) declareTenant(ctx context.Context, qName, dlqName string, expiresMs int32) error {
	return t.retryRabbitMQOperation(ctx, func(ch *amqp.Channel) error {
		// 1) DLQ with TTL
		dlqArgs := amqp.Table{
			"x-message-ttl": t.ttlMs,
		}
		if expiresMs > 0 {
			dlqArgs["x-expires"] = expiresMs
		}
		_, err := ch.QueueDeclare(dlqName, true, false, false, false, dlqArgs)
		if err != nil {
			slog.Warn("DLQ declare failed", "dlq_name", dlqName, "err", err)
			amqpErr, ok := err.(*amqp.Error)
			if !ok || amqpErr.Code != 406 {
				return fmt.Errorf("declare DLQ %q: %w", dlqName, err)
			}
			freshCh, err := t.connMgr.GetChannel(ctx)
			if err != nil {
				return fmt.Errorf("failed to get fresh channel for DLQ operations: %w", err)
			}
			defer freshCh.Close() // nolint:errcheck

			slog.Info("DLQ TTL mismatch detected, attempting delete and recreate", "dlq_name", dlqName, "error_code", amqpErr.Code)
			if _, err := freshCh.QueueDelete(dlqName, false, false, false); err != nil {
				return fmt.Errorf("delete DLQ %q: %w", dlqName, err)
			}
			_, err = freshCh.QueueDeclare(dlqName, true, false, false, false, dlqArgs)
			if err != nil {
				return fmt.Errorf("redeclare DLQ %q with TTL: %w", dlqName, err)
			}
		}

		if err := ch.QueueBind(dlqName, qName, t.dlxExchange, false, nil); err != nil {
			return fmt.Errorf("bind DLQ %q→%q: %w", dlqName, t.dlxExchange, err)
		}

		// 2) Main queue with per-queue TTL + DLX
		args := amqp.Table{
			"x-message-ttl":             t.ttlMs,
			"x-dead-letter-exchange":    t.dlxExchange,
			"x-dead-letter-routing-key": qName,
		}
		if expiresMs > 0 {
			args["x-expires"] = expiresMs
		}
		if _, err := ch.QueueDeclare(qName, true, false, false, false, args); err != nil {
			slog.Warn("Queue declare failed", "queue_name", qName, "err", err)
			amqpErr, ok := err.(*amqp.Error)
			if !ok || amqpErr.Code != 406 {
				return fmt.Errorf("declare queue %q: %w", qName, err)
			}
			freshCh, err := t.connMgr.GetChannel(ctx)
			if err != nil {
				return fmt.Errorf("failed to get fresh channel for queue operations: %w", err)
			}
			defer freshCh.Close() // nolint:errcheck

			if _, err := freshCh.QueueDelete(qName, false, false, false); err != nil {
				return fmt.Errorf("delete queue %q: %w", qName, err)
			}
			_, err = freshCh.QueueDeclare(qName, true, false, false, false, args)
			if err != nil {
				return fmt.Errorf("redeclare queue %q with TTL: %w", qName, err)
			}
		}
		if err := ch.QueueBind(qName, qName, t.exchange, false, nil); err != nil {
			return fmt.Errorf("bind queue %q→%q: %w", qName, t.exchange, err)
		}

		return nil
	})
}

// HasConsumers reports whether qName currently has at least one consumer
// attached on the broker. A queue that does not exist has none. Answers are
// cached for consumerProbeTTL.
//
// It is a passive declare, so it never creates the queue: probing must not
// leave behind a queue that nobody will ever consume.
func (t *Topology) HasConsumers(ctx context.Context, qName string) (bool, error) {
	now := time.Now
	if t.now != nil {
		now = t.now
	}
	if v, ok := t.consumerProbes.Load(qName); ok {
		p := v.(consumerProbe)
		if now().Sub(p.at) < consumerProbeTTL {
			return p.has, nil
		}
	}

	var consumers int
	err := t.retryRabbitMQOperation(ctx, func(ch *amqp.Channel) error {
		q, err := ch.QueueDeclarePassive(qName, true, false, false, false, nil)
		if err != nil {
			return err
		}
		consumers = q.Consumers
		return nil
	})
	if err != nil {
		var amqpErr *amqp.Error
		if errors.As(err, &amqpErr) && amqpErr.Code == amqp.NotFound {
			t.consumerProbes.Store(qName, consumerProbe{has: false, at: now()})
			return false, nil
		}
		return false, err
	}
	has := consumers > 0
	t.consumerProbes.Store(qName, consumerProbe{has: has, at: now()})
	return has, nil
}

// RelayAgentQueueName is the request queue of one specific agent.
//
// Proxy agents each get their own queue, relay_requests_{id}_proxy_{agentID},
// because an account may run several of them with disjoint datasources, and a
// request consumed from the shared per-account queue could land on an agent
// that does not have the addressed datasource. Every other agent type (and a
// proxy agent whose id is unknown) keeps the per-account RelayQueueName.
func RelayAgentQueueName(id, agentType, agentID string) string {
	if agentType != "proxy" || agentID == "" {
		return RelayQueueName(id, agentType)
	}
	return RelayQueueName(id, agentType) + "_" + agentID
}

// RelayQueueName builds the per-tenant queue name, with optional agent type suffix.
// k8s agents use "relay_requests_{id}", proxy agents use "relay_requests_{id}_proxy".
func RelayQueueName(id, agentType string) string {
	if agentType == "" || agentType == "k8s" {
		return "relay_requests_" + id
	}
	return "relay_requests_" + id + "_" + agentType
}
