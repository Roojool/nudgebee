package adapter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"nudgebee/services/internal/database"
	"nudgebee/services/llm"
	"nudgebee/services/security"
)

// A followup run is a goroutine that polls an llm-server conversation; the
// agent itself runs in llm-server and the workspace pod. Anything that replaces
// the services-server pod mid-run (rolling deploy, OOM, eviction) drops the
// goroutine — and with it the finalize that returns the pr_followup row from
// 'addressing' — while the agent carries on and may push to the PR. Recovery
// used to be the 6h cron after a lease measured from last_pr_check_at, which
// every mid-run signal also advances (#38860).
//
// A run now leaves three columns on its pr_followup row:
//
//	run_token            minted when the run is claimed, cleared when it
//	                     finalizes. finalize only applies while the token still
//	                     matches, so a second poller on the same run (or a poller
//	                     that lost the row) cannot apply an outcome twice.
//	run_conversation_id  the conversation being polled, saved once submitted.
//	run_heartbeat_at     refreshed while a poller is alive.
//
// A run whose heartbeat has gone stale is dead. If it had submitted a
// conversation, another pod re-attaches to that same conversation and finalizes
// it normally — no second agent is started next to the one still running. If it
// died before submitting, nothing exists to attach to, and the row is released
// for a fresh dispatch.
const (
	// followupRunHeartbeatInterval is how often a live poller stamps the row.
	followupRunHeartbeatInterval = 60 * time.Second

	// followupRunLease is how stale a heartbeat must be before the run counts as
	// dead: twelve missed beats, enough to ride out a database blip without
	// reclaiming a live run.
	followupRunLease = "12 minutes"

	// followupRunBound bounds how long one poller waits on a conversation, so a
	// stuck llm-server cannot leak the goroutine. Slightly above executeFollowup's
	// 30-minute poll so a legitimate run is never preempted.
	followupRunBound = 35 * time.Minute
)

// staleRunPredicateSQL selects an 'addressing' row whose run is dead. A row that
// carries a heartbeat is judged by it. A row without one was claimed by a pod
// that predates the heartbeat, or by the value-refresh path before its first
// beat, and keeps the original last_pr_check_at lease — so rolling this change
// out does not reclaim runs an older pod still has in flight. heartbeatParam and
// legacyParam are the positional parameters holding the two leases.
func staleRunPredicateSQL(heartbeatParam, legacyParam int) string {
	return fmt.Sprintf(`pr_lifecycle_state = 'addressing' AND (
		(run_heartbeat_at IS NOT NULL AND run_heartbeat_at < now() - $%d::interval)
		OR (run_heartbeat_at IS NULL AND last_pr_check_at < now() - $%d::interval))`, heartbeatParam, legacyParam)
}

type staleRunAction int

const (
	// staleRunNone: the row is not (or no longer) a dead run.
	staleRunNone staleRunAction = iota
	// staleRunReleased: the run died before submitting; the row is back to
	// 'needs_followup' and can be claimed for a fresh dispatch.
	staleRunReleased
	// staleRunReattach: the run had a conversation; the caller must resume it.
	staleRunReattach
)

// staleRun identifies a dead run a pod has taken over.
type staleRun struct {
	ID             string
	PRURL          string
	Token          string
	ConversationID string
}

// startFollowupRunInTable mints the token for a freshly claimed run and stamps its
// first heartbeat. An empty token with an error means the row was not
// 'addressing' (a terminal transition raced the claim); the run then proceeds
// unguarded, exactly as it did before tokens existed.
func startFollowupRunInTable(dbms *database.DatabaseManager, table, id string) (string, error) {
	dbCtx, cancel := context.WithTimeout(context.Background(), prDBOpTimeout)
	defer cancel()
	var token string
	err := dbms.Db.QueryRowContext(dbCtx, fmt.Sprintf(`
		UPDATE %s SET run_token = gen_random_uuid(), run_conversation_id = NULL, run_heartbeat_at = now()
		WHERE id = $1 AND pr_lifecycle_state = 'addressing'
		RETURNING run_token::text`, table), id).Scan(&token)
	return token, err
}

// heartbeatFollowupRunInTable stamps a live run. Matching on the token means a
// poller whose row was taken over stops refreshing a run it no longer owns.
func heartbeatFollowupRunInTable(dbms *database.DatabaseManager, table, id, token string) error {
	dbCtx, cancel := context.WithTimeout(context.Background(), prDBOpTimeout)
	defer cancel()
	_, err := dbms.Db.ExecContext(dbCtx, fmt.Sprintf(
		`UPDATE %s SET run_heartbeat_at = now() WHERE id = $1 AND run_token = $2::uuid`, table), id, token)
	return err
}

