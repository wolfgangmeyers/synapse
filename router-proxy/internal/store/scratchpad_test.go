package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
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
	recent, total, err := db.FetchScratchpadRecent(ctx, agentID, 10)
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, recent, 2)
	for _, e := range recent {
		require.True(t, e.Pinned)
	}

	// 4. Unpin entry2
	err = db.SetScratchpadPinned(ctx, agentID, entry2.ID, false)
	require.NoError(t, err)

	recent, total, err = db.FetchScratchpadRecent(ctx, agentID, 10)
	require.NoError(t, err)
	require.Equal(t, 2, total)
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
	entries, total, err := db.FetchScratchpadRecent(ctx, agentID, 1)
	require.NoError(t, err)
	require.Equal(t, 5, total)
	// Pinned: e4, e2 (both retained!). Unpinned (limit 1): e5.
	require.Len(t, entries, 3)
	require.Equal(t, e4.ID, entries[0].ID)
	require.True(t, entries[0].Pinned)
	require.Equal(t, e2.ID, entries[1].ID)
	require.True(t, entries[1].Pinned)
	require.Equal(t, e5.ID, entries[2].ID)
	require.False(t, entries[2].Pinned)

	// Fetch with limit = 2 (2 most recent unpinned: e5, e3, plus both pinned: e4, e2)
	entries, total, err = db.FetchScratchpadRecent(ctx, agentID, 2)
	require.NoError(t, err)
	require.Equal(t, 5, total)
	require.Len(t, entries, 4)
	require.Equal(t, e4.ID, entries[0].ID)
	require.Equal(t, e2.ID, entries[1].ID)
	require.Equal(t, e5.ID, entries[2].ID)
	require.Equal(t, e3.ID, entries[3].ID)

	// Fetch with limit = 10 (all entries for this agent: 2 pinned + 3 unpinned)
	entries, total, err = db.FetchScratchpadRecent(ctx, agentID, 10)
	require.NoError(t, err)
	require.Equal(t, 5, total)
	require.Len(t, entries, 5)
	require.Equal(t, e4.ID, entries[0].ID)
	require.Equal(t, e2.ID, entries[1].ID)
	require.Equal(t, e5.ID, entries[2].ID)
	require.Equal(t, e3.ID, entries[3].ID)
	require.Equal(t, e1.ID, entries[4].ID)

	// Verify other agent has only their entry
	otherEntries, totalOther, err := db.FetchScratchpadRecent(ctx, otherAgent, 5)
	require.NoError(t, err)
	require.Equal(t, 1, totalOther)
	require.Len(t, otherEntries, 1)
	require.Equal(t, "Other agent note", otherEntries[0].Content)

	// Empty result for unknown agent
	emptyEntries, totalEmpty, err := db.FetchScratchpadRecent(ctx, "unknown-agent", 5)
	require.NoError(t, err)
	require.Equal(t, 0, totalEmpty)
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

func TestScratchpadDelete(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(ctx, dbPath, Options{})
	require.NoError(t, err)
	defer db.Close()

	agentID := "agent-del-1"
	otherAgent := "agent-del-2"

	entry, err := db.AppendScratchpadEntry(ctx, agentID, "Note to be deleted", false)
	require.NoError(t, err)
	require.NotEmpty(t, entry.ID)

	// Other agent cannot delete
	err = db.DeleteScratchpadEntry(ctx, otherAgent, entry.ID)
	require.Error(t, err)
	require.Equal(t, "scratchpad entry not found", err.Error())

	// Successful deletion
	err = db.DeleteScratchpadEntry(ctx, agentID, entry.ID)
	require.NoError(t, err)

	// Verify entry is removed and cannot be fetched
	entries, total, err := db.FetchScratchpadRecent(ctx, agentID, 10)
	require.NoError(t, err)
	require.Equal(t, 0, total)
	require.Empty(t, entries)

	// Deleting already deleted entry returns not found error
	err = db.DeleteScratchpadEntry(ctx, agentID, entry.ID)
	require.Error(t, err)
	require.Equal(t, "scratchpad entry not found", err.Error())

	// Validation errors
	err = db.DeleteScratchpadEntry(ctx, "", entry.ID)
	require.Error(t, err)
	err = db.DeleteScratchpadEntry(ctx, agentID, "")
	require.Error(t, err)
}

