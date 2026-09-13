package managed

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func readyGoalRecoveryFixture(t *testing.T) (*Manager, *store.Store, *managedBinding, store.ManagedRuntimeFence, *goalProjectionRPC, *deterministic.FixedClock) {
	t.Helper()
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC))
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{Clock: clock})
	require.NoError(t, err)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	launch := store.ManagedAgentRecord{AgentID: "goal-recovery-agent", Name: "goal-recovery-agent", SlotHeld: true, Cwd: "/tmp", SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "goal-recovery-session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: managedGoalProjectionTestManifestHash}
	op := store.ManagedOperation{OperationID: "goal-recovery-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "goal-recovery-launch", CanonicalBodyHash: "goal-recovery-body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, launch, op))
	require.NoError(t, durable.UpdateManagedAgentStarting(ctx, launch.AgentID, launch.PiSessionID, store.ProcessIdentityRecord{PID: 199, StartMarker: "goal-recovery-marker", Incarnation: "goal-recovery-incarnation"}, "goal-recovery-generation"))
	stored, err := durable.ManagedAgent(ctx, launch.AgentID)
	require.NoError(t, err)
	fence := storedAgentFence(stored)
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, fence, store.DefaultManagedReadinessPolicy()))
	snapshot := store.ManagedChildSnapshot{SnapshotID: "goal-recovery-snapshot", OwnerSessionID: fence.PiSessionID, Sequence: 0, Canonical: []byte(`{"owner_session_id":"goal-recovery-session","protocol":"mecha.children/v1","runs":[],"sequence":0,"snapshot_id":"goal-recovery-snapshot"}`), SHA256: managedGoalProjectionTestManifestHash}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, fence, snapshot))
	require.NoError(t, durable.MarkManagedReadyFromSources(ctx, fence))
	rpc := &goalProjectionRPC{launchRPC: &launchRPC{session: fence.PiSessionID, active: expectedManagedActiveTools(manifest)}}
	process := &launchProcess{identity: ProcessIdentity{PID: int(fence.ProcessPID), StartMarker: fence.ProcessStartMarker, Incarnation: fence.ProcessIncarnation}, stopped: make(chan struct{})}
	manager, err := NewManager(ManagerOptions{Store: durable, Config: Config{Enabled: false}, Manifest: manifest, ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: directControlProbeOps{state: ProcessOwned}})
	require.NoError(t, err)
	binding := &managedBinding{agentID: fence.AgentID, lane: &managedAgentLane{}, process: process, rpc: rpc, identity: process.identity, sessionID: fence.PiSessionID, sessionEpoch: fence.SessionEpoch, runtimeGeneration: fence.RuntimeGeneration, manifestRevision: fence.ManifestRevision, manifestHash: fence.ManifestHash}
	manager.bindings[fence.AgentID] = binding
	t.Cleanup(func() { _ = manager.Close(context.Background()); _ = durable.Close() })
	return manager, durable, binding, fence, rpc, clock
}

