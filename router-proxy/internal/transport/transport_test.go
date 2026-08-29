package transport_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
	_, payload, err := conn.Read(context.Background())
	require.NoError(t, err)
	var value map[string]any
	require.NoError(t, json.Unmarshal(payload, &value))
	return value
}
func bytesReader(value []byte) *byteReader { return &byteReader{value: value} }

type byteReader struct{ value []byte }

func (r *byteReader) Read(destination []byte) (int, error) {
	if len(r.value) == 0 {
		return 0, io.EOF
	}
	n := copy(destination, r.value)
	r.value = r.value[n:]
	return n, nil
}
