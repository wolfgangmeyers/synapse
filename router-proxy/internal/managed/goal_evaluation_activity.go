package managed

import (
	"log"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// embeddedAuxiliaryRequestKey marks non-foreground model requests.
type embeddedAuxiliaryRequestKey struct{}

func (m *Manager) recordGoalEvaluationActivity(binding store.EmbeddedBinding, key store.EmbeddedGoalEvaluationKey, outcome, reason string, details ...string) {
	preview := reason
	if preview == "" {
		if outcome == store.EmbeddedGoalEvaluationNotDone {
			preview = "Actionable steps remain"
		} else {
			preview = "COMPLETED"
		}
	}
	summary := preview
	if len(summary) > 500 {
		summary = summary[:497] + "..."
	}

	rawContent := preview
	if len(details) > 0 && strings.TrimSpace(details[0]) != "" {
		rawContent = details[0]
	}

	eventKey := store.GoalEvaluationActivityKey(binding, key)
	pres := &store.ManagedHistoryPresentation{
		Kind:    "goal_evaluation",
		Outcome: strings.ToLower(outcome),
		Summary: summary,
	}
	msg := "Goal Evaluation • " + outcome
	entry, err := m.store.RecordEmbeddedGoalEvaluationActivity(m.runCtx, binding, key, outcome, reason, msg, eventKey, pres, rawContent)
	if err != nil {
		log.Printf("[embedded:%s] Failed to record goal evaluation history (%s): %v", binding.AgentID, eventKey, err)
	} else if entry.ID != "" {
		m.publishCompleteHistory(entry)
	}
}
