package managed

import (
	"context"
	"encoding/json"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"testing"
	"time"

	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// SCN-GWS-002/003: exercise real idle admission, SDK failure and SQLite
// policy with the existing deterministic clock; only the provider is replaced.
func TestGoalWakeSafetyFailedIdleAttemptsArePaced(t *testing.T) {
	f := newFailureFixture(t)
	fail := func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
		f.calls.Add(1)
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "deterministic backend failure"}}, ai.StopError)
	}
	steps := []providers.FauxResponseStep{f.response("startup settled")}
	for range 64 {
		steps = append(steps, fail)
	}
	f.reg.SetResponses(steps)
	owner := f.launch(t, "start")
	_, err := f.m.directEmbeddedBrowserGoal(context.Background(), owner.binding.AgentID, "goal.control", map[string]any{"action": "set", "goal": "keep working despite backend outage"}, nil)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return f.calls.Load() >= 2 }, 3*time.Second, time.Millisecond, "first automatic wake must reach failure fixture")
	f.laneBarrier(t, owner.binding.AgentID)
	require.EqualValues(t, 2, f.calls.Load(), "one initial turn and one immediate automatic attempt")
	for range 5 {
		require.NoError(t, f.m.ResumeIdleEmbeddedGoals(context.Background()))
		f.m.MaybeResumeEmbeddedGoal(owner.binding.AgentID)
	}
	// A marker admitted through the same lane joins already queued idle work.
	f.laneBarrier(t, owner.binding.AgentID)
	require.Never(t, func() bool { return f.calls.Load() > 2 }, 150*time.Millisecond, time.Millisecond,
		"failed automatic attempt must not retry while the deterministic clock remains at its settlement time")
	require.NoError(t, f.clock.Advance(9999*time.Millisecond))
	require.NoError(t, f.m.ResumeIdleEmbeddedGoals(context.Background()))
	f.laneBarrier(t, owner.binding.AgentID)
	require.EqualValues(t, 2, f.calls.Load(), "no retry at T+9999ms")
	require.NoError(t, f.clock.Advance(time.Millisecond))
	require.NoError(t, f.m.ResumeIdleEmbeddedGoals(context.Background()))
	require.Eventually(t, func() bool { return f.calls.Load() == 3 }, 3*time.Second, time.Millisecond, "one retry eligible at T+10000ms")
	for attempt := 3; attempt <= 20; attempt++ {
		f.laneBarrier(t, owner.binding.AgentID)
		require.NoError(t, f.clock.Advance(10*time.Second))
		require.NoError(t, f.m.ResumeIdleEmbeddedGoals(context.Background()))
		require.Eventually(t, func() bool { return f.calls.Load() == int32(attempt+1) }, 3*time.Second, time.Millisecond)
	}
	f.laneBarrier(t, owner.binding.AgentID)
	for range 3 {
		require.NoError(t, f.clock.Advance(10*time.Second))
		require.NoError(t, f.m.ResumeIdleEmbeddedGoals(context.Background()))
	}
	require.Never(t, func() bool { return f.calls.Load() > 21 }, 150*time.Millisecond, time.Millisecond, "twenty automatic attempts includes the first, not twenty retries")
	policy, err := f.db.ManagedGoalPolicyFor(context.Background(), owner.binding.AgentID)
	require.NoError(t, err)
	require.True(t, policy.Config.Enabled, "failure cap must not clear the goal")
	require.NotEqual(t, "done", policy.EvaluatorState)
}

