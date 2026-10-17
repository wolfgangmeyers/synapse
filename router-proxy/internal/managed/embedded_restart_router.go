package managed

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

func embeddedRestartRouterSchema() *ai.Schema {
	closed := false
	s := ai.Object(
		ai.Prop("reason", ai.String("Required rationale for restarting the router (e.g., applied migration, rebuilt binary).")),
		ai.Opt("force", ai.Boolean("Force restart even if concurrent workers or active subagents are running. Cannot be set on the first attempt; only valid after an initial rejection within 60 seconds.")),
	)
	s.AdditionalAllowed = &closed
	return s
}

func (m *Manager) embeddedRestartRouterTool(owner embeddedOwner, toolName string) agent.AgentTool {
	desc := "Request a controlled restart of the mailbox router process. Automatically restarts if no other workers or subagents are active. If other workers or subagents are active, requires a two-step rejection safeguard before force can be used."
	return agent.AgentTool{
		Name:        toolName,
		Description: desc,
		Parameters:  embeddedRestartRouterSchema(),
		Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if ctx == nil {
				ctx = context.Background()
			}
			if owner.binding.AgentID != "" && owner.identity != "" && m != nil && m.store != nil {
				_ = m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL)
			}

			reason, _ := args["reason"].(string)
			reason = strings.TrimSpace(reason)
			if reason == "" {
				return agent.AgentToolResult{
					Content: ai.ContentList{
						ai.TextContent{Text: "Error: reason is required for router restart"},
					},
				}, nil
			}

			var force bool
			switch v := args["force"].(type) {
			case bool:
				force = v
			case string:
				force = strings.EqualFold(v, "true")
			}

			callerID := owner.binding.AgentID
			lane := m.laneFor(callerID)
			now := m.now()
			const rejectionTTL = 60 * time.Second

			if force {
				rejectedAt, _ := lane.getRestartRouterRejection()
				if rejectedAt.IsZero() {
					return agent.AgentToolResult{
						Content: ai.ContentList{
							ai.TextContent{
								Text: "[RESTART REJECTED - INVALID FORCE USAGE]\n" +
									"The force flag cannot be used until the request has been evaluated and rejected by the first step.\n" +
									"Please call " + toolName + " without force first so active system state can be checked.",
							},
						},
					}, nil
				}

				diff := now.Sub(rejectedAt)
				if diff < 0 || diff > rejectionTTL {
					return agent.AgentToolResult{
						Content: ai.ContentList{
							ai.TextContent{
								Text: "[RESTART REJECTED - PRIOR ATTEMPT EXPIRED]\n" +
									"Prior rejection occurred more than 60 seconds ago. Active system conditions may have changed.\n" +
									"Call " + toolName + " without force to evaluate current active processes.",
							},
						},
					}, nil
				}

				// Consume valid rejection
				lane.consumeRestartRouterRejection(now, rejectionTTL)

				m.restarting.Store(true)
				go func() {
					time.Sleep(150 * time.Millisecond)
					m.TriggerRouterRestart()
				}()

				return agent.AgentToolResult{
					Content: ai.ContentList{
						ai.TextContent{
							Text: fmt.Sprintf("[RESTART SCHEDULED - FORCED]\n"+
								"Router restart initiated with force override (reason: %q).\n"+
								"Active workers and subagents will be interrupted.\n"+
								"Process exiting with code 75 for runner supervisor reload in 200ms.", reason),
						},
					},
				}, nil
			}

			// Not force: check active conflicts
			conflicts := m.detectRestartConflicts(ctx, callerID)
			if len(conflicts) > 0 {
				conflictSummary := strings.Join(conflicts, "\n")
				lane.recordRestartRouterRejection(now, conflictSummary)

				return agent.AgentToolResult{
					Content: ai.ContentList{
						ai.TextContent{
							Text: fmt.Sprintf("[RESTART REJECTED - ACTIVE PROCESSES RUNNING]\n"+
								"Cannot restart router while other workers or subagents are active:\n%s\n\n"+
								"Recommended next steps:\n"+
								"1. Try again later. You can create a reminder to retry: mecha_reminder(action=\"create\", delay_minutes=2, body=\"retry router restart\")\n"+
								"2. If your change is critical enough to justify interrupting these active processes, re-run with force=true (or force: true) within 60 seconds:\n"+
								"   %s(reason=%q, force=true)",
								conflictSummary, toolName, reason),
						},
					},
				}, nil
			}

			// No conflicts: immediate success
			m.restarting.Store(true)
			go func() {
				time.Sleep(150 * time.Millisecond)
				m.TriggerRouterRestart()
			}()

			return agent.AgentToolResult{
				Content: ai.ContentList{
					ai.TextContent{
						Text: fmt.Sprintf("[RESTART SCHEDULED]\n"+
							"Router restart initiated (reason: %q).\n"+
							"Process exiting with code 75 for runner supervisor reload in 200ms.", reason),
					},
				},
			}, nil
		},
	}
}

