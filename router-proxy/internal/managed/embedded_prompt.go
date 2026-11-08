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
	userMsg := m.steerUserMessage(message)
	active.Steer(userMsg)
	log.Printf("[embedded:%s] Successfully steered prompt mid-turn into active session", agentID)
	return m.recordSteeredPrompt(ctx, agentID, message, requestID)
}

func (m *Manager) steerUserMessage(message string) ai.UserMessage {
	nowMilli := time.Now().UnixMilli()
	if m.clock != nil {
		nowMilli = m.clock.Now().UnixMilli()
	}
	return ai.UserMessage{
		Content:   ai.ContentList{ai.TextContent{Text: message}},
		Timestamp: nowMilli,
	}
}

// writeClaudeSteer writes a reserved direct prompt into the active Claude turn.
// It returns handled=false only when nothing reached stdin and the reservation
// was released, so the caller continues on the normal lane. Accepted means
// written and awaiting its checked echo; completion is recorded by the turn's
// result. An uncertain write keeps the request pending, so it is not retryable.
func (m *Manager) writeClaudeSteer(ctx context.Context, agentID, message, requestID string, binding store.EmbeddedBinding, claude *claudeCodeRuntime) (bool, error) {
	outcome, err := claude.steerInput(ctx, m.steerUserMessage(message), nil, requestID)
	switch outcome {
	case claudeSteerAccepted:
		log.Printf("[embedded:%s] Steered Claude prompt mid-turn; awaiting checked echo", agentID)
		return true, m.recordSteeredPrompt(ctx, agentID, message, requestID)
	case claudeSteerRefused:
		// Nothing reached stdin, so the reservation can be released. Detach from the
		// request context so a client disconnect cannot leave it pending.
		release, cancel := context.WithTimeout(context.WithoutCancel(ctx), steerWriteTimeout)
		defer cancel()
		if releaseErr := m.store.ReleaseEmbeddedPrompt(release, binding, requestID); releaseErr != nil {
			return true, releaseErr
		}
		if err != nil {
			return true, err
		}
		return false, nil
	default:
		if err == nil {
			err = &store.ManagedAgentNotReadyError{AgentID: agentID}
		}
		return true, err
	}
}

func (m *Manager) recordSteeredPrompt(ctx context.Context, agentID, message, requestID string) error {
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
	if claude, ok := active.(*claudeCodeRuntime); ok && claude != nil && claude.canSteer() {
		// Direct Claude steering: reserve under admission, then write outside it so
		// a blocked stdin write never stalls other admissions.
		m.mu.Lock()
		owner, owned := m.embeddedOwners[agentID]
		m.mu.Unlock()
		if !owned {
			lane.admission.Unlock()
			return &store.ManagedAgentNotReadyError{AgentID: agentID}
		}
		// The same durable eligibility gate as the normal lane below: a steer must
		// not enter a still-open turn once the store has stopped accepting prompts.
		saved, err := m.store.ManagedAgent(ctx, agentID)
		if err != nil {
			lane.admission.Unlock()
			return err
		}
		if saved.Runtime != "embedded" || saved.LifecycleState != "running" || saved.DesiredState != "running" || !saved.AcceptingPrompts || !saved.SlotHeld {
			lane.admission.Unlock()
			return &store.ManagedAgentNotReadyError{AgentID: agentID}
		}
		replay, err := m.store.ReserveEmbeddedPrompt(ctx, owner.binding, requestID, message)
		lane.admission.Unlock()
		if err != nil || replay {
			return err
		}
		handled, err := m.writeClaudeSteer(ctx, agentID, message, requestID, owner.binding, claude)
		if handled {
			return err
		}
		// Refused before any write and released: continue on the normal lane. Re-take
		// admission so the shared unlock below stays balanced.
		lane.admission.Lock()
	}
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
