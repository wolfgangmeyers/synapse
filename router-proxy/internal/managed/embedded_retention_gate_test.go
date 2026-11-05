package managed

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// Break only the exact authority read under test, not the whole Store. The
// fallback can still append a fresh user and reach the deterministic provider.
func TestEmbeddedRetentionAuthorityReadErrorsSuppressDispatch(t *testing.T) {
	for _, tc := range []struct {
		name, fault string
		resume      bool
	}{
		{"preference", `ALTER TABLE embedded_goal_snooze_preferences RENAME COLUMN due_at TO unavailable_due_at`, true},
		{"pending", `ALTER TABLE embedded_restart_wake_slots RENAME COLUMN state TO unavailable_state`, false},
		{"reserved", `ALTER TABLE embedded_goal_evaluations RENAME COLUMN final_digest TO unavailable_final_digest`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFailureFixture(t)
			f.reg.SetResponses([]providers.FauxResponseStep{func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
				f.calls.Add(1)
				_, stopErr := f.db.UpdateManagedGoalConfig(ctx, "failure-root", store.ManagedGoalConfig{Settings: store.DefaultManagedGoalSettings()})
				if stopErr != nil {
					panic(stopErr)
				}
				return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "must not dispatch"}}, ai.StopStop)
			}})
			owner := f.launch(t, "")
			f.m.startWG.Wait() // Drain launch followups before installing the fault and goal.
			_, err := f.db.UpdateManagedGoalConfig(ctx, owner.binding.AgentID, store.ManagedGoalConfig{Enabled: true, Goal: "finish", Settings: store.DefaultManagedGoalSettings()})
			require.NoError(t, err)
			_, err = f.raw.Exec(`PRAGMA busy_timeout=5000`)
			require.NoError(t, err)
			_, err = f.raw.Exec(tc.fault)
			require.NoError(t, err)
			// Prove the intended boundary fails instead of merely observing no work.
			switch tc.name {
			case "preference":
				_, err = f.db.CurrentEmbeddedGoalSnoozePreference(ctx, owner.binding.AgentID)
			case "pending":
				_, err = f.db.HasPendingWakeOrStartedContinuation(ctx, owner.binding)
			case "reserved":
				_, _, err = f.db.LatestReservedEmbeddedGoalContinuation(ctx, owner.binding)
			}
			require.Error(t, err)
			before := f.count(t, `SELECT count(*) FROM embedded_entries WHERE agent_id=?`, owner.binding.AgentID)
			if tc.resume {
				f.m.MaybeResumeEmbeddedGoal(owner.binding.AgentID)
			} else {
				f.m.kickoffIdleGoalContinuation(owner.binding)
			}
			done := make(chan struct{})
			go func() { f.m.startWG.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("managed task did not settle")
			}
			require.Zero(t, f.calls.Load(), "authority read failure must suppress model dispatch")
			require.Equal(t, before, f.count(t, `SELECT count(*) FROM embedded_entries WHERE agent_id=?`, owner.binding.AgentID), "failure must not manufacture a fresh fallback turn")
		})
	}
}

