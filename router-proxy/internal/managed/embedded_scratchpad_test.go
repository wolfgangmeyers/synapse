package managed

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedScratchpadSchema(t *testing.T) {
	schema := embeddedScratchpadSchema()
	require.NotNil(t, schema)
	require.Equal(t, "object", schema.Type)

	// Valid arguments
	require.True(t, schema.Check(map[string]any{"action": "append", "content": "hello"}))
	require.True(t, schema.Check(map[string]any{"action": "append", "content": "hello", "pinned": true}))
	require.True(t, schema.Check(map[string]any{"action": "fetch", "limit": 10}))
	require.True(t, schema.Check(map[string]any{"action": "paginate", "limit": 5, "offset": 10}))
	require.True(t, schema.Check(map[string]any{"action": "search", "query": "test", "limit": 5}))
	require.True(t, schema.Check(map[string]any{"action": "pin", "id": "entry-1"}))
	require.True(t, schema.Check(map[string]any{"action": "unpin", "id": "entry-1"}))
	require.True(t, schema.Check(map[string]any{"action": "delete", "id": "entry-1"}))

	// Invalid action
	require.False(t, schema.Check(map[string]any{"action": "unknown"}))
	// Extra unrecognized properties denied (AdditionalAllowed = false)
	require.False(t, schema.Check(map[string]any{"action": "fetch", "unsupported_field": 123}))
}