func TestScratchpadAppendLengthLimit(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(ctx, dbPath, Options{})
	require.NoError(t, err)
	defer db.Close()

	agentID := "agent-len-1"

	// 4000 characters is allowed
	exactContent := strings.Repeat("x", 4000)
	e, err := db.AppendScratchpadEntry(ctx, agentID, exactContent, false)
	require.NoError(t, err)
	require.Equal(t, exactContent, e.Content)

	// 4001 characters is rejected
	tooLong := strings.Repeat("x", 4001)
	_, err = db.AppendScratchpadEntry(ctx, agentID, tooLong, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "scratchpad entry exceeds maximum allowed length of 4000 characters (got 4001); save large content to a file and record the path")
}

func TestCountScratchpadEntries(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(ctx, dbPath, Options{})
	require.NoError(t, err)
	defer db.Close()

	agent1 := "agent-count-1"
	agent2 := "agent-count-2"

	// Validation: empty agentID returns error
	_, err = db.CountScratchpadEntries(ctx, "")
	require.Error(t, err)
	_, err = db.CountScratchpadEntries(ctx, "   ")
	require.Error(t, err)

	// Initially 0 for both agents
	count, err := db.CountScratchpadEntries(ctx, agent1)
	require.NoError(t, err)
	require.Equal(t, 0, count)

	// Append entry to agent1
	e1, err := db.AppendScratchpadEntry(ctx, agent1, "Note 1", false)
	require.NoError(t, err)

	count, err = db.CountScratchpadEntries(ctx, agent1)
	require.NoError(t, err)
	require.Equal(t, 1, count)

	// agent2 still 0
	count2, err := db.CountScratchpadEntries(ctx, agent2)
	require.NoError(t, err)
	require.Equal(t, 0, count2)

	// Append more entries to agent1 (both pinned and unpinned)
	_, err = db.AppendScratchpadEntry(ctx, agent1, "Note 2", true)
	require.NoError(t, err)
	_, err = db.AppendScratchpadEntry(ctx, agent1, "Note 3", false)
	require.NoError(t, err)

	count, err = db.CountScratchpadEntries(ctx, agent1)
	require.NoError(t, err)
	require.Equal(t, 3, count)

	// Append to agent2
	_, err = db.AppendScratchpadEntry(ctx, agent2, "Agent2 Note", false)
	require.NoError(t, err)

	count2, err = db.CountScratchpadEntries(ctx, agent2)
	require.NoError(t, err)
	require.Equal(t, 1, count2)

	// Delete from agent1 decreases count
	err = db.DeleteScratchpadEntry(ctx, agent1, e1.ID)
	require.NoError(t, err)

	count, err = db.CountScratchpadEntries(ctx, agent1)
	require.NoError(t, err)
	require.Equal(t, 2, count)
}

