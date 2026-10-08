package managed

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

// Primary constructor for parent embedded sessions:
//
//	func (m *Manager) EmbeddedSubagentPublicTools(parent store.EmbeddedBinding) []agent.AgentTool
//
// Returns the three public SDK subagent tools: "subagent", "get_subagent_result", and "steer_subagent".
// Registered in internal/managed/embedded_session.go for embedded parent sessions.

// subagentThrottle retrieves or creates the SubagentThrottle shared across all tool calls
// for the given Manager instance using Manager.subagentThrottleInstance.
func (m *Manager) subagentThrottle() *SubagentThrottle {
	if m == nil {
		return NewSubagentThrottle(nil)
	}
	m.subagentsMu.Lock()
	defer m.subagentsMu.Unlock()
	if m.subagentThrottleInstance == nil {
		var clock deterministic.Clock
		if m.clock != nil {
			clock = m.clock
		}
		m.subagentThrottleInstance = NewSubagentThrottle(clock)
	}
	return m.subagentThrottleInstance
}

// EmbeddedSubagentPublicTools is the sole primary constructor returning the three public SDK subagent tools:
// "subagent", "get_subagent_result", and "steer_subagent", bound to the parent's exact EmbeddedBinding fence.
func (m *Manager) EmbeddedSubagentPublicTools(parent store.EmbeddedBinding) []agent.AgentTool {
	return []agent.AgentTool{
		m.embeddedSubagentTool(parent),
		m.embeddedGetSubagentResultTool(parent),
		m.embeddedSteerSubagentTool(parent),
	}
}

// ---------------------------------------------------------------------------
// 1. subagent tool (private builder)
// ---------------------------------------------------------------------------

func embeddedSubagentSpawnSchema() *ai.Schema {
	closed := false
	minTurns := 1.0
	maxTurnsSchema := ai.Integer("Maximum number of agentic turns before stopping. Omit for unlimited (default).")
	maxTurnsSchema.Minimum = &minTurns

	s := ai.Object(
		ai.Prop("prompt", ai.String("The task for the agent to perform.")),
		ai.Opt("description", ai.String("A short (3-5 word) description of the task (shown in UI).")),
		ai.Opt("subagent_type", ai.String("The type of specialized agent to use. Available types: general-purpose, Plan.")),
		ai.Opt("max_turns", maxTurnsSchema),
		ai.Opt("resume", ai.String("Optional agent ID to resume from. Continues from previous context.")),
	)
	s.AdditionalAllowed = &closed
	return s
}

func (m *Manager) embeddedSubagentTool(parent store.EmbeddedBinding) agent.AgentTool {
	return agent.AgentTool{
		Name:        "subagent",
		Description: "Launch a specialized agent for complex, multi-step tasks.",
		Parameters:  embeddedSubagentSpawnSchema(),
		Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if err := ctx.Err(); err != nil {
				return agent.AgentToolResult{}, err
			}

			// Reject unsupported or invented arguments without echoing values
			for _, unsupported := range []string{"run_in_background", "model", "thinking", "inherit_context", "cwd", "parent"} {
				if _, ok := args[unsupported]; ok {
					return agent.AgentToolResult{}, fmt.Errorf("unsupported argument %q", unsupported)
				}
			}

			prompt, _ := args["prompt"].(string)
			prompt = strings.TrimSpace(prompt)
			if prompt == "" {
				return agent.AgentToolResult{}, errors.New("prompt is required")
			}

			description, _ := args["description"].(string)
			description = strings.TrimSpace(description)

			subagentType, _ := args["subagent_type"].(string)
			subagentType = strings.TrimSpace(subagentType)
			if subagentType == "" {
				subagentType = "general-purpose"
			} else if strings.EqualFold(subagentType, "general-purpose") {
				subagentType = "general-purpose"
			} else if strings.EqualFold(subagentType, "plan") {
				subagentType = "Plan"
			} else {
				// Unknown type falls back to general-purpose per pi-subagents
				subagentType = "general-purpose"
			}

			var maxTurns int
			if raw, ok := args["max_turns"]; ok && raw != nil {
				switch v := raw.(type) {
				case float64:
					if v < 1 || v != float64(int(v)) {
						return agent.AgentToolResult{}, errors.New("max_turns must be an integer >= 1")
					}
					maxTurns = int(v)
				case int:
					if v < 1 {
						return agent.AgentToolResult{}, errors.New("max_turns must be an integer >= 1")
					}
					maxTurns = v
				case int64:
					if v < 1 {
						return agent.AgentToolResult{}, errors.New("max_turns must be an integer >= 1")
					}
					maxTurns = int(v)
				default:
					return agent.AgentToolResult{}, errors.New("max_turns must be an integer >= 1")
				}
			}

			resume, _ := args["resume"].(string)
			resume = strings.TrimSpace(resume)

			// SubagentSpawnRequest uses safe child ID derivation when AgentID is empty.
			// Caller cannot control parent, model, or cwd.
			req := SubagentSpawnRequest{
				AgentID:      "",
				Description:  description,
				SubagentType: subagentType,
				MaxTurns:     maxTurns,
				Prompt:       prompt,
				Resume:       resume,
			}

			result, err := m.SpawnSubagent(ctx, parent, req)
			if err != nil {
				return agent.AgentToolResult{}, err
			}

			if resume != "" {
				return agent.AgentToolResult{
					Content: ai.ContentList{ai.TextContent{
						Text: fmt.Sprintf("Resumed agent %q in the background.", result.AgentID),
					}},
				}, nil
			}

			isQueued := result.Status == "queued"
			statusWord := "started"
			if isQueued {
				statusWord = "queued"
			}

			var b strings.Builder
			fmt.Fprintf(&b, "Agent %s in background.\n", statusWord)
			fmt.Fprintf(&b, "Agent ID: %s\n", result.AgentID)
			fmt.Fprintf(&b, "Type: %s\n", subagentType)
			if description != "" {
				fmt.Fprintf(&b, "Description: %s\n", description)
			}
			if isQueued {
				b.WriteString("Position: queued\n")
			}
			b.WriteString("\nYou will be notified when this agent completes.\n")
			b.WriteString("Use get_subagent_result to retrieve full results, or steer_subagent to send it messages.\n")
			b.WriteString("Do not duplicate this agent's work.")

			return agent.AgentToolResult{
				Content: ai.ContentList{ai.TextContent{Text: b.String()}},
			}, nil
		},
	}
}

