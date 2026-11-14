package transport

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

// This test joins one real listening Router and SQLite store with a managed
// manager bound to deterministic process/RPC doubles and a protocol-speaking
// legacy client. The client stands in for a tmux Pi window; it never starts a
// second tmux server.
type mixedDirectRPC struct {
	managedTestRPC
	prompted chan store.ManagedDirectAttempt
}

func (r *mixedDirectRPC) PrepareManagedMailbox(context.Context) (string, error) {
	return "mixed-cursor", nil
}

func (r *mixedDirectRPC) PromptManagedMailbox(_ context.Context, attempt store.ManagedDirectAttempt) (<-chan managed.DirectMailboxEvent, error) {
	r.prompted <- attempt
	events := make(chan managed.DirectMailboxEvent, 2)
	events <- managed.DirectMailboxEvent{Receipt: &managed.DirectMailboxReceipt{EntryIdentity: "mixed-entry"}}
	events <- managed.DirectMailboxEvent{Settled: true}
	close(events)
	return events, nil
}

func (r *mixedDirectRPC) RecoverManagedMailbox(context.Context, store.ManagedDirectAttempt) (managed.DirectMailboxRecovery, error) {
	return managed.DirectMailboxRecovery{}, nil
}

type mixedRPCFactory struct {
	rpc *mixedDirectRPC
}

func (f mixedRPCFactory) Open(context.Context, managed.Process) (managed.RPC, error) {
	return f.rpc, nil
}

type mixedProcessFactory struct {
	process *managedTestProcess
	session *string
}

func (f mixedProcessFactory) Start(_ context.Context, spec managed.CommandSpec) (managed.Process, error) {
	*f.session = spec.SessionID
	return f.process, nil
}

func mixedReadJSON(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, payload, err := conn.Read(ctx)
	require.NoError(t, err)
	var frame map[string]any
	require.NoError(t, json.Unmarshal(payload, &frame))
	return frame
}

func mixedWriteJSON(t *testing.T, conn *websocket.Conn, frame map[string]any) {
	t.Helper()
	payload, err := json.Marshal(frame)
	require.NoError(t, err)
	require.NoError(t, conn.Write(context.Background(), websocket.MessageText, payload))
}

func mixedDialLegacy(t *testing.T, wsURL, name string) (*websocket.Conn, map[string]any) {
	t.Helper()
	conn, _, err := websocket.Dial(context.Background(), wsURL, nil)
	require.NoError(t, err)
	mixedWriteJSON(t, conn, map[string]any{
		"version": "v1", "frame_id": "register-" + name, "type": "register",
		"agent_id": name, "instance_id": name + "-window", "ack_confirmation": "correlated_v1",
	})
	registered := mixedReadJSON(t, conn)
	require.Equal(t, "registered", registered["type"])
	return conn, registered
}

func mixedAckMessage(t *testing.T, conn *websocket.Conn, registration map[string]any) {
	t.Helper()
	frame := mixedReadJSON(t, conn)
	require.Equal(t, "message", frame["type"])
	message := frame["message"].(map[string]any)
	fields := map[string]any{"version": "v1", "agent_id": registration["agent_id"], "instance_id": registration["instance_id"], "lease_id": registration["lease_id"], "message_id": message["id"], "attempt_number": frame["attempt_number"]}
	fields["frame_id"] = "inject-" + message["id"].(string)
	fields["type"] = "injection_requested"
	mixedWriteJSON(t, conn, fields)
	fields["frame_id"] = "ack-" + message["id"].(string)
	fields["type"] = "ack"
	mixedWriteJSON(t, conn, fields)
	ack := mixedReadJSON(t, conn)
	require.Equal(t, "delivery_acknowledged", ack["type"])
}

