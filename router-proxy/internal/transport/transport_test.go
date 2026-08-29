package transport_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
	"github.com/stretchr/testify/require"
)

func TestHTTPOriginAndPreflightMatchCurrentNodeContract(t *testing.T) {
	database := filepath.Join(t.TempDir(), "router.db")
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	durable, err := store.Open(context.Background(), database, store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	router, err := transport.New(transport.Options{Store: durable, Port: 0, Clock: clock})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	defer server.Close()

	request, err := http.NewRequest(http.MethodGet, server.URL+"/v1/agents", nil)
	require.NoError(t, err)
	request.Header.Set("Origin", "https://untrusted.example")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, "*", response.Header.Get("Access-Control-Allow-Origin"))
	require.Equal(t, "GET, POST, PATCH, DELETE, OPTIONS", response.Header.Get("Access-Control-Allow-Methods"))
	require.Equal(t, "content-type", response.Header.Get("Access-Control-Allow-Headers"))

	request, err = http.NewRequest(http.MethodOptions, server.URL+"/v1/agents", nil)
	require.NoError(t, err)
	request.Header.Set("Origin", "https://untrusted.example")
	request.Header.Set("Access-Control-Request-Method", "GET")
	response, err = http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusNoContent, response.StatusCode)
	require.Equal(t, "*", response.Header.Get("Access-Control-Allow-Origin"))
	require.Equal(t, "no-store", response.Header.Get("Cache-Control"))

	request, err = http.NewRequest(http.MethodOptions, server.URL+"/v1/agents", nil)
	require.NoError(t, err)
	response, err = http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusNotFound, response.StatusCode)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"not_found"`)
}

func TestWebSocketRegistrationDeliveryAndAckUseStoreTransitions(t *testing.T) {
	database := filepath.Join(t.TempDir(), "router.db")
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	ids := deterministic.NewSequenceIDSource("lease-id", "registered-frame", "123e4567-e89b-42d3-a456-426614174000", "batch-id", "message-frame", "acknowledged-frame")
	durable, err := store.Open(context.Background(), database, store.Options{Clock: clock, IDSource: ids})
	require.NoError(t, err)
	defer durable.Close()
	router, err := transport.New(transport.Options{Store: durable, Port: 0, Clock: clock, IDSource: ids})
	require.NoError(t, err)
	require.NoError(t, router.Listen(context.Background()))
	defer router.Close(context.Background())
	_, wsURL := router.URLs()
	conn, _, err := websocket.Dial(context.Background(), wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "test done")

	register := map[string]any{"version": "v1", "frame_id": "register-frame", "type": "register", "agent_id": "receiver", "instance_id": "receiver-instance", "ack_confirmation": "correlated_v1"}
	sendJSON(t, conn, register)
	registered := readJSON(t, conn)
	require.Equal(t, "registered", registered["type"])
	require.Equal(t, "receiver", registered["agent_id"])

	client := httptest.NewServer(router)
	defer client.Close()
	message := map[string]any{"id": "message-1", "from": "sender", "to": "receiver", "body": "hello", "created_at": "2026-08-29T12:00:00.000Z"}
	payload, err := json.Marshal(message)
	require.NoError(t, err)
	request, err := http.NewRequest(http.MethodPost, client.URL+"/v1/messages", bytesReader(payload))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Correlation-Id", "http-message-request")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, response.StatusCode)
	response.Body.Close()

	offered := readJSON(t, conn)
	require.Equal(t, "message", offered["type"])
	require.Equal(t, "message-1", offered["message"].(map[string]any)["id"])
	incarnation := offered["message_incarnation_id"].(string)
	injection := map[string]any{"version": "v1", "frame_id": "injection-frame", "type": "injection_requested", "agent_id": "receiver", "instance_id": "receiver-instance", "lease_id": registered["lease_id"], "message_id": "message-1", "attempt_number": 1}
	sendJSON(t, conn, injection)
	ack := map[string]any{"version": "v1", "frame_id": "ack-frame", "type": "ack", "agent_id": "receiver", "instance_id": "receiver-instance", "lease_id": registered["lease_id"], "message_id": "message-1", "attempt_number": 1}
	sendJSON(t, conn, ack)
	acknowledged := readJSON(t, conn)
	require.Equal(t, "delivery_acknowledged", acknowledged["type"])
	require.Equal(t, incarnation, acknowledged["message_incarnation_id"])
	stored, err := durable.MessageFor(context.Background(), "message-1")
	require.NoError(t, err)
	require.Nil(t, stored)
}

func sendJSON(t *testing.T, conn *websocket.Conn, value map[string]any) {
	t.Helper()
	payload, err := json.Marshal(value)
	require.NoError(t, err)
	require.NoError(t, conn.Write(context.Background(), websocket.MessageText, payload))
}
func readJSON(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	return readJSONWithTimeout(t, conn, 10*time.Second)
}

func tryReadJSON(conn *websocket.Conn, timeout time.Duration) (map[string]any, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, payload, err := conn.Read(ctx)
	if err != nil {
		return nil, false
	}
	var value map[string]any
	if json.Unmarshal(payload, &value) != nil {
		return nil, false
	}
	return value, true
}

func readJSONWithTimeout(t *testing.T, conn *websocket.Conn, timeout time.Duration) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, payload, err := conn.Read(ctx)
	require.NoError(t, err)
	var value map[string]any
	require.NoError(t, json.Unmarshal(payload, &value))
	return value
}
func bytesReader(value []byte) *byteReader { return &byteReader{value: value} }

func registerTestSession(t *testing.T, wsURL, agentID, instanceID, conversationSync string) (*websocket.Conn, map[string]any) {
	t.Helper()
	conn, _, err := websocket.Dial(context.Background(), wsURL, nil)
	require.NoError(t, err)
	frame := map[string]any{"version": "v1", "frame_id": "register-" + agentID, "type": "register", "agent_id": agentID, "instance_id": instanceID}
	if conversationSync != "" {
		frame["conversation_sync"] = conversationSync
	}
	sendJSON(t, conn, frame)
	return conn, readJSON(t, conn)
}

func TestLiveProjectionsOnlyReachNodeProjectedRecipients(t *testing.T) {
	database := filepath.Join(t.TempDir(), "router.db")
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	durable, err := store.Open(context.Background(), database, store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	router, err := transport.New(transport.Options{Store: durable, Port: 0, Clock: clock})
	require.NoError(t, err)
	require.NoError(t, router.Listen(context.Background()))
	defer router.Close(context.Background())
	_, wsURL := router.URLs()

	a, aRegistered := registerTestSession(t, wsURL, "a", "a-instance", "v1")
	defer a.Close(websocket.StatusNormalClosure, "test done")
	b, _ := registerTestSession(t, wsURL, "b", "b-instance", "v1")
	defer b.Close(websocket.StatusNormalClosure, "test done")
	c, _ := registerTestSession(t, wsURL, "c", "c-instance", "v1")
	defer c.Close(websocket.StatusNormalClosure, "test done")
	d, _ := registerTestSession(t, wsURL, "d", "d-instance", "")
	defer d.Close(websocket.StatusNormalClosure, "test done")

	payload := []byte(`{"id":"projection-message","from":"a","to":"b","body":"hello","created_at":"2026-08-29T12:00:00.000Z"}`)
	request, err := http.NewRequest(http.MethodPost, strings.Replace(wsURL, "ws://", "http://", 1), bytesReader(payload))
	require.NoError(t, err)
	request.URL.Path = "/v1/messages"
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, response.StatusCode)
	response.Body.Close()
	require.Equal(t, "conversation_message", readJSON(t, a)["type"])
	require.Equal(t, "conversation_message", readJSON(t, b)["type"])
	// b also receives the durable delivery; unrelated conversation c and the
	// non-subscribing d receive no conversation message.
	require.Equal(t, "message", readJSON(t, b)["type"])

	sendJSON(t, a, map[string]any{"version": "v1", "frame_id": "activity-frame", "type": "activity", "agent_id": "a", "instance_id": "a-instance", "lease_id": aRegistered["lease_id"], "activity_state": "active"})
	require.Equal(t, "agent_activity", readJSON(t, a)["type"])
	require.Equal(t, "agent_activity", readJSON(t, b)["type"])
	require.Equal(t, "agent_activity", readJSON(t, c)["type"])

	sendJSON(t, a, map[string]any{"version": "v1", "frame_id": "model-frame", "type": "model_catalog", "agent_id": "a", "instance_id": "a-instance", "lease_id": aRegistered["lease_id"], "model_summaries": []any{map[string]any{"provider": "test", "id": "model"}}})
	require.Equal(t, "agent_model_catalog", readJSON(t, a)["type"])
	require.Equal(t, "agent_model_catalog", readJSON(t, b)["type"])
	require.Equal(t, "agent_model_catalog", readJSON(t, c)["type"])

	sendJSON(t, a, map[string]any{"version": "v1", "frame_id": "arrival-frame", "type": "message_arrival", "agent_id": "a", "instance_id": "a-instance", "lease_id": aRegistered["lease_id"], "from": "sender", "source": map[string]any{"kind": "agent"}, "preview": "preview", "body": "body", "created_at": "2026-08-29T12:00:00.000Z", "message_id": "arrival-message"})
	require.Equal(t, "message_arrival", readJSON(t, a)["type"])
	require.Equal(t, "message_arrival", readJSON(t, b)["type"])
	require.Equal(t, "message_arrival", readJSON(t, c)["type"])

	goalState := map[string]any{"enabled": true, "goal": "do work", "goal_lock_enabled": true, "snooze_enabled": true, "snooze_duration_ms": 1, "snooze_turn_limit": nil, "snooze_budget_cap_ms": nil, "snooze_refill_yield_ms": 1, "snooze_refill_elapsed_ms": 1, "outcome_counts": map[string]any{"COMPLETED": 0, "NOT_DONE": 0}}
	sendJSON(t, a, map[string]any{"version": "v1", "frame_id": "goal-frame", "type": "goal_state", "agent_id": "a", "instance_id": "a-instance", "lease_id": aRegistered["lease_id"], "goal_state": goalState})
	require.Equal(t, "agent_goal_state", readJSON(t, a)["type"])
	require.Equal(t, "agent_goal_state", readJSON(t, b)["type"])
	require.Equal(t, "agent_goal_state", readJSON(t, c)["type"])

	readCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, _, err = d.Read(readCtx)
	cancel()
	require.Error(t, err)
}

func TestRegisterAfterRegistrationReturnsAlreadyRegisteredAndKeepsLease(t *testing.T) {
	database := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(context.Background(), database, store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	router, err := transport.New(transport.Options{Store: durable, Port: 0})
	require.NoError(t, err)
	require.NoError(t, router.Listen(context.Background()))
	defer router.Close(context.Background())
	_, wsURL := router.URLs()
	conn, registered := registerTestSession(t, wsURL, "registered", "instance", "")
	defer conn.Close(websocket.StatusNormalClosure, "test done")
	sendJSON(t, conn, map[string]any{"version": "v1", "frame_id": "second-register", "type": "register", "agent_id": "registered", "instance_id": "instance"})
	rejected := readJSON(t, conn)
	require.Equal(t, "error", rejected["type"])
	require.Equal(t, "already_registered", rejected["code"])
	require.Equal(t, "second-register", rejected["request_id"])
	sendJSON(t, conn, map[string]any{"version": "v1", "frame_id": "ping", "type": "ping"})
	require.Equal(t, "pong", readJSON(t, conn)["type"])
	lease, err := durable.LeaseFor(context.Background(), "registered")
	require.NoError(t, err)
	require.Equal(t, registered["lease_id"], lease.LeaseID)
}

func TestConversationSyncInstancesShareOneLeaseAndOneDeliveryOwner(t *testing.T) {
	database := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(context.Background(), database, store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	router, err := transport.New(transport.Options{Store: durable, Port: 0})
	require.NoError(t, err)
	require.NoError(t, router.Listen(context.Background()))
	defer router.Close(context.Background())
	_, wsURL := router.URLs()

	connections := make([]*websocket.Conn, 2)
	registrations := make([]map[string]any, 2)
	for index := range connections {
		connections[index], _, err = websocket.Dial(context.Background(), wsURL, nil)
		require.NoError(t, err)
		defer connections[index].Close(websocket.StatusNormalClosure, "test done")
	}
	var group sync.WaitGroup
	for index := range connections {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			sendJSON(t, connections[index], map[string]any{"version": "v1", "frame_id": "register-" + string(rune('a'+index)), "type": "register", "agent_id": "shared", "instance_id": "instance-" + string(rune('a'+index)), "conversation_sync": "v1"})
		}(index)
	}
	group.Wait()
	for index := range connections {
		registrations[index] = readJSON(t, connections[index])
		require.Equal(t, "registered", registrations[index]["type"])
	}
	require.Equal(t, registrations[0]["lease_id"], registrations[1]["lease_id"])
	lease, err := durable.LeaseFor(context.Background(), "shared")
	require.NoError(t, err)
	require.Equal(t, registrations[0]["lease_id"], lease.LeaseID)
}

func TestModelControlFastResultIsCorrelatedBeforeForward(t *testing.T) {
	database := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(context.Background(), database, store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	router, err := transport.New(transport.Options{Store: durable, Port: 0})
	require.NoError(t, err)
	require.NoError(t, router.Listen(context.Background()))
	defer router.Close(context.Background())
	_, wsURL := router.URLs()
	source, _, err := websocket.Dial(context.Background(), wsURL, nil)
	require.NoError(t, err)
	defer source.Close(websocket.StatusNormalClosure, "test done")
	sendJSON(t, source, map[string]any{"version": "v1", "frame_id": "operator-register", "type": "register", "agent_id": "operator", "instance_id": "operator-instance", "agent_role": "user_interaction"})
	sourceRegistration := readJSON(t, source)
	target, targetRegistration := registerTestSession(t, wsURL, "target", "target-instance", "")
	defer target.Close(websocket.StatusNormalClosure, "test done")
	sendJSON(t, source, map[string]any{"version": "v1", "frame_id": "control-frame", "type": "model_control", "agent_id": "operator", "instance_id": "operator-instance", "lease_id": sourceRegistration["lease_id"], "target_agent_id": "target", "target_instance_id": "target-instance", "control_id": "control-1", "action": "set_model", "model": map[string]any{"provider": "test", "id": "next"}})
	forwarded := readJSON(t, target)
	require.Equal(t, "model_control", forwarded["type"])
	sendJSON(t, target, map[string]any{"version": "v1", "frame_id": "result-frame", "type": "model_control_result", "agent_id": "target", "instance_id": "target-instance", "lease_id": targetRegistration["lease_id"], "source_agent_id": "operator", "control_id": "control-1", "accepted": false, "error_code": "rejected", "current_model": map[string]any{"provider": "test", "id": "current"}})
	result := readJSON(t, source)
	require.Equal(t, "model_control_result", result["type"])
	require.Equal(t, "control-1", result["control_id"])
}

func TestInvalidSchemaFrameReturnsInvalidFrameWithoutDisconnecting(t *testing.T) {
	database := filepath.Join(t.TempDir(), "router.db")
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	durable, err := store.Open(context.Background(), database, store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	router, err := transport.New(transport.Options{Store: durable, Port: 0, Clock: clock})
	require.NoError(t, err)
	require.NoError(t, router.Listen(context.Background()))
	defer router.Close(context.Background())
	_, wsURL := router.URLs()
	conn, registered := registerTestSession(t, wsURL, "validator", "validator-instance", "v1")
	defer conn.Close(websocket.StatusNormalClosure, "test done")
	sendJSON(t, conn, map[string]any{"version": "v1", "frame_id": "bad-frame", "type": "activity", "agent_id": "validator", "instance_id": "validator-instance", "lease_id": registered["lease_id"], "activity_state": "active", "subagent_count": 1000})
	invalid := readJSON(t, conn)
	require.Equal(t, "error", invalid["type"])
	require.Equal(t, "invalid_frame", invalid["code"])
	require.NotContains(t, invalid, "request_id")
	sendJSON(t, conn, map[string]any{"version": "v1", "frame_id": "ping-frame", "type": "ping"})
	require.Equal(t, "pong", readJSON(t, conn)["type"])
}

func TestWebSocketStripsAdditiveNestedFieldsAndDispatchesAcceptedActivity(t *testing.T) {
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	router, err := transport.New(transport.Options{Store: durable, Port: 0})
	require.NoError(t, err)
	require.NoError(t, router.Listen(context.Background()))
	defer router.Close(context.Background())
	_, wsURL := router.URLs()
	sender, registered := registerTestSession(t, wsURL, "activity-sender", "sender-instance", "v1")
	defer sender.Close(websocket.StatusNormalClosure, "test done")
	observer, _ := registerTestSession(t, wsURL, "activity-observer", "observer-instance", "v1")
	defer observer.Close(websocket.StatusNormalClosure, "test done")

	sendJSON(t, sender, map[string]any{
		"version": "v1", "frame_id": "activity-frame", "type": "activity",
		"agent_id": "activity-sender", "instance_id": "sender-instance", "lease_id": registered["lease_id"],
		"activity_state": "active", "future": map[string]any{"drop": true},
		"subagents":     []any{map[string]any{"id": "child", "description": "working", "status": "running", "future": true}},
		"goal":          map[string]any{"enabled": true, "goal": "ship", "future": true},
		"current_model": map[string]any{"provider": "test", "id": "model", "future": true},
	})
	activity := readJSON(t, observer)
	require.Equal(t, "agent_activity", activity["type"])
	require.Equal(t, "ship", activity["goal"].(map[string]any)["goal"])
	require.NotContains(t, activity, "future")
	require.NotContains(t, activity["goal"], "future")
	require.NotContains(t, activity["current_model"], "future")
	require.NotContains(t, activity["subagents"].([]any)[0], "future")
	// Drain the sender's own projected activity before checking that the socket remains usable.
	require.Equal(t, "agent_activity", readJSON(t, sender)["type"])
	sendJSON(t, sender, map[string]any{"version": "v1", "frame_id": "ping-frame", "type": "ping"})
	require.Equal(t, "pong", readJSON(t, sender)["type"])
}

func TestSameInstanceReconnectDoesNotLetOldCleanupRemoveReplacement(t *testing.T) {
	database := filepath.Join(t.TempDir(), "router.db")
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	durable, err := store.Open(context.Background(), database, store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	router, err := transport.New(transport.Options{Store: durable, Port: 0, Clock: clock})
	require.NoError(t, err)
	require.NoError(t, router.Listen(context.Background()))
	defer router.Close(context.Background())
	_, wsURL := router.URLs()
	old, _ := registerTestSession(t, wsURL, "reconnect", "same-instance", "v1")
	newConn, _ := registerTestSession(t, wsURL, "reconnect", "same-instance", "v1")
	defer newConn.Close(websocket.StatusNormalClosure, "test done")
	// The replacement closes the old connection; its deferred cleanup must not
	// remove the new conversation subscription or active session.
	_ = readJSONWithTimeout(t, old, time.Second)
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	_, _, _ = old.Read(closeCtx)
	cancel()
	sendJSON(t, newConn, map[string]any{"version": "v1", "frame_id": "ping-frame", "type": "ping"})
	require.Equal(t, "pong", readJSON(t, newConn)["type"])
}

func TestConversationSyncActivityPausesPrimaryAndPromotedSession(t *testing.T) {
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	router, err := transport.New(transport.Options{Store: durable, Port: 0})
	require.NoError(t, err)
	require.NoError(t, router.Listen(context.Background()))
	defer router.Close(context.Background())
	_, wsURL := router.URLs()
	primary, _ := registerTestSession(t, wsURL, "shared", "primary", "v1")
	secondary, secondaryRegistration := registerTestSession(t, wsURL, "shared", "secondary", "v1")
	defer secondary.Close(websocket.StatusNormalClosure, "test done")

	sendJSON(t, secondary, map[string]any{"version": "v1", "frame_id": "compact", "type": "activity", "agent_id": "shared", "instance_id": "secondary", "lease_id": secondaryRegistration["lease_id"], "activity_state": "compacting", "subagent_count": 3, "subagents": []map[string]any{{"id": "stale"}}})
	compacting := readJSON(t, primary)
	require.Equal(t, "agent_activity", compacting["type"])
	require.Equal(t, 0, int(compacting["subagent_count"].(float64)))
	require.Empty(t, compacting["subagents"])
	require.Equal(t, "agent_activity", readJSON(t, secondary)["type"])

	client := httptest.NewServer(router)
	defer client.Close()
	payload := []byte(`{"id":"queued","from":"sender","to":"shared","body":"hello","created_at":"2026-08-29T12:00:00.000Z"}`)
	request, err := http.NewRequest(http.MethodPost, client.URL+"/v1/messages", bytesReader(payload))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	response.Body.Close()
	readCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, payload, readErr := primary.Read(readCtx)
	cancel()
	require.NoError(t, readErr)
	var event map[string]any
	require.NoError(t, json.Unmarshal(payload, &event))
	require.NotEqual(t, "message", event["type"])
	require.Equal(t, "conversation_message", readJSON(t, secondary)["type"])

	_ = primary.Close(websocket.StatusNormalClosure, "promote")
	readCtx, cancel = context.WithTimeout(context.Background(), time.Second)
	_, _, _ = primary.Read(readCtx)
	cancel()
	require.Eventually(t, func() bool {
		response, requestErr := http.Get(client.URL + "/v1/agents")
		if requestErr != nil {
			return false
		}
		defer response.Body.Close()
		var snapshot map[string]any
		if json.NewDecoder(response.Body).Decode(&snapshot) != nil {
			return false
		}
		agents, ok := snapshot["agents"].([]any)
		if !ok || len(agents) != 1 {
			return false
		}
		item, ok := agents[0].(map[string]any)
		if !ok {
			return false
		}
		if item["instance_id"] != "secondary" || item["activity_state"] != "compacting" {
			return false
		}
		return true
	}, time.Second, time.Millisecond)
	// Promotion keeps the shared compacting pause. Settling on the promoted
	// socket releases the queue and makes that socket the delivery owner.
	sendJSON(t, secondary, map[string]any{"version": "v1", "frame_id": "settled", "type": "activity", "agent_id": "shared", "instance_id": "secondary", "lease_id": secondaryRegistration["lease_id"], "activity_state": "settled"})
	activity := readJSON(t, secondary)
	require.Equal(t, "agent_activity", activity["type"])
	require.Equal(t, map[string]any{"enabled": false, "goal": ""}, activity["goal"])
	require.Empty(t, activity["subagents"])
	message := readJSON(t, secondary)
	require.Equal(t, "message", message["type"])
	require.Equal(t, "queued", message["message"].(map[string]any)["id"])
}

func TestConversationSyncReconnectRetainsCompactingPause(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	router, err := transport.New(transport.Options{Store: durable, Port: 0, Clock: clock})
	require.NoError(t, err)
	require.NoError(t, router.Listen(context.Background()))
	defer router.Close(context.Background())
	_, wsURL := router.URLs()
	old, registration := registerTestSession(t, wsURL, "reconnect-pause", "instance", "v1")
	defer old.Close(websocket.StatusNormalClosure, "test done")
	sendJSON(t, old, map[string]any{"version": "v1", "frame_id": "compact", "type": "activity", "agent_id": "reconnect-pause", "instance_id": "instance", "lease_id": registration["lease_id"], "activity_state": "compacting"})
	require.Equal(t, "agent_activity", readJSON(t, old)["type"])
	newConn, newRegistration := registerTestSession(t, wsURL, "reconnect-pause", "instance", "v1")
	defer newConn.Close(websocket.StatusNormalClosure, "test done")
	require.Equal(t, registration["lease_id"], newRegistration["lease_id"])
	client := httptest.NewServer(router)
	defer client.Close()
	payload := []byte(`{"id":"reconnect-queued","from":"sender","to":"reconnect-pause","body":"hello","created_at":"2026-08-29T12:00:00.000Z"}`)
	request, err := http.NewRequest(http.MethodPost, client.URL+"/v1/messages", bytesReader(payload))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	response.Body.Close()
	readCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, payload, readErr := newConn.Read(readCtx)
	cancel()
	require.NoError(t, readErr)
	var event map[string]any
	require.NoError(t, json.Unmarshal(payload, &event))
	require.NotEqual(t, "message", event["type"])
}

func TestDifferentInstanceReplacementClearsStaleCompactingPause(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	router, err := transport.New(transport.Options{Store: durable, Port: 0, Clock: clock})
	require.NoError(t, err)
	require.NoError(t, router.Listen(context.Background()))
	defer router.Close(context.Background())
	_, wsURL := router.URLs()

	old, registration := registerTestSession(t, wsURL, "replacement-pause", "old-instance", "v1")
	sendJSON(t, old, map[string]any{"version": "v1", "frame_id": "compact", "type": "activity", "agent_id": "replacement-pause", "instance_id": "old-instance", "lease_id": registration["lease_id"], "activity_state": "compacting"})
	require.Equal(t, "agent_activity", readJSON(t, old)["type"])
	require.NoError(t, old.Close(websocket.StatusNormalClosure, "disconnect"))
	readCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	_, _, _ = old.Read(readCtx)
	cancel()

	newConn, _ := registerTestSession(t, wsURL, "replacement-pause", "new-instance", "v1")
	defer newConn.Close(websocket.StatusNormalClosure, "test done")
	client := httptest.NewServer(router)
	defer client.Close()
	payload := []byte(`{"id":"replacement-delivery","from":"sender","to":"replacement-pause","body":"hello","created_at":"2026-08-29T12:00:00.000Z"}`)
	request, err := http.NewRequest(http.MethodPost, client.URL+"/v1/messages", bytesReader(payload))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, response.StatusCode)
	response.Body.Close()
	require.Equal(t, "conversation_message", readJSON(t, newConn)["type"])
	message := readJSON(t, newConn)
	require.Equal(t, "message", message["type"])
	require.Equal(t, "replacement-delivery", message["message"].(map[string]any)["id"])
}

func TestUnacknowledgedDeliveryRetriesAfterReconnect(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	router, err := transport.New(transport.Options{Store: durable, Port: 0, Clock: clock, DeliveryLeaseDuration: time.Second, SweepInterval: 5 * time.Millisecond})
	require.NoError(t, err)
	require.NoError(t, router.Listen(context.Background()))
	defer router.Close(context.Background())
	_, wsURL := router.URLs()

	old, _ := registerTestSession(t, wsURL, "retry-reconnect", "instance", "")
	client := httptest.NewServer(router)
	defer client.Close()
	payload := []byte(`{"id":"retry-message","from":"sender","to":"retry-reconnect","body":"hello","created_at":"2026-08-29T12:00:00.000Z"}`)
	request, err := http.NewRequest(http.MethodPost, client.URL+"/v1/messages", bytesReader(payload))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, response.StatusCode)
	response.Body.Close()
	first := readJSON(t, old)
	require.Equal(t, "message", first["type"])
	require.Equal(t, float64(1), first["attempt_number"])
	require.NoError(t, old.Close(websocket.StatusNormalClosure, "disconnect"))

	require.NoError(t, clock.Advance(2*time.Second))
	require.Eventually(t, func() bool {
		message, lookupErr := durable.MessageFor(context.Background(), "retry-message")
		return lookupErr == nil && message != nil && message.State == "queued"
	}, time.Second, time.Millisecond)

	reconnected, registration := registerTestSession(t, wsURL, "retry-reconnect", "instance", "")
	defer reconnected.Close(websocket.StatusNormalClosure, "test done")
	second := readJSON(t, reconnected)
	require.Equal(t, "message", second["type"])
	require.Equal(t, "retry-message", second["message"].(map[string]any)["id"])
	require.Equal(t, float64(2), second["attempt_number"])
	sendJSON(t, reconnected, map[string]any{"version": "v1", "frame_id": "injection", "type": "injection_requested", "agent_id": "retry-reconnect", "instance_id": "instance", "lease_id": registration["lease_id"], "message_id": "retry-message", "attempt_number": 2})
	sendJSON(t, reconnected, map[string]any{"version": "v1", "frame_id": "ack", "type": "ack", "agent_id": "retry-reconnect", "instance_id": "instance", "lease_id": registration["lease_id"], "message_id": "retry-message", "attempt_number": 2})
	sendJSON(t, reconnected, map[string]any{"version": "v1", "frame_id": "after-ack", "type": "ping"})
	require.Equal(t, "pong", readJSON(t, reconnected)["type"])
}

func TestConversationSyncReconnectsPausedQueueAndDeliversExactlyOnce(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	router, err := transport.New(transport.Options{Store: durable, Port: 0, Clock: clock})
	require.NoError(t, err)
	require.NoError(t, router.Listen(context.Background()))
	defer router.Close(context.Background())
	_, wsURL := router.URLs()

	old, registration := registerTestSession(t, wsURL, "reconnect-queue", "same-instance", "v1")
	defer old.Close(websocket.StatusNormalClosure, "test done")
	sendJSON(t, old, map[string]any{"version": "v1", "frame_id": "compact", "type": "activity", "agent_id": "reconnect-queue", "instance_id": "same-instance", "lease_id": registration["lease_id"], "activity_state": "compacting"})
	require.Equal(t, "agent_activity", readJSON(t, old)["type"])
	old.Close(websocket.StatusNormalClosure, "disconnect")
	readCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	_, _, _ = old.Read(readCtx)
	cancel()

	client := httptest.NewServer(router)
	defer client.Close()
	payload := []byte(`{"id":"queued-while-disconnected","from":"sender","to":"reconnect-queue","body":"queued","created_at":"2026-08-29T12:00:00.000Z"}`)
	request, err := http.NewRequest(http.MethodPost, client.URL+"/v1/messages", bytesReader(payload))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, response.StatusCode)
	response.Body.Close()
	queued, err := durable.MessageFor(context.Background(), "queued-while-disconnected")
	require.NoError(t, err)
	require.NotNil(t, queued)
	require.Equal(t, "queued", queued.State)

	reconnected, reconnectedRegistration := registerTestSession(t, wsURL, "reconnect-queue", "same-instance", "v1")
	defer reconnected.Close(websocket.StatusNormalClosure, "test done")
	sendJSON(t, reconnected, map[string]any{"version": "v1", "frame_id": "settled", "type": "activity", "agent_id": "reconnect-queue", "instance_id": "same-instance", "lease_id": reconnectedRegistration["lease_id"], "activity_state": "settled"})
	first := readJSON(t, reconnected)
	second := readJSON(t, reconnected)
	require.Equal(t, "agent_activity", first["type"])
	require.Equal(t, "message", second["type"])
	require.Equal(t, "queued-while-disconnected", second["message"].(map[string]any)["id"])

	sendJSON(t, reconnected, map[string]any{"version": "v1", "frame_id": "injection", "type": "injection_requested", "agent_id": "reconnect-queue", "instance_id": "same-instance", "lease_id": reconnectedRegistration["lease_id"], "message_id": "queued-while-disconnected", "attempt_number": 1})
	sendJSON(t, reconnected, map[string]any{"version": "v1", "frame_id": "ack", "type": "ack", "agent_id": "reconnect-queue", "instance_id": "same-instance", "lease_id": reconnectedRegistration["lease_id"], "message_id": "queued-while-disconnected", "attempt_number": 1})
	sendJSON(t, reconnected, map[string]any{"version": "v1", "frame_id": "after-ack", "type": "ping"})
	require.Equal(t, "pong", readJSON(t, reconnected)["type"], "acknowledged message must not be delivered again")

	payload = []byte(`{"id":"after-reconnect","from":"reconnect-queue","to":"sender","body":"after","created_at":"2026-08-29T12:00:01.000Z"}`)
	request, err = http.NewRequest(http.MethodPost, client.URL+"/v1/messages", bytesReader(payload))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, response.StatusCode)
	response.Body.Close()
	require.Equal(t, "conversation_message", readJSON(t, reconnected)["type"])

	historyResponse, err := http.Get(client.URL + "/v1/conversations?owner=reconnect-queue&peer=sender")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, historyResponse.StatusCode)
	var history struct {
		Messages []struct {
			ID       string `json:"id"`
			Sequence int64  `json:"sequence"`
		} `json:"messages"`
	}
	require.NoError(t, json.NewDecoder(historyResponse.Body).Decode(&history))
	historyResponse.Body.Close()
	require.Len(t, history.Messages, 2)
	require.Equal(t, "queued-while-disconnected", history.Messages[0].ID)
	require.Equal(t, "after-reconnect", history.Messages[1].ID)
	require.Equal(t, int64(1), history.Messages[0].Sequence)
	require.Equal(t, int64(2), history.Messages[1].Sequence)
}

func TestConversationSyncFanoutReachesReplacementSubscriber(t *testing.T) {
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	router, err := transport.New(transport.Options{Store: durable, Port: 0})
	require.NoError(t, err)
	require.NoError(t, router.Listen(context.Background()))
	defer router.Close(context.Background())
	_, wsURL := router.URLs()

	owner, _ := registerTestSession(t, wsURL, "fanout", "owner", "v1")
	defer owner.Close(websocket.StatusNormalClosure, "test done")
	subscriber, _ := registerTestSession(t, wsURL, "fanout", "subscriber", "v1")
	defer subscriber.CloseNow()
	client := httptest.NewServer(router)
	defer client.Close()

	payload := []byte(`{"id":"before-replacement","from":"fanout","to":"peer","body":"before","created_at":"2026-08-29T12:00:00.000Z"}`)
	request, err := http.NewRequest(http.MethodPost, client.URL+"/v1/messages", bytesReader(payload))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, response.StatusCode)
	response.Body.Close()
	require.Equal(t, "conversation_message", readJSON(t, owner)["type"])
	require.Equal(t, "conversation_message", readJSON(t, subscriber)["type"])

	replacement, _ := registerTestSession(t, wsURL, "fanout", "subscriber", "v1")
	defer replacement.Close(websocket.StatusNormalClosure, "test done")
	replaced := readJSON(t, subscriber)
	require.Equal(t, "error", replaced["type"])
	subscriberReadCtx, subscriberCancel := context.WithTimeout(context.Background(), time.Second)
	_, _, _ = subscriber.Read(subscriberReadCtx)
	subscriberCancel()
	payload = []byte(`{"id":"after-replacement","from":"fanout","to":"peer","body":"after","created_at":"2026-08-29T12:00:01.000Z"}`)
	request, err = http.NewRequest(http.MethodPost, client.URL+"/v1/messages", bytesReader(payload))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, response.StatusCode)
	response.Body.Close()
	require.Equal(t, "conversation_message", readJSON(t, owner)["type"])
	require.Equal(t, "conversation_message", readJSON(t, replacement)["type"])
}

func TestPeriodicSweepExpiresActivityAndPendingControl(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	router, err := transport.New(transport.Options{Store: durable, Port: 0, Clock: clock, ActivityTTL: time.Second, PendingControlTTL: time.Second, SweepInterval: time.Millisecond})
	require.NoError(t, err)
	require.NoError(t, router.Listen(context.Background()))
	defer router.Close(context.Background())
	_, wsURL := router.URLs()
	source, _, err := websocket.Dial(context.Background(), wsURL, nil)
	require.NoError(t, err)
	defer source.Close(websocket.StatusNormalClosure, "test done")
	sendJSON(t, source, map[string]any{"version": "v1", "frame_id": "source-register", "type": "register", "agent_id": "source", "instance_id": "source-instance", "conversation_sync": "v1", "agent_role": "user_interaction"})
	sourceRegistration := readJSON(t, source)
	target, _ := registerTestSession(t, wsURL, "target", "target-instance", "")
	defer target.Close(websocket.StatusNormalClosure, "test done")
	targetLease, leaseErr := durable.LeaseFor(context.Background(), "target")
	require.NoError(t, leaseErr)
	require.NotNil(t, targetLease)
	sendJSON(t, source, map[string]any{"version": "v1", "frame_id": "activity", "type": "activity", "agent_id": "source", "instance_id": "source-instance", "lease_id": sourceRegistration["lease_id"], "activity_state": "active"})
	require.Equal(t, "agent_activity", readJSON(t, source)["type"])
	sendJSON(t, source, map[string]any{"version": "v1", "frame_id": "control", "type": "model_control", "agent_id": "source", "instance_id": "source-instance", "lease_id": sourceRegistration["lease_id"], "target_agent_id": "target", "target_instance_id": "target-instance", "control_id": "expire-me", "action": "set_model", "model": map[string]any{"provider": "test", "id": "next"}})
	forwarded, ok := tryReadJSON(target, time.Second)
	require.True(t, ok)
	require.Equal(t, "model_control", forwarded["type"])
	require.NoError(t, clock.Advance(time.Second))
	require.Eventually(t, func() bool {
		value, ok := tryReadJSON(source, 10*time.Millisecond)
		return ok && value["code"] == "model_control_expired"
	}, time.Second, time.Millisecond)
}

type byteReader struct{ value []byte }

func (r *byteReader) Read(destination []byte) (int, error) {
	if len(r.value) == 0 {
		return 0, io.EOF
	}
	n := copy(destination, r.value)
	r.value = r.value[n:]
	return n, nil
}
