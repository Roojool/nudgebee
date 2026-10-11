package adapter

import (
	"database/sql"
	"fmt"
	"testing"

	"nudgebee/services/internal/database"
	"nudgebee/services/security"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	tokenA = "11111111-1111-1111-1111-111111111111"
	tokenB = "22222222-2222-2222-2222-222222222222"
)

// runLivenessFixture is a throwaway table with the columns the run-liveness SQL
// touches, plus helpers to seed and read rows. DB-gated like the other adapter
// SQL tests: it skips when no database is reachable.
type runLivenessFixture struct {
	t    *testing.T
	dbms *database.DatabaseManager
	tbl  string
}

func newRunLivenessFixture(t *testing.T) *runLivenessFixture {
	t.Helper()
	dbms, err := database.GetDatabaseManager(database.Metastore)
	if err != nil {
		t.Skipf("skipping: database not accessible: %v", err)
	}
	f := &runLivenessFixture{t: t, dbms: dbms, tbl: "zz_pr_run_liveness_test"}
	f.exec(fmt.Sprintf(`DROP TABLE IF EXISTS %s`, f.tbl))
	f.exec(fmt.Sprintf(`CREATE TABLE %s (
		id text PRIMARY KEY,
		pr_url text NOT NULL DEFAULT '',
		pr_lifecycle_state text NOT NULL,
		pr_iteration_count int NOT NULL DEFAULT 0,
		pr_followup_pending boolean NOT NULL DEFAULT false,
		status_message text,
		addressed_comments jsonb NOT NULL DEFAULT '[]'::jsonb,
		last_pr_check_at timestamptz,
		run_token uuid,
		run_conversation_id text,
		run_heartbeat_at timestamptz
	)`, f.tbl))
	t.Cleanup(func() { _, _ = dbms.Db.Exec(fmt.Sprintf(`DROP TABLE IF EXISTS %s`, f.tbl)) })
	return f
}

func (f *runLivenessFixture) exec(q string, args ...any) {
	f.t.Helper()
	_, err := f.dbms.Db.Exec(q, args...)
	require.NoError(f.t, err)
}

// seedRow inserts a row. heartbeatMinsAgo / checkMinsAgo of nil mean NULL;
// token and conversation of "" mean NULL.
type seedRow struct {
	id, state, token, conversation string
	pending                        bool
	iters                          int
	heartbeatMinsAgo               *int
	checkMinsAgo                   *int
}

func mins(n int) *int { return &n }

func (f *runLivenessFixture) seed(r seedRow) {
	f.t.Helper()
	f.exec(fmt.Sprintf(`INSERT INTO %s
		(id, pr_url, pr_lifecycle_state, pr_iteration_count, pr_followup_pending,
		 last_pr_check_at, run_token, run_conversation_id, run_heartbeat_at)
		VALUES ($1, 'https://github.com/acme/repo/pull/1', $2, $3, $4,
			now() - make_interval(mins => $5::int), NULLIF($6, '')::uuid, NULLIF($7, ''),
			now() - make_interval(mins => $8::int))`, f.tbl),
		r.id, r.state, r.iters, r.pending, r.checkMinsAgo, r.token, r.conversation, r.heartbeatMinsAgo)
}

type rowState struct {
	state            string
	iters            int
	pending          bool
	token, conv      sql.NullString
	heartbeatIsNull  bool
	heartbeatAgeMins float64
	lastCheckAgeMins float64
}

func (f *runLivenessFixture) row(id string) rowState {
	f.t.Helper()
	var r rowState
	var hbAge, checkAge sql.NullFloat64
	require.NoError(f.t, f.dbms.Db.QueryRow(fmt.Sprintf(`
		SELECT pr_lifecycle_state, pr_iteration_count, pr_followup_pending,
			run_token::text, run_conversation_id,
			EXTRACT(EPOCH FROM now() - run_heartbeat_at) / 60,
			EXTRACT(EPOCH FROM now() - last_pr_check_at) / 60
		FROM %s WHERE id = $1`, f.tbl), id).
		Scan(&r.state, &r.iters, &r.pending, &r.token, &r.conv, &hbAge, &checkAge))
	r.heartbeatIsNull = !hbAge.Valid
	r.heartbeatAgeMins = hbAge.Float64
	r.lastCheckAgeMins = checkAge.Float64
	return r
}

