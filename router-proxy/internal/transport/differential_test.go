package transport_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
	"github.com/stretchr/testify/require"
)

type differentialResult struct {
	AgentsStatus    int
	AgentsCORS      string
	PreflightStatus int
	PreflightCORS   string
	NoOriginStatus  int
	FrameTypes      []string
	MessageID       string
	Attempt         int
	AckMessageID    string
}

func TestNodeAndGoTransportDifferentialForCORSAndDelivery(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	nodePort := freePort(t)
	goPort := freePort(t)
	temp := t.TempDir()

	nodeCommand := exec.Command("node", "tools/run_router.mjs", "--database", filepath.Join(temp, "node.db"), "--log", filepath.Join(temp, "node.jsonl"), "--host", "127.0.0.1", "--port", fmt.Sprint(nodePort))
	nodeCommand.Dir = root
	stdout, err := nodeCommand.StdoutPipe()
	require.NoError(t, err)
	stderr, err := nodeCommand.StderrPipe()
	require.NoError(t, err)
	require.NoError(t, nodeCommand.Start())
	defer func() { _ = nodeCommand.Process.Signal(syscall.SIGTERM); _ = nodeCommand.Wait() }()
	ready := make(chan string, 8)
	go scanOutput(stdout, ready)
	go scanOutput(stderr, ready)
	select {
	case line := <-ready:
		require.Contains(t, line, "MECHA_V2_ROUTER_READY")
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for isolated Node router")
	}

	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	goStore, err := store.Open(context.Background(), filepath.Join(temp, "go.db"), store.Options{Clock: clock})
	require.NoError(t, err)
	defer goStore.Close()
	goRouter, err := transport.New(transport.Options{Store: goStore, Host: "127.0.0.1", Port: goPort, Clock: clock})
	require.NoError(t, err)
	require.NoError(t, goRouter.Listen(context.Background()))
	defer goRouter.Close(context.Background())

	nodeResult := exerciseEndpoint(t, nodePort)
	goResult := exerciseEndpoint(t, goPort)
	require.Equal(t, nodeResult, goResult)

	for _, literal := range []string{"1.0", "1e0", `"1"`} {
		nodeOutcome := exerciseLifecycleNumericFrame(t, nodePort, literal)
		goOutcome := exerciseLifecycleNumericFrame(t, goPort, literal)
		require.Equal(t, nodeOutcome, goOutcome, literal)
		if literal == `"1"` {
			require.Equal(t, "invalid_frame", nodeOutcome.Code, literal)
		} else {
			require.Equal(t, "subagent_lifecycle_acknowledged", nodeOutcome.Type, literal)
			require.Equal(t, "applied", nodeOutcome.Disposition, literal)
		}
	}
}

type lifecycleNumericOutcome struct {
	Type        string
	Code        string
	Disposition string
}

func exerciseLifecycleNumericFrame(t *testing.T, port int, literal string) lifecycleNumericOutcome {
	t.Helper()
	identifier := strings.NewReplacer(".", "-", "e", "power", `"`, "quoted").Replace(literal)
	agentID := "numeric-agent-" + identifier
	instanceID := "numeric-instance-" + identifier
	ws, _, err := websocket.Dial(context.Background(), fmt.Sprintf("ws://127.0.0.1:%d/v1/agents", port), nil)
	require.NoError(t, err)
	defer ws.Close(websocket.StatusNormalClosure, "test done")
	sendFrame(t, ws, map[string]any{"version": "v1", "frame_id": "register-" + identifier, "type": "register", "agent_id": agentID, "instance_id": instanceID})
	registered := readFrame(t, ws)
	require.Equal(t, "registered", registered["type"])
	payload := fmt.Sprintf(`{"version":"v1","frame_id":"lifecycle-%s","type":"subagent_lifecycle_snapshot","agent_id":"%s","instance_id":"%s","parent_session_id":"session-%s","lease_id":"%s","producer_epoch":"123e4567-e89b-42d3-a456-426614174000","client_sequence":%s,"event_id":"event-%s","subagents":[{"id":"child-%s","type":"worker","status":"running"}]}`,
		identifier, agentID, instanceID, identifier, registered["lease_id"], literal, identifier, identifier)
	require.NoError(t, ws.Write(context.Background(), websocket.MessageText, []byte(payload)))
	response := readFrame(t, ws)
	outcome := lifecycleNumericOutcome{Type: frameStringValue(response, "type"), Code: frameStringValue(response, "code"), Disposition: frameStringValue(response, "disposition")}
	return outcome
}

