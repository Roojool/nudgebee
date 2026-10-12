package common

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"nudgebee/collector/cloud/config"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

var (
	redisClient *redis.Client
	redisMu     sync.Mutex
)

// ErrLockTimeout is returned by AcquireSyncLockBlocking when the lock cannot be
// acquired within the caller-supplied deadline. Callers should treat it as a
// transient failure (retry via MQ with a real backoff) rather than dropping work.
var ErrLockTimeout = errors.New("sync lock acquisition timed out")

const (
	syncLockPrefix = "cloud-collector:sync-lock:"
	// The holder renews the lock every syncLockRenewInterval for as long as it
	// runs (see TryAcquireSyncLock), so the TTL only bounds how long a holder
	// that died without releasing (pod killed) blocks the account.
	syncLockTTL           = 2 * time.Minute
	syncLockRenewInterval = 30 * time.Second
	redisInitRetries      = 3
	redisInitInterval     = 2 * time.Second
)

// releaseLockScript atomically deletes the key only if the stored value matches
// the token. This prevents one process from releasing another's lock.
var releaseLockScript = redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
	return redis.call("del", KEYS[1])
end
return 0
`)

// renewLockScript extends the TTL only if the stored value still matches the
// token, so a holder never revives a lock that expired and was taken by another.
var renewLockScript = redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
	return redis.call("pexpire", KEYS[1], ARGV[2])
end
return 0
`)

// GetRedisClient returns the Redis client, attempting to connect if not already connected.
// Uses lazy initialization with reconnection — if Redis was down at startup but recovers,
// subsequent calls will reconnect.
func GetRedisClient() *redis.Client {
	redisMu.Lock()
	defer redisMu.Unlock()

	if redisClient != nil {
		return redisClient
	}

	addr := fmt.Sprintf("%s:%d", config.Config.RedisServerHost, config.Config.RedisServerPort)

	for attempt := 1; attempt <= redisInitRetries; attempt++ {
		client := redis.NewClient(&redis.Options{
			Addr:     addr,
			Username: config.Config.RedisUserName,
			Password: config.Config.RedisUserPassword,
		})
		if err := client.Ping(context.Background()).Err(); err != nil {
			slog.Warn("redis: connection attempt failed", "addr", addr, "attempt", attempt, "max_attempts", redisInitRetries, "error", err)
			_ = client.Close()
			if attempt < redisInitRetries {
				time.Sleep(redisInitInterval)
			}
			continue
		}
		slog.Info("redis: connected", "addr", addr)
		redisClient = client
		return redisClient
	}

	slog.Error("redis: all connection attempts failed, distributed locks unavailable", "addr", addr)
	return nil
}

// ResetRedisClient clears the cached client so the next GetRedisClient call
// will attempt to reconnect. Called when a transient Redis error is detected.
func ResetRedisClient() {
	redisMu.Lock()
	defer redisMu.Unlock()
	if redisClient != nil {
		_ = redisClient.Close()
		redisClient = nil
	}
}

// TryAcquireSyncLock attempts to acquire a distributed lock for the given account.
// Returns:
//   - (true, releaseFn, nil) — lock acquired
//   - (false, nil, nil)      — lock held by another process (caller should skip)
//   - (false, nil, error)    — Redis unavailable (caller should treat as error)
func TryAcquireSyncLock(ctx context.Context, accountId string) (acquired bool, release func(context.Context), err error) {
	client := GetRedisClient()
	if client == nil {
		return false, nil, fmt.Errorf("redis unavailable, cannot acquire sync lock for account %s", accountId)
	}

	key := syncLockPrefix + accountId
	token := uuid.New().String()

	res, err := client.SetArgs(ctx, key, token, redis.SetArgs{
		Mode: "NX",
		TTL:  syncLockTTL,
	}).Result()
	if err == redis.Nil {
		// NX failed — key already exists, lock held by another instance
		return false, nil, nil
	}
	if err != nil {
		slog.Error("redis: failed to acquire sync lock", "accountId", accountId, "error", err)
		return false, nil, fmt.Errorf("redis error acquiring sync lock: %w", err)
	}
	if res != "OK" {
		return false, nil, nil
	}
	stopRenew := make(chan struct{})
	go renewSyncLock(key, token, accountId, stopRenew)
	var releaseOnce sync.Once
	return true, func(ctx context.Context) {
		releaseOnce.Do(func() { close(stopRenew) })
		// Reacquire a live client for release — the original client may have been
		// reset by another goroutine's transient error handling.
		releaseClient := GetRedisClient()
		if releaseClient == nil {
			slog.Warn("redis: unavailable during lock release, lock will expire via TTL", "accountId", accountId)
			return
		}
		// Atomically delete only if we still own the lock (token matches).
		_, err := releaseLockScript.Run(ctx, releaseClient, []string{key}, token).Result()
		if err != nil {
			slog.Warn("redis: failed to release sync lock", "accountId", accountId, "error", err)
		}
	}, nil
}

// renewSyncLock keeps a held sync lock alive until stop is closed. A cost sync
// of a large bill runs longer than any fixed TTL; without renewal the lock
// expired mid-run and a second sync or the post-report job could run
// concurrently on the same account. It stops early if the lock is no longer
// ours (expired during a Redis outage and taken by another holder).
func renewSyncLock(key, token, accountId string, stop <-chan struct{}) {
	ticker := time.NewTicker(syncLockRenewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		client := GetRedisClient()
		if client == nil {
			slog.Warn("redis: unavailable while renewing sync lock, retrying", "accountId", accountId)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		renewed, err := renewLockScript.Run(ctx, client, []string{key}, token, syncLockTTL.Milliseconds()).Int()
		cancel()
		if err != nil {
			slog.Warn("redis: failed to renew sync lock, retrying", "accountId", accountId, "error", err)
			continue
		}
		if renewed == 0 {
			slog.Warn("redis: sync lock lost before release, no longer renewing", "accountId", accountId)
			return
		}
	}
}

// AcquireSyncLockBlocking polls TryAcquireSyncLock until the lock is acquired,
// the caller's context is cancelled, or the timeout elapses. The poll interval
// uses bounded exponential backoff (200ms → 2s) so contention is resolved
// quickly without busy-looping Redis.
//
// Use this instead of TryAcquireSyncLock when the caller's correctness depends
// on running serially against another holder (e.g. an MQ consumer that would
// otherwise drop work after exhausting retry budget). Use TryAcquireSyncLock
// when "already running, skip" is the desired behavior (e.g. cron coalescing).
func AcquireSyncLockBlocking(ctx context.Context, accountId string, timeout time.Duration) (release func(context.Context), err error) {
	deadline := time.Now().Add(timeout)
	backoff := 200 * time.Millisecond
	const maxBackoff = 2 * time.Second
	for {
		acquired, releaseFn, lockErr := TryAcquireSyncLock(ctx, accountId)
		if lockErr != nil {
			return nil, lockErr
		}
		if acquired {
			return releaseFn, nil
		}
		if time.Now().After(deadline) {
			return nil, ErrLockTimeout
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}
