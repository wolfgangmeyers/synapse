package managed

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/systemone"
)

// EvaluateCondition evaluates a comparison between actual (from System 1 answer) and condition_threshold using operator.
func EvaluateCondition(operator string, thresholdStr string, actual float64) bool {
	thresh, err := strconv.ParseFloat(strings.TrimSpace(thresholdStr), 64)
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(operator)) {
	case "gte", ">=":
		return actual >= thresh
	case "gt", ">":
		return actual > thresh
	case "lte", "<=":
		return actual <= thresh
	case "lt", "<":
		return actual < thresh
	case "eq", "==":
		return actual == thresh
	default:
		return false
	}
}

// EvaluateChoiceCondition checks if actualChoice matches expected threshold or options.
func EvaluateChoiceCondition(operator, threshold string, actualChoice string) bool {
	op := strings.ToLower(strings.TrimSpace(operator))
	thresh := strings.TrimSpace(threshold)
	actual := strings.TrimSpace(actualChoice)
	switch op {
	case "eq", "==":
		return strings.EqualFold(thresh, actual)
	case "neq", "!=":
		return !strings.EqualFold(thresh, actual)
	default:
		return strings.EqualFold(thresh, actual)
	}
}

// SweepAgentMonitors checks and runs due agent monitors.
func (m *Manager) SweepAgentMonitors(ctx context.Context) (int, int) {
	if m.store == nil {
		return 0, 0
	}

	now := time.Now()
	if m.clock != nil {
		now = m.clock.Now()
	}
	due, err := m.store.QueryDueManagedAgentMonitors(ctx, now, 50)
	if err != nil || len(due) == 0 {
		return 0, 0
	}

	firedCount, evaluatedCount := 0, 0
	for _, mon := range due {
		if !mon.Enabled {
			continue
		}
		fired, err := m.evaluateSingleMonitor(ctx, mon, now)
		if err != nil {
			log.Printf("[agent_monitors:%s:%s] evaluation failed: %v", mon.AgentID, mon.ID, err)
			continue
		}
		evaluatedCount++
		if fired {
			firedCount++
		}
	}
	return firedCount, evaluatedCount
}

// EvaluateSingleMonitor evaluates a single monitor immediately (for manual/API trigger or sweep).
func (m *Manager) EvaluateSingleMonitor(ctx context.Context, mon *store.ManagedAgentMonitor, now time.Time) (bool, error) {
	return m.evaluateSingleMonitor(ctx, mon, now)
}

