package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func processGenerationCount(t *testing.T, s *Store) (int, int64) {
	t.Helper()
	var count int
	var high int64
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_process_generations`).Scan(&count))
	require.NoError(t, s.db.QueryRow(`SELECT generation FROM embedded_process_generation_counter WHERE singleton=1`).Scan(&high))
	return count, high
}

func TestEmbeddedProcessMappingAutomaticBoundExpiredIdentityAndReopen(t *testing.T) {
	ctx := context.Background()
	s, path := embeddedRedStore(t)
	old, err := s.MintEmbeddedProcessGeneration(ctx, "historical-incarnation")
	require.NoError(t, err)
	for i := 0; i < EmbeddedProcessGenerationHistoryLimit*3; i++ {
		g, err := s.MintNewEmbeddedProcessGeneration(ctx)
		require.NoError(t, err)
		require.EqualValues(t, i+2, g)
		count, high := processGenerationCount(t, s)
		require.LessOrEqual(t, count, EmbeddedProcessGenerationHistoryLimit)
		require.Equal(t, g, high)
	}
	_, high := processGenerationCount(t, s)
	var found int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_process_generations WHERE generation=?`, old).Scan(&found))
	require.Zero(t, found)
	_, err = s.MintEmbeddedProcessGeneration(ctx, "historical-incarnation")
	var expired *EmbeddedProcessGenerationExpired
	require.True(t, errors.As(err, &expired))
	_, unchanged := processGenerationCount(t, s)
	require.Equal(t, high, unchanged, "an absent historical key cannot mint replay authority")
	require.NoError(t, s.Close())
	cold, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer cold.Close()
	_, err = cold.MintEmbeddedProcessGeneration(ctx, "historical-incarnation")
	require.ErrorAs(t, err, &expired)
	g, err := cold.MintNewEmbeddedProcessGeneration(ctx)
	require.NoError(t, err)
	require.Equal(t, high+1, g)
	count, after := processGenerationCount(t, cold)
	require.Equal(t, EmbeddedProcessGenerationHistoryLimit, count)
	require.Equal(t, g, after)
	requireGoalForeignKeys(t, cold)
}

func TestEmbeddedProcessMappingPruningRetainsCurrentLastUnknownAndCheckedOrigin(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	b := lifecycleEmbeddedFixture(t, s, "retained-unknown")
	first, err := s.MintEmbeddedProcessGeneration(ctx, "retained-first")
	require.NoError(t, err)
	_, err = s.ReserveEmbeddedRestartWake(ctx, b, first, "first-wake", "root-idle", "first-projection")
	require.NoError(t, err)
	require.NoError(t, s.ClaimEmbeddedRestartWake(ctx, b, first, "first-wake"))
	require.NoError(t, s.ReconcileEmbeddedRestartWake(ctx, b, first, "first-wake"))
	second, err := s.MintNewEmbeddedProcessGeneration(ctx)
	require.NoError(t, err)
	_, err = s.ReserveEmbeddedRestartWake(ctx, b, second, "second-wake", "root-idle", "second-projection")
	require.NoError(t, err)
	require.NoError(t, s.ClaimEmbeddedRestartWake(ctx, b, second, "second-wake"))
	seq, digest, err := s.embeddedHead(ctx, b)
	require.NoError(t, err)
	_, err = s.AppendEmbeddedRestartWakeFinalAt(ctx, b, second, "second-wake", seq, digest, "retained-final", wakeFinalFixture(t))
	require.NoError(t, err)
	for range EmbeddedProcessGenerationHistoryLimit * 2 {
		_, err = s.MintNewEmbeddedProcessGeneration(ctx)
		require.NoError(t, err)
	}
	count, _ := processGenerationCount(t, s)
	require.Equal(t, EmbeddedProcessGenerationHistoryLimit+2, count)
	var state string
	require.NoError(t, s.db.QueryRow(`SELECT state FROM embedded_restart_wake_slots WHERE agent_id=? AND slot='last'`, b.AgentID).Scan(&state))
	require.Equal(t, "unknown", state)
	require.Equal(t, 1, embeddedRedCount(t, s, "embedded_restart_wake_checked_origins"))
	retained, err := s.MintEmbeddedProcessGeneration(ctx, "retained-first")
	require.NoError(t, err)
	require.Equal(t, first, retained)
	// Retained UNKNOWN is still terminal, and its historical process is stale.
	_, err = s.ReserveEmbeddedRestartWake(ctx, b, first, "first-wake", "root-idle", "first-projection")
	require.Error(t, err)
	require.Error(t, s.ClaimEmbeddedRestartWake(ctx, b, first, "first-wake"))
	requireGoalForeignKeys(t, s)
	id := goalTerminationAdmission(t, s, b)
	require.NoError(t, s.CompleteManagedTermination(ctx, id))
	count, _ = processGenerationCount(t, s)
	require.Equal(t, EmbeddedProcessGenerationHistoryLimit, count, "termination releases mapping references and prunes automatically")
	require.Zero(t, embeddedRedCount(t, s, "embedded_restart_wake_checked_origins"))
	requireGoalForeignKeys(t, s)
}

