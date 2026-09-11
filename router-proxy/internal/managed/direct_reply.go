package managed

import (
	"context"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// DirectReplyPublisher persists and broadcasts a completed managed direct-turn
// response at the router boundary. The manager keeps the process fence and the
// transport keeps the conversation/recipient ownership rules.
type DirectReplyPublisher func(context.Context, store.ManagedDirectAttempt, DirectMailboxReply) error

// SetDirectReplyPublisher installs the router-owned reply path after transport
// construction. A replacement publisher is used only for future responses.
func (m *Manager) SetDirectReplyPublisher(publish DirectReplyPublisher) {
	m.mu.Lock()
	m.directReplyPublisher = publish
	m.mu.Unlock()
}

func (m *Manager) publishDirectReply(attempt store.ManagedDirectAttempt, reply DirectMailboxReply) {
	m.mu.Lock()
	publish := m.directReplyPublisher
	m.mu.Unlock()
	if publish == nil {
		return
	}
	m.startManagedTask(func() {
		if err := publish(context.Background(), attempt, reply); err != nil {
			_ = m.store.RecordManagedDirectDiagnostic(context.Background(), attempt.Fence.AgentID, attempt.MessageID, attempt.AttemptNumber, "reply_persistence_failed", "")
		}
	})
}