func (m *Manager) detectRestartConflicts(ctx context.Context, callerID string) []string {
	var conflicts []string
	seenWorkers := make(map[string]bool)
	seenSubagents := make(map[string]bool)

	// 1. Check in-memory active embedded sessions (active turns)
	m.activeSessionsMu.Lock()
	for agentID, sess := range m.activeEmbeddedSessions {
		if agentID != callerID && sess != nil {
			if !seenWorkers[agentID] {
				seenWorkers[agentID] = true
				conflicts = append(conflicts, fmt.Sprintf("- Sibling worker: %s (active turn)", agentID))
			}
		}
	}
	m.activeSessionsMu.Unlock()

	// 2. Check lanes for active turns
	m.mu.Lock()
	laneIDs := make([]string, 0, len(m.lanes))
	for id := range m.lanes {
		laneIDs = append(laneIDs, id)
	}
	m.mu.Unlock()

	for _, agentID := range laneIDs {
		if agentID == callerID || seenWorkers[agentID] {
			continue
		}
		lane := m.laneFor(agentID)
		lane.activeMu.RLock()
		hasTurn := lane.activeSession != nil
		lane.activeMu.RUnlock()

		if hasTurn && !seenWorkers[agentID] {
			seenWorkers[agentID] = true
			conflicts = append(conflicts, fmt.Sprintf("- Sibling worker: %s (active turn)", agentID))
		}
	}

	// 3. Check durable store for active goals / continuations
	if m.store != nil {
		goalWorkers, err := m.store.GetActiveGoalContinuations(ctx, callerID)
		if err == nil {
			for _, agentID := range goalWorkers {
				if !seenWorkers[agentID] {
					seenWorkers[agentID] = true
					conflicts = append(conflicts, fmt.Sprintf("- Sibling worker: %s (active goal)", agentID))
				}
			}
		}
	}

	// 4. Check active subagents in-memory
	m.subagentsMu.Lock()
	for childID, task := range m.subagentsActive {
		task.mu.Lock()
		st := task.status
		parentID := task.parent.AgentID
		task.mu.Unlock()
		if st == "queued" || st == "running" {
			if !seenSubagents[childID] {
				seenSubagents[childID] = true
				if parentID != "" {
					conflicts = append(conflicts, fmt.Sprintf("- Subagent: %s (parent: %s, status: %s)", childID, parentID, st))
				} else {
					conflicts = append(conflicts, fmt.Sprintf("- Subagent: %s (status: %s)", childID, st))
				}
			}
		}
	}
	m.subagentsMu.Unlock()

	// 5. Check active subagents in store
	if m.store != nil {
		subagents, err := m.store.ActiveManagedSubagents(ctx)
		if err == nil {
			for _, s := range subagents {
				if !seenSubagents[s.AgentID] {
					seenSubagents[s.AgentID] = true
					conflicts = append(conflicts, fmt.Sprintf("- Subagent: %s (parent: %s, status: %s)", s.AgentID, s.ParentAgentID, s.Status))
				}
			}
		}
	}

	return conflicts
}
