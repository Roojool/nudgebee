package mq

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// LLMCacheInvalidator asks every llm-server replica to drop its
// integration-derived caches for the given accounts.
type LLMCacheInvalidator interface {
	InvalidateAccounts(accountIDs ...string)
}

// LLMCacheInvalidationPayload is the wire format on the llm cache invalidation
// exchange. It MUST stay in sync with llm-server's consumer
// (llm/llm-server/api/cache_invalidation_mq.go, cacheInvalidationMessage) and
// api-server's publisher (api-server/services/llm/cache.go).
type LLMCacheInvalidationPayload struct {
	AccountIDs []string `json:"account_ids"`
}

// llmCacheInvalidationTimeout bounds one best-effort publish. The publish runs
// off the caller's path, so this only limits how long a broker outage can keep
// a goroutine around.
const llmCacheInvalidationTimeout = 10 * time.Second

// amqpLLMCacheInvalidator publishes to the fanout exchange llm-server
// consumes.
//
// Without it, integrations the relay registers from a proxy agent's inventory
// (and label changes from its metadata) only reach Nubi after llm-server's
// per-cache TTL, because the relay was the one writer to those rows that never
// said so.
type amqpLLMCacheInvalidator struct {
	connMgr  *ConnectionManager
	exchange string
	logger   *slog.Logger

	// declared records that the exchange has been declared on this broker.
	// Reset on any publish failure, so a broker that lost its definitions is
	// re-declared on the next attempt.
	declared atomic.Bool
}

// NewLLMCacheInvalidator returns an invalidator publishing to exchange. An
// empty exchange name disables invalidation (a no-op invalidator), matching
// api-server's behaviour when the exchange is unset.
func NewLLMCacheInvalidator(connMgr *ConnectionManager, exchange string, logger *slog.Logger) LLMCacheInvalidator {
	if exchange == "" || connMgr == nil {
		if logger != nil {
			logger.Info("llm cache invalidation disabled: no exchange configured")
		}
		return noopLLMCacheInvalidator{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &amqpLLMCacheInvalidator{connMgr: connMgr, exchange: exchange, logger: logger}
}

// InvalidateAccounts publishes asynchronously and never blocks or fails the
// caller: a missed invalidation only means waiting for the TTL, which is
// exactly the behaviour before this existed.
func (i *amqpLLMCacheInvalidator) InvalidateAccounts(accountIDs ...string) {
	ids := dedupeNonEmpty(accountIDs)
	if len(ids) == 0 {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				i.logger.Error("llm cache invalidation panicked — recovered", "panic", r, "account_ids", ids)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), llmCacheInvalidationTimeout)
		defer cancel()
		if err := i.publish(ctx, ids); err != nil {
			i.declared.Store(false)
			i.logger.Error("failed to publish llm cache invalidation (best-effort)",
				"exchange", i.exchange, "account_ids", ids, "err", err)
			return
		}
		i.logger.Info("llm cache invalidation published", "exchange", i.exchange, "account_ids", ids)
	}()
}

func (i *amqpLLMCacheInvalidator) publish(ctx context.Context, ids []string) error {
	body, err := json.Marshal(LLMCacheInvalidationPayload{AccountIDs: ids})
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	ch, err := i.connMgr.GetChannel(ctx)
	if err != nil {
		return fmt.Errorf("get channel: %w", err)
	}
	defer ch.Close() // nolint:errcheck

	if !i.declared.Load() {
		// Same arguments as llm-server's consumer and api-server's publisher
		// (durable fanout, not auto-deleted); a mismatch would make the
		// broker reject the declare and close the channel.
		if err := ch.ExchangeDeclare(i.exchange, "fanout", true, false, false, false, nil); err != nil {
			return fmt.Errorf("declare exchange %q: %w", i.exchange, err)
		}
		i.declared.Store(true)
	}

	return ch.PublishWithContext(ctx, i.exchange, "", false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Transient,
		Timestamp:    time.Now(),
		Body:         body,
	})
}

func dedupeNonEmpty(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

type noopLLMCacheInvalidator struct{}

func (noopLLMCacheInvalidator) InvalidateAccounts(...string) {}
