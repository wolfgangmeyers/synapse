package managed

import (
	"context"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

type embeddedToolInvocationContextKey struct{}

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

func embeddedWorkerSchema() *ai.Schema {
	model := embeddedClosedObject(ai.Prop("provider", embeddedBoundedString(128)), ai.Prop("id", embeddedBoundedString(128)))
	s := embeddedClosedObject(ai.Prop("action", ai.EnumOf("list", "launch", "send", "cleanup")), ai.Opt("name", embeddedBoundedString(128)), ai.Opt("cwd", embeddedBoundedString(4096)), ai.Opt("instruction", embeddedBoundedString(MaxInitialInstructionBytes)), ai.Opt("model", model), ai.Opt("body", embeddedBoundedString(protocol.DefaultMaxBodyBytes)), ai.Opt("subject", embeddedBoundedString(256)))
	s.OneOf = []*ai.Schema{
		embeddedClosedObject(ai.Prop("action", ai.EnumOf("list"))),
		embeddedClosedObject(ai.Prop("action", ai.EnumOf("launch")), ai.Prop("name", embeddedBoundedString(128)), ai.Prop("cwd", embeddedBoundedString(4096)), ai.Prop("instruction", embeddedBoundedString(MaxInitialInstructionBytes)), ai.Opt("model", model)),
		embeddedClosedObject(ai.Prop("action", ai.EnumOf("send")), ai.Prop("name", embeddedBoundedString(128)), ai.Prop("body", embeddedBoundedString(protocol.DefaultMaxBodyBytes)), ai.Opt("subject", embeddedBoundedString(256))),
		embeddedClosedObject(ai.Prop("action", ai.EnumOf("cleanup")), ai.Prop("name", embeddedBoundedString(128))),
	}
	return s
}

// Validate the original uncoerced SDK values before recording any intent.
func embeddedAddressedArguments(name string, args map[string]any) error {
	if name == "mecha_mailbox_send" {
		if !embeddedMailboxSchema().Check(args) {
			return errors.New("invalid addressed mailbox arguments")
		}
	} else {
		if name != "mecha_worker" || !embeddedWorkerSchema().Check(args) {
			return errors.New("invalid worker action arguments")
		}
		action, _ := args["action"].(string)
		if action == "list" {
			return nil
		}
		worker, _ := args["name"].(string)
		if !managedNameRE.MatchString(worker) || len(worker) > 128 {
			return errors.New("invalid worker identity")
		}
		if action == "cleanup" {
			return nil
		}
		if action == "launch" {
			cwd, _ := args["cwd"].(string)
			instruction, _ := args["instruction"].(string)
			if cwd == "" || !utf8.ValidString(cwd) || len(cwd) > 4096 || !utf8.ValidString(instruction) || len(instruction) > MaxInitialInstructionBytes {
				return errors.New("invalid worker launch arguments")
			}
			return nil // Canonical Manager admission owns cwd/model validation.
		}
	}
	recipient, _ := args["recipient"].(string)
	if name == "mecha_worker" {
		recipient, _ = args["name"].(string)
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

func (m *Manager) embeddedAddressedTool(owner embeddedOwner, name string, admitted func(string) *store.EmbeddedToolInvocation, completed func(string, *store.EmbeddedToolInvocation), freshLaunch func(string) bool) agent.AgentTool {
	schema := embeddedMailboxSchema()
	description := "Queue an independently addressed mailbox message. Returns acceptance, not delivery or an automatic reply."
	if name == "mecha_worker" {
		schema = embeddedWorkerSchema()
		description = "List, launch, send mail to or clean up independent embedded workers by their saved identity. Launch and cleanup return accepted operations, not readiness or completion."
	}
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
		if name != "mecha_worker" || args["action"] != "launch" {
			if m.embeddedAfterIntent != nil {
				m.embeddedAfterIntent(ctx)
			}
		}
		if err := m.authorizeEmbeddedWorkerLaunch(ctx, owner); err != nil {
			return agent.AgentToolResult{}, err
		}
		var err error
		if name == "mecha_mailbox_send" || args["action"] == "send" {
			var accepted store.EnqueueResult
			if m.embeddedBeforeToolDomain != nil {
				m.embeddedBeforeToolDomain()
			}
			accepted, err = m.store.EnqueueEmbeddedToolMail(ctx, invocation)
			if m.claimEmbeddedToolHandoff(invocation) {
				err = errors.Join(err, m.publishEmbeddedToolMail(ctx, accepted))
			}
		} else {
			switch args["action"] {
			case "list":
				_, err = m.store.ListEmbeddedToolWorkers(ctx, invocation)
			case "launch":
				if args["name"] == owner.binding.AgentID {
					return agent.AgentToolResult{}, errors.New("worker launch cannot reuse its calling serial lane")
				}
				// Keep the existing canonical launch and uncertainty reconciliation.
				// Only this SDK execution can add the atomic producer guard.
				launchCtx := context.WithValue(ctx, embeddedToolInvocationContextKey{}, invocation)
				_, err = m.embeddedWorkerLaunchTool(owner, freshLaunch).Execute(launchCtx, id, args, update)
			case "cleanup":
				worker, _ := args["name"].(string)
				if worker == owner.binding.AgentID {
					return agent.AgentToolResult{}, errors.New("worker cleanup cannot target its calling serial lane")
				}
				body, encodeErr := json.Marshal(args)
				if encodeErr != nil {
					return agent.AgentToolResult{}, encodeErr
				}
				_, err = m.terminateWithEmbeddedProducer(ctx, TerminateRequest{AgentID: worker, Method: "DELETE", CanonicalPath: "/v1/managed-agents/" + worker, RequestID: embeddedWorkerRequestID(owner.binding, id), CanonicalBodyHash: hashString(string(body))}, invocation)
			}
		}
		if err != nil {
			store.RecordEmbeddedToolHandoffError(invocation, err)
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
