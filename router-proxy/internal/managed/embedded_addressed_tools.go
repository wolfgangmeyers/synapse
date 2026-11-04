package managed

import (
	"context"
	"errors"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

// The router supplies its ordinary after-acceptance routing/publication path.
// This callback cannot accept mail: the fenced Store transaction already did.
func (m *Manager) SetEmbeddedMailPublisher(publish func(store.EnqueueResult)) {
	m.mu.Lock()
	m.embeddedMailPublisher = publish
	m.mu.Unlock()
}

func (m *Manager) claimEmbeddedToolHandoff(producer *store.EmbeddedToolInvocation) bool {
	if !store.ClaimEmbeddedToolHandoff(producer) {
		return false
	}
	if m.embeddedBeforeToolHandoff != nil {
		m.embeddedBeforeToolHandoff(producer)
	}
	return true
}

func (m *Manager) publishEmbeddedToolMail(ctx context.Context, accepted store.EnqueueResult) (err error) {
	// Keep publication failure distinct from the original committed-admission
	// error. Never serialize a panic's untrusted payload or retry publication.
	defer func() {
		if recover() != nil {
			err = errors.New("committed mail publication failed")
		}
	}()
	m.mu.Lock()
	publish := m.embeddedMailPublisher
	m.mu.Unlock()
	if publish != nil {
		publish(accepted)
		return nil
	}
	// Manager-only fixtures have no browser/tmux publisher. Managed recipients
	// still use the real durable directory and their own serial delivery lane.
	target, err := m.store.ResolveMessageTarget(ctx, accepted.Message.ID)
	if err == nil && target != nil && target.SourceKind == "managed" {
		m.OfferDirect(target.OwnerIdentity)
	}
	return err
}

func embeddedBoundedString(limit int) *ai.Schema {
	s := ai.String()
	s.MaxLength = &limit
	return s
}

func embeddedClosedObject(fields ...ai.Field) *ai.Schema {
	closed := false
	s := ai.Object(fields...)
	s.AdditionalAllowed = &closed
	return s
}

func embeddedMailboxSchema() *ai.Schema {
	return embeddedClosedObject(ai.Prop("recipient", embeddedBoundedString(128)), ai.Prop("body", embeddedBoundedString(protocol.DefaultMaxBodyBytes)), ai.Opt("subject", embeddedBoundedString(256)))
}

// Validate the original uncoerced SDK values before recording any intent.
func embeddedAddressedArguments(name string, args map[string]any) error {
	if name != "mecha_mailbox_send" || !embeddedMailboxSchema().Check(args) {
		return errors.New("invalid addressed mailbox arguments")
	}
	recipient, _ := args["recipient"].(string)
	if strings.EqualFold(recipient, "system") {
		return errors.New("failed to send message: 'system' is an automated system notifier and does not accept incoming messages. Do not reply to system notifications.")
	}
	body, _ := args["body"].(string)
	envelope := protocol.Envelope{ID: "tool-validation", From: "tool-caller", To: recipient, Body: body, CreatedAt: "2030-01-01T00:00:00.000Z"}
	if subject, exists := args["subject"]; exists {
		text, ok := subject.(string)
		if !ok {
			return errors.New("invalid mail subject")
		}
		envelope.Subject = &text
	}
	return protocol.ValidateEnvelope(envelope, protocol.DefaultMaxBodyBytes)
}

func (m *Manager) embeddedAddressedTool(owner embeddedOwner, name string, admitted func(string) *store.EmbeddedToolInvocation, completed func(string, *store.EmbeddedToolInvocation)) agent.AgentTool {
	schema := embeddedMailboxSchema()
	description := "Queue an independently addressed mailbox message. Returns acceptance, not delivery or an automatic reply."
	return agent.AgentTool{Name: name, Description: description, Parameters: schema, Execute: func(ctx context.Context, id string, args map[string]any, update agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
		current, ok := ctx.Value(embeddedOwnerContextKey{}).(embeddedOwner)
		if !ok || current != owner {
			return agent.AgentToolResult{}, errors.New("managed tool requires current runner context owner")
		}
		if err := embeddedAddressedArguments(name, args); err != nil {
			return agent.AgentToolResult{}, err
		}
		invocation := admitted(id)
		if invocation == nil {
			return agent.AgentToolResult{}, errors.New("managed tool has no fresh SDK invocation")
		}
		if err := m.store.StartEmbeddedToolInvocation(ctx, invocation, ai.ToolCall{ID: id, Name: name, Arguments: args}); err != nil {
			return agent.AgentToolResult{}, err
		}
		if m.embeddedAfterIntent != nil {
			m.embeddedAfterIntent(ctx)
		}
		if err := m.authorizeEmbeddedWorkerLaunch(ctx, owner); err != nil {
			return agent.AgentToolResult{}, err
		}
		var err error
		var accepted store.EnqueueResult
		if m.embeddedBeforeToolDomain != nil {
			m.embeddedBeforeToolDomain()
		}
		accepted, err = m.store.EnqueueEmbeddedToolMail(ctx, invocation)
		if m.claimEmbeddedToolHandoff(invocation) {
			err = errors.Join(err, m.publishEmbeddedToolMail(ctx, accepted))
		}
		if err != nil {
			store.RecordEmbeddedToolHandoffError(invocation, err)
			return agent.AgentToolResult{}, err
		}
		if err := m.observeEmbeddedEffect(ctx, "domain", owner.binding, id); err != nil {
			return agent.AgentToolResult{}, err
		}
		payload, err := store.EmbeddedToolResultContent(invocation)
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		result := agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: string(payload)}}}
		if err := m.store.CompleteEmbeddedToolInvocation(ctx, invocation, ai.ToolResultMessage{ToolCallID: id, ToolName: name, Content: result.Content}); err != nil {
			return agent.AgentToolResult{}, err
		}
		completed(id, invocation)
		return result, nil
	}}
}
