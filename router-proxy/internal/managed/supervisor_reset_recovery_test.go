package managed

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestRecoverPendingManagedLaunchesTerminalizesOnlyTheLinkedLaunch(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = durable.Close() })
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	agent := store.ManagedAgentRecord{AgentID: "pending-launch", Name: "pending-launch", SlotHeld: true, Cwd: "/tmp", SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "pending-session", SessionEpoch: 1, ManifestRevision: manifest.Revision, ManifestHash: manifest.CanonicalSHA256}
	op := store.ManagedOperation{OperationID: "pending-launch-operation", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "pending-launch-request", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, agent, op))
	require.NoError(t, durable.UpdateManagedAgentStarting(ctx, agent.AgentID, agent.PiSessionID, store.ProcessIdentityRecord{PID: 701, StartMarker: "pending-marker", Incarnation: "pending-incarnation"}, "pending-generation"))
	ops := &resetProcessOps{present: map[int]bool{}}
	manager, err := NewManager(ManagerOptions{Store: durable, Config: Config{Enabled: false}, Manifest: manifest, ProcessGroupOps: ops})
	require.NoError(t, err)
	require.NoError(t, manager.RecoverPendingManagedLaunches(ctx))
	stored, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "failed", stored.LifecycleState)
	require.Equal(t, "LAUNCH_RECOVERY_CANCELLED", stored.LastErrorCode)
	storedOperation, err := durable.ManagedOperation(ctx, op.OperationID)
	require.NoError(t, err)
	require.Equal(t, "terminal", storedOperation.State)
}