func TestEmbeddedRetentionContextResetPreservesAdmission(t *testing.T) {
	ctx := context.Background()
	f := newFailureFixture(t)
	f.reg.SetResponses([]providers.FauxResponseStep{f.response("reset wake settled")})
	owner := f.launch(t, "")
	body := []byte(`{"role":"user", "content":[{"type":"text","text":"ported old input"}],"timestamp":9999999999999}`)
	original, err := f.db.AppendEmbeddedEntry(ctx, owner.binding, "user", body)
	require.NoError(t, err)
	var oldTime int64
	require.NoError(t, f.raw.QueryRow(`SELECT retention_admitted_at_ms FROM embedded_entries WHERE agent_id=? AND epoch=? AND seq=?`, owner.binding.AgentID, owner.binding.Epoch, original.Sequence).Scan(&oldTime))
	_, err = f.db.AppendScratchpadEntry(ctx, owner.binding.AgentID, "fresh memory handoff", false)
	require.NoError(t, err)
	_, err = f.raw.Exec(`UPDATE managed_agents SET pending_initial_instruction='fresh genesis' WHERE agent_id=?`, owner.binding.AgentID)
	require.NoError(t, err)
	require.NoError(t, f.clock.Advance(31*24*time.Hour))
	// Advancing fixture time also expires the owner lease; renew through the real
	// owner API before reset rather than relying on stale authority.
	require.NoError(t, f.db.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL))
	now := f.clock.Now().UnixMilli()
	ownerCtx := context.WithValue(ctx, embeddedOwnerContextKey{}, owner)
	require.NoError(t, f.m.ExecuteContextReset(ownerCtx, owner.binding, "retention fixture"))
	rows, err := f.raw.Query(`SELECT body,retention_admitted_at_ms FROM embedded_entries WHERE agent_id=? AND epoch=? AND kind='user' ORDER BY seq`, owner.binding.AgentID, owner.binding.Epoch+1)
	require.NoError(t, err)
	defer rows.Close()
	var bodies [][]byte
	var times []int64
	for rows.Next() {
		var b []byte
		var a int64
		require.NoError(t, rows.Scan(&b, &a))
		bodies = append(bodies, b)
		times = append(times, a)
	}
	require.NoError(t, rows.Err())
	require.Len(t, bodies, 4, "genesis, scratchpad, ported input and wake")
	require.Equal(t, body, bodies[2], "import copies saved bytes, not remarshaled SDK JSON")
	require.Equal(t, []int64{now, now, oldTime, now}, times, "only copied context inherits old admission")
	require.Equal(t, int32(1), f.calls.Load())
}

func TestEmbeddedRetentionRuntimeExecutionSlots(t *testing.T) {
	f := newFailureFixture(t)
	var sdkSeen, evalSeen atomic.Int32
	f.m.embeddedGoalEvaluator = func(_ context.Context, k store.EmbeddedGoalEvaluationKey) (string, string, error) {
		var kind string
		var seq int64
		require.NoError(t, f.raw.QueryRow(`SELECT json_extract(execution_json,'$.kind'),json_extract(execution_json,'$.source.UserSeq') FROM embedded_sessions WHERE agent_id='failure-root' AND current=1`).Scan(&kind, &seq))
		require.Equal(t, "evaluator", kind)
		require.Equal(t, k.UserSeq, seq)
		evalSeen.Add(1)
		return store.EmbeddedGoalEvaluationDone, "finished", nil
	}
	checkSDK := func() {
		var kind string
		var n int
		require.NoError(t, f.raw.QueryRow(`SELECT json_extract(execution_json,'$.kind') FROM embedded_sessions WHERE agent_id='failure-root' AND current=1`).Scan(&kind))
		require.Equal(t, "sdk", kind)
		require.NoError(t, f.raw.QueryRow(`SELECT count(*) FROM embedded_execution_closures WHERE agent_id='failure-root'`).Scan(&n))
		require.Equal(t, 1, n, "provider sees exact persisted current user")
		sdkSeen.Add(1)
	}
	f.m.embeddedAfterCheckedIntent = func(context.Context) {
		require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_execution_closures WHERE agent_id='failure-root'`), "tool Execute admission retains current SDK identity")
	}
	f.reg.SetResponses([]providers.FauxResponseStep{
		func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
			checkSDK()
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "finished"}}, ai.StopStop)
		},
	})
	owner := f.launch(t, "")
	f.m.startWG.Wait()
	_, err := f.db.UpdateManagedGoalConfig(context.Background(), owner.binding.AgentID, store.ManagedGoalConfig{Enabled: true, Goal: "finish", Settings: store.DefaultManagedGoalSettings()})
	require.NoError(t, err)
	saved, err := f.db.ManagedAgent(context.Background(), owner.binding.AgentID)
	require.NoError(t, err)
	lane := f.m.laneFor(owner.binding.AgentID)
	lane.serial.Lock()
	err = f.m.runEmbeddedSessionPrompt(context.WithValue(context.Background(), embeddedOwnerContextKey{}, owner), *saved, "execute tracked work")
	// Claim happened before lane acquisition, but actual evaluator execution
	// cannot begin while this real initiating SDK lane remains held.
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_goal_evaluations WHERE agent_id='failure-root' AND state='UNKNOWN'`))
	require.Zero(t, evalSeen.Load())
	require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_sessions WHERE agent_id='failure-root' AND execution_json IS NOT NULL`), "queued UNKNOWN is not execution authority")
	lane.serial.Unlock()
	require.NoError(t, err)
	done := make(chan struct{})
	go func() { f.m.startWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("evaluation did not settle")
	}
	require.Equal(t, int32(1), sdkSeen.Load())
	require.Equal(t, int32(1), evalSeen.Load())
	require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_sessions WHERE agent_id=? AND execution_json IS NOT NULL`, owner.binding.AgentID), "settlement clears exact execution before lane release")
}

