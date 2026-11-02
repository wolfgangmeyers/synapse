package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func urgencyPUT(t *testing.T, client *http.Client, base, id, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, base+"/v1/agents/"+id+"/urgency", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Origin", "https://arbitrary.example")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
	require.Empty(t, resp.Header.Get("Access-Control-Allow-Credentials"))
	var result map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	return resp.StatusCode, result
}

func TestAgentUrgencyCommonStrictHTTP(t *testing.T) {
	durable, router := newTransportOrderingFixture(t)
	server := httptest.NewServer(router)
	defer server.Close()
	_, err := durable.AcquireAgentLease(context.Background(), store.LeaseRequest{AgentID: "legacy", InstanceID: "instance", LeaseID: "lease", LeaseDuration: time.Minute})
	require.NoError(t, err)
	for _, body := range []string{`{}`, `null`, `[]`, `true`, `{"urgent":null}`, `{"urgent":true,"urgent":false}`, `{"urgent":"true"}`, `{"urgent":1}`, `{"urgent":true,"other":false}`, `{"Urgent":true}`, `{"urgent":true} {}`, `{"urgent":true} garbage`, `{"urgent":`, ``} {
		t.Run(body, func(t *testing.T) {
			status, _ := urgencyPUT(t, server.Client(), server.URL, "legacy", body)
			require.Equal(t, 400, status)
		})
	}
	for _, value := range []bool{true, true, false, false} {
		body, _ := json.Marshal(map[string]bool{"urgent": value})
		status, result := urgencyPUT(t, server.Client(), server.URL, "legacy", string(body))
		require.Equal(t, 200, status)
		require.Equal(t, map[string]any{"agent_id": "legacy", "urgent": value}, result)
	}
	status, _ := urgencyPUT(t, server.Client(), server.URL, "unknown", `{"urgent":true}`)
	require.Equal(t, 404, status)
	req, err := http.NewRequest(http.MethodOptions, server.URL+"/v1/agents/legacy/urgency", nil)
	require.NoError(t, err)
	req.Header.Set("Origin", "not-even-a-url")
	req.Header.Set("Access-Control-Request-Method", "PUT")
	resp, err := server.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 204, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Access-Control-Allow-Methods"), "PUT")
}

func TestAgentUrgencyPublishesBothSocketsInCommitOrder(t *testing.T) {
	router, durable, server := newUILiveTestRouter(t)
	reserveUILiveAgent(t, durable, "urgent-worker")
	ui := dialUILive(t, server, "")
	defer ui.Close(websocket.StatusNormalClosure, "done")
	readUILiveFrame(t, ui)
	readUILiveFrame(t, ui)
	legacy, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/agents", nil)
	require.NoError(t, err)
	require.NoError(t, legacy.Write(context.Background(), websocket.MessageText, []byte(`{"version":"v1","frame_id":"register-observer","type":"register","agent_id":"observer","instance_id":"observer-instance","conversation_sync":"v1"}`)))
	welcome := readUILiveFrame(t, legacy)
	defer legacy.Close(websocket.StatusNormalClosure, "done")
	require.Equal(t, "registered", welcome["type"])
	const count = 12
	var wg sync.WaitGroup
	wg.Add(2)
	streams := make([][]bool, 2)
	for index, conn := range []*websocket.Conn{ui, legacy} {
		go func(index int, conn *websocket.Conn) {
			defer wg.Done()
			var projectionVersion float64
			for len(streams[index]) < count {
				frame := readUILiveFrame(t, conn)
				if version, ok := frame["projection_version"].(float64); ok {
					require.GreaterOrEqual(t, version, projectionVersion)
					projectionVersion = version
				}
				if frame["type"] == "agent_urgency_changed" {
					require.Equal(t, "urgent-worker", frame["agent_id"])
					streams[index] = append(streams[index], frame["urgent"].(bool))
				}
			}
		}(index, conn)
	}
	var writers sync.WaitGroup
	for i := 0; i < count; i++ {
		writers.Add(1)
		go func(index int, value bool) {
			defer writers.Done()
			if index%2 == 0 {
				body, _ := json.Marshal(map[string]bool{"urgent": value})
				status, _ := urgencyPUT(t, server.Client(), server.URL, "urgent-worker", string(body))
				require.Equal(t, 200, status)
			} else {
				_, err := router.coordinateAgentUrgency(func() (store.AgentUrgencyResult, bool, error) {
					saved, err := durable.SetAgentUrgency(context.Background(), "urgent-worker", value)
					return store.AgentUrgencyResult{AgentID: "urgent-worker", Urgent: saved}, err == nil, err
				})
				require.NoError(t, err)
			}
		}(i, i%2 == 0)
	}
	writers.Wait()
	wg.Wait()
	require.Equal(t, streams[0], streams[1])
	snapshot, err := durable.ManagedAgentSnapshot(context.Background(), "urgent-worker")
	require.NoError(t, err)
	require.Equal(t, streams[0][count-1], managed.PublicAgent(snapshot.Agent)["urgent"])
}

