package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

// TestManagedHistoryRetentionBehavioralFunctional exercises the real listening
// router, SQLite store, HTTP message/history handlers, and UI-live history
// projection. The manager's hidden mail.send event has no HTTP route in this
// transport. One message per pair crosses POST /v1/messages, including the
// manager authorization boundary. The remaining rows use Store.Enqueue, the
// exact router-owned persistence ingress called after that HTTP validation.
func TestManagedHistoryRetentionBehavioralFunctional(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2030, 1, 31, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start.Add(-31 * 24 * time.Hour))
	database := t.TempDir() + "/router.db"

	durable, err := store.Open(ctx, database, store.Options{Clock: clock, MaxQueuedMessages: 1000, MaxQueuedBytes: 2 * 1024 * 1024})
	require.NoError(t, err)
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	manager, err := managed.NewManager(managed.ManagerOptions{Store: durable, Config: managed.Config{Enabled: false}, Manifest: manifest})
	require.NoError(t, err)
	// Reserve one durable managed lifetime for each pair. This is the public
	// store ingress that creates the history boundary used after restart; the
	// test does not need to launch a child process for its history projection.
	for _, agentID := range []string{"quiet-a", "quiet-b"} {
		require.NoError(t, durable.ReserveManagedLaunchWithLimit(ctx, store.ManagedAgentRecord{
			AgentID: agentID, Name: agentID, Cwd: t.TempDir(), SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"},
			ManifestRevision: manifest.Revision, ManifestHash: manifest.CanonicalSHA256,
		}, store.ManagedOperation{
			OperationID: "retention-launch-" + agentID, Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "retention-request-" + agentID,
			CanonicalBodyHash: "retention-fixture", ResponseStatus: http.StatusAccepted, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`),
		}, 100))
		require.NoError(t, durable.MarkManagedStopped(ctx, agentID))
	}
	router, err := New(Options{Store: durable, Clock: clock, Host: "127.0.0.1", Port: 0, Managed: manager})
	require.NoError(t, err)
	require.NoError(t, router.Listen(ctx))

	closeFirst := func() {
		require.NoError(t, router.Close(ctx))
		require.NoError(t, durable.Close())
	}
	t.Cleanup(func() {
		// The explicit close below makes restart ordering visible. This cleanup
		// remains for assertion failures before that boundary.
		_ = router.Close(ctx)
		_ = durable.Close()
	})

	httpURL, wsURL := router.URLs()
	require.NotEmpty(t, httpURL)
	live := dialRetentionUILive(t, wsURL)
	defer live.Close(websocket.StatusNormalClosure, "retention test complete")
	readRetentionUILiveFrame(t, live) // managed_agents_snapshot
	readRetentionUILiveFrame(t, live) // managed_children_snapshot

	require.NoError(t, durable.SetCanonicalQuietRecipient(ctx, "supervisor"))
	postQuiet := func(id, sender, authID string) {
		t.Helper()
		require.NoError(t, durable.ReserveManagedQuietSendAuthorization(ctx, authID, sender, "supervisor"))
		body := fmt.Sprintf(`{"id":%q,"from":%q,"to":"supervisor","body":%q,"created_at":"2030-01-01T00:00:00.000Z"}`, id, sender, "quiet "+id)
		response, requestErr := http.Post(httpURL+"/v1/messages", "application/json", strings.NewReader(body))
		require.NoError(t, requestErr)
		defer response.Body.Close()
		require.Equal(t, http.StatusCreated, response.StatusCode)
	}
	seedQuiet := func(id, sender string) {
		t.Helper()
		source := protocol.MessageSource{Kind: "agent"}
		body := "quiet " + id
		_, seedErr := durable.Enqueue(ctx, protocol.Envelope{ID: id, From: sender, To: "supervisor", Body: body, CreatedAt: "2030-01-01T00:00:00.000Z", Source: &source}, store.EnqueueOptions{Conversation: &store.ConversationOptions{Body: &body, Source: &source, QuietEligible: true}})
		require.NoError(t, seedErr)
	}

	// One old row per pair proves the age boundary independently of the count
	// boundary. The fixed clock makes recorded_at, not client created_at, exact.
	postQuiet("quiet-a-old", "quiet-a", "quiet-auth-a-old")
	postQuiet("quiet-b-old", "quiet-b", "quiet-auth-b-old")
	require.NoError(t, clock.Advance(31*24*time.Hour))

	for index := 0; index < 201; index++ {
		for _, sender := range []string{"quiet-a", "quiet-b"} {
			id := fmt.Sprintf("%s-%03d", sender, index)
			if index == 0 {
				// Keep a route-level sample for each pair. The controlled bulk
				// uses the same Store.Enqueue ingress after HTTP validation.
				postQuiet(id, sender, fmt.Sprintf("quiet-auth-%s-%03d", sender, index))
			} else {
				seedQuiet(id, sender)
			}
		}
	}

	// Startup already ran a sweep. Run the same router-owned sweep explicitly
	// after the controlled writes so both count and age rules are observed at a
	// named synchronization barrier, without a timer or wall-clock sleep.
	require.NoError(t, router.runSweep(ctx))
	quietBeforeComplete := map[string][]store.ManagedHistoryEntry{}
	for _, agentID := range []string{"quiet-a", "quiet-b"} {
		quietBeforeComplete[agentID] = fetchRetentionHistory(t, httpURL, agentID, "quiet")
		require.Len(t, quietBeforeComplete[agentID], 200)
		require.NotContains(t, retentionIDs(quietBeforeComplete[agentID]), agentID+"-old")
		require.NotContains(t, retentionIDs(quietBeforeComplete[agentID]), "ordinary-control")
		require.Equal(t, expectedQuietIDs(agentID), retentionIDs(quietBeforeComplete[agentID]))
	}

	// This is a real public message, but without a manager authorization it is
	// deliberately excluded from Quiet even if its source metadata says agent.
	// It is added after the retention barrier so a nonqualifying row cannot
	// consume one of the 200 retained Quiet slots for this assertion.
	ordinary := `{"id":"ordinary-control","from":"ordinary-control-source","to":"supervisor","body":"ordinary control","created_at":"2030-01-01T00:00:00.000Z","source":{"kind":"agent"}}`
	ordinaryResponse, err := http.Post(httpURL+"/v1/messages", "application/json", strings.NewReader(ordinary))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, ordinaryResponse.StatusCode)
	require.NoError(t, ordinaryResponse.Body.Close())
	require.Equal(t, quietBeforeComplete["quiet-a"], fetchRetentionHistory(t, httpURL, "quiet-a", "quiet"))

	// Complete history is produced by the router's internal event boundary. The
	// first event is observed through UI-live before the bounded bulk append.
	router.recordCompleteHistory(ctx, "quiet-a", "managed_lifecycle", "info", "Lifecycle event 000", "retention-complete-000")
	liveFrame := readRetentionUILiveFrame(t, live)
	require.Equal(t, "history_entry", liveFrame["type"])
	require.Equal(t, "complete", liveFrame["view"])
	liveEntry, ok := liveFrame["entry"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "complete:1", liveEntry["id"])

	for index := 1; index < 201; index++ {
		category := "managed_lifecycle"
		if index%2 == 1 {
			category = "managed_control"
		}
		agentID := "quiet-a"
		if index%2 == 1 {
			agentID = "quiet-b"
		}
		router.recordCompleteHistory(ctx, agentID, category, "info", fmt.Sprintf("Lifecycle or control event %03d", index), fmt.Sprintf("retention-complete-%03d", index))
	}

	quietAfterComplete := map[string][]store.ManagedHistoryEntry{}
	for _, agentID := range []string{"quiet-a", "quiet-b"} {
		quietAfterComplete[agentID] = fetchRetentionHistory(t, httpURL, agentID, "quiet")
		require.Equal(t, quietBeforeComplete[agentID], quietAfterComplete[agentID], "Complete pruning must not alter Quiet for %s", agentID)
	}

	completeBeforeRestart := map[string][]store.ManagedHistoryEntry{}
	allCompleteIDs := make([]string, 0, 200)
	for _, agentID := range []string{"quiet-a", "quiet-b"} {
		completeBeforeRestart[agentID] = fetchRetentionHistory(t, httpURL, agentID, "complete")
		allCompleteIDs = append(allCompleteIDs, retentionIDs(completeBeforeRestart[agentID])...)
	}
	require.Len(t, allCompleteIDs, 200, "Complete retention is router-wide")
	require.NotContains(t, allCompleteIDs, "complete:1")
	require.Equal(t, expectedCompleteIDs(), sortedCompleteIDsBySequence(allCompleteIDs))

	live.Close(websocket.StatusNormalClosure, "before router restart")
	closeFirst()

	// Reopen the same SQLite file and start a new listening router. Startup's
	// sweep is another explicit barrier; all public pages must retain identity,
	// order, and cursor semantics across the process/store boundary.
	durable, err = store.Open(ctx, database, store.Options{Clock: clock, MaxQueuedMessages: 1000, MaxQueuedBytes: 2 * 1024 * 1024})
	require.NoError(t, err)
	manager, err = managed.NewManager(managed.ManagerOptions{Store: durable, Config: managed.Config{Enabled: false}, Manifest: manifest})
	require.NoError(t, err)
	router, err = New(Options{Store: durable, Clock: clock, Host: "127.0.0.1", Port: 0, Managed: manager})
	require.NoError(t, err)
	require.NoError(t, router.Listen(ctx))
	httpURL, _ = router.URLs()
	require.NotEmpty(t, httpURL)
	require.NoError(t, router.runSweep(ctx))

	for _, agentID := range []string{"quiet-a", "quiet-b"} {
		require.Equal(t, quietAfterComplete[agentID], fetchRetentionHistory(t, httpURL, agentID, "quiet"), "Quiet IDs/order changed after restart")
		require.Equal(t, completeBeforeRestart[agentID], fetchRetentionHistory(t, httpURL, agentID, "complete"), "Complete IDs/order changed after restart")
	}
}

func dialRetentionUILive(t *testing.T, wsURL string) *websocket.Conn {
	t.Helper()
	endpoint := "ws" + strings.TrimPrefix(wsURL, "ws")
	endpoint = strings.TrimSuffix(endpoint, "/v1/agents") + "/v1/ui-live"
	conn, response, err := websocket.Dial(context.Background(), endpoint, nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	require.NoError(t, err)
	return conn
}

func readRetentionUILiveFrame(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, payload, err := conn.Read(ctx)
	require.NoError(t, err)
	var frame map[string]any
	require.NoError(t, json.Unmarshal(payload, &frame))
	return frame
}

func fetchRetentionHistory(t *testing.T, baseURL, agentID, view string) []store.ManagedHistoryEntry {
	t.Helper()
	before := int64(0)
	entries := make([]store.ManagedHistoryEntry, 0)
	seen := map[string]bool{}
	for {
		query := url.Values{"agent_id": {agentID}, "view": {view}, "limit": {"37"}}
		if before != 0 {
			query.Set("before", strconv.FormatInt(before, 10))
		}
		response, err := http.Get(baseURL + "/v1/history?" + query.Encode())
		require.NoError(t, err)
		payload, readErr := io.ReadAll(response.Body)
		require.NoError(t, readErr)
		require.NoError(t, response.Body.Close())
		require.Equal(t, http.StatusOK, response.StatusCode, string(payload))
		var page store.ManagedHistoryPage
		require.NoError(t, json.Unmarshal(payload, &page))
		require.Equal(t, view, page.View)
		for _, entry := range page.Entries {
			require.False(t, seen[entry.ID], "duplicate history ID %s", entry.ID)
			seen[entry.ID] = true
			entries = append(entries, entry)
		}
		if page.NextBefore == nil {
			return entries
		}
		require.NotEqual(t, before, *page.NextBefore)
		before = *page.NextBefore
	}
}

func retentionIDs(entries []store.ManagedHistoryEntry) []string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID)
	}
	return ids
}

func expectedQuietIDs(agentID string) []string {
	ids := make([]string, 0, 200)
	for index := 200; index >= 1; index-- {
		ids = append(ids, fmt.Sprintf("%s-%03d", agentID, index))
	}
	return ids
}

func expectedCompleteIDs() []string {
	ids := make([]string, 0, 200)
	for sequence := int64(201); sequence >= 2; sequence-- {
		ids = append(ids, fmt.Sprintf("complete:%d", sequence))
	}
	return ids
}

func sortedCompleteIDsBySequence(ids []string) []string {
	result := append([]string(nil), ids...)
	// Pages are split by agent. Rebuild the router sequence order for the
	// assertion without relying on SQLite internals or a private store field.
	for left := 0; left < len(result); left++ {
		for right := left + 1; right < len(result); right++ {
			leftSequence, _ := strconv.ParseInt(strings.TrimPrefix(result[left], "complete:"), 10, 64)
			rightSequence, _ := strconv.ParseInt(strings.TrimPrefix(result[right], "complete:"), 10, 64)
			if leftSequence < rightSequence {
				result[left], result[right] = result[right], result[left]
			}
		}
	}
	return result
}
