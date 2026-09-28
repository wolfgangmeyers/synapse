// Package domain defines mailbox state without storage or transport concerns.
package domain

import (
	"encoding/json"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
)

const (
	MessageQueued              = "queued"
	MessageLeased              = "leased"
	MessageInjectionRequested  = "injection_requested"
	MessageAcknowledged        = "acknowledged"
	MessageDeadLetter          = "dead_letter"
	DeliveryLeased             = "leased"
	DeliveryInjectionRequested = "injection_requested"
	DeliveryAcknowledged       = "acknowledged"
	DeliveryNacked             = "nacked"
	DeliveryTimedOut           = "timed_out"
)

const (
	BatchHeader    = "Mailbox message batch"
	BatchSeparator = "\n\n---\n\n"
)

type Message struct {
	EnqueueSequence      int64                        `json:"enqueue_sequence"`
	ID                   string                       `json:"id"`
	From                 string                       `json:"from"`
	To                   string                       `json:"to"`
	Subject              *string                      `json:"subject,omitempty"`
	Body                 string                       `json:"body"`
	CreatedAt            string                       `json:"created_at"`
	ExpiresAt            string                       `json:"expires_at"`
	MessageIncarnationID string                       `json:"message_incarnation_id"`
	State                string                       `json:"state"`
	Sentinel             *protocol.SentinelDescriptor `json:"sentinel,omitempty"`
	Source               protocol.MessageSource       `json:"source"`
}

func (m Message) Envelope() protocol.Envelope {
	return protocol.Envelope{ID: m.ID, From: m.From, To: m.To, Subject: m.Subject, Body: m.Body, CreatedAt: m.CreatedAt, Sentinel: m.Sentinel, Source: &m.Source}
}

type DeliveryItem = protocol.DeliveryItem

type Delivery struct {
	Message Message `json:"message"`
	// Messages keeps the original envelopes for the additive multi-delivery
	// wire field. Message remains the legacy merged envelope.
	Messages          []Message      `json:"messages"`
	AttemptNumber     int            `json:"attempt_number"`
	DeliveryExpiresAt string         `json:"delivery_expires_at"`
	Items             []DeliveryItem `json:"items"`
}

type Reminder struct {
	ID                 string                       `json:"id"`
	Target             string                       `json:"target"`
	Creator            string                       `json:"creator"`
	Subject            *string                      `json:"subject,omitempty"`
	Body               string                       `json:"body"`
	CreatedAt          string                       `json:"created_at"`
	NextDueAt          string                       `json:"next_due_at"`
	IntervalMS         *int64                       `json:"interval_ms"`
	Sentinel           *protocol.SentinelDescriptor `json:"sentinel,omitempty"`
	State              string                       `json:"state"`
	LinkedMessageID    *string                      `json:"linked_message_id,omitempty"`
	LinkedMessageState *string                      `json:"linked_message_state,omitempty"`
	OccurrenceNumber   int                          `json:"occurrence_number"`
	AttemptCount       int                          `json:"attempt_count"`
	LastAttemptAt      *string                      `json:"last_attempt_at,omitempty"`
}

type ConversationMessage struct {
	Sequence             int64                  `json:"sequence"`
	ID                   string                 `json:"id"`
	DeliveryMessageID    string                 `json:"-"`
	MessageIncarnationID string                 `json:"message_incarnation_id"`
	From                 string                 `json:"from"`
	To                   string                 `json:"to"`
	Subject              *string                `json:"subject,omitempty"`
	Body                 string                 `json:"body"`
	CreatedAt            string                 `json:"created_at"`
	RecordedAt           string                 `json:"recorded_at"`
	Attachments          []json.RawMessage      `json:"attachments"`
	Source               protocol.MessageSource `json:"source"`
}

type Lease struct {
	LeaseID    string `json:"lease_id"`
	AgentID    string `json:"agent_id"`
	InstanceID string `json:"instance_id"`
	AcquiredAt string `json:"acquired_at"`
	ExpiresAt  string `json:"expires_at"`
	Recovered  bool   `json:"recovered"`
}

// MailboxContent adds runtime reply instructions without changing the stored body.
func MailboxContent(body, from string) string {
	return body + "\n\nReply via mailbox to " + from
}

func BatchItem(message Message) string {
	parts := []string{"From: " + message.From}
	if message.Subject != nil {
		parts = append(parts, "Subject: "+*message.Subject)
	}
	parts = append(parts, "", message.Body, "", "[Reply via mailbox to "+message.From+".]")
	return strings.Join(parts, "\n")
}
func IsBatchedBody(body string) bool { return strings.HasPrefix(body, BatchHeader+"\n\n") }
func BatchContent(message Message) string {
	if !IsBatchedBody(message.Body) {
		return BatchItem(message)
	}
	return strings.TrimPrefix(message.Body, BatchHeader+"\n\n")
}
func BatchedBody(target, candidate Message) string {
	existing := target.Body
	if !IsBatchedBody(existing) {
		existing = BatchHeader + "\n\n" + BatchItem(target)
	}
	return existing + BatchSeparator + BatchContent(candidate)
}
func BuildDeliveryMessage(rows []Message) Message {
	first := rows[0]
	if len(rows) == 1 {
		return first
	}
	body := first.Body
	for _, row := range rows[1:] {
		current := first
		current.Body = body
		body = BatchedBody(current, row)
	}
	first.Body = body
	return first
}
