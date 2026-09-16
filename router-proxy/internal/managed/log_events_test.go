package managed

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

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

func TestManagerEventSinkPanicIsContained(t *testing.T) {
	manager := &Manager{eventSink: func(Event) { panic("synthetic sink failure") }}
	require.NotPanics(t, func() {
		manager.emitEvent(Event{Kind: EventManagedMailboxIncoming, AgentID: "agent", MessageID: "message"})
	})
}

func TestManagedMailboxEventTextUsesFixedFallbackAndByteSafeBounds(t *testing.T) {
	subject := strings.Repeat("雪", 100)
	body := strings.Repeat("雪", 22_000)
	attempt := store.ManagedDirectAttempt{
		MessageID: "message",
		Fence:     store.ManagedRuntimeFence{AgentID: "agent"},
		Message:   protocolMessage("agent", subject, body),
	}
	incoming := managedMailboxEventText(EventManagedMailboxIncoming, attempt, body, "incoming", ManagedMailboxIncomingOperation, ManagedMailboxIncomingStatus)
	require.Equal(t, 254, len([]byte(incoming.Subject)))
	require.Equal(t, 65_534, len([]byte(incoming.Body)))
	require.True(t, utf8.ValidString(incoming.Subject))
	require.True(t, utf8.ValidString(incoming.Body))
	require.True(t, incoming.Truncated)
	require.Equal(t, subject[:len(subject)-len("雪")*19]+managedEventTruncationMark, incoming.Subject)
	require.Equal(t, body[:len(body)-len("雪")*159]+managedEventTruncationMark, incoming.Body)

	outgoing := managedMailboxEventText(EventManagedMailboxOutgoing, store.ManagedDirectAttempt{
		MessageID: "reply", Fence: store.ManagedRuntimeFence{AgentID: "agent"},
	}, "reply\r\n雪", "outgoing", ManagedMailboxOutgoingOperation, ManagedMailboxOutgoingStatus)
	require.Equal(t, "agent", outgoing.Source)
	require.Equal(t, "reply\r\n雪", outgoing.Body)
	require.Empty(t, outgoing.Subject)
	require.False(t, outgoing.Truncated)
}

// protocolMessage keeps the bounds test focused on the manager event seam.
func protocolMessage(source, subject, body string) domain.Message {
	return domain.Message{Source: protocol.MessageSource{Kind: source}, Subject: &subject, Body: body}
}
