package transport

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedMailHTTPAppendACKRollbackAndSafeRetry(t *testing.T) {
	ctx := context.Background()
	f := newEmbeddedHTTPG2Fixture(t)
	launched, err := f.manager.LaunchEmbeddedRoot(ctx, managed.LaunchRequest{Name: "mail-root", Cwd: f.root, Runtime: "embedded", Model: &managed.Model{Provider: "openai-codex", ID: "gpt-5.6-sol"}, RequestID: "f1111111-1111-4111-8111-111111111111"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		row, e := f.db.ManagedAgent(ctx, launched.Agent.AgentID)
		return e == nil && row.LifecycleState == "running"
	}, 3*time.Second, 10*time.Millisecond)
	fault, err := sql.Open("sqlite", f.dbPath)
	require.NoError(t, err)
	defer fault.Close()
	// Pin this test's DDL connection so failure diagnostics inspect the same
	// connection, not another pooled connection. No busy timeout or retry.
	connection, err := fault.Conn(ctx)
	require.NoError(t, err)
	defer connection.Close()
	ddl := func(operation, statement string) {
		t.Helper()
		started := time.Now().UTC()
		_, execErr := connection.ExecContext(ctx, statement)
		if execErr != nil {
			t.Logf("mail DDL operation=%q begin=%s returned=%s cause=%v held_connection=%p pool=%+v process_generation=%d process_key=%q provider_calls=%d", operation, started.Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), execErr, connection, fault.Stats(), f.router.embeddedProcessGeneration, "Store-owned incarnation", f.calls.Load())
			embeddedMailRollbackSnapshot(t, connection, operation)
		}
		require.NoError(t, execErr)
	}
	ddl("CREATE TRIGGER before mail submission", `CREATE TRIGGER mail_g2_ack_fault BEFORE UPDATE OF state ON messages WHEN NEW.state='acknowledged' BEGIN SELECT RAISE(ABORT,'mail g2 ack fault'); END`)
	id := "f2222222-2222-4222-8222-222222222222"
	status, raw := f.call(http.MethodPost, "/v1/messages", "", fmt.Sprintf(`{"id":%q,"from":"sender-one","to":"mail-root","body":"ordered mail turn","created_at":"2026-09-27T00:00:00.000Z"}`, id))
	require.Equal(t, http.StatusCreated, status, string(raw))
	require.Eventually(t, func() bool {
		var n int
		return connection.QueryRowContext(ctx, `SELECT count(*) FROM embedded_attempts WHERE message_id=?`, id).Scan(&n) == nil && n == 1
	}, 3*time.Second, 10*time.Millisecond)
	// The failed transaction cannot leak an entry, evidence, processing ACK or provider call.
	require.Never(t, func() bool { return f.calls.Load() != 0 }, 100*time.Millisecond, 10*time.Millisecond)
	row, err := f.db.MessageFor(ctx, id)
	require.NoError(t, err)
	require.Equal(t, domain.MessageQueued, row.State)
	var entries, acks int
	require.NoError(t, connection.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id='mail-root' AND kind='user'`).Scan(&entries))
	require.NoError(t, connection.QueryRowContext(ctx, `SELECT count(*) FROM embedded_acks WHERE message_id=?`, id).Scan(&acks))
	require.Zero(t, entries)
	require.Zero(t, acks)
	var original string
	require.NoError(t, connection.QueryRowContext(ctx, `SELECT attempt_id FROM embedded_attempts WHERE message_id=?`, id).Scan(&original))
	ddl("DROP TRIGGER after failed append/ACK", `DROP TRIGGER mail_g2_ack_fault`)
	f.manager.OfferDirect(launched.Agent.AgentID)
	require.Eventually(t, func() bool {
		row, e := f.db.MessageFor(ctx, id)
		return e == nil && row.State == domain.MessageAcknowledged && f.calls.Load() == 1
	}, 3*time.Second, 10*time.Millisecond)
	var retry string
	require.NoError(t, connection.QueryRowContext(ctx, `SELECT attempt_id FROM embedded_attempts WHERE message_id=?`, id).Scan(&retry))
	require.Equal(t, original, retry)
}

// Inspect the held fault connection before assertion, connection close, or
// fixture cleanup. This is committed state after the DDL error, not a claim
// about the private Store connection's active transaction or lock owner.
func embeddedMailRollbackSnapshot(t *testing.T, connection *sql.Conn, operation string) {
	t.Helper()
	t.Logf("mail snapshot operation=%q at=%s DDL=implicit_transaction engine_begin/rollback/commit_hooks=unavailable Store_active_transaction/lock_owner=unavailable", operation, time.Now().UTC().Format(time.RFC3339Nano))
	tx, err := connection.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	t.Logf("mail snapshot read begin=%v held_connection=%p", err, connection)
	if err != nil {
		return
	}
	defer func() {
		t.Logf("mail snapshot read rollback=%v at=%s", tx.Rollback(), time.Now().UTC().Format(time.RFC3339Nano))
	}()
	for _, query := range []string{
		"PRAGMA busy_timeout", "PRAGMA journal_mode", "PRAGMA locking_mode", "PRAGMA database_list",
		"SELECT * FROM managed_agents", "SELECT * FROM managed_http_operations", "SELECT * FROM managed_lifecycle_actions",
		"SELECT * FROM embedded_process_generations", "SELECT * FROM embedded_sessions", "SELECT * FROM embedded_effects",
		"SELECT * FROM messages", "SELECT * FROM embedded_attempts", "SELECT * FROM embedded_acks",
		"SELECT agent_id, session_id, epoch, generation, seq, kind, digest FROM embedded_entries",
		"SELECT name, sql FROM sqlite_master WHERE type='trigger' AND name='mail_g2_ack_fault'",
	} {
		rows, queryErr := tx.QueryContext(context.Background(), query)
		if queryErr != nil {
			t.Logf("mail snapshot %q query=%v", query, queryErr)
			continue
		}
		columns, columnErr := rows.Columns()
		if columnErr != nil {
			t.Logf("mail snapshot %q columns=%v", query, columnErr)
			_ = rows.Close()
			continue
		}
		var records []map[string]any
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if scanErr := rows.Scan(pointers...); scanErr != nil {
				t.Logf("mail snapshot %q scan=%v", query, scanErr)
				break
			}
			record := make(map[string]any, len(columns))
			for i, column := range columns {
				if value, ok := values[i].([]byte); ok {
					values[i] = string(value)
				}
				record[column] = values[i]
			}
			records = append(records, record)
		}
		body, marshalErr := json.Marshal(records)
		t.Logf("mail snapshot %q=%s rows_error=%v marshal_error=%v close_error=%v", query, body, rows.Err(), marshalErr, rows.Close())
	}
	t.Logf("mail snapshot read commit=%v at=%s", tx.Commit(), time.Now().UTC().Format(time.RFC3339Nano))
}