func TestEmbeddedRetentionSDKCancellationSettlesExecution(t *testing.T) {
	f := newFailureFixture(t)
	entered := make(chan struct{})
	f.reg.SetResponses([]providers.FauxResponseStep{func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
		close(entered)
		<-f.m.runCtx.Done()
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "canceled"}}, ai.StopStop)
	}})
	// Launch waits asynchronously; observe the real provider while it is live.
	_, err := f.m.LaunchEmbeddedRoot(context.Background(), LaunchRequest{Name: "failure-root", Cwd: f.root, Runtime: "embedded", InitialInstruction: "cancel work", RequestID: "e1111111-1111-4111-8111-111111111111"})
	require.NoError(t, err)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider not entered")
	}
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_sessions WHERE agent_id='failure-root' AND execution_json IS NOT NULL`))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, f.m.Close(ctx))
	require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_sessions WHERE agent_id='failure-root' AND execution_json IS NOT NULL`), "canceled SDK must clear using uncanceled exact-token settlement")
}

func TestEmbeddedRetentionContinuationProtectsSourceAndCurrent(t *testing.T) {
	f := newFailureFixture(t)
	ctx := context.Background()
	var evaluations atomic.Int32
	f.m.embeddedGoalEvaluator = func(context.Context, store.EmbeddedGoalEvaluationKey) (string, string, error) {
		if evaluations.Add(1) == 1 {
			return store.EmbeddedGoalEvaluationNotDone, "continue", nil
		}
		return store.EmbeddedGoalEvaluationDone, "finished", nil
	}
	var requests atomic.Int32
	f.reg.SetResponses([]providers.FauxResponseStep{f.response("source settled"), func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
		requests.Add(1)
		var source, current int64
		require.NoError(t, f.raw.QueryRow(`SELECT json_extract(execution_json,'$.source.UserSeq'),json_extract(execution_json,'$.current.UserSeq') FROM embedded_sessions WHERE agent_id='failure-root' AND current=1`).Scan(&source, &current))
		require.Greater(t, current, source)
		require.Equal(t, 2, f.count(t, `SELECT count(*) FROM embedded_execution_closures WHERE agent_id='failure-root'`))
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "continuation settled"}}, ai.StopStop)
	}})
	owner := f.launch(t, "")
	f.m.startWG.Wait()
	_, err := f.db.UpdateManagedGoalConfig(ctx, owner.binding.AgentID, store.ManagedGoalConfig{Enabled: true, Goal: "finish", Settings: store.DefaultManagedGoalSettings()})
	require.NoError(t, err)
	saved, err := f.db.ManagedAgent(ctx, owner.binding.AgentID)
	require.NoError(t, err)
	lane := f.m.laneFor(owner.binding.AgentID)
	lane.serial.Lock()
	err = f.m.runEmbeddedSessionPrompt(context.WithValue(ctx, embeddedOwnerContextKey{}, owner), *saved, "source work")
	lane.serial.Unlock()
	require.NoError(t, err)
	done := make(chan struct{})
	go func() { f.m.startWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("continuation did not settle")
	}
	require.Equal(t, int32(1), requests.Load())
	require.Equal(t, int32(2), evaluations.Load())
	require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_sessions WHERE agent_id='failure-root' AND execution_json IS NOT NULL`))
}
