package managed

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

const managedGoalProjectionTestManifestHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type goalProjectionRPC struct {
	*launchRPC
	revision         int64
	policy           map[string]any
	stale            bool
	fail             bool
	directBodies     chan string
	evaluation       func(context.Context, store.ManagedGoalPolicy) (string, string, error)
	evaluationStarts chan struct{}
	startErr         error
	cleanupErr       error
	cleanupCalls     chan struct{}
	decodedSequence  uint64
	activity         func(store.ManagedGoalActivity)
}

func (r *goalProjectionRPC) ApplyPolicy(ctx context.Context, envelope map[string]any) (map[string]any, error) {
	if r.fail {
		return nil, errors.New("projection unavailable")
	}
	policy, _ := envelope["policy"].(map[string]any)
	r.policy = policy
	goal, _ := policy["goal"].(map[string]any)
	revision, _ := goal["revision"].(int64)
	if !r.stale {
		r.revision = revision
	}
	_, err := r.launchRPC.ApplyPolicy(ctx, envelope)
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "command": "prompt"}, nil
}

func (r *goalProjectionRPC) SetQualifiedGoalActivityEventHandler(handler func(store.ManagedGoalActivity)) {
	r.activity = handler
}

func (r *goalProjectionRPC) PrepareManagedMailbox(context.Context) (string, error) {
	return "goal-wakeup-cursor", nil
}
func (r *goalProjectionRPC) PromptManagedMailbox(_ context.Context, attempt store.ManagedDirectAttempt) (<-chan DirectMailboxEvent, error) {
	if r.directBodies != nil {
		r.directBodies <- attempt.Message.Body
	}
	events := make(chan DirectMailboxEvent, 2)
	events <- DirectMailboxEvent{Receipt: &DirectMailboxReceipt{EntryIdentity: "goal-wakeup-receipt"}}
	events <- DirectMailboxEvent{Settled: true}
	close(events)
	return events, nil
}
func (r *goalProjectionRPC) RecoverManagedMailbox(context.Context, store.ManagedDirectAttempt) (DirectMailboxRecovery, error) {
	return DirectMailboxRecovery{}, nil
}
func (r *goalProjectionRPC) StartManagedGoalEvaluation(context.Context, store.ManagedRuntimeFence, store.ManagedGoalPolicy) error {
	if r.evaluationStarts != nil {
		r.evaluationStarts <- struct{}{}
	}
	return r.startErr
}
func (r *goalProjectionRPC) ReconcileManagedGoalEvaluation(context.Context, store.ManagedRuntimeFence, string, int64, int64) error {
	if r.cleanupCalls != nil {
		r.cleanupCalls <- struct{}{}
	}
	return r.cleanupErr
}
func (r *goalProjectionRPC) ManagedGoalEvaluationCleanupPending() bool { return true }
func (r *goalProjectionRPC) ParentActivitySequence() uint64 {
	return r.decodedSequence
}

func (r *goalProjectionRPC) WaitForManagedGoalEvaluation(ctx context.Context) (string, string, error) {
	if r.evaluation == nil {
		return "", "", errors.New("no evaluator result")
	}
	return r.evaluation(ctx, store.ManagedGoalPolicy{})
}

type goalActivityRPC struct {
	*goalProjectionRPC
	source *rpcClient
}

func (r *goalActivityRPC) SetParentActivityEventHandler(handler func(map[string]any)) {
	r.source.SetParentActivityEventHandler(handler)
}

func (r *goalActivityRPC) ParentActivitySequence() uint64 {
	return r.source.ParentActivitySequence()
}

func (r *goalProjectionRPC) WaitForPolicyApplied(ctx context.Context, generation string, epoch int64) (map[string]any, error) {
	if r.fail {
		return nil, errors.New("projection unavailable")
	}
	receipt, err := r.launchRPC.WaitForPolicyApplied(ctx, generation, epoch)
	if err != nil {
		return nil, err
	}
	if r.policy != nil {
		inline, _ := r.policy["inline"].(map[string]any)
		recall, _ := r.policy["recall"].(map[string]any)
		settled, _ := r.policy["settled"].(map[string]any)
		goalLock, _ := r.policy["goal_lock"].(map[string]any)
		compaction, _ := r.policy["compaction"].(map[string]any)
		receipt["applied"] = map[string]any{
			"inline_enabled": inline["enabled"], "inline_credits": inline["credits"], "recall_content": recall["content"],
			"settled_enabled": settled["enabled"], "goal_lock_enabled": goalLock["enabled"], "compaction_deferred": compaction["deferred"],
		}
	}
	applied := receipt["applied"].(map[string]any)
	if r.revision > 0 {
		applied["goal_revision"] = r.revision
	}
	return receipt, nil
}

