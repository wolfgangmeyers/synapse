package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestStartupReconciliationDanglingHistoryOutcome(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	mgr, err := NewManager(ManagerOptions{
		Store:    db,
		Manifest: manifest,
		Config:   Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
	})
	require.NoError(t, err)
	defer mgr.Close(ctx)

	rawDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer rawDB.Close()

	// Insert dangling active history entry
	_, err = rawDB.ExecContext(ctx, `INSERT INTO managed_complete_history(agent_id, category, level, message, occurred_at, presentation_json)
		VALUES ('workflows', 'managed_tool', 'info', 'Running bash', '2026-10-08T14:40:34.372Z', '{"kind":"tool","tool_name":"bash","outcome":"active","summary":"Running"}')`)
	require.NoError(t, err)

	// Insert completed history entry
	_, err = rawDB.ExecContext(ctx, `INSERT INTO managed_complete_history(agent_id, category, level, message, occurred_at, presentation_json)
		VALUES ('workflows', 'managed_tool', 'info', 'Ran echo', '2026-10-08T14:40:30.000Z', '{"kind":"tool","tool_name":"bash","outcome":"completed","summary":"Succeeded"}')`)
	require.NoError(t, err)

	// Run startup reconciliation via Manager
	require.NoError(t, mgr.ResetActiveEmbeddedAgents(ctx))

	var activeOutcome, activeSummary string
	require.NoError(t, rawDB.QueryRowContext(ctx, `SELECT json_extract(presentation_json, '$.outcome'), json_extract(presentation_json, '$.summary') FROM managed_complete_history WHERE message='Running bash'`).Scan(&activeOutcome, &activeSummary))
	require.Equal(t, "failed", activeOutcome)
	require.Equal(t, "Interrupted by restart", activeSummary)

	var completedOutcome, completedSummary string
	require.NoError(t, rawDB.QueryRowContext(ctx, `SELECT json_extract(presentation_json, '$.outcome'), json_extract(presentation_json, '$.summary') FROM managed_complete_history WHERE message='Ran echo'`).Scan(&completedOutcome, &completedSummary))
	require.Equal(t, "completed", completedOutcome)
	require.Equal(t, "Succeeded", completedSummary)

	// Idempotency: running again should not change anything
	require.NoError(t, mgr.ResetActiveEmbeddedAgents(ctx))
	require.NoError(t, rawDB.QueryRowContext(ctx, `SELECT json_extract(presentation_json, '$.outcome'), json_extract(presentation_json, '$.summary') FROM managed_complete_history WHERE message='Running bash'`).Scan(&activeOutcome, &activeSummary))
	require.Equal(t, "failed", activeOutcome)
	require.Equal(t, "Interrupted by restart", activeSummary)
}

func TestStartupReconciliationDanglingEmbeddedToolCall(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	mgr, err := NewManager(ManagerOptions{
		Store:    db,
		Manifest: manifest,
		Config:   Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
	})
	require.NoError(t, err)
	defer mgr.Close(ctx)

	binding := store.EmbeddedBinding{
		AgentID:    "workflows",
		SessionID:  "sess-reconcile",
		Epoch:      1,
		Generation: "gen-reconcile",
	}

	// Create managed agent and embedded session
	rawDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer rawDB.Close()

	_, err = rawDB.ExecContext(ctx, `INSERT INTO managed_agents(agent_id, name, slot_held, cwd, selected_model_provider, selected_model_id, desired_state, lifecycle_state, activity_state, pi_session_id, session_epoch, runtime_generation, manifest_revision, manifest_hash, created_at, started_at, updated_at, runtime)
		VALUES ('workflows', 'workflows', 1, '/tmp', 'google-vertex', 'gemini-3.8-flash', 'running', 'running', 'active', 'sess-reconcile', 1, 'gen-reconcile', 1, 'hash', '2026-10-09T00:00:00Z', '2026-10-09T00:00:00Z', '2026-10-09T00:00:00Z', 'embedded')`)
	require.NoError(t, err)

	require.NoError(t, db.BindEmbeddedSession(ctx, binding, "worker"))
	token, err := db.AcquireEmbeddedOwner(ctx, binding, "owner-1", time.Minute)
	require.NoError(t, err)
	binding.OwnerToken = token

	// User entry
	userMsg, _ := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "do something"}}})
	_, err = db.AppendEmbeddedEntry(ctx, binding, "user", userMsg)
	require.NoError(t, err)

	// Assistant entry with tool call
	assistantMsg, _ := json.Marshal(ai.AssistantMessage{
		Content: ai.ContentList{
			ai.ToolCall{
				ID:        "call_interrupted_123",
				Name:      "bash",
				Arguments: map[string]any{"command": "sleep 10"},
			},
		},
		StopReason: ai.StopToolUse,
	})
	_, err = db.AppendEmbeddedEntry(ctx, binding, "assistant", assistantMsg)
	require.NoError(t, err)

	// Active tool history entry
	_, err = rawDB.ExecContext(ctx, `INSERT INTO managed_complete_history(agent_id, category, level, message, occurred_at, presentation_json)
		VALUES ('workflows', 'managed_tool', 'info', 'Running bash', '2026-10-09T00:00:01Z', '{"kind":"tool","tool_name":"bash","outcome":"active","summary":"Running"}')`)
	require.NoError(t, err)

	// Run recovery via Manager
	require.NoError(t, mgr.ResetActiveEmbeddedAgents(ctx))

	// Verify history is marked failed
	var activeOutcome, activeSummary string
	require.NoError(t, rawDB.QueryRowContext(ctx, `SELECT json_extract(presentation_json, '$.outcome'), json_extract(presentation_json, '$.summary') FROM managed_complete_history WHERE message='Running bash'`).Scan(&activeOutcome, &activeSummary))
	require.Equal(t, "failed", activeOutcome)
	require.Equal(t, "Interrupted by restart", activeSummary)

	// Verify embedded_entries has synthetic toolResult
	var lastKind, lastBody string
	require.NoError(t, rawDB.QueryRowContext(ctx, `SELECT kind, CAST(body AS TEXT) FROM embedded_entries WHERE agent_id='workflows' ORDER BY seq DESC LIMIT 1`).Scan(&lastKind, &lastBody))
	require.Equal(t, "toolResult", lastKind)

	var tr ai.ToolResultMessage
	require.NoError(t, json.Unmarshal([]byte(lastBody), &tr))
	require.Equal(t, "call_interrupted_123", tr.ToolCallID)
	require.Equal(t, "bash", tr.ToolName)
	require.True(t, tr.IsError)
	require.Equal(t, "Command interrupted by router restart", tr.Content[0].(ai.TextContent).Text)
}