func superCtx() *security.RequestContext {
	return security.NewRequestContextForSuperAdmin(nil, nil, nil)
}

func TestStartFollowupRunInTable_DB(t *testing.T) {
	f := newRunLivenessFixture(t)
	f.seed(seedRow{id: "claimed", state: "addressing", token: tokenA, conversation: "old-conv", heartbeatMinsAgo: mins(30)})
	f.seed(seedRow{id: "idle", state: "needs_followup"})

	token, err := startFollowupRunInTable(f.dbms, f.tbl, "claimed")
	require.NoError(t, err)
	assert.NotEmpty(t, token)
	assert.NotEqual(t, tokenA, token, "a new run gets a new token")

	r := f.row("claimed")
	assert.Equal(t, token, r.token.String)
	assert.False(t, r.conv.Valid, "a new run starts with no conversation; the previous run's id must not leak in")
	assert.Less(t, r.heartbeatAgeMins, 1.0, "first heartbeat stamped at start")

	_, err = startFollowupRunInTable(f.dbms, f.tbl, "idle")
	assert.ErrorIs(t, err, sql.ErrNoRows, "a row that is not addressing is not tokenized")
	assert.False(t, f.row("idle").token.Valid)
}

func TestFollowupRunHeartbeatAndConversation_AreGuardedByToken_DB(t *testing.T) {
	f := newRunLivenessFixture(t)
	f.seed(seedRow{id: "r", state: "addressing", token: tokenA, heartbeatMinsAgo: mins(10)})

	require.NoError(t, heartbeatFollowupRunInTable(f.dbms, f.tbl, "r", tokenB))
	assert.Greater(t, f.row("r").heartbeatAgeMins, 9.0, "a poller that does not own the run cannot refresh it")

	require.NoError(t, heartbeatFollowupRunInTable(f.dbms, f.tbl, "r", tokenA))
	assert.Less(t, f.row("r").heartbeatAgeMins, 1.0)

	require.NoError(t, saveRunConversationInTable(f.dbms, f.tbl, "r", tokenB, "wrong"))
	assert.False(t, f.row("r").conv.Valid, "a foreign token cannot record a conversation")

	require.NoError(t, saveRunConversationInTable(f.dbms, f.tbl, "r", tokenA, "conv-1"))
	assert.Equal(t, "conv-1", f.row("r").conv.String)
}

