package managed

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

const (
	managedEventSubjectMaxBytes   = 256
	managedEventBodyMaxBytes      = protocol.AbsoluteMaxBodyBytes
	managedEventErrorMaxBytes     = 2_000
	managedEventArgumentsMaxBytes = 4_096
	managedEventTruncationMark    = "[TRUNCATED]"

	managedRootControlRequestOperation = "request"
	managedRootControlResultOperation  = "result"
	managedRootControlAdmissionPhase   = "admission"
	managedRootControlCompletionPhase  = "completion"
	managedRootControlAcceptedStatus   = "accepted"
	managedRootControlRejectedStatus   = "rejected"

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
	// Arguments is canonical JSON containing only the operation-specific scalar
	// arguments explicitly approved by rootControlSafeArguments. User text,
	// nested payloads, and all unapproved fields are excluded.
	Arguments string
	Status    string
	ErrorCode string
	ErrorText string
	Phase     string
	ToolID    string
	CallID    string
	Truncated bool
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

// rootControlSafeArguments projects only closed, scalar fields whose values
// are useful for lifecycle diagnostics and are not user-entered content. The
// full validated request remains private to the handler and durable ledger.
func rootControlSafeArguments(operationID string, arguments any) (string, bool) {
	object, ok := arguments.(map[string]any)
	if !ok {
		return "", false
	}
	allowed := map[string]map[string]struct{}{
		"goal.control":             {"action": {}},
		"goal.snooze":              {},
		"context.control":          {"action": {}},
		"ops.git_status":           {"action": {}},
		"mail.send":                {},
		"mail.parent":              {},
		"ops.pr_status":            {"action": {}, "pullRequest": {}},
		"ops.priority":             {"action": {}, "status": {}},
		"recall.control":           {"action": {}},
		"reminder.control":         {"action": {}, "delayMinutes": {}, "intervalMinutes": {}},
		"policy.inline_allowance":  {"usageCount": {}},
		"router.control":           {"action": {}},
		"notifications.settled":    {"action": {}},
		"ops.state":                {"action": {}, "state": {}, "field": {}},
		"ops.verification_handoff": {"action": {}, "remoteVerified": {}},
		"ops.worker":               {"action": {}},
		"web.search":               {"freshness": {}, "maxResults": {}},
	}
	keys, ok := allowed[operationID]
	if !ok {
		return "", false
	}
	projected := make(map[string]any, len(keys))
	for key := range keys {
		value, present := object[key]
		if !present {
			continue
		}
		switch value.(type) {
		case bool, string, json.Number, int, int32, int64, uint, uint64, float32, float64:
			projected[key] = value
		}
	}
	if len(projected) == 0 {
		return "", false
	}
	canonical, err := canonicalJSON(projected)
	if err != nil {
		return "", false
	}
	bounded, truncated := boundedManagedEventText(string(canonical), managedEventArgumentsMaxBytes)
	return bounded, truncated
}

func managedRootControlRequestEvent(agentID string, event RootControlEvent) Event {
	arguments, truncated := rootControlSafeArguments(event.OperationID, event.Arguments)
	return Event{
		Kind: EventManagedRootControl, AgentID: agentID, Operation: managedRootControlRequestOperation,
		OperationID: event.OperationID, RequestID: event.RequestID, ToolID: event.ToolName,
		CallID: event.ToolCallID, Arguments: arguments, Phase: managedRootControlAdmissionPhase,
		Status: managedRootControlAcceptedStatus, Truncated: truncated,
	}
}

func managedRootControlRejectedEvent(agentID string) Event {
	return Event{
		Kind: EventManagedRootControl, AgentID: agentID, Operation: managedRootControlRequestOperation,
		Phase: managedRootControlAdmissionPhase, Status: managedRootControlRejectedStatus,
		ErrorCode: "INVALID_REQUEST",
	}
}

func managedRootControlResultEvent(row store.ManagedControlRequest) Event {
	content, contentTruncated := boundedManagedEventText(row.ResultContent, managedEventBodyMaxBytes)
	errorText, errorTruncated := boundedManagedEventText(row.ErrorDetail, managedEventErrorMaxBytes)
	return Event{
		Kind: EventManagedRootControl, AgentID: row.AgentID, Operation: managedRootControlResultOperation,
		OperationID: row.OperationID, RequestID: row.RequestID, ToolID: row.ToolName,
		CallID: row.ToolCallID, Phase: managedRootControlCompletionPhase, Status: row.ResultStatus,
		Content: content, ErrorCode: row.ErrorCode, ErrorText: errorText,
		Truncated: contentTruncated || errorTruncated,
	}
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
