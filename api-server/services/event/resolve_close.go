package event

import (
	"context"
	"strings"
	"time"

	"nudgebee/services/common"
	"nudgebee/services/config"
	"nudgebee/services/internal/database"
	"nudgebee/services/security"

	eventtypes "nudgebee/services/event/types"
)

// isCloseStatus reports whether an incoming event asks to close the event it
// names rather than to open or update one.
func isCloseStatus(status EventStatus) bool {
	return strings.EqualFold(string(status), string(EventStatusResolved)) ||
		strings.EqualFold(string(status), string(EventStatusClosed))
}

// resolveEndsAt is the end time an incoming resolve stamps on the events it closes.
func resolveEndsAt(e Event) time.Time {
	if e.EndsAt == nil {
		return eventtypes.ResolveEndsAt(time.Time{})
	}
	return eventtypes.ResolveEndsAt(*e.EndsAt)
}

// closeEventById closes one event by primary key when it belongs to source and is
// still open. It returns the id when the row changed and nothing when the event
// was already CLOSED, so a repeated resolve does not notify twice.
func closeEventById(dbms *database.DatabaseManager, eventId, source string, endsAt time.Time) ([]string, error) {
	return queryClosedIds(dbms, eventtypes.CloseEventByIdQuery, eventId, string(EventStatusClosed), endsAt, source)
}

// closeEventsByFingerprint closes every open event of one fingerprint, source and
// account, and returns the ids it changed.
func closeEventsByFingerprint(dbms *database.DatabaseManager, tenantId, accountId, fingerprint, source string, endsAt time.Time) ([]string, error) {
	return queryClosedIds(dbms, eventtypes.CloseEventsByFingerprintQuery, tenantId, accountId, fingerprint, string(EventStatusClosed), endsAt, source)
}

func queryClosedIds(dbms *database.DatabaseManager, query string, args ...any) ([]string, error) {
	var ids []string
	if err := dbms.Db.Select(&ids, query, args...); err != nil {
		return nil, err
	}
	return ids, nil
}

// resolveByFingerprint handles a resolve that names no firing of its own sender:
// finding_id is empty, or a RESOLVED whose finding_id matches no event or another
// source's event (see resolvesUnmatchedByFingerprint). It
// closes every open event of the fingerprint from the same source and account, and
// never inserts a row — a resolve for something that is not open is a no-op, not a
// new closed event. It returns the first event it closed, or "" when none was open.
func resolveByFingerprint(sc *security.RequestContext, dbms *database.DatabaseManager, tenantId, accountId string, e Event) (string, error) {
	if e.Fingerprint == "" {
		sc.GetLogger().Info("event: resolve matches no open event of its source and carries no fingerprint, ignoring",
			"finding_id", e.FindingId, "source", e.Source, "account_id", accountId)
		return "", nil
	}
	closed, err := closeEventsByFingerprint(dbms, tenantId, accountId, e.Fingerprint, e.Source, resolveEndsAt(e))
	if err != nil {
		sc.GetLogger().Error("event: failed to close events by fingerprint", "error", err, "fingerprint", e.Fingerprint, "source", e.Source)
		return "", err
	}
	sc.GetLogger().Info("event: resolve closed events by fingerprint",
		"fingerprint", e.Fingerprint, "source", e.Source, "account_id", accountId, "closed", len(closed))
	publishResolved(sc.GetContext(), closed)
	if len(closed) == 0 {
		return "", nil
	}
	return closed[0], nil
}

// publishResolved sends the resolved notification for each event a resolve closed.
// A variable so tests can count notifications without a broker.
var publishResolved = func(ctx context.Context, eventIds []string) {
	for _, id := range eventIds {
		_ = common.MqPublish(
			config.Config.RabbitMqEventPostProcessExchange,
			config.Config.RabbitMqEventPostProcessQueue,
			map[string]any{"event_id": id, "notify_resolved": true},
			common.MqPublishWithExpiration(1*time.Hour),
			common.MqPublishWithBackgroundRetry(),
			common.MqPublishWithContext(ctx),
		)
	}
}

// IsResolveByFingerprint reports whether e is a resolve that may omit finding_id:
// it closes every open event of its fingerprint instead of one named firing.
func IsResolveByFingerprint(e Event) bool {
	return isCloseStatus(e.Status) && e.Fingerprint != ""
}

// resolvesUnmatchedByFingerprint reports whether a resolve whose finding_id matches
// no event of its source closes by fingerprint instead of being inserted.
//
// Only RESOLVED does. A CLOSED under a finding_id nobody has seen is a record that
// was born closed, not a recovery: cloud-collector sends CloudTrail, EventBridge
// and Cloud Foundry audit/build/task events that way, each under a fresh finding_id
// and with a fingerprint set, and they must still be stored.
func resolvesUnmatchedByFingerprint(e Event) bool {
	return strings.EqualFold(string(e.Status), string(EventStatusResolved)) && e.Fingerprint != ""
}