func TestRecoverBoundResetTargetReapsExactGroupAndResumesRetainedSession(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = durable.Close() })
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	root := t.TempDir()
	sessionRoot := filepath.Join(root, "sessions")
	require.NoError(t, os.MkdirAll(sessionRoot, 0o700))
	sourceFile := filepath.Join(sessionRoot, "source.json")
	targetFile := filepath.Join(sessionRoot, "target.json")
	require.NoError(t, os.WriteFile(sourceFile, []byte("{}"), 0o600))
	require.NoError(t, os.WriteFile(targetFile, []byte("{}"), 0o600))

	const sourceSession = "123e4567-e89b-42d3-a456-426614174101"
	const targetSession = "123e4567-e89b-42d3-a456-426614174104"
	const targetGeneration = "123e4567-e89b-42d3-a456-426614174105"
	sourceProcess := &launchProcess{identity: ProcessIdentity{PID: 801, StartMarker: sourceSession, Incarnation: "source-incarnation"}, stopped: make(chan struct{})}
	sourceFactory := &resetProcessFactory{processes: []*launchProcess{sourceProcess}}
	sourceRPCs := &resetRPCFactory{rpcs: []*resetRPC{{launchRPC: &launchRPC{}, file: sourceFile}}}
	ops := &resetProcessOps{present: map[int]bool{801: true, 802: true, 803: true}}
	ids := &resetTestIDs{values: []string{
		sourceSession, "123e4567-e89b-42d3-a456-426614174102", "123e4567-e89b-42d3-a456-426614174103",
		"123e4567-e89b-42d3-a456-426614174106", "123e4567-e89b-42d3-a456-426614174107",
	}}
	sourceManager, err := NewManager(ManagerOptions{Store: durable, Config: Config{Enabled: false, SessionRoot: sessionRoot, AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest, ProcessFactory: sourceFactory, RPCFactory: sourceRPCs, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), IDSource: ids, ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: ops})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sourceManager.Close(context.Background()) })
	_, err = sourceManager.Launch(ctx, LaunchRequest{Name: "recover-reset", Cwd: root, RequestID: "123e4567-e89b-42d3-a456-426614174121"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "recover-reset")
		return readErr == nil && agent.LifecycleState == "running" && agent.AcceptingPrompts
	}, 3*time.Second, time.Millisecond)
	sourceAgent, err := durable.ManagedAgent(ctx, "recover-reset")
	require.NoError(t, err)
	source := storedAgentFence(sourceAgent)
	sourceArtifact, err := sourceManager.artifacts.discover(ctx, source.AgentID, source.SessionEpoch, source.PiSessionID, map[string]any{"sessionId": source.PiSessionID, "sessionFile": sourceFile})
	require.NoError(t, err)
	target := source
	target.PiSessionID, target.SessionEpoch = targetSession, source.SessionEpoch+1
	target.ProcessPID, target.ProcessStartMarker, target.ProcessIncarnation = 802, targetSession, "bound-target-incarnation"
	target.RuntimeGeneration = targetGeneration
	targetArtifact, err := sourceManager.artifacts.discover(ctx, target.AgentID, target.SessionEpoch, target.PiSessionID, map[string]any{"sessionId": target.PiSessionID, "sessionFile": targetFile})
	require.NoError(t, err)
	operationID := "123e4567-e89b-42d3-a456-426614174122"
	admission, err := durable.ReserveManagedReset(ctx, store.ManagedResetReservation{
		AgentID: source.AgentID, Method: "POST", CanonicalPath: "/v1/managed-agents/recover-reset/reset", RequestID: operationID,
		CanonicalBodyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SourceFence: source, LiveFence: source,
		PlannedTargetRuntimeGeneration: targetGeneration,
		Operation:                      store.ManagedOperation{OperationID: operationID, Method: "POST", CanonicalPath: "/v1/managed-agents/recover-reset/reset", RequestID: operationID, CanonicalBodyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)},
	})
	require.NoError(t, err)
	require.NoError(t, durable.AdvanceManagedReset(ctx, admission.Action.ActionID, "admitted", "admission", "awaiting_settle", "source_settle", source))
	require.NoError(t, durable.AdvanceManagedReset(ctx, admission.Action.ActionID, "awaiting_settle", "source_settle", "stopping_source", "source_stop", source))
	require.NoError(t, durable.MarkManagedResetSourceStopped(ctx, admission.Action.ActionID, source))
	require.NoError(t, durable.BindManagedResetTarget(ctx, admission.Action.ActionID, source, target, targetArtifact.ArtifactID))

	recoveredTarget := &launchProcess{identity: ProcessIdentity{PID: 803, StartMarker: targetSession, Incarnation: "recovered-target-incarnation"}, stopped: make(chan struct{})}
	recoveryFactory := &resetProcessFactory{processes: []*launchProcess{recoveredTarget}}
	recoveryRPCs := &resetRPCFactory{rpcs: []*resetRPC{{launchRPC: &launchRPC{}, file: targetFile}}}
	recoveryManager, err := NewManager(ManagerOptions{Store: durable, Config: Config{Enabled: false, SessionRoot: sessionRoot, AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest, ProcessFactory: recoveryFactory, RPCFactory: recoveryRPCs, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), IDSource: ids, ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: ops})
	require.NoError(t, err)
	t.Cleanup(func() { _ = recoveryManager.Close(context.Background()) })
	require.NoError(t, recoveryManager.RecoverManagedLifecycleControls(ctx))
	require.Eventually(t, func() bool {
		recovered, actionErr := durable.ManagedLifecycleAction(ctx, operationID)
		agent, agentErr := durable.ManagedAgent(ctx, source.AgentID)
		return actionErr == nil && agentErr == nil && recovered.State == "succeeded" && agent.LifecycleState == "running" && agent.PiSessionID == targetSession && agent.AcceptingPrompts
	}, 3*time.Second, time.Millisecond)
	require.Len(t, recoveryFactory.specs, 1)
	require.Equal(t, ResumeExisting, recoveryFactory.specs[0].LaunchMode)
	require.Equal(t, targetSession, recoveryFactory.specs[0].SessionID)
	require.Contains(t, recoveryFactory.specs[0].Environment, "MECHA_RPC_RUNTIME_GENERATION="+targetGeneration)
	require.Positive(t, ops.waitCount(802), "recovery reaps the complete retained target group before resuming")
	require.Zero(t, ops.waitCount(801), "recovery never repeats source work")
	storedSourceArtifact, err := durable.ManagedSessionArtifact(ctx, sourceArtifact.ArtifactID)
	require.NoError(t, err)
	require.Equal(t, "unlinked", storedSourceArtifact.CleanupState)
}
