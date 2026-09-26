package managed

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestNativeModelRPCSetModelUsesStockJSONLWireAndRejectsMismatchedResponse(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	requests := readJSONLRequests(stdinReader)

	result := make(chan struct {
		model Model
		err   error
	}, 1)
	go func() {
		model, err := client.SetModel(context.Background(), "google", "gemini-2.5-pro")
		result <- struct {
			model Model
			err   error
		}{model, err}
	}()
	request := requireRequest(t, requests)
	require.Equal(t, "set_model", request["type"])
	require.Equal(t, "google", request["provider"])
	require.Equal(t, "gemini-2.5-pro", request["modelId"])
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": request["id"], "type": "response", "command": "set_model", "success": true, "data": map[string]any{"provider": "google", "id": "gemini-2.5-pro", "name": "Gemini 2.5 Pro"}}))
	outcome := <-result
	require.NoError(t, outcome.err)
	require.Equal(t, Model{Provider: "google", ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro", Available: true}, outcome.model)

	go func() {
		model, err := client.SetModel(context.Background(), "google", "gemini-2.5-pro")
		result <- struct {
			model Model
			err   error
		}{model, err}
	}()
	request = requireRequest(t, requests)
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": request["id"], "type": "response", "command": "set_model", "success": true, "data": map[string]any{"provider": "google", "id": "different"}}))
	outcome = <-result
	require.ErrorContains(t, outcome.err, "does not match")
}

type modelSwitchRPC struct {
	current  Model
	result   Model
	setErr   error
	stateErr error
	setCalls int
	prompts  []string
}

func (r *modelSwitchRPC) GetState(context.Context) (map[string]any, error) {
	if r.stateErr != nil {
		return nil, r.stateErr
	}
	return map[string]any{"model": map[string]any{"provider": r.current.Provider, "id": r.current.ID, "name": r.current.Name}}, nil
}
func (r *modelSwitchRPC) GetCommands(context.Context) ([]string, error)   { return nil, nil }
func (r *modelSwitchRPC) Bootstrap(context.Context, map[string]any) error { return nil }
func (r *modelSwitchRPC) WaitForReceipt(context.Context, string, string, int, int64, string) error {
	return nil
}
func (r *modelSwitchRPC) GetActiveTools(context.Context) ([]string, error) { return nil, nil }
func (r *modelSwitchRPC) Prompt(_ context.Context, message string) error {
	r.prompts = append(r.prompts, message)
	return nil
}
func (r *modelSwitchRPC) Abort(context.Context) error { return nil }
func (r *modelSwitchRPC) Close() error                { return nil }
func (r *modelSwitchRPC) SetModel(_ context.Context, provider, modelID string) (Model, error) {
	r.setCalls++
	if r.result.Provider != "" {
		r.current = r.result
	}
	return r.result, r.setErr
}

func modelSwitchManagerFixture(t *testing.T, rpc *modelSwitchRPC) (*Manager, *store.Store, store.ManagedRuntimeFence, string) {
	t.Helper()
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, databasePath, store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, durable.Close()) })
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	fence := store.ManagedRuntimeFence{AgentID: "model-agent", PiSessionID: "model-session", SessionEpoch: 1, ProcessPID: 731, ProcessStartMarker: "model-start", ProcessIncarnation: "model-incarnation", RuntimeGeneration: "model-generation", ManifestRevision: manifestRevision(manifest), ManifestHash: manifest.CanonicalSHA256}
	agent := store.ManagedAgentRecord{AgentID: fence.AgentID, Name: fence.AgentID, SlotHeld: true, Cwd: t.TempDir(), SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: fence.PiSessionID, SessionEpoch: fence.SessionEpoch, ManifestRevision: fence.ManifestRevision, ManifestHash: fence.ManifestHash}
	launch := store.ManagedOperation{OperationID: "model-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "model-launch", CanonicalBodyHash: "model-launch-body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, agent, launch))
	const sessionReservationID = "model-session-reservation"
	const sessionRelativePath = "model-session.jsonl"
	require.NoError(t, durable.ReserveManagedSessionPath(ctx, store.ManagedPiSessionReservation{ReservationID: sessionReservationID, AgentID: fence.AgentID, SessionEpoch: fence.SessionEpoch, LaunchOperationID: launch.OperationID, RootRelativePath: sessionRelativePath}))
	require.NoError(t, durable.UpdateManagedAgentStarting(ctx, fence.AgentID, fence.PiSessionID, store.ProcessIdentityRecord{PID: fence.ProcessPID, StartMarker: fence.ProcessStartMarker, Incarnation: fence.ProcessIncarnation}, fence.RuntimeGeneration))
	require.NoError(t, durable.BindManagedPiSession(ctx, sessionReservationID, launch.OperationID, fence.PiSessionID, sessionRelativePath, fence))
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, fence, store.DefaultManagedReadinessPolicy()))
	snapshot := store.ManagedChildSnapshot{SnapshotID: "model-snapshot", OwnerSessionID: fence.PiSessionID, Sequence: 0, Canonical: []byte(`{"owner_session_id":"model-session","protocol":"mecha.children/v1","runs":[],"sequence":0,"snapshot_id":"model-snapshot"}`), SHA256: strings.Repeat("b", 64)}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, fence, snapshot))
	require.NoError(t, durable.MarkManagedReadyFromSources(ctx, fence))
	recipe, err := store.ManagedLaunchRecipeDigest(store.ManagedLaunchRecipe{PiExecutable: "/test/pi", SessionRoot: "/test/sessions", Cwd: agent.Cwd, Provider: agent.SelectedModel.Provider, ModelID: agent.SelectedModel.ID, MechaRPCExtension: "/test/mecha-rpc.ts", PiSubagentsExtension: "/test/subagents.ts", TestOnlyExtensions: []string{}, Environment: []string{}})
	require.NoError(t, err)
	require.NoError(t, durable.PutManagedLaunchRecipe(ctx, fence.AgentID, recipe))
	_, err = durable.UpdateManagedGoalPolicy(ctx, fence, store.ManagedGoalConfig{Enabled: true, Goal: "keep working", Settings: store.DefaultManagedGoalSettings()})
	require.NoError(t, err)
	lane := &managedAgentLane{}
	process := &launchProcess{identity: ProcessIdentity{PID: fence.ProcessPID, StartMarker: fence.ProcessStartMarker, Incarnation: fence.ProcessIncarnation}, stopped: make(chan struct{})}
	binding := &managedBinding{agentID: fence.AgentID, lane: lane, process: process, rpc: rpc, operationID: launch.OperationID, identity: process.identity, sessionID: fence.PiSessionID, sessionEpoch: fence.SessionEpoch, runtimeGeneration: fence.RuntimeGeneration, manifestRevision: fence.ManifestRevision, manifestHash: fence.ManifestHash}
	manager := &Manager{store: durable, catalog: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}, Model{Provider: "google", ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro", Available: true}), bindings: map[string]*managedBinding{fence.AgentID: binding}, lanes: map[string]*managedAgentLane{fence.AgentID: lane}, processes: map[string]Process{fence.AgentID: process}, rpcs: map[string]RPC{fence.AgentID: rpc}, operationIDs: map[string]string{fence.AgentID: launch.OperationID}, groupLeader: func(ProcessIdentity) bool { return true }}
	return manager, durable, fence, databasePath
}

