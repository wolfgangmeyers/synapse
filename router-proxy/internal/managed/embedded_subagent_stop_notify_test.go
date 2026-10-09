package managed

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestStopSubagentNotifiesParentWorker(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "stop_subagent.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-worker-stop")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	parentRecord, err := db.ManagedAgent(ctx, parent.AgentID)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(parentRecord.Cwd, "test.txt"), []byte("sample content"), 0o600))

	steps := make([]providers.FauxResponseStep, 20)
	for i := 0; i < 20; i++ {
		idx := i + 1
		steps[i] = func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			time.Sleep(30 * time.Millisecond)
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.ToolCall{ID: fmt.Sprintf("call-%d", idx), Name: "read", Arguments: map[string]any{"path": "test.txt"}},
			}, ai.StopStop)
		}
	}
	reg.SetResponses(steps)

	mgr := makeSubagentTestManager(t, db, reg, 1)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	_, err = mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:     "subagent-parent-stop-1",
		Description: "running child",
		Prompt:      "prompt",
	})
	require.NoError(t, err)

	childID := "subagent-parent-stop-2"
	childDesc := "queued child to be stopped"
	res2, err := mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:     childID,
		Description: childDesc,
		Prompt:      "prompt",
	})
	require.NoError(t, err)
	require.Equal(t, "queued", res2.Status)

	// Stop subagent
	err = mgr.StopSubagent(ctx, parent.AgentID, childID)
	require.NoError(t, err)

	// Verify notification message in store
	expectedMsgID := "subagent-terminated-" + childID + "-1"
	msg, err := db.MessageFor(ctx, expectedMsgID)
	require.NoError(t, err)
	require.NotNil(t, msg, "mailbox message must be enqueued when subagent is stopped")
	require.Equal(t, "system", msg.From)
	require.Equal(t, parent.AgentID, msg.To)
	require.NotNil(t, msg.Subject)
	require.Equal(t, "[System Notice] Subagent Terminated: "+childID, *msg.Subject)
	expectedBody := "Subagent " + childID + " (\"" + childDesc + "\") was stopped. You can fetch its latest state and findings using get_subagent_result(agent_id=\"" + childID + "\").\n\nAutomated system notification. No mailbox reply is needed or expected. Proceed directly with your next step."
	require.Equal(t, expectedBody, msg.Body)
}