func frameStringValue(frame map[string]any, key string) string {
	value, _ := frame[key].(string)
	return value
}

func exerciseEndpoint(t *testing.T, port int) differentialResult {
	t.Helper()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	request, err := http.NewRequest(http.MethodGet, base+"/v1/agents", nil)
	require.NoError(t, err)
	request.Header.Set("Origin", "https://diff.example")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	result := differentialResult{AgentsStatus: response.StatusCode, AgentsCORS: response.Header.Get("Access-Control-Allow-Origin")}
	response.Body.Close()

	request, err = http.NewRequest(http.MethodOptions, base+"/v1/agents", nil)
	require.NoError(t, err)
	request.Header.Set("Origin", "https://diff.example")
	response, err = http.DefaultClient.Do(request)
	require.NoError(t, err)
	result.PreflightStatus, result.PreflightCORS = response.StatusCode, response.Header.Get("Access-Control-Allow-Origin")
	response.Body.Close()

	request, err = http.NewRequest(http.MethodOptions, base+"/v1/agents", nil)
	require.NoError(t, err)
	response, err = http.DefaultClient.Do(request)
	require.NoError(t, err)
	result.NoOriginStatus = response.StatusCode
	response.Body.Close()

	ws, _, err := websocket.Dial(context.Background(), fmt.Sprintf("ws://127.0.0.1:%d/v1/agents", port), &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{"https://diff.example"}}})
	require.NoError(t, err)
	defer ws.Close(websocket.StatusNormalClosure, "test done")
	sendFrame(t, ws, map[string]any{"version": "v1", "frame_id": "register-frame", "type": "register", "agent_id": "diff-agent", "instance_id": "diff-instance", "ack_confirmation": "correlated_v1"})
	registered := readFrame(t, ws)
	require.Equal(t, "registered", registered["type"])
	message := map[string]any{"id": "diff-message", "from": "diff-sender", "to": "diff-agent", "body": "hello", "created_at": "2026-08-29T12:00:00.000Z"}
	payload, err := json.Marshal(message)
	require.NoError(t, err)
	request, err = http.NewRequest(http.MethodPost, base+"/v1/messages", strings.NewReader(string(payload)))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Correlation-Id", "diff-http")
	response, err = http.DefaultClient.Do(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, response.StatusCode)
	response.Body.Close()
	offered := readFrame(t, ws)
	result.FrameTypes = append(result.FrameTypes, registered["type"].(string), offered["type"].(string))
	result.MessageID = offered["message"].(map[string]any)["id"].(string)
	result.Attempt = int(offered["attempt_number"].(float64))
	leaseID := registered["lease_id"].(string)
	for _, frameType := range []string{"injection_requested", "ack"} {
		sendFrame(t, ws, map[string]any{"version": "v1", "frame_id": frameType + "-frame", "type": frameType, "agent_id": "diff-agent", "instance_id": "diff-instance", "lease_id": leaseID, "message_id": "diff-message", "attempt_number": 1})
	}
	ack := readFrame(t, ws)
	result.FrameTypes = append(result.FrameTypes, ack["type"].(string))
	result.AckMessageID = ack["message_id"].(string)
	return result
}

func freePort(t *testing.T) int {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	return port
}
func scanOutput(reader io.Reader, lines chan<- string) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		lines <- scanner.Text()
	}
}
func sendFrame(t *testing.T, conn *websocket.Conn, frame map[string]any) {
	t.Helper()
	payload, err := json.Marshal(frame)
	require.NoError(t, err)
	require.NoError(t, conn.Write(context.Background(), websocket.MessageText, payload))
}
func readFrame(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, payload, err := conn.Read(ctx)
	require.NoError(t, err)
	var frame map[string]any
	require.NoError(t, json.Unmarshal(payload, &frame))
	return frame
}
