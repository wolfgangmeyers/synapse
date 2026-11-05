package transport

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testsupport/storetest"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// The retained receipt is not a rate-limit exemption once its resolved ID can
// be admitted by Store as a fresh incarnation. Unresolved IDs still need to
// reach Store for the typed 410 without consuming a rate token.
func TestConversationReceiptExpiredResolvedReuseRateAdmission(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "router.db")
	opts := store.Options{Clock: clock}
	if opts.Migrations == nil {
		if err := storetest.CloneTemplateDB(t, path); err == nil {
			opts.Migrations = []store.Migration{}
		}
	}
	durable, err := store.Open(ctx, path, opts)
	require.NoError(t, err)
	defer durable.Close()
	router, err := New(Options{Store: durable, Clock: clock})
	require.NoError(t, err)
	defer router.Close(ctx)
	check, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer check.Close()

	post := func(id, body, recipient string) (int, map[string]any) {
		t.Helper()
		payload, e := json.Marshal(map[string]string{
			"id": id, "from": "rate-reuse-sender", "to": recipient, "body": body,
			"created_at": "2026-10-01T12:00:00.000Z",
		})
		require.NoError(t, e)
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		var result map[string]any
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
		return response.Code, result
	}
	counts := func() (int, int, int) {
		t.Helper()
		var receipts, deliveries, history int
		require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM conversation_send_receipts`).Scan(&receipts))
		require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM messages`).Scan(&deliveries))
		require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM conversation_messages`).Scan(&history))
		return receipts, deliveries, history
	}
	status, original := post("rate-reuse-resolved", "original body", "rate-reuse-target")
	require.Equal(t, http.StatusCreated, status, original)
	firstIncarnation := original["message"].(map[string]any)["message_incarnation_id"]
	status, unresolved := post("rate-reuse-unresolved", "unresolved body", "rate-reuse-other-target")
	require.Equal(t, http.StatusCreated, status, unresolved)
	router.rateMu.Lock()
	require.Len(t, router.rates["rate-reuse-sender"], 2, "both accepted sends must charge the sender")
	router.rateMu.Unlock()

	// Resolve one ID through the actual delivery path, leaving the other queued.
	_, err = durable.AcquireAgentLease(ctx, store.LeaseRequest{AgentID: "rate-reuse-target", InstanceID: "rate-reuse-instance", LeaseID: "rate-reuse-lease"})
	require.NoError(t, err)
	claimed, err := durable.ClaimNextMessage(ctx, store.ClaimRequest{AgentID: "rate-reuse-target", InstanceID: "rate-reuse-instance", LeaseID: "rate-reuse-lease"})
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, "rate-reuse-resolved", claimed.Message.ID)
	ack := store.ReceiptRequest{AgentID: "rate-reuse-target", InstanceID: "rate-reuse-instance", LeaseID: "rate-reuse-lease", MessageID: "rate-reuse-resolved", AttemptNumber: claimed.AttemptNumber, DeliveryItems: claimed.Items}
	_, err = durable.RecordInjectionRequested(ctx, ack)
	require.NoError(t, err)
	_, err = durable.AcknowledgeDelivery(ctx, ack)
	require.NoError(t, err)

	// The in-horizon receipt owns its ID even under a full sender rate bucket.
	require.NoError(t, clock.Advance(30*24*time.Hour-time.Millisecond))
	router.rateMu.Lock()
	router.rates["rate-reuse-sender"] = nil
	for i := 0; i < 60; i++ {
		router.rates["rate-reuse-sender"] = append(router.rates["rate-reuse-sender"], clock.Now())
	}
	router.rateMu.Unlock()
	beforeReceipts, beforeDeliveries, beforeHistory := counts()
	status, replay := post("rate-reuse-resolved", "original body", "rate-reuse-target")
	require.Equal(t, http.StatusOK, status, replay)
	require.Equal(t, original["conversation_message"], replay["conversation_message"])
	status, conflict := post("rate-reuse-resolved", "changed body", "rate-reuse-target")
	require.Equal(t, http.StatusConflict, status, conflict)
	require.Equal(t, "idempotency_conflict", conflict["error"].(map[string]any)["code"])
	receipts, deliveries, history := counts()
	require.Equal(t, beforeReceipts, receipts)
	require.Equal(t, beforeDeliveries, deliveries)
	require.Equal(t, beforeHistory, history)
	router.rateMu.Lock()
	require.Len(t, router.rates["rate-reuse-sender"], 60)
	router.rateMu.Unlock()

	// Exact accepted-at + 30 days. Both receipts remain in SQLite: only
	// the resolved one is eligible to become a new send.
	require.NoError(t, clock.Advance(time.Millisecond))
	var retained int
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM conversation_send_receipts WHERE message_id IN ('rate-reuse-resolved', 'rate-reuse-unresolved')`).Scan(&retained))
	require.Equal(t, 2, retained)
	for _, body := range []string{"unresolved body", "changed unresolved body"} {
		status, gone := post("rate-reuse-unresolved", body, "rate-reuse-other-target")
		require.Equal(t, http.StatusGone, status, gone)
		problem := gone["error"].(map[string]any)
		require.Equal(t, "conversation_receipt_expired_unresolved", problem["code"])
		require.Equal(t, "rate-reuse-unresolved", problem["message_id"])
		receipts, deliveries, history = counts()
		require.Equal(t, beforeReceipts, receipts)
		require.Equal(t, beforeDeliveries, deliveries)
		require.Equal(t, beforeHistory, history)
	}
	router.rateMu.Lock()
	require.Len(t, router.rates["rate-reuse-sender"], 60, "410 must not charge the sender")
	router.rateMu.Unlock()

	// This is a fresh send despite the retained receipt. HTTP must apply the
	// sender cap before Store can replace the resolved ID's incarnation.
	status, limited := post("rate-reuse-resolved", "new incarnation body", "rate-reuse-target")
	require.Equal(t, http.StatusTooManyRequests, status, limited)
	require.Equal(t, "rate_limited", limited["error"].(map[string]any)["code"])
	receipts, deliveries, history = counts()
	require.Equal(t, beforeReceipts, receipts, "429 must not replace the receipt")
	require.Equal(t, beforeDeliveries, deliveries, "429 must not enqueue")
	require.Equal(t, beforeHistory, history, "429 must not add history")
	router.rateMu.Lock()
	require.Len(t, router.rates["rate-reuse-sender"], 60, "429 must not charge another token")
	delete(router.rates, "rate-reuse-sender")
	router.rateMu.Unlock()

	// Removing only the synthetic pressure proves Store would admit the ID as
	// a new incarnation; this is not an idempotency conflict in disguise.
	status, reused := post("rate-reuse-resolved", "new incarnation body", "rate-reuse-target")
	require.Equal(t, http.StatusCreated, status, reused)
	require.NotEqual(t, firstIncarnation, reused["message"].(map[string]any)["message_incarnation_id"])
	receipts, deliveries, history = counts()
	require.Equal(t, beforeReceipts, receipts)
	require.Equal(t, beforeDeliveries+1, deliveries)
	require.Equal(t, beforeHistory+1, history)
}
