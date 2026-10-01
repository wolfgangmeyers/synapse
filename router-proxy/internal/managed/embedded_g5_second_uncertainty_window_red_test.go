package managed

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

const g5SecondWindowCallID = "g5-second-window-original-call"

func g5SecondWindowArgs(root string) map[string]any {
	return map[string]any{"action": "launch", "name": "g5-second-window-child", "cwd": root, "instruction": "independent task"}
}

func g5SecondWindowManager(t *testing.T, db *store.Store, root string, reg *providers.FauxProviderRegistration, fault func(context.Context)) *Manager {
	t.Helper()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() }, EmbeddedAfterWorkerLaunch: fault, CodexCredentialStore: testfixture.CodexAdmission(t)})
	require.NoError(t, err)
	return m
}

// The child uses the real SDK tool path. The sole fault exits after the launch
// returns, with the worker and operation durable but no SDK result commit.
func TestEmbeddedG5SecondWindowAbruptChild(t *testing.T) {
	if os.Getenv("G5_SECOND_WINDOW_CHILD") != "1" {
		return
	}
	ctx := context.Background()
	root, path := os.Getenv("G5_SECOND_WINDOW_ROOT"), os.Getenv("G5_SECOND_WINDOW_DB")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	reg.SetResponses([]providers.FauxResponseStep{func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("mecha_worker", g5SecondWindowArgs(root), g5SecondWindowCallID)}, ai.StopToolUse)
	}})
	m := g5SecondWindowManager(t, db, root, reg, func(context.Context) { os.Exit(78) })
	setEmbeddedRecoveryGeneration(t, ctx, db, m) // initial listener-backed authority, once
	_, err = m.Launch(ctx, LaunchRequest{Name: "g5-second-window-sender", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "launch independent worker", RequestID: "a5555555-5555-4555-8555-555555555580"})
	require.NoError(t, err)
	select {} // parent bounds the wait; expected exit is inside SDK Execute
}

