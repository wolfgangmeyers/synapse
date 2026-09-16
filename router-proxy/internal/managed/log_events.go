package managed

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

// emitEvent is the only manager-owned sink seam. No managed producer is wired
// to it yet; later producers can call it without importing transport.
func (m *Manager) emitEvent(event Event) {
	if m == nil || m.eventSink == nil || !event.Valid() {
		return
	}
	m.eventSink(event)
}
