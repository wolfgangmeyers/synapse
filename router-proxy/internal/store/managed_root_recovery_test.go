package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func managedRootRecoveryFenceFixture() ManagedRuntimeFence {
	return ManagedRuntimeFence{
		AgentID: "cold-agent", PiSessionID: "session-1", SessionEpoch: 1,
		ProcessPID: 71, ProcessStartMarker: "start-1", ProcessIncarnation: "inc-1",
		RuntimeGeneration: "runtime-1", ManifestRevision: 1, ManifestHash: testManifestHash,
	}
}

func managedRootRecoveryRecipeFixture() ManagedLaunchRecipe {
	recipe, err := ManagedLaunchRecipeDigest(ManagedLaunchRecipe{
		PiExecutable: "/managed/bin/pi", SessionRoot: "/managed/sessions", Cwd: "/managed/work",
		Provider: "openai-codex", ModelID: "gpt-5.6-terra", MechaRPCExtension: "/managed/mecha.ts",
		PiSubagentsExtension: "/managed/subagents.ts", CursorExtension: "/managed/cursor.ts",
		Environment: []string{"HOME=/managed/home", "PATH=/usr/bin"},
	})
	if err != nil {
		panic(err)
	}
	return recipe
}

func seedManagedRootRecoveryAgent(t *testing.T, durable *Store) ManagedRuntimeFence {
	t.Helper()
	ctx := context.Background()
	fence := managedRootRecoveryFenceFixture()
	agent := ManagedAgentRecord{AgentID: fence.AgentID, Name: fence.AgentID, SlotHeld: true, Cwd: "/managed/work", SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: fence.PiSessionID, SessionEpoch: fence.SessionEpoch, ManifestRevision: fence.ManifestRevision, ManifestHash: fence.ManifestHash}
	op := ManagedOperation{OperationID: "cold-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "cold-launch", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, agent, op))
	require.NoError(t, durable.UpdateManagedAgentStarting(ctx, fence.AgentID, fence.PiSessionID, ProcessIdentityRecord{PID: fence.ProcessPID, StartMarker: fence.ProcessStartMarker, Incarnation: fence.ProcessIncarnation}, fence.RuntimeGeneration))
	_, err := durable.db.Exec(`UPDATE managed_agents SET lifecycle_state='running',activity_state='idle',accepting_prompts=1 WHERE agent_id=?`, fence.AgentID)
	require.NoError(t, err)
	return fence
}

func TestManagedRootRecoveryMigrationCreatesBoundedConstrainedTables(t *testing.T) {
	durable := openTestStore(t)
	var tables, indexes int
	require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('managed_launch_recipes','managed_root_recovery_actions')`).Scan(&tables))
	require.Equal(t, 2, tables)
	require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name IN ('managed_root_recovery_active_agent_idx','managed_root_recovery_retention_idx','managed_launch_recipes_hash_idx')`).Scan(&indexes))
	require.Equal(t, 3, indexes)

	fence := seedManagedRootRecoveryAgent(t, durable)
	recipe := managedRootRecoveryRecipeFixture()
	require.NoError(t, durable.PutManagedLaunchRecipe(context.Background(), fence.AgentID, recipe))
	_, err := durable.db.Exec(`UPDATE managed_root_recovery_actions SET stage='ready', expires_at='2026-01-01T00:00:00.000Z' WHERE 1=0`)
	require.NoError(t, err)
}

func TestManagedLaunchRecipeIsImmutableAndRedactsUnknownEnvironment(t *testing.T) {
	durable := openTestStore(t)
	fence := seedManagedRootRecoveryAgent(t, durable)
	recipe := managedRootRecoveryRecipeFixture()
	require.NoError(t, durable.PutManagedLaunchRecipe(context.Background(), fence.AgentID, recipe))
	loaded, err := durable.ManagedLaunchRecipe(context.Background(), fence.AgentID)
	require.NoError(t, err)
	recipe.CreatedAt, recipe.UpdatedAt = loaded.CreatedAt, loaded.UpdatedAt
	require.Equal(t, recipe, *loaded)
	mutated := recipe
	mutated.Cwd = "/another/work"
	mutated, err = ManagedLaunchRecipeDigest(mutated)
	require.NoError(t, err)
	require.ErrorContains(t, durable.PutManagedLaunchRecipe(context.Background(), fence.AgentID, mutated), "immutable")

	invalid := recipe
	invalid.Environment = []string{"API_TOKEN=secret"}
	invalid, err = ManagedLaunchRecipeDigest(invalid)
	require.NoError(t, err)
	require.ErrorContains(t, durable.PutManagedLaunchRecipe(context.Background(), "other", invalid), "environment")
}

