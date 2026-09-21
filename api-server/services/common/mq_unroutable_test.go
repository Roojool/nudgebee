package common

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// The registry is what makes MqPublishWithMandatory able to tell a delivered
// message from one the broker threw away: basic.return arrives on a separate
// goroutine, so the publish correlates it by message id instead of ordering.

func resetReturnedIDs(t *testing.T) {
	t.Helper()
	mqReturnedMux.Lock()
	mqReturnedIDs = make(map[string]time.Time)
	mqReturnedPruned = time.Time{} // let the next record prune
	mqReturnedMux.Unlock()
}

func TestTakeReturnedMessageConsumesTheRecord(t *testing.T) {
	resetReturnedIDs(t)

	recordReturnedMessage("msg-1")

	assert.True(t, takeReturnedMessage("msg-1"), "first take should see the return")
	assert.False(t, takeReturnedMessage("msg-1"), "a return belongs to exactly one publish")
	assert.False(t, takeReturnedMessage("other"), "unrelated ids are not returned")
}

func TestRecordReturnedMessagePrunesExpiredEntries(t *testing.T) {
	resetReturnedIDs(t)

	mqReturnedMux.Lock()
	mqReturnedIDs["stale"] = time.Now().Add(-mqReturnRetention - time.Minute)
	mqReturnedMux.Unlock()

	recordReturnedMessage("fresh")

	assert.False(t, takeReturnedMessage("stale"), "expired returns must not accumulate")
	assert.True(t, takeReturnedMessage("fresh"))
}

// Pruning is rate-limited because a consumer outage returns every message, and
// scanning the map on each one would make the publish path quadratic exactly
// when it is busiest.
func TestRecordReturnedMessageDoesNotPruneOnEveryWrite(t *testing.T) {
	resetReturnedIDs(t)

	recordReturnedMessage("first") // this one prunes and arms the interval
	mqReturnedMux.Lock()
	mqReturnedIDs["stale"] = time.Now().Add(-mqReturnRetention - time.Minute)
	mqReturnedMux.Unlock()

	recordReturnedMessage("second")

	assert.True(t, takeReturnedMessage("stale"), "a write inside the prune interval must not scan the map")
}

func TestRecordReturnedMessageIgnoresEmptyID(t *testing.T) {
	resetReturnedIDs(t)

	recordReturnedMessage("")

	assert.False(t, takeReturnedMessage(""))
}

func TestWaitForReturnPicksUpALateReturn(t *testing.T) {
	resetReturnedIDs(t)

	// The return landing after the confirm is the race the grace window exists
	// for — without the wait, the publish reports success on a dropped message.
	go func() {
		time.Sleep(20 * time.Millisecond)
		recordReturnedMessage("late")
	}()

	assert.True(t, waitForReturn("late"))
}

func TestWaitForReturnGivesUpAfterGrace(t *testing.T) {
	resetReturnedIDs(t)

	start := time.Now()
	returned := waitForReturn("never-returned")
	elapsed := time.Since(start)

	assert.False(t, returned)
	assert.GreaterOrEqual(t, elapsed, mqReturnGrace, "must wait the full grace before declaring success")
	assert.Less(t, elapsed, mqReturnGrace*4, "must not block the publish much beyond the grace")
}

// A consumer whose first dial fails now retries forever instead of giving up,
// so the backoff is what keeps eight consumers from flooding the log.
func TestConsumerReconnectDelayBacksOffAndCaps(t *testing.T) {
	assert.Equal(t, reconnectTimeDelay, consumerReconnectDelay(1))
	assert.Equal(t, 2*reconnectTimeDelay, consumerReconnectDelay(2))
	assert.Equal(t, 4*reconnectTimeDelay, consumerReconnectDelay(3))
	assert.Equal(t, maxConsumerReconnectDelay, consumerReconnectDelay(50))
	assert.LessOrEqual(t, consumerReconnectDelay(1000), maxConsumerReconnectDelay)
}

func TestShouldLogConsumerRetryThrottles(t *testing.T) {
	assert.True(t, shouldLogConsumerRetry(1))
	assert.True(t, shouldLogConsumerRetry(2))
	assert.False(t, shouldLogConsumerRetry(4))
	assert.True(t, shouldLogConsumerRetry(10))
	assert.False(t, shouldLogConsumerRetry(11))
}