func readyGoalProjectionFixture(t *testing.T) (*Manager, *store.Store, *managedBinding, store.ManagedRuntimeFence, *goalProjectionRPC) {
	t.Helper()
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	launch := store.ManagedAgentRecord{AgentID: "goal-agent", Name: "goal-agent", SlotHeld: true, Cwd: "/tmp", SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "goal-session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: managedGoalProjectionTestManifestHash}
	op := store.ManagedOperation{OperationID: "goal-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "goal-launch", CanonicalBodyHash: "goal-body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, launch, op))
	require.NoError(t, durable.UpdateManagedAgentStarting(ctx, launch.AgentID, launch.PiSessionID, store.ProcessIdentityRecord{PID: 99, StartMarker: "goal-marker", Incarnation: "goal-incarnation"}, "123e4567-e89b-42d3-a456-426614174100"))
	stored, err := durable.ManagedAgent(ctx, launch.AgentID)
	require.NoError(t, err)
	fence := storedAgentFence(stored)
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, fence, store.DefaultManagedReadinessPolicy()))
	snapshot := store.ManagedChildSnapshot{SnapshotID: "goal-snapshot", OwnerSessionID: fence.PiSessionID, Sequence: 0, Canonical: []byte(`{"owner_session_id":"goal-session","protocol":"mecha.children/v1","runs":[],"sequence":0,"snapshot_id":"goal-snapshot"}`), SHA256: managedGoalProjectionTestManifestHash}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, fence, snapshot))
	require.NoError(t, durable.MarkManagedReadyFromSources(ctx, fence))

	rpc := &goalProjectionRPC{launchRPC: &launchRPC{session: fence.PiSessionID, active: expectedManagedActiveTools(manifest)}}
	process := &launchProcess{identity: ProcessIdentity{PID: int(fence.ProcessPID), StartMarker: fence.ProcessStartMarker, Incarnation: fence.ProcessIncarnation}, stopped: make(chan struct{})}
	manager, err := NewManager(ManagerOptions{Store: durable, Config: Config{Enabled: false}, Manifest: manifest, ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: directControlProbeOps{state: ProcessOwned}})
	require.NoError(t, err)
	binding := &managedBinding{agentID: fence.AgentID, lane: &managedAgentLane{}, process: process, rpc: rpc, identity: process.identity, sessionID: fence.PiSessionID, sessionEpoch: fence.SessionEpoch, runtimeGeneration: fence.RuntimeGeneration, manifestRevision: fence.ManifestRevision, manifestHash: fence.ManifestHash}
	manager.bindings[fence.AgentID] = binding
	t.Cleanup(func() { _ = manager.Close(context.Background()); _ = durable.Close() })
	return manager, durable, binding, fence, rpc
}

func readyGoalActivityFixture(t *testing.T) (*Manager, *store.Store, *managedBinding, store.ManagedRuntimeFence, *goalActivityRPC, *io.PipeWriter) {
	t.Helper()
	manager, durable, binding, fence, baseRPC := readyGoalProjectionFixture(t)
	stdoutReader, stdoutWriter := io.Pipe()
	source := NewRPCClient(io.Discard, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	rpc := &goalActivityRPC{goalProjectionRPC: baseRPC, source: source}
	binding.rpc = rpc
	t.Cleanup(func() { require.NoError(t, source.Close()) })
	return manager, durable, binding, fence, rpc, stdoutWriter
}

type managedGoalStatusRootControlRPC struct {
	*goalProjectionRPC
	handler   func(RootControlEvent)
	submitted chan map[string]any
}

type managedGoalStatusResultToken struct{}

func (managedGoalStatusResultToken) Watermark() uint64 { return 0 }
func (managedGoalStatusResultToken) Release()          {}

func (r *managedGoalStatusRootControlRPC) SetRootControlEventHandler(handler func(RootControlEvent)) {
	r.handler = handler
}

func (r *managedGoalStatusRootControlRPC) ArmRootControlResult(string, string) (RootControlAppendToken, error) {
	return managedGoalStatusResultToken{}, nil
}

func (r *managedGoalStatusRootControlRPC) SubmitRootControlResult(_ context.Context, _ RootControlAppendToken, payload map[string]any) (<-chan RootControlResultReceipt, error) {
	r.submitted <- payload
	details := make(map[string]any, len(payload)-1)
	for key, value := range payload {
		if key != "content" {
			details[key] = value
		}
	}
	receipts := make(chan RootControlResultReceipt, 1)
	receipts <- RootControlResultReceipt{Event: map[string]any{"id": "goal-status-result", "customType": "mecha.control.result.v1", "display": false, "content": payload["content"], "details": details}, Watermark: 1}
	close(receipts)
	return receipts, nil
}

func (r *managedGoalStatusRootControlRPC) ReconcileRootControlResult(ctx context.Context, payload map[string]any) (RootControlPersistedReceipt, error) {
	requestID, _ := payload["request_id"].(string)
	resultID, _ := payload["result_id"].(string)
	generation, _ := payload["runtime_generation"].(string)
	manifest, _ := payload["manifest_sha256"].(string)
	epoch, _ := readinessInteger(payload["target_session_epoch"])
	canonical, _ := canonicalJSON(payload)
	hash := fmt.Sprintf("%x", sha256.Sum256(canonical))
	return RootControlPersistedReceipt{
		ReceiptID:         "goal-status-receipt",
		RequestID:         requestID,
		ResultID:          resultID,
		PayloadSHA256:     hash,
		RuntimeGeneration: generation,
		SessionEpoch:      epoch,
		ManifestSHA256:    manifest,
	}, nil
}

func (r *managedGoalStatusRootControlRPC) GetRootControlEntry(context.Context, string) (map[string]any, error) {
	return nil, errors.New("goal status result lookup should not be needed")
}

func TestManagedGoalStatusCanonicalizesNullableSettings(t *testing.T) {
	for _, test := range []struct {
		name, wantTurnLimit, wantBudget string
		turnLimit, budget               *int64
	}{
		{name: "nil optional settings", wantTurnLimit: "null", wantBudget: "null"},
		{name: "integer optional settings", wantTurnLimit: "3", wantBudget: "300000", turnLimit: int64Pointer(3), budget: int64Pointer(300_000)},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			manager, durable, binding, fence, _ := readyGoalProjectionFixture(t)
			_, err := durable.UpdateManagedGoalPolicy(ctx, fence, store.ManagedGoalConfig{Enabled: true, Goal: "finish the migration", Settings: store.ManagedGoalSettings{SnoozeEnabled: true, SnoozeDurationMS: 60_000, SnoozeTurnLimit: test.turnLimit, SnoozeBudgetCapMS: test.budget, SnoozeRefillYieldMS: 30_000, SnoozeRefillElapsedMS: 60_000}})
			require.NoError(t, err)

			result, err := manager.executeManagedGoalControl(ctx, binding, RootControlInvocation{RequestID: "123e4567-e89b-42d3-a456-426614174020", OperationID: "goal.control", Arguments: map[string]any{"action": "status"}, Fence: fence})
			require.NoError(t, err)
			canonical, err := canonicalJSON(result.Details)
			require.NoError(t, err)
			require.JSONEq(t, `{"goal_state":{"enabled":true,"goal":"finish the migration","desired_revision":1,"applied_revision":0,"projection_state":"pending","goal_lock_enabled":false,"snooze_enabled":true,"snooze_duration_ms":60000,"snooze_turn_limit":`+test.wantTurnLimit+`,"snooze_budget_cap_ms":`+test.wantBudget+`,"snooze_refill_yield_ms":30000,"snooze_refill_elapsed_ms":60000}}`, string(canonical))
		})
	}
}

