package managed

import (
	"context"
	"errors"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

const builtinToolOrphanEndLimit = 64
const builtinToolOrphanEndLifetime = 30 * time.Second

type builtinToolOrphanEnd struct {
	event    BuiltinToolLifecycleEvent
	received time.Time
}

// installBuiltinToolLifecycleReceiver starts only after readiness has made the
// binding current. Pi's stdout is the native ordered source; no extension
// custom message is involved in active or terminal Complete state.
func (m *Manager) installBuiltinToolLifecycleReceiver(binding *managedBinding) {
	if binding == nil {
		return
	}
	rpc, ok := binding.rpc.(BuiltinToolLifecycleRPC)
	if !ok {
		return
	}
	rpc.SetBuiltinToolLifecycleEventHandler(func(event BuiltinToolLifecycleEvent) {
		m.handleBuiltinToolLifecycleEvent(binding, event)
	})
	if terminalRPC, ok := binding.rpc.(RPCReaderTerminalRPC); ok {
		terminalRPC.SetRPCReaderTerminalHandler(func(err error) {
			m.handleBuiltinToolLifecycleReaderTerminal(binding, err)
		})
	}
}

func (m *Manager) handleBuiltinToolLifecycleEvent(binding *managedBinding, event BuiltinToolLifecycleEvent) {
	if binding == nil || (event.Phase != "start" && event.Phase != "end") || !validBuiltinToolName(event.ToolName) || !validBuiltinToolCallID(event.ToolCallID) {
		return
	}
	binding.serial.Lock()
	fence := bindingFence(binding.agentID, binding)
	binding.serial.Unlock()
	if !m.rootBindingCurrent(context.Background(), binding, fence) {
		return
	}

	binding.builtinToolSerial.Lock()
	defer binding.builtinToolSerial.Unlock()
	if !m.rootBindingCurrent(context.Background(), binding, fence) {
		return
	}
	m.expireBuiltinToolOrphanEnds(binding)
	if event.Phase == "end" {
		entry, changed, err := m.store.FinishManagedBuiltinInvocation(context.Background(), fence, event.ToolCallID, builtinToolTerminalOutcome(event.IsError))
		if errors.Is(err, store.ErrManagedBuiltinInvocationNotFound) {
			m.bufferBuiltinToolOrphanEnd(binding, event)
			return
		}
		if err == nil && changed {
			m.publishCompleteHistory(entry)
			if !event.IsError {
				m.recordManagedGoalActivity(binding, fence, managedGoalActivity{Kind: "builtin", ID: entry.ID})
			}
		}
		return
	}

	entry, created, err := m.store.BeginManagedBuiltinInvocation(context.Background(), fence, event.ToolCallID, event.ToolName)
	if err != nil {
		return
	}
	if created {
		m.publishCompleteHistory(entry)
	}
	orphan, found := binding.builtinToolOrphanEnds[event.ToolCallID]
	if !found || orphan.event.ToolName != event.ToolName {
		return
	}
	delete(binding.builtinToolOrphanEnds, event.ToolCallID)
	terminal, changed, err := m.store.FinishManagedBuiltinInvocation(context.Background(), fence, event.ToolCallID, builtinToolTerminalOutcome(orphan.event.IsError))
	if err == nil && changed {
		m.publishCompleteHistory(terminal)
		if !orphan.event.IsError {
			m.recordManagedGoalActivity(binding, fence, managedGoalActivity{Kind: "builtin", ID: terminal.ID})
		}
	}
}

// handleBuiltinToolLifecycleReaderTerminal reconciles only the exact stream
// fence. EOF is missing telemetry, not process loss, so it must not stop or
// otherwise alter the managed process lifecycle.
func (m *Manager) handleBuiltinToolLifecycleReaderTerminal(binding *managedBinding, err error) {
	if binding == nil || err == nil {
		return
	}
	binding.serial.Lock()
	fence := bindingFence(binding.agentID, binding)
	binding.serial.Unlock()
	m.startManagedTask(func() {
		binding.lane.serial.Lock()
		defer binding.lane.serial.Unlock()
		m.interruptBuiltinToolInvocations(binding, fence)
	})
}

// reconcileBuiltinToolTelemetryMissing runs synchronously on the native
// stdout intake path. A settled parent turn does not prove a builtin success;
// it only proves the tool stream can no longer provide its terminal event.
func (m *Manager) reconcileBuiltinToolTelemetryMissing(binding *managedBinding) {
	if binding == nil {
		return
	}
	binding.lane.serial.Lock()
	defer binding.lane.serial.Unlock()
	binding.serial.Lock()
	fence := bindingFence(binding.agentID, binding)
	binding.serial.Unlock()
	if !m.runningBindingCurrent(context.Background(), binding, fence) {
		return
	}
	m.interruptBuiltinToolInvocations(binding, fence)
}

func builtinToolTerminalOutcome(isError bool) store.ManagedBuiltinToolOutcome {
	if isError {
		return store.ManagedBuiltinToolFailed
	}
	return store.ManagedBuiltinToolSucceeded
}

func (m *Manager) expireBuiltinToolOrphanEnds(binding *managedBinding) {
	if binding.builtinToolOrphanEnds == nil {
		return
	}
	cutoff := m.clock.Now().Add(-builtinToolOrphanEndLifetime)
	for callID, orphan := range binding.builtinToolOrphanEnds {
		if !orphan.received.After(cutoff) {
			delete(binding.builtinToolOrphanEnds, callID)
		}
	}
}

func (m *Manager) bufferBuiltinToolOrphanEnd(binding *managedBinding, event BuiltinToolLifecycleEvent) {
	if binding.builtinToolOrphanEnds == nil {
		binding.builtinToolOrphanEnds = map[string]builtinToolOrphanEnd{}
	}
	if _, exists := binding.builtinToolOrphanEnds[event.ToolCallID]; exists || len(binding.builtinToolOrphanEnds) >= builtinToolOrphanEndLimit {
		return
	}
	binding.builtinToolOrphanEnds[event.ToolCallID] = builtinToolOrphanEnd{event: event, received: m.clock.Now()}
}

// interruptBuiltinToolInvocations is called at every proved loss/replacement
// boundary. A late old stdout callback cannot reopen a row because binding and
// full-fence checks run before every lifecycle store operation.
func (m *Manager) interruptBuiltinToolInvocations(binding *managedBinding, fence store.ManagedRuntimeFence) {
	entries, err := m.store.InterruptActiveManagedBuiltinInvocations(context.Background(), fence)
	if err != nil {
		return
	}
	for _, entry := range entries {
		m.publishCompleteHistory(entry)
	}
	if binding != nil {
		binding.builtinToolSerial.Lock()
		binding.builtinToolOrphanEnds = nil
		binding.builtinToolSerial.Unlock()
	}
}