func TestEmbeddedProcessMappingMintPruneFailureRollsBackCounterAndRows(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	for range EmbeddedProcessGenerationHistoryLimit {
		_, err := s.MintNewEmbeddedProcessGeneration(ctx)
		require.NoError(t, err)
	}
	beforeCount, beforeHigh := processGenerationCount(t, s)
	_, err := s.db.Exec(`CREATE TRIGGER process_mapping_prune_fault BEFORE DELETE ON embedded_process_generations BEGIN SELECT RAISE(ABORT,'injected prune fault'); END`)
	require.NoError(t, err)
	failedGeneration, err := s.MintNewEmbeddedProcessGeneration(ctx)
	require.ErrorContains(t, err, "injected prune fault")
	require.Zero(t, failedGeneration, "conclusive rollback supplies no assignment")
	var uncertain *EmbeddedProcessGenerationUncertain
	require.False(t, errors.As(err, &uncertain), "a proven rollback remains safely retryable")
	afterCount, afterHigh := processGenerationCount(t, s)
	require.Equal(t, beforeCount, afterCount)
	require.Equal(t, beforeHigh, afterHigh)
	_, err = s.db.Exec(`DROP TRIGGER process_mapping_prune_fault`)
	require.NoError(t, err)
	g, err := s.MintNewEmbeddedProcessGeneration(ctx)
	require.NoError(t, err)
	require.Equal(t, beforeHigh+1, g)
	requireGoalForeignKeys(t, s)
}

func TestEmbeddedProcessMappingCommittedPermissionErrorPreservesAssignment(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	// Fault only this Store's post-COMMIT state-file path. The live SQLite
	// connection and its disposable database/WAL files are untouched.
	component := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(component, []byte("fixture"), 0o600))
	originalPath := s.path
	defer func() { s.path = originalPath }()
	s.path = filepath.Join(component, "state.db")
	generation, err := s.MintNewEmbeddedProcessGeneration(ctx)
	require.ErrorContains(t, err, "stat SQLite state file")
	require.Positive(t, generation, "committed assignment survives the permission-stage error")
	count, high := processGenerationCount(t, s)
	require.Equal(t, 1, count)
	require.Equal(t, generation, high)
	var key string
	require.NoError(t, s.db.QueryRow(`SELECT incarnation_key FROM embedded_process_generations WHERE generation=?`, generation).Scan(&key))
	// The keyed API has the same positive-generation-plus-error contract.
	again, err := s.MintEmbeddedProcessGeneration(ctx, key)
	require.ErrorContains(t, err, "stat SQLite state file")
	require.Equal(t, generation, again)
	s.path = originalPath
	again, err = s.MintEmbeddedProcessGeneration(ctx, key)
	require.NoError(t, err)
	require.Equal(t, generation, again)
	count, high = processGenerationCount(t, s)
	require.Equal(t, 1, count)
	require.Equal(t, generation, high)
	requireGoalForeignKeys(t, s)
}