func TestManagedRPCModelSwitchMapsMissingAgentToStoreContract(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, durable.Close()) })
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	manager, err := NewManager(ManagerOptions{Store: durable, Config: Config{Enabled: false}, Manifest: manifest})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close(ctx)) })

	_, err = manager.ChangeModel(ctx, ModelChangeRequest{AgentID: "missing-agent", Method: "PUT", CanonicalPath: "/v1/managed-agents/missing-agent/model", RequestID: "missing-model-change", CanonicalBodyHash: "missing-model-change-body", Model: Model{Provider: "google", ID: "gemini-2.5-pro"}})
	var missing *store.ManagedAgentNotFoundError
	require.ErrorAs(t, err, &missing)
	require.Equal(t, "missing-agent", missing.AgentID)
}

func TestFreshManagedRPCModelSwitchPreservesRuntimeAndNextPrompt(t *testing.T) {
	rpc := &modelSwitchRPC{current: Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, result: Model{Provider: "google", ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro", Available: true}}
	manager, durable, fence, _ := modelSwitchManagerFixture(t, rpc)
	before, err := durable.ManagedAgent(context.Background(), fence.AgentID)
	require.NoError(t, err)
	childrenBefore, err := durable.ManagedReadinessSource(context.Background(), fence.AgentID)
	require.NoError(t, err)
	goalBefore, err := durable.ManagedGoalPolicyFor(context.Background(), fence.AgentID)
	require.NoError(t, err)

	result, err := manager.ChangeModel(context.Background(), ModelChangeRequest{AgentID: fence.AgentID, Method: "PUT", CanonicalPath: "/v1/managed-agents/model-agent/model", RequestID: "model-change", CanonicalBodyHash: "model-change-body", Model: Model{Provider: "google", ID: "gemini-2.5-pro"}})
	require.NoError(t, err)
	require.NotNil(t, result.Operation)
	after, err := durable.ManagedAgent(context.Background(), fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, before.RuntimeGeneration, after.RuntimeGeneration)
	require.Equal(t, before.ProcessPID, after.ProcessPID)
	require.Equal(t, before.ProcessStartMarker, after.ProcessStartMarker)
	require.Equal(t, before.ProcessIncarnation, after.ProcessIncarnation)
	require.Equal(t, before.PiSessionID, after.PiSessionID)
	require.Equal(t, before.SessionEpoch, after.SessionEpoch)
	require.Equal(t, "running", after.LifecycleState)
	require.True(t, after.AcceptingPrompts)
	require.Equal(t, store.ManagedModel{Provider: "google", ID: "gemini-2.5-pro"}, after.SelectedModel)
	childrenAfter, err := durable.ManagedReadinessSource(context.Background(), fence.AgentID)
	require.NoError(t, err)
	goalAfter, err := durable.ManagedGoalPolicyFor(context.Background(), fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, childrenBefore, childrenAfter)
	require.Equal(t, goalBefore, goalAfter)
	require.NoError(t, manager.AdmitPrompt(context.Background(), fence.AgentID, "same stream"))
	require.Equal(t, []string{"same stream"}, rpc.prompts)
	require.Equal(t, 1, rpc.setCalls)
}

func TestManagedRPCModelSwitchSuccessUpdatesRecipeUsedByColdRecovery(t *testing.T) {
	ctx := context.Background()
	rpc := &modelSwitchRPC{current: Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, result: Model{Provider: "google", ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro", Available: true}}
	manager, durable, fence, databasePath := modelSwitchManagerFixture(t, rpc)
	beforeRecipe, err := durable.ManagedLaunchRecipe(ctx, fence.AgentID)
	require.NoError(t, err)
	request := ModelChangeRequest{AgentID: fence.AgentID, Method: "PUT", CanonicalPath: "/v1/managed-agents/model-agent/model", RequestID: "successful-cold-model", CanonicalBodyHash: "successful-cold-model-body", Model: Model{Provider: "google", ID: "gemini-2.5-pro"}}
	result, err := manager.ChangeModel(ctx, request)
	require.NoError(t, err)
	require.Equal(t, 200, result.Operation.ResponseStatus)
	updatedRecipe, err := durable.ManagedLaunchRecipe(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "google", updatedRecipe.Provider)
	require.Equal(t, "gemini-2.5-pro", updatedRecipe.ModelID)
	require.NotEqual(t, beforeRecipe.SHA256, updatedRecipe.SHA256)
	terminal, err := durable.FindManagedOperation(ctx, "PUT", request.RequestID)
	require.NoError(t, err)
	require.Equal(t, "terminal", terminal.State)

	_, err = durable.RecordManagedUnexpectedExit(ctx, fence, "", "test crash after model change")
	require.NoError(t, err)
	injector, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	_, err = injector.Exec(`UPDATE managed_crash_recovery SET retry_at='1970-01-01T00:00:00.000Z' WHERE agent_id=?`, fence.AgentID)
	require.NoError(t, err)
	require.NoError(t, injector.Close())
	crashed, err := durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "stopped", crashed.ActivityState)
	require.NotNil(t, crashed.LaunchRecipe)
	require.Equal(t, "gemini-2.5-pro", crashed.LaunchRecipe.ModelID)
	due, err := durable.ManagedCrashRecoveryDue(ctx, fence.AgentID)
	require.NoError(t, err)
	require.True(t, due)

	config, _ := managedLaunchConfigFixture(t)
	config.AllowManagedLaunchRecipeMismatch = true
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	var started CommandSpec
	recovery, err := NewManager(ManagerOptions{
		Store: durable, Config: config, Manifest: manifest,
		IDSource: deterministic.NewSequenceIDSource("new-model-recovery"),
		ProcessFactory: ProcessFactoryFunc(func(_ context.Context, spec CommandSpec) (Process, error) {
			started = spec
			return nil, noChildStartError{errors.New("capture cold recovery command")}
		}),
		ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true },
		ProcessGroupOps:            &coldRecoveryOps{states: map[int]ProcessProbeState{fence.ProcessPID: ProcessAbsent}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, recovery.Close(ctx)) })
	require.NoError(t, recovery.RecoverManagedRootColdStarts(ctx))
	require.Equal(t, "google", started.Provider)
	require.Equal(t, "gemini-2.5-pro", started.ModelID)
}

func TestManagedRPCModelSwitchConfirmedRejectionTerminalReplaysWithoutSecondRPC(t *testing.T) {
	ctx := context.Background()
	rpc := &modelSwitchRPC{current: Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, setErr: errors.New("model rejected")}
	manager, durable, fence, _ := modelSwitchManagerFixture(t, rpc)
	request := ModelChangeRequest{AgentID: fence.AgentID, Method: "PUT", CanonicalPath: "/v1/managed-agents/model-agent/model", RequestID: "rejected-change", CanonicalBodyHash: "rejected-body", Model: Model{Provider: "google", ID: "gemini-2.5-pro"}}

	result, err := manager.ChangeModel(ctx, request)
	require.NoError(t, err)
	require.NotNil(t, result.Operation)
	require.Equal(t, 409, result.Operation.ResponseStatus)
	require.Equal(t, 1, rpc.setCalls)
	agent, err := durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "gpt-5.6-terra", agent.SelectedModel.ID)
	require.True(t, agent.AcceptingPrompts)
	terminal, err := durable.FindManagedOperation(ctx, "PUT", request.RequestID)
	require.NoError(t, err)
	require.Equal(t, "terminal", terminal.State)
	require.Equal(t, 409, terminal.ResponseStatus)

	replay, err := manager.ChangeModel(ctx, request)
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	require.Equal(t, terminal.ResponseBody, replay.Operation.ResponseBody)
	require.Equal(t, 1, rpc.setCalls)
}

func TestManagedRPCModelSwitchAmbiguousMutationFailsAndTerminalizesAtomically(t *testing.T) {
	ctx := context.Background()
	rpc := &modelSwitchRPC{current: Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, result: Model{Provider: "google", ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro"}, setErr: errors.New("connection lost after write")}
	manager, durable, fence, _ := modelSwitchManagerFixture(t, rpc)
	request := ModelChangeRequest{AgentID: fence.AgentID, Method: "PUT", CanonicalPath: "/v1/managed-agents/model-agent/model", RequestID: "ambiguous-change", CanonicalBodyHash: "ambiguous-body", Model: Model{Provider: "google", ID: "gemini-2.5-pro"}}

	result, err := manager.ChangeModel(ctx, request)
	require.NoError(t, err)
	require.NotNil(t, result.Operation)
	require.Equal(t, 500, result.Operation.ResponseStatus)
	agent, err := durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "failed", agent.LifecycleState)
	require.False(t, agent.AcceptingPrompts)
	require.False(t, agent.SlotHeld)
	require.Equal(t, "MODEL_SWITCH_UNCONFIRMED", agent.LastErrorCode)
	require.Equal(t, "gpt-5.6-terra", agent.SelectedModel.ID)
	terminal, err := durable.FindManagedOperation(ctx, "PUT", request.RequestID)
	require.NoError(t, err)
	require.Equal(t, "terminal", terminal.State)
	require.Equal(t, 500, terminal.ResponseStatus)
	require.ErrorAs(t, manager.AdmitPrompt(ctx, fence.AgentID, "must remain closed"), new(*store.ManagedAgentNotReadyError))
}

func TestManagedRPCModelSwitchReservationFailureMakesZeroRPCMutation(t *testing.T) {
	rpc := &modelSwitchRPC{current: Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, result: Model{Provider: "google", ID: "gemini-2.5-pro"}}
	manager, _, fence, databasePath := modelSwitchManagerFixture(t, rpc)
	injector, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, injector.Close()) })
	_, err = injector.Exec(`CREATE TRIGGER fail_model_reservation BEFORE INSERT ON managed_http_operations WHEN NEW.operation_id='reservation-failure' BEGIN SELECT RAISE(ABORT, 'injected reservation failure'); END`)
	require.NoError(t, err)

	_, err = manager.ChangeModel(context.Background(), ModelChangeRequest{AgentID: fence.AgentID, Method: "PUT", CanonicalPath: "/v1/managed-agents/model-agent/model", RequestID: "reservation-failure", CanonicalBodyHash: "reservation-failure-body", Model: Model{Provider: "google", ID: "gemini-2.5-pro"}})
	require.Error(t, err)
	require.Zero(t, rpc.setCalls)
}

