package store

import (
	"context"
	"database/sql/driver"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"modernc.org/sqlite"
)

// Registered before fixture connections open; triggers invoke it while holding
// SQLite's write transaction, giving the tests a deterministic in-flight barrier.
var seedBarrierEntered, seedBarrierRelease chan struct{}

func init() {
	sqlite.MustRegisterScalarFunction("reset_seed_test_barrier", 0, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
		close(seedBarrierEntered)
		<-seedBarrierRelease
		return int64(0), nil
	})
}

// Inspect all rows for the epoch, including unexpected generations, rather than
// relying on the generation-filtered snapshot alone.
func assertResetSeedRows(t *testing.T, s *Store, b EmbeddedBinding, owner string, token int64, kinds []string, bodies [][]byte) {
	t.Helper()
	require.Len(t, kinds, len(bodies))
	var gotOwner string
	var gotToken int64
	require.NoError(t, s.db.QueryRow(`SELECT owner_id,owner_token FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&gotOwner, &gotToken))
	require.Equal(t, owner, gotOwner)
	require.Equal(t, token, gotToken)
	rows, err := s.db.Query(`SELECT generation,seq,parent_digest,kind,body,digest FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? ORDER BY seq`, b.AgentID, b.SessionID, b.Epoch)
	require.NoError(t, err)
	defer rows.Close()
	parent := embeddedGenesis
	i := 0
	for rows.Next() {
		require.Less(t, i, len(bodies), "unexpected entry in reset epoch")
		var generation string
		var e EmbeddedEntry
		require.NoError(t, rows.Scan(&generation, &e.Sequence, &e.ParentDigest, &e.Kind, &e.Body, &e.Digest))
		require.Equal(t, b.Generation, generation)
		require.Equal(t, int64(i+1), e.Sequence)
		require.Equal(t, parent, e.ParentDigest)
		require.Equal(t, kinds[i], e.Kind)
		require.Equal(t, bodies[i], e.Body)
		require.Equal(t, embeddedEntryDigest(b, e), e.Digest)
		parent = e.Digest
		i++
	}
	require.NoError(t, rows.Err())
	require.Equal(t, len(bodies), i)
}

func TestResetSeedSQLiteRecovery(t *testing.T) {
	ctx := context.Background()
	s, old, _ := embeddedMailFixture(t)
	token, err := s.AcquireEmbeddedOwner(ctx, old, "old", time.Minute)
	require.NoError(t, err)
	old.OwnerToken = token
	imported, err := s.AppendEmbeddedEntry(ctx, old, "user", []byte(`{"text":"ported"}`))
	require.NoError(t, err)
	next, err := s.ResetEmbeddedSessionEpochAndClaim(ctx, old, resetReservationNext(old), "root", "reset")
	require.NoError(t, err)
	bodies := [][]byte{[]byte(`{"text":"genesis"}`), []byte(`{"text":"scratch"}`)}
	refs := []EmbeddedEntry{imported}
	_, err = s.db.Exec(`CREATE TRIGGER seed_fault BEFORE INSERT ON embedded_entries WHEN NEW.seq=2 BEGIN SELECT RAISE(ABORT,'seed fault'); END`)
	require.NoError(t, err)
	err = s.InstallEmbeddedResetSeed(ctx, next, old, "reset", bodies, refs)
	require.ErrorContains(t, err, "seed fault")
	snap, err := s.ReopenEmbedded(ctx, next)
	require.NoError(t, err)
	require.Empty(t, snap.Entries)
	assertResetSeedRows(t, s, next, "reset", next.OwnerToken, nil, nil)
	_, err = s.db.Exec(`DROP TRIGGER seed_fault`)
	require.NoError(t, err)
	// Simulate a prefix written by the prior non-atomic implementation.
	_, err = s.AppendEmbeddedEntry(ctx, next, "user", bodies[0])
	require.NoError(t, err)
	require.NoError(t, s.InstallEmbeddedResetSeed(ctx, next, old, "reset", bodies, refs))
	require.NoError(t, s.InstallEmbeddedResetSeed(ctx, next, old, "reset", bodies, refs))
	snap, err = s.ReopenEmbedded(ctx, next)
	require.NoError(t, err)
	require.Len(t, snap.Entries, 3)
	assertResetSeedRows(t, s, next, "reset", next.OwnerToken, []string{"user", "user", imported.Kind}, [][]byte{bodies[0], bodies[1], imported.Body})
	require.Error(t, s.InstallEmbeddedResetSeed(ctx, next, old, "wrong", bodies, refs))
	require.Error(t, s.InstallEmbeddedResetSeed(ctx, next, old, "reset", [][]byte{[]byte(`different`)}, refs))
	_, err = s.AcquireEmbeddedOwner(ctx, next, "successor", time.Minute)
	require.NoError(t, err)
	require.Error(t, s.InstallEmbeddedResetSeed(ctx, next, old, "reset", bodies, refs))
}

func TestResetSeedRejectsMatchingPrefixFromOtherGeneration(t *testing.T) {
	ctx := context.Background()
	s, old, _ := embeddedMailFixture(t)
	token, err := s.AcquireEmbeddedOwner(ctx, old, "old", time.Minute)
	require.NoError(t, err)
	old.OwnerToken = token
	next, err := s.ResetEmbeddedSessionEpochAndClaim(ctx, old, resetReservationNext(old), "root", "reset")
	require.NoError(t, err)
	body := []byte(`{"text":"genesis"}`)
	// The entry key is shared across generations and its digest does not include generation.
	_, err = s.db.Exec(`INSERT INTO embedded_sessions(agent_id,session_id,epoch,generation,role,current) VALUES(?,?,?,?,?,0)`, next.AgentID, next.SessionID, next.Epoch, "other-generation", "root")
	require.NoError(t, err)
	e := EmbeddedEntry{Sequence: 1, ParentDigest: embeddedGenesis, Kind: "user", Body: body}
	e.Digest = embeddedEntryDigest(next, e)
	_, err = s.db.Exec(`INSERT INTO embedded_entries(agent_id,session_id,epoch,generation,seq,parent_digest,kind,body,digest,retention_admitted_at_ms) VALUES(?,?,?,?,?,?,?,?,?,?)`, next.AgentID, next.SessionID, next.Epoch, "other-generation", e.Sequence, e.ParentDigest, e.Kind, e.Body, e.Digest, time.Now().UnixMilli())
	require.NoError(t, err)
	require.ErrorContains(t, s.InstallEmbeddedResetSeed(ctx, next, old, "reset", [][]byte{body}, nil), "conflicting reset seed")
}

func TestResetSeedSQLiteImportedMetadataAndPostCommitRetry(t *testing.T) {
	ctx := context.Background()
	s, old, path := embeddedMailFixture(t)
	token, err := s.AcquireEmbeddedOwner(ctx, old, "old", time.Minute)
	require.NoError(t, err)
	old.OwnerToken = token
	imported, err := s.AppendEmbeddedEntry(ctx, old, "assistant", []byte(`{"text":"imported"}`))
	require.NoError(t, err)
	var admitted int64
	require.NoError(t, s.db.QueryRow(`SELECT retention_admitted_at_ms FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND seq=?`, old.AgentID, old.SessionID, old.Epoch, imported.Sequence).Scan(&admitted))
	_, err = s.db.Exec(`UPDATE embedded_sessions SET evidence_expired_through_ms=? WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, admitted, old.AgentID, old.SessionID, old.Epoch, old.Generation)
	require.NoError(t, err)
	next, err := s.ResetEmbeddedSessionEpochAndClaim(ctx, old, resetReservationNext(old), "root", "reset")
	require.NoError(t, err)
	// A symlink loop makes the post-COMMIT state-file stat fail without touching SQLite.
	loop := filepath.Join(t.TempDir(), "loop")
	require.NoError(t, os.Symlink(loop, loop))
	s.path = loop
	bodies := [][]byte{[]byte(`{"text":"genesis"}`), []byte(`{"text":"scratch"}`)}
	err = s.InstallEmbeddedResetSeed(ctx, next, old, "reset", bodies, []EmbeddedEntry{imported})
	s.path = path
	require.ErrorContains(t, err, "stat SQLite state file")
	require.NoError(t, s.InstallEmbeddedResetSeed(ctx, next, old, "reset", bodies, []EmbeddedEntry{imported}))
	var gotAdmission, cutoff int64
	var kind string
	require.NoError(t, s.db.QueryRow(`SELECT kind,retention_admitted_at_ms FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND seq=3`, next.AgentID, next.SessionID, next.Epoch, next.Generation).Scan(&kind, &gotAdmission))
	require.NoError(t, s.db.QueryRow(`SELECT evidence_expired_through_ms FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, next.AgentID, next.SessionID, next.Epoch, next.Generation).Scan(&cutoff))
	require.Equal(t, imported.Kind, kind)
	require.Equal(t, admitted, gotAdmission)
	require.GreaterOrEqual(t, cutoff, admitted)
	snap, err := s.ReopenEmbedded(ctx, next)
	require.NoError(t, err)
	require.Len(t, snap.Entries, 3)
	require.Equal(t, bodies[0], snap.Entries[0].Body)
	require.Equal(t, bodies[1], snap.Entries[1].Body)
	require.Equal(t, imported.Body, snap.Entries[2].Body)
	assertResetSeedRows(t, s, next, "reset", next.OwnerToken, []string{"user", "user", imported.Kind}, [][]byte{bodies[0], bodies[1], imported.Body})
}

func TestResetSeedSQLiteCompetingOwnerAndWriter(t *testing.T) {
	for _, order := range []string{"seed-first", "successor-first"} {
		t.Run(order, func(t *testing.T) {
			ctx := context.Background()
			s, old, path := embeddedMailFixture(t)
			token, err := s.AcquireEmbeddedOwner(ctx, old, "old", time.Minute)
			require.NoError(t, err)
			old.OwnerToken = token
			next, err := s.ResetEmbeddedSessionEpochAndClaim(ctx, old, resetReservationNext(old), "root", "reset")
			require.NoError(t, err)
			other, err := Open(ctx, path, Options{})
			require.NoError(t, err)
			defer other.Close()
			// Fail immediately on a held write lock: the error proves the competing
			// operation reached SQLite before the paused transaction was released.
			_, err = other.db.Exec(`PRAGMA busy_timeout=0`)
			require.NoError(t, err)
			_, err = s.db.Exec(`PRAGMA busy_timeout=0`)
			require.NoError(t, err)
			seedBarrierEntered, seedBarrierRelease = make(chan struct{}), make(chan struct{})
			release := seedBarrierRelease
			released := false
			defer func() {
				if !released {
					close(release)
				}
			}()
			bodies := [][]byte{[]byte(`seed`), []byte(`second`)}
			done := make(chan error, 1)
			if order == "seed-first" {
				_, err = s.db.Exec(`CREATE TRIGGER seed_pause BEFORE INSERT ON embedded_entries WHEN NEW.seq=2 BEGIN SELECT reset_seed_test_barrier(); END`)
				require.NoError(t, err)
				go func() { done <- s.InstallEmbeddedResetSeed(ctx, next, old, "reset", bodies, nil) }()
				select {
				case <-seedBarrierEntered:
				case <-time.After(5 * time.Second):
					t.Fatal("seed did not reach barrier")
				}
				_, err = other.AcquireEmbeddedOwner(ctx, next, "successor", time.Minute)
				require.ErrorContains(t, err, "locked")
				_, err = other.AppendEmbeddedEntry(ctx, next, "user", []byte(`competitor`))
				require.ErrorContains(t, err, "locked")
				close(release)
				released = true
				require.NoError(t, <-done)
				_, err = s.db.Exec(`DROP TRIGGER seed_pause`)
				require.NoError(t, err)
			} else {
				_, err = s.db.Exec(`CREATE TRIGGER owner_pause BEFORE UPDATE OF owner_id ON embedded_sessions WHEN NEW.owner_id='successor' BEGIN SELECT reset_seed_test_barrier(); END`)
				require.NoError(t, err)
				go func() { _, err := other.AcquireEmbeddedOwner(ctx, next, "successor", time.Minute); done <- err }()
				select {
				case <-seedBarrierEntered:
				case <-time.After(5 * time.Second):
					t.Fatal("successor did not reach barrier")
				}
				err = s.InstallEmbeddedResetSeed(ctx, next, old, "reset", bodies, nil)
				require.ErrorContains(t, err, "locked")
				close(release)
				released = true
				require.NoError(t, <-done)
				_, err = s.db.Exec(`DROP TRIGGER owner_pause`)
				require.NoError(t, err)
			}
			var newToken int64
			require.NoError(t, s.db.QueryRow(`SELECT owner_token FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, next.AgentID, next.SessionID, next.Epoch, next.Generation).Scan(&newToken))
			if order == "seed-first" {
				newToken, err = other.AcquireEmbeddedOwner(ctx, next, "successor", time.Minute)
				require.NoError(t, err)
			}
			require.Equal(t, next.OwnerToken+1, newToken)
			require.ErrorIs(t, s.InstallEmbeddedResetSeed(ctx, next, old, "reset", bodies, nil), ErrStaleEmbeddedFence)
			successor := next
			successor.OwnerToken = newToken
			_, err = other.AppendEmbeddedEntry(ctx, successor, "user", []byte(`successor`))
			require.NoError(t, err)
			snap, err := s.ReopenEmbedded(ctx, successor)
			require.NoError(t, err)
			if order == "seed-first" {
				require.Len(t, snap.Entries, 3)
				require.Equal(t, bodies[0], snap.Entries[0].Body)
				require.Equal(t, bodies[1], snap.Entries[1].Body)
			} else {
				require.Len(t, snap.Entries, 1)
			}
			require.Equal(t, []byte(`successor`), snap.Entries[len(snap.Entries)-1].Body)
			if order == "seed-first" {
				assertResetSeedRows(t, s, next, "successor", newToken, []string{"user", "user", "user"}, [][]byte{bodies[0], bodies[1], []byte(`successor`)})
			} else {
				assertResetSeedRows(t, s, next, "successor", newToken, []string{"user"}, [][]byte{[]byte(`successor`)})
			}
		})
	}
}
