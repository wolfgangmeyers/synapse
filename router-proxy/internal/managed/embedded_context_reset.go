package managed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/coding"
)

const ContextResetWakePrompt = "Context reset completed. You are now operating with fresh context headroom. Your recent conversation window has been preserved. Proceed with your next step."
const ContextResetWarningReminder = "Context usage threshold warning: You are approaching the context reset boundary. Please use the scratchpad tool to record your current plan, key findings, and immediate next steps now before context reset occurs."

// DefaultContextWarningPercent and DefaultContextResetPercent mirror store defaults.
const (
	DefaultContextWarningPercent = 10
	DefaultContextResetPercent   = 20
)

// ResolveContextThresholds determines the effective warning and reset percentages for a given model.
// Priority:
// 1. Model override by provider/id
// 2. Model override by id
// 3. Config global thresholds (if > 0)
// 4. Default thresholds (10% warning, 20% reset)
func ResolveContextThresholds(model Model, cfg store.ContextThresholdConfig) (warningPct int, resetPct int) {
	if cfg.ModelThresholds != nil {
		if model.Provider != "" && model.ID != "" {
			fullKey := model.Provider + "/" + model.ID
			if override, ok := cfg.ModelThresholds[fullKey]; ok {
				if override.WarningPercent > 0 && override.ResetPercent > override.WarningPercent {
					return override.WarningPercent, override.ResetPercent
				}
			}
		}
		if model.ID != "" {
			if override, ok := cfg.ModelThresholds[model.ID]; ok {
				if override.WarningPercent > 0 && override.ResetPercent > override.WarningPercent {
					return override.WarningPercent, override.ResetPercent
				}
			}
		}
	}

	if cfg.WarningPercent > 0 && cfg.ResetPercent > cfg.WarningPercent {
		return cfg.WarningPercent, cfg.ResetPercent
	}

	return DefaultContextWarningPercent, DefaultContextResetPercent
}

func FormatContextResetWarningReminder(currentTokens, contextWindow, warningPct, resetPct int) string {
	pct := float64(0)
	if contextWindow > 0 {
		pct = float64(currentTokens) / float64(contextWindow) * 100.0
	}
	return fmt.Sprintf("Context usage warning: You have reached %d / %d tokens (%.1f%%), crossing the %d%% warning threshold. Please immediately update your scratchpad with all key findings, progress, and pending steps, then call context_reset. Your turn will end immediately to reset context with fresh headroom.",
		currentTokens, contextWindow, pct, warningPct)
}

func contextWarningThresholdExceeded(model Model, currentTokens, warningPct int) bool {
	if model.ContextWindow <= 0 || warningPct <= 0 {
		return false
	}
	threshold := model.ContextWindow * warningPct / 100
	return currentTokens >= threshold
}

func ContextWarningThresholdExceeded(model Model, currentTokens, warningPct int) bool {
	return contextWarningThresholdExceeded(model, currentTokens, warningPct)
}

func contextThresholdExceededWithPct(model Model, currentTokens, resetPct int) bool {
	if model.ContextWindow <= 0 || resetPct <= 0 {
		return false
	}
	threshold := model.ContextWindow * resetPct / 100
	return currentTokens >= threshold
}

type managerContextKey struct{}

var defaultManagerInstance *Manager

func embeddedContextResetSchema() *ai.Schema {
	closed := false
	schema := ai.Object(ai.Prop("reason", ai.String("Optional reason for triggering context reset.")))
	schema.AdditionalAllowed = &closed
	return schema
}

func (m *Manager) embeddedContextResetTool(owner embeddedOwner, requested func(string), toolName string) agent.AgentTool {
	return agent.AgentTool{
		Name:        toolName,
		Description: "Trigger a clean context reset to wipe past conversation history down to 0 tokens with fresh headroom while preserving recent actions and scratchpad notes.",
		Parameters:  embeddedContextResetSchema(),
		Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			reason := "worker_requested"
			if r, ok := args["reason"].(string); ok && r != "" {
				reason = r
			}
			requested(reason)
			return agent.AgentToolResult{
				Content: ai.ContentList{
					ai.TextContent{Text: "Context reset initiated. Stopping turn to perform epoch rotation and bootstrap fresh context."},
				},
				Terminate: true,
			}, nil
		},
	}
}

func messageContentText(msg ai.Message) string {
	if u, ok := msg.(ai.UserMessage); ok {
		for _, c := range u.Content {
			if t, ok := c.(ai.TextContent); ok {
				return t.Text
			}
		}
	}
	return ""
}

