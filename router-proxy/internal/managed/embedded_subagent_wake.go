package managed

import (
	"context"
	"errors"
	"fmt"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// SubagentWakeRequestID returns the stable, deterministic prompt request ID
// for a subagent completion wake scoped strictly to childID and attempt.
func SubagentWakeRequestID(childID string, attempt int) string {
	return fmt.Sprintf("subagent-wake-%s-%d", childID, attempt)
}

func isTerminalSubagentStatus(status string) bool {
	switch status {
	case "completed", "steered", "aborted", "stopped", "error", "interrupted":
		return true
	default:
		return false
	}
}

// DeliverSubagentParentWake delivers a completion notification into the parent's
// serial lane, atomically fenced by the original parent EmbeddedBinding to prevent TOCTOU.
// It strictly verifies that the subagent exists in durable store, has matching attempt/parent
// lineage, and is in a terminal status. It formats the notification from the persisted child
// record and ensures exact-once execution per child attempt using a durable prompt request ID.
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

	// Strictly verify against durable child record in store before touching parent lane.
	// Nonexistent, foreign, or unpersisted child IDs are rejected here.
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

	lane := m.laneFor(parent.AgentID)
	lane.admission.Lock()
	defer lane.admission.Unlock()
	lane.serial.Lock()
	defer lane.serial.Unlock()

	lane.selection.Lock()
	selectionHeld := true
	defer func() {
		if selectionHeld {
			lane.selection.Unlock()
		}
	}()

	saved, err := m.store.ManagedAgent(ctx, parent.AgentID)
	if err != nil {
		return err
	}
	if saved.Runtime != "embedded" {
		return &store.ManagedAgentNotReadyError{AgentID: parent.AgentID}
	}
	if saved.LifecycleState != "running" || saved.DesiredState != "running" || !saved.AcceptingPrompts || !saved.SlotHeld {
		return &store.ManagedAgentNotReadyError{AgentID: parent.AgentID}
	}
	// Initial in-memory fence check against expected parent binding
	if saved.PiSessionID != parent.SessionID || saved.SessionEpoch != parent.Epoch || saved.RuntimeGeneration != parent.Generation {
		return &store.ManagedAgentNotReadyError{AgentID: parent.AgentID}
	}

	m.mu.Lock()
	owner, ok := m.embeddedOwners[parent.AgentID]
	closed := m.closed
	m.mu.Unlock()
	if !ok || closed || (m.runCtx != nil && m.runCtx.Err() != nil) {
		return &store.ManagedAgentNotReadyError{AgentID: parent.AgentID}
	}
	if owner.binding.AgentID != parent.AgentID || owner.binding.SessionID != parent.SessionID || owner.binding.Epoch != parent.Epoch || owner.binding.Generation != parent.Generation {
		return &store.ManagedAgentNotReadyError{AgentID: parent.AgentID}
	}
	if parent.OwnerToken > 0 && owner.binding.OwnerToken != parent.OwnerToken {
		return &store.ManagedAgentNotReadyError{AgentID: parent.AgentID}
	}

	if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
		return &store.ManagedAgentNotReadyError{AgentID: parent.AgentID}
	}

	// Deterministic barrier hook for testing race conditions (e.g. concurrent epoch reset)
	// before the atomic store reservation commit.
	if m.beforeSubagentWakeReservation != nil {
		m.beforeSubagentWakeReservation(parent)
	}

	// Format notification from the authentic persisted child record, not forged caller stats
	notification := FormatSubagentNotification(*storedChild)
	reqID := SubagentWakeRequestID(storedChild.AgentID, storedChild.Attempt)

	// ReserveEmbeddedPrompt executes inside an immediate SQLite transaction, performing
	// embeddedFence(owner.binding) against both managed_agents and embedded_sessions tables.
	// If the parent epoch, session, or owner token changed concurrently, it fails atomically here.
	// On replay (prompt request exists in state 'complete' with durable assistant head), it returns
	// replay = true, allowing exact-once deduplication without executing another turn.
	replay, err := m.store.ReserveEmbeddedPrompt(ctx, owner.binding, reqID, notification)
	lane.selection.Unlock()
	selectionHeld = false
	if err != nil {
		return err
	}
	if replay {
		// Exact-once deduplication: prompt request was already executed and completed in store.
		return nil
	}

	baseCtx := m.runCtx
	if baseCtx == nil {
		baseCtx = ctx
	}
	turnCtx, cancel := context.WithCancel(baseCtx)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()

	runCtx := context.WithValue(turnCtx, embeddedOwnerContextKey{}, owner)
	if err := m.runEmbeddedSessionPrompt(runCtx, *saved, notification); err != nil {
		return err
	}
	return m.store.CompleteEmbeddedPrompt(ctx, owner.binding, reqID)
}

// AsyncDeliverSubagentParentWake dispatches DeliverSubagentParentWake in a managed background task.
// If the parent is restarting or not ready, it delivers the completion notification to the parent mailbox.
func (m *Manager) AsyncDeliverSubagentParentWake(parent store.EmbeddedBinding, record store.ManagedSubagentRecord) {
	m.startManagedTask(func() {
		if err := m.DeliverSubagentParentWake(m.runCtx, parent, record); err != nil {
			m.deliverSubagentMailboxNotification(parent, record)
		}
	})
}

func (m *Manager) deliverSubagentMailboxNotification(parent store.EmbeddedBinding, record store.ManagedSubagentRecord) {
	subject := fmt.Sprintf("Subagent %s Completed", record.AgentID)
	if record.Status != "completed" && record.Status != "steered" {
		subject = fmt.Sprintf("Subagent %s %s", record.AgentID, record.Status)
	}
	body := FormatSubagentNotification(record)
	source := protocol.MessageSource{Kind: "system"}
	msgID := fmt.Sprintf("subagent-completed-%s-%d", record.AgentID, record.Attempt)
	nowText := formatManagedTime(m.clock.Now())
	envelope := protocol.Envelope{
		ID:        msgID,
		From:      "supervisor",
		To:        parent.AgentID,
		Subject:   &subject,
		Body:      body,
		CreatedAt: nowText,
		Source:    &source,
	}
	result, err := m.store.Enqueue(context.Background(), envelope, store.EnqueueOptions{
		Conversation: &store.ConversationOptions{
			Body:   &body,
			Source: &source,
		},
	})
	if err == nil && (result.Created || result.ConversationCreated) {
		m.mu.Lock()
		publish := m.embeddedMailPublisher
		m.mu.Unlock()
		if publish != nil {
			publish(result)
		} else {
			_, _ = m.store.ResolveMessageTarget(context.Background(), result.Message.ID)
		}
		m.OfferDirect(parent.AgentID)
	}
}
