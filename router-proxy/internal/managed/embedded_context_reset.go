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

const (
	DefaultPreservedContextWindow = 20
	MaxPreservedContextWindow     = 100
)

type contextResetRequest struct {
	reason         string
	window         int
	id             string
	committed      bool
	handoffBinding *store.EmbeddedBinding // guarded by lane.admission
}

type managerContextKey struct{}

var defaultManagerInstance *Manager

func embeddedContextResetSchema() *ai.Schema {
	closed := false
	schema := ai.Object(
		ai.Opt("reason", ai.String("Optional reason for triggering context reset.")),
		ai.Opt("window", ai.Integer("Optional number of recent messages to preserve across the reset (default 20, max 100).")),
	)
	schema.AdditionalAllowed = &closed
	return schema
}

func (m *Manager) embeddedContextResetTool(owner embeddedOwner, requested func(*contextResetRequest), toolName string) agent.AgentTool {
	return agent.AgentTool{
		Name:        toolName,
		Description: "Trigger a clean context reset to wipe past conversation history down to 0 tokens with fresh headroom while preserving recent actions and scratchpad notes.",
		Parameters:  embeddedContextResetSchema(),
		Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			reason := "worker_requested"
			if r, ok := args["reason"].(string); ok && r != "" {
				reason = r
			}
			window := DefaultPreservedContextWindow
			if w, ok := args["window"]; ok {
				switch val := w.(type) {
				case int:
					if val > 0 {
						window = val
					}
				case int64:
					if val > 0 {
						window = int(val)
					}
				case float64:
					if val > 0 {
						window = int(val)
					}
				case json.Number:
					if n, err := val.Int64(); err == nil && n > 0 {
						window = int(n)
					}
				}
			}
			if window > MaxPreservedContextWindow {
				window = MaxPreservedContextWindow
			}
			req := contextResetRequest{reason: reason, window: window, id: id}
			// Block fresh admissions now, but let checked result callbacks settle.
			lane := m.laneFor(owner.binding.AgentID)
			lane.admission.Lock()
			if len(lane.resetToolGates) != 0 {
				lane.admission.Unlock()
				return agent.AgentToolResult{}, errors.New("context reset already requested")
			}
			if lane.resetToolGates == nil {
				lane.resetToolGates = make(map[*contextResetRequest]struct{})
			}
			lane.resetToolGates[&req] = struct{}{}
			lane.admission.Unlock()
			requested(&req)
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
// - Extracts the last recent messages (default 20, max 100) from the current session context.
// - Starts a new session epoch in embedded_sessions (and updates managed_agents).
// - Appends the genesis prompt + scratchpad entries (if any) + the ported recent messages.
// - Immediately dispatches a wake turn to the agent with ContextResetWakePrompt.
func (m *Manager) ExecuteContextReset(ctx context.Context, binding store.EmbeddedBinding, reason string, window ...int) error {
	lane := m.laneFor(binding.AgentID)
	if err := m.validateResetBinding(ctx, binding); err != nil {
		return err
	}
	active, ok := lane.beginEmbeddedReset(binding)
	if !ok {
		return &store.ManagedAgentNotReadyError{AgentID: binding.AgentID}
	}
	return m.executePendingEmbeddedReset(ctx, binding, reason, active, window...)
}

func (m *Manager) executePendingEmbeddedReset(ctx context.Context, binding store.EmbeddedBinding, reason string, active embeddedRuntime, window ...int) error {
	lane := m.laneFor(binding.AgentID)
	reoffer := true
	defer func() {
		if reoffer {
			m.endEmbeddedReset(binding.AgentID, lane)
		} else {
			lane.endEmbeddedReset()
		}
	}()
	if err := m.validateResetBinding(ctx, binding); err != nil {
		return err
	}
	if active != nil {
		active.Interrupt()
	}
	// Run/Continue join callbacks before their caller releases serial.
	lane.serial.Lock()
	defer lane.serial.Unlock()
	if active != nil {
		active.Close()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.claudeCodeProcesses.stopBinding(binding); err != nil {
		reoffer = false
		return m.recordClaudeStopFailure(binding, err)
	}
	err := m.executeContextResetOnLane(ctx, binding, reason, window...)
	if errors.Is(err, errEmbeddedExecutionUnsettled) {
		reoffer = false
	}
	return err
}

// Called with serial held, only after Run/Continue have joined callbacks.
func (m *Manager) resetEmbeddedContextAfterDrain(ctx context.Context, binding store.EmbeddedBinding, reason string, window ...int) error {
	lane := m.laneFor(binding.AgentID)
	active, ok := lane.beginEmbeddedReset(binding)
	if !ok {
		return &store.ManagedAgentNotReadyError{AgentID: binding.AgentID}
	}
	reoffer := true
	defer func() {
		if reoffer {
			m.endEmbeddedReset(binding.AgentID, lane)
		} else {
			lane.endEmbeddedReset()
		}
	}()
	if err := m.validateResetBinding(ctx, binding); err != nil {
		return err
	}
	if active != nil {
		active.Interrupt()
		active.Close()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.claudeCodeProcesses.stopBinding(binding); err != nil {
		reoffer = false
		return m.recordClaudeStopFailure(binding, err)
	}
	err := m.executeContextResetOnLane(ctx, binding, reason, window...)
	if errors.Is(err, errEmbeddedExecutionUnsettled) {
		reoffer = false
	}
	return err
}

// validateResetBinding checks both the cached runtime owner and the store fence
// before any agent-ID-scoped interrupt or process stop.
func (m *Manager) validateResetBinding(ctx context.Context, binding store.EmbeddedBinding) error {
	m.mu.Lock()
	owner, ok := m.embeddedOwners[binding.AgentID]
	m.mu.Unlock()
	if ok && owner.binding != binding {
		return errors.New("cached embedded owner binding mismatch")
	}
	if m.store == nil { // Synthetic coordinator tests have no store.
		return nil
	}
	_, err := m.store.ReopenEmbedded(ctx, binding)
	return err
}

// Hold launch admission across cleanup and the epoch transaction. Cleanup must
// finish before committing: otherwise a committed epoch has no retryable owner.
func (m *Manager) resetBackgroundBashOwner(binding store.EmbeddedBinding, reset func() error) error {
	m.mu.Lock()
	owner, ok := m.embeddedOwners[binding.AgentID]
	m.mu.Unlock()
	if ok && owner.binding != binding {
		return errors.New("cached embedded owner binding mismatch")
	}
	if !ok {
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, exists := m.embeddedOwners[binding.AgentID]; exists {
			return errors.New("cached embedded owner changed during reset")
		}
		return reset()
	}
	capability := owner.backgroundBash
	if capability != nil {
		capability.mu.Lock()
		defer capability.mu.Unlock()
	}
	m.mu.Lock()
	current, exists := m.embeddedOwners[binding.AgentID]
	jobs := m.backgroundBash
	m.mu.Unlock()
	if !exists || current != owner {
		return errors.New("cached embedded owner changed during reset")
	}
	if jobs != nil {
		if err := jobs.StopOwner(backgroundBashOwnerKey(owner)); err != nil {
			if capability != nil {
				capability.revoked = true
			}
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if current, exists = m.embeddedOwners[binding.AgentID]; !exists || current != owner {
		return errors.New("cached embedded owner changed during reset")
	}
	if err := reset(); err != nil {
		if current, exists := m.embeddedOwners[binding.AgentID]; !exists || current != owner {
			if capability != nil {
				capability.revoked = true
			}
		}
		return err
	}
	if capability != nil {
		capability.revoked = true
	}
	return nil
}

// Requires serial and resetPending. Never snapshot before callback drain.
func (m *Manager) executeContextResetOnLane(ctx context.Context, binding store.EmbeddedBinding, reason string, window ...int) error {
	{
		saved, err := m.store.ManagedAgent(ctx, binding.AgentID)
		if err != nil {
			return fmt.Errorf("managed agent: %w", err)
		}

		snap, err := m.store.ReopenEmbedded(ctx, binding)
		if err != nil {
			return fmt.Errorf("reopen embedded: %w", err)
		}

		var sessionMessages []agent.AgentMessage
		var sessionEntries []store.EmbeddedEntry
		for _, entry := range snap.Entries {
			msg, err := ai.UnmarshalMessage(entry.Body)
			if err != nil {
				continue
			}
			if msg.MessageRole() != ai.RoleSystem {
				sessionMessages = append(sessionMessages, msg)
				sessionEntries = append(sessionEntries, entry)
			}
		}

		targetWindow := DefaultPreservedContextWindow
		if len(window) > 0 && window[0] > 0 {
			targetWindow = window[0]
			if targetWindow > MaxPreservedContextWindow {
				targetWindow = MaxPreservedContextWindow
			}
		}

		// Port the last targetWindow messages (skipping an orphaned leading tool result).
		var portedEntries []store.EmbeddedEntry
		if len(sessionMessages) <= targetWindow {
			portedEntries = sessionEntries
		} else {
			startIdx := len(sessionMessages) - targetWindow
			// Ensure we don't start on an orphaned tool result
			for startIdx < len(sessionMessages) && sessionMessages[startIdx].MessageRole() == ai.RoleToolResult {
				startIdx++
			}
			if startIdx < len(sessionMessages) {
				portedEntries = sessionEntries[startIdx:]
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

		identity, err := newEmbeddedOwnerIdentity()
		if err != nil {
			return fmt.Errorf("create reset owner identity: %w", err)
		}
		var preparedGenesis, preparedScratchpad []byte
		// Append initial instruction if explicitly set on launch, otherwise do not synthesize a genesis prompt.
		if saved.PendingInitialInstruction != "" {
			genesisText := saved.PendingInitialInstruction
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
			preparedGenesis = genesisBody
		}

		// Append scratchpad entries (if any)
		scratchpadEntries, totalCount, err := m.store.FetchScratchpadRecent(ctx, binding.AgentID, 20)
		if err != nil {
			return fmt.Errorf("prepare scratchpad handoff: %w", err)
		}
		if len(scratchpadEntries) > 0 {
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
			if err != nil {
				return fmt.Errorf("marshal scratchpad handoff: %w", err)
			}
			preparedScratchpad = scratchpadBody
		}

		h := directResetSeed{Old: binding, Next: nextBinding, Identity: identity, Role: role, Reason: reason, GenesisBody: preparedGenesis, ScratchpadBody: preparedScratchpad, PortedEntries: portedEntries}
		return m.installDirectReset(ctx, binding, h)
	}
}

func (m *Manager) installDirectReset(ctx context.Context, binding store.EmbeddedBinding, h directResetSeed) error {
	var nextBinding store.EmbeddedBinding
	claim := func() error {
		claimed, err := m.store.ResetEmbeddedSessionEpochClaimWithSeed(ctx, h.Old, h.Next, h.Role, h.Identity, [][]byte{h.GenesisBody, h.ScratchpadBody}, h.PortedEntries)
		if claimed.OwnerToken > 0 {
			nextBinding = claimed
			if m.embeddedOwners == nil {
				m.embeddedOwners = make(map[string]embeddedOwner)
			}
			m.embeddedOwners[claimed.AgentID] = embeddedOwner{binding: claimed, identity: h.Identity, backgroundBash: &backgroundBashOwnerCapability{}}
		} else if err != nil && !errors.Is(err, store.ErrEmbeddedResetRolledBack) {
			// Unknown authority must not leave a potentially retired owner cached.
			delete(m.embeddedOwners, binding.AgentID)
		}
		return err
	}
	if err := m.resetBackgroundBashOwner(binding, claim); err != nil {
		return fmt.Errorf("reset embedded session epoch and cleanup: %w", err)
	}
	var resetMessages []agent.AgentMessage
	for _, body := range [][]byte{h.GenesisBody, h.ScratchpadBody} {
		if len(body) == 0 {
			continue
		}
		msg, err := ai.UnmarshalMessage(body)
		if err != nil {
			return err
		}
		resetMessages = append(resetMessages, msg)
	}
	for _, entry := range h.PortedEntries {
		msg, err := ai.UnmarshalMessage(entry.Body)
		if err != nil {
			return err
		}
		resetMessages = append(resetMessages, msg)
	}
	nextEpoch := nextBinding.Epoch
	reason := h.Reason
	// Immediately dispatches a wake turn to the agent
	updatedAgent, err := m.store.ManagedAgent(ctx, nextBinding.AgentID)
	if err != nil {
		return fmt.Errorf("read updated agent: %w", err)
	}

	// Immediately update context usage for the reset agent so the UI immediately sees the reset tokens.
	resetTokens := embeddedProjectedTokens(resetMessages)
	resetWindow := 0
	if resolvedModel, err := ResolveModel(updatedAgent.SelectedModel); err == nil && resolvedModel != nil && resolvedModel.ContextWindow > 0 {
		resetWindow = resolvedModel.ContextWindow
	}
	m.updateAgentContextUsage(nextBinding.AgentID, &resetTokens, resetWindow)

	log.Printf("[embedded:%s] Context reset prepared (%s), epoch %d", binding.AgentID, reason, nextEpoch)
	lane := m.laneFor(binding.AgentID)
	lane.warningEmitted = false
	lane.warningEpoch = nextEpoch

	// Record explicit context_reset entry in managed_complete_history so UI timeline renders the Epoch marker.
	resetKey := fmt.Sprintf("context-reset:%s:%d:%s", binding.AgentID, nextEpoch, h.Identity)
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

	// The epoch committed and fresh seed is installed. Old correlation is now
	// detached; fresh runtime registration and admissions may proceed.
	lane.activeMu.Lock()
	if lane.activeBinding == binding {
		lane.activeSession = nil
		lane.activePersistence = nil
		lane.activeBinding = store.EmbeddedBinding{}
	}
	lane.activeMu.Unlock()
	lane.reopenEmbeddedResetAdmissionsForBinding(binding)
	owner := embeddedOwner{binding: nextBinding, identity: h.Identity}
	runCtx := context.WithValue(ctx, embeddedOwnerContextKey{}, owner)
	if err := m.runEmbeddedSessionPrompt(runCtx, *updatedAgent, ContextResetWakePrompt); err != nil {
		return fmt.Errorf("dispatch context reset wake turn: %w", err)
	}

	return nil
}

// Package-level ExecuteContextReset wrapper
func ExecuteContextReset(ctx context.Context, binding store.EmbeddedBinding, reason string, window ...int) error {
	if m, ok := ctx.Value(managerContextKey{}).(*Manager); ok && m != nil {
		return m.ExecuteContextReset(ctx, binding, reason, window...)
	}
	if defaultManagerInstance != nil {
		return defaultManagerInstance.ExecuteContextReset(ctx, binding, reason, window...)
	}
	return errors.New("manager unavailable for ExecuteContextReset")
}

// checkContextResetAfterTurn evaluates threshold monitoring and explicit reset requests after a turn.
func (m *Manager) checkContextResetAfterTurn(ctx context.Context, binding store.EmbeddedBinding, model *ai.Model, session embeddedRuntime) {
	if session == nil || model == nil {
		return
	}
	lane := m.laneFor(binding.AgentID)
	lane.admission.Lock()
	toolPending := len(lane.resetToolGates) > 0
	lane.admission.Unlock()
	if toolPending {
		return
	}
	resetRunning := lane.embeddedResetRunning()
	// Only Claude's new-binding wake may monitor usage before the reset
	// coordinator releases ownership. Old-binding draining turns still skip it.
	freshClaudeWake := model.Provider == claudeCodeProvider && resetRunning && !lane.embeddedResetPending()
	if resetRunning && !freshClaudeWake {
		return
	}

	snapshot := session.Snapshot()
	currentTokens := snapshot.ContextTokens
	if model.Provider == claudeCodeProvider && snapshot.ContextWindow > 0 {
		copy := *model
		copy.ContextWindow = snapshot.ContextWindow
		model = &copy
	}
	managedModel := Model{Provider: model.Provider, ID: model.ID, ContextWindow: model.ContextWindow}

	warningPct := DefaultContextWarningPercent
	resetPct := DefaultContextResetPercent
	if m.store != nil {
		if cfg, err := m.store.GetContextThresholdConfig(ctx); err == nil {
			warningPct, resetPct = ResolveContextThresholds(managedModel, cfg)
		}
	}

	resetExceeded := contextThresholdExceededWithPct(managedModel, currentTokens, resetPct)
	suppressReset := false
	if model.Provider == claudeCodeProvider {
		// This is deliberately not a token high-water mark: fresh seeds that
		// cannot fit the configured boundary must not reset-loop as usage grows.
		// A usable higher threshold or CLI compaction rearms normal monitoring.
		if lane.claudeFreshResetGuard != nil && *lane.claudeFreshResetGuard != binding {
			lane.claudeFreshResetGuard = nil
		}
		if model.ContextWindow > 0 && !resetExceeded {
			lane.claudeFreshResetGuard = nil
		}
		if freshClaudeWake && resetExceeded {
			guardBinding := binding
			lane.claudeFreshResetGuard = &guardBinding
		}
		suppressReset = lane.claudeFreshResetGuard != nil
	}

	if resetExceeded && !suppressReset {
		log.Printf("[embedded:%s] Context threshold exceeded: %d tokens (window: %d, reset: %d%%). Triggering auto-reset.",
			binding.AgentID, currentTokens, model.ContextWindow, resetPct)
		if err := m.resetEmbeddedContextAfterDrain(ctx, binding, "threshold_exceeded", DefaultPreservedContextWindow); err != nil {
			log.Printf("[embedded:%s] auto context reset failed: %v", binding.AgentID, err)
		}
	} else if contextWarningThresholdExceeded(managedModel, currentTokens, warningPct) {
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

// finishToolContextReset runs after the runtime has joined its callbacks. It
// releases only this turn's gate; a separate coordinator retains its own gate.
func (m *Manager) finishToolContextReset(ctx context.Context, binding store.EmbeddedBinding, settled bool) {
	m.mu.Lock()
	req := m.contextResetRequested[binding.AgentID]
	if req != nil {
		delete(m.contextResetRequested, binding.AgentID)
	}
	m.mu.Unlock()
	if req == nil {
		return
	}
	m.finishCommittedToolReset(ctx, binding, req, settled)
}

// Caller holds serial; a pending coordinator is joined asynchronously.
func (m *Manager) finishCommittedToolReset(ctx context.Context, binding store.EmbeddedBinding, req *contextResetRequest, settled bool) {
	lane := m.laneFor(binding.AgentID)
	releaseGate := true
	defer func() {
		if releaseGate {
			lane.admission.Lock()
			delete(lane.resetToolGates, req)
			lane.admission.Unlock()
			m.offerEmbeddedMail(binding.AgentID)
		}
	}()
	if !req.committed || !settled {
		return
	}
	lane.admission.Lock()
	wait := lane.resetDone
	if wait != nil {
		oldBinding := binding
		req.handoffBinding = &oldBinding
	}
	if wait == nil {
		lane.resetPending = true
		lane.resetRunning = true
		lane.resetDone = make(chan struct{})
	}
	lane.admission.Unlock()
	if wait != nil {
		// The current coordinator may only be provisional. Keep this exact
		// request's gate until it finishes; never wait while holding serial.
		if m.startManagedTask(func() {
			<-wait
			lane.serial.Lock()
			defer lane.serial.Unlock()
			current, err := m.store.ManagedAgent(ctx, binding.AgentID)
			if err != nil || current.SessionEpoch != binding.Epoch || current.PiSessionID != binding.SessionID || current.RuntimeGeneration != binding.Generation {
				lane.admission.Lock()
				delete(lane.resetToolGates, req)
				lane.admission.Unlock()
				m.offerEmbeddedMail(binding.AgentID)
				return
			}
			m.finishCommittedToolReset(ctx, binding, req, true)
		}) {
			// Ownership moved to the task, not the provisional coordinator.
			releaseGate = false
			return
		}
		return
	}

	// Ownership transferred atomically under admission above.
	lane.activeMu.RLock()
	var active embeddedRuntime
	if lane.activeBinding == binding {
		active = lane.activeSession
	}
	lane.activeMu.RUnlock()
	lane.admission.Lock()
	delete(lane.resetToolGates, req)
	lane.admission.Unlock()
	// The coordinator now owns the release and its single re-offer.
	releaseGate = false
	reoffer := true
	defer func() {
		if reoffer {
			m.endEmbeddedReset(binding.AgentID, lane)
		} else {
			lane.endEmbeddedReset()
		}
	}()
	if active != nil {
		active.Interrupt()
		active.Close()
	}
	if err := ctx.Err(); err != nil {
		return
	}
	if err := m.claudeCodeProcesses.stopBinding(binding); err != nil {
		reoffer = false
		log.Printf("[embedded:%s] context reset stop failed: %v", binding.AgentID, m.recordClaudeStopFailure(binding, err))
		return
	}
	if err := m.executeContextResetOnLane(ctx, binding, req.reason, req.window); err != nil {
		if errors.Is(err, errEmbeddedExecutionUnsettled) {
			reoffer = false
		}
		log.Printf("[embedded:%s] context reset failed: %v", binding.AgentID, err)
	}
}