// ExecuteContextReset performs a clean context reset for the managed embedded agent:
// - Extracts the last 4-6 messages from the current session context.
// - Starts a new session epoch in embedded_sessions (and updates managed_agents).
// - Appends the genesis prompt + scratchpad entries (if any) + the ported last few messages.
// - Immediately dispatches a wake turn to the agent with ContextResetWakePrompt.
func (m *Manager) ExecuteContextReset(ctx context.Context, binding store.EmbeddedBinding, reason string) error {
	saved, err := m.store.ManagedAgent(ctx, binding.AgentID)
	if err != nil {
		return fmt.Errorf("managed agent: %w", err)
	}

	snap, err := m.store.ReopenEmbedded(ctx, binding)
	if err != nil {
		return fmt.Errorf("reopen embedded: %w", err)
	}

	var sessionMessages []agent.AgentMessage
	for _, entry := range snap.Entries {
		msg, err := ai.UnmarshalMessage(entry.Body)
		if err != nil {
			continue
		}
		if msg.MessageRole() != ai.RoleSystem {
			sessionMessages = append(sessionMessages, msg)
		}
	}

	// Port the last few messages (last 4-6 messages / recent turns).
	var portedMessages []agent.AgentMessage
	if len(sessionMessages) <= 6 {
		portedMessages = sessionMessages
	} else {
		startIdx := len(sessionMessages) - 6
		// Ensure we don't start on an orphaned tool result
		for startIdx < len(sessionMessages) && sessionMessages[startIdx].MessageRole() == ai.RoleToolResult {
			startIdx++
		}
		if startIdx < len(sessionMessages) {
			portedMessages = sessionMessages[startIdx:]
		}
	}

	nextEpoch := binding.Epoch + 1
	var nextSessionID string
	if m.ids != nil {
		nextSessionID, _ = m.ids.Next()
	}
	if nextSessionID == "" {
		nextSessionID = fmt.Sprintf("%s-s%d", binding.AgentID, nextEpoch)
	}
	nextBinding := store.EmbeddedBinding{
		AgentID:    binding.AgentID,
		SessionID:  nextSessionID,
		Epoch:      nextEpoch,
		Generation: binding.Generation,
	}

	role, err := m.store.SavedEmbeddedSessionRole(ctx, binding)
	if err != nil {
		role = "worker"
	}

	if err := m.store.ResetEmbeddedSessionEpoch(ctx, binding, nextBinding, role); err != nil {
		return fmt.Errorf("reset embedded session epoch: %w", err)
	}

	identity, err := newEmbeddedOwnerIdentity()
	if err != nil {
		identity = "reset:" + nextBinding.AgentID
	}
	token, err := m.store.AcquireEmbeddedOwner(ctx, nextBinding, identity, embeddedOwnerTTL)
	if err != nil {
		return fmt.Errorf("acquire embedded owner for next binding: %w", err)
	}
	nextBinding.OwnerToken = token

	m.mu.Lock()
	if m.embeddedOwners == nil {
		m.embeddedOwners = make(map[string]embeddedOwner)
	}
	m.embeddedOwners[nextBinding.AgentID] = embeddedOwner{
		binding:  nextBinding,
		identity: identity,
	}
	m.mu.Unlock()

	// Append initial instruction if explicitly set on launch, otherwise do not synthesize a genesis prompt.
	var genesisMsg *ai.UserMessage
	if saved.PendingInitialInstruction != "" {
		genesisText := saved.PendingInitialInstruction
		role, _ := m.store.EmbeddedSessionRole(ctx, binding)
		if role != "root" {
			genesisText = formatGenesisPrompt(saved.AgentID, genesisText)
		}
		msg := ai.UserMessage{
			Content: ai.ContentList{
				ai.TextContent{Text: genesisText},
			},
		}
		genesisBody, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("marshal genesis message: %w", err)
		}
		if _, err := m.store.AppendEmbeddedEntry(ctx, nextBinding, "user", genesisBody); err != nil {
			return fmt.Errorf("append genesis prompt: %w", err)
		}
		genesisMsg = &msg
	}

	// Append scratchpad entries (if any)
	var scratchpadMsg *ai.UserMessage
	scratchpadEntries, totalCount, err := m.store.FetchScratchpadRecent(ctx, binding.AgentID, 10)
	if err == nil && len(scratchpadEntries) > 0 {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("### Persistent Scratchpad Memory (%d of %d entries shown):\n", len(scratchpadEntries), totalCount))
		for _, e := range scratchpadEntries {
			status := "Recent"
			if e.Pinned {
				status = "Pinned"
			}
			sb.WriteString(fmt.Sprintf("- [%s] %s\n", status, e.Content))
		}
		sb.WriteString("\nProceed with your work.")
		msg := ai.UserMessage{
			Content: ai.ContentList{
				ai.TextContent{Text: strings.TrimRight(sb.String(), "\n")},
			},
		}
		scratchpadBody, err := json.Marshal(msg)
		if err == nil {
			if _, err := m.store.AppendEmbeddedEntry(ctx, nextBinding, "user", scratchpadBody); err != nil {
				return fmt.Errorf("append scratchpad handoff: %w", err)
			}
			scratchpadMsg = &msg
		}
	}

	// Append ported recent messages
	for _, msg := range portedMessages {
		body, err := json.Marshal(msg)
		if err != nil {
			continue
		}
		roleKind := string(msg.MessageRole())
		if roleKind == "" {
			roleKind = "user"
		}
		if _, err := m.store.AppendEmbeddedEntry(ctx, nextBinding, roleKind, body); err != nil {
			return fmt.Errorf("append ported message: %w", err)
		}
	}

	// Immediately dispatches a wake turn to the agent
	updatedAgent, err := m.store.ManagedAgent(ctx, nextBinding.AgentID)
	if err != nil {
		return fmt.Errorf("read updated agent: %w", err)
	}

	// Immediately update context usage for the reset agent so the UI immediately sees the reset tokens.
	var resetMessages []agent.AgentMessage
	if genesisMsg != nil {
		resetMessages = append(resetMessages, *genesisMsg)
	}
	if scratchpadMsg != nil {
		resetMessages = append(resetMessages, *scratchpadMsg)
	}
	resetMessages = append(resetMessages, portedMessages...)
	resetTokens := embeddedProjectedTokens(resetMessages)
	resetWindow := 0
	if resolvedModel, err := ResolveModel(updatedAgent.SelectedModel); err == nil && resolvedModel != nil && resolvedModel.ContextWindow > 0 {
		resetWindow = resolvedModel.ContextWindow
	}
	m.updateAgentContextUsage(nextBinding.AgentID, &resetTokens, resetWindow)

	owner := embeddedOwner{binding: nextBinding, identity: identity}
	runCtx := context.WithValue(ctx, embeddedOwnerContextKey{}, owner)

	log.Printf("[embedded:%s] Context reset completed (%s). Dispatched wake turn to epoch %d", binding.AgentID, reason, nextEpoch)
	lane := m.laneFor(binding.AgentID)
	lane.warningEmitted = false
	lane.warningEpoch = nextEpoch

	// Record explicit context_reset entry in managed_complete_history so UI timeline renders the Epoch marker.
	resetKey := fmt.Sprintf("context-reset:%s:%d:%s", binding.AgentID, nextEpoch, uuid.New().String()[:8])
	resetSummary := fmt.Sprintf("Context reset to epoch %d (%s)", nextEpoch, reason)
	resetMsg := fmt.Sprintf("Context reset (Epoch %d)", nextEpoch)
	pres := &store.ManagedHistoryPresentation{
		Kind:     "context_reset",
		ToolName: "context_reset",
		Outcome:  "success",
		Summary:  resetSummary,
	}
	historyEntry, err := m.store.RecordManagedCompleteHistoryWithDetails(
		ctx,
		binding.AgentID,
		"context_reset",
		"info",
		resetMsg,
		resetKey,
		pres,
		map[string]any{"epoch": nextEpoch, "reason": reason},
		&resetSummary,
	)
	if err == nil {
		m.publishCompleteHistory(historyEntry)
	} else {
		log.Printf("[embedded:%s] failed to record context_reset history: %v", binding.AgentID, err)
	}

	if err := m.runEmbeddedSessionPrompt(runCtx, *updatedAgent, ContextResetWakePrompt); err != nil {
		return fmt.Errorf("dispatch context reset wake turn: %w", err)
	}

	m.OfferDirect(binding.AgentID)
	return nil
}