// SCN-GWS-004/006: a failed startup must not leave its predecessor cached.
// Corrupt SDK bytes model an unreadable persisted session, not a provider error.
func TestGoalWakeSafetyFailedRecoveryEvictsOriginOwner(t *testing.T) {
	ctx := context.Background()
	f := newFailureFixture(t)
	f.reg.SetResponses([]providers.FauxResponseStep{f.response("startup settled"), f.response("must not dispatch")})
	origin := f.launch(t, "start")
	saved, err := f.db.ManagedAgent(ctx, origin.binding.AgentID)
	require.NoError(t, err)
	_, err = f.raw.Exec(`UPDATE embedded_entries SET body=X'FF' WHERE agent_id=? AND kind='assistant'`, origin.binding.AgentID)
	require.NoError(t, err)
	recovery := productionPolicyManager(t, f.db, f.root, f.reg)
	t.Cleanup(func() { require.NoError(t, recovery.Close(ctx)) })
	recovery.mu.Lock()
	recovery.embeddedOwners = map[string]embeddedOwner{origin.binding.AgentID: origin}
	recovery.mu.Unlock()
	var oldID string
	require.NoError(t, f.raw.QueryRow(`SELECT owner_id FROM embedded_sessions WHERE agent_id=? AND current=1`, origin.binding.AgentID).Scan(&oldID))
	recovery.startEmbeddedRoot(*saved, saved.LaunchOperationID, true)
	require.Eventually(t, func() bool {
		var id string
		err := f.raw.QueryRow(`SELECT owner_id FROM embedded_sessions WHERE agent_id=? AND current=1`, origin.binding.AgentID).Scan(&id)
		return err == nil && id != oldID
	}, 3*time.Second, time.Millisecond, "recovery must acquire successor B after cached owner A")
	lane := recovery.laneFor(origin.binding.AgentID)
	lane.serial.Lock()
	lane.serial.Unlock()
	recovery.mu.Lock()
	selected, exists := recovery.embeddedOwners[origin.binding.AgentID]
	recovery.mu.Unlock()
	require.False(t, exists && selected == origin, "failed recovery B must evict origin A, not just compare cache against B")
	require.EqualValues(t, 1, f.calls.Load(), "unreadable recovery must fail before provider dispatch")
}

// SCN-GWS-007: cancelling after a failed automatic turn overrides stale idle
// notifications even after the retry deadline has passed.
func TestGoalWakeSafetyGoalOffCancelsFailedRetry(t *testing.T) {
	f := newFailureFixture(t)
	fail := func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
		f.calls.Add(1)
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "backend unavailable"}}, ai.StopError)
	}
	f.reg.SetResponses([]providers.FauxResponseStep{f.response("startup settled"), fail, fail, fail})
	owner := f.launch(t, "start")
	ctx := context.Background()
	_, err := f.m.directEmbeddedBrowserGoal(ctx, owner.binding.AgentID, "goal.control", map[string]any{"action": "set", "goal": "continue until cancelled"}, nil)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return f.calls.Load() == 2 }, 3*time.Second, time.Millisecond)
	f.laneBarrier(t, owner.binding.AgentID)
	_, err = f.m.directEmbeddedBrowserGoal(ctx, owner.binding.AgentID, "goal.control", map[string]any{"action": "off"}, nil)
	require.NoError(t, err)
	for range 3 {
		require.NoError(t, f.clock.Advance(10*time.Second))
		require.NoError(t, f.m.ResumeIdleEmbeddedGoals(ctx))
		f.m.MaybeResumeEmbeddedGoal(owner.binding.AgentID)
		f.laneBarrier(t, owner.binding.AgentID)
	}
	require.Never(t, func() bool { return f.calls.Load() > 2 }, 150*time.Millisecond, time.Millisecond)
	policy, err := f.db.ManagedGoalPolicyFor(ctx, owner.binding.AgentID)
	require.NoError(t, err)
	require.False(t, policy.Config.Enabled)
}

// SCN-GWS-004/005/006: passive delivery is not an operator recovery action.
func TestGoalWakeSafetyPassiveMailPreservesOwnerHold(t *testing.T) {
	f := newFailureFixture(t)
	f.reg.SetResponses([]providers.FauxResponseStep{f.response("passive mail received"), f.response("user recovery received")})
	owner := f.launch(t, "")
	lane := f.m.laneFor(owner.binding.AgentID)
	lane.serial.Lock()
	lane.goalOwnerInvalid = true
	lane.consecutiveContinuations.Store(20)
	lane.serial.Unlock()
	f.enqueue(t, "f1111111-1111-4111-8111-111111111111")
	f.m.dispatchEmbeddedMail(owner.binding.AgentID)
	f.laneBarrier(t, owner.binding.AgentID)
	lane.serial.Lock()
	held := lane.goalOwnerInvalid
	lane.serial.Unlock()
	if !held {
		t.Error("passive mailbox delivery must not clear an automatic ownership hold")
	}
	require.NoError(t, f.m.AdmitPromptWithRequestID(context.Background(), owner.binding.AgentID, "deliberately resume useful work", "f2222222-2222-4222-8222-222222222222"))
	f.laneBarrier(t, owner.binding.AgentID)
	lane.serial.Lock()
	defer lane.serial.Unlock()
	require.False(t, lane.goalOwnerInvalid, "a deliberate admitted user prompt with valid ownership may recover")
	require.Zero(t, lane.consecutiveContinuations.Load())
}

