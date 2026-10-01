package managed

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/stretchr/testify/require"
)

// Compile RED is intentional: LaunchEmbeddedRoot is the proposed router-owned
// internal authority. Neither HTTP fields nor caller/name heuristics may grant it.
func TestEmbeddedInternalRootSourceReservationAndColdRestoreRED(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(t.TempDir(), "roles.db")
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	db, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	var calls atomic.Int32
	seen := make(chan string, 2)
	newManager := func(s *store.Store) *Manager {
		m, e := NewManager(ManagerOptions{Store: s, Manifest: manifest,
			Config:               Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
			CatalogSource:        CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
			CodexCredentialStore: testfixture.CodexAdmission(t),
			EmbeddedRunnerFactory: func(runCtx context.Context, saved store.ManagedAgentRecord) error {
				binding := store.EmbeddedBinding{AgentID: saved.AgentID, SessionID: saved.PiSessionID, Epoch: saved.SessionEpoch, Generation: saved.RuntimeGeneration}
				var role string
				read, e := sql.Open("sqlite", path)
				if e != nil {
					return e
				}
				defer read.Close()
				if e = read.QueryRowContext(runCtx, `SELECT role,owner_token FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation).Scan(&role, &binding.OwnerToken); e != nil {
					return e
				}
				if _, e = s.ReopenEmbedded(runCtx, binding); e != nil {
					return e
				}
				calls.Add(1)
				seen <- role
				return nil
			},
		})
		require.NoError(t, e)
		return m
	}
	m := newManager(db)
	request := LaunchRequest{Name: "internal-root", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, RequestID: "c8111111-1111-4111-8111-111111111111"}
	launched, err := m.LaunchEmbeddedRoot(ctx, request)
	require.NoError(t, err)
	require.Equal(t, "embedded", launched.Agent.Runtime)
	// Reservation is committed before Launch returns, not lazily inferred by a runner.
	reservation, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	var reservedRole string
	require.NoError(t, reservation.QueryRowContext(ctx, `SELECT role FROM embedded_sessions WHERE agent_id=? AND current=1`, launched.Agent.AgentID).Scan(&reservedRole))
	require.Equal(t, "root", reservedRole)
	require.NoError(t, reservation.Close())
	select {
	case role := <-seen:
		require.Equal(t, "root", role, "source must choose role before constructing runner")
	case <-time.After(3 * time.Second):
		t.Fatal("internal root runner did not start")
	}
	first := *launched.Agent
	require.NoError(t, m.Close(ctx))
	require.NoError(t, db.Close())
	require.NoError(t, clock.Advance(embeddedOwnerTTL+time.Second))
	db, err = store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer db.Close()
	saved, err := db.ManagedAgent(ctx, first.AgentID)
	require.NoError(t, err)
	require.Equal(t, first.PiSessionID, saved.PiSessionID)
	m = newManager(db)
	defer m.Close(ctx)
	require.NoError(t, m.RecoverEmbeddedRoots(ctx))
	select {
	case role := <-seen:
		require.Equal(t, "root", role, "cold restore must use saved role")
	case <-time.After(3 * time.Second):
		t.Fatal("internal root was not recovered")
	}
	require.EqualValues(t, 2, calls.Load())
}
