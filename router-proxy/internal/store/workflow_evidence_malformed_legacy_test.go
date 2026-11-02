package store

import (
	"context"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

// Historical FKs constrain sequence, not digest equality. Do not bypass guards.
func TestWorkflowRetentionMalformedLegacyMigration123(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/legacy.db"
	migrations := DefaultMigrations()
	legacy, err := Open(ctx, path, Options{Migrations: migrations[:migrationIndex(t, migrations, "121_workflow_evidence_retention")]})
	require.NoError(t, err)
	type saved struct {
		b           EmbeddedBinding
		user, final EmbeddedEntry
		digest, due string
	}
	var rows []saved
	for _, mode := range []string{"digest", "deadline"} {
		b := goalBind(t, legacy, "malformed-"+mode, "session", 1, "generation", "root")
		user := EmbeddedEntry{Sequence: 1, ParentDigest: "genesis", Kind: "user", Body: []byte(`{"role":"user","content":"unchanged"}`)}
		user.Digest = embeddedEntryDigest(b, user)
		final := EmbeddedEntry{Sequence: 2, ParentDigest: user.Digest, Kind: "assistant", Body: []byte(`{"role":"assistant","content":[]}`)}
		final.Digest = embeddedEntryDigest(b, final)
		for _, e := range []EmbeddedEntry{user, final} {
			_, err = legacy.db.Exec(`INSERT INTO embedded_entries(agent_id,session_id,epoch,generation,seq,parent_digest,kind,body,digest) VALUES(?,?,?,?,?,?,?,?,?)`, b.AgentID, b.SessionID, b.Epoch, b.Generation, e.Sequence, e.ParentDigest, e.Kind, e.Body, e.Digest)
			require.NoError(t, err)
		}
		digest, due := user.Digest, "not-a-date"
		if mode == "digest" {
			digest = "mismatched-legacy-digest"
			due = "2099-01-01T00:00:00Z"
		}
		_, err = legacy.db.Exec(`INSERT INTO embedded_goal_evaluations(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,owner_token,final_seq,final_digest,state,claimed_at) VALUES(?,?,?,?,1,?,1,?,2,?,'UNKNOWN','2026-01-01T00:00:00Z')`, b.AgentID, b.SessionID, b.Epoch, b.Generation, digest, b.OwnerToken, final.Digest)
		require.NoError(t, err)
		_, err = legacy.db.Exec(`UPDATE embedded_goal_evaluations SET state='NOT_DONE',completed_at='2026-01-01T00:00:00Z' WHERE agent_id=?`, b.AgentID)
		require.NoError(t, err)
		_, err = legacy.db.Exec(`INSERT INTO embedded_goal_continuations(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,state,disposition,reserved_at,due_at) VALUES(?,?,?,?,1,?,1,'reserved','pending','2026-01-01T00:00:00Z',?)`, b.AgentID, b.SessionID, b.Epoch, b.Generation, digest, due)
		require.NoError(t, err)
		_, err = legacy.db.Exec(`INSERT INTO embedded_goal_snooze_calls(call_id,agent_id,session_id,epoch,generation,role,user_seq,user_digest,goal_revision,args_json,result_json,policy_version) VALUES(?,?,?,?,?,'root',1,?,1,'{}','{}',1)`, b.AgentID, b.AgentID, b.SessionID, b.Epoch, b.Generation, digest)
		require.NoError(t, err)
		_, err = legacy.db.Exec(`INSERT INTO embedded_goal_snooze_preferences(agent_id,session_id,epoch,generation,role,user_seq,user_digest,goal_revision,policy_version,call_id,due_at) VALUES(?,?,?,?,'root',1,?,1,1,?,?)`, b.AgentID, b.SessionID, b.Epoch, b.Generation, digest, b.AgentID, due)
		require.NoError(t, err)
		// Receipts require the real transcript digest; malformed evaluations do
		// not authorize corrupting it. Cover all five nonempty evidence tables.
		_, err = legacy.db.Exec(`INSERT INTO managed_agent_policies(agent_id,history_scope_id,desired_revision,goal_enabled,goal_text,settings_json,created_at,updated_at) VALUES(?,?,1,1,'legacy goal','{}','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, b.AgentID, "goal-history-"+b.AgentID)
		require.NoError(t, err)
		_, err = legacy.db.Exec(`INSERT INTO embedded_goal_applied_receipts(turn_key,agent_id,session_id,epoch,generation,role,turn_entry_seq,turn_entry_digest,goal_revision,goal_enabled,goal_text,applied_at) VALUES(?,?,?,?,?,'root',1,?,1,1,'legacy goal','2026-01-01T00:00:00Z')`, b.AgentID, b.AgentID, b.SessionID, b.Epoch, b.Generation, user.Digest)
		require.NoError(t, err)
		rows = append(rows, saved{b, user, final, digest, due})
	}
	require.NoError(t, legacy.Close())
	clock := deterministic.NewFixedClock(time.Now())
	s, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err, "real 120→123 malformed legacy upgrade must not abort")
	defer s.Close()
	var h int64
	require.NoError(t, s.db.QueryRow(`SELECT legacy_evidence_expires_at_ms FROM embedded_sessions LIMIT 1`).Scan(&h))
	require.Equal(t, time.Date(2099, 1, 31, 0, 0, 0, 0, time.UTC).UnixMilli(), h)
	checkTranscript := func() {
		for _, row := range rows {
			for _, e := range []EmbeddedEntry{row.user, row.final} {
				var body []byte
				var digest string
				require.NoError(t, s.db.QueryRow(`SELECT body,digest FROM embedded_entries WHERE agent_id=? AND seq=?`, row.b.AgentID, e.Sequence).Scan(&body, &digest))
				require.Equal(t, e.Body, body)
				require.Equal(t, e.Digest, digest)
			}
		}
		var n int
		require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&n))
		require.Zero(t, n)
	}
	for _, row := range rows {
		var r int64
		var due string
		require.NoError(t, s.db.QueryRow(`SELECT retention_expires_at_ms,due_at FROM embedded_goal_continuations WHERE agent_id=?`, row.b.AgentID).Scan(&r, &due))
		require.Equal(t, h, r)
		require.Equal(t, row.due, due)
		_, err = s.db.Exec(`UPDATE embedded_goal_continuations SET state='started',disposition='unknown',started_at='2100-01-01T00:00:00Z' WHERE agent_id=?`, row.b.AgentID)
		require.Error(t, err, "invalid legacy authority must never start")
		_, started, _ := s.StartEmbeddedGoalContinuation(ctx, row.b, EmbeddedGoalEvaluationKey{UserSeq: 1, UserDigest: row.digest, GoalRevision: 1, FinalSeq: 2, FinalDigest: row.final.Digest})
		// Existing policy gates may deny with false,nil before identity checks.
		// Raw SQL above separately proves the retention start guard.
		require.False(t, started)
		var state, disposition string
		require.NoError(t, s.db.QueryRow(`SELECT state,disposition FROM embedded_goal_continuations WHERE agent_id=?`, row.b.AgentID).Scan(&state, &disposition))
		if row.due == "not-a-date" {
			// Policy is unapplied: Start invalidates before parsing D. Its
			// deletion-only fallback must survive loss of reserved authority.
			require.Equal(t, "invalidated", state)
			require.Equal(t, "invalidated", disposition)
			var closures int
			require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_retention_closures WHERE agent_id=?`, row.b.AgentID).Scan(&closures))
			require.Zero(t, closures, "deletion hold must not grant replay authority")
		} else {
			require.Equal(t, "reserved", state)
			require.Equal(t, "pending", disposition)
		}

	}
	_, err = s.db.Exec(`UPDATE embedded_sessions SET legacy_evidence_expires_at_ms=legacy_evidence_expires_at_ms+1`)
	require.Error(t, err)
	checkTranscript()
	require.NoError(t, s.Close())
	s, err = Open(ctx, path, Options{Clock: deterministic.NewFixedClock(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))})
	require.NoError(t, err)
	defer s.Close()
	var reopened int64
	require.NoError(t, s.db.QueryRow(`SELECT legacy_evidence_expires_at_ms FROM embedded_sessions LIMIT 1`).Scan(&reopened))
	require.Equal(t, h, reopened)
	// Exercise the same transactional pruner used by startup/sweep, without
	// unrelated lifecycle stale-agent removal at this deliberately distant date.
	s.clock = deterministic.NewFixedClock(time.UnixMilli(h - 1))
	require.NoError(t, s.runImmediate(ctx, func() error { return s.pruneEmbeddedEvidenceTx(ctx) }))
	var n int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_goal_continuations`).Scan(&n))
	require.Equal(t, 2, n)
	for _, table := range []string{"embedded_goal_applied_receipts", "embedded_goal_evaluations", "embedded_goal_continuations", "embedded_goal_snooze_calls", "embedded_goal_snooze_preferences"} {
		require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM `+table).Scan(&n))
		require.Equal(t, 2, n, table)
		_, err = s.db.Exec(`DELETE FROM ` + table)
		require.Error(t, err, "direct SQL must honor the deletion-only H hold: %s", table)
	}
	s.clock = deterministic.NewFixedClock(time.UnixMilli(h))
	require.NoError(t, s.runImmediate(ctx, func() error { return s.pruneEmbeddedEvidenceTx(ctx) }))
	for _, table := range []string{"embedded_goal_applied_receipts", "embedded_goal_evaluations", "embedded_goal_continuations", "embedded_goal_snooze_calls", "embedded_goal_snooze_preferences"} {
		require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM `+table).Scan(&n))
		require.Zero(t, n, table)
	}
	checkTranscript()
	s.clock = deterministic.NewFixedClock(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, s.runImmediate(ctx, func() error { return s.pruneEmbeddedEvidenceTx(ctx) }))
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_goal_continuations`).Scan(&n))
	require.Zero(t, n)
}