// saveRunConversationInTable records the conversation the run is polling, so a
// later pod can re-attach to it.
func saveRunConversationInTable(dbms *database.DatabaseManager, table, id, token, conversationID string) error {
	dbCtx, cancel := context.WithTimeout(context.Background(), prDBOpTimeout)
	defer cancel()
	_, err := dbms.Db.ExecContext(dbCtx, fmt.Sprintf(
		`UPDATE %s SET run_conversation_id = $3, run_heartbeat_at = now() WHERE id = $1 AND run_token = $2::uuid`, table),
		id, token, conversationID)
	return err
}

// startFollowupHeartbeat stamps the run every followupRunHeartbeatInterval until
// ctx ends. It does nothing without a token (the run is unguarded).
func startFollowupHeartbeat(ctx context.Context, tenantCtx *security.RequestContext, dbms *database.DatabaseManager, id, token string) {
	if token == "" {
		return
	}
	go func() {
		ticker := time.NewTicker(followupRunHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := heartbeatFollowupRunInTable(dbms, prFollowupTable, id, token); err != nil {
					tenantCtx.GetLogger().Warn("pr_lifecycle: followup heartbeat failed", "followup_id", id, "error", err)
				}
			}
		}
	}()
}

// takeOverStaleRunInTable claims a dead run in one row-locked statement, so two
// pods (or a webhook and the cron) racing for the same row cannot both take it.
// The staleness test is re-evaluated under the lock: a run that beat again
// between the caller's look and this statement is left alone.
//
// With a stored conversation the row stays 'addressing' under the same token
// with a fresh heartbeat, and the caller resumes it — pending is untouched, so a
// signal that arrived meanwhile still re-dispatches once the resumed run
// finalizes. Without one the row is released to 'needs_followup', like the old
// reclaim: iteration count preserved (the dead attempt is not charged), pending
// cleared.
func takeOverStaleRunInTable(ctx *security.RequestContext, dbms *database.DatabaseManager, table, id string) (staleRunAction, staleRun, error) {
	dbCtx, cancel := context.WithTimeout(context.Background(), prDBOpTimeout)
	defer cancel()

	var prURL string
	var token, conversation sql.NullString
	err := dbms.Db.QueryRowContext(dbCtx, fmt.Sprintf(`
		WITH cur AS (
			SELECT id, pr_url, run_token::text AS token, run_conversation_id AS conv
			FROM %s
			WHERE id = $1 AND %s
			FOR UPDATE
		),
		upd AS (
			UPDATE %s t SET
				pr_lifecycle_state = CASE WHEN cur.conv IS NULL OR cur.token IS NULL THEN 'needs_followup' ELSE t.pr_lifecycle_state END,
				pr_followup_pending = CASE WHEN cur.conv IS NULL OR cur.token IS NULL THEN false ELSE t.pr_followup_pending END,
				run_token = CASE WHEN cur.conv IS NULL OR cur.token IS NULL THEN NULL ELSE t.run_token END,
				run_conversation_id = CASE WHEN cur.conv IS NULL OR cur.token IS NULL THEN NULL ELSE t.run_conversation_id END,
				run_heartbeat_at = CASE WHEN cur.conv IS NULL OR cur.token IS NULL THEN NULL ELSE now() END,
				last_pr_check_at = CASE WHEN cur.conv IS NULL OR cur.token IS NULL THEN now() ELSE t.last_pr_check_at END
			FROM cur WHERE t.id = cur.id
			RETURNING cur.pr_url, cur.token, cur.conv
		)
		SELECT pr_url, token, conv FROM upd`, table, staleRunPredicateSQL(2, 3), table),
		id, followupRunLease, followupAddressingLease).Scan(&prURL, &token, &conversation)
	if errors.Is(err, sql.ErrNoRows) {
		return staleRunNone, staleRun{}, nil
	}
	if err != nil {
		return staleRunNone, staleRun{}, fmt.Errorf("failed to take over stale followup run: %w", err)
	}
	if !conversation.Valid || !token.Valid {
		return staleRunReleased, staleRun{ID: id, PRURL: prURL}, nil
	}
	return staleRunReattach, staleRun{ID: id, PRURL: prURL, Token: token.String, ConversationID: conversation.String}, nil
}

// releaseStaleRunInTable hands a taken-over run back to 'needs_followup' when it
// cannot be resumed, guarded on the token so it never undoes a newer run.
func releaseStaleRunInTable(dbms *database.DatabaseManager, table, id, token string) error {
	dbCtx, cancel := context.WithTimeout(context.Background(), prDBOpTimeout)
	defer cancel()
	_, err := dbms.Db.ExecContext(dbCtx, fmt.Sprintf(`
		UPDATE %s SET pr_lifecycle_state = 'needs_followup', pr_followup_pending = false,
			run_token = NULL, run_conversation_id = NULL, run_heartbeat_at = NULL, last_pr_check_at = now()
		WHERE id = $1 AND run_token = $2::uuid AND pr_lifecycle_state = 'addressing'`, table), id, token)
	return err
}