// Package-level ExecuteContextReset wrapper
func ExecuteContextReset(ctx context.Context, binding store.EmbeddedBinding, reason string) error {
	if m, ok := ctx.Value(managerContextKey{}).(*Manager); ok && m != nil {
		return m.ExecuteContextReset(ctx, binding, reason)
	}
	if defaultManagerInstance != nil {
		return defaultManagerInstance.ExecuteContextReset(ctx, binding, reason)
	}
	return errors.New("manager unavailable for ExecuteContextReset")
}

// checkContextResetAfterTurn evaluates threshold monitoring and explicit reset requests after a turn.
func (m *Manager) checkContextResetAfterTurn(ctx context.Context, binding store.EmbeddedBinding, model *ai.Model, session *coding.Session) {
	if session == nil || model == nil {
		return
	}
	m.mu.Lock()
	resetReason, resetRequested := m.contextResetRequested[binding.AgentID]
	if resetRequested {
		delete(m.contextResetRequested, binding.AgentID)
	}
	m.mu.Unlock()

	if resetRequested {
		if err := m.ExecuteContextReset(ctx, binding, resetReason); err != nil {
			log.Printf("[embedded:%s] context reset failed: %v", binding.AgentID, err)
		}
		return
	}

	currentTokens := embeddedProjectedTokens(session.History())
	managedModel := Model{Provider: model.Provider, ID: model.ID, ContextWindow: model.ContextWindow}

	warningPct := DefaultContextWarningPercent
	resetPct := DefaultContextResetPercent
	if m.store != nil {
		if cfg, err := m.store.GetContextThresholdConfig(ctx); err == nil {
			warningPct, resetPct = ResolveContextThresholds(managedModel, cfg)
		}
	}

	if contextThresholdExceededWithPct(managedModel, currentTokens, resetPct) {
		log.Printf("[embedded:%s] Context threshold exceeded: %d tokens (window: %d, reset: %d%%). Triggering auto-reset.",
			binding.AgentID, currentTokens, model.ContextWindow, resetPct)
		if err := m.ExecuteContextReset(ctx, binding, "threshold_exceeded"); err != nil {
			log.Printf("[embedded:%s] auto context reset failed: %v", binding.AgentID, err)
		}
	} else if contextWarningThresholdExceeded(managedModel, currentTokens, warningPct) {
		lane := m.laneFor(binding.AgentID)
		if lane.warningEpoch != binding.Epoch {
			lane.warningEpoch = binding.Epoch
			lane.warningEmitted = false
		}
		if !lane.warningEmitted {
			lane.warningEmitted = true
			log.Printf("[embedded:%s] Emitted pre-reset scratchpad reminder (tokens: %d, warning: %d%%, reset: %d%%)", binding.AgentID, currentTokens, warningPct, resetPct)
			if err := m.emitContextWarningReminder(ctx, binding, currentTokens, model.ContextWindow, warningPct, resetPct); err != nil {
				log.Printf("[embedded:%s] failed to emit pre-reset scratchpad reminder: %v", binding.AgentID, err)
			}
		}
	}
}

