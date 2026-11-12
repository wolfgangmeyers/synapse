package managed

import (
	"context"
	"errors"
	"fmt"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

func (m *Manager) embeddedWorkflowJournalNoteTool(owner embeddedOwner) agent.AgentTool {
	closed := false
	schema := ai.Object(
		ai.Prop("source", ai.EnumOf("proactive", "human-requested", "prompted")),
		ai.Prop("text", ai.String("Concise plain-language journal note or entry, at most 4096 UTF-8 bytes.")),
		ai.Opt("run_id", ai.String("Run ID returned by a transition or exit, required for prompted entries.")),
		ai.Opt("target_event_id", ai.String("Event ID returned by a transition or exit, required for prompted entries.")),
	)
	schema.AdditionalAllowed = &closed
	return agent.AgentTool{Name: "workflow_journal_note", Description: "Record a useful workflow journal note or an entry for a transition or exit.", Parameters: schema,
		Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if m == nil || m.store == nil {
				return agent.AgentToolResult{}, errors.New("store unavailable for workflow journal")
			}
			source, _ := args["source"].(string)
			text, _ := args["text"].(string)
			run, _ := args["run_id"].(string)
			event, _ := args["target_event_id"].(string)
			input := store.WorkflowJournalNoteInput{Source: source, Text: text, RunID: run, TargetEventID: event}
			// Reject subagent callers before consulting the receipt store.
			rec, err := m.store.ManagedAgent(ctx, owner.binding.AgentID)
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			if rec == nil || rec.ParentAgentID != "" {
				return agent.AgentToolResult{}, errors.New("subagents cannot write workflow journals")
			}
			result, _, err := m.store.AppendWorkflowJournalNote(ctx, owner.binding.AgentID, id, input)
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			return agent.AgentToolResult{Content: []ai.Content{ai.TextContent{Text: fmt.Sprintf("Journal note recorded (run_id: %s, event_id: %s).", result.RunID, result.EventID)}}}, nil
		},
	}
}
