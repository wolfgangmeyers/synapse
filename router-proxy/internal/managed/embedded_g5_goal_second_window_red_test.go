package managed

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

const g5GoalSecondCallID = "g5-goal-second-window-call"
const g5GoalSecondAgent = "g5-goal-second-root"

func g5GoalSecondManager(t *testing.T, db *store.Store, root string, reg *providers.FauxProviderRegistration, barrier func(context.Context)) *Manager {
	t.Helper()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest,
		Config:                  Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:           CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver:   func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		EmbeddedAfterGoalCommit: barrier,
		CodexCredentialStore:    testfixture.CodexAdmission(t),
	})
	require.NoError(t, err)
	return m
}

func TestEmbeddedG5GoalSecondWindowChild(t *testing.T) {
	if os.Getenv("G5_GOAL_SECOND_CHILD") != "1" {
		return
	}
	ctx := context.Background()
	root, path := os.Getenv("G5_GOAL_SECOND_ROOT"), os.Getenv("G5_GOAL_SECOND_DB")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	reg.SetResponses([]providers.FauxResponseStep{func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal", g5GoalFirstArgs(), g5GoalSecondCallID)}, ai.StopToolUse)
	}})
	m := g5GoalSecondManager(t, db, root, reg, func(context.Context) {
		_, _ = os.Stdout.WriteString("GOAL_RESULT_COMMITTED\n")
		select {} // Parent verifies SQLite independently, then SIGKILLs this child.
	})
	setEmbeddedRecoveryGeneration(t, ctx, db, m)
	_, err = m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: g5GoalSecondAgent, Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "set own goal", RequestID: "a5555555-5555-4555-8555-555555555572"})
	require.NoError(t, err)
	select {}
}