func TestManagedGoalStatusDynamicRootControlPersistsAndDeliversSuccess(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, baseRPC := readyGoalProjectionFixture(t)
	rpc := &managedGoalStatusRootControlRPC{goalProjectionRPC: baseRPC, submitted: make(chan map[string]any, 1)}
	binding.rpc = rpc
	manager.installRootControlReceiver(binding)
	_, err := durable.UpdateManagedGoalPolicy(ctx, fence, store.ManagedGoalConfig{Enabled: true, Goal: "finish the migration", Settings: store.DefaultManagedGoalSettings()})
	require.NoError(t, err)

	var tool ManifestTool
	for _, candidate := range manager.manifest.Tools {
		if candidate.OperationID == "goal.control" {
			tool = candidate
			break
		}
	}
	require.NotEmpty(t, tool.Name)
	requestID := "123e4567-e89b-42d3-a456-426614174021"
	rpc.handler(RootControlEvent{RequestID: requestID, ToolCallID: "goal-status-call", CapabilityID: tool.CapabilityID, ToolName: tool.Name, DefinitionID: tool.DefinitionID, DefinitionSHA256: tool.DefinitionSHA256, OperationID: tool.OperationID, ManifestSHA256: fence.ManifestHash, RuntimeGeneration: fence.RuntimeGeneration, SessionEpoch: fence.SessionEpoch, Arguments: map[string]any{"action": "status"}})

	var payload map[string]any
	select {
	case payload = <-rpc.submitted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for goal status delivery")
	}
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	var delivered map[string]any
	require.NoError(t, json.Unmarshal(encoded, &delivered))
	require.Equal(t, requestID, delivered["request_id"])
	require.Equal(t, "ok", delivered["status"])
	require.Equal(t, "Goal is active.", delivered["content"])
	deliveredDetails, err := json.Marshal(delivered["details"])
	require.NoError(t, err)
	require.JSONEq(t, `{"goal_state":{"enabled":true,"goal":"finish the migration","desired_revision":1,"applied_revision":0,"projection_state":"pending","goal_lock_enabled":false,"snooze_enabled":true,"snooze_duration_ms":60000,"snooze_turn_limit":null,"snooze_budget_cap_ms":null,"snooze_refill_yield_ms":30000,"snooze_refill_elapsed_ms":60000}}`, string(deliveredDetails))
	require.Eventually(t, func() bool {
		row, lookupErr := durable.ManagedControlRequest(ctx, requestID)
		return lookupErr == nil && row.State == "delivered"
	}, time.Second, time.Millisecond)
	row, err := durable.ManagedControlRequest(ctx, requestID)
	require.NoError(t, err)
	require.Equal(t, "ok", row.ResultStatus)
	require.Empty(t, row.ErrorCode)
	require.JSONEq(t, `{"goal_state":{"enabled":true,"goal":"finish the migration","desired_revision":1,"applied_revision":0,"projection_state":"pending","goal_lock_enabled":false,"snooze_enabled":true,"snooze_duration_ms":60000,"snooze_turn_limit":null,"snooze_budget_cap_ms":null,"snooze_refill_yield_ms":30000,"snooze_refill_elapsed_ms":60000}}`, row.ResultDetailsJSON)
}

func int64Pointer(value int64) *int64 { return &value }

