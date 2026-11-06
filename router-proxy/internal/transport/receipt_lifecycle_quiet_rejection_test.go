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
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// A rejected Store admission must not spend a managed mail.send authorization.
// The next accepted send should carry its quiet provenance, exactly once.
func TestManagedQuietAuthorizationSurvivesReceiptRejection(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		status     int
	}{
		{name: "idempotency conflict", status: http.StatusConflict, code: "idempotency_conflict"},
		{name: "receipt capacity", status: http.StatusInsufficientStorage, code: "conversation_receipt_capacity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			clock := deterministic.NewFixedClock(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
			path := filepath.Join(t.TempDir(), "router.db")
			durable, err := store.Open(ctx, path, store.Options{Clock: clock})
			require.NoError(t, err)
			t.Cleanup(func() { _ = durable.Close() })
			require.NoError(t, durable.SetCanonicalQuietRecipient(ctx, "supervisor"))
			router, err := New(Options{Store: durable, Clock: clock})
			require.NoError(t, err)
			t.Cleanup(func() { _ = router.Close(ctx) })
			check, err := sql.Open("sqlite", path)
			require.NoError(t, err)
			t.Cleanup(func() { _ = check.Close() })

			post := func(id, body, controlID string) (int, map[string]any) {
				t.Helper()
				payload, e := json.Marshal(map[string]string{
					"id": id, "from": "managed-agent", "to": "supervisor", "body": body,
					"created_at": "2026-10-01T12:00:00.000Z",
				})
				require.NoError(t, e)
				req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(payload))
				req.Header.Set("Content-Type", "application/json")
				if controlID != "" {
					req.Header.Set("X-Mecha-Managed-Control-Request-ID", controlID)
				}
				response := httptest.NewRecorder()
				router.ServeHTTP(response, req)
				var result map[string]any
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
				return response.Code, result
			}
			count := func(table string) int {
				t.Helper()
				var n int
				require.NoError(t, check.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n))
				return n
			}
			// The conflicting ID already owns a delivery, history, and receipt.
			if tc.status == http.StatusConflict {
				status, _ := post("rejected-id", "original body", "")
				require.Equal(t, http.StatusCreated, status)
			}
			if tc.status == http.StatusInsufficientStorage {
				status, _ := post("capacity-seed", "ordinary seed", "")
				require.Equal(t, http.StatusCreated, status)
				_, err = check.ExecContext(ctx, `UPDATE conversation_send_receipt_capacity SET max_count=1 WHERE singleton=1`)
				require.NoError(t, err)
			}
			baseline := []int{count("conversation_send_receipts"), count("messages"), count("conversation_messages"), count("embedded_effects")}
			require.NoError(t, durable.ReserveManagedQuietSendAuthorization(ctx, "quiet-control", "managed-agent", "supervisor"))
			body := "original body"
			if tc.status == http.StatusConflict {
				body = "different body"
			}
			status, rejected := post("rejected-id", body, "quiet-control")
			require.Equal(t, tc.status, status, rejected)
			require.Equal(t, tc.code, rejected["error"].(map[string]any)["code"])
			require.Equal(t, baseline, []int{count("conversation_send_receipts"), count("messages"), count("conversation_messages"), count("embedded_effects")}, "rejection must not create a receipt, delivery, history, or effect")
			if tc.status == http.StatusInsufficientStorage {
				_, err = check.ExecContext(ctx, `UPDATE conversation_send_receipt_capacity SET max_count=1000 WHERE singleton=1`)
				require.NoError(t, err)
			}
			status, accepted := post("accepted-quiet-id", "accepted body", "quiet-control")
			require.Equal(t, http.StatusCreated, status, accepted)
			status, ordinary := post("accepted-ordinary-id", "second accepted body", "quiet-control")
			require.Equal(t, http.StatusCreated, status, ordinary)
			page, err := durable.ManagedHistoryPage(ctx, "managed-agent", "quiet", 0, 100)
			require.NoError(t, err)
			require.Len(t, page.Entries, 1, "only the first accepted use may carry quiet provenance")
			require.Equal(t, "accepted-quiet-id", page.Entries[0].ID)
			var quietRows, remaining int
			require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM conversation_messages WHERE quiet_eligible=1`).Scan(&quietRows))
			require.Equal(t, 1, quietRows)
			require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM managed_quiet_send_authorizations WHERE request_id='quiet-control'`).Scan(&remaining))
			require.Zero(t, remaining)
		})
	}
}
