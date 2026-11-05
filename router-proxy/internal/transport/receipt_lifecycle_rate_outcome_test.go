package transport

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testsupport/storetest"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// Every case owns its SQLite files and fixed clock. Responses are checked by
// status/code only: neither bodies nor sender content belong in test output.
func rateOutcomeFixture(t *testing.T) (*Router, *store.Store, *sql.DB, *deterministic.FixedClock) {
	t.Helper()
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
	t.Cleanup(func() { _ = durable.Close() })
	router, err := New(Options{Store: durable, Clock: clock})
	require.NoError(t, err)
	t.Cleanup(func() { _ = router.Close(ctx) })
	check, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = check.Close() })
	return router, durable, check, clock
}

func rateOutcomePost(r *Router, id, body string, recipients ...string) (int, string) {
	recipient := "rate-outcome-target"
	if len(recipients) != 0 {
		recipient = recipients[0]
	}
	payload, _ := json.Marshal(map[string]string{
		"id": id, "from": "rate-outcome-sender", "to": recipient, "body": body,
		"created_at": "2026-10-01T12:00:00.000Z",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	r.ServeHTTP(response, req)
	var result struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &result)
	return response.Code, result.Error.Code
}

func rateOutcomeCharges(t *testing.T, r *Router, expected int) {
	t.Helper()
	r.rateMu.Lock()
	defer r.rateMu.Unlock()
	require.Len(t, r.rates["rate-outcome-sender"], expected)
	require.Empty(t, r.rateReservations)
}

func TestReceiptRateOutcomeConcurrentFullBucket(t *testing.T) {
	r, _, check, clock := rateOutcomeFixture(t)
	r.rateMu.Lock()
	for i := 0; i < 59; i++ {
		r.rates["rate-outcome-sender"] = append(r.rates["rate-outcome-sender"], clock.Now())
	}
	r.rateMu.Unlock()
	start := make(chan struct{})
	results := make(chan int, 2)
	var wg sync.WaitGroup
	for _, id := range []string{"fresh-a", "fresh-b"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			<-start
			status, _ := rateOutcomePost(r, id, "ordinary body")
			results <- status
		}(id)
	}
	close(start)
	wg.Wait()
	close(results)
	created, limited := 0, 0
	for status := range results {
		switch status {
		case http.StatusCreated:
			created++
		case http.StatusTooManyRequests:
			limited++
		default:
			t.Fatalf("unexpected HTTP status %d", status)
		}
	}
	require.Equal(t, 1, created)
	require.Equal(t, 1, limited)
	rateOutcomeCharges(t, r, 60)
	var receipts, deliveries int
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM conversation_send_receipts`).Scan(&receipts))
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM messages`).Scan(&deliveries))
	require.Equal(t, 1, receipts)
	require.Equal(t, 1, deliveries)
}

