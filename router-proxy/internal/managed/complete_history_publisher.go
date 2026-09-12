package managed

import "github.com/reflexivity/mecha-developer/router-proxy/internal/store"

// CompleteHistoryPublisher lets the transport publish an already-redacted,
// durable timeline fact. The manager never constructs browser frames.
type CompleteHistoryPublisher func(store.ManagedHistoryEntry)

// SetCompleteHistoryPublisher installs the router-owned timeline wakeup after
// transport construction. It is a notification only; the store remains the
// source of truth for reconnects and history reads.
func (m *Manager) SetCompleteHistoryPublisher(publish CompleteHistoryPublisher) {
	m.mu.Lock()
	m.completeHistoryPublisher = publish
	m.mu.Unlock()
}

func (m *Manager) publishCompleteHistory(entry store.ManagedHistoryEntry) {
	if entry.ID == "" || entry.AgentID == "" {
		return
	}
	m.mu.Lock()
	publish := m.completeHistoryPublisher
	m.mu.Unlock()
	if publish != nil {
		publish(entry)
	}
}
