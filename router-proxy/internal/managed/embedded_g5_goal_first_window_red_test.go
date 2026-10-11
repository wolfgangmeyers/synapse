package managed

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

const g5GoalFirstCallID = "g5-goal-first-window-call"

func g5GoalFirstArgs() map[string]any {
	return map[string]any{"action": "set", "goal": "finish own goal"}
}

// The SDK commits the original effect intent before EmbeddedAfterIntent fires.
// The child stops there, and only the parent may kill it. No policy transaction
// or SDK result has been allowed to start at this barrier.
func TestEmbeddedG5GoalFirstWindowChild(t *testing.T) {
	if os.Getenv("G5_GOAL_FIRST_CHILD") != "1" {
		return
	}
	ctx := context.Background()
	root, path := os.Getenv("G5_GOAL_FIRST_ROOT"), os.Getenv("G5_GOAL_FIRST_DB")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	reg.SetResponses([]providers.FauxResponseStep{func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal", g5GoalFirstArgs(), g5GoalFirstCallID)}, ai.StopToolUse)
	}})
	m := g5FirstWindowManager(t, db, root, reg, func(context.Context) {
		_, _ = os.Stdout.WriteString("GOAL_INTENT_COMMITTED\n")
		select {} // deterministic barrier: SIGKILL from parent, never return to Execute
	})
	setEmbeddedRecoveryGeneration(t, ctx, db, m)
	_, err = m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "g5-goal-window-root", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "set own goal", RequestID: "a5555555-5555-4555-8555-555555555571"})
	require.NoError(t, err)
	select {}
}

