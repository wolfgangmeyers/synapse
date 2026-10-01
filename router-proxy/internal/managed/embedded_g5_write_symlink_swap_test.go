package managed

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// The test swaps an internal parent after path validation but immediately before
// its descriptor-relative, nofollow open. This is not a same-UID root/ancestor
// rename sandbox proof: the saved root and its ancestors remain private and pinned.
func TestEmbeddedG5WorkerWriteParentSymlinkSwap(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	parent := filepath.Join(project, "inside")
	parked := filepath.Join(project, "parked")
	outside := filepath.Join(root, "outside")
	for _, dir := range []string{project, parent, outside} {
		require.NoError(t, os.Mkdir(dir, 0700))
	}
	const sentinelBytes = "outside stays intact\n"
	sentinel := filepath.Join(outside, "created.txt")
	require.NoError(t, os.WriteFile(sentinel, []byte(sentinelBytes), 0600))
	db, err := store.Open(ctx, filepath.Join(root, "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	const callID = "g5-parent-swap-write"
	args := map[string]any{"path": "inside/created.txt", "content": "escaped\n"}
	results := make(chan *ai.ToolResultMessage, 1)
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("write", args, callID)}, ai.StopToolUse)
		},
		func(history ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			var checked *ai.ToolResultMessage
			for _, message := range history.Messages {
				if result, ok := message.(ai.ToolResultMessage); ok && result.ToolCallID == callID {
					copy := result
					checked = &copy
				}
			}
			results <- checked
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
		},
	})
	m := g5WriteRenameManager(t, db, root, reg)
	defer m.Close(ctx)
	// The hook runs inline in the actual worker write, not in a competing timer.
	// Only this child component changes; neither the root nor its ancestors move.
	swapped := make(chan error, 1)
	embeddedBeforeWriteParentOpenForTest = func() {
		err := os.Rename(parent, parked)
		if err == nil {
			err = os.Symlink(outside, parent)
		}
		swapped <- err
	}
	defer func() { embeddedBeforeWriteParentOpenForTest = nil }()
	_, err = m.Launch(ctx, LaunchRequest{Name: "g5-swap-worker", Cwd: project, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "write the file", RequestID: "a5555555-5555-4555-8555-555555555590"})
	require.NoError(t, err)
	select {
	case err := <-swapped:
		require.NoError(t, err, "swap must happen at the actual parent-open boundary")
	case <-time.After(4 * time.Second):
		t.Fatal("worker did not reach the parent-open boundary")
	}
	select {
	case checked := <-results:
		require.NotNil(t, checked, "SDK must not silently report a successful write")
		require.Equal(t, callID, checked.ToolCallID)
		require.Equal(t, "write", checked.ToolName)
		require.True(t, checked.IsError, "nofollow parent denial must not become checked success")
		require.NotEmpty(t, checked.Content)
		require.Contains(t, checked.Content[0].(ai.TextContent).Text, "write parent denied")
	case <-time.After(4 * time.Second):
		t.Fatal("worker did not produce a checked denial")
	}
	actual, err := os.ReadFile(sentinel)
	require.NoError(t, err)
	require.Equal(t, sentinelBytes, string(actual))
	_, err = os.Lstat(filepath.Join(parked, "created.txt"))
	require.ErrorIs(t, err, os.ErrNotExist)
	link, err := os.Lstat(parent)
	require.NoError(t, err)
	require.NotZero(t, link.Mode()&os.ModeSymlink)

	raw, err := sql.Open("sqlite", filepath.Join(root, "router.db"))
	require.NoError(t, err)
	defer raw.Close()
	var count int
	var state string
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*), max(state) FROM embedded_effects WHERE effect_id=?`, callID).Scan(&count, &state))
	require.Equal(t, 1, count, "one admitted intent, no duplicate effect")
	require.Contains(t, []string{"unknown", "observed"}, state, "denied write cannot have a successful effect")
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE kind='toolResult'`).Scan(&count))
	require.Equal(t, 1, count, "one checked SDK result, no duplicate write")
}
