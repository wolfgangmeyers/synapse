package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMigration062ReceiptRetentionKeepsThirtyDaysAndTwoHundredPerAgent(t *testing.T) {
	ctx := context.Background()
	durable := openTestStore(t)
	now := durable.clock.Now()
	const agentID = "migration-062-receipt-retention"

	insert := func(requestID, state string, terminalAt *time.Time) {
		resultID := requestID + "-result"
		resultEntryID := "mcr1_" + strings.ReplaceAll(resultID, "-", "")
		var receiptEntryIdentity, deliveredAt, storedTerminalAt, retryable any
		var receiptID, receiptRuntimeGeneration, receiptSessionEpoch, receiptManifestHash, receiptAt any
		reconciliationState := "append_pending"
		if state == "delivered" {
			formattedTerminalAt := formatTimestamp(*terminalAt)
			receiptEntryIdentity = resultEntryID
			deliveredAt = formattedTerminalAt
			storedTerminalAt = formattedTerminalAt
			retryable = 0
			reconciliationState = "delivered"
			receiptID = "receipt-" + requestID
			receiptRuntimeGeneration = "migration-062-generation"
			receiptSessionEpoch = 1
			receiptManifestHash = testManifestHash
			receiptAt = formattedTerminalAt
		}

		_, err := durable.db.ExecContext(ctx, `INSERT INTO managed_control_requests (
			request_id,result_id,agent_id,tool_call_id,capability_id,tool_name,definition_id,operation_id,
			context_kind,canonical_arguments_sha256,request_fingerprint,state,pi_session_id,session_epoch,
			process_pid,process_start_marker,process_incarnation,runtime_generation,manifest_revision,manifest_hash,
			result_status,result_content,result_details_json,result_payload_sha256,decoded_payload_bytes,result_entry_id,
			append_attempt,received_at,append_owner,append_started_at,append_watermark,append_submitted_at,
			receipt_entry_identity,delivered_at,terminal_at,retryable,
			delivery_protocol,delivery_payload_sha256,reconciliation_state,
			receipt_id,receipt_runtime_generation,receipt_session_epoch,receipt_manifest_hash,receipt_at
		) VALUES (?,?,?,'tool-call','capability','control','definition','operation',
			'root',?,?,?,'migration-062-session',1,
			862,'migration-062-start','migration-062-incarnation','migration-062-generation',1,?,
			'ok','result','{}',?,8,?,
			1,?,'append-owner',?,'watermark',?,
			?,?,?, ?,
			'stock_transcript_receipt_v2',?,?,
			?,?,?,?,?)`,
			requestID, resultID, agentID, testManifestHash, testManifestHash, state,
			testManifestHash, testManifestHash, resultEntryID,
			formatTimestamp(now.Add(-29*24*time.Hour)), formatTimestamp(now.Add(-2*time.Hour)), formatTimestamp(now.Add(-time.Hour)),
			receiptEntryIdentity, deliveredAt, storedTerminalAt, retryable,
			testManifestHash, reconciliationState,
			receiptID, receiptRuntimeGeneration, receiptSessionEpoch, receiptManifestHash, receiptAt,
		)
		require.NoError(t, err)
	}

	expiredAt := now.Add(-31 * 24 * time.Hour)
	insert("retention-expired", "delivered", &expiredAt)
	for index := 0; index < managedControlTerminalPerAgent+1; index++ {
		terminalAt := now.Add(-time.Duration(managedControlTerminalPerAgent+1-index) * time.Minute)
		insert(fmt.Sprintf("retention-terminal-%03d", index), "delivered", &terminalAt)
	}
	insert("retention-append-pending", "append_pending", nil)

	deleted, err := durable.PruneManagedControlTerminal(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, deleted)

	var terminalCount int
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_control_requests WHERE agent_id=? AND state='delivered'`, agentID).Scan(&terminalCount))
	require.Equal(t, managedControlTerminalPerAgent, terminalCount)

	for _, requestID := range []string{"retention-expired", "retention-terminal-000"} {
		var count int
		require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_control_requests WHERE request_id=?`, requestID).Scan(&count))
		require.Zero(t, count)
	}

	var pendingState, pendingProtocol, pendingReconciliation string
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT state,delivery_protocol,reconciliation_state FROM managed_control_requests WHERE request_id=?`, "retention-append-pending").Scan(&pendingState, &pendingProtocol, &pendingReconciliation))
	require.Equal(t, "append_pending", pendingState)
	require.Equal(t, "stock_transcript_receipt_v2", pendingProtocol)
	require.Equal(t, "append_pending", pendingReconciliation)
}
