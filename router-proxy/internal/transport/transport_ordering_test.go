package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestConcurrentConversationMessagesBroadcastInPersistedSequence(t *testing.T) {
	_, router := newTransportOrderingFixture(t)
	firstBroadcast := make(chan struct{})
	releaseFirst := make(chan struct{})
	delivered := make(chan int64, 2)
	subscriber := &session{router: router, sendOverride: func(fields map[string]any) error {
		if fields["type"] != "conversation_message" {
			return nil
		}
		message := fields["message"].(domain.ConversationMessage)
		if message.Sequence == 1 {
			close(firstBroadcast)
			<-releaseFirst
		}
		delivered <- message.Sequence
		return nil
	}}
	router.mu.Lock()
	router.convos["recipient"] = map[string]*session{"instance": subscriber}
	router.mu.Unlock()

	server := httptest.NewServer(router)
	defer server.Close()
	firstResult := make(chan error, 1)
	go func() { firstResult <- postOrderingMessage(server.URL, "first", "sender", "recipient") }()
	<-firstBroadcast

	secondResult := make(chan error, 1)
	go func() { secondResult <- postOrderingMessage(server.URL, "second", "sender", "recipient") }()
	select {
	case sequence := <-delivered:
		t.Fatalf("second message broadcast before first was released: sequence %d", sequence)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFirst)
	require.NoError(t, <-firstResult)
	require.NoError(t, <-secondResult)
	require.Equal(t, int64(1), <-delivered)
	require.Equal(t, int64(2), <-delivered)
}

func TestDistinctConversationMessagesDoNotBlockEachOther(t *testing.T) {
	_, router := newTransportOrderingFixture(t)
	firstBroadcast := make(chan struct{})
	releaseFirst := make(chan struct{})
	otherConversation := make(chan struct{})
	blocked := &session{router: router, sendOverride: func(fields map[string]any) error {
		if fields["type"] == "conversation_message" {
			close(firstBroadcast)
			<-releaseFirst
		}
		return nil
	}}
	other := &session{router: router, sendOverride: func(fields map[string]any) error {
		if fields["type"] == "conversation_message" {
			close(otherConversation)
		}
		return nil
	}}
	router.mu.Lock()
	router.convos["recipient"] = map[string]*session{"blocked": blocked}
	router.convos["other-recipient"] = map[string]*session{"other": other}
	router.mu.Unlock()

	server := httptest.NewServer(router)
	defer server.Close()
	firstResult := make(chan error, 1)
	go func() { firstResult <- postOrderingMessage(server.URL, "blocked", "sender", "recipient") }()
	<-firstBroadcast
	otherResult := make(chan error, 1)
	go func() { otherResult <- postOrderingMessage(server.URL, "other", "other-sender", "other-recipient") }()
	select {
	case <-otherConversation:
	case <-time.After(time.Second):
		t.Fatal("different conversation waited for blocked broadcast")
	}
	close(releaseFirst)
	require.NoError(t, <-firstResult)
	require.NoError(t, <-otherResult)
}

func TestControlResultsCorrelateToExactSessionsLeasesAndControlIDs(t *testing.T) {
	for _, kind := range []string{"model", "goal", "reminder"} {
		t.Run(kind, func(t *testing.T) {
			durable, router := newTransportOrderingFixture(t)
			sourceResults := make(chan map[string]any, 1)
			source := newTransportSession(t, router, durable, "source", "source-instance", "source-lease", "user_interaction", func(fields map[string]any) error {
				if fields["type"] == controlType(kind, true) {
					sourceResults <- fields
				}
				return nil
			})
			var target *session
			target = newTransportSession(t, router, durable, "target", "target-instance", "target-lease", "agent", func(fields map[string]any) error {
				if fields["type"] == controlType(kind, false) {
					result := controlResultFrame(kind, "source", "control-1", "target", "target-instance", "target-lease")
					if kind == "goal" {
						result["revision"] = rawJSON(int64(1))
					}
					if kind == "reminder" {
						result["reminder_id"] = rawJSON("reminder-1")
					}
					switch kind {
					case "model":
						return router.modelControlResult(target, result)
					case "goal":
						return router.goalControlResult(target, result)
					default:
						return router.reminderLockResult(target, result)
					}
				}
				return nil
			})
			if kind == "reminder" {
				created, _, err := durable.CreateReminder(context.Background(), store.ReminderRequest{ID: "reminder-1", Target: "target", Creator: "source", Body: "body", CreatedAt: "2026-08-29T12:00:00.000Z", NextDueAt: "2026-08-30T12:00:00.000Z"})
				require.NoError(t, err)
				require.True(t, created)
			}

			request := controlRequestFrame(kind, "source", "source-instance", "source-lease", "target", "target-instance", "control-1")
			var err error
			switch kind {
			case "model":
				err = router.modelControl(source, request)
			case "goal":
				err = router.goalControl(source, request)
			default:
				err = router.reminderLockControl(source, request)
			}
			require.NoError(t, err)
			result := <-sourceResults
			require.Equal(t, "target", result["agent_id"])
			require.Equal(t, "target-instance", result["instance_id"])
			require.Equal(t, "target-lease", result["lease_id"])
			require.Equal(t, "source", result["source_agent_id"])
			require.Equal(t, "control-1", result["control_id"])
			if kind == "reminder" {
				require.Equal(t, "reminder-1", result["reminder_id"])
			}
		})
	}
}