func TestManagedRootRecoveryClaimIsIdempotentAndConcurrent(t *testing.T) {
	durable := openTestStore(t)
	fence := seedManagedRootRecoveryAgent(t, durable)
	const callers = 12
	results := make(chan *ManagedRootRecoveryAction, callers)
	failures := make(chan error, callers)
	var wait sync.WaitGroup
	for i := 0; i < callers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			action, created, err := durable.ClaimManagedRootRecovery(context.Background(), fence.AgentID, fence, managedRootRecoveryRecipeFixture().SHA256, "target-1")
			if err != nil {
				failures <- err
				return
			}
			if created && action == nil {
				failures <- errors.New("created claim returned no action")
				return
			}
			results <- action
		}()
	}
	wait.Wait()
	close(results)
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	var first *ManagedRootRecoveryAction
	for action := range results {
		if first == nil {
			first = action
			continue
		}
		require.Equal(t, first.ActionID, action.ActionID)
		require.Equal(t, first.TargetRuntimeGeneration, action.TargetRuntimeGeneration)
	}
	require.NotNil(t, first)
	var count int
	require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM managed_root_recovery_actions`).Scan(&count))
	require.Equal(t, 1, count)
	_, _, err := durable.ClaimManagedRootRecovery(context.Background(), fence.AgentID, fence, managedRootRecoveryRecipeFixture().SHA256, "target-2")
	require.ErrorContains(t, err, "immutable")
}

func TestManagedRootRecoveryTransitionsAreFencedWriteOnceAndRetained(t *testing.T) {
	durable := openTestStore(t)
	ctx := context.Background()
	fence := seedManagedRootRecoveryAgent(t, durable)
	recipe := managedRootRecoveryRecipeFixture()
	action, created, err := durable.ClaimManagedRootRecovery(ctx, fence.AgentID, fence, recipe.SHA256, "target-1")
	require.NoError(t, err)
	require.True(t, created)
	replayed, replayCreated, err := durable.ClaimManagedRootRecovery(ctx, fence.AgentID, fence, recipe.SHA256, "target-1")
	require.NoError(t, err)
	require.False(t, replayCreated)
	require.Equal(t, action.ActionID, replayed.ActionID)
	require.NoError(t, durable.RecordManagedRootRecoveryUnexpectedExit(ctx, action.ActionID))
	counted, err := durable.ManagedRootRecoveryAction(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, 1, counted.UnexpectedExitCount)
	require.NoError(t, durable.AdvanceManagedRootRecovery(ctx, action.ActionID, "claimed", "reaping"))
	require.NoError(t, durable.AdvanceManagedRootRecovery(ctx, action.ActionID, "reaping", "stopped"))
	require.Error(t, durable.InstallManagedRootRecoveryTarget(ctx, action.ActionID, ManagedProcessIdentity{PID: 72, StartMarker: "start-2", Incarnation: "inc-2"}, "target-1"))
	require.NoError(t, durable.BeginManagedRootRecoveryTarget(ctx, action.ActionID, "target-1"))
	var stale *ManagedRootRecoveryTargetStaleError
	require.ErrorAs(t, durable.BeginManagedRootRecoveryTarget(ctx, action.ActionID, "target-1"), &stale)
	require.Equal(t, action.ActionID, stale.ActionID)
	require.NoError(t, durable.InstallManagedRootRecoveryTarget(ctx, action.ActionID, ManagedProcessIdentity{PID: 72, StartMarker: "start-2", Incarnation: "inc-2"}, "target-1"))
	target := fence
	target.ProcessPID, target.ProcessStartMarker, target.ProcessIncarnation, target.RuntimeGeneration = 72, "start-2", "inc-2", "target-1"
	wrongOld := fence
	wrongOld.ProcessStartMarker = "wrong-start"
	require.ErrorContains(t, durable.InstallManagedRootRecoveryFence(ctx, wrongOld, target), "stale")
	require.NoError(t, durable.InstallManagedRootRecoveryFence(ctx, fence, target))
	require.NoError(t, durable.CompleteManagedRootRecovery(ctx, fence.AgentID, target))
	require.Error(t, durable.AdvanceManagedRootRecovery(ctx, action.ActionID, "starting", "ready"))
	require.Error(t, durable.InstallManagedRootRecoveryTarget(ctx, action.ActionID, ManagedProcessIdentity{PID: 73, StartMarker: "start-3", Incarnation: "inc-3"}, "target-1"))
	mismatchedManifest := target
	mismatchedManifest.ManifestHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	require.ErrorContains(t, durable.InstallManagedRootRecoveryFence(ctx, fence, mismatchedManifest), "invalid")
	stored, err := durable.ManagedRootRecoveryActionByID(ctx, action.ActionID)
	require.NoError(t, err)
	require.Equal(t, "ready", stored.Stage)
	require.Equal(t, 72, stored.TargetProcessPID)
	require.NotEmpty(t, stored.ExpiresAt)
	require.Error(t, durable.RecordManagedRootRecoveryUnexpectedExit(ctx, action.ActionID))

	message := "/private/credential/path token=secret-value"
	failedFence := fence
	failedFence.RuntimeGeneration = "target-2"
	failedAction, _, err := durable.ClaimManagedRootRecovery(ctx, fence.AgentID, failedFence, recipe.SHA256, "target-2")
	require.NoError(t, err)
	require.NoError(t, durable.FailManagedRootRecovery(ctx, failedAction.ActionID, "ORPHAN_IDENTITY_UNSAFE", message))
	failed, err := durable.ManagedRootRecoveryActionByID(ctx, failedAction.ActionID)
	require.NoError(t, err)
	require.Equal(t, "failed", failed.Stage)
	require.NotContains(t, failed.FailureMessage, "/private/credential/path")
	require.NotContains(t, failed.FailureMessage, "secret-value")
	require.Error(t, durable.FailManagedRootRecovery(ctx, failedAction.ActionID, "ORPHAN_IDENTITY_UNSAFE", "already terminal"))
	require.Error(t, durable.AdvanceManagedRootRecovery(ctx, failedAction.ActionID, "claimed", "reaping"))
	_, err = durable.PruneManagedRootRecovery(ctx)
	require.NoError(t, err)
}

func TestManagedLaunchRecipeReadRejectsPersistedHashMismatch(t *testing.T) {
	durable := openTestStore(t)
	fence := seedManagedRootRecoveryAgent(t, durable)
	recipe := managedRootRecoveryRecipeFixture()
	require.NoError(t, durable.PutManagedLaunchRecipe(context.Background(), fence.AgentID, recipe))
	_, err := durable.db.Exec(`UPDATE managed_launch_recipes SET recipe_sha256=? WHERE agent_id=?`, strings.Repeat("a", 64), fence.AgentID)
	require.NoError(t, err)
	_, err = durable.ManagedLaunchRecipe(context.Background(), fence.AgentID)
	require.ErrorContains(t, err, "hash is invalid")
}

func TestManagedRootRecoveryPrunesExpiredTerminalRowsAndKeepsActiveClaims(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC))
	durable := openTestStoreWithOptions(t, Options{Clock: clock})
	ctx := context.Background()
	fence := seedManagedRootRecoveryAgent(t, durable)
	recipe := managedRootRecoveryRecipeFixture()
	action, _, err := durable.ClaimManagedRootRecovery(ctx, fence.AgentID, fence, recipe.SHA256, "target-1")
	require.NoError(t, err)
	require.NoError(t, durable.FailManagedRootRecovery(ctx, action.ActionID, "ORPHAN_IDENTITY_UNSAFE", "mismatch"))
	secondFence := fence
	secondFence.ProcessPID = 72
	secondFence.ProcessStartMarker = "start-2"
	secondFence.ProcessIncarnation = "inc-2"
	secondFence.RuntimeGeneration = "runtime-2"
	second, _, err := durable.ClaimManagedRootRecovery(ctx, fence.AgentID, secondFence, recipe.SHA256, "target-2")
	require.NoError(t, err)
	require.NoError(t, clock.Advance(48*time.Hour))
	removed, err := durable.PruneManagedRootRecovery(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	_, err = durable.ManagedRootRecoveryActionByID(ctx, action.ActionID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	stored, err := durable.ManagedRootRecoveryActionByID(ctx, second.ActionID)
	require.NoError(t, err)
	require.Equal(t, "claimed", stored.Stage)
}

func TestManagedRootRecoveryRejectsMutationOfOldFence(t *testing.T) {
	durable := openTestStore(t)
	ctx := context.Background()
	fence := seedManagedRootRecoveryAgent(t, durable)
	recipe := managedRootRecoveryRecipeFixture()
	action, _, err := durable.ClaimManagedRootRecovery(ctx, fence.AgentID, fence, recipe.SHA256, "target-1")
	require.NoError(t, err)
	mutated := fence
	mutated.ProcessIncarnation = "other-incarnation"
	_, _, err = durable.ClaimManagedRootRecovery(ctx, fence.AgentID, mutated, recipe.SHA256, "target-2")
	require.ErrorContains(t, err, "already active")
	require.NoError(t, durable.FailManagedRootRecovery(ctx, action.ActionID, "ORPHAN_IDENTITY_UNSAFE", "unsafe process identity"))
	err = durable.AdvanceManagedRootRecovery(ctx, action.ActionID, "claimed", "reaping")
	require.Error(t, err)
}

func TestManagedRootRecoveryInterruptsOnlyTheOldFence(t *testing.T) {
	durable := openTestStore(t)
	ctx := context.Background()
	fence := seedManagedRootRecoveryAgent(t, durable)
	require.NoError(t, durable.InterruptManagedControlRequests(ctx, fence))
	require.NoError(t, durable.InterruptManagedGoalEvaluation(ctx, fence))
	fresh := fence
	fresh.ProcessPID = 72
	fresh.ProcessStartMarker = "fresh-start"
	fresh.ProcessIncarnation = "fresh-inc"
	fresh.RuntimeGeneration = "fresh-runtime"
	require.NoError(t, durable.InterruptManagedControlRequests(ctx, fresh))
	require.NoError(t, durable.InterruptManagedGoalEvaluation(ctx, fresh))
}
