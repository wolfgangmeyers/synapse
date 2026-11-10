package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Insert completed evidence directly to model a crash before activity publication.
func restartEvidence(t *testing.T, s *Store, b EmbeddedBinding, count int, distinct bool) {
	t.Helper()
	for i := 0; i < count; i++ {
		u := redAppend(t, s, b, "user", fmt.Sprintf("turn %d", i))
		f := redAppend(t, s, b, "assistant", fmt.Sprintf("answer %d", i))
		state := "NOT_DONE"
		if distinct && i == 4 {
			state = "DONE"
		}
		reason := "same"
		if distinct {
			reason = fmt.Sprintf("reason %d", i)
		}
		_, err := s.db.Exec(`INSERT INTO embedded_goal_evaluations(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,owner_token,final_seq,final_digest,state,reason,claimed_at,completed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, b.AgentID, b.SessionID, b.Epoch, b.Generation, u.Sequence, u.Digest, 1, b.OwnerToken, f.Sequence, f.Digest, state, reason, fmt.Sprintf("2026-01-01T00:00:%02dZ", i), fmt.Sprintf("2026-01-01T00:00:%02dZ", i))
		require.NoError(t, err)
	}
}

func TestGoalActivityRestartReviewBackfill(t *testing.T) {
	for _, distinct := range []bool{false, true} {
		t.Run(fmt.Sprint(distinct), func(t *testing.T) {
			s, b, _ := evaluationFixture(t)
			ctx := context.Background()
			_, err := s.db.Exec(`UPDATE managed_agent_policies SET applied_revision=desired_revision,projection_state='applied' WHERE agent_id=?`, b.AgentID)
			require.NoError(t, err)
			restartEvidence(t, s, b, 9, distinct)
			want := 1
			if distinct {
				want = 9
			}
			var dbIndex int
			var dbName, dbPath string
			require.NoError(t, s.db.QueryRow(`PRAGMA database_list`).Scan(&dbIndex, &dbName, &dbPath))
			for i := 0; i < 3; i++ {
				if i > 0 {
					require.NoError(t, s.Close())
					s, err = Open(ctx, dbPath, Options{})
					require.NoError(t, err)
					defer s.Close()
				}
				require.NoError(t, s.BackfillEmbeddedGoalEvaluations(ctx))
				var n, handled int
				require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM managed_complete_history WHERE agent_id=? AND category='managed_goal_evaluation'`, b.AgentID).Scan(&n))
				require.Equal(t, want, n)
				require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_goal_activity_publications WHERE agent_id=?`, b.AgentID).Scan(&handled))
				require.Equal(t, 9, handled)
			}
		})
	}
}

func TestGoalActivityRestartReviewUpgradeEvicted(t *testing.T) {
	s, b, _ := evaluationFixture(t)
	ctx := context.Background()
	_, err := s.db.Exec(`UPDATE managed_agent_policies SET applied_revision=desired_revision,projection_state='applied' WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	restartEvidence(t, s, b, 8, true)
	// Recreate the migration boundary: pre-upgrade evidence has no publication ledger.
	_, err = s.db.Exec(`DROP TABLE embedded_goal_activity_publications`)
	require.NoError(t, err)
	migration, err := os.ReadFile("migrations/128_goal_activity_publication.sql")
	require.NoError(t, err)
	_, err = s.db.Exec(string(migration))
	require.NoError(t, err)
	var n int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_goal_activity_publications WHERE agent_id=?`, b.AgentID).Scan(&n))
	require.Equal(t, 8, n)
	for i := 0; i < 2; i++ {
		require.NoError(t, s.BackfillEmbeddedGoalEvaluations(ctx))
	}
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM managed_complete_history WHERE agent_id=? AND category='managed_goal_evaluation'`, b.AgentID).Scan(&n))
	require.Zero(t, n)
}

func TestGoalActivityUpgradeCopiedDatabase(t *testing.T) {
	ctx := context.Background()
	s, b, _ := evaluationFixture(t)
	restartEvidence(t, s, b, 3, true)
	// Create a self-contained SQLite copy; all upgrade operations target the copy.
	copyPath := filepath.Join(t.TempDir(), "copied.db")
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, copyPath)
	require.NoError(t, err)
	copyStore, err := Open(ctx, copyPath, Options{})
	require.NoError(t, err)
	_, err = copyStore.db.ExecContext(ctx, `DROP TABLE embedded_goal_activity_publications`)
	require.NoError(t, err)
	_, err = copyStore.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE id='128_goal_activity_publication'`)
	require.NoError(t, err)
	require.NoError(t, copyStore.Close())
	copyStore, err = Open(ctx, copyPath, Options{})
	require.NoError(t, err)
	defer copyStore.Close()
	var count int
	require.NoError(t, copyStore.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_goal_activity_publications WHERE agent_id=?`, b.AgentID).Scan(&count))
	require.Equal(t, 3, count)
	require.NoError(t, copyStore.BackfillEmbeddedGoalEvaluations(ctx))
	require.NoError(t, copyStore.db.QueryRowContext(ctx, `SELECT count(*) FROM managed_complete_history WHERE agent_id=? AND category='managed_goal_evaluation'`, b.AgentID).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE id='128_goal_activity_publication'`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, s.Close())
}