func TestGoalControlRecoveryDoesNotDuplicateRevisionOrContinuation(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, _, clock := readyGoalRecoveryFixture(t)

	var tool ManifestTool
	for _, candidate := range manager.manifest.Tools {
		if candidate.OperationID == "goal.control" {
			tool = candidate
			break
		}
	}
	require.NotEmpty(t, tool.Name)
	request := store.ManagedControlRequestInput{
		RequestID: "123e4567-e89b-42d3-a456-426614174030", ResultID: "123e4567-e89b-42d3-a456-426614174031", AgentID: fence.AgentID,
		ToolCallID: "goal-replay-call", CapabilityID: tool.CapabilityID, ToolName: tool.Name, DefinitionID: tool.DefinitionID,
		OperationID: tool.OperationID, CanonicalArgumentsSHA256: strings.Repeat("c", 64), RequestFingerprint: strings.Repeat("d", 64), Fence: fence,
		ReceivedAt: "2026-09-14T12:00:00.000Z",
	}
	_, err := durable.ReserveManagedControlRequest(ctx, request)
	require.NoError(t, err)
	claim, err := durable.ClaimManagedControlHandler(ctx, request.RequestID, "123e4567-e89b-42d3-a456-426614174032", fence)
	require.NoError(t, err)

	handler, available := manager.rootControlHandlers.Handler(tool)
	require.True(t, available)
	result, err := manager.executeRootControlHandler(ctx, binding, handler, RootControlInvocation{
		RequestID: request.RequestID, ResultID: request.ResultID, ToolCallID: request.ToolCallID, OperationID: request.OperationID,
		Arguments: map[string]any{"action": "set", "goal": "finish the recovered goal"}, Fence: fence,
	}, false)
	require.NoError(t, err)
	require.Equal(t, "ok", result.Status)
	// Leave the side effect committed, as if the process stopped before it
	// could complete the durable request row.
	require.NoError(t, durable.CommitManagedControlSideEffect(ctx, request.RequestID, claim.Owner, fence))
	// Recovery only transfers an expired handler lease. Advance the
	// controllable store clock past its exact thirty-second deadline.
	require.NoError(t, clock.Advance(31*time.Second))

	before, err := durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
	require.NoError(t, err)
	require.EqualValues(t, 1, before.DesiredRevision)
	require.EqualValues(t, 1, before.AppliedRevision)

	manager.resumeRootControl(ctx, binding, request.RequestID, nil)
	recovered, err := durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
	require.NoError(t, err)
	require.EqualValues(t, before.DesiredRevision, recovered.DesiredRevision)
	require.EqualValues(t, before.AppliedRevision, recovered.AppliedRevision)
	require.Equal(t, "idle", recovered.EvaluatorState)
	require.Zero(t, recovered.EvaluatorAttempts)
	require.Empty(t, recovered.WakeupMessageID)
	row, err := durable.ManagedControlRequest(ctx, request.RequestID)
	require.NoError(t, err)
	require.Equal(t, "completed", row.State)

	replayed, err := durable.ReserveManagedControlRequest(ctx, request)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	manager.resumeRootControl(ctx, binding, request.RequestID, nil)
	afterReplay, err := durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
	require.NoError(t, err)
	require.EqualValues(t, recovered.DesiredRevision, afterReplay.DesiredRevision)
	require.EqualValues(t, recovered.AppliedRevision, afterReplay.AppliedRevision)
	require.Empty(t, afterReplay.WakeupMessageID)
}

func TestRPCClientStartsGoalEvaluationWithFencedHiddenNoToolPolicy(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	t.Cleanup(func() { _ = client.Close() })
	requests := readJSONLRequests(stdinReader)
	wantTools := []string{"bash", "edit", "goal.control", "read"}
	client.mu.Lock()
	client.activeTools = append([]string(nil), wantTools...)
	client.mu.Unlock()
	fence := store.ManagedRuntimeFence{AgentID: "agent-a", PiSessionID: "session-a", SessionEpoch: 2, ProcessPID: 3, ProcessStartMarker: "start", ProcessIncarnation: "incarnation", RuntimeGeneration: "generation", ManifestRevision: 1, ManifestHash: strings.Repeat("a", 64)}
	policy := store.ManagedGoalPolicy{EvaluatorID: "evaluation", EvaluatorGoalRevision: 4, EvaluatorSettledSequence: 9, Config: store.ManagedGoalConfig{Goal: "finish the goal"}}
	started := make(chan error, 1)
	go func() { started <- client.StartManagedGoalEvaluation(context.Background(), fence, policy) }()
	request := requireRequest(t, requests)
	command, ok := request["message"].(string)
	require.True(t, ok)
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(command, "/mecha-policy-apply-v1 "))
	require.NoError(t, err)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(raw, &envelope))
	require.Equal(t, map[string]any{}, envelope["policy"])
	require.Equal(t, fence.RuntimeGeneration, envelope["runtime_generation"])
	require.EqualValues(t, fence.SessionEpoch, envelope["session_epoch"])
	evaluator := envelope["evaluator"].(map[string]any)
	require.Equal(t, policy.EvaluatorID, evaluator["evaluation_id"])
	require.Equal(t, policy.Config.Goal, evaluator["goal"])
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": request["id"], "type": "response", "success": true, "data": map[string]any{"success": true, "command": "prompt"}}))
	require.NoError(t, <-started)
	active, err := client.GetActiveTools(context.Background())
	require.NoError(t, err)
	require.Equal(t, wantTools, active)
}

