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
	"sync/atomic"
	"syscall"
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

// Only a real Bash process receives the private post-Start pipe. The fixed
// command cannot run until the test has verified its process and canceled its
// own SDK turn. No executable or caller-controlled shell source is substituted.
func TestEmbeddedG5BashTimeoutUnknownRED(t *testing.T) {
	ctx, cancelAll := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancelAll()
	base := t.TempDir()
	project := filepath.Join(base, "project")
	require.NoError(t, os.Mkdir(project, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(project, "one.txt"), []byte("literal\n"), 0600))
	path := filepath.Join(base, "router.db")
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	const callID = "g5-bash-timeout-original"
	const command = "grep -F -n -- literal one.txt"
	args := map[string]any{"command": command, "timeout": float64(1)}
	var callbacks, bashStarts, readyCallbacks, originalCallbacks, noticeCallbacks, retryCallbacks atomic.Int32
	var noticeID, noticeDigest string
	var noticeOwner, noticeProcess int64
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			callbacks.Add(1)
			readyCallbacks.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "ready"}}, ai.StopStop)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			callbacks.Add(1)
			originalCallbacks.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("bash", args, callID)}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			callbacks.Add(1)
			snapshotG5Bash(t, path, "g5-bash-timeout-root", callID, callbacks.Load(), bashStarts.Load(), &h)
			ro, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
			require.NoError(t, err)
			defer ro.Close()
			var body []byte
			var text string
			require.NoError(t, ro.QueryRowContext(ctx, `SELECT n.notice_id,e.digest,e.body,n.notice_text,n.start_owner_token,w.process_generation FROM embedded_interrupted_notices n JOIN embedded_entries e ON e.agent_id=n.agent_id AND e.session_id=n.session_id AND e.epoch=n.epoch AND e.generation=n.generation AND e.seq=n.entry_seq JOIN embedded_sessions s ON s.agent_id=n.agent_id AND s.session_id=n.session_id AND s.epoch=n.epoch AND s.generation=n.generation AND s.current=1 AND s.owner_token=n.start_owner_token JOIN embedded_restart_wake_slots w ON w.agent_id=n.agent_id AND w.session_id=n.session_id AND w.epoch=n.epoch AND w.runtime_generation=n.generation AND w.wake_id=n.notice_id AND w.claim_owner_token=s.owner_token WHERE n.agent_id='g5-bash-timeout-root' AND n.source_seq=4 AND n.entry_seq=6 AND n.state='started' AND e.kind='user' AND w.slot='current' AND w.kind='interrupted' AND w.digest=e.digest AND w.state='claimed'`).Scan(&noticeID, &noticeDigest, &body, &text, &noticeOwner, &noticeProcess))
			require.Positive(t, noticeOwner)
			require.Positive(t, noticeProcess)
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
			require.Equal(t, body, lastUser, "only interrupted notice seq6 may use this completion callback")
			require.EqualValues(t, 1, noticeCallbacks.Add(1), "notice must enter provider only once")
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "checked"}}, ai.StopStop)
		},
	})
	makeManager := func(db *store.Store) *Manager {
		t.Helper()
		m, e := NewManager(ManagerOptions{Store: db, Manifest: manifest,
			Config:                Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{project}},
			CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
			EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
			CodexCredentialStore:  testfixture.CodexAdmission(t),
		})
		require.NoError(t, e)
		return m
	}
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	m := makeManager(db)
	t.Cleanup(func() { _ = m.Close(context.Background()); _ = db.Close() })
	t.Cleanup(func() {
		if t.Failed() {
			snapshotG5Bash(t, path, "g5-bash-timeout-root", callID, callbacks.Load(), bashStarts.Load(), nil)
		}
	})
	_, err = m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "g5-bash-timeout-root", Cwd: project, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "ready", RequestID: "a5555555-5555-4555-8555-5555555555c1"})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return callbacks.Load() == 1 }, 4*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		_, ok := m.embeddedOwners["g5-bash-timeout-root"]
		return ok
	}, 4*time.Second, 10*time.Millisecond)
	m.mu.Lock()
	owner := m.embeddedOwners["g5-bash-timeout-root"]
	m.mu.Unlock()
	saved, err := db.ManagedAgent(ctx, owner.binding.AgentID)
	require.NoError(t, err)
	spawned := make(chan int, 1)
	m.embeddedBashAfterStart = func(pid int, _ *os.File) { bashStarts.Add(1); spawned <- pid }
	turnCtx, cancelTurn := context.WithCancel(context.WithValue(ctx, embeddedOwnerContextKey{}, owner))
	defer cancelTurn()
	finished := make(chan error, 1)
	go func() { finished <- m.runEmbeddedSessionPrompt(turnCtx, *saved, "run the literal check") }()
	var pid int
	select {
	case pid = <-spawned:
	case <-ctx.Done():
		t.Fatal("real Bash did not start")
	}
	// Start returned a real process, not an exec stub. PGID is the leader PID.
	exe, err := os.Readlink(filepath.Join("/proc", itoaG5(pid), "exe"))
	require.NoError(t, err)
	require.Equal(t, "/usr/bin/bash", exe)
	pgid, err := syscall.Getpgid(pid)
	require.NoError(t, err)
	require.Equal(t, pid, pgid)
	// Keep the SDK turn live through the bounded Bash timeout. The gate
	// stays shut, so the verification command cannot execute.
	select {
	case <-time.After(1500 * time.Millisecond):
	case <-ctx.Done():
		t.Fatal("timed out before Bash deadline")
	}
	cancelTurn()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled SDK turn did not finish")
	}
	// The process group is killed and Wait bounded even though the private pipe
	// remains unwritten. A live leader is a failed cleanup, not a proven absence.
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) == syscall.ESRCH }, 2*time.Second, 10*time.Millisecond, "Bash leader absence UNPROVED")
	require.Eventually(t, func() bool { return syscall.Kill(-pid, 0) == syscall.ESRCH }, 2*time.Second, 10*time.Millisecond, "process-group descendant absence UNPROVED")
	ro, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer ro.Close()
	var state string
	var intent []byte
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT state,intent FROM embedded_effects WHERE effect_id=?`, callID).Scan(&state, &intent))
	var original ai.ToolCall
	require.NoError(t, json.Unmarshal(intent, &original))
	require.Equal(t, callID, original.ID)
	require.Equal(t, "bash", original.Name)
	require.Equal(t, args, original.Arguments)
	require.Equal(t, "unknown", state, "G5 first RED: post-spawn canceled intent must remain UNKNOWN; checked result and cold gates UNEXECUTED")
	var results int
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id=? AND kind='toolResult'`, owner.binding.AgentID).Scan(&results))
	require.Zero(t, results, "G5 first RED: no checked SDK success or checked IsError may mask uncertain Bash; cold gates UNEXECUTED")
	require.EqualValues(t, 1, readyCallbacks.Load())
	require.EqualValues(t, 1, originalCallbacks.Load(), "original Bash provider entry must not repeat")
	require.Zero(t, noticeCallbacks.Load(), "notice must not run before cold recovery")
	require.Equal(t, readyCallbacks.Load()+originalCallbacks.Load(), callbacks.Load())
	checkNoticeCompletion := func() {
	require.EqualValues(t, 1, noticeCallbacks.Load(), "one exact interrupted notice is required")
	require.Equal(t, readyCallbacks.Load()+originalCallbacks.Load()+noticeCallbacks.Load(), callbacks.Load())
	for _, seq := range []int64{2, 4} {
		var entries int
		require.NoError(t, ro.QueryRowContext(ctx, `SELECT count(*) FROM embedded_nonmail_provider_entries p JOIN embedded_entries e ON e.agent_id=p.agent_id AND e.session_id=p.session_id AND e.epoch=p.epoch AND e.generation=p.generation AND e.seq=p.user_seq AND e.digest=p.user_digest WHERE p.agent_id=? AND p.session_id=? AND p.epoch=? AND p.generation=? AND p.user_seq=? AND e.kind='user'`, owner.binding.AgentID, owner.binding.SessionID, owner.binding.Epoch, owner.binding.Generation, seq).Scan(&entries))
		require.Equal(t, 1, entries, "each original provider entry must be unique: seq=%d", seq)
	}
	var entries int
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT count(*) FROM embedded_nonmail_provider_entries WHERE agent_id=?`, owner.binding.AgentID).Scan(&entries))
	require.EqualValues(t, readyCallbacks.Load()+originalCallbacks.Load(), entries, "interrupted notice is wake-attributed, not a nonmail provider entry")
	var currentToken int64
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT owner_token FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, owner.binding.AgentID, owner.binding.SessionID, owner.binding.Epoch, owner.binding.Generation).Scan(&currentToken))
	require.Equal(t, currentToken, noticeOwner)
	require.Greater(t, noticeOwner, owner.binding.OwnerToken)
	var checkedBody []byte
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT e.body FROM embedded_restart_wake_slots w JOIN embedded_restart_wake_checked_origins o ON o.agent_id=w.agent_id AND o.slot=w.slot AND o.session_id=w.session_id AND o.epoch=w.epoch AND o.runtime_generation=w.runtime_generation AND o.wake_id=w.wake_id AND o.process_generation=w.process_generation AND o.claim_owner_token=w.claim_owner_token JOIN embedded_entries e ON e.agent_id=o.agent_id AND e.session_id=o.session_id AND e.epoch=o.epoch AND e.generation=o.runtime_generation AND e.seq=o.seq WHERE w.agent_id=? AND w.session_id=? AND w.epoch=? AND w.runtime_generation=? AND w.wake_id=? AND w.process_generation=? AND w.claim_owner_token=? AND w.state='checked-complete' AND e.kind='assistant' AND e.seq=7 AND e.parent_digest=?`, owner.binding.AgentID, owner.binding.SessionID, owner.binding.Epoch, owner.binding.Generation, noticeID, noticeProcess, noticeOwner, noticeDigest).Scan(&checkedBody))
	checkedMessage, err := ai.UnmarshalMessage(checkedBody)
	require.NoError(t, err)
	checkedFinal, ok := checkedMessage.(ai.AssistantMessage)
	require.True(t, ok)
	require.Equal(t, ai.StopStop, checkedFinal.StopReason)
	require.EqualValues(t, 1, bashStarts.Load(), "notice completion must not start Bash again")
	}

	// Cold reopen may complete its exact notice, never replay the original Bash.
	// The same ID must not spawn. Execution measurement remains a diagnostic gate.
	require.NoError(t, m.Close(ctx))
	require.NoError(t, db.Close())
	db, err = store.Open(ctx, path, store.Options{Clock: deterministic.NewFixedClock(time.Now().Add(embeddedOwnerTTL + time.Minute))})
	require.NoError(t, err)
	m = makeManager(db)
	m.embeddedBashAfterStart = func(int, *os.File) { bashStarts.Add(1); t.Error("recovery or same call ID respawned") }
	setEmbeddedRecoveryGeneration(t, ctx, db, m)
	require.NoError(t, m.RecoverEmbeddedRoots(ctx))
	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		_, ok := m.embeddedOwners[owner.binding.AgentID]
		return ok
	}, 4*time.Second, 10*time.Millisecond)
	require.EqualValues(t, 1, readyCallbacks.Load())
	require.EqualValues(t, 1, originalCallbacks.Load(), "cold reopen replayed original Bash")
	checkNoticeCompletion()
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT state FROM embedded_effects WHERE effect_id=?`, callID).Scan(&state))
	require.Equal(t, "unknown", state, "notice completion must preserve original uncertainty")
	reg.SetResponses([]providers.FauxResponseStep{func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		callbacks.Add(1)
		require.EqualValues(t, 1, retryCallbacks.Add(1), "explicit same-ID retry provider entry must be unique")
		return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("bash", args, callID)}, ai.StopToolUse)
	}})
	m.mu.Lock()
	coldOwner := m.embeddedOwners[owner.binding.AgentID]
	m.mu.Unlock()
	coldSaved, err := db.ManagedAgent(ctx, owner.binding.AgentID)
	require.NoError(t, err)
	_ = m.runEmbeddedSessionPrompt(context.WithValue(ctx, embeddedOwnerContextKey{}, coldOwner), *coldSaved, "explicit retry")
	effect, err := db.RecoverEmbeddedEffect(ctx, coldOwner.binding, callID)
	require.NoError(t, err)
	require.Equal(t, "unknown", effect.State)
	require.EqualValues(t, 1, retryCallbacks.Load())
	require.EqualValues(t, 1, originalCallbacks.Load())
	require.EqualValues(t, 1, noticeCallbacks.Load())
	require.Equal(t, readyCallbacks.Load()+originalCallbacks.Load()+noticeCallbacks.Load()+retryCallbacks.Load(), callbacks.Load())
	require.EqualValues(t, 1, bashStarts.Load(), "recovery and same-ID retry must not respawn Bash")
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id=? AND kind='toolResult'`, owner.binding.AgentID).Scan(&results))
	require.Zero(t, results, "notice and retry must not fabricate an original Bash result")
}