func TestEmbeddedProcessMappingInconclusiveRollbackReturnsTypedUncertainty(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	// RAISE(ROLLBACK) ends the transaction before the helper's rollback.
	// That rollback fails, so its disposition is deliberately inconclusive.
	_, err := s.db.Exec(`CREATE TRIGGER process_mapping_uncertain_fault BEFORE INSERT ON embedded_process_generations BEGIN SELECT RAISE(ROLLBACK,'injected uncertain outcome'); END`)
	require.NoError(t, err)
	generation, err := s.MintNewEmbeddedProcessGeneration(ctx)
	require.Zero(t, generation)
	var uncertain *EmbeddedProcessGenerationUncertain
	require.ErrorAs(t, err, &uncertain)
	require.ErrorContains(t, uncertain.Cause, "rollback failed")
	require.Zero(t, embeddedRedCount(t, s, "embedded_process_generations"))
	require.Zero(t, embeddedRedCount(t, s, "embedded_process_generation_counter"))
	requireGoalForeignKeys(t, s)
}

func mappingCapacityBinding(t *testing.T, s *Store, id string) EmbeddedBinding {
	t.Helper()
	ctx := context.Background()
	agent := ManagedAgentRecord{AgentID: id, Name: id, Cwd: t.TempDir(), Runtime: "embedded", SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5"}, PiSessionID: id + "-session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: testManifestHash}
	op := ManagedOperation{OperationID: id + "-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: id + "-launch", CanonicalBodyHash: "hash", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	// This fixture deliberately exceeds the ordinary 32-agent launch default
	// to reach the independent, finite global mapping cap using real FK owners.
	require.NoError(t, s.ReserveManagedLaunchWithLimit(ctx, agent, op, EmbeddedProcessGenerationMappingLimit))
	require.NoError(t, s.UpdateManagedAgentStarting(ctx, id, agent.PiSessionID, ProcessIdentityRecord{PID: 321, StartMarker: "start", Incarnation: "incarnation"}, "generation-1"))
	require.NoError(t, s.MarkManagedReady(ctx, id))
	b := EmbeddedBinding{AgentID: id, SessionID: agent.PiSessionID, Epoch: 1, Generation: "generation-1"}
	require.NoError(t, s.BindEmbeddedSession(ctx, b, "root"))
	return claimEmbeddedFixture(t, s, b)
}

func TestEmbeddedProcessMappingAllReferencedAdmissionCapFailsClosed(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	var firstBinding EmbeddedBinding
	for i := 0; i < EmbeddedProcessGenerationMappingLimit/2; i++ {
		id := lifecycleUUID(2000 + i)
		b := mappingCapacityBinding(t, s, id)
		if i == 0 {
			firstBinding = b
		}
		for j := 0; j < 2; j++ {
			g, err := s.MintNewEmbeddedProcessGeneration(ctx)
			require.NoError(t, err)
			_, err = s.ReserveEmbeddedRestartWake(ctx, b, g, id+string(rune('a'+j)), "root-idle", "projection")
			require.NoError(t, err)
			require.NoError(t, s.ClaimEmbeddedRestartWake(ctx, b, g, id+string(rune('a'+j))))
			require.NoError(t, s.ReconcileEmbeddedRestartWake(ctx, b, g, id+string(rune('a'+j))))
		}
	}
	count, high := processGenerationCount(t, s)
	require.Equal(t, EmbeddedProcessGenerationMappingLimit, count)
	_, err := s.MintNewEmbeddedProcessGeneration(ctx)
	var capacity *EmbeddedProcessGenerationCapacity
	require.ErrorAs(t, err, &capacity)
	require.Equal(t, "embedded process generation mapping capacity reached", err.Error())
	afterCount, afterHigh := processGenerationCount(t, s)
	require.Equal(t, count, afterCount)
	require.Equal(t, high, afterHigh)
	requireGoalForeignKeys(t, s)
	id := goalTerminationAdmission(t, s, firstBinding)
	require.NoError(t, s.CompleteManagedTermination(ctx, id))
	g, err := s.MintNewEmbeddedProcessGeneration(ctx)
	require.NoError(t, err)
	require.Equal(t, high+1, g)
	count, _ = processGenerationCount(t, s)
	require.LessOrEqual(t, count, EmbeddedProcessGenerationMappingLimit)
	_, err = s.ManagedAgent(ctx, firstBinding.AgentID)
	require.ErrorIs(t, err, sql.ErrNoRows)
}