func (m *Manager) evaluateSingleMonitor(ctx context.Context, mon *store.ManagedAgentMonitor, now time.Time) (bool, error) {
	// 0. Cooldown check: if last_fired_at is within cooldown, skip evaluation and do not fire
	if mon.LastFiredAt != nil && *mon.LastFiredAt != "" {
		if lastFired, err := time.Parse(time.RFC3339Nano, *mon.LastFiredAt); err == nil {
			cooldown := mon.CooldownSeconds
			if cooldown <= 0 {
				cooldown = 120
			}
			if now.Before(lastFired.Add(time.Duration(cooldown) * time.Second)) {
				// Still within cooldown period, skip evaluation
				return false, nil
			}
		}
	}

	// 0b. Max alerts check: if consecutive_alerts >= max_alerts, skip evaluation
	if mon.MaxAlerts > 0 && mon.ConsecutiveAlerts >= mon.MaxAlerts {
		// Already reached max alerts, skip evaluation
		return false, nil
	}

	// 1. Check if agent is currently running and active/idle
	agent, err := m.store.ManagedAgent(ctx, mon.AgentID)
	if err != nil || agent == nil {
		return false, fmt.Errorf("agent %s not found", mon.AgentID)
	}
	if agent.LifecycleState != "running" {
		// Agent not running, skip evaluation and reschedule
		nextEval := now.Add(time.Duration(mon.IntervalSeconds) * time.Second)
		_ = m.store.RecordManagedAgentMonitorEvaluated(ctx, mon.ID, now, nextEval, mon.LastEvaluatedSequence)
		return false, nil
	}

	// 2. Read trailing conversation / history messages across all peers since mon.LastEvaluatedSequence
	trailingCount := mon.TrailingMessagesCount
	if trailingCount <= 0 {
		trailingCount = 6
	}

	messages, err := m.store.RecentAgentMessagesSince(ctx, mon.AgentID, mon.LastEvaluatedSequence, trailingCount)
	if err != nil {
		return false, fmt.Errorf("fetch recent agent messages: %w", err)
	}

	// IF count == 0: SKIP evaluation completely! Do not call System 1. Update next_eval_at to avoid immediate tight loop, but do NOT fire.
	if len(messages) == 0 {
		nextEval := now.Add(time.Duration(mon.IntervalSeconds) * time.Second)
		_ = m.store.RecordManagedAgentMonitorEvaluated(ctx, mon.ID, now, nextEval, mon.LastEvaluatedSequence)
		return false, nil
	}

	maxSeq := mon.LastEvaluatedSequence
	for _, msg := range messages {
		if msg.Sequence > maxSeq {
			maxSeq = msg.Sequence
		}
	}

	stateStr := m.formatTranscript(mon, messages)

	// 3. Prepare System 1 Question
	qKey := "mon_q"
	qPayload := map[string]any{
		"type":         mon.QuestionType,
		"instructions": mon.QuestionInstructions,
	}
	if len(mon.QuestionCriteria) > 0 && string(mon.QuestionCriteria) != "{}" && string(mon.QuestionCriteria) != "null" {
		var crit any
		if json.Unmarshal(mon.QuestionCriteria, &crit) == nil && crit != nil {
			qPayload["criteria"] = crit
		}
	}
	if len(mon.QuestionOptions) > 0 && string(mon.QuestionOptions) != "[]" && string(mon.QuestionOptions) != "null" {
		var opts any
		if json.Unmarshal(mon.QuestionOptions, &opts) == nil && opts != nil {
			qPayload["options"] = opts
		}
	}

	sysReq := systemone.Request{
		Model: "clef",
		State: stateStr,
		Questions: map[string]any{
			qKey: qPayload,
		},
	}

	// 4. Call System 1
	sysCfg, cfgErr := m.store.GetSystemOneConfig(ctx)
	if cfgErr != nil || !sysCfg.Enabled || sysCfg.Host == "" || sysCfg.Port <= 0 {
		// System 1 not available. Check fallback mode:
		if mon.FallbackMode == "alert" {
			// Trigger alert anyway
			return m.fireMonitorAlert(ctx, mon, now, "Fallback alert (System 1 unavailable)", maxSeq)
		}
		// Default "skip": reschedule
		nextEval := now.Add(time.Duration(mon.IntervalSeconds) * time.Second)
		_ = m.store.RecordManagedAgentMonitorEvaluated(ctx, mon.ID, now, nextEval, maxSeq)
		return false, nil
	}

	client := systemone.NewClient(&http.Client{Timeout: 5 * time.Second})
	cfg := systemone.Config{
		Host:    sysCfg.Host,
		Port:    sysCfg.Port,
		Enabled: sysCfg.Enabled,
	}

	resp, err := client.Invoke(ctx, cfg, sysReq)
	if err != nil {
		if mon.FallbackMode == "alert" {
			return m.fireMonitorAlert(ctx, mon, now, fmt.Sprintf("Fallback alert (System 1 error: %v)", err), maxSeq)
		}
		nextEval := now.Add(time.Duration(mon.IntervalSeconds) * time.Second)
		_ = m.store.RecordManagedAgentMonitorEvaluated(ctx, mon.ID, now, nextEval, maxSeq)
		return false, nil
	}

	// 5. Evaluate condition
	conditionMet := false
	switch strings.ToLower(mon.QuestionType) {
	case "noul":
		val, err := resp.Noul(qKey)
		if err == nil {
			conditionMet = EvaluateCondition(mon.ConditionOperator, mon.ConditionThreshold, val)
		}
	case "score":
		val, err := resp.Score(qKey)
		if err == nil {
			conditionMet = EvaluateCondition(mon.ConditionOperator, mon.ConditionThreshold, val)
		}
	case "choice":
		choice, err := resp.Choice(qKey)
		if err == nil {
			conditionMet = EvaluateChoiceCondition(mon.ConditionOperator, mon.ConditionThreshold, choice)
		}
	}

	if !conditionMet {
		// Condition not met -> record evaluation, reset consecutive alerts, schedule next interval
		nextEval := now.Add(time.Duration(mon.IntervalSeconds) * time.Second)
		_ = m.store.RecordManagedAgentMonitorEvaluated(ctx, mon.ID, now, nextEval, maxSeq)
		return false, nil
	}

	// 6. Condition met: check max alerts
	if mon.MaxAlerts > 0 && mon.ConsecutiveAlerts >= mon.MaxAlerts {
		// Suppress alert, schedule next interval
		nextEval := now.Add(time.Duration(mon.IntervalSeconds) * time.Second)
		_ = m.store.RecordManagedAgentMonitorEvaluated(ctx, mon.ID, now, nextEval, maxSeq)
		return false, nil
	}

	// Fire alert
	return m.fireMonitorAlert(ctx, mon, now, "", maxSeq)
}

