package managed

import (
	"context"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/coding"
)

// embeddedRuntime is the active worker boundary, not a session factory.
// SDK messages remain the private checked-persistence compatibility encoding.
// Run/Continue join persistence callbacks before returning. Steer acceptance
// alone never proves consumption. Close releases resources, unlike Interrupt.
// Native Pi subagents deliberately do not use this boundary.
type embeddedRuntime interface {
	Run(context.Context, string, ...ai.ImageContent) (*embeddedOutcome, error)
	RunMessages(context.Context, []agent.AgentMessage) (*embeddedOutcome, error)
	Continue(context.Context) error
	Steer(agent.AgentMessage)
	Interrupt()
	Close()
	Snapshot() embeddedSnapshot
}

type embeddedOutcome struct{ StopReason ai.StopReason }
type embeddedSnapshot struct {
	LastAssistantText string
	HasUser           bool
	ContextTokens     int // Pi's existing projected estimate, not reported usage.
	ContextWindow     int
}

// Keep Pi construction, restore, hooks and context fitting at their existing
// call site. Embedding supplies Continue/Steer without another forwarding layer.
type piEmbeddedRuntime struct{ *coding.Session }

func piOutcome(result *coding.RunResult) *embeddedOutcome {
	if result == nil {
		return nil
	}
	return &embeddedOutcome{StopReason: result.StopReason}
}
func (p *piEmbeddedRuntime) Run(ctx context.Context, text string, images ...ai.ImageContent) (*embeddedOutcome, error) {
	result, err := p.Session.Run(ctx, text, images...)
	return piOutcome(result), err
}
func (p *piEmbeddedRuntime) RunMessages(ctx context.Context, messages []agent.AgentMessage) (*embeddedOutcome, error) {
	result, err := p.Session.RunMessages(ctx, messages)
	return piOutcome(result), err
}
func (p *piEmbeddedRuntime) Interrupt() {
	if p.Agent != nil {
		p.Agent.Abort()
	}
}
func (p *piEmbeddedRuntime) Close() {} // Pi has no between-turn engine resource.
func (p *piEmbeddedRuntime) Snapshot() embeddedSnapshot {
	history := p.History()
	snapshot := embeddedSnapshot{LastAssistantText: p.LastAssistantText(), ContextTokens: embeddedProjectedTokens(history)}
	if p.Model != nil {
		snapshot.ContextWindow = p.Model.ContextWindow
	}
	for _, message := range history {
		if message.MessageRole() == "user" {
			snapshot.HasUser = true
			break
		}
	}
	return snapshot
}

// Workflow refresh is a Pi-only binding, not a generic capability API.
func refreshPiEmbeddedTools(runtime embeddedRuntime, tools []agent.AgentTool) {
	if pi, ok := runtime.(*piEmbeddedRuntime); ok && pi.Agent != nil {
		pi.Agent.SetTools(tools)
	}
}
