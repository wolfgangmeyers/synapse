package managed

import (
	"fmt"
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

	if should, _ := m.store.ShouldRecordGoalEvaluation(m.runCtx, binding.AgentID, outcome, preview); !should {
		return
	}

	rawContent := preview
	if len(details) > 0 && strings.TrimSpace(details[0]) != "" {
		rawContent = details[0]
	}

	eventKey := fmt.Sprintf("goal-evaluation:%s:%d:%d:%s", binding.AgentID, key.UserSeq, key.FinalSeq, outcome)
	pres := &store.ManagedHistoryPresentation{
		Kind:    "goal_evaluation",
		Outcome: strings.ToLower(outcome),
		Summary: summary,
	}
	msg := "Goal Evaluation • " + outcome
	entry, err := m.store.RecordManagedCompleteHistoryWithDetails(m.runCtx, binding.AgentID, "managed_goal_evaluation", "info", msg, eventKey, pres, nil, &rawContent)
	if err != nil {
		log.Printf("[embedded:%s] Failed to record goal evaluation history (%s): %v", binding.AgentID, eventKey, err)
	} else {
		m.publishCompleteHistory(entry)
	}
}