func TestManagedParentActivityDropsDecodedPriorTurnAfterAdmission(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, rpc, stdoutWriter := readyGoalActivityFixture(t)
	rpc.evaluation = func(context.Context, store.ManagedGoalPolicy) (string, string, error) {
		return store.ManagedGoalEvaluationNotDone, "keep working", nil
	}
	rpc.evaluationStarts = make(chan struct{}, 1)
	rpc.directBodies = make(chan string, 1)
	_, err := manager.executeManagedGoalControl(ctx, binding, RootControlInvocation{
		RequestID: "123e4567-e89b-42d3-a456-426614174030", OperationID: "goal.control",
		Arguments: map[string]any{"action": "set", "goal": "finish the migration"}, Fence: fence,
	})
	require.NoError(t, err)
	beforeVersion, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	beforeAgent, err := durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)

	callbackEntered := make(chan struct{})
	callbackRelease := make(chan struct{})
	ParentCompactionRPC(rpc.source).SetParentCompactionEventHandler(func(map[string]any) {
		close(callbackEntered)
		<-callbackRelease
	})
	manager.installParentActivityReceiver(binding)
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "compaction_start", "reason": "manual"}))
	select {
	case <-callbackEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for semantic FIFO blocker")
	}
	// These native parent frames are decoded and sequenced while the earlier
	// semantic callback is blocked. Their callbacks have not run yet.
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "agent_start"}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "agent_settled"}))
	require.Eventually(t, func() bool { return rpc.ParentActivitySequence() == 3 }, time.Second, time.Millisecond)

	manager.installParentActivityReceiver(binding)
	require.NoError(t, manager.AdmitPrompt(ctx, fence.AgentID, "new prompt"))
	binding.serial.Lock()
	require.True(t, binding.turnAdmitted)
	require.False(t, binding.turnStarted)
	require.Equal(t, uint64(1), binding.turnGeneration)
	require.Equal(t, int64(3), binding.turnAdmissionSequence)
	binding.serial.Unlock()
	close(callbackRelease)
	require.Eventually(t, func() bool {
		binding.serial.Lock()
		defer binding.serial.Unlock()
		return binding.lastParentActivitySequence == 3
	}, time.Second, time.Millisecond)
	binding.serial.Lock()
	require.True(t, binding.turnAdmitted, "delayed prior active/idle must not clear the new turn")
	require.False(t, binding.turnStarted, "delayed prior active must not start the new turn")
	binding.serial.Unlock()
	afterAgent, err := durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	afterVersion, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, beforeAgent.ActivityState, afterAgent.ActivityState)
	require.Equal(t, beforeVersion, afterVersion, "stale active/idle must not apply durable activity")
	require.Never(t, func() bool {
		select {
		case <-rpc.evaluationStarts:
			return true
		default:
			return false
		}
	}, 100*time.Millisecond, time.Millisecond)
	require.Never(t, func() bool {
		select {
		case <-rpc.directBodies:
			return true
		default:
			return false
		}
	}, 100*time.Millisecond, time.Millisecond)

	// A post-admission active/idle pair follows the same installed production
	// callback and must start, settle, and evaluate the current turn.
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "agent_start"}))
	require.Eventually(t, func() bool {
		agent, lookupErr := durable.ManagedAgent(ctx, fence.AgentID)
		binding.serial.Lock()
		started := binding.turnStarted
		binding.serial.Unlock()
		return lookupErr == nil && agent.ActivityState == "active" && started
	}, time.Second, time.Millisecond)
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "agent_settled"}))
	require.Eventually(t, func() bool {
		binding.serial.Lock()
		admitted := binding.turnAdmitted
		binding.serial.Unlock()
		agent, lookupErr := durable.ManagedAgent(ctx, fence.AgentID)
		return lookupErr == nil && !admitted && agent.ActivityState == "idle"
	}, 2*time.Second, time.Millisecond)
	select {
	case <-rpc.evaluationStarts:
	case <-time.After(2 * time.Second):
		t.Fatal("current settled turn did not evaluate")
	}
	select {
	case <-rpc.directBodies:
	case <-time.After(2 * time.Second):
		t.Fatal("current settled turn did not wake the goal")
	}
}

func TestManagedParentActivityGenerationChangeDropsQueuedWorker(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, rpc, stdoutWriter := readyGoalActivityFixture(t)
	manager.installParentActivityReceiver(binding)
	binding.lane.serial.Lock()
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "agent_start"}))
	require.Eventually(t, func() bool {
		binding.serial.Lock()
		defer binding.serial.Unlock()
		return binding.lastParentActivitySequence == 1
	}, time.Second, time.Millisecond)
	// The callback has enqueued its worker, which is held behind lane.serial.
	require.NoError(t, manager.AdmitPrompt(ctx, fence.AgentID, "new prompt"))
	binding.lane.serial.Unlock()
	require.Eventually(t, func() bool {
		binding.serial.Lock()
		defer binding.serial.Unlock()
		return binding.turnAdmitted && !binding.turnStarted
	}, time.Second, time.Millisecond)
	agent, err := durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", agent.ActivityState, "queued prior event must be dropped after generation changes")
	require.Nil(t, rpc.evaluationStarts)
}

func TestManagedGoalEvaluatorDoesNotCompleteClaimWhenCleanupFails(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, rpc := readyGoalProjectionFixture(t)
	_, err := manager.executeManagedGoalControl(ctx, binding, RootControlInvocation{
		RequestID: "123e4567-e89b-42d3-a456-426614174090", OperationID: "goal.control",
		Arguments: map[string]any{"action": "set", "goal": "finish the migration"}, Fence: fence,
	})
	require.NoError(t, err)
	rpc.startErr = errors.New("prompt response timeout")
	rpc.cleanupErr = errors.New("abort response timeout")
	rpc.cleanupCalls = make(chan struct{}, 1)
	manager.evaluateManagedGoalAtSettled(binding, fence, 1)
	select {
	case <-rpc.cleanupCalls:
	case <-time.After(time.Second):
		t.Fatal("evaluator cleanup was not attempted after Start failed")
	}
	policy, err := durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "evaluating", policy.EvaluatorState)
	require.Zero(t, policy.NotDoneWakeups)
	require.Equal(t, 0, len(rpc.directBodies))
	stored, err := durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "running", stored.LifecycleState, "the lane cleanup is still responsible for the exact process")
}

func TestManagedGoalEvaluatorCleanupStopFailureRetainsExactBindingAndClaim(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, rpc := readyGoalProjectionFixture(t)
	_, err := manager.executeManagedGoalControl(ctx, binding, RootControlInvocation{
		RequestID: "123e4567-e89b-42d3-a456-426614174091", OperationID: "goal.control",
		Arguments: map[string]any{"action": "set", "goal": "finish the migration"}, Fence: fence,
	})
	require.NoError(t, err)
	rpc.startErr = errors.New("prompt response timeout")
	rpc.cleanupErr = errors.New("abort response timeout")
	rpc.cleanupCalls = make(chan struct{}, 1)
	process := binding.process.(*launchProcess)
	process.stopErr = errors.New("stop failed")
	manager.evaluateManagedGoalAtSettled(binding, fence, 1)
	select {
	case <-rpc.cleanupCalls:
	case <-time.After(time.Second):
		t.Fatal("evaluator cleanup was not attempted after Start failed")
	}
	require.Eventually(t, func() bool {
		stored, lookupErr := durable.ManagedAgent(ctx, fence.AgentID)
		binding.serial.Lock()
		retained := binding.retainBinding
		binding.serial.Unlock()
		return lookupErr == nil && stored.LastErrorCode == "PROCESS_STOP_FAILED" && retained
	}, time.Second, time.Millisecond)
	policy, err := durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "evaluating", policy.EvaluatorState)
	require.Zero(t, policy.NotDoneWakeups)
	stored, err := durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Contains(t, stored.LastErrorMessage, "abort response timeout")
	require.Contains(t, stored.LastErrorMessage, "stop failed")
}

