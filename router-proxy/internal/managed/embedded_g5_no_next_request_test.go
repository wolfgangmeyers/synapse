package managed

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// A committed goal is pending until the SDK actually selects another provider request.
func TestEmbeddedG5GoalSetWithoutNextProviderRequestStaysPending(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(root, "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal", map[string]any{"action": "set", "goal": "pending goal"}, "g5-pending-call")}, ai.StopToolUse)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "finished"}}, ai.StopStop)
		},
	})
	calls := make(chan struct{}, 1)
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest,
		Config:                  Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:           CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver:   func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		CodexCredentialStore:    testfixture.CodexAdmission(t),
		EmbeddedAfterGoalCommit: func(context.Context) { close(entered); <-release },
	})
	require.NoError(t, err)
	defer func() { unblock(); _ = m.Close(ctx) }()
	setEmbeddedRecoveryGeneration(t, ctx, db, m)
	launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "g5-pending", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "set goal", RequestID: "a5555555-5555-4555-8555-555555555583"})
	require.NoError(t, err)
	select {
	case <-entered:
	case <-time.After(4 * time.Second):
		t.Fatal("goal commit not reached")
	}
	policy, err := db.ManagedGoalPolicyFor(ctx, launched.Agent.AgentID)
	require.NoError(t, err)
	require.EqualValues(t, 1, policy.DesiredRevision)
	require.Zero(t, policy.AppliedRevision)
	require.Equal(t, "pending", policy.ProjectionState)
	var receiptCount, evaluationCount int
	// The hook holds the SDK before its tool result and next provider request.
	readOnly, err := sql.Open("sqlite", "file:"+filepath.Join(root, "router.db")+"?mode=ro")
	require.NoError(t, err)
	defer readOnly.Close()
	require.NoError(t, readOnly.QueryRowContext(ctx, `SELECT count(*) FROM embedded_goal_applied_receipts WHERE agent_id=?`, launched.Agent.AgentID).Scan(&receiptCount))
	require.NoError(t, readOnly.QueryRowContext(ctx, `SELECT count(*) FROM embedded_goal_evaluations WHERE agent_id=?`, launched.Agent.AgentID).Scan(&evaluationCount))
	require.Zero(t, receiptCount)
	require.Zero(t, evaluationCount)
	select {
	case <-calls:
		t.Fatal("evaluated without next request")
	default:
	}
}
