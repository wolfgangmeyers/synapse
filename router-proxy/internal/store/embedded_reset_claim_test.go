package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResetEpochClaimAtomicAndFenced(t *testing.T) {
	ctx := context.Background()
	s, old, _ := embeddedMailFixture(t)
	token, err := s.AcquireEmbeddedOwner(ctx, old, "old", time.Minute)
	require.NoError(t, err)
	old.OwnerToken = token
	next := resetReservationNext(old)
	claimed, err := s.ResetEmbeddedSessionEpochAndClaim(ctx, old, next, "root", "operation-1")
	require.NoError(t, err)
	require.EqualValues(t, 1, claimed.OwnerToken)
	_, err = s.ReopenEmbedded(ctx, old)
	require.Error(t, err)
	resolved, err := s.ResolveEmbeddedResetClaim(ctx, old, next, "root", "operation-1")
	require.NoError(t, err)
	require.Equal(t, claimed, resolved)
	_, err = s.ResolveEmbeddedResetClaim(ctx, old, next, "root", "different")
	require.Error(t, err)
	retry, err := s.ResetEmbeddedSessionEpochAndClaim(ctx, old, next, "root", "operation-1")
	require.NoError(t, err)
	require.Equal(t, claimed, retry)
	_, err = s.AcquireEmbeddedOwner(ctx, next, "successor", time.Minute)
	require.NoError(t, err)
	_, err = s.ResetEmbeddedSessionEpochAndClaim(ctx, old, next, "root", "operation-1")
	require.Error(t, err)
	_, err = s.ResolveEmbeddedResetClaim(ctx, old, next, "root", "operation-1")
	require.Error(t, err)
}

func TestResetEpochClaimRollback(t *testing.T) {
	ctx := context.Background()
	s, old, _ := embeddedMailFixture(t)
	token, err := s.AcquireEmbeddedOwner(ctx, old, "old", time.Minute)
	require.NoError(t, err)
	old.OwnerToken = token
	next := resetReservationNext(old)
	_, err = s.db.Exec(`CREATE TRIGGER reject_claim BEFORE INSERT ON embedded_sessions WHEN NEW.owner_id='operation-1' BEGIN SELECT RAISE(ABORT,'reset fault'); END`)
	require.NoError(t, err)
	_, err = s.ResetEmbeddedSessionEpochAndClaim(ctx, old, next, "root", "operation-1")
	require.ErrorContains(t, err, "reset fault")
	_, err = s.ResolveEmbeddedResetClaim(ctx, old, next, "root", "operation-1")
	require.Error(t, err)
	_, err = s.ReopenEmbedded(ctx, old)
	require.NoError(t, err)
}

// A failed ROLLBACK can leave the writer transaction open. Its own reads see
// the provisional claim, but that claim must never become owner authority.
func TestResolveResetClaimIgnoresUncommittedWriter(t *testing.T) {
	ctx := context.Background()
	s, old, _ := embeddedMailFixture(t)
	token, err := s.AcquireEmbeddedOwner(ctx, old, "old", time.Minute)
	require.NoError(t, err)
	old.OwnerToken = token
	next := resetReservationNext(old)
	_, err = s.db.Exec(`BEGIN IMMEDIATE`)
	require.NoError(t, err)
	defer s.db.Exec(`ROLLBACK`)
	_, err = s.db.Exec(`UPDATE managed_agents SET pi_session_id=?, session_epoch=? WHERE agent_id=?`, next.SessionID, next.Epoch, old.AgentID)
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE embedded_sessions SET current=0 WHERE agent_id=? AND current=1`, old.AgentID)
	require.NoError(t, err)
	_, err = s.db.Exec(`INSERT INTO embedded_sessions(agent_id,session_id,epoch,generation,role,current,owner_id,owner_token,owner_expires_at) VALUES(?,?,?,?,?,1,?,1,?)`, next.AgentID, next.SessionID, next.Epoch, next.Generation, "root", "operation-1", "2099-01-01T00:00:00Z")
	require.NoError(t, err)
	var count int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_sessions WHERE current=1 AND owner_id='operation-1'`).Scan(&count))
	require.Equal(t, 1, count)
	_, err = s.ResolveEmbeddedResetClaim(ctx, old, next, "root", "operation-1")
	require.ErrorIs(t, err, ErrStaleEmbeddedFence)
}