func TestPendingManagedModelFenceColdRecoveryFailsClosedBeforeLaunch(t *testing.T) {
	for _, testCase := range []struct {
		name           string
		runtimeMutated bool
	}{
		{name: "crash after reservation before RPC"},
		{name: "crash after RPC mutation before completion", runtimeMutated: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			rpc := &modelSwitchRPC{current: Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}}
			manager, durable, fence, _ := modelSwitchManagerFixture(t, rpc)
			if testCase.runtimeMutated {
				rpc.current = Model{Provider: "google", ID: "gemini-2.5-pro"}
			}
			pendingBody := []byte(`{"operation_id":"cold-model-change","status":"pending"}`)
			operation := store.ManagedOperation{OperationID: "cold-model-change", Method: "PUT", CanonicalPath: "/v1/managed-agents/model-agent/model", RequestID: "cold-model-change", CanonicalBodyHash: "cold-model-body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{"Content-Type": "application/json"}, ResponseBody: pendingBody}
			reserved, replayed, err := durable.ReserveManagedModelChange(ctx, store.ManagedModelChangeReservation{Fence: fence, Operation: operation})
			require.NoError(t, err)
			require.False(t, replayed)
			require.Equal(t, "pending", reserved.State)

			config, _ := managedLaunchConfigFixture(t)
			manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
			require.NoError(t, err)
			starts := 0
			recovery, err := NewManager(ManagerOptions{
				Store: durable, Config: config, Manifest: manifest,
				IDSource: deterministic.NewSequenceIDSource("unused-recovery-id"),
				ProcessFactory: ProcessFactoryFunc(func(context.Context, CommandSpec) (Process, error) {
					starts++
					return nil, noChildStartError{errors.New("pending model fence must suppress launch")}
				}),
				ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true },
				ProcessGroupOps:            &coldRecoveryOps{states: map[int]ProcessProbeState{fence.ProcessPID: ProcessAbsent}},
			})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, recovery.Close(ctx)) })
			_ = manager

			require.NoError(t, recovery.RecoverManagedRootColdStarts(ctx))
			require.Zero(t, starts)
			agent, err := durable.ManagedAgent(ctx, fence.AgentID)
			require.NoError(t, err)
			require.Equal(t, "failed", agent.LifecycleState)
			require.False(t, agent.AcceptingPrompts)
			require.False(t, agent.SlotHeld)
			require.Equal(t, "MODEL_CHANGE_RECOVERY_FAILED", agent.LastErrorCode)
			terminal, err := durable.FindManagedOperation(ctx, "PUT", "cold-model-change")
			require.NoError(t, err)
			require.Equal(t, "terminal", terminal.State)
			require.Equal(t, 500, terminal.ResponseStatus)
		})
	}
}