// recoverStaleRun takes over the run on followupID if its heartbeat has gone
// stale, and resumes it when there is a conversation to resume. The returned
// action tells a webhook caller whether the row is now free to claim
// (staleRunReleased), being resumed (staleRunReattach), or was never dead.
func recoverStaleRun(ctx *security.RequestContext, dbms *database.DatabaseManager, followupID string) staleRunAction {
	action, run, err := takeOverStaleRunInTable(ctx, dbms, prFollowupTable, followupID)
	if err != nil {
		ctx.GetLogger().Error("pr_lifecycle: stale-run takeover failed", "followup_id", followupID, "error", err)
		return staleRunNone
	}
	switch action {
	case staleRunReleased:
		ctx.GetLogger().Warn("pr_lifecycle: dead run had not submitted; released for a fresh dispatch", "pr_url", run.PRURL)
	case staleRunReattach:
		ctx.GetLogger().Warn("pr_lifecycle: re-attaching to a run whose poller died",
			"pr_url", run.PRURL, "conversation_id", run.ConversationID)
		resumeFollowupRun(ctx, dbms, run)
	}
	return action
}

// reattachStaleRuns is the cron backstop: it resumes every dead run that had a
// conversation. Rows without one are released in bulk by reclaimStuckAddressing.
func reattachStaleRuns(ctx *security.RequestContext, dbms *database.DatabaseManager) {
	ids, err := listStaleReattachableRunIDs(dbms)
	if err != nil {
		ctx.GetLogger().Error("pr_lifecycle: failed to list stale runs", "error", err)
		return
	}
	for _, id := range ids {
		recoverStaleRun(ctx, dbms, id)
	}
}

// listStaleReattachableRunIDs reads the ids up front so the connection is
// released before recovery starts spawning work.
func listStaleReattachableRunIDs(dbms *database.DatabaseManager) (ids []string, err error) {
	dbCtx, cancel := context.WithTimeout(context.Background(), prDBOpTimeout)
	defer cancel()
	rows, err := dbms.Db.QueryContext(dbCtx, fmt.Sprintf(`
		SELECT id FROM %s WHERE run_conversation_id IS NOT NULL AND %s`, prFollowupTable, staleRunPredicateSQL(1, 2)),
		followupRunLease, followupAddressingLease)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// resumeFollowupRun waits for the conversation a dead poller left behind and
// finalizes it exactly as the original poller would have, including the one
// re-dispatch for a signal that arrived meanwhile.
func resumeFollowupRun(ctx *security.RequestContext, dbms *database.DatabaseManager, run staleRun) {
	candidates, err := fetchResolutionCandidatesByURL(dbms, run.PRURL)
	var meta prMetadata
	var tenantID, failReason string
	if err == nil {
		meta, tenantID, _, failReason = resolveFollowupMetadata(candidates)
	}
	if err != nil || failReason != "" {
		// Without metadata there is no account or tenant to poll under. Hand the
		// row back so the normal claim path, which marks such a PR unresolvable,
		// decides what happens to it.
		ctx.GetLogger().Warn("pr_lifecycle: cannot resume followup run; releasing",
			"pr_url", run.PRURL, "reason", failReason, "error", err)
		if rerr := releaseStaleRunInTable(dbms, prFollowupTable, run.ID, run.Token); rerr != nil {
			ctx.GetLogger().Error("pr_lifecycle: failed to release unresumable run", "pr_url", run.PRURL, "error", rerr)
		}
		return
	}

	tenantCtx := security.NewRequestContextForTenantAdmin(tenantID, ctx.GetLogger(), ctx.GetTracer(), ctx.GetMeter())
	go func() {
		defer func() {
			if r := recover(); r != nil {
				tenantCtx.GetLogger().Error("pr_lifecycle: panic in resumed followup goroutine", "pr_url", run.PRURL, "recover", r)
			}
		}()

		bgCtx, cancel := context.WithTimeout(context.Background(), followupRunBound)
		defer cancel()
		startFollowupHeartbeat(bgCtx, tenantCtx, dbms, run.ID, run.Token)
		boundedCtx := security.NewRequestContext(bgCtx, tenantCtx.GetSecurityContext(),
			tenantCtx.GetLogger(), tenantCtx.GetTracer(), tenantCtx.GetMeter())

		response, err := llm.WaitForConversation(boundedCtx, llm.ConversationApiRequest{
			ConversationId: run.ConversationID,
			AccountId:      meta.AccountID,
		})
		finishFollowupRun(tenantCtx, dbms, run.PRURL, run.ID, run.Token, candidates, maxRedispatchChain, response, err)
	}()
}
