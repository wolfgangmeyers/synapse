package managed

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/coding"
)

const embeddedGoalEvaluationMaxTokens = 512
const embeddedGoalEvaluationSystem = "goal: <goal description> - return exactly two lines: VERDICT: COMPLETED|NOT_DONE and REASON: <concise rationale>"

// Auxiliary model calls share owner/auth/transport fencing but never enter a
// new ordinary user turn, apply a goal revision, append an assistant or reply.
type embeddedAuxiliaryRequestKey struct{}

func extractConversationTurns(entries []store.EmbeddedEntry) string {
	const maxContextMessages = 32
	const maxContextChars = 24000

	var turns []string
	start := 0
	if len(entries) > maxContextMessages {
		start = len(entries) - maxContextMessages
	}
	recent := entries[start:]

	for _, entry := range recent {
		role := entry.Kind
		if role != "user" && role != "assistant" && role != "toolResult" {
			continue
		}
		var text string
		if msg, err := ai.UnmarshalMessage(entry.Body); err == nil {
			switch m := msg.(type) {
			case ai.UserMessage:
				text = ai.ContentText(m.Content)
			case *ai.UserMessage:
				if m != nil {
					text = ai.ContentText(m.Content)
				}
			case ai.AssistantMessage:
				text = ai.ContentText(m.Content)
			case *ai.AssistantMessage:
				if m != nil {
					text = ai.ContentText(m.Content)
				}
			case ai.ToolResultMessage:
				text = ai.ContentText(m.Content)
			case *ai.ToolResultMessage:
				if m != nil {
					text = ai.ContentText(m.Content)
				}
			}
		}
		if text == "" {
			text = string(entry.Body)
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		turns = append(turns, fmt.Sprintf("%s: %s", role, text))
	}

	result := strings.Join(turns, "\n\n")
	if len(result) > maxContextChars {
		result = result[len(result)-maxContextChars:]
	}
	return result
}

func parseGoalEvaluationResponse(rawText string) (verdict string, reason string) {
	lines := strings.Split(strings.TrimSpace(rawText), "\n")
	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)
		if strings.HasPrefix(trimmedLine, "VERDICT:") {
			v := strings.TrimSpace(strings.TrimPrefix(trimmedLine, "VERDICT:"))
			upperV := strings.ToUpper(v)
			if strings.Contains(upperV, store.EmbeddedGoalEvaluationCompleted) || upperV == "DONE" {
				verdict = store.EmbeddedGoalEvaluationCompleted
			} else if strings.Contains(upperV, store.EmbeddedGoalEvaluationNotDone) {
				verdict = store.EmbeddedGoalEvaluationNotDone
			}
		} else if strings.HasPrefix(trimmedLine, "REASON:") {
			reason = strings.TrimSpace(strings.TrimPrefix(trimmedLine, "REASON:"))
		}
	}
	if verdict == "" {
		upper := strings.ToUpper(strings.TrimSpace(rawText))
		if strings.Contains(upper, store.EmbeddedGoalEvaluationNotDone) {
			verdict = store.EmbeddedGoalEvaluationNotDone
		} else if strings.Contains(upper, store.EmbeddedGoalEvaluationCompleted) || strings.Contains(upper, store.EmbeddedGoalEvaluationDone) {
			verdict = store.EmbeddedGoalEvaluationCompleted
		}
	}
	if reason == "" {
		if verdict == store.EmbeddedGoalEvaluationCompleted {
			reason = "Goal completed"
		} else {
			reason = "Actionable steps remain"
		}
	}
	if len(reason) > 240 {
		reason = reason[:240]
	}
	return verdict, reason
}

