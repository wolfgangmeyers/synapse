package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func schemaAuthorityFixture(t *testing.T) (*embeddedTurnToolAuthority, *Manager, string) {
	t.Helper()
	ctx := context.Background()
	db, b, _ := completionFixture(t)
	m := &Manager{store: db, runCtx: ctx}
	p := &embeddedPersistence{db: db, binding: b, digest: "genesis"}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sample.txt"), []byte("schema regression content\n"), 0600))
	a, err := m.newEmbeddedTurnToolAuthority(ctx, embeddedOwner{binding: b, identity: "wake-fixture"}, p, SavedAgentSession{Cwd: dir}, false)
	require.NoError(t, err)
	require.NoError(t, a.AppendCheckedMessage(ctx, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "read"}}}))
	return a, m, b.AgentID
}

func TestIssue45ReadPaginationHostContract(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"omitted", map[string]any{"path": "sample.txt"}},
		{"integer", map[string]any{"path": "sample.txt", "offset": 1, "limit": 1}},
		{"null", map[string]any{"path": "sample.txt", "offset": nil, "limit": nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, _ := schemaAuthorityFixture(t)
			var read agent.AgentTool
			for _, tool := range a.Tools() {
				if tool.Name == "read" {
					read = tool
				}
			}
			require.NotNil(t, read.Parameters)
			schema, err := json.Marshal(read.Parameters)
			require.NoError(t, err)
			t.Logf("SDK read schema: %s", schema)
			call := ai.ToolCall{ID: "read-" + tc.name, Name: "read", Arguments: tc.args}
			require.NoError(t, a.AppendCheckedMessage(context.Background(), ai.AssistantMessage{Content: ai.ContentList{call}, StopReason: ai.StopToolUse}))
			require.NoError(t, a.BeforeToolExecute(context.Background(), agent.BeforeToolCallContext{ToolCall: call}), "advertised nullable contract must execute")
			result, err := read.Execute(context.Background(), call.ID, call.Arguments, nil)
			require.NoError(t, err)
			require.Contains(t, fmt.Sprint(result.Content), "schema regression content")
		})
	}
}

func TestIssue45HostDenialsAreActionableAndSafe(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  map[string]any
		field string
	}{
		{"fractional-offset", map[string]any{"path": "secret-value-45", "offset": 1.5}, "offset"},
		{"fractional-limit", map[string]any{"path": "secret-value-45", "limit": 1.5}, "limit"},
		{"wrong-type", map[string]any{"path": "secret-value-45", "offset": "secret-value-45"}, "offset"},
		{"missing-path", map[string]any{"offset": 1}, "path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, m, _ := schemaAuthorityFixture(t)
			call := ai.ToolCall{ID: "invalid-" + tc.name, Name: "read", Arguments: tc.args}
			err := a.BeforeToolExecute(context.Background(), agent.BeforeToolCallContext{ToolCall: call})
			require.Error(t, err)
			_, recoverErr := m.store.RecoverEmbeddedEffect(context.Background(), a.persistence.binding, call.ID)
			require.ErrorIs(t, recoverErr, sql.ErrNoRows)
			require.NotContains(t, err.Error(), "secret-value-45")
			require.Contains(t, strings.ToLower(err.Error()), "read")
			require.Contains(t, strings.ToLower(err.Error()), tc.field)
		})
	}
}

func TestIssue45HostCurrentCatalogMiss(t *testing.T) {
	a, m, id := schemaAuthorityFixture(t)
	// SDK's per-turn tool remains available, but the host's current base catalog changed.
	var remaining []agent.AgentTool
	for _, tool := range a.baseTools {
		if tool.Name != "read" {
			remaining = append(remaining, tool)
		}
	}
	m.laneFor(id).setTools(remaining, remaining)
	call := ai.ToolCall{ID: "catalog-miss", Name: "read", Arguments: map[string]any{"path": "secret-value-45"}}
	err := a.BeforeToolExecute(context.Background(), agent.BeforeToolCallContext{ToolCall: call})
	require.Error(t, err)
	_, recoverErr := m.store.RecoverEmbeddedEffect(context.Background(), a.persistence.binding, call.ID)
	require.ErrorIs(t, recoverErr, sql.ErrNoRows)
	require.NotContains(t, err.Error(), "secret-value-45")
	require.Contains(t, strings.ToLower(err.Error()), "read")
	require.Contains(t, strings.ToLower(err.Error()), "registered")
}
