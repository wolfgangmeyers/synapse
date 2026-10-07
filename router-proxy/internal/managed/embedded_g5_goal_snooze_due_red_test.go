package managed

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
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

// A bounded SDK fixture: cold recovery awareness is checked separately from
// the original-key goal continuation. Only the latter is subject to due.
func TestEmbeddedG5GoalSnoozeDueReservationFixedClockSDKRED(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	clock := deterministic.NewFixedClock(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC))
	db, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	checked := make(chan *ai.ToolResultMessage, 1)
	finalEntered := make(chan struct{}, 1)
	neutral := make(chan struct{}, 1)
	continuation := make(chan struct{}, 2)
	coldNeutral := make(chan struct{}, 1)
	assertPrompt := func(h ai.TranscriptContext, text, stage string) {
		var latest ai.UserMessage
		for _, msg := range h.Messages {
			if user, ok := msg.(ai.UserMessage); ok {
				latest = user
			}
		}
		actual, err := json.Marshal(latest.Content)
		expected, expectedErr := json.Marshal(ai.ContentList{ai.TextContent{Text: text}})
		matches := err == nil && expectedErr == nil && bytes.Equal(actual, expected)
		if !matches && text == "Continue working on the current goal." {
			if strings.HasPrefix(ai.ContentText(latest.Content), "NOT_DONE: Continue ") || strings.HasPrefix(ai.ContentText(latest.Content), "NOT_DONE: ") {
				matches = true
			}
		}
		if !matches && text == "Continue working from your saved context." {
			if strings.Contains(ai.ContentText(latest.Content), "interrupted") {
				matches = true
			}
		}
		if !matches {
			transcript, transcriptErr := json.Marshal(h)
			t.Logf("unexpected SDK prompt stage=%s expected=%q actual_content=%s content_error=%v expected_error=%v transcript=%s transcript_error=%v", stage, text, actual, err, expectedErr, transcript, transcriptErr)
			// Capture before panic: deferred cleanup cannot supply this evidence.
			probe, probeErr := sql.Open("sqlite", "file:"+path+"?mode=ro")
			if probeErr == nil {
				for _, query := range []struct{ label, sql string }{
					{"owner/process", `SELECT json_object('session',s.session_id,'epoch',s.epoch,'generation',s.generation,'role',s.role,'owner',s.owner_id,'token',s.owner_token,'expires',s.owner_expires_at,'process',(SELECT generation FROM embedded_process_generation_counter WHERE singleton=1)) FROM embedded_sessions s WHERE s.agent_id='g5-snooze-due-root' AND s.current=1`},
					{"lifecycle", `SELECT json_object('state',lifecycle_state,'desired',desired_state,'error_code',last_error_code,'error_message',last_error_message) FROM managed_agents WHERE agent_id='g5-snooze-due-root'`},
					{"policy", `SELECT json_object('desired',desired_revision,'applied',applied_revision,'enabled',goal_enabled,'projection',projection_state) FROM managed_agent_policies WHERE agent_id='g5-snooze-due-root'`},
					{"entries/SDK stamps", `SELECT json_group_array(json_object('seq',seq,'kind',kind,'digest',digest,'parent',parent_digest,'body',CAST(body AS TEXT))) FROM (SELECT * FROM embedded_entries WHERE agent_id='g5-snooze-due-root' ORDER BY seq)`},
					{"effects", `SELECT json_group_array(json_object('id',effect_id,'state',state,'intent',CAST(intent AS TEXT),'result',CAST(result AS TEXT))) FROM embedded_effects WHERE agent_id='g5-snooze-due-root'`},
					{"goal DOMAIN", `SELECT json_group_array(json_object('call',call_id,'state',state,'args',args_json,'result',result_json)) FROM embedded_goal_calls WHERE agent_id='g5-snooze-due-root'`},
					{"snooze DOMAIN", `SELECT json_group_array(json_object('call',call_id,'user_seq',user_seq,'user_digest',user_digest,'revision',goal_revision,'args',args_json,'result',result_json)) FROM embedded_goal_snooze_calls WHERE agent_id='g5-snooze-due-root'`},
				} {
					var state string
					readErr := probe.QueryRowContext(ctx, query.sql).Scan(&state)
					t.Logf("unexpected SDK prompt stage=%s snapshot=%s state=%s read_error=%v", stage, query.label, state, readErr)
				}
				_ = probe.Close()
			} else {
				t.Logf("unexpected SDK prompt diagnostic_open_error=%v", probeErr)
			}
			panic(fmt.Sprintf("snooze fixture received unexpected provider prompt: stage=%s expected=%q transcript=%s transcript_error=%v", stage, text, transcript, transcriptErr))
		}
	}
	var starts atomic.Int32
	const agentID = "g5-snooze-due-root"
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			starts.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal", map[string]any{"action": "set", "goal": "finish due goal"}, "due-set")}, ai.StopToolUse)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			starts.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal_snooze", map[string]any{"duration_ms": 1000}, "due-snooze")}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			starts.Add(1)
			var result *ai.ToolResultMessage
			for _, msg := range h.Messages {
				if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == "due-snooze" {
					copy := r
					result = &copy
				}
			}
			checked <- result
			finalEntered <- struct{}{}
			<-release
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "initial due final"}}, ai.StopStop)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			assertPrompt(h, "Continue working from your saved context.", "first-cold-neutral")
			starts.Add(1)
			neutral <- struct{}{}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "neutral awareness final"}}, ai.StopStop)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			assertPrompt(h, "Continue working on the current goal.", "original-key-due-continuation")
			starts.Add(1)
			continuation <- struct{}{}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "one due continuation"}}, ai.StopStop)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			assertPrompt(h, "Continue working from your saved context.", "cold-UNKNOWN-neutral")
			starts.Add(1)
			coldNeutral <- struct{}{}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "cold UNKNOWN neutral awareness final"}}, ai.StopStop)
		},
	})
	var evaluations atomic.Int32
	newManager := func(opened *store.Store) *Manager {
		m, e := NewManager(ManagerOptions{Store: opened, Manifest: manifest, Clock: clock,
			Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
			CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
			EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
			CodexCredentialStore:  testfixture.CodexAdmission(t),
			EmbeddedGoalEvaluator: func(_ context.Context, _ store.EmbeddedGoalEvaluationKey) (string, string, error) {
				evaluations.Add(1)
				return store.EmbeddedGoalEvaluationNotDone, "more work remains", nil
			},
		})
		require.NoError(t, e)
		return m
	}
	m := newManager(db)
	setEmbeddedRecoveryGeneration(t, ctx, db, m)
	// Close can wait on the blocked provider even after require.FailNow.
	defer func() { unblock(); _ = m.Close(ctx); _ = db.Close() }()
	launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: agentID, Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "set and snooze goal", RequestID: "a5555555-5555-4555-8555-555555555593"})
	require.NoError(t, err)
	b := store.EmbeddedBinding{AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
	ro, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer ro.Close()
	var result *ai.ToolResultMessage
	select {
	case result = <-checked:
	case <-time.After(4 * time.Second):
		t.Fatal("setup failure: SDK did not reach checked snooze result")
	}
	require.NotNil(t, result, "setup failure: missing checked SDK snooze result")
	require.Equal(t, "due-snooze", result.ToolCallID)
	require.Equal(t, "goal_snooze", result.ToolName)
	require.False(t, result.IsError)
	select {
	case <-finalEntered:
	case <-time.After(4 * time.Second):
		t.Fatal("setup failure: final provider barrier not reached")
	}
	var userSeq, revision int64
	var userDigest, preferenceDue string
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT user_seq,user_digest,goal_revision,due_at FROM embedded_goal_snooze_preferences WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND call_id='due-snooze' AND active=1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&userSeq, &userDigest, &revision, &preferenceDue), "setup failure: 070 preference absent")
	require.EqualValues(t, 2, userSeq)
	require.EqualValues(t, 1, revision)
	require.NotEmpty(t, userDigest)
	require.Equal(t, clock.Now().Add(time.Second).Format("2006-01-02T15:04:05.000Z"), preferenceDue)
	// Move the Store clock before the verdict. The due assertion must use the
	// commit clock, not a stale preference timestamp or wall-clock timer.
	require.NoError(t, clock.Advance(250*time.Millisecond))
	commitNow := clock.Now()
	unblock()
	var finalSeq int64
	var finalDigest string
	require.Eventually(t, func() bool {
		var body []byte
		return ro.QueryRowContext(ctx, `SELECT seq,digest,body FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND kind='assistant' ORDER BY seq DESC LIMIT 1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&finalSeq, &finalDigest, &body) == nil && strings.Contains(string(body), "initial due final")
	}, 3*time.Second, 10*time.Millisecond, "setup failure: checked final absent")
	require.NotEmpty(t, finalDigest)
	var receiptDigest string
	var enabled int
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT turn_entry_digest,goal_enabled FROM embedded_goal_applied_receipts WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND turn_entry_seq=? AND goal_revision=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, userSeq, revision).Scan(&receiptDigest, &enabled), "setup failure: applied receipt absent")
	require.Equal(t, userDigest, receiptDigest)
	require.Equal(t, 1, enabled)
	require.Eventually(t, func() bool {
		var state string
		return ro.QueryRowContext(ctx, `SELECT state FROM embedded_goal_evaluations WHERE agent_id=? AND session_id=? AND epoch=? AND user_seq=? AND user_digest=? AND goal_revision=? AND final_seq=? AND final_digest=?`, b.AgentID, b.SessionID, b.Epoch, userSeq, userDigest, revision, finalSeq, finalDigest).Scan(&state) == nil && state == store.EmbeddedGoalEvaluationNotDone
	}, 3*time.Second, 10*time.Millisecond, "setup failure: durable NOT_DONE verdict absent")
	require.EqualValues(t, 1, evaluations.Load())
	// First genuine RED: exactly the existing 069 unique key, with due_at
	// committed alongside NOT_DONE. No second reservation, reminder, or mail.
	var count int
	var due, state, disposition string
	err = ro.QueryRowContext(ctx, `SELECT count(*),max(due_at),max(state),max(disposition) FROM embedded_goal_continuations WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND user_seq=? AND user_digest=? AND goal_revision=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, userSeq, userDigest, revision).Scan(&count, &due, &state, &disposition)
	require.NoError(t, err, "G5/T6 RED: keyed 069 reservation lacks atomic due_at")
	require.Equal(t, 1, count, "snooze must not create a second reservation")
	require.Equal(t, commitNow.Add(time.Second).Format("2006-01-02T15:04:05.000Z"), due, "due must derive from injected Store clock at verdict commit")
	require.Equal(t, "reserved", state)
	require.Equal(t, "pending", disposition)
	require.EqualValues(t, 3, starts.Load(), "provider must not start early")
	// The durable reservation precedes async timer registration. Wait under
	// the Manager lock before advancing the fake clock past either boundary.
	timerRegistered := func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		timer := m.embeddedGoalTimers[b.AgentID]
		return timer != nil && timer.cancel != nil
	}
	require.Eventually(t, timerRegistered, 3*time.Second, 10*time.Millisecond, "setup failure: due timer not registered before due-1ms")
	// Stop the original Store and Manager with the saved future reservation.
	require.NoError(t, m.Close(ctx))
	require.NoError(t, db.Close())
	// The prior owner lease cannot be reclaimed before its 30s TTL. Expire
	// only that private fixture lease, without moving the 1s due clock.
	lease, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = lease.ExecContext(ctx, `UPDATE embedded_sessions SET owner_expires_at='2000-01-01T00:00:00.000Z' WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation)
	require.NoError(t, err, "setup failure: old owner lease not expired")
	require.NoError(t, lease.Close())
	db, err = store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	m = newManager(db)
	setEmbeddedRecoveryGeneration(t, ctx, db, m)
	require.NoError(t, m.RecoverEmbeddedRoots(ctx))
	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		owner, ok := m.embeddedOwners[b.AgentID]
		return ok && owner.binding.OwnerToken > 0
	}, 3*time.Second, 10*time.Millisecond, "setup failure: new Manager did not claim saved root")
	select {
	case <-neutral:
	case <-time.After(3 * time.Second):
		t.Fatal("setup failure: exact neutral recovery response absent")
	}
	// This is a checked, fenced awareness descendant, not a replacement key.
	var neutralFinalSeq int64
	var neutralFinalDigest string
	require.Eventually(t, func() bool {
		return ro.QueryRowContext(ctx, `SELECT f.seq,f.digest FROM embedded_restart_wake_slots w JOIN embedded_sessions s ON s.agent_id=w.agent_id AND s.session_id=w.session_id AND s.epoch=w.epoch AND s.generation=w.runtime_generation JOIN embedded_restart_wake_checked_origins o ON o.agent_id=w.agent_id AND o.slot=w.slot AND o.wake_id=w.wake_id AND o.process_generation=w.process_generation AND o.claim_owner_token=w.claim_owner_token JOIN embedded_entries f ON f.agent_id=o.agent_id AND f.session_id=o.session_id AND f.epoch=o.epoch AND f.generation=o.runtime_generation AND f.seq=o.seq JOIN embedded_entries u ON u.agent_id=f.agent_id AND u.session_id=f.session_id AND u.epoch=f.epoch AND u.generation=f.generation AND u.seq=f.seq-1 JOIN embedded_nonmail_provider_entries p ON p.agent_id=u.agent_id AND p.session_id=u.session_id AND p.epoch=u.epoch AND p.generation=u.generation AND p.user_seq=u.seq AND p.user_digest=u.digest WHERE w.agent_id=? AND w.session_id=? AND w.epoch=? AND w.runtime_generation=? AND s.current=1 AND w.slot='current' AND w.kind='root-idle' AND w.state='checked-complete' AND w.notice_id IS NULL AND w.process_generation=(SELECT generation FROM embedded_process_generation_counter WHERE singleton=1) AND w.claim_owner_token=s.owner_token AND w.digest=? AND f.seq=? AND f.kind='assistant' AND json_extract(f.body,'$.stopReason')='stop' AND json_extract(f.body,'$.content[0].text')='neutral awareness final' AND u.kind='user' AND json_extract(u.body,'$.content[0].text')='Continue working from your saved context.'`, b.AgentID, b.SessionID, b.Epoch, b.Generation, finalDigest, finalSeq+2).Scan(&neutralFinalSeq, &neutralFinalDigest) == nil
	}, 3*time.Second, 10*time.Millisecond, "setup failure: neutral wake lacks exact checked owner/process/predecessor fence")
	require.NotEqual(t, finalDigest, neutralFinalDigest)
	require.NoError(t, clock.Advance(999*time.Millisecond))
	snapshotAt := func(stage string) {
		t.Logf("snapshot stage=%s neutral_final_seq=%d neutral_final_digest=%s", stage, neutralFinalSeq, neutralFinalDigest)
		t.Logf("saved evaluation key agent=%s session=%s epoch=%d generation=%s user_seq=%d user_digest=%s revision=%d final_seq=%d final_digest=%s clock=%s", b.AgentID, b.SessionID, b.Epoch, b.Generation, userSeq, userDigest, revision, finalSeq, finalDigest, clock.Now().Format("2006-01-02T15:04:05.000Z"))
		m.mu.Lock()
		owner := m.embeddedOwners[b.AgentID]
		timer := m.embeddedGoalTimers[b.AgentID]
		m.mu.Unlock()
		t.Logf("snapshot owner_binding=%+v timer_present=%t provider_starts=%d evaluations=%d", owner.binding, timer != nil, starts.Load(), evaluations.Load())
		var snapshot string
		err := ro.QueryRowContext(ctx, `SELECT json_object('state',state,'generation',generation,'user_seq',user_seq,'user_digest',user_digest,'goal_revision',goal_revision,'final_seq',final_seq,'final_digest',final_digest) FROM embedded_goal_evaluations WHERE agent_id=? AND session_id=? AND epoch=? AND user_seq=? AND user_digest=? AND goal_revision=?`, b.AgentID, b.SessionID, b.Epoch, userSeq, userDigest, revision).Scan(&snapshot)
		t.Logf("snapshot durable_evaluation=%s read_error=%v", snapshot, err)
		snapshot = ""
		err = ro.QueryRowContext(ctx, `SELECT json_object('state',state,'disposition',disposition,'due_at',due_at,'started_at',started_at) FROM embedded_goal_continuations WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND user_seq=? AND user_digest=? AND goal_revision=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, userSeq, userDigest, revision).Scan(&snapshot)
		t.Logf("snapshot continuation=%s read_error=%v", snapshot, err)
		snapshot = ""
		err = ro.QueryRowContext(ctx, `SELECT json_group_array(json_object('slot',w.slot,'wake_id',w.wake_id,'kind',w.kind,'state',w.state,'process_generation',w.process_generation,'claim_owner_token',w.claim_owner_token,'checked_final_seq',e.seq,'checked_final_digest',e.digest,'checked_final_kind',e.kind)) FROM embedded_restart_wake_slots w LEFT JOIN embedded_restart_wake_checked_origins o ON o.agent_id=w.agent_id AND o.slot=w.slot AND o.wake_id=w.wake_id AND o.process_generation=w.process_generation LEFT JOIN embedded_entries e ON e.agent_id=o.agent_id AND e.session_id=o.session_id AND e.epoch=o.epoch AND e.generation=o.runtime_generation AND e.seq=o.seq WHERE w.agent_id=? AND w.session_id=? AND w.epoch=? AND w.runtime_generation=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&snapshot)
		t.Logf("snapshot wake/checked_final=%s (null origin fields are unmeasured) read_error=%v", snapshot, err)
		snapshot = ""
		err = ro.QueryRowContext(ctx, `SELECT json_object('seq',seq,'digest',digest,'kind',kind) FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? ORDER BY seq DESC LIMIT 1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&snapshot)
		t.Logf("snapshot head=%s read_error=%v", snapshot, err)
		snapshot = ""
		err = ro.QueryRowContext(ctx, `SELECT json_object('owner_id',owner_id,'owner_token',owner_token,'owner_expires_at',owner_expires_at,'process_generation_counter',(SELECT generation FROM embedded_process_generation_counter WHERE singleton=1)) FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&snapshot)
		t.Logf("snapshot persisted_owner/process=%s read_error=%v", snapshot, err)
		snapshot = ""
		err = ro.QueryRowContext(ctx, `SELECT json_object('lifecycle',lifecycle_state,'error_code',last_error_code,'error_message',last_error_message,'error_at',last_error_at) FROM managed_agents WHERE agent_id=?`, b.AgentID).Scan(&snapshot)
		t.Logf("snapshot persisted_lifecycle/error=%s read_error=%v; continuation guard rejection=unmeasured (no dedicated persisted rejection field)", snapshot, err)
	}
	snapshotAt("preDue")
	var preState, preDisposition string
	var preStarted sql.NullString
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT state,disposition,started_at FROM embedded_goal_continuations WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND user_seq=? AND user_digest=? AND goal_revision=? AND EXISTS (SELECT 1 FROM embedded_goal_evaluations e WHERE e.agent_id=embedded_goal_continuations.agent_id AND e.session_id=embedded_goal_continuations.session_id AND e.epoch=embedded_goal_continuations.epoch AND e.generation=embedded_goal_continuations.generation AND e.user_seq=embedded_goal_continuations.user_seq AND e.user_digest=embedded_goal_continuations.user_digest AND e.goal_revision=embedded_goal_continuations.goal_revision AND e.final_seq=? AND e.final_digest=? AND e.state='NOT_DONE')`, b.AgentID, b.SessionID, b.Epoch, b.Generation, userSeq, userDigest, revision, finalSeq, finalDigest).Scan(&preState, &preDisposition, &preStarted))
	require.Equal(t, "reserved", preState)
	require.Equal(t, "pending", preDisposition)
	require.False(t, preStarted.Valid, "original-key continuation started before due")
	select {
	case <-continuation:
		t.Fatal("goal continuation provider started before due (neutral is separate)")
	default:
	}
	require.EqualValues(t, 4, starts.Load())
	require.Eventually(t, timerRegistered, 300*time.Millisecond, 10*time.Millisecond, "G5/T6 RED: cold reserved-only due scan did not register timer")
	require.NoError(t, clock.Advance(time.Millisecond))
	select {
	case <-continuation:
	case <-time.After(3 * time.Second):
		// Take the lane after dispatch has had its bounded opportunity. A missing
		// provider is not itself proof of a Store rejection or a persisted error.
		lane := m.laneFor(b.AgentID)
		lane.serial.Lock()
		snapshotAt("AT-DUE missing continuation")
		// Read the exact original key under the same lane as dispatch. Do not
		// call StartEmbeddedGoalContinuation: it would consume the reservation.
		// The semantic-head guard is now inside Store's immediate start CAS.
		// Read-only diagnostics must not consume the original reservation.
		m.mu.Lock()
		owner, owned := m.embeddedOwners[b.AgentID]
		closed := m.closed
		m.mu.Unlock()
		key := store.EmbeddedGoalEvaluationKey{UserSeq: userSeq, UserDigest: userDigest, GoalRevision: revision, FinalSeq: finalSeq, FinalDigest: finalDigest}
		saved, savedErr := db.ManagedAgent(ctx, b.AgentID)
		selectionMatches := savedErr == nil && saved != nil && owned && !closed &&
			saved.Runtime == "embedded" && saved.DesiredState == "running" && saved.LifecycleState == "running" &&
			owner.binding.AgentID == b.AgentID && owner.binding.SessionID == b.SessionID && owner.binding.Epoch == b.Epoch && owner.binding.Generation == b.Generation &&
			saved.PiSessionID == b.SessionID && saved.SessionEpoch == b.Epoch && saved.RuntimeGeneration == b.Generation
		t.Logf("guard diagnostic original_key=%+v exact_current_owner_binding=%+v selection_matches=%t saved_error=%v run_context_error=%v", key, owner.binding, selectionMatches, savedErr, m.runCtx.Err())
		snap, reopenErr := db.ReopenEmbedded(ctx, owner.binding)
		t.Logf("guard diagnostic real_read_only_ReopenEmbedded_error=%v entries=%d", reopenErr, len(snap.Entries))
		if reopenErr == nil && len(snap.Entries) > 0 {
			last := snap.Entries[len(snap.Entries)-1]
			physicalHeadDiffers := last.Sequence != key.FinalSeq || last.Digest != key.FinalDigest || last.Kind != "assistant"
			t.Logf("guard diagnostic physical_head_differs=%t head_seq=%d head_digest=%s head_kind=%s original_final_seq=%d original_final_digest=%s Store_atomic_authority=not_called_by_diagnostic", physicalHeadDiffers, last.Sequence, last.Digest, last.Kind, key.FinalSeq, key.FinalDigest)
		}
		reservation, reservationErr := db.EmbeddedGoalContinuationFor(ctx, owner.binding, key)
		t.Logf("guard diagnostic exact_reservation=%+v read_error=%v Store_start_CAS=not_called_by_diagnostic", reservation, reservationErr)
		lane.serial.Unlock()
		t.Fatal("G5/T6 AT-DUE RED: original-key due continuation provider claim missing; see authoritative snapshot and read-only guard predicates (production guard returns no code)")
	}
	snapshotAt("AT-DUE provider entered")
	require.EqualValues(t, 5, starts.Load())
	require.EqualValues(t, 1, evaluations.Load(), "continuation must not chain evaluation")
	var startedState, startedDisposition string
	var startedAt sql.NullString
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT state,disposition,started_at FROM embedded_goal_continuations WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND user_seq=? AND user_digest=? AND goal_revision=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, userSeq, userDigest, revision).Scan(&startedState, &startedDisposition, &startedAt))
	require.Equal(t, "started", startedState)
	require.Equal(t, "unknown", startedDisposition)
	require.True(t, startedAt.Valid)
	require.Equal(t, due, startedAt.String, "start authority must use the original due boundary")
	require.NoError(t, m.Close(ctx))
	require.NoError(t, db.Close())
	// A started UNKNOWN is evidence, never a replay token.
	require.NoError(t, clock.Advance(embeddedOwnerTTL+time.Second))
	db, err = store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	m = newManager(db)
	setEmbeddedRecoveryGeneration(t, ctx, db, m)
	require.NoError(t, m.RecoverEmbeddedRoots(ctx))
	require.Eventually(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); _, ok := m.embeddedOwners[b.AgentID]; return ok }, 3*time.Second, 10*time.Millisecond)
	select {
	case <-coldNeutral:
	case <-time.After(3 * time.Second):
		t.Fatal("cold UNKNOWN reopen did not enter the exact saved-context awareness callback")
	}
	// The extra callback is authorized only as the current process's checked
	// idle wake. It is not another goal continuation or an arbitrary provider.
	require.Eventually(t, func() bool {
		var count int
		return ro.QueryRowContext(ctx, `SELECT count(*) FROM embedded_restart_wake_slots w JOIN embedded_sessions s ON s.agent_id=w.agent_id AND s.session_id=w.session_id AND s.epoch=w.epoch AND s.generation=w.runtime_generation AND s.owner_token=w.claim_owner_token AND s.current=1 JOIN embedded_process_generation_counter g ON g.singleton=1 AND g.generation=w.process_generation JOIN embedded_restart_wake_checked_origins o ON o.agent_id=w.agent_id AND o.slot=w.slot AND o.session_id=w.session_id AND o.epoch=w.epoch AND o.runtime_generation=w.runtime_generation AND o.wake_id=w.wake_id AND o.process_generation=w.process_generation AND o.claim_owner_token=w.claim_owner_token JOIN embedded_entries f ON f.agent_id=o.agent_id AND f.session_id=o.session_id AND f.epoch=o.epoch AND f.generation=o.runtime_generation AND f.seq=o.seq JOIN embedded_entries u ON u.agent_id=f.agent_id AND u.session_id=f.session_id AND u.epoch=f.epoch AND u.seq=f.seq-1 AND f.parent_digest=u.digest JOIN embedded_nonmail_provider_entries p ON p.agent_id=u.agent_id AND p.session_id=u.session_id AND p.epoch=u.epoch AND p.generation=u.generation AND p.user_seq=u.seq AND p.user_digest=u.digest WHERE w.agent_id=? AND w.slot='current' AND w.kind='root-idle' AND w.notice_id IS NULL AND w.state='checked-complete' AND u.kind='user' AND f.kind='assistant' AND json_extract(u.body,'$.content[0].text')='Continue working from your saved context.' AND json_extract(f.body,'$.content[0].text')='cold UNKNOWN neutral awareness final' AND json_extract(f.body,'$.stopReason')='stop'`, b.AgentID).Scan(&count) == nil && count == 1
	}, 3*time.Second, 10*time.Millisecond, "cold callback lacks exact current checked idle-wake authority")
	require.EqualValues(t, 6, starts.Load(), "only one separately authorized cold awareness may follow started UNKNOWN")
	require.EqualValues(t, 1, evaluations.Load(), "cold awareness cannot reevaluate the original key")
	var evaluationCount, originalKeyCount int
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT count(*),sum(CASE WHEN user_seq=? AND user_digest=? AND goal_revision=? AND final_seq=? AND final_digest=? AND state='NOT_DONE' AND completed_at IS NOT NULL THEN 1 ELSE 0 END) FROM embedded_goal_evaluations WHERE agent_id=? AND session_id=? AND epoch=?`, userSeq, userDigest, revision, finalSeq, finalDigest, b.AgentID, b.SessionID, b.Epoch).Scan(&evaluationCount, &originalKeyCount))
	require.Equal(t, 1, evaluationCount)
	require.Equal(t, 1, originalKeyCount, "original evaluation identity cannot be replaced by either awareness head")
	var coldState, coldDisposition, coldStarted string
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT state,disposition,started_at FROM embedded_goal_continuations WHERE agent_id=? AND session_id=? AND epoch=? AND user_seq=? AND user_digest=? AND goal_revision=?`, b.AgentID, b.SessionID, b.Epoch, userSeq, userDigest, revision).Scan(&coldState, &coldDisposition, &coldStarted))
	require.Equal(t, "started", coldState)
	require.Equal(t, "unknown", coldDisposition)
	require.Equal(t, due, coldStarted, "UNKNOWN must retain the first original-key start")
	select {
	case <-continuation:
		t.Fatal("started UNKNOWN replayed the goal continuation")
	default:
	}
}
