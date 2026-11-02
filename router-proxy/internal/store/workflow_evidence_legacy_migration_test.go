package store

import (
	"context"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func TestWorkflowRetentionLegacyContinuationsMigration123(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/legacy.db"
	migrations := DefaultMigrations()
	tip := migrationIndex(t, migrations, "121_workflow_evidence_retention")
	legacy, err := Open(ctx, path, Options{Migrations: migrations[:tip]})
	require.NoError(t, err)
	defer legacy.Close()
	var guard string
	require.NoError(t, legacy.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='embedded_goal_continuation_transition'`).Scan(&guard))
	type snapshot struct {
		agent, state, disposition, reserved, due, started, digest string
		body                                                      []byte
		r                                                         int64
	}
	var saved []snapshot
	for _, state := range []string{"reserved", "started", "invalidated"} {
		b := goalBind(t, legacy, "legacy-"+state, "session", 1, "generation", "root")
		// Use the historical schema directly: current append/evaluator APIs require 121.
		user := EmbeddedEntry{Sequence: 1, ParentDigest: "genesis", Kind: "user", Body: []byte(`{"role":"user","content":"saved bytes"}`)}
		user.Digest = embeddedEntryDigest(b, user)
		final := EmbeddedEntry{Sequence: 2, ParentDigest: user.Digest, Kind: "assistant", Body: []byte(`{"role":"assistant","content":[]}`)}
		final.Digest = embeddedEntryDigest(b, final)
		for _, e := range []EmbeddedEntry{user, final} {
			_, err = legacy.db.Exec(`INSERT INTO embedded_entries(agent_id,session_id,epoch,generation,seq,parent_digest,kind,body,digest) VALUES(?,?,?,?,?,?,?,?,?)`, b.AgentID, b.SessionID, b.Epoch, b.Generation, e.Sequence, e.ParentDigest, e.Kind, e.Body, e.Digest)
			require.NoError(t, err)
		}
		k := EmbeddedGoalEvaluationKey{UserSeq: 1, UserDigest: user.Digest, GoalRevision: 1}
		_, err = legacy.db.Exec(`INSERT INTO embedded_goal_evaluations(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,owner_token,final_seq,final_digest,state,claimed_at) VALUES(?,?,?,?,?,?,1,?,2,?,'UNKNOWN','2026-01-01T00:00:00Z')`, b.AgentID, b.SessionID, b.Epoch, b.Generation, user.Sequence, user.Digest, b.OwnerToken, final.Digest)
		require.NoError(t, err)
		_, err = legacy.db.Exec(`UPDATE embedded_goal_evaluations SET state='NOT_DONE',completed_at='2026-01-01T00:00:00Z' WHERE agent_id=?`, b.AgentID)
		require.NoError(t, err)
		_, err = legacy.db.Exec(`INSERT INTO embedded_goal_continuations(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,state,disposition,reserved_at,due_at) VALUES(?,?,?,?,?,?,1,'reserved','pending','2026-01-01T00:00:00Z','9999-12-01T00:00:00Z')`, b.AgentID, b.SessionID, b.Epoch, b.Generation, user.Sequence, user.Digest)
		require.NoError(t, err)
		if state == "started" {
			_, err = legacy.db.Exec(`UPDATE embedded_goal_continuations SET state='started',disposition='unknown',started_at='2026-01-01T00:00:00Z' WHERE agent_id=?`, b.AgentID)
		} else if state == "invalidated" {
			_, err = legacy.db.Exec(`UPDATE embedded_goal_continuations SET state='invalidated',disposition='invalidated' WHERE agent_id=?`, b.AgentID)
		}
		require.NoError(t, err)
		snap := snapshot{agent: b.AgentID}
		require.NoError(t, legacy.db.QueryRow(`SELECT state,disposition,reserved_at,due_at,COALESCE(started_at,'') FROM embedded_goal_continuations WHERE agent_id=?`, b.AgentID).Scan(&snap.state, &snap.disposition, &snap.reserved, &snap.due, &snap.started))
		require.Equal(t, state, snap.state)
		require.Equal(t, "9999-12-01T00:00:00Z", snap.due)
		require.NoError(t, legacy.db.QueryRow(`SELECT body,digest FROM embedded_entries WHERE agent_id=? AND seq=?`, b.AgentID, k.UserSeq).Scan(&snap.body, &snap.digest))
		saved = append(saved, snap)
	}
	require.NoError(t, legacy.Close())
	before := time.Now().Add(-time.Second).UnixMilli()
	upgraded, err := Open(ctx, path, Options{})
	require.NoError(t, err, "real SQLite migration 120→123 must accept every saved continuation state")
	defer upgraded.Close()
	var restored string
	require.NoError(t, upgraded.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='embedded_goal_continuation_transition'`).Scan(&restored))
	require.Equal(t, guard, restored)
	check := func(s *Store, first bool) {
		for i := range saved {
			want := &saved[i]
			got := snapshot{agent: want.agent}
			var admitted, expected int64
			require.NoError(t, s.db.QueryRow(`SELECT c.state,c.disposition,c.reserved_at,c.due_at,COALESCE(c.started_at,''),c.retention_expires_at_ms,e.body,e.digest,e.retention_admitted_at_ms,MAX(e.retention_admitted_at_ms+2592000000,CAST(ROUND((julianday(c.due_at)-2440587.5)*86400000) AS INTEGER))+2592000000 FROM embedded_goal_continuations c JOIN embedded_entries e ON e.agent_id=c.agent_id AND e.seq=c.user_seq WHERE c.agent_id=?`, want.agent).Scan(&got.state, &got.disposition, &got.reserved, &got.due, &got.started, &got.r, &got.body, &got.digest, &admitted, &expected))
			require.GreaterOrEqual(t, admitted, before)
			require.LessOrEqual(t, admitted, time.Now().Add(time.Second).UnixMilli())
			require.Equal(t, expected, got.r)
			require.Positive(t, got.r)
			require.Less(t, got.r, int64(1<<62))
			if first {
				want.r = got.r
			}
			require.Equal(t, *want, got)
			_, err := s.db.Exec(`UPDATE embedded_goal_continuations SET retention_expires_at_ms=retention_expires_at_ms+1 WHERE agent_id=?`, want.agent)
			require.Error(t, err)
			_, err = s.db.Exec(`UPDATE embedded_goal_continuations SET state=state WHERE agent_id=?`, want.agent)
			require.ErrorContains(t, err, "continuation transition invalid")
		}
		var n int
		require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&n))
		require.Zero(t, n)
		require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name IN ('embedded_execution_continuation_start','embedded_execution_continuation_started')`).Scan(&n))
		require.Equal(t, 2, n)
	}
	check(upgraded, true)
	require.NoError(t, upgraded.Close())
	reopened, err := Open(ctx, path, Options{Clock: deterministic.NewFixedClock(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))})
	require.NoError(t, err)
	defer reopened.Close()
	check(reopened, false)
}