func TestManagedGoalEvaluatorCompletesSettledBoundaryBeforeSourceBackedWakeup(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, rpc := readyGoalProjectionFixture(t)
	rpc.directBodies = make(chan string, 1)
	rpc.evaluation = func(context.Context, store.ManagedGoalPolicy) (string, string, error) {
		return store.ManagedGoalEvaluationNotDone, "keep working", nil
	}
	_, err := manager.executeManagedGoalControl(ctx, binding, RootControlInvocation{
		RequestID: "123e4567-e89b-42d3-a456-426614174000", OperationID: "goal.control",
		Arguments: map[string]any{"action": "set", "goal": "finish the migration"}, Fence: fence,
	})
	require.NoError(t, err)

	_, err = durable.ApplyManagedParentActivity(ctx, fence, store.ManagedParentActivityEvent{Sequence: 1, State: "active"})
	require.NoError(t, err)
	require.NoError(t, manager.AdmitPrompt(ctx, fence.AgentID, "new prompt"))

	manager.handleParentActivityEvent(binding, map[string]any{"sequence": int64(2), "state": "active"})
	manager.handleParentActivityEvent(binding, map[string]any{"sequence": int64(3), "state": "idle"})
	require.Eventually(t, func() bool {
		policy, lookupErr := durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
		return lookupErr == nil && policy.LastOutcome == store.ManagedGoalEvaluationNotDone && policy.NotDoneWakeups == 1
	}, 2*time.Second, 10*time.Millisecond)
	select {
	case body := <-rpc.directBodies:
		require.Equal(t, "NOT_DONE: Review finish the migration and take the next concrete action now.", body)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for source-backed managed goal wakeup")
	}

	// The same settled source sequence is rejected by durable claim state, so a
	// receipt/delivery retry cannot create a timer-style NOT_DONE loop.
	manager.handleParentActivityEvent(binding, map[string]any{"sequence": int64(1), "state": "idle"})
	select {
	case body := <-rpc.directBodies:
		t.Fatalf("duplicate settled boundary woke goal again: %s", body)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestDelayedOldParentActivityCallbacksAreDroppedAfterPromptAdmission(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, rpc := readyGoalProjectionFixture(t)
	rpc.decodedSequence = 5
	rpc.directBodies = make(chan string, 1)
	rpc.evaluationStarts = make(chan struct{}, 1)
	rpc.evaluation = func(context.Context, store.ManagedGoalPolicy) (string, string, error) {
		return store.ManagedGoalEvaluationNotDone, "keep working", nil
	}
	_, err := manager.executeManagedGoalControl(ctx, binding, RootControlInvocation{
		RequestID: "123e4567-e89b-42d3-a456-426614174030", OperationID: "goal.control",
		Arguments: map[string]any{"action": "set", "goal": "finish the migration"}, Fence: fence,
	})
	require.NoError(t, err)
	_, err = durable.ApplyManagedParentActivity(ctx, fence, store.ManagedParentActivityEvent{Sequence: 5, State: "active"})
	require.NoError(t, err)
	beforeVersion, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)

	// The callbacks have already been admitted to the worker queue. Holding the
	// lane keeps their durable work behind the next prompt admission while their
	// captured generation remains the old one.
	binding.lane.serial.Lock()
	manager.handleParentActivityEvent(binding, map[string]any{"sequence": int64(6), "state": "active"})
	manager.handleParentActivityEvent(binding, map[string]any{"sequence": int64(7), "state": "idle"})
	require.NoError(t, manager.AdmitPrompt(ctx, fence.AgentID, "new prompt"))
	binding.lane.serial.Unlock()

	require.Eventually(t, func() bool {
		agent, lookupErr := durable.ManagedAgent(ctx, fence.AgentID)
		return lookupErr == nil && agent.ActivityState == "active"
	}, time.Second, time.Millisecond)
	binding.serial.Lock()
	require.True(t, binding.turnAdmitted, "stale callbacks must not clear the new prompt marker")
	require.False(t, binding.turnStarted, "stale callbacks must not start the new prompt")
	binding.serial.Unlock()
	require.Never(t, func() bool {
		select {
		case <-rpc.directBodies:
			return true
		case <-rpc.evaluationStarts:
			return true
		default:
			return false
		}
	}, 100*time.Millisecond, 5*time.Millisecond)
	agent, err := durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "active", agent.ActivityState)
	afterVersion, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, beforeVersion, afterVersion, "stale callbacks must not apply durable activity")
}