func TestPinnedEntryLimit(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(ctx, dbPath, Options{})
	require.NoError(t, err)
	defer db.Close()

	agentID := "agent-pin-limit"
	otherAgent := "agent-pin-other"

	// Append 20 pinned entries for agentID (all should succeed)
	var pinnedIDs []string
	for i := 1; i <= 20; i++ {
		e, err := db.AppendScratchpadEntry(ctx, agentID, fmt.Sprintf("Pinned note %d", i), true)
		require.NoError(t, err)
		pinnedIDs = append(pinnedIDs, e.ID)
	}

	// 21st pinned append should fail with ErrPinnedLimitReached
	_, err = db.AppendScratchpadEntry(ctx, agentID, "21st pinned note", true)
	require.Error(t, err)
	require.Equal(t, ErrPinnedLimitReached, err)
	require.Equal(t, "pinned entry limit reached (maximum 20 pinned entries); please unpin an obsolete entry before pinning a new one", err.Error())

	// Append unpinned entry should succeed
	unpinnedEntry, err := db.AppendScratchpadEntry(ctx, agentID, "Unpinned note", false)
	require.NoError(t, err)

	// Setting pin on that unpinned entry should fail
	err = db.SetScratchpadPinned(ctx, agentID, unpinnedEntry.ID, true)
	require.Error(t, err)
	require.Equal(t, ErrPinnedLimitReached, err)

	// Setting pin on an already-pinned entry should not fail
	err = db.SetScratchpadPinned(ctx, agentID, pinnedIDs[0], true)
	require.NoError(t, err)

	// Unpin one entry
	err = db.SetScratchpadPinned(ctx, agentID, pinnedIDs[0], false)
	require.NoError(t, err)

	// Now pinning the unpinned entry should succeed
	err = db.SetScratchpadPinned(ctx, agentID, unpinnedEntry.ID, true)
	require.NoError(t, err)

	// Pinned count is back to 20, so pinning pinnedIDs[0] again should fail
	err = db.SetScratchpadPinned(ctx, agentID, pinnedIDs[0], true)
	require.Error(t, err)
	require.Equal(t, ErrPinnedLimitReached, err)

	// Other agent is isolated and can pin entries
	otherEntry, err := db.AppendScratchpadEntry(ctx, otherAgent, "Other pinned", true)
	require.NoError(t, err)
	require.True(t, otherEntry.Pinned)
}

func TestScratchpadBatchCRUD(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(ctx, dbPath, Options{})
	require.NoError(t, err)
	defer db.Close()

	agentID := "agent-batch-1"
	otherAgent := "agent-batch-2"

	// Create 5 unpinned entries
	var entries []*ScratchpadEntry
	for i := 1; i <= 5; i++ {
		e, err := db.AppendScratchpadEntry(ctx, agentID, fmt.Sprintf("Note %d", i), false)
		require.NoError(t, err)
		entries = append(entries, e)
	}

	// 1. Batch pin entries 0, 1, 2
	pinIDs := []string{entries[0].ID, entries[1].ID, entries[2].ID}
	updated, err := db.SetScratchpadEntriesPinned(ctx, agentID, pinIDs, true)
	require.NoError(t, err)
	require.Equal(t, pinIDs, updated)

	// Verify they are pinned
	for _, id := range pinIDs {
		recent, _, err := db.FetchScratchpadRecent(ctx, agentID, 10)
		require.NoError(t, err)
		for _, e := range recent {
			if e.ID == id {
				require.True(t, e.Pinned)
			}
		}
	}

	// 2. Batch unpin entries 0 and 1
	unpinIDs := []string{entries[0].ID, entries[1].ID}
	updated, err = db.SetScratchpadEntriesPinned(ctx, agentID, unpinIDs, false)
	require.NoError(t, err)
	require.Equal(t, unpinIDs, updated)

	// 3. Batch pin limit enforcement (already have entry 2 pinned, creating 18 more makes 19 pinned)
	for i := 6; i <= 23; i++ {
		_, err := db.AppendScratchpadEntry(ctx, agentID, fmt.Sprintf("Note %d", i), true)
		require.NoError(t, err)
	}
	// Currently 19 pinned. Pinning 2 more entries should fail limit (20).
	_, err = db.SetScratchpadEntriesPinned(ctx, agentID, []string{entries[0].ID, entries[1].ID}, true)
	require.ErrorIs(t, err, ErrPinnedLimitReached)

	// Pinning just 1 more entry should succeed (19 + 1 = 20)
	updated, err = db.SetScratchpadEntriesPinned(ctx, agentID, []string{entries[0].ID}, true)
	require.NoError(t, err)
	require.Equal(t, []string{entries[0].ID}, updated)

	// 4. Batch delete entries 3 and 4
	deleteIDs := []string{entries[3].ID, entries[4].ID}
	deleted, err := db.DeleteScratchpadEntries(ctx, agentID, deleteIDs)
	require.NoError(t, err)
	require.Equal(t, deleteIDs, deleted)

	// Deleting again should return not found error
	_, err = db.DeleteScratchpadEntries(ctx, agentID, deleteIDs)
	require.Error(t, err)
	require.Equal(t, "scratchpad entry not found", err.Error())

	// Deleting with other agent should return not found error
	_, err = db.DeleteScratchpadEntries(ctx, otherAgent, []string{entries[0].ID})
	require.Error(t, err)
	require.Equal(t, "scratchpad entry not found", err.Error())

	// Validation
	_, err = db.SetScratchpadEntriesPinned(ctx, "", []string{entries[0].ID}, false)
	require.Error(t, err)
	_, err = db.SetScratchpadEntriesPinned(ctx, agentID, []string{}, false)
	require.Error(t, err)
	_, err = db.DeleteScratchpadEntries(ctx, "", []string{entries[0].ID})
	require.Error(t, err)
	_, err = db.DeleteScratchpadEntries(ctx, agentID, []string{})
	require.Error(t, err)
}

