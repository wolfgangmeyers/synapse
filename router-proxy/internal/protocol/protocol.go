// Package protocol contains the small, shared v1 wire types used by the
// router. Validation here mirrors harness/protocol.js and protocol/v1/schema.json.
package protocol

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	Version                  = "v1"
	DefaultMaxBodyBytes      = 4 * 1024
	AbsoluteMaxBodyBytes     = 64 * 1024
	MessageIncarnationLength = 36
)

var (
	frameIDPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	agentIDPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	messageIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	failurePattern     = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	incarnationPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

// ValidationError is returned for payloads that fail the v1 schema.
type ValidationError struct{ Code string }

func (e *ValidationError) Error() string { return "invalid mailbox protocol payload" }

// Envelope is the six-field v1 message envelope plus the optional metadata
// fields present in the current schema.
type Envelope struct {
	ID        string              `json:"id"`
	From      string              `json:"from"`
	To        string              `json:"to"`
	Subject   *string             `json:"subject,omitempty"`
	Body      string              `json:"body"`
	CreatedAt string              `json:"created_at"`
	Sentinel  *SentinelDescriptor `json:"sentinel,omitempty"`
	Source    *MessageSource      `json:"source,omitempty"`
}

type SentinelDescriptor struct {
	Model string `json:"model"`
}
type MessageSource struct {
	Kind string `json:"kind"`
}

type DeliveryItem struct {
	MessageID            string `json:"message_id"`
	MessageIncarnationID string `json:"message_incarnation_id"`
	AttemptNumber        int    `json:"attempt_number"`
	DeliveryExpiresAt    string `json:"delivery_expires_at"`
}

type MessageFrame struct {
	Version              string         `json:"version"`
	FrameID              string         `json:"frame_id"`
	Type                 string         `json:"type"`
	Message              Envelope       `json:"message"`
	MessageIncarnationID string         `json:"message_incarnation_id"`
	AttemptNumber        int            `json:"attempt_number"`
	DeliveryExpiresAt    string         `json:"delivery_expires_at"`
	DeliveryItems        []DeliveryItem `json:"delivery_items,omitempty"`
	DeliveryMessages     []Envelope     `json:"delivery_messages,omitempty"`
}

type DeliveryReceipt struct {
	AgentID                        string
	InstanceID                     string
	LeaseID                        string
	MessageID                      string
	AttemptNumber                  int
	DeliveryItems                  []DeliveryItem
	AllowBatchWithoutDeliveryItems bool
}

func ValidateEnvelope(e Envelope, maxBodyBytes int) error {
	if maxBodyBytes <= 0 || maxBodyBytes > AbsoluteMaxBodyBytes {
		return fmt.Errorf("maxBodyBytes must be a positive safe integer no greater than 65536")
	}
	if !validMessageID(e.ID) || !validSender(e.From) || !validAgentID(e.To) || !validTimestamp(e.CreatedAt) || !utf8.ValidString(e.Body) || utf16Len(e.Body) > 65536 || len(e.Body) > maxBodyBytes {
		code := "invalid_request"
		if len(e.Body) > maxBodyBytes {
			code = "payload_too_large"
		}
		return &ValidationError{Code: code}
	}
	if e.Subject != nil && (!utf8.ValidString(*e.Subject) || utf16Len(*e.Subject) > 256) {
		return &ValidationError{Code: "invalid_request"}
	}
	if e.Sentinel != nil && (!utf8.ValidString(e.Sentinel.Model) || e.Sentinel.Model == "" || utf16Len(e.Sentinel.Model) > 128 || !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`).MatchString(e.Sentinel.Model)) {
		return &ValidationError{Code: "invalid_request"}
	}
	if e.Source != nil && !validSourceKind(e.Source.Kind) {
		return &ValidationError{Code: "invalid_request"}
	}
	return nil
}

func ValidateLeaseIdentity(agentID, instanceID, leaseID string) error {
	if !validAgentID(agentID) || !frameIDPattern.MatchString(instanceID) || !frameIDPattern.MatchString(leaseID) {
		return &ValidationError{Code: "invalid_request"}
	}
	return nil
}

func ValidateDeliveryIdentity(r DeliveryReceipt) error {
	if err := ValidateLeaseIdentity(r.AgentID, r.InstanceID, r.LeaseID); err != nil || !validMessageID(r.MessageID) || r.AttemptNumber < 1 {
		return &ValidationError{Code: "invalid_request"}
	}
	return nil
}

func ValidateDeliveryItem(item DeliveryItem) error {
	if !validMessageID(item.MessageID) || !incarnationPattern.MatchString(item.MessageIncarnationID) || item.AttemptNumber < 1 || !validTimestamp(item.DeliveryExpiresAt) {
		return &ValidationError{Code: "invalid_request"}
	}
	return nil
}

func ValidateMessageFrame(frame MessageFrame) error {
	if frame.Version != Version || !frameIDPattern.MatchString(frame.FrameID) || frame.Type != "message" || !incarnationPattern.MatchString(frame.MessageIncarnationID) || frame.AttemptNumber < 1 || !validTimestamp(frame.DeliveryExpiresAt) {
		return &ValidationError{Code: "invalid_request"}
	}
	if err := ValidateEnvelope(frame.Message, AbsoluteMaxBodyBytes); err != nil {
		return err
	}
	if len(frame.DeliveryItems) > 1 || (len(frame.DeliveryItems) > 0 && len(frame.DeliveryMessages) > 0) {
		if len(frame.DeliveryMessages) != len(frame.DeliveryItems) {
			return &ValidationError{Code: "invalid_request"}
		}
	}
	for index, message := range frame.DeliveryMessages {
		if err := ValidateEnvelope(message, AbsoluteMaxBodyBytes); err != nil {
			return err
		}
		if len(frame.DeliveryItems) > 0 && (message.ID != frame.DeliveryItems[index].MessageID || message.To != frame.Message.To) {
			return &ValidationError{Code: "invalid_request"}
		}
	}
	return nil
}

func NewEnvelope(clock interface{ Now() time.Time }, ids interface{ Next() (string, error) }, from, to string, subject *string, body string, sentinel *SentinelDescriptor) (Envelope, error) {
	id, err := ids.Next()
	if err != nil {
		return Envelope{}, err
	}
	envelope := Envelope{ID: id, From: from, To: to, Subject: subject, Body: body, CreatedAt: clock.Now().UTC().Format("2006-01-02T15:04:05.000Z"), Sentinel: sentinel}
	if err := ValidateEnvelope(envelope, DefaultMaxBodyBytes); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}

func NewMessageFrame(clock interface{ Now() time.Time }, ids interface{ Next() (string, error) }, message Envelope, item DeliveryItem, items []DeliveryItem) (MessageFrame, error) {
	if err := ValidateEnvelope(message, AbsoluteMaxBodyBytes); err != nil {
		return MessageFrame{}, err
	}
	if err := ValidateDeliveryItem(item); err != nil {
		return MessageFrame{}, err
	}
	if len(items) > 0 {
		for _, candidate := range items {
			if err := ValidateDeliveryItem(candidate); err != nil {
				return MessageFrame{}, err
			}
		}
		if items[0] != item {
			return MessageFrame{}, &ValidationError{Code: "invalid_request"}
		}
	}
	frameID, err := ids.Next()
	if err != nil {
		return MessageFrame{}, err
	}
	if items == nil {
		items = nil
	}
	return MessageFrame{Version: Version, FrameID: frameID, Type: "message", Message: message, MessageIncarnationID: item.MessageIncarnationID, AttemptNumber: item.AttemptNumber, DeliveryExpiresAt: item.DeliveryExpiresAt, DeliveryItems: items}, nil
}

func NewPingFrame(clock interface{ Now() time.Time }, ids interface{ Next() (string, error) }) (map[string]string, error) {
	id, err := ids.Next()
	if err != nil {
		return nil, err
	}
	_ = clock
	return map[string]string{"version": Version, "frame_id": id, "type": "ping"}, nil
}

func utf16Len(value string) int { return len(utf16.Encode([]rune(value))) }
func validMessageID(value string) bool {
	return utf8.ValidString(value) && utf16Len(value) >= 1 && utf16Len(value) <= 128 && messageIDPattern.MatchString(value)
}
func validSender(value string) bool {
	return utf8.ValidString(value) && utf16Len(value) >= 1 && utf16Len(value) <= 128
}
func validAgentID(value string) bool {
	return utf8.ValidString(value) && !strings.Contains(value, "@") && utf16Len(value) >= 1 && utf16Len(value) <= 128 && agentIDPattern.MatchString(value)
}
func validTimestamp(value string) bool {
	_, err := time.Parse("2006-01-02T15:04:05.000Z", value)
	return err == nil
}
func validSourceKind(value string) bool {
	switch value {
	case "agent", "web_ui", "android", "discord", "reminder", "system", "unknown":
		return true
	}
	return false
}
func ValidFailureReason(value string) bool {
	return utf8.ValidString(value) && utf16Len(value) >= 1 && utf16Len(value) <= 64 && failurePattern.MatchString(value)
}
func ValidIncarnation(value string) bool { return incarnationPattern.MatchString(value) }
func NormalizeSource(source *MessageSource) MessageSource {
	if source != nil && validSourceKind(source.Kind) {
		return *source
	}
	return MessageSource{Kind: "unknown"}
}
func CopyEnvelope(e Envelope) Envelope {
	if e.Subject != nil {
		s := *e.Subject
		e.Subject = &s
	}
	if e.Sentinel != nil {
		s := *e.Sentinel
		e.Sentinel = &s
	}
	if e.Source != nil {
		s := *e.Source
		e.Source = &s
	}
	return e
}
func SentinelJSON(s *SentinelDescriptor) (string, error) {
	if s == nil {
		return "", nil
	}
	b, err := json.Marshal(s)
	return string(b), err
}
func SameSentinel(left, right *SentinelDescriptor) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Model == right.Model
}
func TimestampAfter(value string, delta time.Duration) (string, error) {
	t, err := time.Parse("2006-01-02T15:04:05.000Z", value)
	if err != nil {
		return "", err
	}
	return t.Add(delta).UTC().Format("2006-01-02T15:04:05.000Z"), nil
}
func ParseTimestamp(value string) (time.Time, error) {
	return time.Parse("2006-01-02T15:04:05.000Z", value)
}