func TestReceiptRateOutcomeReplayExpiryAndResolvedReuse(t *testing.T) {
	r, durable, check, clock := rateOutcomeFixture(t)
	status, code := rateOutcomePost(r, "resolved", "original")
	require.Equal(t, http.StatusCreated, status)
	require.Empty(t, code)
	unresolvedTarget := "rate-outcome-unresolved-target"
	status, _ = rateOutcomePost(r, "unresolved", "unresolved original", unresolvedTarget)
	require.Equal(t, http.StatusCreated, status)
	r.rateMu.Lock()
	originalCharges := append([]time.Time(nil), r.rates["rate-outcome-sender"]...)
	r.rateMu.Unlock()
	require.Len(t, originalCharges, 2)
	assertOriginalCharges := func() {
		t.Helper()
		r.rateMu.Lock()
		defer r.rateMu.Unlock()
		require.Equal(t, originalCharges, r.rates["rate-outcome-sender"])
		require.Empty(t, r.rateReservations)
	}
	ctx := context.Background()
	_, err := durable.AcquireAgentLease(ctx, store.LeaseRequest{AgentID: "rate-outcome-target", InstanceID: "fixture-instance", LeaseID: "fixture-lease"})
	require.NoError(t, err)
	claimed, err := durable.ClaimNextMessage(ctx, store.ClaimRequest{AgentID: "rate-outcome-target", InstanceID: "fixture-instance", LeaseID: "fixture-lease"})
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, "resolved", claimed.Message.ID)
	require.Len(t, claimed.Items, 1)
	require.Equal(t, "resolved", claimed.Items[0].MessageID)
	ack := store.ReceiptRequest{AgentID: "rate-outcome-target", InstanceID: "fixture-instance", LeaseID: "fixture-lease", MessageID: "resolved", AttemptNumber: claimed.AttemptNumber, DeliveryItems: claimed.Items}
	_, err = durable.RecordInjectionRequested(ctx, ack)
	require.NoError(t, err)
	_, err = durable.AcknowledgeDelivery(ctx, ack)
	require.NoError(t, err)
	require.NoError(t, clock.Advance(30*24*time.Hour-time.Millisecond))
	for _, tc := range []struct {
		body   string
		status int
		code   string
	}{
		{"original", http.StatusOK, ""},
		{"changed", http.StatusConflict, "idempotency_conflict"},
	} {
		status, code = rateOutcomePost(r, "resolved", tc.body)
		require.Equal(t, tc.status, status)
		require.Equal(t, tc.code, code)
	}
	for _, tc := range []struct {
		body   string
		status int
		code   string
	}{
		{"unresolved original", http.StatusOK, ""},
		{"changed", http.StatusConflict, "idempotency_conflict"},
	} {
		status, code = rateOutcomePost(r, "unresolved", tc.body, unresolvedTarget)
		require.Equal(t, tc.status, status)
		require.Equal(t, tc.code, code)
	}
	assertOriginalCharges() // expired charges remain until the next fresh rate admission
	require.NoError(t, clock.Advance(time.Millisecond))
	var unresolvedReceipt, unresolvedDelivery int
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM conversation_send_receipts WHERE message_id='unresolved'`).Scan(&unresolvedReceipt))
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM messages WHERE id='unresolved' AND recipient=? AND state='queued'`, unresolvedTarget).Scan(&unresolvedDelivery))
	require.Equal(t, 1, unresolvedReceipt)
	require.Equal(t, 1, unresolvedDelivery)
	for _, body := range []string{"unresolved original", "changed"} {
		status, code = rateOutcomePost(r, "unresolved", body, unresolvedTarget)
		require.Equal(t, http.StatusGone, status)
		require.Equal(t, "conversation_receipt_expired_unresolved", code)
	}
	assertOriginalCharges()
	var receipts, history int
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM conversation_send_receipts`).Scan(&receipts))
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM conversation_messages`).Scan(&history))
	require.Equal(t, 2, receipts)
	require.Equal(t, 2, history)
	status, code = rateOutcomePost(r, "resolved", "new incarnation")
	require.Equal(t, http.StatusCreated, status)
	require.Empty(t, code)
	rateOutcomeCharges(t, r, 1)
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM conversation_messages`).Scan(&history))
	require.Equal(t, 3, history)
}

func TestReceiptRateOutcomeRollbackRefundsOnlyFailedReservation(t *testing.T) {
	r, _, check, clock := rateOutcomeFixture(t)
	// Owned SQLite trigger fails after OnFreshAdmission reserved capacity.
	_, err := check.Exec(`CREATE TRIGGER rate_outcome_fault BEFORE INSERT ON messages WHEN NEW.id='faulted' BEGIN SELECT RAISE(ABORT, 'injected rate outcome fault'); END`)
	require.NoError(t, err)
	r.rateMu.Lock()
	for i := 0; i < 58; i++ {
		r.rates["rate-outcome-sender"] = append(r.rates["rate-outcome-sender"], clock.Now())
	}
	r.rateMu.Unlock()
	// Both requests start together. Store serializes SQLite transactions, but
	// the failed reservation must never erase the other request's charge.
	start := make(chan struct{})
	type outcome struct {
		id     string
		status int
		code   string
	}
	results := make(chan outcome, 2)
	for _, id := range []string{"survivor", "faulted"} {
		go func(id string) {
			<-start
			body := "ordinary"
			if id == "faulted" {
				body = "faulted body"
			}
			status, code := rateOutcomePost(r, id, body)
			results <- outcome{id, status, code}
		}(id)
	}
	close(start)
	for i := 0; i < 2; i++ {
		result := <-results
		if result.id == "survivor" {
			require.Equal(t, http.StatusCreated, result.status)
			require.Empty(t, result.code)
		} else {
			require.Equal(t, http.StatusInternalServerError, result.status)
			require.Equal(t, "internal_error", result.code)
		}
	}
	rateOutcomeCharges(t, r, 59)
	var failedRows int
	require.NoError(t, check.QueryRow(`SELECT (SELECT count(*) FROM messages WHERE id='faulted') + (SELECT count(*) FROM conversation_send_receipts WHERE message_id='faulted')`).Scan(&failedRows))
	require.Zero(t, failedRows)
	status, code := rateOutcomePost(r, "replacement", "replacement body")
	require.Equal(t, http.StatusCreated, status)
	require.Empty(t, code)
	rateOutcomeCharges(t, r, 60)
	status, code = rateOutcomePost(r, "blocked", "ordinary")
	require.Equal(t, http.StatusTooManyRequests, status)
	require.Equal(t, "rate_limited", code)
	rateOutcomeCharges(t, r, 60)
}