func TestTruncateScratchpadEntries(t *testing.T) {
	ctx := context.Background()

	t.Run("TruncateByBeforeID_UnpinnedTarget", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "test.db")
		db, err := Open(ctx, dbPath, Options{})
		require.NoError(t, err)
		defer db.Close()

		agentID := "agent-truncate-1"

		// Create 6 unpinned entries (e1..e6) and 2 pinned entries (p1, p2) with time.Sleep
		e1, err := db.AppendScratchpadEntry(ctx, agentID, "e1", false)
		require.NoError(t, err)
		time.Sleep(10 * time.Millisecond)

		e2, err := db.AppendScratchpadEntry(ctx, agentID, "e2", false)
		require.NoError(t, err)
		time.Sleep(10 * time.Millisecond)

		p1, err := db.AppendScratchpadEntry(ctx, agentID, "p1", true)
		require.NoError(t, err)
		time.Sleep(10 * time.Millisecond)

		e3, err := db.AppendScratchpadEntry(ctx, agentID, "e3", false)
		require.NoError(t, err)
		time.Sleep(10 * time.Millisecond)

		e4, err := db.AppendScratchpadEntry(ctx, agentID, "e4", false)
		require.NoError(t, err)
		time.Sleep(10 * time.Millisecond)

		p2, err := db.AppendScratchpadEntry(ctx, agentID, "p2", true)
		require.NoError(t, err)
		time.Sleep(10 * time.Millisecond)

		e5, err := db.AppendScratchpadEntry(ctx, agentID, "e5", false)
		require.NoError(t, err)
		time.Sleep(10 * time.Millisecond)

		e6, err := db.AppendScratchpadEntry(ctx, agentID, "e6", false)
		require.NoError(t, err)

		// Pick e4 as BeforeID
		deletedCount, err := db.TruncateScratchpadEntries(ctx, agentID, ScratchpadTruncateOptions{
			BeforeID: e4.ID,
		})
		require.NoError(t, err)
		require.Equal(t, 3, deletedCount) // e1, e2, e3 deleted

		// Check remaining entries
		entries, total, err := db.PaginateScratchpad(ctx, agentID, 100, 0)
		require.NoError(t, err)
		require.Equal(t, 5, total)
		require.Len(t, entries, 5)

		remainingIDs := make(map[string]bool)
		for _, entry := range entries {
			remainingIDs[entry.ID] = true
			if entry.ID == p1.ID || entry.ID == p2.ID {
				require.True(t, entry.Pinned)
			}
		}

		require.False(t, remainingIDs[e1.ID])
		require.False(t, remainingIDs[e2.ID])
		require.False(t, remainingIDs[e3.ID])

		require.True(t, remainingIDs[p1.ID])
		require.True(t, remainingIDs[e4.ID])
		require.True(t, remainingIDs[p2.ID])
		require.True(t, remainingIDs[e5.ID])
		require.True(t, remainingIDs[e6.ID])
	})

	t.Run("TruncateByBeforeID_PinnedTarget", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "test.db")
		db, err := Open(ctx, dbPath, Options{})
		require.NoError(t, err)
		defer db.Close()

		agentID := "agent-truncate-pinned-target"

		e1, err := db.AppendScratchpadEntry(ctx, agentID, "e1", false)
		require.NoError(t, err)
		time.Sleep(10 * time.Millisecond)

		p1, err := db.AppendScratchpadEntry(ctx, agentID, "p1", true)
		require.NoError(t, err)
		time.Sleep(10 * time.Millisecond)

		e2, err := db.AppendScratchpadEntry(ctx, agentID, "e2", false)
		require.NoError(t, err)

		// Pick p1 as BeforeID
		deletedCount, err := db.TruncateScratchpadEntries(ctx, agentID, ScratchpadTruncateOptions{
			BeforeID: p1.ID,
		})
		require.NoError(t, err)
		require.Equal(t, 1, deletedCount) // e1 deleted

		entries, total, err := db.PaginateScratchpad(ctx, agentID, 100, 0)
		require.NoError(t, err)
		require.Equal(t, 2, total)
		require.Len(t, entries, 2)

		remainingIDs := make(map[string]bool)
		for _, entry := range entries {
			remainingIDs[entry.ID] = true
		}

		require.False(t, remainingIDs[e1.ID])
		require.True(t, remainingIDs[p1.ID])
		require.True(t, remainingIDs[e2.ID])
	})

	t.Run("TruncateByKeepLast", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "test.db")
		db, err := Open(ctx, dbPath, Options{})
		require.NoError(t, err)
		defer db.Close()

		agentID := "agent-truncate-keep-last"

		unpinned := make([]*ScratchpadEntry, 10)
		for i := 0; i < 10; i++ {
			entry, err := db.AppendScratchpadEntry(ctx, agentID, fmt.Sprintf("unpinned-%d", i), false)
			require.NoError(t, err)
			unpinned[i] = entry
			time.Sleep(5 * time.Millisecond)
		}

		pinned := make([]*ScratchpadEntry, 3)
		for i := 0; i < 3; i++ {
			entry, err := db.AppendScratchpadEntry(ctx, agentID, fmt.Sprintf("pinned-%d", i), true)
			require.NoError(t, err)
			pinned[i] = entry
			time.Sleep(5 * time.Millisecond)
		}

		deletedCount, err := db.TruncateScratchpadEntries(ctx, agentID, ScratchpadTruncateOptions{
			KeepLast: 3,
		})
		require.NoError(t, err)
		require.Equal(t, 7, deletedCount)

		entries, total, err := db.PaginateScratchpad(ctx, agentID, 100, 0)
		require.NoError(t, err)
		require.Equal(t, 6, total) // 3 pinned + 3 unpinned
		require.Len(t, entries, 6)

		remainingIDs := make(map[string]bool)
		for _, entry := range entries {
			remainingIDs[entry.ID] = true
		}

		// First 7 unpinned deleted
		for i := 0; i < 7; i++ {
			require.False(t, remainingIDs[unpinned[i].ID])
		}
		// Last 3 unpinned kept
		for i := 7; i < 10; i++ {
			require.True(t, remainingIDs[unpinned[i].ID])
		}
		// All 3 pinned kept
		for i := 0; i < 3; i++ {
			require.True(t, remainingIDs[pinned[i].ID])
		}
	})

	t.Run("TruncateByKeepLast_FewerThanLimit", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "test.db")
		db, err := Open(ctx, dbPath, Options{})
		require.NoError(t, err)
		defer db.Close()

		agentID := "agent-truncate-fewer"

		for i := 0; i < 3; i++ {
			_, err := db.AppendScratchpadEntry(ctx, agentID, fmt.Sprintf("unpinned-%d", i), false)
			require.NoError(t, err)
			time.Sleep(5 * time.Millisecond)
		}

		deletedCount, err := db.TruncateScratchpadEntries(ctx, agentID, ScratchpadTruncateOptions{
			KeepLast: 5,
		})
		require.NoError(t, err)
		require.Equal(t, 0, deletedCount)

		_, total, err := db.PaginateScratchpad(ctx, agentID, 100, 0)
		require.NoError(t, err)
		require.Equal(t, 3, total)
	})

	t.Run("MutualExclusivityValidation", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "test.db")
		db, err := Open(ctx, dbPath, Options{})
		require.NoError(t, err)
		defer db.Close()

		agentID := "agent-validation"

		// Both BeforeID and KeepLast
		_, err = db.TruncateScratchpadEntries(ctx, agentID, ScratchpadTruncateOptions{
			BeforeID: "some-id",
			KeepLast: 3,
		})
		require.Error(t, err)
		require.ErrorIs(t, err, ErrInvalidParameter)
		require.Contains(t, err.Error(), "cannot specify both before_id and keep_last")

		// Neither BeforeID nor KeepLast
		_, err = db.TruncateScratchpadEntries(ctx, agentID, ScratchpadTruncateOptions{})
		require.Error(t, err)
		require.ErrorIs(t, err, ErrInvalidParameter)
		require.Contains(t, err.Error(), "must specify either before_id or keep_last greater than 0")

		// KeepLast < 0
		_, err = db.TruncateScratchpadEntries(ctx, agentID, ScratchpadTruncateOptions{
			KeepLast: -1,
		})
		require.Error(t, err)
		require.ErrorIs(t, err, ErrInvalidParameter)
		require.Contains(t, err.Error(), "keep_last must be greater than 0")

		// Empty agentID
		_, err = db.TruncateScratchpadEntries(ctx, "", ScratchpadTruncateOptions{
			KeepLast: 5,
		})
		require.Error(t, err)
		require.ErrorIs(t, err, ErrInvalidParameter)
	})

	t.Run("NonExistentOrCrossAgentBeforeID", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "test.db")
		db, err := Open(ctx, dbPath, Options{})
		require.NoError(t, err)
		defer db.Close()

		agentA := "agent-A"
		agentB := "agent-B"

		// Non-existent BeforeID
		_, err = db.TruncateScratchpadEntries(ctx, agentA, ScratchpadTruncateOptions{
			BeforeID: "non-existent",
		})
		require.Error(t, err)
		require.ErrorIs(t, err, sql.ErrNoRows)

		// Create entry for agentB
		entryB, err := db.AppendScratchpadEntry(ctx, agentB, "agentB note", false)
		require.NoError(t, err)

		// Pass agentB's entry ID for agentA
		_, err = db.TruncateScratchpadEntries(ctx, agentA, ScratchpadTruncateOptions{
			BeforeID: entryB.ID,
		})
		require.Error(t, err)
		require.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("CrossAgentIsolation", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "test.db")
		db, err := Open(ctx, dbPath, Options{})
		require.NoError(t, err)
		defer db.Close()

		agentA := "agent-isolation-A"
		agentB := "agent-isolation-B"

		// Create entries for agentA and agentB
		for i := 0; i < 5; i++ {
			_, err := db.AppendScratchpadEntry(ctx, agentA, fmt.Sprintf("A-%d", i), false)
			require.NoError(t, err)
			_, err = db.AppendScratchpadEntry(ctx, agentB, fmt.Sprintf("B-%d", i), false)
			require.NoError(t, err)
			time.Sleep(5 * time.Millisecond)
		}

		// Truncate agentA keeping last 2
		deletedCount, err := db.TruncateScratchpadEntries(ctx, agentA, ScratchpadTruncateOptions{
			KeepLast: 2,
		})
		require.NoError(t, err)
		require.Equal(t, 3, deletedCount)

		// Verify agentA entries
		_, totalA, err := db.PaginateScratchpad(ctx, agentA, 100, 0)
		require.NoError(t, err)
		require.Equal(t, 2, totalA)

		// Verify agentB entries are completely untouched
		entriesB, totalB, err := db.PaginateScratchpad(ctx, agentB, 100, 0)
		require.NoError(t, err)
		require.Equal(t, 5, totalB)
		require.Len(t, entriesB, 5)
	})
}