// ---------------------------------------------------------------------------
// 2. get_subagent_result tool (private builder)
// ---------------------------------------------------------------------------

func embeddedGetSubagentResultSchema() *ai.Schema {
	closed := false
	s := ai.Object(
		ai.Prop("agent_id", ai.String("The agent ID to check.")),
		ai.Opt("verbose", ai.Boolean("If true, include the agent's full conversation (messages + tool calls). Default: false.")),
	)
	s.AdditionalAllowed = &closed
	return s
}

func (m *Manager) embeddedGetSubagentResultTool(parent store.EmbeddedBinding) agent.AgentTool {
	return agent.AgentTool{
		Name:        "get_subagent_result",
		Description: SubagentSnapshotWarning,
		Parameters:  embeddedGetSubagentResultSchema(),
		Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if err := ctx.Err(); err != nil {
				return agent.AgentToolResult{}, err
			}
			if _, ok := args["wait"]; ok {
				return agent.AgentToolResult{}, fmt.Errorf("unsupported argument \"wait\": result retrieval is snapshot-only\n\n%s", SubagentSnapshotWarning)
			}

			agentID, _ := args["agent_id"].(string)
			agentID = strings.TrimSpace(agentID)
			if agentID == "" {
				return agent.AgentToolResult{}, errors.New("agent_id is required")
			}

			verbose, _ := args["verbose"].(bool)

			// Fenced read through the Store enforces parent identity, session, epoch, and generation.
			record, err := m.store.ManagedSubagent(ctx, parent, agentID)
			if err != nil {
				return agent.AgentToolResult{}, err
			}

			// Shared throttle per manager enforces rolling anti-polling limit on active subagents.
			throttle := m.subagentThrottle()
			key := SubagentThrottleKeyForBinding(parent, agentID)
			if err := throttle.Allow(key, record.Status); err != nil {
				return agent.AgentToolResult{}, err
			}

			var transcript string
			if verbose {
				var transcriptErr error
				transcript, transcriptErr = m.fetchSubagentTranscript(ctx, parent, agentID, record.Status)
				if transcriptErr != nil {
					return agent.AgentToolResult{}, transcriptErr
				}
			}

			report := FormatSubagentResultReportWithOptions(*record, SubagentReportOptions{Transcript: transcript})
			return agent.AgentToolResult{
				Content: ai.ContentList{ai.TextContent{Text: report}},
			}, nil
		},
	}
}

// fetchSubagentTranscript reads genuine persisted SDK history via ManagedSubagentTranscript.
// Propagates corrupt chain/DB failures rather than masking them as an empty transcript.
// Absent history for unadmitted/stopped-queued or active snapshots returns ("", nil).
func (m *Manager) fetchSubagentTranscript(ctx context.Context, parent store.EmbeddedBinding, childID string, status string) (string, error) {
	if m == nil || m.store == nil {
		return "", errors.New("subagent transcript store is unavailable")
	}

	snap, err := m.store.ManagedSubagentTranscript(ctx, parent, childID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("read subagent transcript: %w", err)
	}
	if len(snap.Entries) == 0 {
		return "", nil
	}

	var messages []agent.AgentMessage
	for _, entry := range snap.Entries {
		switch entry.Kind {
		case "system", "user", "assistant", "tool_result", "toolResult":
			msg, err := ai.UnmarshalMessage(entry.Body)
			if err != nil {
				return "", fmt.Errorf("corrupt embedded entry chain: decode subagent message entry %d (%s): %w", entry.Sequence, entry.Kind, err)
			}
			messages = append(messages, msg)
		default:
			// Deliberately skip non-conversation entries (e.g. tool intent markers)
		}
	}
	if len(messages) == 0 {
		return "", nil
	}

	return formatSessionHistory(messages), nil
}

