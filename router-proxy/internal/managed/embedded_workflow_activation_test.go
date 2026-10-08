package managed

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestWorkflowActivationHistoryAndAssistantReply(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	path := filepath.Join(t.TempDir(), "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	initialStep := func(h ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "ready"}}, ai.StopStop)
	}

	replyStep := func(h ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "3 * 3 is 9."}}, ai.StopStop)
	}

	reg.SetResponses([]providers.FauxResponseStep{initialStep, replyStep})

	manager, err := NewManager(ManagerOptions{
		Store:                 db,
		Manifest:              manifest,
		Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		CodexCredentialStore:  testfixture.CodexAdmission(t),
	})
	require.NoError(t, err)
	defer manager.Close(ctx)

	launched, err := manager.Launch(ctx, LaunchRequest{
		Name:      "workflow-agent",
		Cwd:       root,
		Runtime:   "embedded",
		Model:     &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		RequestID: "d1111111-1111-4111-8111-111111111111",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return e == nil && a.LifecycleState == "running" && a.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	reg.SetResponses([]providers.FauxResponseStep{replyStep})

	agentID := launched.Agent.AgentID
	promptMsg := "[Workflow Activated: test-state]\n\nReport to the user what 3 * 3 is."
	promptReqID := "d2222222-2222-4222-8222-222222222222"

	// Admit workflow activation prompt
	err = manager.AdmitPromptWithRequestID(ctx, agentID, promptMsg, promptReqID)
	require.NoError(t, err)

	// Check managed_complete_history
	rawDB, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer rawDB.Close()

	// 1. Verify prompt entry was recorded in managed_complete_history
	var promptCategory, promptMsgText, promptContent, promptSender string
	err = rawDB.QueryRowContext(ctx, `SELECT category, message, message_content, COALESCE(sender_identity, '') FROM managed_complete_history WHERE agent_id=? AND message='Workflow activated'`, agentID).Scan(&promptCategory, &promptMsgText, &promptContent, &promptSender)
	require.NoError(t, err, "Workflow activation prompt must be recorded in managed_complete_history")
	require.Equal(t, "managed_message", promptCategory)
	require.Equal(t, "Workflow activated", promptMsgText)
	require.Equal(t, promptMsg, promptContent)

	// 2. Verify assistant response was recorded in managed_complete_history
	var replyCategory, replyMsgText, replyContent, replySender string
	err = rawDB.QueryRowContext(ctx, `SELECT category, message, message_content, COALESCE(sender_identity, '') FROM managed_complete_history WHERE agent_id=? AND message='Message sent' AND message_content='3 * 3 is 9.'`, agentID).Scan(&replyCategory, &replyMsgText, &replyContent, &replySender)
	require.NoError(t, err, "Assistant reply must be recorded in managed_complete_history")
	require.Equal(t, "managed_message", replyCategory)
	require.Equal(t, "Message sent", replyMsgText)
	require.Equal(t, "3 * 3 is 9.", replyContent)
}
