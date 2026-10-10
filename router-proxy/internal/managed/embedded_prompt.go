package managed

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/coding"
)

func (m *Manager) steerPrompt(ctx context.Context, agentID, message, requestID string, active *coding.Session) error {
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
	lane.consecutiveContinuations = 0
	lane.consecutiveEmptyTurns = 0
	lane.goalAttempt = 0

	lane.activeMu.RLock()
	active := lane.activeSession
	lane.activeMu.RUnlock()
	if active != nil {
		return m.steerPrompt(ctx, agentID, message, requestID, active)
	}

	lane.admission.Lock()
	defer lane.admission.Unlock()

	lane.activeMu.RLock()
	active = lane.activeSession
	lane.activeMu.RUnlock()
	if active != nil {
		return m.steerPrompt(ctx, agentID, message, requestID, active)
	}

	lane.serial.Lock()
	defer lane.serial.Unlock()
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
	owner, err := m.ensureEmbeddedOwner(ctx, agentID, *saved)
	if err != nil {
		return &store.ManagedAgentNotReadyError{AgentID: agentID}
	}
	replay, err := m.store.ReserveEmbeddedPrompt(ctx, owner.binding, requestID, message)
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
	if err := m.runEmbeddedSessionPrompt(runCtx, *saved, message); err != nil {
		return err
	}
	if err := m.store.CompleteEmbeddedPrompt(ctx, owner.binding, requestID); err != nil {
		return err
	}
	return nil
}