func (m *Manager) fireMonitorAlert(ctx context.Context, mon *store.ManagedAgentMonitor, now time.Time, fallbackNote string, lastEvalSeq ...int64) (bool, error) {
	cooldown := mon.CooldownSeconds
	if cooldown <= 0 {
		cooldown = 120
	}
	nextEval := now.Add(time.Duration(cooldown) * time.Second)

	// Update store state for monitor fired
	if err := m.store.RecordManagedAgentMonitorFired(ctx, mon.ID, now, nextEval, lastEvalSeq...); err != nil {
		return false, err
	}

	// Format nudge body
	nudgeText := mon.NudgeTemplate
	if strings.TrimSpace(nudgeText) == "" {
		nudgeText = fmt.Sprintf("Monitor alert: %s triggered condition.", mon.Name)
	}
	if fallbackNote != "" {
		nudgeText = fmt.Sprintf("%s\n\n[%s]", nudgeText, fallbackNote)
	}

	// Deliver nudge via agent mailbox
	msgID := fmt.Sprintf("mon-alert-%s-%d-%s", mon.AgentID, now.Unix(), uuid.New().String()[:8])
	nowStr := now.UTC().Format("2006-01-02T15:04:05.000Z")
	source := protocol.MessageSource{Kind: "reminder"}
	subject := fmt.Sprintf("System 1 Monitor: %s", mon.Name)
	envelope := protocol.Envelope{
		ID:        msgID,
		From:      "supervisor",
		To:        mon.AgentID,
		Subject:   &subject,
		Body:      nudgeText,
		CreatedAt: nowStr,
		Source:    &source,
	}

	result, err := m.store.Enqueue(ctx, envelope, store.EnqueueOptions{
		Conversation: &store.ConversationOptions{
			Body:   &envelope.Body,
			Source: &source,
		},
	})
	if err != nil {
		log.Printf("[agent_monitors:%s:%s] failed to enqueue monitor nudge: %v", mon.AgentID, mon.ID, err)
		return true, nil
	}

	m.mu.Lock()
	publish := m.embeddedMailPublisher
	m.mu.Unlock()
	if publish != nil {
		publish(result)
	} else {
		_, _ = m.store.ResolveMessageTarget(ctx, result.Message.ID)
	}

	m.OfferDirect(mon.AgentID)
	return true, nil
}

func (m *Manager) formatTranscript(mon *store.ManagedAgentMonitor, messages []domain.ConversationMessage) string {
	var stateBuilder strings.Builder
	stateBuilder.WriteString(fmt.Sprintf("Agent: %s\n", mon.AgentID))
	if mon.WorkflowStateID != nil && *mon.WorkflowStateID != "" {
		stateBuilder.WriteString(fmt.Sprintf("Workflow State: %s\n", *mon.WorkflowStateID))
	}
	stateBuilder.WriteString("Recent transcript:\n")

	if len(messages) == 0 {
		stateBuilder.WriteString("(No recent messages)\n")
		return stateBuilder.String()
	}

	for _, msg := range messages {
		stateBuilder.WriteString(fmt.Sprintf("[%s -> %s]: %s\n", msg.From, msg.To, msg.Body))
	}
	return stateBuilder.String()
}

func (m *Manager) extractTranscript(ctx context.Context, mon *store.ManagedAgentMonitor, limit int) string {
	messages, err := m.store.RecentAgentMessages(ctx, mon.AgentID, limit)
	if err != nil {
		messages = nil
	}
	return m.formatTranscript(mon, messages)
}

