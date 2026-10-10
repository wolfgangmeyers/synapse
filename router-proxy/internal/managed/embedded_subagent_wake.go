package managed

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// SubagentWakeRequestID returns the stable, deterministic prompt request ID
// for a subagent completion wake scoped strictly to childID and attempt.
func SubagentWakeRequestID(childID string, attempt int) string {
	return fmt.Sprintf("subagent-wake-%s-%d", childID, attempt)
}

func isTerminalSubagentStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "stopped", "steered", "aborted", "error", "interrupted", "cancelled":
		return true
	default:
		return false
	}
}

// DeliverSubagentParentWake delivers a completion notification into the parent mailbox,
// fenced by the parent EmbeddedBinding to ensure lineage. It strictly verifies that the
// subagent exists in durable store, has matching attempt/parent lineage, and is in a terminal status.
func (m *Manager) DeliverSubagentParentWake(ctx context.Context, parent store.EmbeddedBinding, record store.ManagedSubagentRecord) error {
	if ctx == nil {
		ctx = m.runCtx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if m.isClosed() {
		return errors.New("manager is closed")
	}
	if m.runCtx != nil && m.runCtx.Err() != nil {
		return m.runCtx.Err()
	}
	if parent.AgentID == "" || parent.SessionID == "" || parent.Epoch <= 0 || parent.Generation == "" {
		return errors.New("invalid parent embedded binding")
	}
	if record.AgentID == "" || record.Attempt <= 0 {
		return errors.New("invalid subagent record for completion wake")
	}

	// Lineage verification on incoming record
	if record.ParentAgentID != "" && record.ParentAgentID != parent.AgentID {
		return fmt.Errorf("subagent parent agent ID mismatch: %q != %q", record.ParentAgentID, parent.AgentID)
	}
	if record.ParentSessionID != "" && record.ParentSessionID != parent.SessionID {
		return fmt.Errorf("subagent parent session ID mismatch: %q != %q", record.ParentSessionID, parent.SessionID)
	}
	if record.ParentEpoch > 0 && record.ParentEpoch != parent.Epoch {
		return fmt.Errorf("subagent parent epoch mismatch: %d != %d", record.ParentEpoch, parent.Epoch)
	}
	if record.ParentGeneration != "" && record.ParentGeneration != parent.Generation {
		return fmt.Errorf("subagent parent generation mismatch: %q != %q", record.ParentGeneration, parent.Generation)
	}

	// Caller's record status must be terminal; non-terminal records are rejected
	if !isTerminalSubagentStatus(record.Status) {
		return fmt.Errorf("cannot deliver completion wake for non-terminal subagent status %q", record.Status)
	}

	// Strictly verify against durable child record in store
	storedChild, err := m.store.ManagedSubagent(ctx, parent, record.AgentID)
	if err != nil {
		return fmt.Errorf("cannot verify subagent %q for parent wake: %w", record.AgentID, err)
	}
	if storedChild == nil {
		return fmt.Errorf("subagent %q not found in durable store", record.AgentID)
	}
	if !isTerminalSubagentStatus(storedChild.Status) {
		return fmt.Errorf("persisted subagent %q is not in terminal status: %q", storedChild.AgentID, storedChild.Status)
	}
	if storedChild.Attempt != record.Attempt {
		return fmt.Errorf("subagent %q attempt mismatch: persisted %d != record %d", storedChild.AgentID, storedChild.Attempt, record.Attempt)
	}
	if storedChild.ParentAgentID != parent.AgentID || storedChild.ParentSessionID != parent.SessionID || storedChild.ParentEpoch != parent.Epoch || storedChild.ParentGeneration != parent.Generation {
		return fmt.Errorf("persisted subagent %q lineage mismatch with parent binding", storedChild.AgentID)
	}

	// Lineage verification on parent binding vs current parent in store
	savedParent, err := m.store.ManagedAgent(ctx, parent.AgentID)
	if err != nil {
		return err
	}
	if savedParent.PiSessionID != parent.SessionID || savedParent.SessionEpoch != parent.Epoch || savedParent.RuntimeGeneration != parent.Generation {
		return fmt.Errorf("stale parent binding: epoch %d != current %d", parent.Epoch, savedParent.SessionEpoch)
	}

	if m.beforeSubagentWakeReservation != nil {
		m.beforeSubagentWakeReservation(parent)
	}

	// Re-check parent in store in case hook or concurrent event changed parent epoch/session
	savedParent, err = m.store.ManagedAgent(ctx, parent.AgentID)
	if err != nil {
		return err
	}
	if savedParent.PiSessionID != parent.SessionID || savedParent.SessionEpoch != parent.Epoch || savedParent.RuntimeGeneration != parent.Generation {
		return fmt.Errorf("stale parent binding after barrier: epoch %d != current %d", parent.Epoch, savedParent.SessionEpoch)
	}

	return m.deliverSubagentMailboxNotificationWithContext(ctx, parent, *storedChild)
}

func (m *Manager) deliverSubagentMailboxNotification(parent store.EmbeddedBinding, record store.ManagedSubagentRecord) {
	ctx := m.runCtx
	if ctx == nil {
		ctx = context.Background()
	}
	_ = m.deliverSubagentMailboxNotificationWithContext(ctx, parent, record)
}

func (m *Manager) deliverSubagentMailboxNotificationWithContext(ctx context.Context, parent store.EmbeddedBinding, record store.ManagedSubagentRecord) error {
	if ctx == nil {
		ctx = context.Background()
	}
	subject := fmt.Sprintf("[System Notice] Subagent Completed: %s", record.AgentID)
	if record.Status != "completed" {
		subject = fmt.Sprintf("[System Notice] Subagent %s: %s", strings.Title(record.Status), record.AgentID)
	}
	body := FormatSubagentWakePrompt(record)
	source := protocol.MessageSource{Kind: "system"}
	msgID := fmt.Sprintf("subagent-completed-%s-%d", record.AgentID, record.Attempt)
	now := formatManagedTime(m.clock.Now())
	envelope := protocol.Envelope{
		ID:        msgID,
		From:      "system",
		To:        parent.AgentID,
		Subject:   &subject,
		Body:      body,
		CreatedAt: now,
		Source:    &source,
	}
	result, err := m.store.Enqueue(ctx, envelope, store.EnqueueOptions{
		Conversation: &store.ConversationOptions{
			Body:   &body,
			Source: &source,
		},
	})
	if err != nil {
		if strings.Contains(err.Error(), "already belongs") || strings.Contains(err.Error(), "already exists") {
			return nil
		}
		return err
	}
	if result.Created || result.ConversationCreated {
		m.mu.Lock()
		publish := m.embeddedMailPublisher
		m.mu.Unlock()
		if publish != nil {
			publish(result)
		} else {
			_, _ = m.store.ResolveMessageTarget(ctx, result.Message.ID)
		}
	}
	m.OfferDirect(parent.AgentID)
	return nil
}

// AsyncDeliverSubagentParentWake dispatches DeliverSubagentParentWake in a managed background task.
func (m *Manager) AsyncDeliverSubagentParentWake(parent store.EmbeddedBinding, record store.ManagedSubagentRecord) {
	m.startManagedTask(func() {
		_ = m.DeliverSubagentParentWake(m.runCtx, parent, record)
	})
}