func TestEmbeddedScratchpadToolExecution(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	agentID := "worker-scratchpad-1"
	binding := store.EmbeddedBinding{AgentID: agentID, SessionID: "session-1", Epoch: 1, Generation: "gen-1"}
	owner := embeddedOwner{binding: binding, identity: "fixture:" + agentID}

	mgr := &Manager{store: db}
	tool := mgr.embeddedScratchpadTool(owner)
	require.Equal(t, "scratchpad", tool.Name)
	require.NotEmpty(t, tool.Description)

	// 1. Append unpinned note
	res, err := tool.Execute(ctx, "call-1", map[string]any{
		"action":  "append",
		"content": "First unpinned note",
	}, nil)
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	appendOut := res.Content[0].(ai.TextContent).Text
	require.Contains(t, appendOut, "Appended scratchpad entry")
	require.Contains(t, appendOut, "pinned: false")

	// 2. Append pinned note
	time.Sleep(10 * time.Millisecond)
	res, err = tool.Execute(ctx, "call-2", map[string]any{
		"action":  "append",
		"content": "Architecture decision milestone",
		"pinned":  true,
	}, nil)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].(ai.TextContent).Text, "pinned: true")

	// 3. Fetch recent
	res, err = tool.Execute(ctx, "call-3", map[string]any{
		"action": "fetch",
		"limit":  5,
	}, nil)
	require.NoError(t, err)
	fetchOut := res.Content[0].(ai.TextContent).Text
	require.Contains(t, fetchOut, "Fetched 2 scratchpad entries:")
	require.Contains(t, fetchOut, "[Pinned]")
	require.Contains(t, fetchOut, "Architecture decision milestone")
	require.Contains(t, fetchOut, "[Recent]")
	require.Contains(t, fetchOut, "First unpinned note")

	// 4. Paginate
	res, err = tool.Execute(ctx, "call-4", map[string]any{
		"action": "paginate",
		"limit":  1,
		"offset": 0,
	}, nil)
	require.NoError(t, err)
	pagOut := res.Content[0].(ai.TextContent).Text
	require.Contains(t, pagOut, "showing 1 of 2")

	// 5. Search
	res, err = tool.Execute(ctx, "call-5", map[string]any{
		"action": "search",
		"query":  "milestone",
	}, nil)
	require.NoError(t, err)
	searchOut := res.Content[0].(ai.TextContent).Text
	require.Contains(t, searchOut, "Found 1 scratchpad entries matching \"milestone\"")
	require.Contains(t, searchOut, "Architecture decision milestone")

	// 6. Pin / Unpin
	entries, err := db.FetchScratchpadRecent(ctx, agentID, 5)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	unpinnedEntry := entries[1] // the unpinned one
	require.False(t, unpinnedEntry.Pinned)

	res, err = tool.Execute(ctx, "call-6", map[string]any{
		"action": "pin",
		"id":     unpinnedEntry.ID,
	}, nil)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].(ai.TextContent).Text, "pinned.")

	// Verify it's now pinned
	entriesAfterPin, err := db.FetchScratchpadRecent(ctx, agentID, 5)
	require.NoError(t, err)
	for _, e := range entriesAfterPin {
		require.True(t, e.Pinned)
	}

	// Unpin it
	res, err = tool.Execute(ctx, "call-7", map[string]any{
		"action": "unpin",
		"id":     unpinnedEntry.ID,
	}, nil)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].(ai.TextContent).Text, "unpinned.")

	// 7. Delete entry
	res, err = tool.Execute(ctx, "call-8", map[string]any{
		"action": "delete",
		"id":     unpinnedEntry.ID,
	}, nil)
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	require.Equal(t, fmt.Sprintf("Deleted scratchpad entry %s", unpinnedEntry.ID), res.Content[0].(ai.TextContent).Text)

	// Verify unpinnedEntry is deleted and cannot be fetched
	entriesAfterDelete, err := db.FetchScratchpadRecent(ctx, agentID, 10)
	require.NoError(t, err)
	require.Len(t, entriesAfterDelete, 1)
	require.NotEqual(t, unpinnedEntry.ID, entriesAfterDelete[0].ID)

	// 8. 4000 character length limit rejection
	tooLongContent := strings.Repeat("a", 4001)
	_, err = tool.Execute(ctx, "call-err-long", map[string]any{
		"action":  "append",
		"content": tooLongContent,
	}, nil)
	require.Error(t, err)
	require.Equal(t, fmt.Sprintf("scratchpad entry exceeds maximum allowed length of 4000 characters (got %d); save large content to a file and record the path", len(tooLongContent)), err.Error())

	// 9. Error handling
	_, err = tool.Execute(ctx, "call-err-1", map[string]any{
		"action": "append",
		// missing content
	}, nil)
	require.Error(t, err)

	_, err = tool.Execute(ctx, "call-err-2", map[string]any{
		"action": "pin",
		// missing id
	}, nil)
	require.Error(t, err)

	_, err = tool.Execute(ctx, "call-err-delete-noid", map[string]any{
		"action": "delete",
		// missing id
	}, nil)
	require.Error(t, err)

	_, err = tool.Execute(ctx, "call-err-delete-notfound", map[string]any{
		"action": "delete",
		"id":     "non-existent-id",
	}, nil)
	require.Error(t, err)

	_, err = tool.Execute(ctx, "call-err-3", map[string]any{
		"action": "unknown_action",
	}, nil)
	require.Error(t, err)
}

func TestRegisterEmbeddedToolsAndManifest(t *testing.T) {
	mgr := &Manager{}
	binding := store.EmbeddedBinding{AgentID: "agent-reg", SessionID: "session-1", Epoch: 1, Generation: "gen-1"}
	owner := embeddedOwner{binding: binding, identity: "fixture:agent-reg"}

	tools := mgr.registerEmbeddedTools(owner)
	require.NotEmpty(t, tools)
	foundScratchpad := false
	for _, tool := range tools {
		if tool.Name == "scratchpad" {
			foundScratchpad = true
		}
	}
	require.True(t, foundScratchpad, "registerEmbeddedTools must include scratchpad")

	manifestTools := embeddedToolsForManifest()
	require.Contains(t, manifestTools, "scratchpad")
	require.Contains(t, manifestTools, "context_reset")
	require.Contains(t, manifestTools, "reset")
}
