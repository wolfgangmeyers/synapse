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

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// A permitted real grep emits output beyond the 512 KiB output cap.
// Truncation bounds output cleanly, kills the process group, and records the tool
// result directly in the transcript without creating an embedded_effects intent.
func TestEmbeddedG5RestrictedBashOutputOverflowRED(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base := t.TempDir()
	project := filepath.Join(base, "project")
	require.NoError(t, os.Mkdir(project, 0700))
	const callID = "bash-overflow"
	const agentID = "bash-overflow-root"
	const command = "grep -F -n -- MARK big.txt"
	big := append([]byte("MARK"), bytes.Repeat([]byte("X"), 600*1024)...)
	big = append(big, '\n')
	require.NoError(t, os.WriteFile(filepath.Join(project, "big.txt"), big, 0600))
	dbPath := filepath.Join(base, "router.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{
		Models: []providers.FauxModelDefinition{{ID: "gpt-5.6-terra", ContextWindow: 1_000_000, MaxTokens: 4096, Input: []string{"text"}}},
	})
	defer reg.Unregister()
	registered := make(chan bool, 1)
	var callbacks atomic.Int32
	toolResultSeen := make(chan string, 1)
	reg.SetResponses([]providers.FauxResponseStep{
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			callbacks.Add(1)
			found := false
			for _, msg := range h.Messages {
				if sys, ok := msg.(ai.SystemMessage); ok {
					for _, tool := range sys.ToolsAdded {
						found = found || tool.Name == "bash"
					}
				}
			}
			registered <- found
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("bash", map[string]any{"command": command}, callID)}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			callbacks.Add(1)
			for _, msg := range h.Messages {
				if tr, ok := msg.(ai.ToolResultMessage); ok && tr.ToolCallID == callID {
					toolResultSeen <- fmt.Sprint(tr.Content)
				}
			}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "handled truncation"}}, ai.StopStop)
		},
	})
	spawned := make(chan int, 1)
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest,
		Config:                Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{project}},
		CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		CodexCredentialStore:  testfixture.CodexAdmission(t),
	})
	require.NoError(t, err)
	defer m.Close(context.Background())
	m.embeddedBashAfterStart = func(pid int) {
		spawned <- pid
	}
	launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: agentID, Cwd: project, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "check the private file", RequestID: "a5555555-5555-4555-8555-5555555555a0", AllowInlineTools: true})
	require.NoError(t, err)
	require.Equal(t, project, launched.Agent.Cwd)
	select {
	case found := <-registered:
		require.True(t, found, "real bash not advertised to SDK")
	case <-ctx.Done():
		t.Fatal("initial SDK request did not register bash")
	}
	var pid int
	select {
	case pid = <-spawned:
	case <-ctx.Done():
		t.Fatal("real Bash did not start")
	}

	select {
	case resultText := <-toolResultSeen:
		require.Contains(t, resultText, "Output truncated at 524288 bytes")
	case <-time.After(5 * time.Second):
		t.Fatal("SDK continuation did not receive truncated tool result")
	}

	require.Eventually(t, func() bool {
		saved, readErr := db.ManagedAgent(ctx, agentID)
		return readErr == nil && saved.LifecycleState == "running" && callbacks.Load() == 2
	}, 5*time.Second, 10*time.Millisecond, "turn did not complete cleanly")

	check, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	require.NoError(t, err)
	defer check.Close()

	// Ordinary tools record result directly in transcript
	var resultCount int
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id=? AND kind='toolResult'`, agentID).Scan(&resultCount))
	require.Equal(t, 1, resultCount, "truncated bash must append SDK toolResult directly to transcript")

	// Ordinary tools do not write to embedded_effects
	var effectCount int
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_effects WHERE effect_id=? AND agent_id=?`, callID, agentID).Scan(&effectCount))
	require.Zero(t, effectCount, "ordinary bash must not write intent to embedded_effects")

	// Bash leader and process group are reaped
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) == syscall.ESRCH }, 2*time.Second, 10*time.Millisecond, "Bash leader not reaped")
	require.Eventually(t, func() bool { return syscall.Kill(-pid, 0) == syscall.ESRCH }, 2*time.Second, 10*time.Millisecond, "process group still alive")
	content, err := os.ReadFile(filepath.Join(project, "big.txt"))
	require.NoError(t, err)
	require.True(t, bytes.Equal(big, content), "read-only bash changed the private input")
}

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
