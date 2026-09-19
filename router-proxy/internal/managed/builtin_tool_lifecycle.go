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

type builtinToolLifecycleQueueItem struct {
	event     BuiltinToolLifecycleEvent
	reconcile bool
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
		// Ordinary tools persist on Pi's semantic callback lane so a following
		// parent-settled frame cannot overtake their start or terminal row. Coding
		// builtins retain the bounded manager queue used by control-sensitive paths.
		if !validBuiltinToolName(event.ToolName) {
			m.handleBuiltinToolLifecycleEvent(binding, event)
			return
		}
		m.enqueueBuiltinToolLifecycle(binding, event)
	})
	if terminalRPC, ok := binding.rpc.(RPCReaderTerminalRPC); ok {
		terminalRPC.SetRPCReaderTerminalHandler(func(err error) {
			m.handleBuiltinToolLifecycleReaderTerminal(binding, err)
		})
	}
}

// enqueueBuiltinToolLifecycle keeps Pi's one stdout reader out of durable
// lifecycle work. Each binding has one ordered worker, so start/end order stays
// native while a SQLite, history, or goal-activity delay cannot stall controls.
func (m *Manager) enqueueBuiltinToolLifecycle(binding *managedBinding, event BuiltinToolLifecycleEvent) {
	m.enqueueBuiltinToolLifecycleItem(binding, builtinToolLifecycleQueueItem{event: event})
}

// enqueueBuiltinToolLifecycleReconciliation adds the loss boundary to the same
// FIFO as source telemetry. A start immediately before EOF or parent settlement
// must be recorded before the loss boundary interrupts it.
func (m *Manager) enqueueBuiltinToolLifecycleReconciliation(binding *managedBinding) {
	m.enqueueBuiltinToolLifecycleItem(binding, builtinToolLifecycleQueueItem{reconcile: true})
}

func (m *Manager) enqueueBuiltinToolLifecycleItem(binding *managedBinding, item builtinToolLifecycleQueueItem) {
	if binding == nil {
		return
	}
	binding.builtinToolQueueSerial.Lock()
	if binding.builtinToolQueueStopped || binding.builtinToolOverflowed {
		binding.builtinToolQueueSerial.Unlock()
		return
	}
	if len(binding.builtinToolQueue) < maxQueuedManagedBuiltinToolLifecycle {
		binding.builtinToolQueue = append(binding.builtinToolQueue, item)
	} else {
		// A dropped lifecycle frame cannot safely be inferred. Interrupt the exact
		// fence, discard the uncertain tail, and accept a later fresh start.
		binding.builtinToolQueue = nil
		binding.builtinToolOverflowed = true
	}
	startDrain := !binding.builtinToolDraining
	if startDrain {
		binding.builtinToolDraining = true
	}
	binding.builtinToolQueueSerial.Unlock()
	if !startDrain {
		return
	}
	if m.startManagedTask(func() { m.drainBuiltinToolLifecycle(binding) }) {
		return
	}
	binding.builtinToolQueueSerial.Lock()
	binding.builtinToolQueue = nil
	binding.builtinToolOverflowed = false
	binding.builtinToolDraining = false
	binding.builtinToolQueueStopped = true
	binding.builtinToolQueueSerial.Unlock()
}

func (m *Manager) drainBuiltinToolLifecycle(binding *managedBinding) {
	for {
		binding.builtinToolQueueSerial.Lock()
		if binding.builtinToolQueueStopped {
			binding.builtinToolQueue = nil
			binding.builtinToolOverflowed = false
			binding.builtinToolDraining = false
			binding.builtinToolQueueSerial.Unlock()
			return
		}
		if binding.builtinToolOverflowed {
			binding.builtinToolQueue = nil
			binding.builtinToolOverflowed = false
			binding.builtinToolQueueSerial.Unlock()
			binding.serial.Lock()
			fence := bindingFence(binding.agentID, binding)
			binding.serial.Unlock()
			m.interruptBuiltinToolInvocations(binding, fence)
			continue
		}
		if len(binding.builtinToolQueue) == 0 {
			binding.builtinToolDraining = false
			binding.builtinToolQueueSerial.Unlock()
			return
		}
		item := binding.builtinToolQueue[0]
		binding.builtinToolQueue[0] = builtinToolLifecycleQueueItem{}
		binding.builtinToolQueue = binding.builtinToolQueue[1:]
		binding.builtinToolQueueSerial.Unlock()
		if item.reconcile {
			m.reconcileBuiltinToolTelemetryMissingInQueue(binding)
			continue
		}
		m.handleBuiltinToolLifecycleEvent(binding, item.event)
	}
}

// stopBuiltinToolLifecycleQueue drops only undurable buffered telemetry during
// shutdown. The admitted drain worker exits after its current callback, and
// Manager.Close waits for it through startWG before returning.
func (m *Manager) stopBuiltinToolLifecycleQueue(binding *managedBinding) {
	if binding == nil {
		return
	}
	binding.builtinToolQueueSerial.Lock()
	binding.builtinToolQueueStopped = true
	binding.builtinToolQueue = nil
	binding.builtinToolOverflowed = false
	binding.builtinToolQueueSerial.Unlock()
}

func (m *Manager) handleBuiltinToolLifecycleEvent(binding *managedBinding, event BuiltinToolLifecycleEvent) {
	if binding == nil || (event.Phase != "start" && event.Phase != "end") || !validOrdinaryToolName(event.ToolName) || !validBuiltinToolCallID(event.ToolCallID) {
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
		entry, changed, err := m.store.FinishManagedToolInvocation(context.Background(), fence, event.ToolCallID, event.ToolName, builtinToolTerminalOutcome(event.IsError))
		if errors.Is(err, store.ErrManagedBuiltinInvocationNotFound) {
			// A terminal frame never manufactures an invocation. Buffer it briefly
			// so a matching delayed start can establish the immutable name and input.
			m.bufferBuiltinToolOrphanEnd(binding, event)
			return
		}
		if err == nil && changed {
			m.publishCompleteHistory(entry)
			if !event.IsError && validBuiltinToolName(event.ToolName) {
				m.recordManagedGoalActivity(binding, fence, managedGoalActivity{Kind: "builtin", ID: entry.ID})
			}
		}
		return
	}

	entry, created, err := m.store.BeginManagedToolInvocation(context.Background(), fence, event.ToolCallID, event.ToolName, event.ToolInput)
	if err != nil {
		return
	}
	if created {
		m.publishCompleteHistory(entry)
	}
	orphan, found := binding.builtinToolOrphanEnds[event.ToolCallID]
	if !found {
		return
	}
	delete(binding.builtinToolOrphanEnds, event.ToolCallID)
	if orphan.event.ToolName != event.ToolName {
		return
	}
	terminal, changed, err := m.store.FinishManagedToolInvocation(context.Background(), fence, event.ToolCallID, event.ToolName, builtinToolTerminalOutcome(orphan.event.IsError))
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
	m.enqueueBuiltinToolLifecycleReconciliation(binding)
}

// reconcileBuiltinToolTelemetryMissing preserves native stdout order by placing
// parent settlement behind every earlier lifecycle event. A settled parent turn
// does not prove a builtin success; it only proves the tool stream can no
// longer provide its terminal event.
func (m *Manager) reconcileBuiltinToolTelemetryMissing(binding *managedBinding) {
	m.enqueueBuiltinToolLifecycleReconciliation(binding)
}

func (m *Manager) reconcileBuiltinToolTelemetryMissingInQueue(binding *managedBinding) {
	if binding == nil || binding.lane == nil {
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
