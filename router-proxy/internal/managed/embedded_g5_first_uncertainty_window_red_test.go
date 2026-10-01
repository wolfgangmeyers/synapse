package managed

import (
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
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

const g5FirstWindowCallID = "g5-first-window-original-call"

func g5FirstWindowManager(t *testing.T, db *store.Store, root string, reg *providers.FauxProviderRegistration, fault func(context.Context)) *Manager {
	t.Helper()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() }, EmbeddedAfterIntent: fault, CodexCredentialStore: testfixture.CodexAdmission(t)})
	require.NoError(t, err)
	return m
}

func g5FirstWindowArgs(root string) map[string]any {
	return map[string]any{"action": "launch", "name": "g5-window-worker", "cwd": root, "instruction": "independent task"}
}

// Child runs the real Manager and SDK call. The only injection is a test
// callback at worker Execute entry, after original intent and before reservation.
// os.Exit is abrupt: no error result, deferred cleanup, or completion append.
func TestEmbeddedG5FirstWindowAbruptChild(t *testing.T) {
	if os.Getenv("G5_FIRST_WINDOW_CHILD") != "1" {
		return
	}
	ctx := context.Background()
	root, path := os.Getenv("G5_FIRST_WINDOW_ROOT"), os.Getenv("G5_FIRST_WINDOW_DB")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	reg.SetResponses([]providers.FauxResponseStep{func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("mecha_worker", g5FirstWindowArgs(root), g5FirstWindowCallID)}, ai.StopToolUse)
	}})
	m := g5FirstWindowManager(t, db, root, reg, func(context.Context) { os.Exit(77) })
	setEmbeddedRecoveryGeneration(t, ctx, db, m)
	_, err = m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "g5-window-root", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "launch independent worker", RequestID: "a5555555-5555-4555-8555-555555555570"})
	require.NoError(t, err)
	select {} // bounded by parent process timeout; normally os.Exit(77) in Execute
}

func TestEmbeddedG5FirstWindowAbruptIntentNoSilentReplayRED(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	cmd := exec.Command(os.Args[0], "-test.run=^TestEmbeddedG5FirstWindowAbruptChild$")
	cmd.Env = append(os.Environ(), "G5_FIRST_WINDOW_CHILD=1", "G5_FIRST_WINDOW_ROOT="+root, "G5_FIRST_WINDOW_DB="+path)
	require.NoError(t, cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		require.Equal(t, 77, cmd.ProcessState.ExitCode(), "child must exit abruptly at worker Execute entry: %v", err)
	case <-time.After(8 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatal("child never reached first window")
	}
	// Independent read-only connection proves both records' relative boundary.
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	var intent []byte
	var state string
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT intent,state FROM embedded_effects WHERE effect_id=?`, g5FirstWindowCallID).Scan(&intent, &state))
	var original struct {
		ID        string         `json:"id"`
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	require.NoError(t, json.Unmarshal(intent, &original))
	require.Equal(t, g5FirstWindowCallID, original.ID)
	require.Equal(t, "mecha_worker", original.Name)
	require.Equal(t, g5FirstWindowArgs(root), original.Arguments)
	require.Equal(t, "unknown", state)
	var operations, workers, results int
	// The deterministic worker request ID must not have a durable operation.
	var parentSession, parentGeneration string
	var parentEpoch int64
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT pi_session_id,session_epoch,runtime_generation FROM managed_agents WHERE agent_id='g5-window-root'`).Scan(&parentSession, &parentEpoch, &parentGeneration))
	parentBinding := store.EmbeddedBinding{AgentID: "g5-window-root", SessionID: parentSession, Epoch: parentEpoch, Generation: parentGeneration}
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM managed_http_operations WHERE request_id=?`, embeddedWorkerRequestID(parentBinding, g5FirstWindowCallID)).Scan(&operations))
	require.Zero(t, operations, "worker launch operation was reserved before fault")
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM managed_agents WHERE agent_id='g5-window-worker'`).Scan(&workers))
	require.Zero(t, workers)
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE kind='toolResult'`).Scan(&results))
	require.Zero(t, results)
	require.NoError(t, raw.Close())

	clock := deterministic.NewFixedClock(time.Now().Add(embeddedOwnerTTL + time.Minute))
	db, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer db.Close()
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	attempted := make(chan struct{}, 1)
	reg.SetResponses([]providers.FauxResponseStep{
		// Recovery is awareness only, not a retry of the UNKNOWN original ID.
		providers.FauxStatic(providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "interruption acknowledged"}}, ai.StopStop)),
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			attempted <- struct{}{}
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("mecha_worker", g5FirstWindowArgs(root), g5FirstWindowCallID)}, ai.StopToolUse)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
		},
	})
	m := g5FirstWindowManager(t, db, root, reg, nil)
	defer m.Close(ctx)
	setEmbeddedRecoveryGeneration(t, ctx, db, m)
	require.NoError(t, m.RecoverEmbeddedRoots(ctx))
	require.Eventually(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); _, ok := m.embeddedOwners["g5-window-root"]; return ok }, 4*time.Second, 10*time.Millisecond, "fresh Manager must reopen the saved root before a new SDK turn")
	m.mu.Lock()
	owner := m.embeddedOwners["g5-window-root"]
	m.mu.Unlock()
	saved, err := db.ManagedAgent(ctx, "g5-window-root")
	require.NoError(t, err)
	runCtx := context.WithValue(ctx, embeddedOwnerContextKey{}, owner)
	require.Error(t, m.runEmbeddedSessionPrompt(runCtx, *saved, "retry same original SDK call ID"))
	select {
	case <-attempted:
	default:
		t.Fatal("fresh SDK did not retry original call ID")
	}
	// First behavioral RED: current code treats unknown intent as permission to
	// create the missing worker operation. Absence alone cannot prove it unstarted.
	agents, err := db.ManagedAgents(ctx)
	require.NoError(t, err)
	require.Len(t, agents, 1, "unknown intent must not silently reserve/launch worker on SDK retry")
	effect, err := db.RecoverEmbeddedEffect(ctx, owner.binding, g5FirstWindowCallID)
	require.NoError(t, err)
	require.Equal(t, "unknown", effect.State)
	require.False(t, effect.ShouldReplay)
}
