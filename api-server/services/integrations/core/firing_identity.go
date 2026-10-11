package core

import (
	"database/sql"
	"errors"
	"strconv"
	"time"

	"nudgebee/services/internal/database"
)

// FiringKeyFromStart is the FiringKey for sources whose payload carries the time
// the current firing began (Alertmanager-style startsAt): the same on every repeat
// notification of one firing, new each time the alert fires again after resolving.
// It returns "" when either input is missing, which leaves the delivery on the
// parser's EventId alone — an empty fingerprint would otherwise compose "-<ts>" and
// merge every such alert that started in the same second into one row.
func FiringKeyFromStart(fingerprint string, startsAt time.Time) string {
	if fingerprint == "" || startsAt.IsZero() {
		return ""
	}
	return strconv.FormatInt(startsAt.Unix(), 10)
}

// FiringFindingID is the finding_id of one firing: the alert's stable id plus the
// source's per-firing key. It is the only place this format is defined in
// api-server. The cloud-collector builds the same shape in its own module
// (providers.Event.FiringFindingID); keep the two in step.
func FiringFindingID(eventId, firingKey string) string {
	if firingKey == "" {
		return eventId
	}
	return eventId + "-" + firingKey
}

// firingRowQuery finds the events row a firing delivery belongs to, if one exists:
// the alert's open row, or else the closed row of this same firing.
//
// The open row wins even when its start differs from the delivery's. startsAt is not
// perfectly stable within one firing — Prometheus resets it when a rule re-enters
// firing after a restart, and Alertmanager does not persist alerts — so a changed
// start with no resolve in between is far more often the same firing than a new one.
// Folding it into the open row matches what these sources did before per-firing ids.
//
// The closed-row arm matches on the start second and is what keeps a close sticky:
// a late repeat of a firing that has already been resolved, or closed by hand while
// still firing, lands on that row and only updates it. It also catches rows written
// before per-firing ids existed, whose finding_id is the bare EventId.
//
// starts_at is `timestamp without time zone`, and lib/pq drops the offset when it
// writes one, so the column holds the payload's wall-clock. The range is therefore
// bound to the same time.Time the insert writes, never rebuilt from the unix key.
const firingRowQuery = `SELECT finding_id FROM events
	WHERE fingerprint = $1 AND cloud_account_id = $2 AND tenant = $3 AND source = $4
	  AND (status = 'FIRING' OR (starts_at >= $5 AND starts_at < $6))
	ORDER BY (status = 'FIRING') DESC, starts_at DESC
	LIMIT 1`

// firingFindingIDFor returns the finding_id a firing delivery should be stored
// under. A delivery without a FiringKey keeps its EventId, as before. Otherwise it
// reuses the row firingRowQuery finds, and mints FiringFindingID when there is none:
// the alert's first firing, or a firing that began after its last row closed.
//
// A failed lookup mints rather than falling back to the bare EventId. Rows for
// these sources are keyed by the minted id, so the minted id is the one that finds
// the current firing's row; the bare EventId would open a duplicate beside it.
func firingFindingIDFor(dbms *database.DatabaseManager, tenantId, accountId, source string, e EventIncomingWebhook) (string, error) {
	if e.FiringKey == "" {
		return e.EventId, nil
	}
	minted := FiringFindingID(e.EventId, e.FiringKey)
	start := e.EventCreatedAt.Truncate(time.Second)
	var findingId string
	err := dbms.Db.QueryRowx(firingRowQuery, e.Investigation.Fingerprint, accountId, tenantId, source, start, start.Add(time.Second)).Scan(&findingId)
	switch {
	case err == nil:
		return findingId, nil
	case errors.Is(err, sql.ErrNoRows):
		return minted, nil
	default:
		return minted, err
	}
}
