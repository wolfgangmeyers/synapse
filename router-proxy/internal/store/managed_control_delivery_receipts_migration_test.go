package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration062AddsStockTranscriptReceiptColumns(t *testing.T) {
	durable := openTestStore(t)

	for table, columns := range map[string][]string{
		"managed_control_requests": {
			"delivery_protocol", "delivery_payload_sha256", "reconciliation_state",
			"receipt_id", "receipt_runtime_generation", "receipt_session_epoch",
			"receipt_manifest_hash", "receipt_at",
		},
		"managed_direct_delivery_attempts": {
			"reconciliation_protocol", "receipt_payload_sha256", "receipt_id",
		},
	} {
		for _, column := range columns {
			var count int
			require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, column).Scan(&count))
			require.Equalf(t, 1, count, "migration 062 must add %s.%s", table, column)
		}
	}
}

func TestMigration062UpgradeClassifiesLegacyControlDeliveryRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	migrations := DefaultMigrations()
	migration062 := migrationIndex(t, migrations, "062_managed_control_delivery_receipts")

	before, err := Open(ctx, path, Options{Migrations: migrations[:migration062]})
	require.NoError(t, err)
	deliveredID := lifecycleUUID(1620)
	deliveredResultID := lifecycleUUID(2620)
	pendingID := lifecycleUUID(1621)
	pendingResultID := lifecycleUUID(2621)
	insertLegacy := func(requestID, resultID, state, content, details string) {
		entryID := "mcr1_" + strings.ReplaceAll(resultID, "-", "")
		_, err := before.db.ExecContext(ctx, `INSERT INTO managed_control_requests (
			request_id,result_id,agent_id,tool_call_id,capability_id,tool_name,definition_id,operation_id,
			context_kind,canonical_arguments_sha256,request_fingerprint,state,pi_session_id,session_epoch,
			process_pid,process_start_marker,process_incarnation,runtime_generation,manifest_revision,manifest_hash,
			result_status,result_content,result_details_json,result_payload_sha256,decoded_payload_bytes,result_entry_id,
			append_attempt,received_at,append_owner,append_started_at,append_watermark,append_submitted_at,
			receipt_entry_identity,delivered_at,terminal_at,retryable
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			requestID, resultID, "migration-062-upgrade", "tool-call", "capability", "control", "definition", "operation",
			"root", testManifestHash, testManifestHash, state, "migration-062-session", 1,
			862, "migration-062-start", "migration-062-incarnation", "migration-062-generation", 1, testManifestHash,
			"ok", content, details, testManifestHash, len(content)+len(details), entryID,
			1, "2026-09-14T00:00:00.000Z", lifecycleUUID(4000), "2026-09-14T00:00:01.000Z", "legacy-watermark",
			func() any {
				if state == "delivered" {
					return "2026-09-14T00:00:02.000Z"
				}
				return nil
			}(),
			func() any {
				if state == "delivered" {
					return entryID
				}
				return nil
			}(),
			func() any {
				if state == "delivered" {
					return "2026-09-14T00:00:03.000Z"
				}
				return nil
			}(),
			func() any {
				if state == "delivered" {
					return "2026-09-14T00:00:03.000Z"
				}
				return nil
			}(),
			func() any {
				if state == "delivered" {
					return 0
				}
				return nil
			}(),
		)
		require.NoError(t, err)
	}

	insertLegacy(deliveredID, deliveredResultID, "delivered", "legacy delivered result", `{"kind":"delivered"}`)
	insertLegacy(pendingID, pendingResultID, "append_pending", "pending immutable result", `{"kind":"pending"}`)
	deliveredBefore := migration062ResultBytes(t, before, deliveredID)
	pendingBefore := migration062ResultBytes(t, before, pendingID)
	require.NoError(t, before.Close())

	after, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer after.Close()

	require.Equal(t, deliveredBefore, migration062ResultBytes(t, after, deliveredID))
	require.Equal(t, pendingBefore, migration062ResultBytes(t, after, pendingID))

	assertClassification := func(requestID, wantState, wantProtocol, wantReconciliation string, wantPayload []byte) {
		var state string
		var protocol, payload, reconciliation sql.NullString
		var receiptID, receiptGeneration, receiptManifest, receiptAt sql.NullString
		var receiptEpoch sql.NullInt64
		require.NoError(t, after.db.QueryRowContext(ctx, `SELECT state,delivery_protocol,delivery_payload_sha256,reconciliation_state,
			receipt_id,receipt_runtime_generation,receipt_session_epoch,receipt_manifest_hash,receipt_at
			FROM managed_control_requests WHERE request_id=?`, requestID).Scan(
			&state, &protocol, &payload, &reconciliation,
			&receiptID, &receiptGeneration, &receiptEpoch, &receiptManifest, &receiptAt,
		))
		require.Equal(t, wantState, state)
		require.Equal(t, sql.NullString{String: wantProtocol, Valid: true}, protocol)
		require.Equal(t, sql.NullString{String: string(wantPayload), Valid: true}, payload)
		require.Equal(t, sql.NullString{String: wantReconciliation, Valid: true}, reconciliation)
		require.False(t, receiptID.Valid)
		require.False(t, receiptGeneration.Valid)
		require.False(t, receiptEpoch.Valid)
		require.False(t, receiptManifest.Valid)
		require.False(t, receiptAt.Valid)
	}

	assertClassification(deliveredID, "delivered", "legacy_explicit_entry_v1", "delivered", deliveredBefore.payloadSHA256)
	assertClassification(pendingID, "append_pending", "stock_transcript_receipt_v2", "append_pending", pendingBefore.payloadSHA256)
}

type migration062ImmutableResult struct {
	status, content, details, payloadSHA256, decodedBytes, entryID []byte
}

func migration062ResultBytes(t *testing.T, durable *Store, requestID string) migration062ImmutableResult {
	t.Helper()
	var result migration062ImmutableResult
	require.NoError(t, durable.db.QueryRow(`SELECT
		CAST(result_status AS BLOB),CAST(result_content AS BLOB),CAST(result_details_json AS BLOB),
		CAST(result_payload_sha256 AS BLOB),CAST(decoded_payload_bytes AS BLOB),CAST(result_entry_id AS BLOB)
		FROM managed_control_requests WHERE request_id=?`, requestID).Scan(
		&result.status, &result.content, &result.details, &result.payloadSHA256, &result.decodedBytes, &result.entryID,
	))
	return result
}