func TestEmbeddedG5GoalFirstWindowUnknownAfterSIGKILLRED(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	cmd := exec.Command(os.Args[0], "-test.run=^TestEmbeddedG5GoalFirstWindowChild$")
	cmd.Env = append(os.Environ(), "G5_GOAL_FIRST_CHILD=1", "G5_GOAL_FIRST_ROOT="+root, "G5_GOAL_FIRST_DB="+path)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	var childErr bytes.Buffer
	cmd.Stderr = &childErr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	marker := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(stdout).ReadString('\n'); marker <- line }()
	select {
	case line := <-marker:
		require.Equal(t, "GOAL_INTENT_COMMITTED\n", line, "child exited before the intended fault boundary")
	case <-time.After(8 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("SDK did not reach durable goal intent barrier; child stderr: %s", childErr.String())
	}

	// Observe the marker from another SQLite connection before killing the child.
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()
	var intent []byte
	var state string
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT intent,state FROM embedded_effects WHERE effect_id=?`, g5GoalFirstCallID).Scan(&intent, &state))
	var call ai.ToolCall
	require.NoError(t, json.Unmarshal(intent, &call))
	require.Equal(t, g5GoalFirstCallID, call.ID)
	require.Equal(t, "goal", call.Name)
	require.Equal(t, g5GoalFirstArgs(), call.Arguments)
	require.Equal(t, "unknown", state)
	var session, generation, role string
	var epoch, token int64
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT a.pi_session_id,a.session_epoch,a.runtime_generation,s.role,s.owner_token FROM managed_agents a JOIN embedded_sessions s ON s.agent_id=a.agent_id AND s.current=1 WHERE a.agent_id='g5-goal-window-root'`).Scan(&session, &epoch, &generation, &role, &token))
	require.Equal(t, "root", role)
	require.Positive(t, token)
	stale := store.EmbeddedBinding{AgentID: "g5-goal-window-root", SessionID: session, Epoch: epoch, Generation: generation, OwnerToken: token}
	var calls, policies, results int
	count := func() {
		t.Helper()
		require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM embedded_goal_calls WHERE call_id=?`, g5GoalFirstCallID).Scan(&calls))
		require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM managed_agent_policies WHERE agent_id=? AND desired_revision>0`, stale.AgentID).Scan(&policies))
		require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id=? AND kind='toolResult'`, stale.AgentID).Scan(&results))
	}
	count()
	require.Zero(t, calls)
	require.Zero(t, policies, "no new desired policy revision before the atomic transaction")
	require.Zero(t, results)
	require.NoError(t, cmd.Process.Kill())
	err = cmd.Wait()
	require.Error(t, err)
	require.Equal(t, -1, cmd.ProcessState.ExitCode(), "child must die by signal, not exit normally")

	// Only now open another Store and Manager, with a clock past the dead lease.
	clock := deterministic.NewFixedClock(time.Now().Add(embeddedOwnerTTL + time.Minute))
	db, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer db.Close()
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	attempted := make(chan struct{}, 1)
	continuation := make(chan *ai.ToolResultMessage, 1)
	var recoveryProcess, noticeSeq, noticeOwner int64
	var noticeID, noticeDigest string
	reg.SetResponses([]providers.FauxResponseStep{
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			// Only the persisted interrupted launch notice gets this checked final.
			var body []byte
			var text string
			require.NoError(t, raw.QueryRowContext(ctx, `SELECT n.notice_id,n.entry_seq,e.digest,e.body,n.notice_text,n.start_owner_token FROM embedded_interrupted_notices n JOIN embedded_entries e ON e.agent_id=n.agent_id AND e.session_id=n.session_id AND e.epoch=n.epoch AND e.generation=n.generation AND e.seq=n.entry_seq JOIN embedded_sessions s ON s.agent_id=n.agent_id AND s.session_id=n.session_id AND s.epoch=n.epoch AND s.generation=n.generation AND s.current=1 AND s.owner_token=n.start_owner_token JOIN embedded_restart_wake_slots w ON w.agent_id=n.agent_id AND w.session_id=n.session_id AND w.epoch=n.epoch AND w.runtime_generation=n.generation AND w.wake_id=n.notice_id AND w.claim_owner_token=s.owner_token WHERE n.agent_id=? AND n.session_id=? AND n.epoch=? AND n.generation=? AND n.source_seq=2 AND n.state='started' AND e.kind='user' AND w.slot='current' AND w.kind='interrupted' AND w.digest=e.digest AND w.state='claimed' AND w.process_generation=?`, stale.AgentID, stale.SessionID, stale.Epoch, stale.Generation, recoveryProcess).Scan(&noticeID, &noticeSeq, &noticeDigest, &body, &text, &noticeOwner))
			require.Equal(t, stale.OwnerToken, noticeOwner)
			require.NotEmpty(t, noticeDigest)
			exact, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: text}}})
			require.NoError(t, err)
			require.Equal(t, exact, body)
			var lastUser []byte
			for _, message := range h.Messages {
				if user, ok := message.(ai.UserMessage); ok {
					lastUser, err = json.Marshal(user)
					require.NoError(t, err)
				}
			}
			require.Equal(t, body, lastUser, "only the exact persisted notice may complete recovery")
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "interruption acknowledged"}}, ai.StopStop)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			attempted <- struct{}{}
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal", g5GoalFirstArgs(), g5GoalFirstCallID)}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			var result *ai.ToolResultMessage
			for _, msg := range h.Messages {
				if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == g5GoalFirstCallID {
					copy := r
					result = &copy
				}
			}
			continuation <- result
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
		},
	})
	m := g5FirstWindowManager(t, db, root, reg, nil)
	defer m.Close(ctx)
	recoveryProcess = setEmbeddedRecoveryGeneration(t, ctx, db, m)
	require.NoError(t, m.RecoverEmbeddedRoots(ctx))
	if !assert.Eventually(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); _, ok := m.embeddedOwners[stale.AgentID]; return ok }, 4*time.Second, 10*time.Millisecond) {
		// Read the failure before deferred Close can change the lifecycle.
		var lifecycle, desired, activity, code, message, operation, ownerState, wakes string
		err := raw.QueryRowContext(ctx, `SELECT lifecycle_state,desired_state,activity_state,COALESCE(last_error_code,''),COALESCE(last_error_message,'') FROM managed_agents WHERE agent_id=?`, stale.AgentID).Scan(&lifecycle, &desired, &activity, &code, &message)
		t.Logf("owner wait: lifecycle=%s desired=%s activity=%s error=%s message=%q read_error=%v", lifecycle, desired, activity, code, message, err)
		err = raw.QueryRowContext(ctx, `SELECT json_group_array(json_object('id',o.operation_id,'state',o.state,'status',o.response_status)) FROM managed_http_operations o JOIN managed_agents a ON a.launch_operation_id=o.operation_id WHERE a.agent_id=?`, stale.AgentID).Scan(&operation)
		t.Logf("owner wait: operation=%s read_error=%v", operation, err)
		err = raw.QueryRowContext(ctx, `SELECT json_group_array(json_object('session',session_id,'epoch',epoch,'generation',generation,'token',owner_token,'expires',owner_expires_at)) FROM embedded_sessions WHERE agent_id=? AND current=1`, stale.AgentID).Scan(&ownerState)
		t.Logf("owner wait: persisted_owner=%s read_error=%v", ownerState, err)
		err = raw.QueryRowContext(ctx, `SELECT json_group_array(json_object('slot',slot,'id',wake_id,'kind',kind,'state',state,'process_generation',process_generation,'token',claim_owner_token)) FROM embedded_restart_wake_slots WHERE agent_id=?`, stale.AgentID).Scan(&wakes)
		t.Logf("owner wait: wakes=%s read_error=%v", wakes, err)
		t.FailNow()
	}
	select {
	case <-attempted:
		t.Fatal("recovery retried the original goal call instead of only its notice")
	default:
	}
	m.mu.Lock()
	owner := m.embeddedOwners[stale.AgentID]
	m.mu.Unlock()
	require.Equal(t, noticeOwner, owner.binding.OwnerToken)
	var checkedBody []byte
	var checkedSeq int64
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT e.seq,e.body FROM embedded_restart_wake_slots w JOIN embedded_restart_wake_checked_origins o ON o.agent_id=w.agent_id AND o.slot=w.slot AND o.session_id=w.session_id AND o.epoch=w.epoch AND o.runtime_generation=w.runtime_generation AND o.wake_id=w.wake_id AND o.process_generation=w.process_generation AND o.claim_owner_token=w.claim_owner_token JOIN embedded_entries e ON e.agent_id=o.agent_id AND e.session_id=o.session_id AND e.epoch=o.epoch AND e.generation=o.runtime_generation AND e.seq=o.seq WHERE w.agent_id=? AND w.wake_id=? AND w.process_generation=? AND w.claim_owner_token=? AND w.state='checked-complete' AND e.kind='assistant' AND e.parent_digest=?`, stale.AgentID, noticeID, recoveryProcess, noticeOwner, noticeDigest).Scan(&checkedSeq, &checkedBody))
	require.Equal(t, noticeSeq+1, checkedSeq)
	checkedMessage, err := ai.UnmarshalMessage(checkedBody)
	require.NoError(t, err)
	checkedFinal, ok := checkedMessage.(ai.AssistantMessage)
	require.True(t, ok)
	require.Equal(t, ai.StopStop, checkedFinal.StopReason)
	require.Equal(t, ai.ContentList{ai.TextContent{Text: "interruption acknowledged"}}, checkedFinal.Content)
	count()
	require.Zero(t, calls)
	require.Zero(t, policies)
	require.Zero(t, results)
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT state FROM embedded_effects WHERE effect_id=?`, g5GoalFirstCallID).Scan(&state))
	require.Equal(t, "unknown", state, "notice final is not an original goal result")
	command := store.EmbeddedGoalCommand{Action: "set", Goal: "finish own goal", Settings: store.DefaultManagedGoalSettings()}
	staleBinding := stale
	staleBinding.Epoch++
	_, _, err = db.ApplyEmbeddedGoalCall(ctx, staleBinding, g5GoalFirstCallID, command)
	require.Error(t, err)
	saved, err := db.ManagedAgent(ctx, stale.AgentID)
	require.NoError(t, err)
	retryErr := m.runEmbeddedSessionPrompt(context.WithValue(ctx, embeddedOwnerContextKey{}, owner), *saved, "retry original goal call")
	select {
	case <-attempted:
	default:
		t.Fatal("fresh SDK never attempted original call ID")
	}
	select {
	case result := <-continuation:
		require.Nil(t, result, "unknown outcome must not fabricate a successful SDK tool result")
	default:
		// Admission may stop the turn before provider continuation. Either way
		// it cannot turn the unknown intent into a successful result.
		_ = retryErr
	}
	count()
	require.Zero(t, calls, "recovery must not claim completion of an unstarted policy transaction")
	require.Zero(t, policies, "unknown original call must not create a desired revision")
	require.Zero(t, results, "unknown original call must not append a fabricated toolResult")
	// Explicit reconciliation of that same saved call must report unknown. A
	// different payload for the same ID is a conflict, not permission to set.
	_, _, err = db.ApplyEmbeddedGoalCall(ctx, owner.binding, g5GoalFirstCallID, command)
	require.ErrorContains(t, err, "unknown", "first behavioral RED: the durable SDK intent was lost by the policy transaction")
	_, _, err = db.ApplyEmbeddedGoalCall(ctx, owner.binding, g5GoalFirstCallID, store.EmbeddedGoalCommand{Action: "set", Goal: "changed", Settings: store.DefaultManagedGoalSettings()})
	require.ErrorContains(t, err, "conflict")
	count()
	require.Zero(t, policies)
	require.Zero(t, results)
}
