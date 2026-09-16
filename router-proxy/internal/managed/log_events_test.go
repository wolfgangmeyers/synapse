package managed

import "testing"

func TestManagerEventSinkNilIsNoOp(t *testing.T) {
	manager := &Manager{}
	manager.emitEvent(Event{Kind: EventManagedProcess, AgentID: "agent", Status: "started"})
	manager.emitEvent(Event{Kind: EventKind("unknown")})
}

func TestManagedEventVocabularyIsClosed(t *testing.T) {
	for _, kind := range []EventKind{
		EventManagedMailboxIncoming,
		EventManagedMailboxOutgoing,
		EventManagedRootControl,
		EventManagedReminder,
		EventManagedToolLifecycle,
		EventManagedProcess,
		EventManagedRPC,
		EventManagedRecovery,
	} {
		if !(Event{Kind: kind}).Valid() {
			t.Fatalf("expected managed event kind %q to be valid", kind)
		}
	}
	if (Event{Kind: EventKind("provider_payload")}).Valid() {
		t.Fatal("unexpected open managed event vocabulary")
	}
}

func TestManagerEventSinkReceivesTypedEvent(t *testing.T) {
	var received Event
	manager := &Manager{eventSink: func(event Event) { received = event }}
	manager.emitEvent(Event{Kind: EventManagedRPC, OperationID: "operation", CallID: "call", Status: "failed"})
	if received.Kind != EventManagedRPC || received.OperationID != "operation" || received.CallID != "call" || received.Status != "failed" {
		t.Fatalf("unexpected typed event: %#v", received)
	}
}
