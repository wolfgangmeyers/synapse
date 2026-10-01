package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func lifecycleEmbeddedFixture(t *testing.T, s *Store, id string) EmbeddedBinding {
	t.Helper()
	b := embeddedManagedAgent(t, s, id)
	require.NoError(t, s.BindEmbeddedSession(context.Background(), b, "root"))
	return claimEmbeddedFixture(t, s, b)
}

func TestEmbeddedSupportedTerminationCleansNoticeAndWake(t *testing.T) {
	for _, name := range []string{"planned", "unknown", "claimed-with-origin"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s, path := embeddedRedStore(t)
			b := lifecycleEmbeddedFixture(t, s, "cleanup-"+name)
			notice := restartNoticeFixture(t, s, b)
			gen, err := s.MintEmbeddedProcessGeneration(ctx, name)
			require.NoError(t, err)
			_, err = s.ReserveEmbeddedRestartWake(ctx, b, gen, notice.ID, "interrupted", "checked-projection")
			require.NoError(t, err)
			if name != "planned" {
				require.NoError(t, s.ClaimEmbeddedRestartWake(ctx, b, gen, notice.ID))
				if name == "unknown" {
					require.NoError(t, s.ReconcileEmbeddedRestartWake(ctx, b, gen, notice.ID))
					wake, err := s.EmbeddedRestartWake(ctx, b, gen, notice.ID)
					require.NoError(t, err)
					require.Equal(t, "unknown", wake.State)
				} else {
					seq, digest, err := s.embeddedHead(ctx, b)
					require.NoError(t, err)
					_, err = s.AppendEmbeddedRestartWakeFinalAt(ctx, b, gen, notice.ID, seq, digest, "checked-final", wakeFinalFixture(t))
					require.NoError(t, err)
					require.Equal(t, 1, embeddedRedCount(t, s, "embedded_restart_wake_checked_origins"))
				}
			}
			agent, err := s.ManagedAgent(ctx, b.AgentID)
			require.NoError(t, err)
			fence := restartFenceFromAgent(agent)
			actionID := lifecycleUUID(990)
			op := ManagedOperation{OperationID: actionID, Method: "DELETE", CanonicalPath: "/v1/managed-agents/" + b.AgentID, RequestID: actionID, CanonicalBodyHash: testManifestHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
			admission, err := s.ReserveManagedLifecycle(ctx, ManagedLifecycleReservation{AgentID: b.AgentID, Kind: "terminate", Source: "direct", Method: op.Method, CanonicalPath: op.CanonicalPath, RequestID: actionID, CanonicalBodyHash: op.CanonicalBodyHash, Fence: fence, Operation: op})
			require.NoError(t, err)
			require.NoError(t, s.AdvanceManagedTermination(ctx, actionID, "terminating", "abort", "terminating", "terminate_reap", &fence))
			require.NoError(t, s.AdvanceManagedTermination(ctx, actionID, "terminating", "terminate_reap", "cleanup", "direct_mail_retire", &fence))
			require.NoError(t, s.RetireManagedRecipientSourceForTermination(ctx, b.AgentID, agent.Name))
			require.NoError(t, s.AdvanceManagedTermination(ctx, actionID, "cleanup", "direct_mail_retire", "cleanup", "agent_cleanup", &fence))
			require.NoError(t, s.AdvanceManagedTermination(ctx, actionID, "cleanup", "agent_cleanup", "cleanup", "durable_write", &fence))
			s.managedTerminationFault = func(stage string) error {
				if stage == "before_terminalize" {
					return sql.ErrTxDone
				}
				return nil
			}
			require.Error(t, s.CompleteManagedTermination(ctx, admission.Action.ActionID))
			s.managedTerminationFault = nil
			for _, table := range []string{"embedded_interrupted_notices", "embedded_restart_wake_slots"} {
				var retained int
				require.NoError(t, s.db.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE agent_id=?", b.AgentID).Scan(&retained))
				require.Equal(t, 1, retained, "failed cleanup cannot discard recovery proof: "+table)
			}
			if name == "claimed-with-origin" {
				require.Equal(t, 1, embeddedRedCount(t, s, "embedded_restart_wake_checked_origins"))
			}
			require.NoError(t, s.CompleteManagedTermination(ctx, admission.Action.ActionID))
			_, err = s.ManagedAgent(ctx, b.AgentID)
			require.ErrorIs(t, err, sql.ErrNoRows)
			for _, table := range []string{"embedded_interrupted_notices", "embedded_restart_wake_slots", "embedded_restart_wake_checked_origins", "embedded_entries"} {
				var n int
				require.NoError(t, s.db.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE agent_id=?", b.AgentID).Scan(&n))
				require.Zero(t, n, table)
			}
			require.NoError(t, s.Close())
			fresh, err := Open(ctx, path, Options{})
			require.NoError(t, err)
			defer fresh.Close()
			_, err = fresh.MintEmbeddedProcessGeneration(ctx, name+"-fresh")
			require.NoError(t, err)
			var n int
			require.NoError(t, fresh.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_restart_wake_slots WHERE agent_id=?`, b.AgentID).Scan(&n))
			require.Zero(t, n, "no ghost wake in a fresh process generation")
			require.NoError(t, fresh.db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&n))
			require.Zero(t, n)
		})
	}
}

func TestEmbeddedSupportedResetClearsOldNoticeAndWake(t *testing.T) {
	ctx := context.Background()
	s, path := embeddedRedStore(t)
	b := lifecycleEmbeddedFixture(t, s, "reset-cleanup")
	notice := restartNoticeFixture(t, s, b)
	gen, err := s.MintEmbeddedProcessGeneration(ctx, "reset-before")
	require.NoError(t, err)
	_, err = s.ReserveEmbeddedRestartWake(ctx, b, gen, notice.ID, "interrupted", "checked-projection")
	require.NoError(t, err)
	require.NoError(t, s.ClaimEmbeddedRestartWake(ctx, b, gen, notice.ID))
	seq, digest, err := s.embeddedHead(ctx, b)
	require.NoError(t, err)
	_, err = s.AppendEmbeddedRestartWakeFinalAt(ctx, b, gen, notice.ID, seq, digest, "reset-checked", wakeFinalFixture(t))
	require.NoError(t, err)
	next := b
	next.SessionID, next.Epoch, next.Generation, next.OwnerToken = "replacement-session", b.Epoch+1, "replacement-generation", 0
	// The managed reset lifecycle commits the replacement identity before the embedded CAS.
	_, err = s.db.ExecContext(ctx, `UPDATE managed_agents SET pi_session_id=?,session_epoch=?,runtime_generation=? WHERE agent_id=?`, next.SessionID, next.Epoch, next.Generation, b.AgentID)
	require.NoError(t, err)
	stale := b
	stale.OwnerToken++
	require.Error(t, s.ResetEmbeddedSession(ctx, stale, next, "root"))
	require.Equal(t, 1, embeddedRedCount(t, s, "embedded_restart_wake_checked_origins"))
	require.NoError(t, s.ResetEmbeddedSession(ctx, b, next, "root"))
	require.NoError(t, s.Close())
	fresh, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer fresh.Close()
	var n int
	for _, table := range []string{"embedded_interrupted_notices", "embedded_restart_wake_slots", "embedded_restart_wake_checked_origins"} {
		require.NoError(t, fresh.db.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE agent_id=? AND session_id=? AND epoch=?", b.AgentID, b.SessionID, b.Epoch).Scan(&n))
		require.Zero(t, n, table)
	}
	require.NoError(t, fresh.db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&n))
	require.Zero(t, n)
}