func TestManagedRPCModelSwitchRejectsUnavailableCatalogBeforeRPC(t *testing.T) {
	rpc := &modelSwitchRPC{current: Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}}
	manager, durable, fence, _ := modelSwitchManagerFixture(t, rpc)
	_, err := manager.ChangeModel(context.Background(), ModelChangeRequest{AgentID: fence.AgentID, Method: "PUT", CanonicalPath: "/v1/managed-agents/model-agent/model", RequestID: "unavailable-model", CanonicalBodyHash: "unavailable-model-body", Model: Model{Provider: "google", ID: "not-in-catalog"}})
	require.True(t, IsModelUnavailable(err))
	require.Zero(t, rpc.setCalls)
	agent, readErr := durable.ManagedAgent(context.Background(), fence.AgentID)
	require.NoError(t, readErr)
	require.Equal(t, "gpt-5.6-terra", agent.SelectedModel.ID)
	require.True(t, agent.AcceptingPrompts)
}

func TestManagedRPCModelSwitchCompletionFailureRetainsFenceAndColdRecoverySuppressesRelaunch(t *testing.T) {
	ctx := context.Background()
	rpc := &modelSwitchRPC{current: Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, result: Model{Provider: "google", ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro", Available: true}}
	manager, durable, fence, databasePath := modelSwitchManagerFixture(t, rpc)
	process := manager.processes[fence.AgentID].(*launchProcess)
	injector, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, injector.Close()) })
	_, err = injector.Exec(`CREATE TRIGGER fail_model_projection BEFORE UPDATE OF selected_model_provider,selected_model_id ON managed_agents BEGIN SELECT RAISE(ABORT, 'injected model completion failure'); END`)
	require.NoError(t, err)

	result, err := manager.ChangeModel(ctx, ModelChangeRequest{AgentID: fence.AgentID, Method: "PUT", CanonicalPath: "/v1/managed-agents/model-agent/model", RequestID: "persist-failure", CanonicalBodyHash: "persist-failure-body", Model: Model{Provider: "google", ID: "gemini-2.5-pro"}})
	var persistence *ModelStatePersistenceError
	require.ErrorAs(t, err, &persistence)
	require.ErrorContains(t, persistence.CommitErr, "injected model completion failure")
	require.NoError(t, persistence.TerminationErr)
	require.Nil(t, result.Operation)
	require.Equal(t, 1, process.Stops())
	_, bound := manager.bindingFor(fence.AgentID)
	require.False(t, bound)
	require.ErrorAs(t, manager.AdmitPrompt(ctx, fence.AgentID, "must remain closed"), new(*store.ManagedAgentNotReadyError))

	pending, err := durable.FindManagedOperation(ctx, "PUT", "persist-failure")
	require.NoError(t, err)
	require.Equal(t, "pending", pending.State)
	persisted, err := durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "running", persisted.LifecycleState)
	require.True(t, persisted.AcceptingPrompts)
	require.Equal(t, "gpt-5.6-terra", persisted.SelectedModel.ID)
	_, err = injector.Exec(`DROP TRIGGER fail_model_projection`)
	require.NoError(t, err)

	config, _ := managedLaunchConfigFixture(t)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	starts := 0
	recovery, err := NewManager(ManagerOptions{
		Store: durable, Config: config, Manifest: manifest,
		IDSource: deterministic.NewSequenceIDSource("unused-recovery-id"),
		ProcessFactory: ProcessFactoryFunc(func(context.Context, CommandSpec) (Process, error) {
			starts++
			return nil, noChildStartError{errors.New("pending model fence must suppress launch")}
		}),
		ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true },
		ProcessGroupOps:            &coldRecoveryOps{states: map[int]ProcessProbeState{fence.ProcessPID: ProcessAbsent}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, recovery.Close(ctx)) })
	require.NoError(t, recovery.RecoverManagedRootColdStarts(ctx))
	require.Zero(t, starts)
	failed, err := durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "failed", failed.LifecycleState)
	require.False(t, failed.AcceptingPrompts)
	require.False(t, failed.SlotHeld)
	terminal, err := durable.FindManagedOperation(ctx, "PUT", "persist-failure")
	require.NoError(t, err)
	require.Equal(t, "terminal", terminal.State)
	require.Equal(t, 500, terminal.ResponseStatus)
}