// SpawnWorkflowStateMonitors creates monitors for an agent based on a workflow state's monitor definitions,
// performing variable substitution on template fields.
func (m *Manager) SpawnWorkflowStateMonitors(ctx context.Context, agentID string, state *store.WorkflowState, vars ...map[string]string) error {
	if state == nil || len(state.Monitors) == 0 {
		return nil
	}

	var varMap map[string]string
	if len(vars) > 0 {
		varMap = vars[0]
	}

	nowTime := time.Now()
	if m.clock != nil {
		nowTime = m.clock.Now()
	}
	nowStr := nowTime.UTC().Format("2006-01-02T15:04:05.000Z")

	for _, cfg := range state.Monitors {
		monID := fmt.Sprintf("wfmon-%s-%s-%d", truncateSafe(agentID, 12), truncateSafe(cfg.ID, 8), nowTime.UnixNano()%1000000)
		if len(monID) > 99 {
			monID = monID[:99]
		}

		name := cfg.Name
		if m.store != nil {
			name = m.store.SubstituteAndExpandText(ctx, name, varMap)
		} else if varMap != nil {
			name = store.SubstituteVariables(name, varMap)
		}

		desc := cfg.Description
		if desc != "" {
			if m.store != nil {
				desc = m.store.SubstituteAndExpandText(ctx, desc, varMap)
			} else if varMap != nil {
				desc = store.SubstituteVariables(desc, varMap)
			}
		}

		instructions := cfg.QuestionInstructions
		if m.store != nil {
			instructions = m.store.SubstituteAndExpandText(ctx, instructions, varMap)
		} else if varMap != nil {
			instructions = store.SubstituteVariables(instructions, varMap)
		}

		nudge := cfg.NudgeTemplate
		if m.store != nil {
			nudge = m.store.SubstituteAndExpandText(ctx, nudge, varMap)
		} else if varMap != nil {
			nudge = store.SubstituteVariables(nudge, varMap)
		}

		interval := cfg.IntervalSeconds
		if interval <= 0 {
			interval = 30
		}
		cooldown := cfg.CooldownSeconds
		if cooldown <= 0 {
			cooldown = 120
		}
		trailing := cfg.TrailingMessagesCount
		if trailing <= 0 {
			trailing = 6
		}
		qType := cfg.QuestionType
		if qType == "" {
			qType = "noul"
		}
		op := cfg.ConditionOperator
		if op == "" {
			op = "gte"
		}
		th := cfg.ConditionThreshold
		if th == "" {
			th = "0.8"
		}
		fb := cfg.FallbackMode
		if fb == "" {
			fb = "skip"
		}

		critJSON := cfg.QuestionCriteria
		if len(critJSON) == 0 || string(critJSON) == "null" {
			critJSON = json.RawMessage("{}")
		}
		optsJSON := cfg.QuestionOptions
		if len(optsJSON) == 0 || string(optsJSON) == "null" {
			optsJSON = json.RawMessage("[]")
		}

		stateID := state.ID

		monitor := &store.ManagedAgentMonitor{
			ID:                    monID,
			AgentID:               agentID,
			WorkflowStateID:      &stateID,
			Name:                  name,
			Description:           desc,
			Enabled:               true,
			IntervalSeconds:       interval,
			NextEvalAt:            nowStr,
			CooldownSeconds:       cooldown,
			MaxAlerts:             cfg.MaxAlerts,
			TrailingMessagesCount: trailing,
			QuestionType:          qType,
			QuestionInstructions:  instructions,
			QuestionCriteria:      critJSON,
			QuestionOptions:       optsJSON,
			ConditionOperator:     op,
			ConditionThreshold:    th,
			NudgeTemplate:         nudge,
			FallbackMode:          fb,
			CreatedAt:             nowStr,
			UpdatedAt:             nowStr,
		}

		if err := m.store.CreateManagedAgentMonitor(ctx, monitor); err != nil {
			log.Printf("[agent_monitors:%s] failed to create workflow state monitor %s: %v", agentID, cfg.ID, err)
			return err
		}
	}

	return nil
}

func truncateSafe(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

