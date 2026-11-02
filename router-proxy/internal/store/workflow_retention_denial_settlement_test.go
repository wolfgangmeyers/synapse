package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
	"modernc.org/sqlite"
)

// Helper-level regression with production migrations and real SQLite locks.
// DELETE journal mode permits a reader to block COMMIT, not BEGIN IMMEDIATE.
func TestWorkflowRetentionDenialMetadataCommitBusySettles(t *testing.T) {
	s, b, _ := evaluationFixture(t)
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	s.clock = clock
	var journal string
	require.NoError(t, s.db.QueryRow(`PRAGMA journal_mode=DELETE`).Scan(&journal))
	require.Equal(t, "delete", journal)
	_, err := s.db.Exec(`PRAGMA busy_timeout=0; CREATE TABLE denial_domain_probe(value INTEGER)`)
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE embedded_sessions SET evidence_expired_through_ms=123 WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	reader, err := sql.Open("sqlite", s.path)
	require.NoError(t, err)
	reader.SetMaxOpenConns(1)
	defer reader.Close()
	defer reader.Exec(`ROLLBACK`)
	committed, conclusive, err := s.runImmediateWithCommitOutcome(ctx, func() error {
		if _, err := s.db.Exec(`INSERT INTO denial_domain_probe VALUES(1)`); err != nil {
			return err
		}
		if _, err := reader.Exec(`BEGIN`); err != nil {
			return err
		}
		var n int
		if err := reader.QueryRow(`SELECT count(*) FROM denial_domain_probe`).Scan(&n); err != nil {
			return err
		}
		require.Zero(t, n, "reader holds a shared lock without seeing domain writes")
		return ErrEmbeddedEvidenceExpired
	})
	require.False(t, committed)
	require.True(t, conclusive, "domain rollback is conclusive despite metadata failure")
	require.ErrorIs(t, err, ErrEmbeddedEvidenceExpired)
	var sqliteErr *sqlite.Error
	require.True(t, errors.As(err, &sqliteErr), "expected real SQLite COMMIT failure: %v", err)
	require.Equal(t, 5, sqliteErr.Code(), "SQLITE_BUSY")
	require.NoError(t, func() error { _, err := reader.Exec(`ROLLBACK`); return err }())
	var n int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM denial_domain_probe`).Scan(&n))
	require.Zero(t, n, "denied domain write must remain rolled back")
	require.NoError(t, s.runImmediate(ctx, func() error {
		_, err := s.db.Exec(`INSERT INTO denial_domain_probe VALUES(2)`)
		return err
	}), "COMMIT failure must not leave an active metadata transaction")
	var cutoff int64
	require.NoError(t, s.db.QueryRow(`SELECT evidence_expired_through_ms FROM embedded_sessions WHERE agent_id=?`, b.AgentID).Scan(&cutoff))
	require.EqualValues(t, 123, cutoff, "failed metadata transaction must roll back")
	deny := func() {
		committed, conclusive, err := s.runImmediateWithCommitOutcome(ctx, func() error { return ErrEmbeddedEvidenceExpired })
		require.False(t, committed)
		require.True(t, conclusive)
		require.ErrorIs(t, err, ErrEmbeddedEvidenceExpired)
		require.EqualError(t, err, ErrEmbeddedEvidenceExpired.Error())
	}
	deny()
	want := clock.Now().UnixMilli() - embeddedEvidenceAgeMS
	require.NoError(t, s.db.QueryRow(`SELECT evidence_expired_through_ms FROM embedded_sessions WHERE agent_id=?`, b.AgentID).Scan(&cutoff))
	require.Equal(t, want, cutoff)
	s.clock = deterministic.NewFixedClock(clock.Now().Add(-24 * time.Hour))
	deny()
	require.NoError(t, s.db.QueryRow(`SELECT evidence_expired_through_ms FROM embedded_sessions WHERE agent_id=?`, b.AgentID).Scan(&cutoff))
	require.Equal(t, want, cutoff, "retry under clock regression cannot lower cutoff")
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM denial_domain_probe WHERE value=2`).Scan(&n))
	require.Equal(t, 1, n)
}

func TestWorkflowRetentionDenialInconclusiveRollbackSkipsMetadata(t *testing.T) {
	s, b, _ := evaluationFixture(t)
	s.clock = deterministic.NewFixedClock(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	_, err := s.db.Exec(`UPDATE embedded_sessions SET evidence_expired_through_ms=123 WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	committed, conclusive, err := s.runImmediateWithCommitOutcome(context.Background(), func() error {
		// Settle behind the helper's back to make its ROLLBACK fail in real SQLite.
		// The helper cannot infer an outcome from that failure and must not continue.
		if _, err := s.db.Exec(`ROLLBACK`); err != nil {
			return err
		}
		return ErrEmbeddedEvidenceExpired
	})
	require.False(t, committed)
	require.False(t, conclusive)
	require.ErrorIs(t, err, ErrEmbeddedEvidenceExpired)
	require.ErrorContains(t, err, "rollback failed")
	var cutoff int64
	require.NoError(t, s.db.QueryRow(`SELECT evidence_expired_through_ms FROM embedded_sessions WHERE agent_id=?`, b.AgentID).Scan(&cutoff))
	require.EqualValues(t, 123, cutoff, "inconclusive rollback must not start metadata transaction")
}
