package managed

import (
	"context"
	"errors"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// dispatchEmbeddedMail is a saved-runtime branch of OfferDirect. The lane
// excludes launch, public prompts and other mail turns. A committed inbound
// append with an uncertain provider outcome is never replayed on recovery.
func (m *Manager) dispatchEmbeddedMail(agentID string) {
	lane := m.laneFor(agentID)
	lane.serial.Lock()
	defer lane.serial.Unlock()
	for {
		if m.runCtx.Err() != nil {
			return
		}
		m.flushEmbeddedReplies(m.runCtx, agentID)
		// A picker can commit between turns, never between the model snapshot
		// and this mail turn's durable admission.
		var saved *store.ManagedAgentRecord
		var owner embeddedOwner
		var attempt *store.EmbeddedMailAttempt
		admitted := func() bool {
			lane.selection.Lock()
			defer lane.selection.Unlock()
			var err error
			saved, err = m.store.ManagedAgent(m.runCtx, agentID)
			if err != nil || saved.Runtime != "embedded" || saved.DesiredState != "running" || saved.LifecycleState != "running" || !saved.AcceptingPrompts || !saved.SlotHeld {
				return false
			}
			m.mu.Lock()
			var ok bool
			owner, ok = m.embeddedOwners[agentID]
			closed := m.closed
			m.mu.Unlock()
			if !ok || closed || owner.binding.AgentID != agentID || owner.binding.SessionID != saved.PiSessionID || owner.binding.Epoch != saved.SessionEpoch || owner.binding.Generation != saved.RuntimeGeneration {
				return false
			}
			if err := m.store.RenewEmbeddedOwner(m.runCtx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
				return false
			}
			id, err := m.store.NextEmbeddedMail(m.runCtx, agentID)
			if err != nil || id == "" {
				return false
			}
			attempt, err = m.store.ReserveEmbeddedMail(m.runCtx, owner.binding, id)
			return err == nil
		}()
		if !admitted {
			return
		}
		ctx := context.WithValue(m.runCtx, embeddedOwnerContextKey{}, owner)
		if err := m.runEmbeddedMail(ctx, *saved, attempt); err != nil {
			var rolledBack *store.EmbeddedMailAppendRolledBack
			if errors.As(err, &rolledBack) {
				// Keep the unstarted reservation and live owner available for a
				// later explicit offer or cold recovery. Do not spin on the fault.
				return
			}
			// Do not fail the worker on turn errors. Transition cleanly back to idle:
			if fenceErr := m.store.RenewEmbeddedOwner(context.Background(), owner.binding, owner.identity, embeddedOwnerTTL); fenceErr == nil {
				_ = m.store.SetEmbeddedActivityState(context.Background(), owner.binding, "idle")
				_ = m.store.ResetEmbeddedAgentInterrupt(context.Background(), owner.binding.AgentID) // or ensure running, accepting_prompts=1, slot_held=1
				m.publishProjection()
			}
			if errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "context canceled") {
				m.OfferDirect(agentID)
			}
			return // no retry of an ambiguous started SDK turn
		}
		m.flushEmbeddedReplies(ctx, agentID)
	}
}
