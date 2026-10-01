package managed

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// The checked SDK result is the only authority for observation. The read-only
// command has no external marker; its intent alone cannot prove completion.
// The provider is mocked, but Bash and its fixed command body are real. Neither
// process start nor gate release is a command-body execution counter.
func TestEmbeddedG5BashObservedExactColdReplayRED(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base := t.TempDir()
	project := filepath.Join(base, "project")
	require.NoError(t, os.Mkdir(project, 0700))
	const callID = "g5-bash-observed-once"
	const command = "grep -F -n -- literal one.txt"
	require.NoError(t, os.WriteFile(filepath.Join(project, "one.txt"), []byte("literal\n"), 0600))
	path := filepath.Join(base, "router.db")
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	var starts, bashStarts, gateReleases, gateErrors, checkedResults, replayCalls, neutralCalls atomic.Int32
	checked := make(chan ai.ToolResultMessage, 2)
	reg.SetResponses([]providers.FauxResponseStep{
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			starts.Add(1)
			found := false
			for _, msg := range h.Messages {
				if sys, ok := msg.(ai.SystemMessage); ok {
					for _, tool := range sys.ToolsAdded {
						if tool.Name == "bash" {
							found = true
							declaration, e := json.Marshal(tool)
							require.NoError(t, e)
							t.Logf("first SDK callback bash declaration=%s", declaration)
						}
					}
				}
			}
			require.True(t, found, "real saved SDK root must advertise bash")
			t.Logf("first SDK callback tool=bash call_id=%s arguments_command=%q", callID, command)
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("bash", map[string]any{"command": command}, callID)}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			starts.Add(1)
			for _, msg := range h.Messages {
				if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == callID {
					checkedResults.Add(1)
					body, e := json.Marshal(r)
					require.NoError(t, e)
					t.Logf("post-append SDK callback tool=%s call_id=%s is_error=%t result_sha256=%x", r.ToolName, r.ToolCallID, r.IsError, sha256.Sum256(body))
					checked <- r
				}
			}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "checked"}}, ai.StopStop)
		},
	})
	newManager := func(db *store.Store) *Manager {
		t.Helper()
		m, e := NewManager(ManagerOptions{Store: db, Manifest: manifest,
			Config:                Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{project}},
			CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
			EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
			CodexCredentialStore:  testfixture.CodexAdmission(t),
		})
		require.NoError(t, e)
		m.embeddedBashAfterStart = func(_ int, gate *os.File) {
			bashStarts.Add(1)
			// Installing this hook adds a blocking read before the real command.
			// Release it, as the other successful real-Bash fixtures do. Leaving
			// it open without a newline times out before the command body starts.
			n, e := gate.Write([]byte("\n"))
			if e != nil || n != 1 {
				gateErrors.Add(1)
				t.Errorf("Bash gate release: bytes=%d err=%v", n, e)
			} else {
				gateReleases.Add(1)
			}
			if e := gate.Close(); e != nil {
				gateErrors.Add(1)
				t.Errorf("Bash gate close: %v", e)
			}
		}
		return m
	}
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	m := newManager(db)
	t.Cleanup(func() { _ = m.Close(context.Background()); _ = db.Close() })
	t.Cleanup(func() {
		t.Logf("Bash boundaries bash_start_count=%d gate_release_count=%d gate_error_count=%d checked_result_count=%d command_body_exec_count=unmeasured (no command-body hook in real runner)", bashStarts.Load(), gateReleases.Load(), gateErrors.Load(), checkedResults.Load())
		if t.Failed() {
			snapshotG5Bash(t, path, "g5-bash-root", callID, starts.Load()+replayCalls.Load()+neutralCalls.Load(), bashStarts.Load(), nil)
		}
	})
	_, err = m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "g5-bash-root", Cwd: project, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "check the literal", RequestID: "a5555555-5555-4555-8555-5555555555b1"})
	require.NoError(t, err)
	// Test-only bounded diagnosis. Never print stored result bodies or provider text.
	diagnoseWait := func() string {
		probeCtx, probeCancel := context.WithTimeout(context.Background(), time.Second)
		defer probeCancel()
		lifecycle, activity, code, category := "unavailable", "unavailable", "unavailable", "unknown"
		if saved, e := db.ManagedAgent(probeCtx, "g5-bash-root"); e == nil {
			lifecycle, activity, code = saved.LifecycleState, saved.ActivityState, saved.LastErrorCode
			switch {
			case strings.Contains(saved.LastErrorMessage, "bash SDK result differs from checked effect"):
				category = "bash-result-preappend-mismatch"
			case strings.Contains(saved.LastErrorMessage, "bash outcome UNKNOWN"):
				category = "bash-outcome-unknown"
			case strings.Contains(saved.LastErrorMessage, "bash outcome unknown: no checked SDK result"):
				category = "bash-result-preappend-missing"
			case strings.Contains(saved.LastErrorMessage, "bash path denied"):
				category = "bash-path-denied"
			case saved.LastErrorMessage == "":
				category = "none"
			default:
				category = "other"
			}
		}
		effectState, effectCount, toolResults := "none", 0, 0
		if ro, e := sql.Open("sqlite", "file:"+path+"?mode=ro"); e == nil {
			defer ro.Close()
			if e = ro.QueryRowContext(probeCtx, `SELECT state FROM embedded_effects WHERE effect_id=?`, callID).Scan(&effectState); e != nil {
				effectState = "unavailable"
			}
			_ = ro.QueryRowContext(probeCtx, `SELECT count(*) FROM embedded_effects WHERE effect_id=?`, callID).Scan(&effectCount)
			_ = ro.QueryRowContext(probeCtx, `SELECT count(*) FROM embedded_entries WHERE agent_id=? AND kind='toolResult'`, "g5-bash-root").Scan(&toolResults)
		}
		return fmt.Sprintf("lifecycle=%s activity=%s error_code=%s error_category=%s effect_state=%s effect_count=%d toolResult_count=%d provider_callbacks=%d bash_start_count=%d gate_release_count=%d gate_error_count=%d checked_result_count=%d command_body_exec_count=unmeasured", lifecycle, activity, code, category, effectState, effectCount, toolResults, starts.Load(), bashStarts.Load(), gateReleases.Load(), gateErrors.Load(), checkedResults.Load())
	}
	var result ai.ToolResultMessage
	wait := time.NewTicker(50 * time.Millisecond)
	defer wait.Stop()
	for waiting := true; waiting; {
		select {
		case result = <-checked:
			waiting = false
		case <-wait.C:
			saved, e := db.ManagedAgent(ctx, "g5-bash-root")
			if e == nil && saved.LifecycleState == "failed" {
				t.Fatalf("no checked SDK bash result: %s", diagnoseWait())
			}
		case <-ctx.Done():
			t.Fatalf("no checked SDK bash result: %s", diagnoseWait())
		}
	}
	require.EqualValues(t, 1, bashStarts.Load())
	require.EqualValues(t, 1, gateReleases.Load())
	require.Zero(t, gateErrors.Load())
	require.Equal(t, callID, result.ToolCallID)
	require.Equal(t, "bash", result.ToolName)
	require.False(t, result.IsError)
	require.Contains(t, fmt.Sprint(result.Content), "exit=0\n1:literal")
	canonical, err := json.Marshal(result)
	require.NoError(t, err)

	ro, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer ro.Close()
	var agentID, sessionID, generation, state string
	var epoch int64
	var intent, observed []byte
	err = ro.QueryRowContext(ctx, `SELECT agent_id,session_id,epoch,generation,intent,state,result FROM embedded_effects WHERE effect_id=?`, callID).Scan(&agentID, &sessionID, &epoch, &generation, &intent, &state, &observed)
	require.NoError(t, err)
	binding := inspectManagedOwnerBinding(t, path, store.EmbeddedBinding{AgentID: agentID, SessionID: sessionID, Epoch: epoch, Generation: generation})
	require.Equal(t, "g5-bash-root", agentID)
	var original ai.ToolCall
	require.NoError(t, json.Unmarshal(intent, &original))
	require.Equal(t, callID, original.ID)
	require.Equal(t, "bash", original.Name)
	require.Equal(t, map[string]any{"command": command}, original.Arguments)
	t.Logf("bash effect agent=%s session=%s epoch=%d generation=%s state=%s intent_sha256=%x result_sha256=%x", agentID, sessionID, epoch, generation, state, sha256.Sum256(intent), sha256.Sum256(observed))
	require.Equal(t, "observed", state, "G5 first RED: checked successful bash result has no observed effect; cold replay and conflict gates UNEXECUTED")
	require.True(t, bytes.Equal(canonical, observed), "effect bytes differ from exact checked SDK result")
	var entry []byte
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT body FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND kind='toolResult'`, agentID, sessionID, epoch, generation).Scan(&entry))
	require.True(t, bytes.Equal(canonical, entry), "ordered committed toolResult differs from checked result")

	// Only after the observed gate passes: release original Manager and Store,
	// reopen against an expired lease via a private injected clock (no SQL epoch).
	var originalFinalSeq int64
	var originalFinalDigest string
	var originalFinalBody []byte
	require.Eventually(t, func() bool {
		return ro.QueryRowContext(ctx, `SELECT seq,digest,body FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND kind='assistant' AND json_extract(body,'$.stopReason')='stop' ORDER BY seq DESC LIMIT 1`, agentID, sessionID, epoch, generation).Scan(&originalFinalSeq, &originalFinalDigest, &originalFinalBody) == nil
	}, 4*time.Second, 10*time.Millisecond, "original checked result must reach its own checked final before cold recovery")
	require.NoError(t, m.Close(ctx))
	originalFinalMessage, err := ai.UnmarshalMessage(originalFinalBody)
	require.NoError(t, err)
	originalFinal, ok := originalFinalMessage.(ai.AssistantMessage)
	require.True(t, ok)
	require.Equal(t, ai.StopStop, originalFinal.StopReason)
	require.Equal(t, ai.ContentList{ai.TextContent{Text: "checked"}}, originalFinal.Content)
	require.NoError(t, db.Close())
	db, err = store.Open(ctx, path, store.Options{Clock: deterministic.NewFixedClock(time.Now().Add(embeddedOwnerTTL + time.Minute))})
	require.NoError(t, err)
	defer db.Close()
	m = newManager(db)
	defer m.Close(context.Background())
	// This runs before the cold Manager/Store defers, not after cleanup.
	defer func() {
		if t.Failed() {
			snapshotG5Bash(t, path, "g5-bash-root", callID, starts.Load()+replayCalls.Load()+neutralCalls.Load(), bashStarts.Load(), nil)
		}
	}()
	type replayObservation struct {
		baselineCount, baselineLast, count, newIndex int
		result                                       *ai.ToolResultMessage
	}
	replayChecked := make(chan replayObservation, 1)
	baselineCount, baselineLast := 0, -1
	replaySteps := []providers.FauxResponseStep{
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			replayCalls.Add(1)
			snapshotG5Bash(t, path, "g5-bash-root", callID, starts.Load()+replayCalls.Load()+neutralCalls.Load(), bashStarts.Load(), &h)
			// Snapshot matching results already present in restored history.
			for i, msg := range h.Messages {
				if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == callID {
					baselineCount++
					baselineLast = i
				}
			}
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("bash", map[string]any{"command": command}, callID)}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			replayCalls.Add(1)
			snapshotG5Bash(t, path, "g5-bash-root", callID, starts.Load()+replayCalls.Load()+neutralCalls.Load(), bashStarts.Load(), &h)
			report := replayObservation{baselineCount: baselineCount, baselineLast: baselineLast, newIndex: -1}
			for i, msg := range h.Messages {
				if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == callID {
					report.count++
					if i > baselineLast {
						copy := r
						report.result = &copy
						report.newIndex = i
					}
				}
			}
			replayChecked <- report
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "replayed"}}, ai.StopStop)
		},
	}
	// Cold awareness is a separate checked wake, never a same-ID tool replay.
	var neutralSeq int64
	var neutralDigest string
	neutralBody, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "Continue working from your saved context."}}})
	require.NoError(t, err)
	process := setEmbeddedRecoveryGeneration(t, ctx, db, m)
	reg.SetResponses([]providers.FauxResponseStep{
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			// The exact saved predecessor, source turn, current lease and process
			// must authorize this callback before it can receive a stop final.
			require.NoError(t, ro.QueryRowContext(ctx, `SELECT u.seq,u.digest FROM embedded_sessions s JOIN embedded_restart_wake_slots w ON w.agent_id=s.agent_id AND w.session_id=s.session_id AND w.epoch=s.epoch AND w.runtime_generation=s.generation AND w.claim_owner_token=s.owner_token JOIN embedded_entries u ON u.agent_id=s.agent_id AND u.session_id=s.session_id AND u.epoch=s.epoch AND u.generation=s.generation JOIN embedded_nonmail_provider_entries p ON p.agent_id=u.agent_id AND p.session_id=u.session_id AND p.epoch=u.epoch AND p.generation=u.generation AND p.user_seq=u.seq AND p.user_digest=u.digest WHERE s.current=1 AND s.agent_id=? AND s.session_id=? AND s.epoch=? AND s.generation=? AND s.owner_token>? AND w.slot='current' AND w.kind='root-idle' AND w.state='claimed' AND w.notice_id IS NULL AND w.process_generation=? AND w.wake_id=? AND w.digest=? AND u.kind='user' AND u.seq=? AND u.parent_digest=? AND u.body=? AND u.seq=(SELECT max(seq) FROM embedded_entries WHERE agent_id=s.agent_id AND session_id=s.session_id AND epoch=s.epoch)`, agentID, sessionID, epoch, generation, binding.OwnerToken, process, fmt.Sprintf("settled:%s:2:%d", agentID, process), originalFinalDigest, originalFinalSeq+1, originalFinalDigest, neutralBody).Scan(&neutralSeq, &neutralDigest))
			require.NotEmpty(t, h.Messages)
			last, e := json.Marshal(h.Messages[len(h.Messages)-1])
			require.NoError(t, e)
			require.Equal(t, neutralBody, last, "only the exact fenced neutral user may complete recovery")
			require.EqualValues(t, 1, neutralCalls.Add(1), "neutral wake is one-shot")
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "neutral recovery complete"}}, ai.StopStop)
		},
	})
	require.NoError(t, m.RecoverEmbeddedRoots(ctx))
	require.Eventually(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); _, ok := m.embeddedOwners[agentID]; return ok }, 4*time.Second, 10*time.Millisecond)
	require.EqualValues(t, 2, starts.Load(), "cold reopen must not restart the original tool turn")
	require.Zero(t, replayCalls.Load(), "cold reopen must not attempt same-ID replay")
	require.EqualValues(t, 1, neutralCalls.Load(), "only one exact fenced awareness callback")
	var neutralFinalBody []byte
	var neutralFinalSeq int64
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT f.seq,f.body FROM embedded_restart_wake_slots w JOIN embedded_sessions s ON s.agent_id=w.agent_id AND s.session_id=w.session_id AND s.epoch=w.epoch AND s.generation=w.runtime_generation AND s.owner_token=w.claim_owner_token JOIN embedded_restart_wake_checked_origins o ON o.agent_id=w.agent_id AND o.slot=w.slot AND o.session_id=w.session_id AND o.epoch=w.epoch AND o.runtime_generation=w.runtime_generation AND o.wake_id=w.wake_id AND o.process_generation=w.process_generation AND o.claim_owner_token=w.claim_owner_token JOIN embedded_entries f ON f.agent_id=o.agent_id AND f.session_id=o.session_id AND f.epoch=o.epoch AND f.generation=o.runtime_generation AND f.seq=o.seq WHERE s.current=1 AND w.agent_id=? AND w.session_id=? AND w.epoch=? AND w.runtime_generation=? AND w.slot='current' AND w.kind='root-idle' AND w.state='checked-complete' AND w.process_generation=? AND w.wake_id=? AND w.digest=? AND f.kind='assistant' AND f.parent_digest=?`, agentID, sessionID, epoch, generation, process, fmt.Sprintf("settled:%s:2:%d", agentID, process), originalFinalDigest, neutralDigest).Scan(&neutralFinalSeq, &neutralFinalBody))
	require.Equal(t, neutralSeq+1, neutralFinalSeq)
	neutralMessage, err := ai.UnmarshalMessage(neutralFinalBody)
	require.NoError(t, err)
	neutralFinal, ok := neutralMessage.(ai.AssistantMessage)
	require.True(t, ok)
	require.Equal(t, ai.StopStop, neutralFinal.StopReason)
	require.Equal(t, ai.ContentList{ai.TextContent{Text: "neutral recovery complete"}}, neutralFinal.Content)
	require.EqualValues(t, 1, bashStarts.Load(), "neutral must not spawn Bash")
	var coldResult []byte
	var coldState string
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT state,result FROM embedded_effects WHERE effect_id=?`, callID).Scan(&coldState, &coldResult))
	require.Equal(t, "observed", coldState)
	require.Equal(t, canonical, coldResult, "neutral cannot mutate the exact checked effect")
	reg.SetResponses(replaySteps)
	// Removing the only input makes any second execution impossible. An observed
	// result must replay without touching the filesystem or starting grep again.
	require.NoError(t, os.Remove(filepath.Join(project, "one.txt")))
	m.mu.Lock()
	owner := m.embeddedOwners[agentID]
	m.mu.Unlock()
	require.Equal(t, binding.AgentID, owner.binding.AgentID)
	require.Equal(t, binding.SessionID, owner.binding.SessionID)
	require.Equal(t, binding.Epoch, owner.binding.Epoch)
	require.Equal(t, binding.Generation, owner.binding.Generation)
	saved, err := db.ManagedAgent(ctx, agentID)
	require.NoError(t, err)
	require.NoError(t, m.runEmbeddedSessionPrompt(context.WithValue(ctx, embeddedOwnerContextKey{}, owner), *saved, "explicit same call replay"))
	select {
	case report := <-replayChecked:
		require.Greater(t, report.count, report.baselineCount, "no new matching SDK result after replay: baseline_count=%d baseline_last_index=%d second_count=%d", report.baselineCount, report.baselineLast, report.count)
		require.Greater(t, report.newIndex, report.baselineLast, "no post-baseline SDK result: baseline_last_index=%d new_index=%d", report.baselineLast, report.newIndex)
		require.NotNil(t, report.result, "no new matching SDK result")
		body, e := json.Marshal(report.result)
		require.NoError(t, e)
		if !bytes.Equal(canonical, body) {
			var expected, actual map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(canonical, &expected))
			require.NoError(t, json.Unmarshal(body, &actual))
			field := func(name string) string {
				return fmt.Sprintf("%s_equal=%t %s_expected_sha256=%x %s_actual_sha256=%x", name, bytes.Equal(expected[name], actual[name]), name, sha256.Sum256(expected[name]), name, sha256.Sum256(actual[name]))
			}
			t.Fatalf("new SDK replay JSON differs: %s; %s; %s; %s; %s; %s", field("timestamp"), field("toolCallId"), field("toolName"), field("isError"), field("details"), field("content"))
		}
	case <-ctx.Done():
		t.Fatal("explicit replay did not return new checked SDK result")
	}
	require.EqualValues(t, 2, replayCalls.Load())
	require.EqualValues(t, 1, bashStarts.Load(), "observed replay must not spawn Bash")
	require.EqualValues(t, 1, gateReleases.Load(), "observed replay must not release a new command gate")
	var after []byte
	var afterState string
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT state,result FROM embedded_effects WHERE effect_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, callID, agentID, sessionID, epoch, generation).Scan(&afterState, &after))
	require.Equal(t, "observed", afterState)
	require.True(t, bytes.Equal(observed, after), "replay mutated immutable effect result")

	// A changed argument under this same ID is not a replay. Give the new
	// command a real, different input: it must be rejected by identity, not
	// merely by the missing file used to prove the identical replay.
	require.NoError(t, os.WriteFile(filepath.Join(project, "two.txt"), []byte("different\n"), 0600))
	conflictChecked := make(chan replayObservation, 1)
	conflictBaselineCount, conflictBaselineLast := 0, -1
	var conflictCalls atomic.Int32
	reg.SetResponses([]providers.FauxResponseStep{
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			conflictCalls.Add(1)
			for i, msg := range h.Messages {
				if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == callID {
					conflictBaselineCount++
					conflictBaselineLast = i
				}
			}
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("bash", map[string]any{"command": "grep -F -n -- different two.txt"}, callID)}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			conflictCalls.Add(1)
			report := replayObservation{baselineCount: conflictBaselineCount, baselineLast: conflictBaselineLast, newIndex: -1}
			for i, msg := range h.Messages {
				if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == callID {
					report.count++
					if i > conflictBaselineLast {
						copy := r
						report.result = &copy
						report.newIndex = i
					}
				}
			}
			conflictChecked <- report
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "conflict checked"}}, ai.StopStop)
		},
	})
	conflictErr := m.runEmbeddedSessionPrompt(context.WithValue(ctx, embeddedOwnerContextKey{}, owner), *saved, "explicit conflicting call ID")
	// The first provider snapshot includes restored results. Only a result added
	// after that snapshot can prove the conflicting call was checked by the SDK.
	var conflictReport *replayObservation
	select {
	case report := <-conflictChecked:
		conflictReport = &report
	default:
	}
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT state,result FROM embedded_effects WHERE effect_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, callID, agentID, sessionID, epoch, generation).Scan(&afterState, &after))
	require.Equal(t, "observed", afterState)
	require.True(t, bytes.Equal(observed, after), "conflicting call mutated immutable effect result")
	require.EqualValues(t, 1, bashStarts.Load(), "conflicting call must not spawn Bash")
	require.EqualValues(t, 1, gateReleases.Load(), "conflicting call must not release a new command gate")
	if conflictReport == nil {
		require.EqualValues(t, 1, conflictCalls.Load(), "unexpected conflicting-call provider turn count")
		// A pre-intent conflict may stop the SDK before it emits a checked
		// ToolResult. That is fail-closed, not a checked denial.
		require.ErrorContains(t, conflictErr, "conflict", "no new checked SDK result or explicit pre-intent conflict")
		t.Log("changed arguments rejected before checked SDK result (fatal pre-intent conflict)")
		return
	}
	require.EqualValues(t, 2, conflictCalls.Load(), "checked conflict must reach second provider callback")
	require.Greater(t, conflictReport.count, conflictReport.baselineCount, "no new matching SDK result after conflict: baseline_count=%d baseline_last_index=%d second_count=%d", conflictReport.baselineCount, conflictReport.baselineLast, conflictReport.count)
	require.Greater(t, conflictReport.newIndex, conflictReport.baselineLast, "no post-baseline conflicting SDK result: baseline_last_index=%d new_index=%d", conflictReport.baselineLast, conflictReport.newIndex)
	require.NotNil(t, conflictReport.result, "no new matching SDK result")
	require.True(t, conflictReport.result.IsError, "changed args spawned or replayed successful bash")
	require.NotContains(t, fmt.Sprint(conflictReport.result.Content), "1:different")
}