func TestCurrentParentActivityActiveAndIdleStillSettleAndEvaluate(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, rpc := readyGoalProjectionFixture(t)
	rpc.decodedSequence = 5
	rpc.directBodies = make(chan string, 1)
	rpc.evaluationStarts = make(chan struct{}, 1)
	rpc.evaluation = func(context.Context, store.ManagedGoalPolicy) (string, string, error) {
		return store.ManagedGoalEvaluationNotDone, "keep working", nil
	}
	_, err := manager.executeManagedGoalControl(ctx, binding, RootControlInvocation{
		RequestID: "123e4567-e89b-42d3-a456-426614174031", OperationID: "goal.control",
		Arguments: map[string]any{"action": "set", "goal": "finish the migration"}, Fence: fence,
	})
	require.NoError(t, err)
	_, err = durable.ApplyManagedParentActivity(ctx, fence, store.ManagedParentActivityEvent{Sequence: 5, State: "active"})
	require.NoError(t, err)
	require.NoError(t, manager.AdmitPrompt(ctx, fence.AgentID, "new prompt"))

	manager.handleParentActivityEvent(binding, map[string]any{"sequence": int64(6), "state": "active"})
	require.Eventually(t, func() bool {
		binding.serial.Lock()
		started := binding.turnStarted
		binding.serial.Unlock()
		return started
	}, time.Second, time.Millisecond)
	manager.handleParentActivityEvent(binding, map[string]any{"sequence": int64(7), "state": "idle"})

	require.Eventually(t, func() bool {
		agent, lookupErr := durable.ManagedAgent(ctx, fence.AgentID)
		return lookupErr == nil && agent.ActivityState == "idle"
	}, time.Second, time.Millisecond)
	binding.serial.Lock()
	require.False(t, binding.turnAdmitted, "matching idle must clear the prompt marker")
	binding.serial.Unlock()
	select {
	case <-rpc.evaluationStarts:
	case <-time.After(time.Second):
		t.Fatal("matching idle did not start goal evaluation")
	}
	select {
	case body := <-rpc.directBodies:
		require.Equal(t, "NOT_DONE: Review finish the migration and take the next concrete action now.", body)
	case <-time.After(time.Second):
		t.Fatal("matching idle did not wake the goal")
	}
}

func TestManagedGoalNotDoneWakeupCannotRetargetReplacement(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, rpc := readyGoalProjectionFixture(t)
	rpc.directBodies = make(chan string, 1)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	rpc.evaluation = func(context.Context, store.ManagedGoalPolicy) (string, string, error) {
		once.Do(func() { close(started) })
		<-release
		return store.ManagedGoalEvaluationNotDone, "keep working", nil
	}
	_, err := manager.executeManagedGoalControl(ctx, binding, RootControlInvocation{RequestID: "123e4567-e89b-42d3-a456-426614174006", OperationID: "goal.control", Arguments: map[string]any{"action": "set", "goal": "finish the migration"}, Fence: fence})
	require.NoError(t, err)

	_, err = durable.ApplyManagedParentActivity(ctx, fence, store.ManagedParentActivityEvent{Sequence: 1, State: "active"})
	require.NoError(t, err)
	require.NoError(t, manager.AdmitPrompt(ctx, fence.AgentID, "new prompt"))

	manager.handleParentActivityEvent(binding, map[string]any{"sequence": int64(2), "state": "active"})
	manager.handleParentActivityEvent(binding, map[string]any{"sequence": int64(3), "state": "idle"})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("evaluator did not start")
	}

	// Replacement wins the live binding before the old evaluator returns. The
	// old evaluation still commits its immutable reservation, but its wakeup is
	// never offered to the replacement.
	// Do not copy managedBinding: its serializers are intentionally per binding.
	replacement := &managedBinding{agentID: binding.agentID, lane: binding.lane, process: binding.process, rpc: binding.rpc, identity: binding.identity, sessionID: binding.sessionID, sessionEpoch: binding.sessionEpoch, runtimeGeneration: "replacement-generation", manifestRevision: binding.manifestRevision, manifestHash: binding.manifestHash}
	manager.mu.Lock()
	manager.bindings[fence.AgentID] = replacement
	manager.mu.Unlock()
	close(release)
	require.Eventually(t, func() bool {
		policy, lookupErr := durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
		return lookupErr == nil && policy.NotDoneWakeups == 1 && policy.WakeupMessageID != ""
	}, time.Second, 10*time.Millisecond)
	select {
	case body := <-rpc.directBodies:
		t.Fatalf("replacement received stale wakeup: %s", body)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestManagedGoalDirectControlPersistsSharedSettingsBeforeProjection(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, rpc := readyGoalProjectionFixture(t)
	settings := map[string]any{
		"goal_lock_enabled": true, "snooze_enabled": true, "snooze_duration_ms": int64(120_000),
		"snooze_turn_limit": int64(3), "snooze_budget_cap_ms": int64(300_000),
		"snooze_refill_yield_ms": int64(30_000), "snooze_refill_elapsed_ms": int64(60_000),
	}
	result, err := manager.DirectRootControl(ctx, fence.AgentID, "123e4567-e89b-42d3-a456-426614174003", "goal.control", map[string]any{"action": "set", "goal": "finish the migration", "settings": settings})
	require.NoError(t, err)
	state, stateOK := result.Details["goal_state"].(map[string]any)
	require.True(t, stateOK)
	require.Equal(t, true, state["goal_lock_enabled"])
	require.Equal(t, int64(120_000), state["snooze_duration_ms"])
	policy, err := durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, true, policy.Config.Settings.GoalLockEnabled)
	require.NotNil(t, policy.Config.Settings.SnoozeTurnLimit)
	require.EqualValues(t, 3, *policy.Config.Settings.SnoozeTurnLimit)
	projected := rpc.policy["goal"].(map[string]any)["settings"].(map[string]any)
	require.Equal(t, true, projected["goalLockEnabled"])
	require.Equal(t, int64(120_000), projected["snoozeDurationMs"])

	_, err = manager.executeManagedGoalControl(ctx, binding, RootControlInvocation{RequestID: "123e4567-e89b-42d3-a456-426614174004", OperationID: "goal.control", Arguments: map[string]any{"action": "set", "goal": "invalid settings", "settings": map[string]any{}}, Fence: fence})
	require.Error(t, err)
	stored, err := durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
	require.NoError(t, err)
	require.EqualValues(t, 1, stored.DesiredRevision)
}