func TestEmbeddedG5GoalSecondWindowCommittedBeforeSDKAppendRED(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	cmd := exec.Command(os.Args[0], "-test.run=^TestEmbeddedG5GoalSecondWindowChild$")
	cmd.Env = append(os.Environ(), "G5_GOAL_SECOND_CHILD=1", "G5_GOAL_SECOND_ROOT="+root, "G5_GOAL_SECOND_DB="+path)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
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
		require.Equal(t, "GOAL_RESULT_COMMITTED\n", line, "child exited before second fault boundary; stderr: %s", stderr.String())
	case <-time.After(8 * time.Second):
		t.Fatalf("goal result barrier not reached; stderr: %s", stderr.String())
	}
	// Read-only independent connection while the child is still blocked.
	raw, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer raw.Close()
	var state, evidence, args string
	var count, results int
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM embedded_goal_calls WHERE call_id=?`, g5GoalSecondCallID).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT state,result_json,args_json FROM embedded_goal_calls WHERE call_id=?`, g5GoalSecondCallID).Scan(&state, &evidence, &args))
	require.Equal(t, "complete", state)
	require.NotEmpty(t, evidence)
	command := store.EmbeddedGoalCommand{Action: "set", Goal: "finish own goal", Settings: store.DefaultManagedGoalSettings()}
	canonical, err := json.Marshal(command)
	require.NoError(t, err)
	require.Equal(t, string(canonical), args)
	var immutable store.ManagedGoalPolicy
	require.NoError(t, json.Unmarshal([]byte(evidence), &immutable))
	require.EqualValues(t, 1, immutable.DesiredRevision)
	require.EqualValues(t, 0, immutable.AppliedRevision)
	require.True(t, immutable.Config.Enabled)
	require.Equal(t, command.Goal, immutable.Config.Goal)
	var revision, applied, version int64
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT desired_revision,applied_revision FROM managed_agent_policies WHERE agent_id=?`, g5GoalSecondAgent).Scan(&revision, &applied))
	require.EqualValues(t, 1, revision)
	require.Zero(t, applied)
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT projection_version FROM managed_settings WHERE singleton_id=1`).Scan(&version))
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id=? AND kind='toolResult'`, g5GoalSecondAgent).Scan(&results))
	require.Zero(t, results)
	var session, generation string
	var epoch, token int64
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT a.pi_session_id,a.session_epoch,a.runtime_generation,s.owner_token FROM managed_agents a JOIN embedded_sessions s ON s.agent_id=a.agent_id AND s.current=1 WHERE a.agent_id=?`, g5GoalSecondAgent).Scan(&session, &epoch, &generation, &token))
	stale := store.EmbeddedBinding{AgentID: g5GoalSecondAgent, SessionID: session, Epoch: epoch, Generation: generation, OwnerToken: token}
	require.NoError(t, cmd.Process.Kill())
	require.Error(t, cmd.Wait())
	require.Equal(t, -1, cmd.ProcessState.ExitCode(), "child must die by signal")

	clock := deterministic.NewFixedClock(time.Now().Add(embeddedOwnerTTL + time.Minute))
	db, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer db.Close()
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	attempted := make(chan struct{}, 1)
	continued := make(chan *ai.ToolResultMessage, 1)
	var recoveryProcess, noticeSeq, noticeOwner int64
	var noticeID, noticeDigest string
	var recoveryCallbacks atomic.Int32
	reg.SetResponses([]providers.FauxResponseStep{
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			recoveryCallbacks.Add(1)
			// Complete only the interrupted launch notice, not the committed goal call.
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
			snapshotG5Bash(t, path, g5GoalSecondAgent, g5GoalSecondCallID, recoveryCallbacks.Load(), 0, &h)
			final := providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "interruption acknowledged"}}, ai.StopStop)
			finalBody, err := json.Marshal(final)
			require.NoError(t, err)
			t.Logf("exact notice callback id=%s source_seq=2 entry_seq=%d digest=%s owner=%d process=%d response_stop=%s response_sha256=%x (not a persisted checked-final claim)", noticeID, noticeSeq, noticeDigest, noticeOwner, recoveryProcess, final.StopReason, sha256.Sum256(finalBody))
			return final
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			recoveryCallbacks.Add(1)
			attempted <- struct{}{}
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal", g5GoalFirstArgs(), g5GoalSecondCallID)}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			recoveryCallbacks.Add(1)
			var result *ai.ToolResultMessage
			for _, message := range h.Messages {
				if r, ok := message.(ai.ToolResultMessage); ok && r.ToolCallID == g5GoalSecondCallID {
					copy := r
					result = &copy
				}
			}
			continued <- result
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
		},
	})
	m := g5GoalSecondManager(t, db, root, reg, nil)
	defer m.Close(ctx)
	recoveryProcess = setEmbeddedRecoveryGeneration(t, ctx, db, m)
	require.NoError(t, m.RecoverEmbeddedRoots(ctx))
	if !assert.Eventually(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); _, ok := m.embeddedOwners[g5GoalSecondAgent]; return ok }, 4*time.Second, 10*time.Millisecond) {
		// Read the failure before deferred Close can change the lifecycle.
		var lifecycle, desired, activity, code, message, operation, ownerState, wakes string
		err := raw.QueryRowContext(ctx, `SELECT lifecycle_state,desired_state,activity_state,COALESCE(last_error_code,''),COALESCE(last_error_message,'') FROM managed_agents WHERE agent_id=?`, g5GoalSecondAgent).Scan(&lifecycle, &desired, &activity, &code, &message)
		t.Logf("owner wait: lifecycle=%s desired=%s activity=%s error=%s message=%q read_error=%v", lifecycle, desired, activity, code, message, err)
		err = raw.QueryRowContext(ctx, `SELECT json_group_array(json_object('id',o.operation_id,'state',o.state,'status',o.response_status)) FROM managed_http_operations o JOIN managed_agents a ON a.launch_operation_id=o.operation_id WHERE a.agent_id=?`, g5GoalSecondAgent).Scan(&operation)
		t.Logf("owner wait: operation=%s read_error=%v", operation, err)
		err = raw.QueryRowContext(ctx, `SELECT json_group_array(json_object('session',session_id,'epoch',epoch,'generation',generation,'token',owner_token,'expires',owner_expires_at)) FROM embedded_sessions WHERE agent_id=? AND current=1`, g5GoalSecondAgent).Scan(&ownerState)
		t.Logf("owner wait: persisted_owner=%s read_error=%v", ownerState, err)
		err = raw.QueryRowContext(ctx, `SELECT json_group_array(json_object('slot',slot,'id',wake_id,'kind',kind,'state',state,'process_generation',process_generation,'token',claim_owner_token)) FROM embedded_restart_wake_slots WHERE agent_id=?`, g5GoalSecondAgent).Scan(&wakes)
		t.Logf("owner wait: wakes=%s read_error=%v", wakes, err)
		snapshotG5Bash(t, path, g5GoalSecondAgent, g5GoalSecondCallID, recoveryCallbacks.Load(), 0, nil)
		var turns, provenance, original, retainedFailureState string
		err = raw.QueryRowContext(ctx, `SELECT json_group_array(json_object('seq',seq,'digest',digest,'parent',parent_digest,'kind',kind,'stop',json_extract(body,'$.stopReason'),'error_message',json_extract(body,'$.errorMessage'),'call_id',json_extract(body,'$.toolCallId'))) FROM (SELECT seq,digest,parent_digest,kind,body FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? ORDER BY seq DESC LIMIT 12)`, stale.AgentID, stale.SessionID, stale.Epoch, stale.Generation).Scan(&turns)
		t.Logf("owner wait: exact persisted turn/final stops/errors=%s read_error=%v; scripted_callbacks=%d queued_responses=%d (not a queue-exhaustion inference)", turns, err, recoveryCallbacks.Load(), reg.PendingResponseCount())
		err = raw.QueryRowContext(ctx, `SELECT json_group_array(json_object('notice_id',n.notice_id,'source_seq',n.source_seq,'notice_seq',n.entry_seq,'notice_state',n.state,'notice_owner',n.start_owner_token,'wake_state',w.state,'process',w.process_generation,'claim_owner',w.claim_owner_token,'checked_seq',o.seq,'checked_stop',json_extract(f.body,'$.stopReason'),'checked_digest',f.digest)) FROM embedded_interrupted_notices n LEFT JOIN embedded_restart_wake_slots w ON w.agent_id=n.agent_id AND w.session_id=n.session_id AND w.epoch=n.epoch AND w.runtime_generation=n.generation AND w.wake_id=n.notice_id LEFT JOIN embedded_restart_wake_checked_origins o ON o.agent_id=w.agent_id AND o.slot=w.slot AND o.session_id=w.session_id AND o.epoch=w.epoch AND o.runtime_generation=w.runtime_generation AND o.wake_id=w.wake_id AND o.process_generation=w.process_generation AND o.claim_owner_token=w.claim_owner_token LEFT JOIN embedded_entries f ON f.agent_id=o.agent_id AND f.session_id=o.session_id AND f.epoch=o.epoch AND f.generation=o.runtime_generation AND f.seq=o.seq WHERE n.agent_id=? AND n.session_id=? AND n.epoch=? AND n.generation=?`, stale.AgentID, stale.SessionID, stale.Epoch, stale.Generation).Scan(&provenance)
		t.Logf("owner wait: exact notice source/final provenance=%s read_error=%v (null final is unmeasured)", provenance, err)
		err = raw.QueryRowContext(ctx, `SELECT state,result_json FROM embedded_goal_calls WHERE call_id=?`, g5GoalSecondCallID).Scan(&retainedFailureState, &original)
		t.Logf("owner wait: original goal state=%s immutable_result_equal=%t read_error=%v; stop without fabricating notice or goal success", retainedFailureState, original == evidence, err)
		t.FailNow()
	}
	select {
	case <-attempted:
		t.Fatal("recovery automatically called provider")
	default:
	}
	m.mu.Lock()
	owner := m.embeddedOwners[g5GoalSecondAgent]
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
	var retainedState, retainedEvidence string
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT state,result_json FROM embedded_goal_calls WHERE call_id=?`, g5GoalSecondCallID).Scan(&retainedState, &retainedEvidence))
	require.Equal(t, "complete", retainedState)
	require.Equal(t, evidence, retainedEvidence, "notice final must preserve the immutable goal result")
	require.EqualValues(t, 1, recoveryCallbacks.Load(), "only the checked awareness notice may reach the provider")
	var awarenessReceipts int
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM embedded_goal_applied_receipts WHERE agent_id=?`, g5GoalSecondAgent).Scan(&awarenessReceipts))
	require.Zero(t, awarenessReceipts, "awareness must not apply the original unappended goal result")
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT applied_revision FROM managed_agent_policies WHERE agent_id=?`, g5GoalSecondAgent).Scan(&applied))
	require.Zero(t, applied, "goal domain completion is not checked SDK application")
	// Taking over the dead lease can bump the public version independently
	// of the goal. The retry itself must not bump it again.
	beforeRetryVersion, err := db.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.GreaterOrEqual(t, beforeRetryVersion, version)
	staleBinding := stale
	staleBinding.Epoch++
	_, _, err = db.ApplyEmbeddedGoalCall(ctx, staleBinding, g5GoalSecondCallID, command)
	require.Error(t, err)
	saved, err := db.ManagedAgent(ctx, g5GoalSecondAgent)
	require.NoError(t, err)
	_ = m.runEmbeddedSessionPrompt(context.WithValue(ctx, embeddedOwnerContextKey{}, owner), *saved, "explicit retry")
	select {
	case <-attempted:
	default:
		t.Fatal("explicit SDK retry never attempted original call")
	}
	select {
	case result := <-continued:
		require.Nil(t, result, "retry must not fabricate a checked SDK result")
	default: // Admission may stop the turn before provider continuation.
	}
	replay, wasReplay, err := db.ApplyEmbeddedGoalCall(ctx, owner.binding, g5GoalSecondCallID, command)
	require.NoError(t, err)
	require.True(t, wasReplay)
	replayJSON, err := json.Marshal(replay)
	require.NoError(t, err)
	require.Equal(t, evidence, string(replayJSON), "only the saved immutable result may replay")
	_, _, err = db.ApplyEmbeddedGoalCall(ctx, owner.binding, g5GoalSecondCallID, store.EmbeddedGoalCommand{Action: "set", Goal: "changed", Settings: store.DefaultManagedGoalSettings()})
	require.ErrorContains(t, err, "conflict")
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM embedded_goal_calls WHERE call_id=?`, g5GoalSecondCallID).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id=? AND kind='toolResult'`, g5GoalSecondAgent).Scan(&results))
	require.Zero(t, results)
	require.NoError(t, db.Close())
	reopened, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer reopened.Close()
	snapshot, err := reopened.ManagedAgentsProjectionSnapshot(ctx)
	require.NoError(t, err)
	require.GreaterOrEqual(t, snapshot.ProjectionVersion, beforeRetryVersion)
	policy := snapshot.GoalPolicies[g5GoalSecondAgent]
	require.NotNil(t, policy)
	require.EqualValues(t, 1, policy.DesiredRevision)
	require.EqualValues(t, 0, policy.AppliedRevision)
	require.True(t, policy.Config.Enabled)
	require.Equal(t, command.Goal, policy.Config.Goal)
}
