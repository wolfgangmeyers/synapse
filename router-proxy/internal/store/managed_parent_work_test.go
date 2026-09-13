package store

import (
	"context"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/stretchr/testify/require"
)

func parentWorkAttempt(fence ManagedRuntimeFence, id string) ManagedDirectAttempt {
	return ManagedDirectAttempt{MessageID: id, MessageIncarnationID: id + "-incarnation", AttemptNumber: 1, Fence: fence, Message: domain.Message{ID: id, From: "wolfgang", To: fence.AgentID}}
}

func openParentWorkForTest(t *testing.T, durable *Store, attempt ManagedDirectAttempt) {
	t.Helper()
	require.NoError(t, durable.runImmediate(context.Background(), func() error {
		return durable.openManagedParentWorkTx(context.Background(), attempt, formatTimestamp(durable.clock.Now()))
	}))
}

func parentWorkState(t *testing.T, durable *Store, attempt ManagedDirectAttempt) string {
	t.Helper()
	var state string
	require.NoError(t, durable.db.QueryRow(`SELECT state FROM managed_parent_work WHERE message_id=? AND message_incarnation_id=? AND attempt_number=?`, attempt.MessageID, attempt.MessageIncarnationID, attempt.AttemptNumber).Scan(&state))
	return state
}

func TestManagedFinalDeliveryConvergesReplyAndSettlementInEitherOrder(t *testing.T) {
	ctx := context.Background()
	for _, first := range []string{"reply", "settled"} {
		t.Run(first, func(t *testing.T) {
			durable, fence := readyParentCompactionFixture(t)
			attempt := parentWorkAttempt(fence, "final-"+first)
			openParentWorkForTest(t, durable, attempt)
			var delivery *ManagedFinalDelivery
			if first == "reply" {
				var err error
				delivery, err = durable.QueueManagedFinalDelivery(ctx, attempt, "assistant-entry", "final")
				require.NoError(t, err)
				require.NoError(t, durable.SettleManagedParentDirectTurn(ctx, attempt))
			} else {
				require.NoError(t, durable.SettleManagedParentDirectTurn(ctx, attempt))
				var err error
				delivery, err = durable.QueueManagedFinalDelivery(ctx, attempt, "assistant-entry", "final")
				require.NoError(t, err)
			}
			require.NotNil(t, delivery)
			claimed, err := durable.ClaimManagedFinalDelivery(ctx, *delivery)
			require.NoError(t, err)
			require.NotNil(t, claimed)
			changed, err := durable.MarkManagedFinalDeliveryDelivered(ctx, *claimed)
			require.NoError(t, err)
			require.True(t, changed)
			require.Equal(t, "settled", parentWorkState(t, durable, attempt))
			agent, err := durable.ManagedAgent(ctx, fence.AgentID)
			require.NoError(t, err)
			require.Equal(t, "idle", agent.ActivityState)
		})
	}
}

func TestManagedFinalDeliveryFailureIsBoundedAndInterruptClosesExactFence(t *testing.T) {
	ctx := context.Background()
	durable, fence := readyParentCompactionFixture(t)
	attempt := parentWorkAttempt(fence, "final-failure")
	openParentWorkForTest(t, durable, attempt)
	delivery, err := durable.QueueManagedFinalDelivery(ctx, attempt, "assistant-entry", "final")
	require.NoError(t, err)
	for want := 1; want <= managedFinalDeliveryMaxAttempts; want++ {
		claimed, err := durable.ClaimManagedFinalDelivery(ctx, *delivery)
		require.NoError(t, err)
		require.NotNil(t, claimed)
		terminal, err := durable.FailManagedFinalDelivery(ctx, *claimed, "transport_failed")
		require.NoError(t, err)
		require.Equal(t, want == managedFinalDeliveryMaxAttempts, terminal)
		if !terminal {
			_, err = durable.db.Exec(`UPDATE managed_parent_final_deliveries SET next_attempt_at='1970-01-01T00:00:00.000Z' WHERE delivery_id=?`, delivery.DeliveryID)
			require.NoError(t, err)
		}
	}
	require.Equal(t, "final_delivery_failed", parentWorkState(t, durable, attempt))
	agent, err := durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", agent.ActivityState)

	other := parentWorkAttempt(fence, "final-interrupt")
	openParentWorkForTest(t, durable, other)
	_, err = durable.QueueManagedFinalDelivery(ctx, other, "assistant-entry-two", "final")
	require.NoError(t, err)
	require.NoError(t, durable.InterruptManagedParentWork(ctx, fence))
	require.Equal(t, "interrupted", parentWorkState(t, durable, other))
}