func (m *Manager) evaluateEmbeddedProductionGoal(ctx context.Context, binding store.EmbeddedBinding, key store.EmbeddedGoalEvaluationKey, snapshot store.EmbeddedSnapshot, goal string, model *ai.Model, stream agent.StreamFn) (verdict string, reason string, err error) {
	defer func() {
		if err != nil {
			log.Printf("GOAL EVALUATION ERROR for %s: %v", binding.AgentID, err)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	current, err := m.store.ReopenEmbedded(ctx, binding)
	if err != nil {
		return "", "", err
	}
	policy, err := m.store.ManagedGoalPolicyFor(ctx, binding.AgentID)
	if err != nil {
		return "", "", err
	}
	if !policy.Config.Enabled || policy.DesiredRevision != key.GoalRevision || policy.AppliedRevision != key.GoalRevision || policy.ProjectionState != "applied" {
		return "", "", errors.New("goal evaluation canceled or superseded")
	}

	entries := current.Entries
	if len(entries) == 0 {
		entries = snapshot.Entries
	}
	if len(entries) == 0 {
		return "", "", errors.New("goal evaluation transcript empty")
	}
	activeConversation := extractConversationTurns(entries)
	userPrompt := fmt.Sprintf("Goal: %s\n\nActive conversation:\n%s\n\nReturn exactly two lines: VERDICT: COMPLETED|NOT_DONE and REASON: <concise rationale>.", goal, activeConversation)
	systemPrompt := strings.ReplaceAll(embeddedGoalEvaluationSystem, "<goal description>", goal)

	request := ai.NormalizeContext(ai.Context{SystemPrompt: systemPrompt, Messages: []ai.Message{ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: userPrompt}}}}})
	if model == nil || model.ContextWindow <= 0 || coding.EstimateContextTokens(request.Messages) > model.ContextWindow-embeddedContextHeadroom {
		return "", "", errors.New("goal evaluation context cannot fit fixed headroom")
	}
	maxTokens := embeddedGoalEvaluationMaxTokens
	opts := &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{ProviderRequestOptions: ai.ProviderRequestOptions{MaxRetries: 0}, MaxTokens: &maxTokens, SessionID: binding.SessionID, CacheRetention: ai.CacheNone}, ToolChoice: ai.ToolChoiceNone}
	result := stream(context.WithValue(ctx, embeddedAuxiliaryRequestKey{}, true), model, request, opts).Result()
	if ctx.Err() != nil || result == nil || result.StopReason != ai.StopStop {
		return "", "", errors.New("goal evaluation did not produce a bounded verdict")
	}
	var textBuilder strings.Builder
	for _, c := range result.Content {
		if tc, ok := c.(ai.TextContent); ok {
			textBuilder.WriteString(tc.Text)
		} else if tc, ok := c.(*ai.TextContent); ok && tc != nil {
			textBuilder.WriteString(tc.Text)
		}
	}
	rawText := textBuilder.String()
	if rawText == "" {
		return store.EmbeddedGoalEvaluationNotDone, "Inconclusive evaluator output; continuing progress on goal", nil
	}
	verdict, reason = parseGoalEvaluationResponse(rawText)
	if verdict == "" {
		log.Printf("GOAL EVALUATION WARNING for %s: unparseable verdict output: %q", binding.AgentID, rawText)
		return store.EmbeddedGoalEvaluationNotDone, "Inconclusive evaluator output; continuing progress on goal", nil
	}
	if verdict != store.EmbeddedGoalEvaluationCompleted {
		verdict = store.EmbeddedGoalEvaluationNotDone
	}
	return verdict, reason, nil
}

func (m *Manager) recordGoalEvaluationActivity(binding store.EmbeddedBinding, key store.EmbeddedGoalEvaluationKey, outcome, reason string) {
	preview := reason
	if preview == "" {
		if outcome == store.EmbeddedGoalEvaluationNotDone {
			preview = "Actionable steps remain"
		} else {
			preview = "COMPLETED"
		}
	}
	eventKey := fmt.Sprintf("goal-evaluation:%s:%d:%d:%s", binding.AgentID, key.UserSeq, key.FinalSeq, outcome)
	pres := &store.ManagedHistoryPresentation{
		Kind:    "goal_evaluation",
		Outcome: strings.ToLower(outcome),
		Summary: preview,
	}
	msg := "Goal Evaluation • " + outcome
	entry, err := m.store.RecordManagedCompleteHistoryWithDetails(m.runCtx, binding.AgentID, "managed_goal_evaluation", "info", msg, eventKey, pres, nil, &preview)
	if err == nil {
		m.publishCompleteHistory(entry)
	}
}

