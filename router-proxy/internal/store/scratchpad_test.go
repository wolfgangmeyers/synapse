package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestScratchpadCRUD(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(ctx, dbPath, Options{})
	require.NoError(t, err)
	defer db.Close()

	agentID := "agent-crud-1"

	// 1. Append entry (pinned = false)
	entry1, err := db.AppendScratchpadEntry(ctx, agentID, "First scratchpad note", false)
	require.NoError(t, err)
	require.NotEmpty(t, entry1.ID)
	require.Equal(t, agentID, entry1.AgentID)
	require.Equal(t, "First scratchpad note", entry1.Content)
	require.False(t, entry1.Pinned)
	require.NotEmpty(t, entry1.CreatedAt)
	require.Equal(t, entry1.CreatedAt, entry1.UpdatedAt)

	// 2. Append entry (pinned = true)
	entry2, err := db.AppendScratchpadEntry(ctx, agentID, "Important pinned note", true)
	require.NoError(t, err)
	require.NotEmpty(t, entry2.ID)
	require.NotEqual(t, entry1.ID, entry2.ID)
	require.True(t, entry2.Pinned)

	// 3. Pin entry1
	err = db.SetScratchpadPinned(ctx, agentID, entry1.ID, true)
	require.NoError(t, err)

	// Verify entry1 is now pinned
	recent, err := db.FetchScratchpadRecent(ctx, agentID, 10)
	require.NoError(t, err)
	require.Len(t, recent, 2)
	for _, e := range recent {
		require.True(t, e.Pinned)
	}

	// 4. Unpin entry2
	err = db.SetScratchpadPinned(ctx, agentID, entry2.ID, false)
	require.NoError(t, err)

	recent, err = db.FetchScratchpadRecent(ctx, agentID, 10)
	require.NoError(t, err)
	require.Len(t, recent, 2)
	// First entry in recent should be pinned (entry1)
	require.Equal(t, entry1.ID, recent[0].ID)
	require.True(t, recent[0].Pinned)
	// Second entry in recent should be unpinned (entry2)
	require.Equal(t, entry2.ID, recent[1].ID)
	require.False(t, recent[1].Pinned)

	// 5. Setting pin on non-existent entry returns error
	err = db.SetScratchpadPinned(ctx, agentID, "non-existent-id", true)
	require.Error(t, err)

	// 6. Setting pin for wrong agent returns error
	err = db.SetScratchpadPinned(ctx, "different-agent", entry1.ID, false)
	require.Error(t, err)

	// 7. Validation errors
	_, err = db.AppendScratchpadEntry(ctx, "", "content", false)
	require.Error(t, err)
	err = db.SetScratchpadPinned(ctx, "", entry1.ID, false)
	require.Error(t, err)
	err = db.SetScratchpadPinned(ctx, agentID, "", false)
	require.Error(t, err)
}

func TestScratchpadFetchRecent(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(ctx, dbPath, Options{})
	require.NoError(t, err)
	defer db.Close()

	agentID := "agent-fetch-1"
	otherAgent := "agent-fetch-2"

	// Create entries with distinct timestamps
	e1, err := db.AppendScratchpadEntry(ctx, agentID, "Note 1 - unpinned", false)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)

	e2, err := db.AppendScratchpadEntry(ctx, agentID, "Note 2 - pinned", true)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)

	e3, err := db.AppendScratchpadEntry(ctx, agentID, "Note 3 - unpinned", false)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)

	e4, err := db.AppendScratchpadEntry(ctx, agentID, "Note 4 - pinned", true)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)

	e5, err := db.AppendScratchpadEntry(ctx, agentID, "Note 5 - unpinned", false)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)

	// Other agent entry (should be isolated)
	_, err = db.AppendScratchpadEntry(ctx, otherAgent, "Other agent note", true)
	require.NoError(t, err)

	// Fetch with limit = 1 (1 most recent unpinned, but ALL pinned)
	entries, err := db.FetchScratchpadRecent(ctx, agentID, 1)
	require.NoError(t, err)
	// Pinned: e4, e2 (both retained!). Unpinned (limit 1): e5.
	require.Len(t, entries, 3)
	require.Equal(t, e4.ID, entries[0].ID)
	require.True(t, entries[0].Pinned)
	require.Equal(t, e2.ID, entries[1].ID)
	require.True(t, entries[1].Pinned)
	require.Equal(t, e5.ID, entries[2].ID)
	require.False(t, entries[2].Pinned)

	// Fetch with limit = 2 (2 most recent unpinned: e5, e3, plus both pinned: e4, e2)
	entries, err = db.FetchScratchpadRecent(ctx, agentID, 2)
	require.NoError(t, err)
	require.Len(t, entries, 4)
	require.Equal(t, e4.ID, entries[0].ID)
	require.Equal(t, e2.ID, entries[1].ID)
	require.Equal(t, e5.ID, entries[2].ID)
	require.Equal(t, e3.ID, entries[3].ID)

	// Fetch with limit = 10 (all entries for this agent: 2 pinned + 3 unpinned)
	entries, err = db.FetchScratchpadRecent(ctx, agentID, 10)
	require.NoError(t, err)
	require.Len(t, entries, 5)
	require.Equal(t, e4.ID, entries[0].ID)
	require.Equal(t, e2.ID, entries[1].ID)
	require.Equal(t, e5.ID, entries[2].ID)
	require.Equal(t, e3.ID, entries[3].ID)
	require.Equal(t, e1.ID, entries[4].ID)

	// Verify other agent has only their entry
	otherEntries, err := db.FetchScratchpadRecent(ctx, otherAgent, 5)
	require.NoError(t, err)
	require.Len(t, otherEntries, 1)
	require.Equal(t, "Other agent note", otherEntries[0].Content)

	// Empty result for unknown agent
	emptyEntries, err := db.FetchScratchpadRecent(ctx, "unknown-agent", 5)
	require.NoError(t, err)
	require.Empty(t, emptyEntries)
}

