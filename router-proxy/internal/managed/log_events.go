package managed

import (
	"unicode/utf8"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

const (
	managedEventSubjectMaxBytes = 256
	managedEventBodyMaxBytes    = protocol.AbsoluteMaxBodyBytes
	managedEventTruncationMark  = "[TRUNCATED]"

	ManagedMailboxIncomingOperation = "append"
	ManagedMailboxIncomingStatus    = "acknowledged"
	ManagedMailboxOutgoingOperation = "publish"
	ManagedMailboxOutgoingStatus    = "delivered"

	// A recovered outbox row retains its body but not the original message
	// envelope. Keep the fixed event shape and use the known reply source.
	managedMailboxOutgoingSource = "agent"
	managedMailboxIncomingSource = "unknown"
)

// EventKind is the closed vocabulary of managed events that may eventually be
// sent to the router's bounded diagnostic log. Producers are added separately;
// this phase only defines the typed sink contract.
type EventKind string

const (
	EventManagedMailboxIncoming EventKind = "managed_mailbox_incoming"
	EventManagedMailboxOutgoing EventKind = "managed_mailbox_outgoing"
	EventManagedRootControl     EventKind = "managed_root_control"
	EventManagedReminder        EventKind = "managed_reminder"
	EventManagedToolLifecycle   EventKind = "managed_tool_lifecycle"
	EventManagedProcess         EventKind = "managed_process"
	EventManagedRPC             EventKind = "managed_rpc"
	EventManagedRecovery        EventKind = "managed_recovery"
)

// Event is a deliberately fixed-shape managed diagnostic. It has no map or
// provider payload field, so a producer cannot pass an arbitrary object to the
// log sink. Empty fields are omitted by the router adapter.
type Event struct {
	Kind        EventKind
	AgentID     string
	Direction   string
	Source      string
	Operation   string
	MessageID   string
	OperationID string
	RequestID   string
	ReminderID  string
	Subject     string
	Body        string
	Message     string
	Content     string
	Status      string
	ErrorCode   string
	ErrorText   string
	Phase       string
	ToolID      string
	CallID      string
	Truncated   bool
}

// EventSink receives typed managed diagnostics. A nil sink disables this
// optional path without changing managed behavior.
type EventSink func(Event)

func (e Event) Valid() bool {
	switch e.Kind {
	case EventManagedMailboxIncoming, EventManagedMailboxOutgoing,
		EventManagedRootControl, EventManagedReminder, EventManagedToolLifecycle,
		EventManagedProcess, EventManagedRPC, EventManagedRecovery:
		return true
	default:
		return false
	}
}

// emitEvent is the only manager-owned sink seam. Sink failures are isolated
// here because logging is diagnostic and must not alter a durable transition.
func (m *Manager) emitEvent(event Event) {
	if m == nil || m.eventSink == nil || !event.Valid() {
		return
	}
	defer func() { _ = recover() }()
	m.eventSink(event)
}

func boundedManagedEventText(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	prefixLimit := limit - len(managedEventTruncationMark)
	if prefixLimit < 0 {
		return managedEventTruncationMark[:limit], true
	}
	prefix := value[:prefixLimit]
	for !utf8.ValidString(prefix) {
		prefix = prefix[:len(prefix)-1]
	}
	return prefix + managedEventTruncationMark, true
}

func managedMailboxEventText(kind EventKind, attempt store.ManagedDirectAttempt, body string, direction, operation, status string) Event {
	subject := ""
	if attempt.Message.Subject != nil {
		subject = *attempt.Message.Subject
	}
	subject, subjectTruncated := boundedManagedEventText(subject, managedEventSubjectMaxBytes)
	body, bodyTruncated := boundedManagedEventText(body, managedEventBodyMaxBytes)
	source := attempt.Message.Source.Kind
	if direction == "outgoing" {
		// Replies are produced by the managed agent. The incoming envelope's
		// source may be reminder/web UI, and recovered outbox rows omit it.
		source = managedMailboxOutgoingSource
	} else if source == "" {
		source = managedMailboxIncomingSource
	}
	return Event{
		Kind: kind, AgentID: attempt.Fence.AgentID, Direction: direction,
		Source: source, Operation: operation, MessageID: attempt.MessageID,
		Subject: subject, Body: body, Status: status, Truncated: subjectTruncated || bodyTruncated,
	}
}
