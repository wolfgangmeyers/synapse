package managed

import (
	"context"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// installParentActivityReceiver starts after readiness commits, so every Pi
// start/settled event is associated with the same durable binding that owns the
// managed row. It intentionally uses Pi's native lifecycle instead of treating
// an accepted direct prompt as work.
func (m *Manager) installParentActivityReceiver(binding *managedBinding) {
	rpc, ok := binding.rpc.(ParentActivityRPC)
	if !ok {
		return
	}
	rpc.SetParentActivityEventHandler(func(event map[string]any) {
		m.handleParentActivityEvent(binding, event)
	})
}

type managedParentActivityCapture struct {
	generation        uint64
	admissionSequence int64
	admitted          bool
}

func parentActivityCaptureCurrentLocked(binding *managedBinding, capture managedParentActivityCapture, sequence int64, sequenceOK bool) bool {
	if binding.turnGeneration != capture.generation {
		return false
	}
	// A decoded frame at or before the current prompt boundary belongs to the
	// previous stream position, even when its semantic callback was delayed.
	if sequenceOK && binding.turnAdmitted && sequence <= binding.turnAdmissionSequence {
		return false
	}
	// The current idle callback clears the marker before its durable task runs.
	// The generation remains the identity of that completed turn. If a new
	// marker is present, its boundary must still be the one captured here.
	if capture.admitted && binding.turnAdmitted && binding.turnAdmissionSequence != capture.admissionSequence {
		return false
	}
	return true
}

func (m *Manager) handleParentActivityEvent(binding *managedBinding, event map[string]any) {
	if binding == nil {
		return
	}
	// This runs in Pi stdout order before a following terminal tool frame can
	// claim success. The later asynchronous activity projection remains
	// unchanged; this is only the missing-telemetry fallback for builtins.
	sequence, sequenceOK := readinessInteger(event["sequence"])
	state, stateOK := event["state"].(string)
	var capture managedParentActivityCapture
	if stateOK {
		binding.serial.Lock()
		capture = managedParentActivityCapture{
			generation:        binding.turnGeneration,
			admissionSequence: binding.turnAdmissionSequence,
			admitted:          binding.turnAdmitted,
		}
		previousSequence := binding.lastParentActivitySequence
		if sequenceOK && sequence > binding.lastParentActivitySequence {
			binding.lastParentActivitySequence = sequence
		}
		stale := capture.admitted && sequenceOK && sequence <= capture.admissionSequence
		if !stale && state == "active" && binding.turnAdmitted && (!sequenceOK || (sequence > binding.turnAdmissionSequence && sequence > previousSequence)) {
			binding.turnStarted = true
			binding.turnStartedSequence = sequence
		} else if !stale && state == "idle" && binding.turnAdmitted && binding.turnStarted && (!sequenceOK || sequence > binding.turnStartedSequence) {
			// An idle frame only closes a marker after its matching active frame.
			// This keeps a delayed idle from an older turn from clearing a new one.
			clearManagedTurnLocked(binding)
		}
		binding.serial.Unlock()
		if stale {
			return
		}
	}
	if sequenceOK && stateOK && state == "idle" {
		m.reconcileBuiltinToolTelemetryMissing(binding)
	}
	if !sequenceOK || !stateOK {
		return
	}
	m.startManagedTask(func() {
		binding.lane.serial.Lock()
		binding.serial.Lock()
		if !parentActivityCaptureCurrentLocked(binding, capture, sequence, sequenceOK) {
			binding.serial.Unlock()
			binding.lane.serial.Unlock()
			return
		}
		fence := bindingFence(binding.agentID, binding)
		if !m.liveBindingForFence(m.runCtx, binding, fence) {
			binding.serial.Unlock()
			binding.lane.serial.Unlock()
			return
		}
		updated, err := m.store.ApplyManagedParentActivity(context.Background(), fence, store.ManagedParentActivityEvent{
			Sequence: sequence,
			State:    state,
		})
		if err == nil && updated {
			m.publishProjection()
		}
		binding.serial.Unlock()
		binding.lane.serial.Unlock()
		if err == nil && updated && state == "idle" {
			binding.serial.Lock()
			current := parentActivityCaptureCurrentLocked(binding, capture, sequence, sequenceOK)
			binding.serial.Unlock()
			if !current {
				return
			}
			m.wakeSnoozedGoalInLane(binding, fence)
			binding.serial.Lock()
			current = parentActivityCaptureCurrentLocked(binding, capture, sequence, sequenceOK)
			binding.serial.Unlock()
			if current {
				m.evaluateManagedGoalAtSettled(binding, fence, sequence)
			}
		}
	})
}