func TestScratchpadPaginate(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(ctx, dbPath, Options{})
	require.NoError(t, err)
	defer db.Close()

	agentID := "agent-page-1"
	var ids []string
	for i := 1; i <= 5; i++ {
		e, err := db.AppendScratchpadEntry(ctx, agentID, "Paginated Note", false)
		require.NoError(t, err)
		ids = append(ids, e.ID)
		time.Sleep(10 * time.Millisecond)
	}

	// Page 1: limit 2, offset 0 -> entries 5 and 4 (recent first)
	p1, total, err := db.PaginateScratchpad(ctx, agentID, 2, 0)
	require.NoError(t, err)
	require.Equal(t, 5, total)
	require.Len(t, p1, 2)
	require.Equal(t, ids[4], p1[0].ID)
	require.Equal(t, ids[3], p1[1].ID)

	// Page 2: limit 2, offset 2 -> entries 3 and 2
	p2, total, err := db.PaginateScratchpad(ctx, agentID, 2, 2)
	require.NoError(t, err)
	require.Equal(t, 5, total)
	require.Len(t, p2, 2)
	require.Equal(t, ids[2], p2[0].ID)
	require.Equal(t, ids[1], p2[1].ID)

	// Page 3: limit 2, offset 4 -> entry 1
	p3, total, err := db.PaginateScratchpad(ctx, agentID, 2, 4)
	require.NoError(t, err)
	require.Equal(t, 5, total)
	require.Len(t, p3, 1)
	require.Equal(t, ids[0], p3[0].ID)

	// Page 4: offset beyond total
	p4, total, err := db.PaginateScratchpad(ctx, agentID, 2, 10)
	require.NoError(t, err)
	require.Equal(t, 5, total)
	require.Empty(t, p4)
}

func TestScratchpadSearch(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(ctx, dbPath, Options{})
	require.NoError(t, err)
	defer db.Close()

	agentID := "agent-search-1"

	e1, err := db.AppendScratchpadEntry(ctx, agentID, "Task Alpha started with initial plan", false)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)

	_, err = db.AppendScratchpadEntry(ctx, agentID, "Refactoring database migrations for performance", false)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)

	e3, err := db.AppendScratchpadEntry(ctx, agentID, "Task Alpha completed successfully and verified", false)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)

	_, err = db.AppendScratchpadEntry(ctx, agentID, "Unrelated item about deployment", false)
	require.NoError(t, err)

	// Search for "Alpha" (substring search): should match e3 and e1, ordered recent first (e3, then e1)
	results, err := db.SearchScratchpad(ctx, agentID, "Alpha", 10)
	require.NoError(t, err)
	require.Len(t, results, 2)
	require.Equal(t, e3.ID, results[0].ID)
	require.Equal(t, e1.ID, results[1].ID)

	// Search with limit 1
	results, err = db.SearchScratchpad(ctx, agentID, "Alpha", 1)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, e3.ID, results[0].ID)

	// Non-matching search query
	results, err = db.SearchScratchpad(ctx, agentID, "NonExistentTermXYZ", 10)
	require.NoError(t, err)
	require.Empty(t, results)

	// Other agent isolation
	otherResults, err := db.SearchScratchpad(ctx, "other-agent", "Alpha", 10)
	require.NoError(t, err)
	require.Empty(t, otherResults)
}
