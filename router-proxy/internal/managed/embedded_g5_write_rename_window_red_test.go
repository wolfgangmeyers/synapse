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
	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
)

const g5WriteRenameCallID = "g5-write-rename-call"
const g5WriteRenameContent = "published once\n"

func g5WriteRenameArgs() map[string]any {
	return map[string]any{"path": "created.txt", "content": g5WriteRenameContent}
}

func g5WriteRenameManager(t *testing.T, db *store.Store, root string, reg *providers.FauxProviderRegistration) *Manager {
	t.Helper()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{filepath.Join(root, "project")}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() }, CodexCredentialStore: testfixture.CodexAdmission(t)})
	require.NoError(t, err)
	return m
}

// Real worker SDK execution. The fault only interrupts the process after the
// create-only rename succeeds, before fsync or checked SDK toolResult.
func TestEmbeddedG5WriteRenameAbruptChild(t *testing.T) {
	if os.Getenv("G5_WRITE_RENAME_CHILD") != "1" {
		return
	}
	ctx := context.Background()
	root := os.Getenv("G5_WRITE_RENAME_ROOT")
	db, err := store.Open(ctx, filepath.Join(root, "router.db"), store.Options{})
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	reg.SetResponses([]providers.FauxResponseStep{func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("write", g5WriteRenameArgs(), g5WriteRenameCallID)}, ai.StopToolUse)
	}})
	embeddedAfterWriteRenameForTest = func() { os.Exit(79) }
	m := g5WriteRenameManager(t, db, root, reg)
	_, err = m.Launch(ctx, LaunchRequest{Name: "g5-rename-worker", Cwd: filepath.Join(root, "project"), Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "write the file", RequestID: "a5555555-5555-4555-8555-555555555589"})
	require.NoError(t, err)
	select {} // Parent bounds the wait; expected exit is in the rename seam.
}

