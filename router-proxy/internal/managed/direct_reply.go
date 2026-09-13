package managed

import (
	"context"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// DirectReplyPublisher persists and broadcasts a completed managed direct-turn
// response at the router boundary. The manager keeps the process fence and the
// transport keeps the conversation/recipient ownership rules.
type DirectReplyPublisher func(context.Context, store.ManagedDirectAttempt, DirectMailboxReply) error

func (m *Manager) SetDirectReplyPublisher(publish DirectReplyPublisher) {
	m.mu.Lock()
	m.directReplyPublisher = publish
	m.mu.Unlock()
}

// publishDirectReply first records the source, then claims exactly one bounded
// outbox attempt. The transport's successful return is the existing broker /
// conversation-store delivery receipt. Its deterministic message identity makes
// recovery after a post-write crash idempotent; the protocol has no browser ACK.
func (m *Manager) publishDirectReply(attempt store.ManagedDirectAttempt, reply DirectMailboxReply) {
	delivery, err := m.store.QueueManagedFinalDelivery(context.Background(), attempt, reply.EntryIdentity, reply.Body)
	if err != nil || delivery == nil {
		if err != nil {
			_ = m.store.RecordManagedDirectDiagnostic(context.Background(), attempt.Fence.AgentID, attempt.MessageID, attempt.AttemptNumber, "reply_persistence_failed", "")
		}
		return
	}
	m.publishManagedFinalDelivery(*delivery)
}

func (m *Manager) publishManagedFinalDelivery(delivery store.ManagedFinalDelivery) {
	m.startManagedTask(func() {
		claimed, err := m.store.ClaimManagedFinalDelivery(context.Background(), delivery)
		if err != nil || claimed == nil {
			return
		}
		m.mu.Lock()
		publish := m.directReplyPublisher
		m.mu.Unlock()
		if publish == nil {
			m.failManagedFinalDelivery(*claimed, "reply_publisher_unavailable")
			return
		}
		reply := DirectMailboxReply{EntryIdentity: claimed.EntryID, Body: claimed.Body}
		if err := publish(context.Background(), claimed.Attempt, reply); err != nil {
			m.failManagedFinalDelivery(*claimed, "reply_transport_failed")
			return
		}
		if _, err := m.store.MarkManagedFinalDeliveryDelivered(context.Background(), *claimed); err != nil {
			// The outbox remains delivering and startup recovery retries the
			// deterministic conversation write; never claim success from persistence.
			_ = m.store.RecordManagedDirectDiagnostic(context.Background(), claimed.Attempt.Fence.AgentID, claimed.Attempt.MessageID, claimed.Attempt.AttemptNumber, "reply_delivery_receipt_persistence_failed", "")
		}
	})
}

func (m *Manager) failManagedFinalDelivery(delivery store.ManagedFinalDelivery, code string) {
	terminal, err := m.store.FailManagedFinalDelivery(context.Background(), delivery, code)
	if err != nil {
		_ = m.store.RecordManagedDirectDiagnostic(context.Background(), delivery.Attempt.Fence.AgentID, delivery.Attempt.MessageID, delivery.Attempt.AttemptNumber, "reply_failure_persistence_failed", "")
		return
	}
	_ = m.store.RecordManagedDirectDiagnostic(context.Background(), delivery.Attempt.Fence.AgentID, delivery.Attempt.MessageID, delivery.Attempt.AttemptNumber, code, "")
	if !terminal {
		m.scheduleManagedFinalDeliveryRetry(delivery.Attempt.Fence.AgentID)
	}
}