func TestManagedGoalSnoozeSchedulesOneFencedOrdinaryWakeup(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, rpc := readyGoalProjectionFixture(t)
	rpc.directBodies = make(chan string, 2)
	settings := map[string]any{"goal_lock_enabled": false, "snooze_enabled": true, "snooze_duration_ms": int64(1), "snooze_turn_limit": nil, "snooze_budget_cap_ms": nil, "snooze_refill_yield_ms": int64(30_000), "snooze_refill_elapsed_ms": int64(60_000)}
	_, err := manager.executeManagedGoalControl(ctx, binding, RootControlInvocation{RequestID: "123e4567-e89b-42d3-a456-426614174011", OperationID: "goal.control", Arguments: map[string]any{"action": "set", "goal": "finish the migration", "settings": settings}, Fence: fence})
	require.NoError(t, err)

	result, err := manager.executeManagedGoalSnooze(ctx, binding, RootControlInvocation{RequestID: "123e4567-e89b-42d3-a456-426614174012", OperationID: "goal.snooze", Arguments: map[string]any{}, Fence: fence})
	require.NoError(t, err)
	require.Equal(t, "ok", result.Status)
	policy, err := durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "snoozed", policy.EvaluatorState)

	select {
	case body := <-rpc.directBodies:
		require.Equal(t, "NOT_DONE: Review finish the migration and take the next concrete action now.", body)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for timer-backed snooze wakeup")
	}
	select {
	case body := <-rpc.directBodies:
		t.Fatalf("duplicate snooze wakeup: %s", body)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestManagedGoalMeaningfulActivityRearmsOnlyQualifiedEvents(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, rpc := readyGoalProjectionFixture(t)
	manager.installQualifiedGoalActivityReceiver(binding)
	require.NotNil(t, rpc.activity)
	_, err := manager.executeManagedGoalControl(ctx, binding, RootControlInvocation{RequestID: "123e4567-e89b-42d3-a456-426614174013", OperationID: "goal.control", Arguments: map[string]any{"action": "set", "goal": "finish the migration"}, Fence: fence})
	require.NoError(t, err)
	for sequence := int64(1); sequence <= 1_000; sequence++ {
		_, claimed, claimErr := durable.ClaimManagedGoalEvaluation(ctx, fence, sequence)
		require.NoError(t, claimErr)
		require.True(t, claimed)
		_, changed, completeErr := durable.CompleteManagedGoalEvaluation(ctx, fence, sequence, store.ManagedGoalEvaluationNotDone)
		require.NoError(t, completeErr)
		require.True(t, changed)
	}

	manager.recordManagedGoalActivity(binding, fence, managedGoalActivity{Kind: "routine", ID: "ignored"})
	stale := fence
	stale.ProcessIncarnation = "old-incarnation"
	for index := 0; index < 3; index++ {
		manager.recordManagedGoalActivity(binding, stale, managedGoalActivity{Kind: "tool", ID: fmt.Sprintf("stale-%d", index)})
	}
	require.Never(t, func() bool {
		policy, lookupErr := durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
		return lookupErr == nil && policy.EvaluatorState == "idle"
	}, 50*time.Millisecond, 5*time.Millisecond)
	for index := 0; index < 3; index++ {
		rpc.activity(store.ManagedGoalActivity{Kind: "builtin", ID: fmt.Sprintf("builtin-%d", index)})
	}
	require.Eventually(t, func() bool {
		policy, lookupErr := durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
		return lookupErr == nil && policy.EvaluatorState == "idle" && policy.NotDoneWakeups == 0
	}, time.Second, 10*time.Millisecond)
}

func TestManagedGoalProjectionAppliesCurrentRevisionAndKeepsFailurePending(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, rpc := readyGoalProjectionFixture(t)
	result, err := manager.executeManagedGoalControl(ctx, binding, RootControlInvocation{RequestID: "123e4567-e89b-42d3-a456-426614174001", OperationID: "goal.control", Arguments: map[string]any{"action": "set", "goal": "finish the migration"}, Fence: fence})
	require.NoError(t, err)
	require.Equal(t, "ok", result.Status)
	state, stateOK := result.Details["goal_state"].(map[string]any)
	require.True(t, stateOK)
	require.Equal(t, true, state["enabled"])
	policy, err := durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
	require.NoError(t, err)
	require.EqualValues(t, 1, policy.DesiredRevision)
	require.EqualValues(t, 1, policy.AppliedRevision)

	rpc.revision = 1 // A delayed receipt from revision one cannot settle revision two.
	rpc.stale = true
	pending, err := manager.executeManagedGoalControl(ctx, binding, RootControlInvocation{RequestID: "123e4567-e89b-42d3-a456-426614174002", OperationID: "goal.control", Arguments: map[string]any{"action": "set", "goal": "finish the validation"}, Fence: fence})
	require.NoError(t, err)
	require.Equal(t, "ok", pending.Status)
	pendingState, pendingStateOK := pending.Details["goal_state"].(map[string]any)
	require.True(t, pendingStateOK)
	require.Equal(t, "pending", pendingState["projection_state"])
	policy, err = durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
	require.NoError(t, err)
	require.EqualValues(t, 2, policy.DesiredRevision)
	require.EqualValues(t, 1, policy.AppliedRevision)
	require.Equal(t, "pending", policy.ProjectionState)
	require.Equal(t, "GOAL_PROJECTION_FAILED", policy.LastErrorCode)

	// The failed post-commit projection is retried from the durable revision;
	// it does not create a third desired revision or rerun a mutable control.
	rpc.stale = false
	require.Eventually(t, func() bool {
		current, lookupErr := durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
		return lookupErr == nil && current.DesiredRevision == 2 && current.AppliedRevision == 2 && current.ProjectionState == "applied"
	}, 3*time.Second, 10*time.Millisecond)
}

func TestManagedGoalLockAndWorkflowStateSynchronization(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, _ := readyGoalProjectionFixture(t)

	// 1. Worker goal(action="set") permitted when no workflow state
	resSet, err := manager.executeManagedGoalControl(ctx, binding, RootControlInvocation{
		RequestID:   "123e4567-e89b-42d3-a456-426614174080",
		OperationID: "goal.control",
		Arguments:   map[string]any{"action": "set", "goal": "initial freeform goal"},
		Fence:       fence,
	})
	require.NoError(t, err)
	require.Equal(t, "ok", resSet.Status)

	// 2. Create workflow states:
	// - StateWithGoal: has goal "State locked goal"
	// - StateWithoutGoal: has empty goal ""
	stateWithGoal, err := durable.CreateWorkflowState(ctx, store.WorkflowStateRequest{
		Name:        "LockedPhase",
		Description: "Phase with locked goal",
		Goal:        "State locked goal",
	})
	require.NoError(t, err)

	stateWithoutGoal, err := durable.CreateWorkflowState(ctx, store.WorkflowStateRequest{
		Name:        "FreePhase",
		Description: "Phase without goal",
		Goal:        "",
	})
	require.NoError(t, err)

	// 3. Set active workflow state to StateWithGoal and verify synchronization + lock
	require.NoError(t, durable.SetManagedAgentActiveWorkflowState(ctx, fence.AgentID, &stateWithGoal.ID))
	goalSettings := store.DefaultManagedGoalSettings()
	goalSettings.GoalLockEnabled = true
	_, err = durable.UpdateManagedGoalConfig(ctx, fence.AgentID, store.ManagedGoalConfig{
		Enabled:  true,
		Goal:     stateWithGoal.Goal,
		Settings: goalSettings,
	})
	require.NoError(t, err)

	policy, err := durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
	require.NoError(t, err)
	require.True(t, policy.Config.Enabled)
	require.Equal(t, "State locked goal", policy.Config.Goal)
	require.True(t, policy.Config.Settings.GoalLockEnabled)

	// 4. Worker goal(action="set") rejected with lock error when state has goal
	_, err = manager.executeManagedGoalControl(ctx, binding, RootControlInvocation{
		RequestID:   "123e4567-e89b-42d3-a456-426614174081",
		OperationID: "goal.control",
		Arguments:   map[string]any{"action": "set", "goal": "overriding goal"},
		Fence:       fence,
	})
	require.Error(t, err)
	require.Equal(t, fmt.Sprintf("goal is locked by active workflow state %q", stateWithGoal.Name), err.Error())

	// 5. Worker goal(action="off") rejected with lock error when state has goal
	_, err = manager.executeManagedGoalControl(ctx, binding, RootControlInvocation{
		RequestID:   "123e4567-e89b-42d3-a456-426614174082",
		OperationID: "goal.control",
		Arguments:   map[string]any{"action": "off"},
		Fence:       fence,
	})
	require.Error(t, err)
	require.Equal(t, fmt.Sprintf("goal is locked by active workflow state %q", stateWithGoal.Name), err.Error())

	// 6. Worker goal(action="status") permitted when state has goal
	resStatus, err := manager.executeManagedGoalControl(ctx, binding, RootControlInvocation{
		RequestID:   "123e4567-e89b-42d3-a456-426614174083",
		OperationID: "goal.control",
		Arguments:   map[string]any{"action": "status"},
		Fence:       fence,
	})
	require.NoError(t, err)
	require.Equal(t, "ok", resStatus.Status)
	require.Equal(t, "Goal is active.", resStatus.Content)

	// 7. Transition to state with empty goal clears agent goal and unlocks it
	require.NoError(t, durable.SetManagedAgentActiveWorkflowState(ctx, fence.AgentID, &stateWithoutGoal.ID))
	goalSettingsUnlocked := store.DefaultManagedGoalSettings()
	goalSettingsUnlocked.GoalLockEnabled = false
	_, err = durable.UpdateManagedGoalConfig(ctx, fence.AgentID, store.ManagedGoalConfig{
		Enabled:  false,
		Goal:     "",
		Settings: goalSettingsUnlocked,
	})
	require.NoError(t, err)

	policy, err = durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
	require.NoError(t, err)
	require.False(t, policy.Config.Enabled)
	require.Empty(t, policy.Config.Goal)
	require.False(t, policy.Config.Settings.GoalLockEnabled)

	// 8. Worker goal(action="set") permitted when state has empty goal
	resSetEmptyState, err := manager.executeManagedGoalControl(ctx, binding, RootControlInvocation{
		RequestID:   "123e4567-e89b-42d3-a456-426614174084",
		OperationID: "goal.control",
		Arguments:   map[string]any{"action": "set", "goal": "worker goal in free state"},
		Fence:       fence,
	})
	require.NoError(t, err)
	require.Equal(t, "ok", resSetEmptyState.Status)

	// 9. Clear workflow state completely (exit)
	require.NoError(t, durable.SetManagedAgentActiveWorkflowState(ctx, fence.AgentID, nil))
	_, err = durable.UpdateManagedGoalConfig(ctx, fence.AgentID, store.ManagedGoalConfig{
		Enabled:  false,
		Goal:     "",
		Settings: goalSettingsUnlocked,
	})
	require.NoError(t, err)

	policy, err = durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
	require.NoError(t, err)
	require.False(t, policy.Config.Enabled)
	require.Empty(t, policy.Config.Goal)
	require.False(t, policy.Config.Settings.GoalLockEnabled)

	// 10. Worker goal(action="set") permitted when no workflow state
	resSetNoState, err := manager.executeManagedGoalControl(ctx, binding, RootControlInvocation{
		RequestID:   "123e4567-e89b-42d3-a456-426614174085",
		OperationID: "goal.control",
		Arguments:   map[string]any{"action": "set", "goal": "worker goal after exit"},
		Fence:       fence,
	})
	require.NoError(t, err)
	require.Equal(t, "ok", resSetNoState.Status)
}
