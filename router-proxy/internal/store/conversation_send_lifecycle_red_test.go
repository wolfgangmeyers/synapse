package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

const receiptHorizon = 30 * time.Minute

func lifecycleFixture(t *testing.T) (*Store, *deterministic.FixedClock, string) {
	t.Helper()
	clock := deterministic.NewFixedClock(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "router.db")
	s, err := Open(context.Background(), path, Options{Clock: clock})
	require.NoError(t, err)
	t.Cleanup(func() {
		if !s.closed {
			require.NoError(t, s.Close())
		}
	})
	return s, clock, path
}

func lifecycleSend(s *Store, id, body string) (EnqueueResult, error) {
	e := testEnvelope(id, body)
	return s.Enqueue(context.Background(), e, EnqueueOptions{Conversation: &ConversationOptions{Body: &e.Body}})
}

func lifecycleACK(t *testing.T, s *Store, id string) {
	t.Helper()
	ctx := context.Background()
	_, err := s.AcquireAgentLease(ctx, LeaseRequest{AgentID: "agent-a", InstanceID: "lifecycle-instance", LeaseID: "lifecycle-lease"})
	require.NoError(t, err)
	claimed, err := s.ClaimNextMessage(ctx, ClaimRequest{AgentID: "agent-a", InstanceID: "lifecycle-instance", LeaseID: "lifecycle-lease"})
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, id, claimed.Message.ID)
	receipt := ReceiptRequest{AgentID: "agent-a", InstanceID: "lifecycle-instance", LeaseID: "lifecycle-lease", MessageID: id, AttemptNumber: claimed.AttemptNumber, DeliveryItems: claimed.Items}
	_, err = s.RecordInjectionRequested(ctx, receipt)
	require.NoError(t, err)
	_, err = s.AcknowledgeDelivery(ctx, receipt)
	require.NoError(t, err)
}

func lifecycleUsage(t *testing.T, s *Store) (int64, int64) {
	t.Helper()
	var count, bytes int64
	require.NoError(t, s.db.QueryRow(`SELECT used_count,used_bytes FROM conversation_send_receipt_capacity WHERE singleton=1`).Scan(&count, &bytes))
	return count, bytes
}

func TestConversationReceiptLifecycleREDProtectedUntilExactDeadline(t *testing.T) {
	s, clock, path := lifecycleFixture(t)
	first, err := lifecycleSend(s, "horizon-id", "original")
	require.NoError(t, err)
	lifecycleACK(t, s, "horizon-id")
	require.NoError(t, clock.Advance(receiptHorizon-time.Millisecond))
	_, err = s.SweepLifecycle(context.Background(), DefaultRetentionPolicy)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	s, err = Open(context.Background(), path, Options{Clock: clock})
	require.NoError(t, err)
	defer s.Close()
	replayed, err := lifecycleSend(s, "horizon-id", "original")
	require.NoError(t, err)
	require.False(t, replayed.Created)
	require.Equal(t, first.Message, replayed.Message)
	require.Equal(t, first.ConversationMessage, replayed.ConversationMessage)
	_, err = lifecycleSend(s, "horizon-id", "changed")
	require.ErrorAs(t, err, new(*IdempotencyConflictError))
	require.NoError(t, clock.Advance(time.Millisecond))
	fresh, err := lifecycleSend(s, "horizon-id", "new incarnation")
	require.NoError(t, err)
	require.True(t, fresh.Created, "at the exact 30-minute deadline the ACKed receipt must not be replayed or conflicted")
	require.NotEqual(t, first.Message.MessageIncarnationID, fresh.Message.MessageIncarnationID)
}

func TestConversationReceiptLifecycleExpiredUnresolvedKeepsDelivery(t *testing.T) {
	s, clock, _ := lifecycleFixture(t)
	first, err := lifecycleSend(s, "unresolved-id", "original")
	require.NoError(t, err)
	require.NoError(t, clock.Advance(receiptHorizon))
	replayed, err := lifecycleSend(s, "unresolved-id", "original")
	require.NoError(t, err)
	require.False(t, replayed.Created, "the still-live delivery owns its ID")
	_, err = lifecycleSend(s, "unresolved-id", "different")
	require.ErrorAs(t, err, new(*IdempotencyConflictError))
	count, bytes := lifecycleUsage(t, s)
	require.Zero(t, count)
	require.Zero(t, bytes)
	var deliveries, histories int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM messages WHERE id=? AND message_incarnation_id=?`, first.Message.ID, first.Message.MessageIncarnationID).Scan(&deliveries))
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM conversation_messages WHERE message_id=?`, first.Message.ID).Scan(&histories))
	require.Equal(t, 1, deliveries)
	require.Equal(t, 1, histories)
}