func TestEmbeddedG5WriteRenameNoReplayRED(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	require.NoError(t, os.Mkdir(project, 0700))
	cmd := exec.Command(os.Args[0], "-test.run=^TestEmbeddedG5WriteRenameAbruptChild$")
	cmd.Env = append(os.Environ(), "G5_WRITE_RENAME_CHILD=1", "G5_WRITE_RENAME_ROOT="+root)
	require.NoError(t, cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		require.Equal(t, 79, cmd.ProcessState.ExitCode(), "child must exit at successful rename: %v", err)
	case <-time.After(8 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatal("child never reached post-rename fault")
	}
	final := filepath.Join(project, "created.txt")
	contents, err := os.ReadFile(final)
	require.NoError(t, err)
	require.Equal(t, g5WriteRenameContent, string(contents))
	raw, err := sql.Open("sqlite", filepath.Join(root, "router.db"))
	require.NoError(t, err)
	var intent []byte
	var state string
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT intent,state FROM embedded_effects WHERE effect_id=?`, g5WriteRenameCallID).Scan(&intent, &state))
	var original struct {
		ID        string         `json:"id"`
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	require.NoError(t, json.Unmarshal(intent, &original))
	require.Equal(t, g5WriteRenameCallID, original.ID)
	require.Equal(t, "write", original.Name)
	require.Equal(t, g5WriteRenameArgs(), original.Arguments)
	require.Equal(t, "unknown", state)
	var results int
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE kind='toolResult'`).Scan(&results))
	require.Zero(t, results, "no checked SDK result can predate the crash")
	require.NoError(t, raw.Close())

	clock := deterministic.NewFixedClock(time.Now().Add(embeddedOwnerTTL + time.Minute))
	db, err := store.Open(ctx, filepath.Join(root, "router.db"), store.Options{Clock: clock})
	require.NoError(t, err)
	defer db.Close()
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	attempted := make(chan struct{}, 1)
	continuation := make(chan *ai.ToolResultMessage, 1)
	var recoveryGeneration, noticeSeq, noticeOwner int64
	var noticeID, noticeDigest string
	reg.SetResponses([]providers.FauxResponseStep{
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			read, err := sql.Open("sqlite", "file:"+filepath.Join(root, "router.db")+"?mode=ro")
			require.NoError(t, err)
			defer read.Close()
			var body []byte
			require.NoError(t, read.QueryRowContext(ctx, `SELECT n.notice_id,n.entry_seq,e.digest,e.body,n.start_owner_token FROM embedded_interrupted_notices n JOIN embedded_entries e ON e.agent_id=n.agent_id AND e.session_id=n.session_id AND e.epoch=n.epoch AND e.generation=n.generation AND e.seq=n.entry_seq JOIN embedded_sessions s ON s.agent_id=n.agent_id AND s.session_id=n.session_id AND s.epoch=n.epoch AND s.generation=n.generation AND s.current=1 AND s.owner_token=n.start_owner_token JOIN embedded_restart_wake_slots w ON w.agent_id=s.agent_id AND w.session_id=s.session_id AND w.epoch=s.epoch AND w.runtime_generation=s.generation AND w.wake_id=n.notice_id AND w.claim_owner_token=s.owner_token WHERE n.agent_id='g5-rename-worker' AND n.source_seq=2 AND n.state='started' AND e.kind='user' AND w.slot='current' AND w.kind='interrupted' AND w.digest=e.digest AND w.state='claimed' AND w.process_generation=?`, recoveryGeneration).Scan(&noticeID, &noticeSeq, &noticeDigest, &body, &noticeOwner))
			var lastUser []byte
			for _, message := range h.Messages {
				if user, ok := message.(ai.UserMessage); ok {
					lastUser, err = json.Marshal(user)
					require.NoError(t, err)
				}
			}
			require.Equal(t, body, lastUser, "only the exact claimed interruption may complete recovery")
			t.Logf("write-rename recovery request notice=%s seq=%d digest=%s owner=%d process=%d finish=%s", noticeID, noticeSeq, noticeDigest, noticeOwner, recoveryGeneration, ai.StopStop)
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "interruption acknowledged"}}, ai.StopStop)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			attempted <- struct{}{}
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("write", g5WriteRenameArgs(), g5WriteRenameCallID)}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			var result *ai.ToolResultMessage
			for _, msg := range h.Messages {
				if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == g5WriteRenameCallID {
					copy := r
					result = &copy
				}
			}
			continuation <- result
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
		},
	})
	m := g5WriteRenameManager(t, db, root, reg)
	defer m.Close(ctx)
	recoveryGeneration = setEmbeddedRecoveryGeneration(t, ctx, db, m)
	defer func() {
		if !t.Failed() {
			return
		}
		m.mu.Lock()
		publishedOwner, ownerPublished := m.embeddedOwners["g5-rename-worker"]
		m.mu.Unlock()
		t.Logf("write-rename recovery clock=%s process_generation=%d child_exit=%d original_state=%s checked_results=%d owner_published=%t binding=%+v", clock.Now(), recoveryGeneration, cmd.ProcessState.ExitCode(), state, results, ownerPublished, publishedOwner.binding)
		row, rowErr := db.ManagedAgent(ctx, "g5-rename-worker")
		t.Logf("write-rename managed=%+v query_error=%v", row, rowErr)
		cwdPrivacyManagedSnapshot(t, filepath.Join(root, "router.db"))
		read, readErr := sql.Open("sqlite", "file:"+filepath.Join(root, "router.db")+"?mode=ro")
		if readErr != nil {
			t.Logf("write-rename snapshot open_error=%v", readErr)
			return
		}
		defer read.Close()
		for _, query := range []string{
			`SELECT json_group_array(json_object('id',effect_id,'state',state,'intent',CAST(intent AS TEXT),'result',CAST(result AS TEXT))) FROM embedded_effects`,
			`SELECT json_group_array(json_object('seq',seq,'kind',kind,'body',CAST(body AS TEXT))) FROM embedded_entries`,
		} {
			var snapshot string
			queryErr := read.QueryRowContext(ctx, query).Scan(&snapshot)
			t.Logf("write-rename query=%s result=%s query_error=%v", query, snapshot, queryErr)
		}
		published, fileErr := os.ReadFile(final)
		t.Logf("write-rename published=%q file_error=%v", published, fileErr)
	}()
	require.NoError(t, m.RecoverEmbeddedRoots(ctx))
	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		_, ok := m.embeddedOwners["g5-rename-worker"]
		return ok
	}, 4*time.Second, 10*time.Millisecond)
	m.mu.Lock()
	owner := m.embeddedOwners["g5-rename-worker"]
	m.mu.Unlock()
	require.Equal(t, noticeOwner, owner.binding.OwnerToken)
	require.NotEmpty(t, noticeID)
	read, err := sql.Open("sqlite", "file:"+filepath.Join(root, "router.db")+"?mode=ro")
	require.NoError(t, err)
	defer read.Close()
	var checkedSeq int64
	var checkedBody []byte
	require.NoError(t, read.QueryRowContext(ctx, `SELECT e.seq,e.body FROM embedded_restart_wake_slots w JOIN embedded_restart_wake_checked_origins o ON o.agent_id=w.agent_id AND o.slot=w.slot AND o.session_id=w.session_id AND o.epoch=w.epoch AND o.runtime_generation=w.runtime_generation AND o.wake_id=w.wake_id AND o.process_generation=w.process_generation AND o.claim_owner_token=w.claim_owner_token JOIN embedded_entries e ON e.agent_id=o.agent_id AND e.session_id=o.session_id AND e.epoch=o.epoch AND e.generation=o.runtime_generation AND e.seq=o.seq WHERE w.agent_id=? AND w.wake_id=? AND w.process_generation=? AND w.claim_owner_token=? AND w.state='checked-complete' AND e.kind='assistant' AND e.parent_digest=?`, owner.binding.AgentID, noticeID, recoveryGeneration, noticeOwner, noticeDigest).Scan(&checkedSeq, &checkedBody))
	require.Equal(t, noticeSeq+1, checkedSeq)
	checkedMessage, err := ai.UnmarshalMessage(checkedBody)
	require.NoError(t, err)
	checkedFinal, ok := checkedMessage.(ai.AssistantMessage)
	require.True(t, ok)
	require.Equal(t, ai.StopStop, checkedFinal.StopReason)
	require.Equal(t, ai.ContentList{ai.TextContent{Text: "interruption acknowledged"}}, checkedFinal.Content)
	require.NoError(t, read.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE kind='toolResult'`).Scan(&results))
	require.Zero(t, results, "checked recovery is not an original write result")
	select {
	case <-attempted:
		t.Fatal("recovery retried original write")
	default:
	}
	worker, err := db.ManagedAgent(ctx, "g5-rename-worker")
	require.NoError(t, err)
	_ = m.runEmbeddedSessionPrompt(context.WithValue(ctx, embeddedOwnerContextKey{}, owner), *worker, "retry original call")
	select {
	case <-attempted:
	default:
		t.Fatal("fresh SDK never retried original call ID")
	}
	select {
	case result := <-continuation:
		require.Nil(t, result, "retry must not invent a checked success result")
	default:
		// Admission may abort the turn before any SDK continuation.
	}
	// SDK transcript dedup may suppress Execute. Check the recovered tool directly
	// too: the same intent must remain UNKNOWN, not return create success or rerun.
	fd, err := pinEmbeddedReadRoot(project)
	require.NoError(t, err)
	defer func() { _ = unix.Close(fd) }()
	changed := map[string]any{"path": "second.txt", "content": "different\n"}
	_, conflictErr := m.embeddedWorkerWriteTool(owner, fd, func(string) bool { return true }).Execute(ctx, g5WriteRenameCallID, changed, nil)
	require.ErrorContains(t, conflictErr, "conflict", "changed args with same call ID must conflict")
	_, err = os.Stat(filepath.Join(project, "second.txt"))
	require.ErrorIs(t, err, os.ErrNotExist)
	contents, err = os.ReadFile(final)
	require.NoError(t, err)
	require.Equal(t, g5WriteRenameContent, string(contents))
	effect, err := db.RecoverEmbeddedEffect(ctx, owner.binding, g5WriteRenameCallID)
	require.NoError(t, err)
	require.Equal(t, "unknown", effect.State)
	require.False(t, effect.ShouldReplay)
	_, retryErr := m.embeddedWorkerWriteTool(owner, fd, func(string) bool { return true }).Execute(ctx, g5WriteRenameCallID, g5WriteRenameArgs(), nil)
	require.ErrorContains(t, retryErr, "unknown", "same original call cannot report success or retry the write")
}
