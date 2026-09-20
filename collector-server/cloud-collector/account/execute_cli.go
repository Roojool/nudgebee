package account

import (
	"fmt"
	"nudgebee/collector/cloud/config"
	"nudgebee/collector/cloud/providers"
	"nudgebee/collector/cloud/security"
	"sync"
	"time"
)

var (
	cliSlotsMu sync.Mutex
	cliSlots   chan struct{}
)

func commandExecutionSlots() chan struct{} {
	maxConcurrent := config.Config.CloudCollectorCliMaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 8
	}
	cliSlotsMu.Lock()
	defer cliSlotsMu.Unlock()
	// Configuration is loaded at process startup. Never replace a live
	// semaphore: callers holding the old channel and callers using the new one
	// could otherwise exceed both limits during a resize.
	if cliSlots == nil {
		cliSlots = make(chan struct{}, maxConcurrent)
	}
	return cliSlots
}

// ExecuteCliCommandV2 runs one provider CLI process with bounded
// concurrency. Waiting is cancelable so saturated command traffic cannot pin
// request goroutines past their deadline.
func ExecuteCliCommandV2(ctx *security.RequestContext, accountId string, command string) (providers.CliExecutionResult, error) {
	account, providerName, err := getAccount(ctx, accountId)
	if err != nil {
		return providers.CliExecutionResult{}, err
	}
	provider, ok := providers.GetProvider(providerName)
	if !ok {
		return providers.CliExecutionResult{}, fmt.Errorf("provider not found")
	}
	executor, ok := provider.(providers.CliV2Executor)
	if !ok {
		return providers.CliExecutionResult{}, fmt.Errorf("provider %s does not support CLI v2 execution", providerName)
	}

	slots := commandExecutionSlots()
	queuedAt := time.Now()
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-ctx.GetContext().Done():
		return providers.CliExecutionResult{}, fmt.Errorf("cloud CLI executor unavailable: %w", ctx.GetContext().Err())
	}
	queueDuration := time.Since(queuedAt)
	startedAt := time.Now()
	result, err := executor.ExecuteCliCommandV2(ctx, account, command)
	status := "ok"
	if err != nil || result.ExitCode != 0 {
		status = "error"
	}
	ctx.GetLogger().Info("CLI v2 command completed",
		"verb", CommandVerb(command),
		"provider", providerName,
		"accountId", accountId,
		"status", status,
		"exit_code", result.ExitCode,
		"timed_out", result.TimedOut,
		"queue_ms", queueDuration.Milliseconds(),
		"duration_ms", time.Since(startedAt).Milliseconds())
	return result, err
}

func ExecuteCliCommand(ctx *security.RequestContext, accountId string, command string) (string, error) {
	account, provider, err := getAccount(ctx, accountId)
	if err != nil {
		ctx.GetLogger().Error("unable to fetch account", "error", err, "accountId", accountId)
		return "", err
	}
	cloudProvider, ok := providers.GetProvider(provider)
	if !ok {
		return "", fmt.Errorf("provider not found")
	}

	// Every CLI execution is a fresh interpreter process (plus a re-auth on GCP
	// and Azure), so this route dominates cloud-collector CPU. Log the verb — not
	// the command, which carries ARNs and secrets — so the per-command split is
	// measurable. Both /execute_cli and /execute_cli_batch land here; only the
	// batch path writes an audit record, so this is the sole record of the
	// single-command traffic.
	start := time.Now()
	response, err := cloudProvider.ExecuteCliCommand(ctx, account, command)
	status := "ok"
	if err != nil {
		status = "error"
	}
	ctx.GetLogger().Info("execute_cli command completed",
		"verb", CommandVerb(command),
		"provider", provider,
		"accountId", accountId,
		"status", status,
		"duration_ms", time.Since(start).Milliseconds())

	return response, err
}