func TestStaleGoalResultCannotSatisfyNewerPendingControl(t *testing.T) {
	durable, router := newTransportOrderingFixture(t)
	targetErrors := make(chan map[string]any, 1)
	sourceResults := make(chan map[string]any, 1)
	source := newTransportSession(t, router, durable, "source", "source-instance", "source-lease", "user_interaction", func(fields map[string]any) error {
		if fields["type"] == controlType("goal", true) {
			sourceResults <- fields
		}
		return nil
	})
	target := newTransportSession(t, router, durable, "target", "target-instance", "target-lease", "agent", func(fields map[string]any) error {
		if fields["type"] == "error" {
			targetErrors <- fields
		}
		return nil
	})

	oldRequest := controlRequestFrame("goal", "source", "source-instance", "source-lease", "target", "target-instance", "old-control")
	newRequest := controlRequestFrame("goal", "source", "source-instance", "source-lease", "target", "target-instance", "new-control")
	require.NoError(t, router.goalControl(source, oldRequest))
	require.NoError(t, router.goalControl(source, newRequest))

	current := controlResultFrame("goal", "source", "new-control", "target", "target-instance", "target-lease")
	current["revision"] = rawJSON(int64(2))
	current["goal_state"] = rawJSON(map[string]any{"enabled": true, "goal": "current"})
	require.NoError(t, router.goalControlResult(target, current))
	result := <-sourceResults
	require.Equal(t, "new-control", result["control_id"])

	stale := controlResultFrame("goal", "source", "old-control", "target", "target-instance", "target-lease")
	stale["revision"] = rawJSON(int64(1))
	stale["goal_state"] = rawJSON(map[string]any{"enabled": true, "goal": "stale"})
	require.NoError(t, router.goalControlResult(target, stale))
	errorFrame := <-targetErrors
	require.Equal(t, "goal_control_result_not_pending", errorFrame["code"])
	require.Empty(t, sourceResults)
	router.mu.RLock()
	goal := router.goals["target"]
	router.mu.RUnlock()
	require.Equal(t, "current", goal.state["goal"])
}

func newTransportOrderingFixture(t *testing.T) (*store.Store, *Router) {
	t.Helper()
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{Clock: clock})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, durable.Close()) })
	router, err := New(Options{Store: durable, Clock: clock})
	require.NoError(t, err)
	return durable, router
}

func newTransportSession(t *testing.T, router *Router, durable *store.Store, agent, instance, lease, role string, send func(map[string]any) error) *session {
	t.Helper()
	_, err := durable.AcquireAgentLease(context.Background(), store.LeaseRequest{AgentID: agent, InstanceID: instance, LeaseID: lease, LeaseDuration: time.Minute})
	require.NoError(t, err)
	s := &session{router: router, sendOverride: send}
	router.configureSession(s, registrationFrameForOrdering(agent, instance, role), lease, instance)
	router.mu.Lock()
	router.sessions[agent] = s
	router.mu.Unlock()
	return s
}

func registrationFrameForOrdering(agent, instance, role string) map[string]json.RawMessage {
	return map[string]json.RawMessage{"agent_id": rawJSON(agent), "instance_id": rawJSON(instance), "agent_role": rawJSON(role)}
}

func postOrderingMessage(base, id, from, to string) error {
	payload := []byte(`{"id":"` + id + `","from":"` + from + `","to":"` + to + `","body":"body-` + id + `","created_at":"2026-08-29T12:00:00.000Z"}`)
	request, err := http.NewRequest(http.MethodPost, base+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return &orderingHTTPError{status: response.StatusCode}
	}
	return nil
}

type orderingHTTPError struct{ status int }

func (e *orderingHTTPError) Error() string { return "unexpected HTTP status" }

func controlRequestFrame(kind, sourceAgent, sourceInstance, sourceLease, targetAgent, targetInstance, controlID string) map[string]json.RawMessage {
	frame := map[string]json.RawMessage{
		"type": rawJSON(controlType(kind, false)), "frame_id": rawJSON("request-" + controlID),
		"agent_id": rawJSON(sourceAgent), "instance_id": rawJSON(sourceInstance), "lease_id": rawJSON(sourceLease),
		"target_agent_id": rawJSON(targetAgent), "target_instance_id": rawJSON(targetInstance), "control_id": rawJSON(controlID),
		"action": rawJSON("set"),
	}
	if kind == "model" {
		frame["model"] = rawJSON(map[string]string{"provider": "test", "id": "model"})
	}
	if kind == "goal" {
		frame["goal"] = rawJSON("goal")
		frame["settings"] = rawJSON(map[string]any{})
	}
	if kind == "reminder" {
		frame["reminder_id"] = rawJSON("reminder-1")
	}
	return frame
}

func controlResultFrame(kind, sourceAgent, controlID, targetAgent, targetInstance, targetLease string) map[string]json.RawMessage {
	return map[string]json.RawMessage{
		"type": rawJSON(controlType(kind, true)), "frame_id": rawJSON("result-" + controlID),
		"agent_id": rawJSON(targetAgent), "instance_id": rawJSON(targetInstance), "lease_id": rawJSON(targetLease),
		"source_agent_id": rawJSON(sourceAgent), "control_id": rawJSON(controlID), "accepted": rawJSON(true),
	}
}

func rawJSON(value any) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}

func controlType(kind string, result bool) string {
	if kind == "reminder" {
		if result {
			return "reminder_lock_control_result"
		}
		return "reminder_lock_control"
	}
	if result {
		return kind + "_control_result"
	}
	return kind + "_control"
}
