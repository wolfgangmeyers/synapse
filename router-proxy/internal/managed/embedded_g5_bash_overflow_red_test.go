package managed

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// A permitted real grep emits a line beyond the output cap. After its intent,
// overflow must fail the turn explicitly without checking a partial SDK result.
func TestEmbeddedG5RestrictedBashOutputOverflowRED(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base := t.TempDir()
	project := filepath.Join(base, "project")
	require.NoError(t, os.Mkdir(project, 0700))
	const callID = "bash-overflow"
	const agentID = "bash-overflow-root"
	const command = "grep -F -n -- MARK big.txt"
	big := append([]byte("MARK"), bytes.Repeat([]byte("X"), 96*1024)...)
	big = append(big, '\n')
	require.NoError(t, os.WriteFile(filepath.Join(project, "big.txt"), big, 0600))
	dbPath := filepath.Join(base, "router.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	registered := make(chan bool, 1)
	var callbacks atomic.Int32
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
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			callbacks.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "unexpected continuation"}}, ai.StopStop)
		},
	})
	// The test-only gate from the deterministic timeout fixture lets us observe
	// the real process leader, then release the fixed host-authored command.
	spawned := make(chan int, 1)
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest,
		Config:                Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{project}},
		CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		CodexCredentialStore:  testfixture.CodexAdmission(t),
	})
	require.NoError(t, err)
	defer m.Close(context.Background())
	m.embeddedBashAfterStart = func(pid int, gate *os.File) {
		spawned <- pid
		_, _ = gate.Write([]byte("\n"))
		_ = gate.Close()
	}
	launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: agentID, Cwd: project, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "check the private file", RequestID: "a5555555-5555-4555-8555-5555555555a0"})
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
	// A terminal saved lifecycle is the synchronization point, not a second
	// provider callback (which must never run after an uncertain effect).
	require.Eventually(t, func() bool {
		saved, readErr := db.ManagedAgent(ctx, agentID)
		return readErr == nil && saved.LifecycleState == "failed"
	}, 5*time.Second, 10*time.Millisecond, "overflow did not terminate the agent lifecycle")
	failed, err := db.ManagedAgent(ctx, agentID)
	require.NoError(t, err)
	require.Equal(t, "EMBEDDED_START_FAILED", failed.LastErrorCode)
	require.LessOrEqual(t, len(failed.LastErrorMessage), 512, "saved lifecycle error must be bounded")
	require.True(t, strings.Contains(strings.ToLower(failed.LastErrorMessage), "truncat"), "saved lifecycle lost the explicit truncated error")

	check, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	require.NoError(t, err)
	defer check.Close()
	var intent, savedResult []byte
	var state string
	require.NoError(t, check.QueryRowContext(ctx, `SELECT intent,state,result FROM embedded_effects WHERE effect_id=? AND agent_id=?`, callID, agentID).Scan(&intent, &state, &savedResult))
	var original struct {
		ID        string         `json:"id"`
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	require.NoError(t, json.Unmarshal(intent, &original))
	require.Equal(t, callID, original.ID)
	require.Equal(t, "bash", original.Name)
	require.Equal(t, map[string]any{"command": command}, original.Arguments)
	require.Equal(t, "unknown", state, "overflow cannot certify an observed effect")
	require.Empty(t, savedResult, "UNKNOWN effect cannot carry a checked result")
	var resultCount int
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id=? AND kind='toolResult'`, agentID).Scan(&resultCount))
	require.Zero(t, resultCount, "post-intent overflow cannot append an SDK toolResult")
	var outboxCount int
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_reply_outbox`).Scan(&outboxCount))
	require.Zero(t, outboxCount, "overflow must not queue a reply")
	var body []byte
	require.NoError(t, check.QueryRowContext(ctx, `SELECT body FROM embedded_entries WHERE agent_id=? AND kind='assistant' ORDER BY seq DESC LIMIT 1`, agentID).Scan(&body))
	message, err := ai.UnmarshalMessage(body)
	require.NoError(t, err)
	persisted, ok := message.(ai.AssistantMessage)
	require.True(t, ok, "last assistant must be a saved SDK message")
	require.Equal(t, ai.StopToolUse, persisted.StopReason, "preappend rejection must retain the ordered tool-use message")
	// A fixed quiet interval follows the saved terminal lifecycle. Never wait for
	// a prohibited provider continuation or mistake a restored result for one.
	select {
	case <-time.After(250 * time.Millisecond):
	case <-ctx.Done():
		t.Fatal("deadline before provider quiet interval")
	}
	require.EqualValues(t, 1, callbacks.Load(), "post-overflow provider continuation")
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) == syscall.ESRCH }, 2*time.Second, 10*time.Millisecond, "Bash leader not reaped")
	require.Eventually(t, func() bool { return syscall.Kill(-pid, 0) == syscall.ESRCH }, 2*time.Second, 10*time.Millisecond, "process group still alive")
	content, err := os.ReadFile(filepath.Join(project, "big.txt"))
	require.NoError(t, err)
	require.True(t, bytes.Equal(big, content), "read-only bash changed the private input")
}