// SCN-GWS-003/005/006: actual freshly admitted goal tool execution cannot
// renew its automatic caller's budget; manual execution retains reset semantics.
func TestGoalWakeSafetyGoalToolPreservesAutomaticCap(t *testing.T) {
	for _, automatic := range []bool{true, false} {
		name := "manual"
		if automatic {
			name = "automatic"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db, b, _ := completionFixture(t)
			g, err := db.MintEmbeddedProcessGeneration(ctx, "cap-guard")
			require.NoError(t, err)
			body, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "continue work"}}})
			require.NoError(t, err)
			user, err := db.AppendEmbeddedEntry(ctx, b, "user", body)
			require.NoError(t, err)
			require.NoError(t, db.EnterEmbeddedNonMailProviderTurn(ctx, b, user.Sequence, user.Digest))
			args := map[string]any{"action": "set", "goal": "bounded work"}
			intent, err := json.Marshal(map[string]any{"id": "cap-set", "name": "goal", "arguments": args})
			require.NoError(t, err)
			invocation, err := db.RecordEmbeddedGoalInvocation(ctx, b, g, user.Sequence, user.Digest, "", "cap-set", intent)
			require.NoError(t, err)
			m := &Manager{store: db}
			lane := m.laneFor(b.AgentID)
			lane.consecutiveContinuations.Store(20)
			if automatic {
				ctx = context.WithValue(ctx, embeddedAutomaticGoalKey{}, true)
			}
			completed := false
			tool := m.embeddedGoalTool(embeddedOwner{binding: b, identity: "wake-fixture"}, func(string) *store.EmbeddedGoalInvocation { return invocation }, func(string, *store.EmbeddedGoalInvocation, ai.ToolResultMessage) { completed = true })
			_, err = tool.Execute(ctx, "cap-set", args, nil)
			require.NoError(t, err)
			require.True(t, completed, "must execute the admitted goal tool, not merely settle a provider turn")
			want := int64(0)
			if automatic {
				want = 20
			}
			require.Equal(t, want, lane.consecutiveContinuations.Load(), "automatic goal setting must not renew its own allowance")
		})
	}
}

// SCN-GWS-002/004/006: an automatic failure can legitimately rotate context
// before settling. Its retired origin is not evidence the published owner failed.
func TestGoalWakeSafetyFailedAutomaticResetDoesNotHoldSuccessor(t *testing.T) {
	f := newFailureFixture(t)
	var id string
	failAndReset := func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
		f.calls.Add(1)
		f.m.mu.Lock()
		if f.m.contextResetRequested == nil {
			f.m.contextResetRequested = make(map[string]*contextResetRequest)
		}
		// Model a committed reset request, as in the reset-gate fixtures;
		// an uncommitted request must no longer rotate context.
		f.m.contextResetRequested[id] = &contextResetRequest{reason: "guard-reset-after-failure", committed: true}
		f.m.mu.Unlock()
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "deterministic failed automatic turn"}}, ai.StopError)
	}
	f.reg.SetResponses([]providers.FauxResponseStep{f.response("startup settled"), failAndReset, f.response("successor awake"), f.response("NOT_DONE\nREASON: Continue bounded work")})
	owner := f.launch(t, "start")
	id = owner.binding.AgentID
	_, err := f.db.UpdateManagedGoalConfig(context.Background(), id, store.ManagedGoalConfig{Enabled: true, Goal: "continue bounded work", Settings: store.DefaultManagedGoalSettings()})
	require.NoError(t, err)
	f.m.kickoffIdleGoalContinuation(owner.binding)
	f.laneBarrier(t, id)
	saved, err := f.db.ManagedAgent(context.Background(), id)
	require.NoError(t, err)
	require.Greater(t, saved.SessionEpoch, owner.binding.Epoch, "fixture must actually rotate the failed turn's context")
	f.m.mu.Lock()
	successor, exists := f.m.embeddedOwners[id]
	f.m.mu.Unlock()
	require.True(t, exists, "reset must publish a successor")
	require.Greater(t, successor.binding.Epoch, owner.binding.Epoch)
	require.Equal(t, owner.binding.Generation, successor.binding.Generation)
	require.NoError(t, f.m.refreshEmbeddedOwner(context.Background(), &successor), "published successor must have valid ownership")
	lane := f.m.laneFor(id)
	lane.serial.Lock()
	defer lane.serial.Unlock()
	require.False(t, lane.goalOwnerInvalid, "retired automatic origin must not falsely hold a valid published reset successor")
}