func (m *Manager) emitContextWarningReminder(ctx context.Context, binding store.EmbeddedBinding, currentTokens, contextWindow, warningPct, resetPct int) error {
	if m.store == nil {
		return errors.New("store is nil")
	}
	msgID := fmt.Sprintf("warning-reminder-%s-%d-%s", binding.AgentID, binding.Epoch, uuid.New().String()[:8])
	now := formatManagedTime(time.Now())
	if m.clock != nil {
		now = formatManagedTime(m.clock.Now())
	}
	source := protocol.MessageSource{Kind: "reminder"}
	subject := "Context usage threshold warning"
	body := FormatContextResetWarningReminder(currentTokens, contextWindow, warningPct, resetPct)
	envelope := protocol.Envelope{
		ID:        msgID,
		From:      "system",
		To:        binding.AgentID,
		Subject:   &subject,
		Body:      body,
		CreatedAt: now,
		Source:    &source,
	}
	result, err := m.store.Enqueue(ctx, envelope, store.EnqueueOptions{
		Conversation: &store.ConversationOptions{
			Body:   &envelope.Body,
			Source: &source,
		},
	})
	if err != nil {
		return fmt.Errorf("enqueue warning reminder: %w", err)
	}
	m.mu.Lock()
	publish := m.embeddedMailPublisher
	m.mu.Unlock()
	if publish != nil {
		publish(result)
	} else {
		_, _ = m.store.ResolveMessageTarget(ctx, result.Message.ID)
	}

	// Record explicit complete history entry for UI timeline so the warning reminder is immediately visible.
	eventKey := fmt.Sprintf("context-warning:%s:%d:%s", binding.AgentID, binding.Epoch, msgID)
	pres := &store.ManagedHistoryPresentation{
		Kind:    "reminder",
		Outcome: "warning",
		From:    "system",
		To:      binding.AgentID,
		Summary: "Context usage threshold warning",
	}
	historyEntry, histErr := m.store.RecordManagedCompleteHistoryWithDetails(
		ctx,
		binding.AgentID,
		"reminder",
		"info",
		"Context usage threshold warning",
		eventKey,
		pres,
		nil,
		&body,
	)
	if histErr == nil {
		m.publishCompleteHistory(historyEntry)
	} else {
		log.Printf("[embedded:%s] failed to record context warning history: %v", binding.AgentID, histErr)
	}

	m.OfferDirect(binding.AgentID)
	return nil
}
