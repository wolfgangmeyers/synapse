package managed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

func embeddedReminderSchema() *ai.Schema {
	closed := false
	s := ai.Object(ai.Prop("action", ai.EnumOf("list", "create", "update", "delete")), ai.Opt("body", ai.String()), ai.Opt("due_at", ai.String()), ai.Opt("id", ai.String()), ai.Opt("interval_ms", ai.Integer()))
	s.AdditionalAllowed = &closed
	return s
}

func embeddedReminderArguments(args map[string]any) ([]byte, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	if _, err = store.ParseEmbeddedOwnReminderArguments(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// The effect intent is the SDK's original call. The own-reminder ledger is the
// atomic effect receipt; an unknown effect without that receipt is never retried.
func (m *Manager) embeddedReminderTool(owner embeddedOwner, fresh func(string) bool, uncertain func(string), produced func(string, ai.ToolResultMessage, bool)) agent.AgentTool {
	return agent.AgentTool{Name: "mecha_reminder", Description: "Create, list, update or delete reminders for this saved embedded agent only.", Parameters: embeddedReminderSchema(), Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
		fail := func(err error) (agent.AgentToolResult, error) { uncertain(id); return agent.AgentToolResult{}, err }
		if m.embeddedAfterIntent != nil {
			m.embeddedAfterIntent(ctx)
		}
		raw, err := embeddedReminderArguments(args)
		if err != nil {
			return fail(err)
		}
		if err = m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
			return fail(err)
		}
		effect, err := m.store.RecoverEmbeddedEffect(ctx, owner.binding, id)
		if err != nil {
			return fail(fmt.Errorf("reminder intent unavailable: %w", err))
		}
		var original struct {
			ID        string         `json:"id"`
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err = json.Unmarshal(effect.Intent, &original); err != nil {
			return fail(err)
		}
		originalRaw, err := json.Marshal(original.Arguments)
		if err != nil {
			return fail(err)
		}
		if original.ID != id || original.Name != "mecha_reminder" || !bytes.Equal(raw, originalRaw) {
			// This is a checked conflict, not permission to run another effect.
			return agent.AgentToolResult{}, errors.New("reminder call ID conflicts with original arguments")
		}
		if effect.State == "observed" {
			message, decodeErr := ai.UnmarshalMessage(effect.Result)
			if decodeErr != nil {
				return fail(decodeErr)
			}
			saved, ok := message.(ai.ToolResultMessage)
			if !ok || saved.ToolCallID != id || saved.ToolName != "mecha_reminder" || saved.IsError {
				return fail(errors.New("invalid saved reminder result"))
			}
			produced(id, saved, true)
			return agent.AgentToolResult{Content: saved.Content}, nil
		}
		if !fresh(id) {
			return fail(errors.New("reminder intent unknown: no fresh SDK admission"))
		}
		result, replay, err := m.store.EmbeddedOwnReminderAdmittedCall(ctx, owner.binding, id, raw, true)
		if err != nil {
			return fail(err)
		}
		if replay {
			return fail(errors.New("reminder receipt predates fresh admission: SDK result unknown"))
		}
		payload, err := json.Marshal(result)
		if err != nil {
			return fail(err)
		}
		content := ai.ContentList{ai.TextContent{Text: string(payload)}}
		produced(id, ai.ToolResultMessage{ToolCallID: id, ToolName: "mecha_reminder", Content: content}, false)
		return agent.AgentToolResult{Content: content}, nil
	}}
}
