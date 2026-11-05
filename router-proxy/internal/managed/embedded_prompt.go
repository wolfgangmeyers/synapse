package managed

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
)

// embeddedExplicitUserPromptKey marks only a fresh public user admission, not
// passive mail, workflow activation, reset wakes, or replayed requests.
type embeddedExplicitUserPromptKey struct{}

func (m *Manager) steerPrompt(ctx context.Context, agentID, message, requestID string, active embeddedRuntime) error {
	nowMilli := time.Now().UnixMilli()
	if m.clock != nil {
		nowMilli = m.clock.Now().UnixMilli()
	}
	userMsg := ai.UserMessage{
		Content:   ai.ContentList{ai.TextContent{Text: message}},
		Timestamp: nowMilli,
	}
	active.Steer(userMsg)
	log.Printf("[embedded:%s] Successfully steered prompt mid-turn into active session", agentID)

	from := "user"
	messageText := "Message received"
	summary := ""
	if strings.HasPrefix(message, "[Workflow Activated:") {
		from = "system"
		messageText = "Workflow activated"
		summary = strings.SplitN(strings.TrimPrefix(message, "["), "]", 2)[0]
	}
	entry, err := m.store.RecordManagedCompleteHistoryWithDetails(ctx, agentID, "managed_message", "info", messageText, fmt.Sprintf("steered-prompt:%s:%s", agentID, requestID), &store.ManagedHistoryPresentation{Kind: "message", Outcome: "success", From: from, To: agentID, Summary: summary}, nil, &message)
	if err == nil {
		m.publishCompleteHistory(entry)
	}
	return nil
}

// AdmitPromptWithRequestID is the embedded public turn admission with a durable
// retry identity. Stock RPC admission retains its existing behavior.
func (m *Manager) AdmitPromptWithRequestID(ctx context.Context, agentID, message, requestID string) error {
	initial, err := m.store.ManagedAgent(ctx, agentID)
	if err != nil {
		return err
	}
	if initial.Runtime != "embedded" {
		return m.AdmitPrompt(ctx, agentID, message)
	}
	lane := m.laneFor(agentID)
	lane.consecutiveContinuations.Store(0)
	lane.consecutiveEmptyTurns.Store(0)
	lane.goalAttempt.Store(0)
	// Admission protects only the steer/submit decision, never a whole turn.
	lane.admission.Lock()
	if (lane.resetPending || len(lane.resetToolGates) > 0) || lane.terminationPending > 0 {
		lane.admission.Unlock()
		return &store.ManagedAgentNotReadyError{AgentID: agentID}
	}
	lane.activeMu.RLock()
	active := lane.activeSession
	lane.activeMu.RUnlock()
	if _, claude := active.(*claudeCodeRuntime); active != nil && !claude {
		err := m.steerPrompt(ctx, agentID, message, requestID, active)
		lane.admission.Unlock()
		return err
	}
	lane.admission.Unlock()
	lane.serial.Lock()
	defer lane.serial.Unlock()
	if err := serialLaneTestBarrier(ctx, agentID); err != nil {
		return err
	}
	if lane.embeddedResetPending() {
		return &store.ManagedAgentNotReadyError{AgentID: agentID}
	}
	lane.selection.Lock()
	selectionHeld := true
	defer func() {
		if selectionHeld {
			lane.selection.Unlock()
		}
	}()
	saved, err := m.store.ManagedAgent(ctx, agentID)
	if err != nil {
		return err
	}
	if saved.Runtime != "embedded" {
		return &store.ManagedAgentNotReadyError{AgentID: agentID}
	}
	if saved.LifecycleState != "running" || saved.DesiredState != "running" || !saved.AcceptingPrompts || !saved.SlotHeld {
		return &store.ManagedAgentNotReadyError{AgentID: agentID}
	}
	m.mu.Lock()
	owner, ok := m.embeddedOwners[agentID]
	m.mu.Unlock()
	if !ok {
		return &store.ManagedAgentNotReadyError{AgentID: agentID}
	}
	lane.admission.Lock()
	if (lane.resetPending || len(lane.resetToolGates) > 0) || lane.terminationPending > 0 {
		lane.admission.Unlock()
		return &store.ManagedAgentNotReadyError{AgentID: agentID}
	}
	replay, err := m.store.ReserveEmbeddedPrompt(ctx, owner.binding, requestID, message)
	lane.admission.Unlock()
	lane.selection.Unlock()
	selectionHeld = false
	if err != nil {
		return err
	}
	if replay {
		return nil
	}
	from := "user"
	messageText := "Message received"
	summary := ""
	if strings.HasPrefix(message, "[Workflow Activated:") {
		from = "system"
		messageText = "Workflow activated"
		summary = strings.SplitN(strings.TrimPrefix(message, "["), "]", 2)[0]
	}
	promptEntry, err := m.store.RecordManagedCompleteHistoryWithDetails(
		ctx,
		agentID,
		"managed_message",
		"info",
		messageText,
		fmt.Sprintf("embedded-prompt:%s:%s", agentID, requestID),
		&store.ManagedHistoryPresentation{
			Kind:    "message",
			Outcome: "success",
			From:    from,
			To:      agentID,
			Summary: summary,
		},
		nil,
		&message,
	)
	if err == nil {
		m.publishCompleteHistory(promptEntry)
	}
	turnCtx, cancel := context.WithCancel(m.runCtx)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	runCtx := context.WithValue(turnCtx, embeddedOwnerContextKey{}, owner)
	if from == "user" {
		runCtx = context.WithValue(runCtx, embeddedExplicitUserPromptKey{}, true)
	}
	if err := m.runEmbeddedSessionPrompt(runCtx, *saved, message); err != nil {
		return err
	}
	if err := m.store.CompleteEmbeddedPrompt(ctx, owner.binding, requestID); err != nil {
		return err
	}
	return nil
}
