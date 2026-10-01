package managed

import (
	"context"
	"errors"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/coding"
)

// The binding includes the agent, session, epoch, runtime generation and owner
// token. The process generation and stable wake ID complete the claimed scope.
// A scope is supplied by the claim caller, never inferred from the Store head.
type embeddedClaimedWakeScope struct {
	binding           store.EmbeddedBinding
	processGeneration int64
	id                string
	kind              string
}

func (p *embeddedPersistence) checkClaimedWake(ctx context.Context, scope embeddedClaimedWakeScope) error {
	if scope.binding != p.binding || scope.processGeneration <= 0 || scope.id == "" || scope.kind == "" || p.wake != nil || p.wakeUsed {
		return errors.New("claimed wake scope mismatch or already used")
	}
	wake, err := p.db.EmbeddedRestartWake(ctx, scope.binding, scope.processGeneration, scope.id)
	if err != nil {
		return err
	}
	if wake.State != "claimed" || wake.Kind != scope.kind {
		return errors.New("restart wake is not claimed for this invocation")
	}
	return nil
}

// Exactly one SDK run owns this persistence instance. Final append is checked
// at BeforeMessageEnd; completion follows only after the serial SDK run returns.
func (p *embeddedPersistence) runClaimedWake(ctx context.Context, session *coding.Session, scope embeddedClaimedWakeScope, prompts []agent.AgentMessage) error {
	if err := p.checkClaimedWake(ctx, scope); err != nil {
		return err
	}
	if session == nil || (scope.kind == "interrupted") != (len(prompts) == 0) {
		return errors.New("claimed wake prompt/continuation mismatch")
	}
	p.wakeUsed = true
	p.wake = &scope
	if len(prompts) == 0 {
		if err := session.Continue(ctx); err != nil {
			return err
		}
	} else {
		if _, err := session.RunMessages(ctx, prompts); err != nil {
			return err
		}
	}
	if p.wakeFinal == nil {
		return errors.New("claimed wake SDK run has no checked final")
	}
	return p.db.CompleteEmbeddedRestartWake(ctx, scope.binding, scope.processGeneration, scope.id, p.wakeFinal.Sequence, p.wakeFinal.Digest)
}
