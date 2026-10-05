package managed

import (
	"context"
	"errors"
	"fmt"
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
			if errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "context canceled") {
				_ = m.store.ResetEmbeddedAgentInterrupt(context.Background(), agentID)
				m.publishProjection()
				m.OfferDirect(agentID)
				return
			}
			if ctx.Err() == nil {
				// Checked final persistence can reject the SDK's append; its
				// synthetic error then exists only in this local session. Keep
				// a bounded current cause without undoing ACK or replaying work.
				cause := store.RedactedManagedDiagnostic(fmt.Sprintf("mail attempt %s: %s", attempt.AttemptID, sanitizeError(err)))
				if recordErr := m.store.MarkEmbeddedOwnerFailed(context.Background(), owner.binding, saved.LaunchOperationID, "EMBEDDED_MAIL_FAILED", cause); recordErr != nil {
					m.emitEvent(Event{Kind: EventManagedRecovery, AgentID: agentID, Operation: "embedded_mail", MessageID: attempt.MessageID,
						Status: "failure_not_recorded", ErrorCode: "EMBEDDED_MAIL_FAILURE_RECORD_FAILED",
						ErrorText: store.RedactedManagedDiagnostic(fmt.Sprintf("mail failure recording: %s; %s", sanitizeError(recordErr), cause))})
				}
			}
			return // no retry of an ambiguous started SDK turn
		}
		m.flushEmbeddedReplies(ctx, agentID)
	}
}
