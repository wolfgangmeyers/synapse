package managed

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func nextRootLogEvent(t *testing.T, events <-chan Event) Event {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for managed root-control event")
		return Event{}
	}
}

func TestRootControlEventsUseDurableAdmissionAndCompletionSeams(t *testing.T) {
	manager, durable, binding, fence, tool, _, handler := newRootAdmissionFixtureWithReceiver(t, false)
	events := make(chan Event, 4)
	manager.eventSink = func(event Event) { events <- event }
	handler.result = RootControlHandlerResult{Status: "ok", Content: "line one\n雪 line two", Details: map[string]any{"accepted": true}}
	requestID := "123e4567-e89b-42d3-a456-426614174351"

	manager.admitAndRunRootControl(context.Background(), binding, rootAdmissionEvent(fence, tool, requestID, rootAdmissionArguments("private multiline\n雪")))
	row := requireManagedControlDelivered(t, durable, requestID)
	require.Equal(t, "ok", row.ResultStatus)

	request := nextRootLogEvent(t, events)
	require.Equal(t, EventManagedRootControl, request.Kind)
	require.Equal(t, fence.AgentID, request.AgentID)
	require.Equal(t, managedRootControlRequestOperation, request.Operation)
	require.Equal(t, managedRootControlAcceptedStatus, request.Status)
	require.Equal(t, tool.OperationID, request.OperationID)
	require.Equal(t, tool.Name, request.ToolID)
	require.Equal(t, "root-call-1", request.CallID)
	require.Equal(t, requestID, request.RequestID)
	require.Equal(t, `{"action":"status"}`, request.Arguments)
	require.NotContains(t, request.Arguments, "private multiline")

	result := nextRootLogEvent(t, events)
	require.Equal(t, managedRootControlResultOperation, result.Operation)
	require.Equal(t, "ok", result.Status)
	require.Equal(t, "line one\n雪 line two", result.Content)
	require.Empty(t, result.ErrorCode)
	require.Empty(t, result.ErrorText)
	require.False(t, result.Truncated)
}

func TestRootControlValidationEventExcludesUnvalidatedArguments(t *testing.T) {
	manager, _, binding, fence, tool, _, handler := newRootAdmissionFixtureWithReceiver(t, false)
	events := make(chan Event, 1)
	manager.eventSink = func(event Event) { events <- event }
	raw := "private-argument-marker"
	manager.admitAndRunRootControl(context.Background(), binding, rootAdmissionEvent(fence, tool, "123e4567-e89b-42d3-a456-426614174352", map[string]any{
		"action": "status", "task": raw, "unexpected": true,
	}))
	event := nextRootLogEvent(t, events)
	require.Equal(t, EventManagedRootControl, event.Kind)
	require.Equal(t, managedRootControlRejectedStatus, event.Status)
	require.Equal(t, "INVALID_REQUEST", event.ErrorCode)
	require.Empty(t, event.Arguments)
	require.NotContains(t, event.RequestID, raw)
	require.Zero(t, handler.callCount())
}

func TestRootControlHandlerErrorEventCarriesOnlyBoundedSafeFields(t *testing.T) {
	manager, durable, binding, fence, tool, _, handler := newRootAdmissionFixtureWithReceiver(t, false)
	events := make(chan Event, 2)
	manager.eventSink = func(event Event) { events <- event }
	handler.result = RootControlHandlerResult{Status: "error", Content: "The operation failed.", Details: map[string]any{}, ErrorCode: "SAFE_FAILURE", ErrorMessage: "safe failure\n雪"}
	requestID := "123e4567-e89b-42d3-a456-426614174353"
	manager.admitAndRunRootControl(context.Background(), binding, rootAdmissionEvent(fence, tool, requestID, rootAdmissionArguments("router")))
	requireManagedControlDelivered(t, durable, requestID)
	_ = nextRootLogEvent(t, events)
	result := nextRootLogEvent(t, events)
	require.Equal(t, "error", result.Status)
	require.Equal(t, "SAFE_FAILURE", result.ErrorCode)
	require.Equal(t, "safe failure\n雪", result.ErrorText)
	require.Equal(t, "The operation failed.", result.Content)
}

func TestRootControlInvalidHandlerResultEmitsTerminalFailureEvent(t *testing.T) {
	manager, durable, binding, fence, tool, _, handler := newRootAdmissionFixtureWithReceiver(t, false)
	events := make(chan Event, 2)
	manager.eventSink = func(event Event) { events <- event }
	handler.result = RootControlHandlerResult{Status: "error", Content: "ignored", Details: map[string]any{}, ErrorCode: "", ErrorMessage: "ignored"}
	requestID := "123e4567-e89b-42d3-a456-426614174355"
	manager.admitAndRunRootControl(context.Background(), binding, rootAdmissionEvent(fence, tool, requestID, rootAdmissionArguments("invalid-result")))
	row := waitForManagedControl(t, durable, requestID, "failed")
	require.Equal(t, "HANDLER_INVALID_RESULT", row.ErrorCode)
	_ = nextRootLogEvent(t, events)
	result := nextRootLogEvent(t, events)
	require.Equal(t, "result", result.Operation)
	require.Equal(t, "error", result.Status)
	require.Equal(t, "HANDLER_INVALID_RESULT", result.ErrorCode)
	require.Equal(t, "handler returned an invalid error code", result.ErrorText)
}

func TestRootControlDuplicateFinishEmitsOneTerminalEvent(t *testing.T) {
	manager, durable, binding, fence, tool, _, handler := newRootAdmissionFixtureWithReceiver(t, false)
	events := make(chan Event, 4)
	manager.eventSink = func(event Event) { events <- event }
	requestID := "123e4567-e89b-42d3-a456-426614174354"
	event := rootAdmissionEvent(fence, tool, requestID, rootAdmissionArguments("router"))
	manager.admitAndRunRootControl(context.Background(), binding, event)
	manager.admitAndRunRootControl(context.Background(), binding, event)
	requireManagedControlDelivered(t, durable, requestID)
	require.Equal(t, 1, handler.callCount())
	_ = nextRootLogEvent(t, events)
	_ = nextRootLogEvent(t, events)
	select {
	case duplicate := <-events:
		t.Fatalf("duplicate root-control event: %#v", duplicate)
	default:
	}
}

func TestRootControlResultEventUsesDeterministicUTF8Bounds(t *testing.T) {
	row := store.ManagedControlRequest{AgentID: "agent", OperationID: "ops.git_status", RequestID: "request", ToolName: "mecha_git", ToolCallID: "call", ResultStatus: "error", ResultContent: strings.Repeat("雪", 40_000), ErrorCode: "SAFE_FAILURE", ErrorDetail: strings.Repeat("é", 2_000)}
	event := managedRootControlResultEvent(row)
	require.True(t, event.Truncated)
	require.True(t, utf8.ValidString(event.Content))
	require.True(t, utf8.ValidString(event.ErrorText))
	require.LessOrEqual(t, len([]byte(event.Content)), managedEventBodyMaxBytes)
	require.LessOrEqual(t, len([]byte(event.ErrorText)), managedEventErrorMaxBytes)
	require.Equal(t, strings.Repeat("é", 994)+managedEventTruncationMark, event.ErrorText)
}
