package protocol

import (
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func TestValidateEnvelopeMatchesV1Bounds(t *testing.T) {
	valid := Envelope{ID: "message-1", From: "sender", To: "agent-1", Body: "hello", CreatedAt: "2026-08-29T12:00:00.000Z"}
	require.NoError(t, ValidateEnvelope(valid, DefaultMaxBodyBytes))
	invalid := valid
	invalid.To = "agent@example.com"
	require.Error(t, ValidateEnvelope(invalid, DefaultMaxBodyBytes))
	invalid = valid
	invalid.ID = "bad id"
	require.Error(t, ValidateEnvelope(invalid, DefaultMaxBodyBytes))
	invalid = valid
	invalid.CreatedAt = "2026-08-29T12:00:00Z"
	require.Error(t, ValidateEnvelope(invalid, DefaultMaxBodyBytes))
	invalid = valid
	invalid.Body = "12345"
	err := ValidateEnvelope(invalid, 4)
	var validation *ValidationError
	require.ErrorAs(t, err, &validation)
	require.Equal(t, "payload_too_large", validation.Code)
}

func TestEnvelopeUsesInjectedClockAndIDSource(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	ids := deterministic.NewSequenceIDSource("message-1")
	envelope, err := NewEnvelope(clock, ids, "sender", "agent-1", nil, "body", nil)
	require.NoError(t, err)
	require.Equal(t, "message-1", envelope.ID)
	require.Equal(t, "2026-08-29T12:00:00.000Z", envelope.CreatedAt)
	require.Equal(t, []string{"message-1"}, ids.Calls())
	require.Len(t, clock.Calls(), 1)
}

func TestMessageFrameUsesInjectedIDsAndPreservesDeliveryIdentity(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	ids := deterministic.NewSequenceIDSource("frame-1")
	message := Envelope{ID: "message-1", From: "sender", To: "agent-1", Body: "hello", CreatedAt: "2026-08-29T12:00:00.000Z"}
	item := DeliveryItem{MessageID: "message-1", MessageIncarnationID: "123e4567-e89b-42d3-a456-426614174000", AttemptNumber: 1, DeliveryExpiresAt: "2026-08-29T12:00:30.000Z"}
	frame, err := NewMessageFrame(clock, ids, message, item, []DeliveryItem{item})
	require.NoError(t, err)
	require.Equal(t, "frame-1", frame.FrameID)
	require.Equal(t, item.MessageIncarnationID, frame.MessageIncarnationID)
	require.Equal(t, []DeliveryItem{item}, frame.DeliveryItems)
	require.Equal(t, []string{"frame-1"}, ids.Calls())
	require.Len(t, clock.Calls(), 0, "frame construction has no implicit wall-clock read")
}
