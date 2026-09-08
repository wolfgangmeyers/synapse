package managed

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func restartAllManagedFixture(t *testing.T, dispatcher RestartAllDispatcher, recorder RestartAllDispatchFailureRecorder) (*Manager, *store.Store) {
	t.Helper()
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = durable.Close() })
	agent := store.ManagedAgentRecord{AgentID: "restart-all-agent", Name: "restart-all-agent", SlotHeld: true, Cwd: "/tmp", SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "restart-all-session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	launch := store.ManagedOperation{OperationID: "restart-all-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "restart-all-launch-request", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, agent, launch))
	require.NoError(t, durable.UpdateManagedAgentStarting(ctx, agent.AgentID, agent.PiSessionID, store.ProcessIdentityRecord{PID: 700, StartMarker: "restart-all-start", Incarnation: "restart-all-incarnation"}, "restart-all-generation"))
	require.NoError(t, durable.MarkManagedReady(ctx, agent.AgentID))
	require.NoError(t, durable.MarkManagedOperationTerminal(ctx, launch.OperationID))
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	manager, err := NewManager(ManagerOptions{
		Store: durable, Config: Config{Enabled: false}, Manifest: manifest,
		IDSource:             deterministic.NewSequenceIDSource("123e4567-e89b-42d3-a456-000000000501", "123e4567-e89b-42d3-a456-000000000502"),
		RestartAllDispatcher: dispatcher, RestartAllDispatchFailureRecorder: recorder,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	return manager, durable
}

func TestManagerRestartAllDispatchesOnlyCommittedAcceptedChildren(t *testing.T) {
	var parents []string
	var dispatched [][]string
	manager, durable := restartAllManagedFixture(t, func(_ context.Context, parent string, childIDs []string) error {
		parents = append(parents, parent)
		dispatched = append(dispatched, append([]string(nil), childIDs...))
		return nil
	}, nil)
	request := RestartAllRequest{Method: "POST", CanonicalPath: "/v1/managed-agents/restart-all", RequestID: "123e4567-e89b-42d3-a456-000000000500", CanonicalBodyHash: "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a"}
	result, err := manager.RestartAll(context.Background(), request)
	require.NoError(t, err)
	require.False(t, result.Replayed)
	require.NotNil(t, result.Operation)
	require.Equal(t, []string{request.RequestID}, parents)
	require.Equal(t, [][]string{{"123e4567-e89b-42d3-a456-000000000501"}}, dispatched)

	replay, err := manager.RestartAll(context.Background(), request)
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	require.Equal(t, result.Operation.ResponseBody, replay.Operation.ResponseBody)
	require.Len(t, parents, 1)
	child, err := durable.ManagedLifecycleAction(context.Background(), dispatched[0][0])
	require.NoError(t, err)
	require.Equal(t, "restart_all_child", child.Source)
}

func TestManagerRestartAllDefaultDispatcherHasNoDurableQueue(t *testing.T) {
	manager, _ := restartAllManagedFixture(t, nil, nil)
	require.NoError(t, manager.dispatchAdmittedRestartAllChildren(context.Background(), "123e4567-e89b-42d3-a456-000000000520", []string{"123e4567-e89b-42d3-a456-000000000521"}))
	require.NoError(t, manager.Close(context.Background()))
	require.Error(t, manager.dispatchAdmittedRestartAllChildren(context.Background(), "123e4567-e89b-42d3-a456-000000000520", []string{"123e4567-e89b-42d3-a456-000000000521"}))
}

func restartAllRequestForTest(requestID string) RestartAllRequest {
	return RestartAllRequest{Method: "POST", CanonicalPath: "/v1/managed-agents/restart-all", RequestID: requestID, CanonicalBodyHash: "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a"}
}

func admitUndispatchedRestartAllChild(t *testing.T, fixture *restartExecutionFixture, parentID, childID, generation, resetSession, resetGeneration string) ([]byte, string) {
	t.Helper()
	fixture.manager.restartAllDispatcher = func(context.Context, string, []string) error { return nil }
	fixture.manager.ids = deterministic.NewSequenceIDSource(childID, generation, resetSession, resetGeneration)
	result, err := fixture.manager.RestartAll(context.Background(), restartAllRequestForTest(parentID))
	require.NoError(t, err)
	require.NotNil(t, result.Operation)
	child, err := fixture.durable.ManagedLifecycleAction(context.Background(), childID)
	require.NoError(t, err)
	require.Equal(t, "restart_all_child", child.Source)
	binding, ok := fixture.manager.bindingFor(child.AgentID)
	require.True(t, ok)
	fixture.manager.clearBinding(binding)
	return append([]byte(nil), result.Operation.ResponseBody...), child.AgentID
}

func advanceRestartAllChildToUnboundTargetStart(t *testing.T, fixture *restartExecutionFixture, actionID string) {
	t.Helper()
	ctx := context.Background()
	action, err := fixture.durable.ManagedLifecycleAction(ctx, actionID)
	require.NoError(t, err)
	require.NotNil(t, action.SourceFence)
	source := *action.SourceFence
	info, err := os.Stat(filepath.Join(fixture.root, "session.json"))
	require.NoError(t, err)
	device, inode, err := deviceInode(info)
	require.NoError(t, err)
	artifact := store.ManagedSessionArtifact{ArtifactID: "123e4567-e89b-42d3-a456-0000000009f0", AgentID: source.AgentID, PiSessionID: source.PiSessionID, SessionEpoch: source.SessionEpoch, RootRelativePath: "session.json", PathSHA256: artifactPathHash("session.json"), DiscoveryState: "materialized", FileDevice: device, FileInode: inode, CleanupState: "retained", DiscoveredAt: formatManagedTime(time.Now()), CleanupUpdatedAt: formatManagedTime(time.Now())}
	require.NoError(t, fixture.durable.RecordManagedSessionArtifact(ctx, artifact))
	require.NoError(t, fixture.durable.AttachManagedLifecycleRestartResumeArtifact(ctx, actionID, source, artifact.ArtifactID))
	require.NoError(t, fixture.durable.AdvanceManagedLifecycleRestartToSourceStop(ctx, actionID, source))
	require.NoError(t, fixture.durable.MarkManagedLifecycleRestartSourceStopped(ctx, actionID, source))
	fixture.ops.mu.Lock()
	fixture.ops.states[fixture.old.identity.PID] = ProcessAbsent
	fixture.ops.mu.Unlock()
}

func TestManagerRestartAllDefaultDispatcherAdoptsLiveBindingAndExecutesChild(t *testing.T) {
	fixture := newRestartExecutionFixture(t, time.Millisecond)
	parentID, childID, generation := "123e4567-e89b-42d3-a456-000000000a01", "123e4567-e89b-42d3-a456-000000000a02", "123e4567-e89b-42d3-a456-000000000a03"
	ids := deterministic.NewSequenceIDSource(childID, generation, "123e4567-e89b-42d3-a456-000000000a04")
	fixture.manager.ids = ids
	fixture.manager.artifacts.ids = ids
	result, err := fixture.manager.RestartAll(context.Background(), restartAllRequestForTest(parentID))
	require.NoError(t, err)
	require.NotNil(t, result.Operation)

	require.Eventually(t, func() bool {
		action, actionErr := fixture.durable.ManagedLifecycleAction(context.Background(), childID)
		agent, agentErr := fixture.durable.ManagedAgent(context.Background(), "restart-agent")
		return actionErr == nil && agentErr == nil && action.State == "succeeded" && agent.RuntimeGeneration == generation && agent.AcceptingPrompts
	}, time.Second, time.Millisecond)
	action, err := fixture.durable.ManagedLifecycleAction(context.Background(), childID)
	require.NoError(t, err)
	require.Equal(t, "restart_all_child", action.Source)
	require.Equal(t, parentID, action.ParentActionID)
	require.Equal(t, generation, action.PlannedTargetRuntimeGeneration)
}

func TestManagerResetCancelsUnboundRestartAllChildBeforeExternalWork(t *testing.T) {
	fixture := newRestartExecutionFixture(t, time.Millisecond)
	parentID, childID := "123e4567-e89b-42d3-a456-000000000a11", "123e4567-e89b-42d3-a456-000000000a12"
	parentBody, agentID := admitUndispatchedRestartAllChild(t, fixture, parentID, childID, "123e4567-e89b-42d3-a456-000000000a13", "123e4567-e89b-42d3-a456-000000000a14", "123e4567-e89b-42d3-a456-000000000a15")

	resetID := "123e4567-e89b-42d3-a456-000000000a16"
	result, err := fixture.manager.Reset(context.Background(), ResetRequest{AgentID: agentID, Method: "POST", CanonicalPath: "/v1/managed-agents/" + agentID + "/reset", RequestID: resetID, CanonicalBodyHash: fixture.manifest.CanonicalSHA256})
	require.NoError(t, err)
	require.NotNil(t, result.Operation)
	require.Eventually(t, func() bool {
		action, actionErr := fixture.durable.ManagedLifecycleAction(context.Background(), childID)
		operation, operationErr := fixture.durable.ManagedOperation(context.Background(), childID)
		return actionErr == nil && operationErr == nil && action.State == "cancelled" && operation.State == "terminal"
	}, time.Second, time.Millisecond)
	child, err := fixture.durable.ManagedLifecycleAction(context.Background(), childID)
	require.NoError(t, err)
	require.Equal(t, "CANCELLED_BY_HIGHER_ACTION", child.TerminalCode)
	parent, err := fixture.durable.ManagedOperation(context.Background(), parentID)
	require.NoError(t, err)
	require.Equal(t, parentBody, parent.ResponseBody)
}

func TestManagerResetSupersedesUnboundRestartAllChildAfterSourceStop(t *testing.T) {
	fixture := newRestartExecutionFixture(t, time.Millisecond)
	parentID, childID := "123e4567-e89b-42d3-a456-000000000a21", "123e4567-e89b-42d3-a456-000000000a22"
	parentBody, agentID := admitUndispatchedRestartAllChild(t, fixture, parentID, childID, "123e4567-e89b-42d3-a456-000000000a23", "123e4567-e89b-42d3-a456-000000000a24", "123e4567-e89b-42d3-a456-000000000a25")
	advanceRestartAllChildToUnboundTargetStart(t, fixture, childID)

	resetID := "123e4567-e89b-42d3-a456-000000000a26"
	result, err := fixture.manager.Reset(context.Background(), ResetRequest{AgentID: agentID, Method: "POST", CanonicalPath: "/v1/managed-agents/" + agentID + "/reset", RequestID: resetID, CanonicalBodyHash: fixture.manifest.CanonicalSHA256})
	require.NoError(t, err)
	require.NotNil(t, result.Operation)
	require.Eventually(t, func() bool {
		action, actionErr := fixture.durable.ManagedLifecycleAction(context.Background(), childID)
		return actionErr == nil && action.State == "superseded"
	}, time.Second, time.Millisecond)
	child, err := fixture.durable.ManagedLifecycleAction(context.Background(), childID)
	require.NoError(t, err)
	require.Equal(t, "SUPERSEDED_BY_HIGHER_ACTION", child.TerminalCode)
	parent, err := fixture.durable.ManagedOperation(context.Background(), parentID)
	require.NoError(t, err)
	require.Equal(t, parentBody, parent.ResponseBody)
}

func TestManagerRestartAllRecoveryExecutesCommittedChildAfterManagerRestart(t *testing.T) {
	fixture := newRestartExecutionFixture(t, time.Millisecond)
	parentID, childID, generation := "123e4567-e89b-42d3-a456-000000000a31", "123e4567-e89b-42d3-a456-000000000a32", "123e4567-e89b-42d3-a456-000000000a33"
	parentBody, _ := admitUndispatchedRestartAllChild(t, fixture, parentID, childID, generation, "123e4567-e89b-42d3-a456-000000000a34", "123e4567-e89b-42d3-a456-000000000a35")
	advanceRestartAllChildToUnboundTargetStart(t, fixture, childID)
	require.NoError(t, fixture.manager.Close(context.Background()))

	recovered := recoveredLifecycleRestartManager(t, fixture)
	require.NoError(t, recovered.RecoverManagedLifecycleControls(context.Background()))
	require.Eventually(t, func() bool {
		action, actionErr := fixture.durable.ManagedLifecycleAction(context.Background(), childID)
		return actionErr == nil && action.State == "succeeded"
	}, time.Second, time.Millisecond)
	parent, err := fixture.durable.ManagedOperation(context.Background(), parentID)
	require.NoError(t, err)
	require.Equal(t, parentBody, parent.ResponseBody)
	child, err := fixture.durable.ManagedLifecycleAction(context.Background(), childID)
	require.NoError(t, err)
	require.Equal(t, parentID, child.ParentActionID)
	require.NotNil(t, child.SourceFence)
}

func TestManagerRestartAllReturnsCommittedParentWhenDispatchFails(t *testing.T) {
	var recordedParent string
	var recordedChildren []string
	manager, _ := restartAllManagedFixture(t, func(context.Context, string, []string) error {
		return errors.New("dispatch unavailable")
	}, func(_ context.Context, parent string, children []string, err error) {
		require.Error(t, err)
		recordedParent, recordedChildren = parent, append([]string(nil), children...)
	})
	request := RestartAllRequest{Method: "POST", CanonicalPath: "/v1/managed-agents/restart-all", RequestID: "123e4567-e89b-42d3-a456-000000000510", CanonicalBodyHash: "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a"}
	result, err := manager.RestartAll(context.Background(), request)
	require.NoError(t, err)
	require.NotNil(t, result.Operation)
	require.Equal(t, "terminal", result.Operation.State)
	require.Equal(t, request.RequestID, recordedParent)
	require.Equal(t, []string{"123e4567-e89b-42d3-a456-000000000501"}, recordedChildren)
}