func TestRPCClientGoalEvaluationKeepsExactToolSnapshotAcrossTerminalOutcomes(t *testing.T) {
	wantTools := []string{"bash", "edit", "goal.control", "read"}
	fence := store.ManagedRuntimeFence{AgentID: "agent-a", PiSessionID: "session-a", SessionEpoch: 2, ProcessPID: 3, ProcessStartMarker: "start", ProcessIncarnation: "incarnation", RuntimeGeneration: "generation", ManifestRevision: 1, ManifestHash: strings.Repeat("a", 64)}

	for _, test := range []struct {
		name             string
		outcome, reason  string
		wantOutcome      string
		wantReason       string
		wantErr          string
		cancelBeforeWait bool
		replacement      bool
	}{
		{name: "DONE", outcome: "DONE", reason: "complete", wantOutcome: store.ManagedGoalEvaluationCompleted, wantReason: "complete"},
		{name: "NOT_DONE", outcome: store.ManagedGoalEvaluationNotDone, reason: "continue", wantOutcome: store.ManagedGoalEvaluationNotDone, wantReason: "continue"},
		{name: "malformed output", outcome: "MALFORMED", reason: "invalid", wantErr: "managed goal evaluation result is invalid"},
		{name: "timeout", wantErr: context.DeadlineExceeded.Error(), cancelBeforeWait: true},
		{name: "cancel", wantErr: context.Canceled.Error(), cancelBeforeWait: true},
		{name: "replacement", outcome: "DONE", reason: "replacement runtime", wantOutcome: store.ManagedGoalEvaluationCompleted, wantReason: "replacement runtime", replacement: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			stdoutReader, stdoutWriter := io.Pipe()
			client := NewRPCClient(io.Discard, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
			t.Cleanup(func() { _ = client.Close() })
			client.mu.Lock()
			client.activeTools = append([]string(nil), wantTools...)
			client.goalEvaluationToken = &managedGoalEvaluationToken{evaluationID: "evaluation", goalRevision: 4, settled: 9, fence: fence}
			client.mu.Unlock()

			if test.replacement {
				// The replacement runtime owns the current token. Deliver the old
				// runtime's result first, then a matching replacement result. The
				// backlog checks prove the stale result reached the client and was
				// consumed before the current result settled the wait.
				replacementFence := fence
				replacementFence.PiSessionID = "replacement-session"
				replacementFence.SessionEpoch++
				replacementFence.ProcessPID++
				replacementFence.ProcessStartMarker = "replacement-start"
				replacementFence.ProcessIncarnation = "replacement-incarnation"
				replacementFence.RuntimeGeneration = "replacement-generation"
				client.mu.Lock()
				client.goalEvaluationToken = &managedGoalEvaluationToken{evaluationID: "evaluation", goalRevision: 4, settled: 9, fence: replacementFence}
				client.mu.Unlock()

				require.NoError(t, writeRPC(stdoutWriter, managedGoalEvaluationReceipt(fence, "evaluation", 4, 9, test.outcome, "old runtime")))
				require.Eventually(t, func() bool {
					client.mu.Lock()
					defer client.mu.Unlock()
					if len(client.backlog) != 1 {
						return false
					}
					details := receiptDetails(client.backlog[0].value)
					return details != nil && details["runtime_generation"] == fence.RuntimeGeneration
				}, time.Second, time.Millisecond)
				require.NoError(t, writeRPC(stdoutWriter, managedGoalEvaluationReceipt(replacementFence, "evaluation", 4, 9, test.outcome, test.reason)))
				require.Eventually(t, func() bool {
					client.mu.Lock()
					defer client.mu.Unlock()
					return len(client.backlog) == 2
				}, time.Second, time.Millisecond)
			} else if !test.cancelBeforeWait {
				require.NoError(t, writeRPC(stdoutWriter, managedGoalEvaluationReceipt(fence, "evaluation", 4, 9, test.outcome, test.reason)))
			}

			waitCtx := context.Background()
			var cancel context.CancelFunc
			if test.cancelBeforeWait {
				if test.name == "timeout" {
					waitCtx, cancel = context.WithDeadline(context.Background(), time.Unix(0, 0))
				} else {
					waitCtx, cancel = context.WithCancel(context.Background())
					cancel()
				}
				defer cancel()
			}
			outcome, reason, err := client.WaitForManagedGoalEvaluation(waitCtx)
			if test.wantErr != "" {
				require.EqualError(t, err, test.wantErr)
			} else {
				require.NoError(t, err)
				require.Equal(t, test.wantOutcome, outcome)
				require.Equal(t, test.wantReason, reason)
			}
			client.mu.Lock()
			require.Nil(t, client.goalEvaluationToken)
			if test.replacement {
				require.Empty(t, client.backlog, "stale old-generation result must be consumed")
			}
			client.mu.Unlock()
			active, err := client.GetActiveTools(context.Background())
			require.NoError(t, err)
			require.Equal(t, wantTools, active)
		})
	}
}

