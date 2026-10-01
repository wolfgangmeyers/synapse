package managed

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

func embeddedCompactionSchema() *ai.Schema {
	closed := false
	schema := ai.Object(ai.Prop("action", ai.EnumOf("status", "compact")))
	schema.AdditionalAllowed = &closed
	return schema
}

// Compact records a deferred request; the checked pre-provider barrier owns
// the summarizer and checkpoint. Status is a fenced read of durable state.
func (m *Manager) embeddedCompactionTool(owner embeddedOwner, fresh func(string) bool, head func() (int64, string), produced func(string, ai.ToolResultMessage)) agent.AgentTool {
	return agent.AgentTool{Name: "mecha_context", Description: "Read this session's saved compaction status or request deferred compaction.", Parameters: embeddedCompactionSchema(), Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
		if len(args) != 1 {
			return agent.AgentToolResult{}, errors.New("invalid context action")
		}
		action, ok := args["action"].(string)
		if !ok {
			return agent.AgentToolResult{}, errors.New("invalid context action")
		}
		if action == "status" {
			status, err := m.store.EmbeddedOwnCompactionStatus(ctx, owner.binding)
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			body, err := json.Marshal(status)
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: string(body)}}}, nil
		}
		if action != "compact" {
			return agent.AgentToolResult{}, errors.New("invalid context action")
		}
		if m.embeddedAfterIntent != nil {
			m.embeddedAfterIntent(ctx)
		}
		if !fresh(id) {
			return agent.AgentToolResult{}, errors.New("compaction intent unknown: no fresh SDK admission")
		}
		if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
			return agent.AgentToolResult{}, err
		}
		seq, digest := head()
		result, err := m.store.RequestEmbeddedOwnCompaction(ctx, owner.binding, id, seq, digest, true)
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		produced(id, result)
		return agent.AgentToolResult{Content: result.Content}, nil
	}}
}
