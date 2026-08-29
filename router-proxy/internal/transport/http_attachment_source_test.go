package transport

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/attachments"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestHTTPImageAttachmentResponseAndDeliveredBodyMatchNode(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	database := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(context.Background(), database, store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	attachmentDirectory := filepath.Join(t.TempDir(), "images")
	attachmentStore, err := attachments.New(attachments.Options{Directory: attachmentDirectory, Clock: clock})
	require.NoError(t, err)
	router, err := New(Options{Store: durable, Port: 0, Clock: clock, Attachments: attachmentStore})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/agents"
	sender, registration := registerHTTPSourceClient(t, wsURL, "web-sender", "web-instance", "web")
	defer sender.Close(websocket.StatusNormalClosure, "test done")

	image := pngDataURL(pngBytes(2, 3))
	payload := map[string]any{
		"id":          "image-message",
		"from":        "web-sender",
		"to":          "receiver",
		"body":        "see image",
		"created_at":  "2026-08-29T12:00:00.000Z",
		"attachments": []any{map[string]any{"type": "image", "data_url": image, "name": "screen shot.png"}},
	}
	response := postJSON(t, server.URL+"/v1/messages", payload)
	require.Equal(t, http.StatusCreated, response.StatusCode)
	var result map[string]any
	require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
	require.NoError(t, response.Body.Close())

	message := result["message"].(map[string]any)
	attachment := message["attachments"].([]any)[0].(map[string]any)
	require.Equal(t, "image-message-1-", attachment["id"].(string)[:len("image-message-1-")])
	require.Equal(t, "/v1/attachments/"+attachment["id"].(string), attachment["url"])
	require.Equal(t, "image/png", attachment["mime_type"])
	require.Equal(t, float64(len(pngBytes(2, 3))), attachment["size_bytes"])
	require.Equal(t, "screen shot.png", attachment["name"])
	require.NotContains(t, attachment, "path")
	require.Equal(t, map[string]any{"kind": "web_ui"}, message["source"])

	stored, err := durable.MessageFor(context.Background(), "image-message")
	require.NoError(t, err)
	require.NotNil(t, stored)
	attachmentID := attachment["id"].(string)
	expectedPath := filepath.Join(attachmentDirectory, attachmentID)
	require.Equal(t, "see image\n\nAttached image:\n1. "+expectedPath, stored.Body)

	conversation := result["conversation_message"].(map[string]any)
	require.Equal(t, "see image", conversation["body"])
	require.Equal(t, map[string]any{"kind": "web_ui"}, conversation["source"])

	getResponse, err := http.Get(server.URL + attachment["url"].(string))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, getResponse.StatusCode)
	require.Equal(t, "image/png", getResponse.Header.Get("Content-Type"))
	require.NoError(t, getResponse.Body.Close())

	traversal, err := http.Get(server.URL + "/v1/attachments/" + url.PathEscape("../"+attachmentID))
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, traversal.StatusCode)
	require.NoError(t, traversal.Body.Close())

	require.NotEmpty(t, registration["lease_id"])
}

func TestHTTPSourceInferenceMatchesConnectedNodeClient(t *testing.T) {
	for _, test := range []struct {
		clientType string
		kind       string
	}{
		{clientType: "web", kind: "web_ui"},
		{clientType: "android", kind: "android"},
	} {
		t.Run(test.clientType, func(t *testing.T) {
			clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
			database := filepath.Join(t.TempDir(), "router.db")
			durable, err := store.Open(context.Background(), database, store.Options{Clock: clock})
			require.NoError(t, err)
			defer durable.Close()
			router, err := New(Options{Store: durable, Port: 0, Clock: clock})
			require.NoError(t, err)
			server := httptest.NewServer(router)
			defer server.Close()

			wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/agents"
			sender, _ := registerHTTPSourceClient(t, wsURL, "source-client", "source-instance", test.clientType)
			defer sender.Close(websocket.StatusNormalClosure, "test done")

			response := postJSON(t, server.URL+"/v1/messages", map[string]any{
				"id":         "source-message-" + test.clientType,
				"from":       "source-client",
				"to":         "receiver",
				"body":       "hello",
				"created_at": "2026-08-29T12:00:00.000Z",
			})
			require.Equal(t, http.StatusCreated, response.StatusCode)
			var result map[string]any
			require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
			require.NoError(t, response.Body.Close())
			require.Equal(t, map[string]any{"kind": test.kind}, result["message"].(map[string]any)["source"])
			require.Equal(t, map[string]any{"kind": test.kind}, result["conversation_message"].(map[string]any)["source"])
		})
	}
}

func TestHTTPSourceInferenceUsesFirstAndroidThenWebSession(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	router, err := New(Options{Store: durable, Port: 0, Clock: clock})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/agents"
	android, _ := registerHTTPSourceClient(t, wsURL, "source-client", "android-instance", "android")
	defer android.Close(websocket.StatusNormalClosure, "test done")
	web, _ := registerHTTPSourceClient(t, wsURL, "source-client", "web-instance", "web")
	defer web.Close(websocket.StatusNormalClosure, "test done")

	response := postJSON(t, server.URL+"/v1/messages", map[string]any{
		"id": "source-message-android-first", "from": "source-client", "to": "receiver", "body": "hello", "created_at": "2026-08-29T12:00:00.000Z",
	})
	require.Equal(t, http.StatusCreated, response.StatusCode)
	var result map[string]any
	require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
	require.NoError(t, response.Body.Close())
	require.Equal(t, map[string]any{"kind": "android"}, result["message"].(map[string]any)["source"])
	require.Equal(t, map[string]any{"kind": "android"}, result["conversation_message"].(map[string]any)["source"])
}

func registerHTTPSourceClient(t *testing.T, wsURL, agentID, instanceID, clientType string) (*websocket.Conn, map[string]any) {
	t.Helper()
	conn, _, err := websocket.Dial(context.Background(), wsURL, nil)
	require.NoError(t, err)
	sendHTTPTestJSON(t, conn, map[string]any{
		"version": "v1", "frame_id": "register-" + agentID, "type": "register",
		"agent_id": agentID, "instance_id": instanceID, "conversation_sync": "v1", "client_type": clientType,
	})
	return conn, readHTTPTestJSON(t, conn)
}

func sendHTTPTestJSON(t *testing.T, conn *websocket.Conn, value map[string]any) {
	t.Helper()
	payload, err := json.Marshal(value)
	require.NoError(t, err)
	require.NoError(t, conn.Write(context.Background(), websocket.MessageText, payload))
}

func readHTTPTestJSON(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, payload, err := conn.Read(ctx)
	require.NoError(t, err)
	var value map[string]any
	require.NoError(t, json.Unmarshal(payload, &value))
	return value
}

func postJSON(t *testing.T, endpoint string, payload map[string]any) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	request, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(body)))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	return response
}

func pngBytes(width, height byte) []byte {
	data := make([]byte, 24)
	copy(data, []byte("\x89PNG\r\n\x1a\n"))
	copy(data[12:], []byte("IHDR"))
	data[19], data[23] = width, height
	return data
}

func pngDataURL(data []byte) string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
}