type failedInlineAllowanceRPC struct {
	*inlineAllowanceRPC
	fail bool
}

func (r *failedInlineAllowanceRPC) ApplyPolicy(ctx context.Context, envelope map[string]any) (map[string]any, error) {
	if r.fail {
		return nil, errors.New("inline allowance projection failed")
	}
	return r.inlineAllowanceRPC.ApplyPolicy(ctx, envelope)
}

func TestInlineAllowanceRestoresOnlyMatchingFence(t *testing.T) {
	ctx := context.Background()
	manager, binding, fence, baseRPC, _ := readyInlineAllowanceFixture(t)
	require.NoError(t, manager.SetManagedInlineTools(ctx, fence.AgentID, true))
	rpc := &failedInlineAllowanceRPC{inlineAllowanceRPC: baseRPC}
	binding.rpc = rpc
	arguments := map[string]any{"toolName": "bash", "usageCount": int64(2)}
	mutations := []struct {
		name   string
		mutate func(*store.ManagedRuntimeFence)
	}{
		{name: "agent", mutate: func(value *store.ManagedRuntimeFence) { value.AgentID = "other-agent" }},
		{name: "session", mutate: func(value *store.ManagedRuntimeFence) { value.PiSessionID = "other-session" }},
		{name: "epoch", mutate: func(value *store.ManagedRuntimeFence) { value.SessionEpoch++ }},
		{name: "pid", mutate: func(value *store.ManagedRuntimeFence) { value.ProcessPID++ }},
		{name: "start marker", mutate: func(value *store.ManagedRuntimeFence) { value.ProcessStartMarker = "other-start" }},
		{name: "incarnation", mutate: func(value *store.ManagedRuntimeFence) { value.ProcessIncarnation = "other-incarnation" }},
		{name: "generation", mutate: func(value *store.ManagedRuntimeFence) { value.RuntimeGeneration = "other-generation" }},
		{name: "manifest revision", mutate: func(value *store.ManagedRuntimeFence) { value.ManifestRevision++ }},
		{name: "manifest hash", mutate: func(value *store.ManagedRuntimeFence) { value.ManifestHash = strings.Repeat("c", 64) }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			stale := fence
			mutation.mutate(&stale)
			_, err := manager.executeManagedInlineAllowance(ctx, binding, RootControlInvocation{OperationID: "policy.inline_allowance", Arguments: arguments, Fence: stale})
			require.Error(t, err)
			require.Empty(t, inlineCreditsForTest(baseRPC))
		})
	}

	rpc.fail = true
	_, err := manager.executeManagedInlineAllowance(ctx, binding, RootControlInvocation{OperationID: "policy.inline_allowance", Arguments: arguments, Fence: fence})
	require.Error(t, err)
	require.Empty(t, inlineCreditsForTest(baseRPC))

	rpc.fail = false
	result, err := manager.executeManagedInlineAllowance(ctx, binding, RootControlInvocation{OperationID: "policy.inline_allowance", Arguments: arguments, Fence: fence})
	require.NoError(t, err)
	require.Equal(t, "ok", result.Status)
	require.Equal(t, int64(2), inlineCreditsForTest(baseRPC)["bash"])

	stale := fence
	stale.RuntimeGeneration = "stale-generation"
	_, err = manager.executeManagedInlineAllowance(ctx, binding, RootControlInvocation{OperationID: "policy.inline_allowance", Arguments: map[string]any{"toolName": "bash", "usageCount": int64(5)}, Fence: stale})
	require.Error(t, err)
	require.Equal(t, int64(2), inlineCreditsForTest(baseRPC)["bash"])
}

func managedGoalEvaluationReceipt(fence store.ManagedRuntimeFence, evaluationID string, goalRevision, settled int64, outcome, reason string) map[string]any {
	return map[string]any{"type": "message_end", "message": map[string]any{"customType": "mecha.policy.applied.v1", "details": map[string]any{
		"protocol": "mecha.policy/v1", "manifest_sha256": fence.ManifestHash, "runtime_generation": fence.RuntimeGeneration, "session_epoch": fence.SessionEpoch,
		"evaluator": map[string]any{"evaluation_id": evaluationID, "goal_revision": goalRevision, "settled_sequence": settled, "phase": "result", "outcome": outcome, "reason": reason},
	}}}
}

func inlineCreditsForTest(rpc *inlineAllowanceRPC) map[string]any {
	inline, _ := rpc.policy["inline"].(map[string]any)
	credits, _ := inline["credits"].(map[string]any)
	return credits
}