func TestConversationReceiptLifecycleREDPruneReleasesExactBytesAtCap(t *testing.T) {
	s, clock, _ := lifecycleFixture(t)
	first, err := lifecycleSend(s, "cap-reuse-id", "é original")
	require.NoError(t, err)
	lifecycleACK(t, s, first.Message.ID)
	var serializedBytes int64
	require.NoError(t, s.db.QueryRow(`SELECT length(CAST(message_id AS BLOB))+length(CAST(conversation_json AS BLOB))+length(CAST(delivery_json AS BLOB)) FROM conversation_send_receipts WHERE message_id=?`, first.Message.ID).Scan(&serializedBytes))
	count, bytes := lifecycleUsage(t, s)
	require.Equal(t, int64(1), count)
	require.Equal(t, serializedBytes, bytes)
	_, err = s.db.Exec(`UPDATE conversation_send_receipt_capacity SET max_count=1,max_bytes=? WHERE singleton=1`, bytes)
	require.NoError(t, err)
	_, err = lifecycleSend(s, "blocked-id", "other")
	require.ErrorAs(t, err, new(*ConversationReceiptCapacityError))
	require.NoError(t, clock.Advance(receiptHorizon))
	_, err = s.SweepLifecycle(context.Background(), DefaultRetentionPolicy)
	require.NoError(t, err)
	count, bytes = lifecycleUsage(t, s)
	require.Zero(t, count)
	require.Zero(t, bytes, "sweep must release the persisted UTF-8 byte charge exactly")
	var remaining int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM conversation_send_receipts WHERE message_id=?`, first.Message.ID).Scan(&remaining))
	require.Zero(t, remaining)
	fresh, err := lifecycleSend(s, first.Message.ID, "new")
	require.NoError(t, err)
	require.True(t, fresh.Created, "retained old history must not replay a pruned receipt")
	require.NotEqual(t, first.Message.MessageIncarnationID, fresh.Message.MessageIncarnationID)
}

func TestConversationReceiptLifecycleREDLegacyUpgradeBaselinePersists(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "pre082.db")
	migrations := DefaultMigrations()
	var through081 int
	for i, m := range migrations {
		if m.ID == "081_embedded_restart_wake_checked_origin" {
			through081 = i + 1
			break
		}
	}
	require.Positive(t, through081, "fixture needs migration 081")
	legacy, err := Open(ctx, path, Options{Clock: clock, Migrations: migrations[:through081]})
	require.NoError(t, err)
	_, err = lifecycleSend(legacy, "legacy-id", "original")
	require.NoError(t, err)
	beforeCount, beforeBytes := lifecycleUsage(t, legacy)
	for _, id := range []string{"078_conversation_send_receipt", "079_conversation_send_receipt_capacity", "080_embedded_restart_wakes", "081_embedded_restart_wake_checked_origin"} {
		var exists int
		require.NoError(t, legacy.db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE id=?`, id).Scan(&exists))
		require.Equal(t, 1, exists, id)
	}
	require.NoError(t, legacy.Close(), "closing checkpoints this owned WAL fixture before migration")
	require.NoError(t, clock.Advance(60*24*time.Hour))
	through082 := migrationIndex(t, migrations, "082_conversation_send_receipt_lifecycle") + 1
	upgraded, err := Open(ctx, path, Options{Clock: clock, Migrations: migrations[:through082]})
	require.NoError(t, err)
	defer upgraded.Close()
	var applied int
	require.NoError(t, upgraded.db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE id='082_conversation_send_receipt_lifecycle'`).Scan(&applied))
	require.Equal(t, 1, applied, "082 must apply exactly once to the owned pre-082 WAL fixture")
	var acceptedAt, deadlineAt string
	err = upgraded.db.QueryRow(`SELECT accepted_at,deadline_at FROM conversation_send_receipts WHERE message_id='legacy-id'`).Scan(&acceptedAt, &deadlineAt)
	require.NoError(t, err)
	require.Equal(t, formatTimestamp(clock.Now()), acceptedAt)
	require.Equal(t, formatTimestamp(clock.Now().Add(receiptHorizon)), deadlineAt)
	count, bytes := lifecycleUsage(t, upgraded)
	require.Equal(t, beforeCount, count)
	require.Equal(t, beforeBytes, bytes)
	require.NoError(t, upgraded.Close())
	require.NoError(t, clock.Advance(time.Minute))
	reopened, err := Open(ctx, path, Options{Clock: clock, Migrations: migrations[:through082]})
	require.NoError(t, err)
	defer reopened.Close()
	var persisted string
	require.NoError(t, reopened.db.QueryRow(`SELECT accepted_at FROM conversation_send_receipts WHERE message_id='legacy-id'`).Scan(&persisted))
	require.Equal(t, acceptedAt, persisted, "reopen cannot re-baseline a legacy receipt")
	var receiptCount int
	require.NoError(t, reopened.db.QueryRow(`SELECT count(*) FROM conversation_send_receipts`).Scan(&receiptCount))
	require.Equal(t, 1, receiptCount)
}