func TestTakeOverStaleRunInTable_DB(t *testing.T) {
	f := newRunLivenessFixture(t)
	ctx := superCtx()

	t.Run("dead run with a conversation is re-attached, not restarted", func(t *testing.T) {
		f.seed(seedRow{id: "reattach", state: "addressing", token: tokenA, conversation: "conv-1",
			pending: true, iters: 2, heartbeatMinsAgo: mins(20), checkMinsAgo: mins(20)})

		action, run, err := takeOverStaleRunInTable(ctx, f.dbms, f.tbl, "reattach")
		require.NoError(t, err)
		assert.Equal(t, staleRunReattach, action)
		assert.Equal(t, staleRun{ID: "reattach", PRURL: "https://github.com/acme/repo/pull/1", Token: tokenA, ConversationID: "conv-1"}, run)

		r := f.row("reattach")
		assert.Equal(t, "addressing", r.state, "stays addressing: the run is being resumed, not abandoned")
		assert.Equal(t, tokenA, r.token.String, "same token, so the resumed poller finalizes as the original would")
		assert.True(t, r.pending, "a signal that arrived meanwhile must still re-dispatch after the resumed run finalizes")
		assert.Equal(t, 2, r.iters)
		assert.Less(t, r.heartbeatAgeMins, 1.0, "taking over refreshes the heartbeat so a second pod does not also take it")
	})

	t.Run("a second takeover of the same run finds nothing to take", func(t *testing.T) {
		f.seed(seedRow{id: "twice", state: "addressing", token: tokenA, conversation: "conv-2", heartbeatMinsAgo: mins(20)})

		first, _, err := takeOverStaleRunInTable(ctx, f.dbms, f.tbl, "twice")
		require.NoError(t, err)
		second, _, err := takeOverStaleRunInTable(ctx, f.dbms, f.tbl, "twice")
		require.NoError(t, err)
		assert.Equal(t, staleRunReattach, first)
		assert.Equal(t, staleRunNone, second, "two pods racing for one dead run must not both resume it")
	})

	t.Run("dead run that never submitted is released for a fresh dispatch", func(t *testing.T) {
		f.seed(seedRow{id: "release", state: "addressing", token: tokenA, pending: true, iters: 3,
			heartbeatMinsAgo: mins(20), checkMinsAgo: mins(20)})

		action, _, err := takeOverStaleRunInTable(ctx, f.dbms, f.tbl, "release")
		require.NoError(t, err)
		assert.Equal(t, staleRunReleased, action)

		r := f.row("release")
		assert.Equal(t, "needs_followup", r.state)
		assert.Equal(t, 3, r.iters, "the dead attempt is not charged")
		assert.False(t, r.pending)
		assert.False(t, r.token.Valid)
		assert.True(t, r.heartbeatIsNull)
	})

	t.Run("a live run is left alone", func(t *testing.T) {
		f.seed(seedRow{id: "live", state: "addressing", token: tokenA, conversation: "conv-3", heartbeatMinsAgo: mins(1)})

		action, _, err := takeOverStaleRunInTable(ctx, f.dbms, f.tbl, "live")
		require.NoError(t, err)
		assert.Equal(t, staleRunNone, action)
		assert.Equal(t, "addressing", f.row("live").state)
	})

	t.Run("a mid-run signal does not extend the lease", func(t *testing.T) {
		// claimOrMarkResolution stamps last_pr_check_at on every mark-pending. The
		// old lease was measured from it, so each signal pushed recovery back.
		f.seed(seedRow{id: "signalled", state: "addressing", token: tokenA, conversation: "conv-4",
			heartbeatMinsAgo: mins(20), checkMinsAgo: mins(0)})

		action, _, err := takeOverStaleRunInTable(ctx, f.dbms, f.tbl, "signalled")
		require.NoError(t, err)
		assert.Equal(t, staleRunReattach, action, "the heartbeat, not last_pr_check_at, decides")
	})

	t.Run("a row from a pod that predates the heartbeat keeps the 45-minute lease", func(t *testing.T) {
		f.seed(seedRow{id: "legacy_young", state: "addressing", checkMinsAgo: mins(20)})
		f.seed(seedRow{id: "legacy_old", state: "addressing", checkMinsAgo: mins(60)})
		f.seed(seedRow{id: "legacy_null_check", state: "addressing"})

		young, _, err := takeOverStaleRunInTable(ctx, f.dbms, f.tbl, "legacy_young")
		require.NoError(t, err)
		assert.Equal(t, staleRunNone, young, "an old pod's in-flight run must survive the rollout of this change")

		old, _, err := takeOverStaleRunInTable(ctx, f.dbms, f.tbl, "legacy_old")
		require.NoError(t, err)
		assert.Equal(t, staleRunReleased, old)

		unknown, _, err := takeOverStaleRunInTable(ctx, f.dbms, f.tbl, "legacy_null_check")
		require.NoError(t, err)
		assert.Equal(t, staleRunNone, unknown, "no heartbeat and no last check is unknown, not dead")
	})

	t.Run("only an addressing row can be taken over", func(t *testing.T) {
		f.seed(seedRow{id: "settled", state: "needs_followup", token: tokenA, conversation: "conv-5", heartbeatMinsAgo: mins(60)})

		action, _, err := takeOverStaleRunInTable(ctx, f.dbms, f.tbl, "settled")
		require.NoError(t, err)
		assert.Equal(t, staleRunNone, action)
	})
}

func TestReleaseStaleRunInTable_IsGuardedByToken_DB(t *testing.T) {
	f := newRunLivenessFixture(t)
	f.seed(seedRow{id: "r", state: "addressing", token: tokenB, heartbeatMinsAgo: mins(1)})

	require.NoError(t, releaseStaleRunInTable(f.dbms, f.tbl, "r", tokenA))
	assert.Equal(t, "addressing", f.row("r").state, "a release for an older run must not undo a newer one")

	require.NoError(t, releaseStaleRunInTable(f.dbms, f.tbl, "r", tokenB))
	assert.Equal(t, "needs_followup", f.row("r").state)
}