func formatSessionHistory(history []agent.AgentMessage) string {
	var b strings.Builder
	for i, msg := range history {
		if i > 0 {
			b.WriteString("\n")
		}
		switch m := msg.(type) {
		case ai.UserMessage:
			b.WriteString("[user]\n")
			for _, part := range m.Content {
				if t, ok := part.(ai.TextContent); ok {
					b.WriteString(t.Text)
					b.WriteString("\n")
				}
			}
		case *ai.UserMessage:
			if m != nil {
				b.WriteString("[user]\n")
				for _, part := range m.Content {
					if t, ok := part.(ai.TextContent); ok {
						b.WriteString(t.Text)
						b.WriteString("\n")
					}
				}
			}
		case ai.AssistantMessage:
			b.WriteString("[assistant]\n")
			for _, part := range m.Content {
				if t, ok := part.(ai.TextContent); ok {
					b.WriteString(t.Text)
					b.WriteString("\n")
				} else if call, ok := part.(ai.ToolCall); ok {
					fmt.Fprintf(&b, "Tool call: %s(%v)\n", call.Name, call.Arguments)
				}
			}
		case *ai.AssistantMessage:
			if m != nil {
				b.WriteString("[assistant]\n")
				for _, part := range m.Content {
					if t, ok := part.(ai.TextContent); ok {
						b.WriteString(t.Text)
						b.WriteString("\n")
					} else if call, ok := part.(ai.ToolCall); ok {
						fmt.Fprintf(&b, "Tool call: %s(%v)\n", call.Name, call.Arguments)
					}
				}
			}
		case ai.ToolResultMessage:
			fmt.Fprintf(&b, "[tool_result: %s]\n", m.ToolName)
			for _, part := range m.Content {
				if t, ok := part.(ai.TextContent); ok {
					b.WriteString(t.Text)
					b.WriteString("\n")
				}
			}
		case *ai.ToolResultMessage:
			if m != nil {
				fmt.Fprintf(&b, "[tool_result: %s]\n", m.ToolName)
				for _, part := range m.Content {
					if t, ok := part.(ai.TextContent); ok {
						b.WriteString(t.Text)
						b.WriteString("\n")
					}
				}
			}
		case ai.SystemMessage:
			b.WriteString("[system]\n")
			for _, part := range m.Content {
				if t, ok := part.(ai.TextContent); ok {
					b.WriteString(t.Text)
					b.WriteString("\n")
				}
			}
		case *ai.SystemMessage:
			if m != nil {
				b.WriteString("[system]\n")
				for _, part := range m.Content {
					if t, ok := part.(ai.TextContent); ok {
						b.WriteString(t.Text)
						b.WriteString("\n")
					}
				}
			}
		default:
			fmt.Fprintf(&b, "[message: %T]\n", msg)
		}
	}
	return strings.TrimSpace(b.String())
}

// ---------------------------------------------------------------------------
// 3. steer_subagent tool (private builder)
// ---------------------------------------------------------------------------

func embeddedSteerSubagentSchema() *ai.Schema {
	closed := false
	s := ai.Object(
		ai.Prop("agent_id", ai.String("The agent ID to steer (must be currently running).")),
		ai.Prop("message", ai.String("The steering message to send. This will appear as a user message in the agent's conversation.")),
	)
	s.AdditionalAllowed = &closed
	return s
}

func (m *Manager) embeddedSteerSubagentTool(parent store.EmbeddedBinding) agent.AgentTool {
	return agent.AgentTool{
		Name:        "steer_subagent",
		Description: "Send a mid-run message to redirect a running background agent.",
		Parameters:  embeddedSteerSubagentSchema(),
		Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if err := ctx.Err(); err != nil {
				return agent.AgentToolResult{}, err
			}

			agentID, _ := args["agent_id"].(string)
			agentID = strings.TrimSpace(agentID)
			if agentID == "" {
				return agent.AgentToolResult{}, errors.New("agent_id is required")
			}

			message, _ := args["message"].(string)
			message = strings.TrimSpace(message)
			if message == "" {
				return agent.AgentToolResult{}, errors.New("message is required")
			}

			// SteerSubagent validates caller parent binding, non-terminal status, and buffers or delivers.
			if err := m.SteerSubagent(ctx, parent, agentID, message); err != nil {
				return agent.AgentToolResult{}, err
			}

			rec, err := m.store.ManagedSubagent(ctx, parent, agentID)
			if err == nil && rec.Status == "queued" {
				return agent.AgentToolResult{
					Content: ai.ContentList{ai.TextContent{
						Text: fmt.Sprintf("Steering message queued for agent %s. It will be delivered once the session initializes.", agentID),
					}},
				}, nil
			}

			return agent.AgentToolResult{
				Content: ai.ContentList{ai.TextContent{
					Text: fmt.Sprintf("Steering message sent to agent %s. The agent will process it after its current tool execution.", agentID),
				}},
			}, nil
		},
	}
}