func TestAgentUrgencyFailureDoesNotPublish(t *testing.T) {
	durable, router := newTransportOrderingFixture(t)
	server := httptest.NewServer(router)
	defer server.Close()
	frames := 0
	observer := &session{router: router, sendOverride: func(map[string]any) error { frames++; return nil }}
	router.convos["observer"] = map[string]*session{"instance": observer}
	status, _ := urgencyPUT(t, server.Client(), server.URL, "missing", `{"urgent":true}`)
	require.Equal(t, 404, status)
	require.Zero(t, frames)
	require.NoError(t, durable.Close())
	status, _ = urgencyPUT(t, server.Client(), server.URL, "missing", `{"urgent":true}`)
	require.Equal(t, 500, status)
	require.Zero(t, frames)
}

func TestAgentUrgencyConnectedProjectionWithoutLifecycle(t *testing.T) {
	durable, router := newTransportOrderingFixture(t)
	router.lifecycleAvailable = false
	lease, err := durable.AcquireAgentLease(context.Background(), store.LeaseRequest{AgentID: "tmux-worker", InstanceID: "instance", LeaseID: "lease", LeaseDuration: time.Minute})
	require.NoError(t, err)
	router.sessions["tmux-worker"] = &session{router: router, agentID: "tmux-worker", instanceID: "instance", deliveryInstance: "instance", leaseID: lease.LeaseID, closeOverride: func(websocket.StatusCode, string) error { return nil }}
	for _, value := range []bool{false, true, false} {
		_, err := durable.SetAgentUrgency(context.Background(), "tmux-worker", value)
		require.NoError(t, err)
		projection := router.connectedAgents(context.Background(), false)
		agents := projection["agents"].([]map[string]any)
		require.Len(t, agents, 1)
		require.Equal(t, value, agents[0]["urgent"])
	}
}

func TestAgentUrgencyManagedProjectionAndReconnect(t *testing.T) {
	_, durable, server := newUILiveTestRouter(t)
	reserveUILiveAgent(t, durable, "managed-worker")
	for _, value := range []bool{false, true, false} {
		body, _ := json.Marshal(map[string]bool{"urgent": value})
		status, _ := urgencyPUT(t, server.Client(), server.URL, "managed-worker", string(body))
		require.Equal(t, 200, status)
		for _, path := range []string{"/v1/managed-agents", "/v1/managed-agents/managed-worker"} {
			response, err := server.Client().Get(server.URL + path)
			require.NoError(t, err)
			require.Equal(t, 200, response.StatusCode)
			var projection map[string]any
			require.NoError(t, json.NewDecoder(response.Body).Decode(&projection))
			response.Body.Close()
			if path == "/v1/managed-agents" {
				require.Equal(t, value, projection["agents"].([]any)[0].(map[string]any)["urgent"])
			} else {
				require.Equal(t, value, projection["agent"].(map[string]any)["urgent"])
			}
		}
		conn := dialUILive(t, server, "")
		snapshot := readUILiveFrame(t, conn)
		require.Equal(t, value, snapshot["agents"].([]any)[0].(map[string]any)["urgent"])
		conn.Close(websocket.StatusNormalClosure, "done")
	}
}