func TestEmbeddedG5SecondWindowAbruptLaunchNoInventedResultRED(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700)) // parent-owned Cwd for both independent processes
	path := filepath.Join(root, "router.db")
	cmd := exec.Command(os.Args[0], "-test.run=^TestEmbeddedG5SecondWindowAbruptChild$")
	cmd.Env = append(os.Environ(), "G5_SECOND_WINDOW_CHILD=1", "G5_SECOND_WINDOW_ROOT="+root, "G5_SECOND_WINDOW_DB="+path)
	require.NoError(t, cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		require.Equal(t, 78, cmd.ProcessState.ExitCode(), "child must exit after worker launch returns: %v", err)
	case <-time.After(8 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatal("child never reached second window")
	}

	// A separate SQLite connection checks the boundary without invoking Manager.
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	var intent []byte
	var effectState string
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT intent,state FROM embedded_effects WHERE effect_id=?`, g5SecondWindowCallID).Scan(&intent, &effectState))
	var original struct {
		ID        string         `json:"id"`
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	require.NoError(t, json.Unmarshal(intent, &original))
	require.Equal(t, g5SecondWindowCallID, original.ID)
	require.Equal(t, "mecha_worker", original.Name)
	require.Equal(t, g5SecondWindowArgs(root), original.Arguments)
	require.Equal(t, "unknown", effectState)
	var senderSession, senderGeneration string
	var senderEpoch int64
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT pi_session_id,session_epoch,runtime_generation FROM managed_agents WHERE agent_id='g5-second-window-sender'`).Scan(&senderSession, &senderEpoch, &senderGeneration))
	binding := store.EmbeddedBinding{AgentID: "g5-second-window-sender", SessionID: senderSession, Epoch: senderEpoch, Generation: senderGeneration}
	operationID := embeddedWorkerRequestID(binding, g5SecondWindowCallID)
	var operationCount, workerCount, resultCount int
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM managed_http_operations WHERE request_id=?`, operationID).Scan(&operationCount))
	require.Equal(t, 1, operationCount, "worker operation must be durable before fault")
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM managed_agents WHERE agent_id='g5-second-window-child' AND launch_operation_id=?`, operationID).Scan(&workerCount))
	require.Equal(t, 1, workerCount, "independent worker must be linked to the operation")
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE kind='toolResult'`).Scan(&resultCount))
	require.Zero(t, resultCount, "SDK toolResult must not have committed")
	require.NoError(t, raw.Close())

	clock := deterministic.NewFixedClock(time.Now().Add(embeddedOwnerTTL + time.Minute))
	db, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer db.Close()
	operation, err := db.ManagedOperation(ctx, operationID)
	require.NoError(t, err)
	require.Equal(t, "POST", operation.Method)
	require.Equal(t, "/v1/managed-agents", operation.CanonicalPath)
	require.Equal(t, operationID, operation.RequestID)
	worker, err := db.ManagedAgent(ctx, "g5-second-window-child")
	require.NoError(t, err)
	require.Equal(t, operationID, worker.LaunchOperationID)
	require.NotEqual(t, binding.SessionID, worker.PiSessionID)
	require.NotEqual(t, binding.Generation, worker.RuntimeGeneration)
	workerRole, err := db.EmbeddedSessionRole(ctx, store.EmbeddedBinding{AgentID: worker.AgentID, SessionID: worker.PiSessionID, Epoch: worker.SessionEpoch, Generation: worker.RuntimeGeneration})
	require.NoError(t, err)
	require.Equal(t, "worker", workerRole)

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	attempted := make(chan struct{}, 1)
	continuation := make(chan *ai.ToolResultMessage, 1)
	var providerSteps atomic.Int32
	var recoveryGeneration, noticeSeq, noticeOwner, workerOwner int64
	var noticeID, noticeDigest, workerDigest string
	var recoveryMu sync.Mutex
	var noticeAcknowledged, workerAcknowledged bool
	callbackErrors := make(chan error, 4)
	// No callback uses FailNow: return a real SDK error after recording failure,
	// so the runner unwinds and Manager.Close cannot wait on a Goexit-stranded SDK.
	reject := func(format string, args ...any) *ai.AssistantMessage {
		err := fmt.Errorf(format, args...)
		callbackErrors <- err
		t.Error(err)
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: err.Error()}}, ai.StopError)
	}
	// These two recovery slots accept exactly the two persisted task identities,
	// once each. Arrival order is not authority.
	recoveryResponse := func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		step := providerSteps.Add(1)
		var lastUser ai.UserMessage
		for _, message := range h.Messages {
			if user, ok := message.(ai.UserMessage); ok {
				lastUser = user
			}
		}
		actual, marshalErr := json.Marshal(lastUser)
		content, contentErr := json.Marshal(lastUser.Content)
		childContent, _ := json.Marshal(ai.ContentList{ai.TextContent{Text: "independent task"}})
		source := "sender-claimed-notice"
		if bytes.Equal(content, childContent) {
			source = "independent-child-task"
		}
		prompt, promptErr := json.Marshal(h.Messages)
		t.Logf("second-window recovery candidate_source=%s step=%d process=%d transcript=%s marshal_error=%v", source, step, recoveryGeneration, prompt, promptErr)
		if marshalErr != nil || contentErr != nil || promptErr != nil {
			return reject("recovery source=%s marshal errors user=%v content=%v transcript=%v", source, marshalErr, contentErr, promptErr)
		}
		recoveryMu.Lock()
		defer recoveryMu.Unlock()
		read, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
		if err != nil {
			return reject("recovery source=%s open: %v", source, err)
		}
		defer read.Close()
		var body []byte
		if source == "independent-child-task" {
			if workerAcknowledged {
				return reject("duplicate independent child recovery")
			}
			err = read.QueryRowContext(ctx, `SELECT e.body,e.digest,s.owner_token FROM embedded_entries e JOIN embedded_sessions s ON s.agent_id=e.agent_id AND s.session_id=e.session_id AND s.epoch=e.epoch AND s.generation=e.generation AND s.current=1 AND s.role='worker' JOIN embedded_nonmail_provider_entries p ON p.agent_id=e.agent_id AND p.session_id=e.session_id AND p.epoch=e.epoch AND p.generation=e.generation AND p.user_seq=e.seq AND p.user_digest=e.digest WHERE e.agent_id=? AND e.session_id=? AND e.epoch=? AND e.generation=? AND e.seq=2 AND e.kind='user' AND s.owner_id IS NOT NULL AND s.owner_token>0 AND s.owner_expires_at>? AND (SELECT generation FROM embedded_process_generation_counter WHERE singleton=1)=?`, worker.AgentID, worker.PiSessionID, worker.SessionEpoch, worker.RuntimeGeneration, clock.Now().UTC().Format("2006-01-02T15:04:05.000Z"), recoveryGeneration).Scan(&body, &workerDigest, &workerOwner)
			if err != nil || workerDigest == "" || !bytes.Equal(body, actual) {
				return reject("independent child binding/user/provider/owner/process mismatch: %v", err)
			}
			workerAcknowledged = true
			t.Logf("second-window independent task agent=%s session=%s epoch=%d runtime=%s digest=%s owner=%d process=%d", worker.AgentID, worker.PiSessionID, worker.SessionEpoch, worker.RuntimeGeneration, workerDigest, workerOwner, recoveryGeneration)
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "independent task acknowledged"}}, ai.StopStop)
		}
		if noticeAcknowledged {
			return reject("duplicate sender interruption recovery")
		}
		err = read.QueryRowContext(ctx, `SELECT n.notice_id,n.entry_seq,e.digest,e.body,n.start_owner_token FROM embedded_interrupted_notices n JOIN embedded_entries e ON e.agent_id=n.agent_id AND e.session_id=n.session_id AND e.epoch=n.epoch AND e.generation=n.generation AND e.seq=n.entry_seq JOIN embedded_sessions s ON s.agent_id=n.agent_id AND s.session_id=n.session_id AND s.epoch=n.epoch AND s.generation=n.generation AND s.current=1 AND s.role='worker' AND s.owner_token=n.start_owner_token JOIN embedded_restart_wake_slots w ON w.agent_id=s.agent_id AND w.session_id=s.session_id AND w.epoch=s.epoch AND w.runtime_generation=s.generation AND w.wake_id=n.notice_id AND w.claim_owner_token=s.owner_token JOIN embedded_effects f ON f.agent_id=s.agent_id AND f.session_id=s.session_id AND f.epoch=s.epoch AND f.generation=s.generation WHERE n.agent_id=? AND n.session_id=? AND n.epoch=? AND n.generation=? AND n.source_seq=2 AND n.state='started' AND e.kind='user' AND w.slot='current' AND w.kind='interrupted' AND w.digest=e.digest AND w.state='claimed' AND w.process_generation=? AND s.owner_id IS NOT NULL AND s.owner_expires_at>? AND f.effect_id=? AND f.state='unknown' AND f.result IS NULL AND w.process_generation=(SELECT generation FROM embedded_process_generation_counter WHERE singleton=1)`, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation, recoveryGeneration, clock.Now().UTC().Format("2006-01-02T15:04:05.000Z"), g5SecondWindowCallID).Scan(&noticeID, &noticeSeq, &noticeDigest, &body, &noticeOwner)
		if err != nil || noticeDigest == "" || !bytes.Equal(body, actual) {
			return reject("sender exact claimed interruption mismatch: %v", err)
		}
		noticeAcknowledged = true
		t.Logf("second-window recovery notice=%s seq=%d digest=%s owner=%d process=%d", noticeID, noticeSeq, noticeDigest, noticeOwner, recoveryGeneration)
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "interruption acknowledged"}}, ai.StopStop)
	}
	reg.SetResponses([]providers.FauxResponseStep{
		recoveryResponse,
		recoveryResponse,
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			step := providerSteps.Add(1)
			prompt, promptErr := json.Marshal(h.Messages)
			t.Logf("second-window source=manual-retry provider_step=%d transcript=%s marshal_error=%v", step, prompt, promptErr)
			if promptErr != nil || step != 3 {
				return reject("manual retry step=%d transcript_error=%v", step, promptErr)
			}
			var lastUser ai.UserMessage
			for _, message := range h.Messages {
				if user, ok := message.(ai.UserMessage); ok {
					lastUser = user
				}
			}
			content, err := json.Marshal(lastUser.Content)
			wanted, _ := json.Marshal(ai.ContentList{ai.TextContent{Text: "retry original call"}})
			if err != nil || !bytes.Equal(content, wanted) {
				return reject("manual retry unexpected user: %v", err)
			}
			actual, err := json.Marshal(lastUser)
			if err != nil {
				return reject("manual retry user marshal: %v", err)
			}
			read, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
			if err != nil {
				return reject("manual retry open: %v", err)
			}
			defer read.Close()
			var body []byte
			var digest string
			err = read.QueryRowContext(ctx, `SELECT e.body,e.digest FROM embedded_entries e JOIN embedded_sessions s ON s.agent_id=e.agent_id AND s.session_id=e.session_id AND s.epoch=e.epoch AND s.generation=e.generation AND s.current=1 JOIN embedded_nonmail_provider_entries p ON p.agent_id=e.agent_id AND p.session_id=e.session_id AND p.epoch=e.epoch AND p.generation=e.generation AND p.user_seq=e.seq AND p.user_digest=e.digest WHERE e.agent_id=? AND e.session_id=? AND e.epoch=? AND e.generation=? AND e.kind='user' AND e.seq=(SELECT max(seq) FROM embedded_entries WHERE agent_id=e.agent_id AND session_id=e.session_id AND epoch=e.epoch AND generation=e.generation) AND s.owner_token=? AND s.owner_id IS NOT NULL AND s.owner_expires_at>? AND (SELECT generation FROM embedded_process_generation_counter WHERE singleton=1)=?`, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation, noticeOwner, clock.Now().UTC().Format("2006-01-02T15:04:05.000Z"), recoveryGeneration).Scan(&body, &digest)
			if err != nil || digest == "" || !bytes.Equal(body, actual) {
				return reject("manual retry exact binding/user/provider/owner/process mismatch: %v", err)
			}
			attempted <- struct{}{}
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("mecha_worker", g5SecondWindowArgs(root), g5SecondWindowCallID)}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			step := providerSteps.Add(1)
			prompt, promptErr := json.Marshal(h.Messages)
			t.Logf("second-window source=retry-continuation provider_step=%d transcript=%s marshal_error=%v", step, prompt, promptErr)
			if promptErr != nil || step != 4 {
				return reject("retry continuation step=%d transcript_error=%v", step, promptErr)
			}
			var result *ai.ToolResultMessage
			for _, msg := range h.Messages {
				if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == g5SecondWindowCallID {
					copy := r
					result = &copy
				}
			}
			continuation <- result
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
		},
	})
	m := g5SecondWindowManager(t, db, root, reg, nil)
	defer m.Close(ctx)
	recoveryGeneration = setEmbeddedRecoveryGeneration(t, ctx, db, m)
	defer func() {
		if !t.Failed() {
			return
		}
		t.Logf("second-window before cleanup clock=%s recovery_process=%d configured_faux_steps=4 entered_faux_steps=%d child_exit=%d original_effect=%s", clock.Now(), recoveryGeneration, providerSteps.Load(), cmd.ProcessState.ExitCode(), effectState)
		cwdPrivacyManagedSnapshot(t, path)
		read, readErr := sql.Open("sqlite", "file:"+path+"?mode=ro")
		if readErr != nil {
			t.Logf("second-window snapshot open_error=%v", readErr)
			return
		}
		defer read.Close()
		for _, query := range []string{
			`SELECT json_group_array(json_object('agent',agent_id,'session',pi_session_id,'epoch',session_epoch,'runtime',runtime_generation,'lifecycle',lifecycle_state,'error',last_error_code,'message',last_error_message)) FROM managed_agents`,
			`SELECT json_group_array(json_object('seq',seq,'agent',agent_id,'kind',kind,'body',CAST(body AS TEXT))) FROM embedded_entries`,
			`SELECT json_group_array(json_object('id',effect_id,'state',state,'intent',CAST(intent AS TEXT),'result',CAST(result AS TEXT))) FROM embedded_effects`,
			`SELECT json_group_array(json_object('process',generation,'incarnation',incarnation_key)) FROM embedded_process_generations`,
		} {
			var snapshot string
			queryErr := read.QueryRowContext(ctx, query).Scan(&snapshot)
			t.Logf("second-window query=%s result=%s query_error=%v", query, snapshot, queryErr)
		}
	}()
	require.NoError(t, m.RecoverEmbeddedRoots(ctx))
	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		_, senderReady := m.embeddedOwners[binding.AgentID]
		_, childReady := m.embeddedOwners[worker.AgentID]
		return senderReady && childReady
	}, 4*time.Second, 10*time.Millisecond)
	m.mu.Lock()
	owner := m.embeddedOwners[binding.AgentID]
	childOwner := m.embeddedOwners[worker.AgentID]
	m.mu.Unlock()
	select {
	case callbackErr := <-callbackErrors:
		require.NoError(t, callbackErr)
	default:
	}
	recoveryMu.Lock()
	noticeDone, workerDone := noticeAcknowledged, workerAcknowledged
	recoveryMu.Unlock()
	require.True(t, noticeDone)
	require.True(t, workerDone)
	require.Equal(t, noticeOwner, owner.binding.OwnerToken)
	require.Equal(t, workerOwner, childOwner.binding.OwnerToken)
	require.EqualValues(t, 2, providerSteps.Load(), "recovery acknowledgments are not the manual retry")
	read, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer read.Close()
	var checkedSeq int64
	var checkedBody []byte
	require.NoError(t, read.QueryRowContext(ctx, `SELECT e.seq,e.body FROM embedded_restart_wake_slots w JOIN embedded_restart_wake_checked_origins o ON o.agent_id=w.agent_id AND o.slot=w.slot AND o.session_id=w.session_id AND o.epoch=w.epoch AND o.runtime_generation=w.runtime_generation AND o.wake_id=w.wake_id AND o.process_generation=w.process_generation AND o.claim_owner_token=w.claim_owner_token JOIN embedded_entries e ON e.agent_id=o.agent_id AND e.session_id=o.session_id AND e.epoch=o.epoch AND e.generation=o.runtime_generation AND e.seq=o.seq WHERE w.agent_id=? AND w.session_id=? AND w.epoch=? AND w.runtime_generation=? AND w.wake_id=? AND w.process_generation=? AND w.claim_owner_token=? AND w.state='checked-complete' AND e.kind='assistant' AND e.parent_digest=?`, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation, noticeID, recoveryGeneration, noticeOwner, noticeDigest).Scan(&checkedSeq, &checkedBody))
	require.Equal(t, noticeSeq+1, checkedSeq)
	checkedMessage, err := ai.UnmarshalMessage(checkedBody)
	require.NoError(t, err)
	checkedFinal, ok := checkedMessage.(ai.AssistantMessage)
	require.True(t, ok)
	require.Equal(t, ai.StopStop, checkedFinal.StopReason)
	require.Equal(t, ai.ContentList{ai.TextContent{Text: "interruption acknowledged"}}, checkedFinal.Content)
	var childFinalBody []byte
	require.NoError(t, read.QueryRowContext(ctx, `SELECT e.body FROM embedded_entries e JOIN embedded_sessions s ON s.agent_id=e.agent_id AND s.session_id=e.session_id AND s.epoch=e.epoch AND s.generation=e.generation AND s.current=1 AND s.role='worker' WHERE e.agent_id=? AND e.session_id=? AND e.epoch=? AND e.generation=? AND e.seq=3 AND e.kind='assistant' AND e.parent_digest=? AND s.owner_token=? AND s.owner_id IS NOT NULL AND s.owner_expires_at>? AND (SELECT generation FROM embedded_process_generation_counter WHERE singleton=1)=?`, worker.AgentID, worker.PiSessionID, worker.SessionEpoch, worker.RuntimeGeneration, workerDigest, workerOwner, clock.Now().UTC().Format("2006-01-02T15:04:05.000Z"), recoveryGeneration).Scan(&childFinalBody))
	childMessage, err := ai.UnmarshalMessage(childFinalBody)
	require.NoError(t, err)
	childFinal, ok := childMessage.(ai.AssistantMessage)
	require.True(t, ok)
	require.Equal(t, ai.StopStop, childFinal.StopReason)
	require.Equal(t, ai.ContentList{ai.TextContent{Text: "independent task acknowledged"}}, childFinal.Content)
	require.NoError(t, read.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE kind='toolResult'`).Scan(&resultCount))
	require.Zero(t, resultCount, "checked recovery acknowledgment is not an original launch result")
	preRetryEffect, err := db.RecoverEmbeddedEffect(ctx, owner.binding, g5SecondWindowCallID)
	require.NoError(t, err)
	require.Equal(t, "unknown", preRetryEffect.State)
	require.Equal(t, intent, preRetryEffect.Intent)
	require.Empty(t, preRetryEffect.Result)
	require.False(t, preRetryEffect.ShouldReplay)
	select {
	case <-attempted:
		t.Fatal("recovery retried the original worker launch")
	default:
	}
	sender, err := db.ManagedAgent(ctx, binding.AgentID)
	require.NoError(t, err)
	require.Error(t, m.runEmbeddedSessionPrompt(context.WithValue(ctx, embeddedOwnerContextKey{}, owner), *sender, "retry original call"))
	select {
	case <-attempted:
	default:
		t.Fatal("fresh SDK did not attempt original call ID")
	}
	// Original-ID admission is fatal: no Execute, checked result or continuation.
	select {
	case <-continuation:
		t.Fatal("rejected original ID continued the provider")
	default:
	}
	require.NoError(t, read.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE kind='toolResult'`).Scan(&resultCount))
	require.Zero(t, resultCount, "rejected original ID creates no new SDK receipt")
	require.EqualValues(t, 3, providerSteps.Load())
	select {
	case callbackErr := <-callbackErrors:
		require.NoError(t, callbackErr)
	default:
	}
	// Exercise reconciliation directly too, under the recovered sender owner.
	reconciled, reconcileErr := m.embeddedWorkerLaunchTool(owner).Execute(ctx, g5SecondWindowCallID, g5SecondWindowArgs(root), nil)
	if reconcileErr == nil {
		for _, content := range reconciled.Content {
			if text, ok := content.(ai.TextContent); ok {
				require.NotContains(t, text.Text, `"status":"accepted"`, "first behavioral RED: operation and worker do not prove an SDK result commit")
			}
		}
	} else {
		require.ErrorContains(t, reconcileErr, "unknown", "first behavioral RED: recovered launch must expose an unknown SDK outcome, not a generic idempotency conflict")
	}
	effect, err := db.RecoverEmbeddedEffect(ctx, owner.binding, g5SecondWindowCallID)
	require.NoError(t, err)
	require.Equal(t, "unknown", effect.State, "retry must expose unknown, not invent a completed SDK result")
	require.False(t, effect.ShouldReplay)
	require.Equal(t, preRetryEffect, effect, "fresh retry error must not alter the original intent/effect/result")
	var outboxCount int
	require.NoError(t, read.QueryRowContext(ctx, `SELECT count(*) FROM embedded_reply_outbox`).Scan(&outboxCount))
	require.Zero(t, outboxCount)
	require.NoError(t, read.QueryRowContext(ctx, `SELECT count(*) FROM managed_http_operations WHERE request_id=?`, operationID).Scan(&operationCount))
	require.Equal(t, 1, operationCount)
	agents, err := db.ManagedAgents(ctx)
	require.NoError(t, err)
	require.Len(t, agents, 2, "no extra worker reservation or launch on retry")
	replayedOp, err := db.ManagedOperation(ctx, operationID)
	require.NoError(t, err)
	require.Equal(t, operation.OperationID, replayedOp.OperationID)
	require.Equal(t, operation.CanonicalBodyHash, replayedOp.CanonicalBodyHash)
	replayedWorker, err := db.ManagedAgent(ctx, worker.AgentID)
	require.NoError(t, err)
	require.Equal(t, worker.PiSessionID, replayedWorker.PiSessionID)
	require.Equal(t, worker.RuntimeGeneration, replayedWorker.RuntimeGeneration)
	_, err = m.embeddedWorkerLaunchTool(owner).Execute(ctx, g5SecondWindowCallID, map[string]any{"action": "launch", "name": "g5-different", "cwd": root, "instruction": "changed"}, nil)
	require.ErrorContains(t, err, "conflict", "same sender call ID with changed arguments must conflict")
}