func TestReclaimStuckAddressing_LeavesRunsThatCanBeReattached_DB(t *testing.T) {
	f := newRunLivenessFixture(t)
	f.seed(seedRow{id: "has_conv", state: "addressing", token: tokenA, conversation: "conv-1", heartbeatMinsAgo: mins(60)})
	f.seed(seedRow{id: "no_conv", state: "addressing", token: tokenA, heartbeatMinsAgo: mins(60)})
	f.seed(seedRow{id: "live", state: "addressing", token: tokenA, heartbeatMinsAgo: mins(1)})

	n := reclaimStuckAddressingInTable(superCtx(), f.dbms, f.tbl)

	assert.Equal(t, int64(1), n, "only the run with nothing to re-attach to is released in bulk")
	assert.Equal(t, "addressing", f.row("has_conv").state, "left for reattachStaleRuns, which resumes it instead of restarting it")
	assert.Equal(t, "needs_followup", f.row("no_conv").state)
	assert.Equal(t, "addressing", f.row("live").state)
}

func TestApplyFollowupOutcomeForRun_OnlyTheOwningRunFinalizes_DB(t *testing.T) {
	ctx := superCtx()

	t.Run("owner finalizes, clears the run columns, and learns about pending", func(t *testing.T) {
		f := newRunLivenessFixture(t)
		f.seed(seedRow{id: "r", state: "addressing", token: tokenA, conversation: "conv-1", pending: true, iters: 2, heartbeatMinsAgo: mins(1)})

		wasPending, applied := applyFollowupOutcomeForRun(ctx, f.dbms, f.tbl, "r", tokenA, followupOutcomeSuccess)

		assert.True(t, applied)
		assert.True(t, wasPending)
		r := f.row("r")
		assert.NotEqual(t, "addressing", r.state)
		assert.Equal(t, 0, r.iters, "success resets the counter")
		assert.False(t, r.pending)
		assert.False(t, r.token.Valid)
		assert.False(t, r.conv.Valid)
		assert.True(t, r.heartbeatIsNull)
	})

	t.Run("a second poller on the same run records nothing", func(t *testing.T) {
		f := newRunLivenessFixture(t)
		f.seed(seedRow{id: "r", state: "addressing", token: tokenA, conversation: "conv-1", iters: 1, heartbeatMinsAgo: mins(1)})

		_, first := applyFollowupOutcomeForRun(ctx, f.dbms, f.tbl, "r", tokenA, followupOutcomeFailed)
		afterFirst := f.row("r")
		wasPending, second := applyFollowupOutcomeForRun(ctx, f.dbms, f.tbl, "r", tokenA, followupOutcomeFailed)

		assert.True(t, first)
		assert.False(t, second, "the original poller and the re-attached one both reach finalize; only one may apply")
		assert.False(t, wasPending, "and only one may trigger the re-dispatch")
		assert.Equal(t, afterFirst.iters, f.row("r").iters, "a failed outcome must not be counted twice")
	})

	t.Run("a poller whose run was replaced cannot finalize the newer run", func(t *testing.T) {
		f := newRunLivenessFixture(t)
		f.seed(seedRow{id: "r", state: "addressing", token: tokenB, conversation: "newer", pending: true, iters: 1, heartbeatMinsAgo: mins(1)})

		wasPending, applied := applyFollowupOutcomeForRun(ctx, f.dbms, f.tbl, "r", tokenA, followupOutcomeSuccess)

		assert.False(t, applied)
		assert.False(t, wasPending)
		r := f.row("r")
		assert.Equal(t, "addressing", r.state, "the newer run is still in flight")
		assert.Equal(t, 1, r.iters)
		assert.True(t, r.pending, "the signal is still owed a re-dispatch by the newer run")
		assert.Equal(t, tokenB, r.token.String)
	})

	t.Run("a run that never got a token still finalizes", func(t *testing.T) {
		f := newRunLivenessFixture(t)
		f.seed(seedRow{id: "r", state: "addressing", token: tokenB, iters: 1})

		_, applied := applyFollowupOutcomeForRun(ctx, f.dbms, f.tbl, "r", "", followupOutcomeSuccess)

		assert.True(t, applied, "tracking failing to start must not leave the row stuck in addressing")
		assert.NotEqual(t, "addressing", f.row("r").state)
	})
}