func mixedPostMessage(t *testing.T, base, id, from, to, body string, headers map[string]string) (int, []byte) {
	t.Helper()
	payload, err := json.Marshal(map[string]string{
		"id": id, "from": from, "to": to, "body": body,
		"created_at": "2030-01-01T00:00:00.000Z",
	})
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, base+"/v1/messages", strings.NewReader(string(payload)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer response.Body.Close()
	result, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, result
}

func TestMixedManagedLegacyRoutingAndRetiredRestartAllIsolation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	home := t.TempDir()
	sessionRoot := filepath.Join(home, "sessions")
	piAgentDir := filepath.Join(home, ".pi", "agent")
	require.NoError(t, osMkdirAll(sessionRoot))
	require.NoError(t, osMkdirAll(piAgentDir))
	manifestPath, err := filepath.Abs(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	manifest, _, err := managed.LoadManifest(manifestPath)
	require.NoError(t, err)

	clock := deterministic.NewFixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{Clock: clock})
	require.NoError(t, err)
	require.NoError(t, durable.SetCanonicalQuietRecipient(ctx, "supervisor"))

	process := &managedTestProcess{identity: managed.ProcessIdentity{PID: 42001, StartMarker: "mixed-start", Incarnation: "mixed-incarnation"}, stopped: make(chan struct{})}
	sessionID := ""
	directRPC := &mixedDirectRPC{managedTestRPC: managedTestRPC{session: &sessionID, active: expectedManagedActiveToolsForTransportTest(manifest)}, prompted: make(chan store.ManagedDirectAttempt, 2)}
	manager, err := managed.NewManager(managed.ManagerOptions{
		Store: durable,
		Config: managed.Config{
			Enabled: true, PiExecutable: "/bin/sh", ManagedHome: home, PiAgentDir: piAgentDir, SessionRoot: sessionRoot,
			RouterURL: "http://127.0.0.1:1", ManifestPath: manifestPath, MechaRPCExtension: manifestPath,
			PiSubagentsExtension: manifestPath, AllowedWorkingDirectoryRoots: []string{root}, CanonicalQuietRecipientID: "supervisor",
		},
		Manifest: manifest, ProcessFactory: mixedProcessFactory{process: process, session: &sessionID}, RPCFactory: mixedRPCFactory{rpc: directRPC},
		CatalogSource:              managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		ProcessGroupLeaderVerifier: func(managed.ProcessIdentity) bool { return true },
	})
	require.NoError(t, err)

	router, err := New(Options{Store: durable, Clock: clock, Host: "127.0.0.1", Port: 0, Managed: manager})
	require.NoError(t, err)
	require.NoError(t, router.Listen(ctx))
	httpURL, wsURL := router.URLs()
	legacy, legacyRegistration := mixedDialLegacy(t, wsURL, "supervisor")
	defer legacy.Close(websocket.StatusNormalClosure, "test done")

	t.Cleanup(func() {
		_ = router.Close(context.Background())
		_ = manager.Close(context.Background())
		_ = durable.Close()
	})

	launchBody := `{"name":"managed-agent","cwd":"` + root + `"}`
	request, err := http.NewRequest(http.MethodPost, httpURL+"/v1/managed-agents", strings.NewReader(launchBody))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", "123e4567-e89b-42d3-a456-426614174001")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	response.Body.Close()
	require.Equal(t, http.StatusAccepted, response.StatusCode)

	var managedAgent *store.ManagedAgentRecord
	for {
		managedAgent, err = durable.ManagedAgent(ctx, "managed-agent")
		if err == nil && managedAgent.LifecycleState == "running" && managedAgent.AcceptingPrompts {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		default:
		}
		if managedAgent != nil && managedAgent.LastErrorCode != "" {
			t.Fatalf("managed launch failed: %s %s", managedAgent.LastErrorCode, managedAgent.LastErrorMessage)
		}
		runtimeGosched()
	}

	// Legacy -> managed uses the managed recipient directory and its direct
	// manager lane. The RPC sees the router-resolved sender and one attempt.
	status, _ := mixedPostMessage(t, httpURL, "legacy-to-managed", "supervisor", "managed-agent", "legacy payload", nil)
	require.Equal(t, http.StatusCreated, status)
	attempt := <-directRPC.prompted
	require.Equal(t, "supervisor", attempt.Message.From)
	require.Equal(t, "managed-agent", attempt.Fence.AgentID)
	select {
	case <-directRPC.prompted:
		t.Fatal("legacy message was delivered to managed process twice")
	default:
	}

	// Managed -> legacy uses the full managed fence for the sender. The persisted
	// attempt names the original legacy sender as the reply recipient, and a
	// duplicate reply call cannot create a second ordinary message.
	managedAttempt := store.ManagedDirectAttempt{MessageID: "legacy-request", Message: domain.Message{From: "supervisor"}, Fence: managedFence(managedAgent)}
	reply := managed.DirectMailboxReply{EntryIdentity: "managed-entry", Body: "managed payload"}
	require.NoError(t, router.persistManagedDirectReply(ctx, managedAttempt, reply))
	require.NoError(t, router.persistManagedDirectReply(ctx, managedAttempt, reply))
	messageFrame := mixedReadJSON(t, legacy)
	require.Equal(t, "message", messageFrame["type"])
	delivered := messageFrame["message"].(map[string]any)
	require.Equal(t, "managed-agent", delivered["from"])
	require.Equal(t, "supervisor", delivered["to"])
	require.Equal(t, "managed payload", delivered["body"])
	incarnation := messageFrame["message_incarnation_id"].(string)
	mixedWriteJSON(t, legacy, map[string]any{"version": "v1", "frame_id": "inject-managed", "type": "injection_requested", "agent_id": "supervisor", "instance_id": "supervisor-window", "lease_id": legacyRegistration["lease_id"], "message_id": delivered["id"], "attempt_number": 1})
	mixedWriteJSON(t, legacy, map[string]any{"version": "v1", "frame_id": "ack-managed", "type": "ack", "agent_id": "supervisor", "instance_id": "supervisor-window", "lease_id": legacyRegistration["lease_id"], "message_id": delivered["id"], "attempt_number": 1})
	ack := mixedReadJSON(t, legacy)
	require.Equal(t, "delivery_acknowledged", ack["type"])
	require.Equal(t, incarnation, ack["message_incarnation_id"])
	stored, err := durable.MessageFor(ctx, delivered["id"].(string))
	require.NoError(t, err)
	require.Nil(t, stored)
	history, err := durable.ConversationHistory(ctx, "managed-agent", "supervisor")
	require.NoError(t, err)
	require.Len(t, history, 2)
	require.Equal(t, "managed-agent", history[1].From)
	require.Equal(t, "managed payload", history[1].Body)

	// A caller-provided source cannot classify an ordinary message as quiet.
	status, _ = mixedPostMessage(t, httpURL, "ordinary-supervisor", "managed-agent", "supervisor", "ordinary", map[string]string{"X-Mecha-Managed-Control-Request-ID": ""})
	require.Equal(t, http.StatusCreated, status)
	mixedAckMessage(t, legacy, legacyRegistration)
	quiet, err := durable.ManagedHistoryPage(ctx, "managed-agent", "quiet", 0, 20)
	require.NoError(t, err)
	require.Empty(t, quiet.Entries)
	// The canonical managed direct-mail authorization is one-use and writes to
	// quiet history only when the exact sender and recipient match.
	require.NoError(t, durable.ReserveManagedQuietSendAuthorization(ctx, "quiet-control", "managed-agent", "supervisor"))
	status, _ = mixedPostMessage(t, httpURL, "quiet-supervisor", "managed-agent", "supervisor", "quiet", map[string]string{"X-Mecha-Managed-Control-Request-ID": "quiet-control"})
	require.Equal(t, http.StatusCreated, status)
	quiet, err = durable.ManagedHistoryPage(ctx, "managed-agent", "quiet", 0, 20)
	require.NoError(t, err)
	require.Len(t, quiet.Entries, 1)
	require.Equal(t, "quiet", quiet.Entries[0].Message)
	mixedAckMessage(t, legacy, legacyRegistration)

	// A forged window/identity cannot acknowledge the legacy lease.
	spoof, _ := mixedDialLegacy(t, wsURL, "spoof-window")
	defer spoof.Close(websocket.StatusNormalClosure, "test done")
	mixedWriteJSON(t, spoof, map[string]any{"version": "v1", "frame_id": "forged", "type": "injection_requested", "agent_id": "supervisor", "instance_id": "supervisor-window", "lease_id": legacyRegistration["lease_id"], "message_id": delivered["id"], "attempt_number": 1})
	forged := mixedReadJSON(t, spoof)
	require.Equal(t, "error", forged["type"])
	require.Equal(t, "lease_not_active", forged["code"])

	// The retired Restart All route is a plain 404 on the same real router and
	// store. The managed agent is untouched and the live legacy protocol client
	// sees no signal or request; the tmux-backed lease remains usable.
	managedBeforeRetired, err := durable.ManagedAgent(ctx, "managed-agent")
	require.NoError(t, err)
	restartRequest, err := http.NewRequest(http.MethodPost, httpURL+"/v1/managed-agents/restart-all", strings.NewReader(`{}`))
	require.NoError(t, err)
	restartRequest.Header.Set("Content-Type", "application/json")
	restartRequest.Header.Set("X-Request-ID", "123e4567-e89b-42d3-a456-426614174002")
	restartResponse, err := http.DefaultClient.Do(restartRequest)
	require.NoError(t, err)
	defer restartResponse.Body.Close()
	require.Equal(t, http.StatusNotFound, restartResponse.StatusCode)
	managedAfterRetired, err := durable.ManagedAgent(ctx, "managed-agent")
	require.NoError(t, err)
	require.Equal(t, managedBeforeRetired.LifecycleState, managedAfterRetired.LifecycleState)
	require.NotEqual(t, "restarting", managedAfterRetired.LifecycleState)
	require.Equal(t, managedBeforeRetired.RuntimeGeneration, managedAfterRetired.RuntimeGeneration)

	// The managed name supervisor has no special authority: this live legacy
	// registration is the canonical recipient and still owns its lease.
	mixedWriteJSON(t, legacy, map[string]any{"version": "v1", "frame_id": "legacy-ping", "type": "ping"})
	pong := mixedReadJSON(t, legacy)
	require.Equal(t, "pong", pong["type"])
	// Read deadlines are terminal for this client, so this is the final
	// protocol assertion. The retired Restart All request must not emit a control frame to legacy.
	readCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	_, _, readErr := legacy.Read(readCtx)
	cancel()
	require.Error(t, readErr, "The retired Restart All request must not emit a control frame to legacy")
}

func managedFence(agent *store.ManagedAgentRecord) store.ManagedRuntimeFence {
	return store.ManagedRuntimeFence{AgentID: agent.AgentID, PiSessionID: agent.PiSessionID, SessionEpoch: agent.SessionEpoch, ProcessPID: agent.ProcessPID, ProcessStartMarker: agent.ProcessStartMarker, ProcessIncarnation: agent.ProcessIncarnation, RuntimeGeneration: agent.RuntimeGeneration, ManifestRevision: agent.ManifestRevision, ManifestHash: agent.ManifestHash}
}

func runtimeGosched()              { runtime.Gosched() }
func osMkdirAll(path string) error { return os.MkdirAll(path, 0o700) }