// SQLite's RAISE(ROLLBACK) ends the transaction before the helper's rollback,
// making its outcome inconclusive and exercising failure resolution end to end.
func TestResetClaimResolutionPreservesTransactionError(t *testing.T) {
	ctx := context.Background()
	s, old, _ := embeddedMailFixture(t)
	token, err := s.AcquireEmbeddedOwner(ctx, old, "old", time.Minute)
	require.NoError(t, err)
	old.OwnerToken = token
	next := resetReservationNext(old)
	_, err = s.db.Exec(`CREATE TRIGGER reset_rollback AFTER INSERT ON embedded_sessions WHEN NEW.owner_id='operation-1' BEGIN SELECT RAISE(ROLLBACK,'injected reset failure'); END`)
	require.NoError(t, err)
	claim, err := s.ResetEmbeddedSessionEpochAndClaim(ctx, old, next, "root", "operation-1")
	require.ErrorContains(t, err, "injected reset failure")
	require.ErrorContains(t, err, "rollback failed")
	require.ErrorIs(t, err, ErrStaleEmbeddedFence)
	require.Equal(t, EmbeddedBinding{}, claim)
	_, err = s.ReopenEmbedded(ctx, old)
	require.NoError(t, err)
}

func TestResolveResetClaimRejectsMismatchedAgent(t *testing.T) {
	ctx := context.Background()
	s, old, _ := embeddedMailFixture(t)
	token, err := s.AcquireEmbeddedOwner(ctx, old, "old", time.Minute)
	require.NoError(t, err)
	old.OwnerToken = token
	next := resetReservationNext(old)
	_, err = s.ResetEmbeddedSessionEpochAndClaim(ctx, old, next, "root", "operation-1")
	require.NoError(t, err)
	next.AgentID = "different-agent"
	claim, err := s.ResolveEmbeddedResetClaim(ctx, old, next, "root", "operation-1")
	require.Error(t, err)
	require.Equal(t, EmbeddedBinding{}, claim)
}

func TestResetClaimReturnsPostCommitPermissionError(t *testing.T) {
	ctx := context.Background()
	s, old, _ := embeddedMailFixture(t)
	token, err := s.AcquireEmbeddedOwner(ctx, old, "old", time.Minute)
	require.NoError(t, err)
	old.OwnerToken = token
	next := resetReservationNext(old)
	path := s.path
	// A symlink loop makes the state-file permission check fail after commit.
	badPath := filepath.Join(t.TempDir(), "loop")
	require.NoError(t, os.Symlink(badPath, badPath))
	s.path = badPath
	claim, err := s.ResetEmbeddedSessionEpochAndClaim(ctx, old, next, "root", "operation-1")
	s.path = path
	require.ErrorContains(t, err, "stat SQLite state file")
	require.Equal(t, EmbeddedBinding{}, claim)
	resolved, err := s.ResolveEmbeddedResetClaim(ctx, old, next, "root", "operation-1")
	require.NoError(t, err)
	require.EqualValues(t, 1, resolved.OwnerToken)
}

func TestResetClaimWithSeedReturnsCommittedOwnerOnPostCommitError(t *testing.T) {
	ctx := context.Background()
	s, old, _ := embeddedMailFixture(t)
	token, err := s.AcquireEmbeddedOwner(ctx, old, "old", time.Minute)
	require.NoError(t, err)
	old.OwnerToken = token
	next := resetReservationNext(old)
	path := s.path
	badPath := filepath.Join(t.TempDir(), "loop")
	require.NoError(t, os.Symlink(badPath, badPath))
	s.path = badPath
	claimed, err := s.ResetEmbeddedSessionEpochClaimWithSeed(ctx, old, next, "root", "operation-1", [][]byte{[]byte("seed")}, nil)
	s.path = path
	require.ErrorContains(t, err, "stat SQLite state file")
	require.EqualValues(t, 1, claimed.OwnerToken)
	snap, err := s.ReopenEmbedded(ctx, claimed)
	require.NoError(t, err)
	require.Len(t, snap.Entries, 1)
	require.Equal(t, []byte("seed"), snap.Entries[0].Body)
}