func itoaG5(pid int) string { return fmt.Sprint(pid) }

// Read only this test's private Store. Bodies stay private: equality and hashes
// identify the exact user at provider entry without dumping prompts or results.
func snapshotG5Bash(t *testing.T, path, agentID, callID string, callbacks, bashStarts int32, h *ai.TranscriptContext) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ro, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Logf("G5 snapshot open_error=%T", err)
		return
	}
	defer ro.Close()
	t.Logf("G5 snapshot agent=%s call_id=%s provider_callbacks=%d bash_start_count=%d command_exec_count=unmeasured provider_boundary=%t", agentID, callID, callbacks, bashStarts, h != nil)
	var providerUser []byte
	if h != nil {
		for _, msg := range h.Messages {
			if user, ok := msg.(ai.UserMessage); ok {
				providerUser, err = json.Marshal(user)
				if err != nil {
					t.Logf("G5 provider_user_marshal_error=%T", err)
				}
			}
		}
		t.Logf("G5 provider_last_user_present=%t body_sha256=%x", providerUser != nil, sha256.Sum256(providerUser))
	}
	neutral, _ := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "Continue working from your saved context."}}})
	rows, err := ro.QueryContext(ctx, `SELECT e.session_id,e.epoch,e.generation,e.seq,e.digest,e.body,n.notice_text FROM embedded_entries e LEFT JOIN embedded_interrupted_notices n ON n.agent_id=e.agent_id AND n.session_id=e.session_id AND n.epoch=e.epoch AND n.entry_seq=e.seq WHERE e.agent_id=? AND e.kind='user' ORDER BY e.session_id,e.epoch,e.seq`, agentID)
	if err != nil {
		t.Logf("G5 user_query_error=%T", err)
	} else {
		for rows.Next() {
			var session, generation, digest string
			var epoch, seq int64
			var body []byte
			var notice sql.NullString
			if err := rows.Scan(&session, &epoch, &generation, &seq, &digest, &body, &notice); err != nil {
				t.Logf("G5 user_scan_error=%T", err)
				break
			}
			classification := "other"
			for _, prompt := range []string{"ready", "run the literal check", "check the literal", "explicit retry", "explicit same call replay", "explicit conflicting call ID"} {
				candidate, _ := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: prompt}}})
				if bytes.Equal(body, candidate) {
					classification = "exact-fixture-user:" + prompt
				}
			}
			if bytes.Equal(body, neutral) {
				classification = "exact-neutral"
			}
			if notice.Valid {
				candidate, _ := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: notice.String}}})
				if bytes.Equal(body, candidate) {
					classification = "exact-interrupted-notice"
				} else {
					classification = "notice-linked-body-not-equal"
				}
			}
			t.Logf("G5 user session=%s epoch=%d generation=%s seq=%d digest=%s body_sha256=%x classification=%s provider_last_user_equal=%t", session, epoch, generation, seq, digest, sha256.Sum256(body), classification, providerUser != nil && bytes.Equal(body, providerUser))
		}
		if err := rows.Err(); err != nil {
			t.Logf("G5 user_rows_error=%T", err)
		}
		rows.Close()
	}
	// Selected identity/state columns only. Never SELECT * or stored text.
	queries := []struct{ label, sql string }{
		{"provider-entry", `SELECT session_id,epoch,generation,user_seq,user_digest FROM embedded_nonmail_provider_entries WHERE agent_id=? ORDER BY user_seq`},
		{"wake", `SELECT slot,session_id,epoch,runtime_generation,process_generation,wake_id,kind,state,claim_owner_token,digest FROM embedded_restart_wake_slots WHERE agent_id=? ORDER BY slot`},
		{"wake-origin", `SELECT slot,session_id,epoch,runtime_generation,seq,wake_id,process_generation,claim_owner_token FROM embedded_restart_wake_checked_origins WHERE agent_id=? ORDER BY seq`},
		{"notice", `SELECT session_id,epoch,generation,source_seq,notice_id,state,entry_seq,start_owner_token FROM embedded_interrupted_notices WHERE agent_id=? ORDER BY source_seq`},
		{"effect", `SELECT effect_id,session_id,epoch,generation,state FROM embedded_effects WHERE agent_id=? ORDER BY effect_id`},
	}
	for _, query := range queries {
		rows, err := ro.QueryContext(ctx, query.sql, agentID)
		if err != nil {
			t.Logf("G5 %s query_error=%T", query.label, err)
			continue
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Logf("G5 %s columns_error=%T", query.label, err)
			rows.Close()
			continue
		}
		count := 0
		for rows.Next() {
			values := make([]any, len(columns))
			dest := make([]any, len(columns))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				t.Logf("G5 %s scan_error=%T", query.label, err)
				break
			}
			count++
			t.Logf("G5 %s columns=%v values=%v", query.label, columns, values)
		}
		if err := rows.Err(); err != nil {
			t.Logf("G5 %s rows_error=%T", query.label, err)
		}
		t.Logf("G5 %s row_count=%d", query.label, count)
		rows.Close()
	}
}
