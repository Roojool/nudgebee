package watch

import (
	"context"
	"log/slog"
	"nudgebee/llm/config"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Watch terminal-path observability. The terminate flow has three side-effecting
// stages after MarkTerminal commits — summarizer, responder, notifier — and
// each can fail silently with only a logged error. These counters give
// on-call a quantitative signal so a dead notifications-server, a database
// hiccup, or a hung LLM does not turn into invisible silent notification
// drops. Cardinality is bounded: a single `outcome` attribute per counter.

var (
	watchMeter metric.Meter

	metricsWatchTerminalTransitions metric.Int64Counter
	metricsWatchLostRace            metric.Int64Counter
	metricsWatchSummarizerFailures  metric.Int64Counter
	metricsWatchResponderFailures   metric.Int64Counter
	metricsWatchNotifierFailures    metric.Int64Counter
	metricsWatchPollFailures        metric.Int64Counter

	watchMetricsOnce sync.Once
)

// InitMetrics is idempotent; called from any path that wants to emit a
// watch metric so we do not depend on init order.
func InitMetrics() {
	watchMetricsOnce.Do(func() {
		watchMeter = otel.Meter(config.SERVICE_NAME)

		var err error
		metricsWatchTerminalTransitions, err = watchMeter.Int64Counter(
			"nb_llm_watch_terminal_transitions",
			metric.WithDescription("Watch terminal transitions, labelled by status"),
		)
		if err != nil {
			slog.Error("watch metrics: failed to create nb_llm_watch_terminal_transitions", "error", err)
		}

		metricsWatchLostRace, err = watchMeter.Int64Counter(
			"nb_llm_watch_lost_race",
			metric.WithDescription("MarkTerminal matched 0 rows — another transition (e.g. Cancel) committed first; summarizer/responder/notifier skipped"),
		)
		if err != nil {
			slog.Error("watch metrics: failed to create nb_llm_watch_lost_race", "error", err)
		}

		metricsWatchSummarizerFailures, err = watchMeter.Int64Counter(
			"nb_llm_watch_summarizer_failures",
			metric.WithDescription("Summarizer call failed; responder fell back to raw predicate summary"),
		)
		if err != nil {
			slog.Error("watch metrics: failed to create nb_llm_watch_summarizer_failures", "error", err)
		}

		metricsWatchResponderFailures, err = watchMeter.Int64Counter(
			"nb_llm_watch_responder_failures",
			metric.WithDescription("Responder failed to append the terminal block to the conversation"),
		)
		if err != nil {
			slog.Error("watch metrics: failed to create nb_llm_watch_responder_failures", "error", err)
		}

		metricsWatchNotifierFailures, err = watchMeter.Int64Counter(
			"nb_llm_watch_notifier_failures",
			metric.WithDescription("Notifier failed to deliver the terminal notification (Slack/email). User chip will not update without retry."),
		)
		if err != nil {
			slog.Error("watch metrics: failed to create nb_llm_watch_notifier_failures", "error", err)
		}

		// Poll failures had no counter at all, despite being the dominant
		// real failure class: every FAILED row in the dev metastore is an
		// "observe failed: ..." — missing tool config, workspace exec
		// failure, expired GitHub App token. Without this, a tenant whose
		// watches all die on a broken integration is invisible until someone
		// reads the rows by hand.
		metricsWatchPollFailures, err = watchMeter.Int64Counter(
			"nb_llm_watch_poll_failures",
			metric.WithDescription("A watch poll failed (source observe or predicate eval). terminal=true means the consecutive-failure budget was spent and the watch moved to FAILED."),
		)
		if err != nil {
			slog.Error("watch metrics: failed to create nb_llm_watch_poll_failures", "error", err)
		}
	})
}

// recordTerminalTransition is called once per successful MarkTerminal where rows > 0.
func recordTerminalTransition(ctx context.Context, status Status) {
	InitMetrics()
	if metricsWatchTerminalTransitions == nil {
		return
	}
	metricsWatchTerminalTransitions.Add(ctx, 1, metric.WithAttributes(
		attribute.String("status", string(status)),
	))
}

// recordLostRace is called when MarkTerminal returns rows == 0 — another
// terminal write (typically Cancel) committed first and the executor must
// skip the post-terminal side effects. Alerting on a sustained non-zero
// rate helps catch races that don't fall under the documented Cancel path.
func recordLostRace(ctx context.Context, status Status) {
	InitMetrics()
	if metricsWatchLostRace == nil {
		return
	}
	metricsWatchLostRace.Add(ctx, 1, metric.WithAttributes(
		attribute.String("attempted_status", string(status)),
	))
}

func recordSummarizerFailure(ctx context.Context, reason string) {
	InitMetrics()
	if metricsWatchSummarizerFailures == nil {
		return
	}
	metricsWatchSummarizerFailures.Add(ctx, 1, metric.WithAttributes(
		attribute.String("reason", reason),
	))
}

func recordResponderFailure(ctx context.Context) {
	InitMetrics()
	if metricsWatchResponderFailures == nil {
		return
	}
	metricsWatchResponderFailures.Add(ctx, 1)
}

// recordPollFailure is called on every failed poll, whether it is being
// retried with backoff or is the one that exhausts WatchMaxFailures.
// phase distinguishes a source error from a predicate error so the two
// (very different) remediations are separable on a dashboard.
func recordPollFailure(ctx context.Context, phase string, terminal bool) {
	InitMetrics()
	if metricsWatchPollFailures == nil {
		return
	}
	metricsWatchPollFailures.Add(ctx, 1, metric.WithAttributes(
		attribute.String("phase", phase),
		attribute.Bool("terminal", terminal),
	))
}

func recordNotifierFailure(ctx context.Context, status Status) {
	InitMetrics()
	if metricsWatchNotifierFailures == nil {
		return
	}
	metricsWatchNotifierFailures.Add(ctx, 1, metric.WithAttributes(
		attribute.String("status", string(status)),
	))
}
