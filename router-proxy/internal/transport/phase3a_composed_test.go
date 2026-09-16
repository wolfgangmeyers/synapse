package transport

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managedbridge"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestPhase3AComposedRouterBridgeRPCStream(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	require.Contains(t, durable.MigrationIDs(), "049_managed_bridge_store")

	verifier, err := managedbridge.NewVerifier(bytes.Repeat([]byte{'p'}, 32))
	require.NoError(t, err)
	activated := make(chan struct{}, 1)
	admitted := make(chan struct{}, 4)
	release := make(chan struct{}, 4)
	handler := NewManagedBridgeHandler(durable, ManagedBridgeHandlerOptions{
		afterActivate: func(*managedBridgeSession) { activated <- struct{}{} },
		afterEventAdmission: func(*managedBridgeSession) {
			admitted <- struct{}{}
			<-release
		},
	})
	router, err := New(Options{Store: durable, ManagedBridge: handler})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	defer server.Close()

	script := filepath.Join(t.TempDir(), "pi-helper.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nwhile IFS= read -r line; do printf '%s\\n' \"$line\"; done\n"), 0o700))

	var bridge *managedbridge.Bridge
	dial := func(dialCtx context.Context, rawURL string) (*websocket.Conn, error) {
		fence := bridge.Fence()
		if err := durable.BindManagedBridgeRuntime(dialCtx, fence.BridgeInstance, phase3AStoreFence(fence)); err != nil {
			return nil, err
		}
		conn, _, dialErr := websocket.Dial(dialCtx, rawURL, nil)
		return conn, dialErr
	}
	config := managedbridge.Config{
		RouterURL: "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/managed-bridge",
		TokenPath: filepath.Join(t.TempDir(), "unused-token"), Agent: "phase3a.bridge",
		PiExecutable: script, PiDir: t.TempDir(), MaxStdoutLine: 4096, MaxStderrBytes: 4096,
		OutboxCapacity: 8, CommandWindow: 8, ReconnectInitial: time.Millisecond, ReconnectMax: 4 * time.Millisecond,
	}
	bridge, err = managedbridge.NewBridge(config, verifier, managedbridge.Options{Dial: dial, Random: func(int64) int64 { return 0 }})
	require.NoError(t, err)
	bridgeID := bridge.Fence().BridgeInstance
	initial := phase3AStoreFence(bridge.Fence())
	require.NoError(t, durable.ReserveManagedLaunch(ctx,
		store.ManagedAgentRecord{AgentID: config.Agent, Name: config.Agent, SlotHeld: true, Cwd: t.TempDir(),
			SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "running", ActivityState: "idle", AcceptingPrompts: true,
			PiSessionID: initial.PiSessionID, SessionEpoch: initial.SessionEpoch, ProcessPID: initial.ProcessPID, ProcessStartMarker: initial.ProcessStartMarker, ProcessIncarnation: initial.ProcessIncarnation, RuntimeGeneration: initial.RuntimeGeneration, ManifestRevision: initial.ManifestRevision, ManifestHash: initial.ManifestHash},
		store.ManagedOperation{OperationID: "123e4567-e89b-42d3-a456-426614174200", Method: http.MethodPost, CanonicalPath: "/v1/managed-agents", RequestID: "123e4567-e89b-42d3-a456-426614174201", CanonicalBodyHash: strings.Repeat("b", 64), ResponseStatus: http.StatusAccepted, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}))
	secret, err := verifier.Bytes()
	require.NoError(t, err)
	_, err = durable.ReserveManagedBridge(ctx, store.ManagedBridgeReservation{AgentID: config.Agent, BridgeID: bridgeID, Verifier: secret, Fence: initial})
	require.NoError(t, err)

	runCtx, cancel := context.WithCancel(ctx)
	runDone := make(chan error, 1)
	go func() { runDone <- bridge.Run(runCtx) }()
	awaitSignal(t, activated)
	fence := bridge.Fence()

	// The actual Router ServeHTTP route handles the authenticated bridge. The
	// RPC stream is the manager-side in-process reader/writer for that route.
	stream1, err := handler.Open(ctx, fence)
	require.NoError(t, err)
	first := `{"jsonrpc":"2.0","method":"echo","id":"same","params":{"value":1}}`
	firstCanonical := `{"id":"same","jsonrpc":"2.0","method":"echo","params":{"value":1}}`
	cut := len(first) / 2
	n, err := stream1.Write([]byte(first[:cut]))
	require.NoError(t, err)
	require.Equal(t, cut, n)
	n, err = stream1.Write(append([]byte(first[cut:]), '\n'))
	require.NoError(t, err)
	require.Equal(t, len(first)-cut+1, n)
	awaitSignal(t, admitted)
	release <- struct{}{}
	require.Equal(t, firstCanonical+"\n", readRPCLine(t, stream1, len(firstCanonical)+1, func() {
		cursors, cursorErr := durable.ManagedBridgeCursors(ctx, bridgeID)
		require.NoError(t, cursorErr)
		require.Equal(t, uint64(1), cursors.EventAppliedNext)
	}))
	assertPhase3ACursors(t, durable, bridgeID, fence, 2, 2, 2, 1)

	// Attach replaces the old reader. Closing the replacement leaves a durable
	// admitted RPC event unapplied; the next attach drains that exact event.
	stream2, err := handler.Attach(ctx, fence)
	require.NoError(t, err)
	_, err = stream1.Write([]byte(first + "\n"))
	require.ErrorIs(t, err, managedbridge.ErrRPCStreamClosed)
	require.NoError(t, stream2.Close())
	second := `{"jsonrpc":"2.0","method":"echo","id":"other","params":{"value":2}}`
	secondCanonical := `{"id":"other","jsonrpc":"2.0","method":"echo","params":{"value":2}}`
	secondHash, err := managedbridge.HashPayload([]byte(second))
	require.NoError(t, err)
	_, err = durable.QueueManagedBridgeCommand(ctx, store.ManagedBridgeCommandInput{BridgeID: bridgeID, RequestID: "123e4567-e89b-42d3-a456-426614174202", Name: managedbridge.CommandRPC, Payload: []byte(second), PayloadHash: secondHash, Fence: phase3AStoreFence(fence)})
	require.NoError(t, err)
	handler.Wake(config.Agent, bridgeID)
	awaitSignal(t, admitted)
	assertPhase3ACursors(t, durable, bridgeID, fence, 3, 2, 3, 2)
	release <- struct{}{}

	stream3, err := handler.Attach(ctx, fence)
	require.NoError(t, err)
	require.Equal(t, secondCanonical+"\n", readRPCLine(t, stream3, len(secondCanonical)+1, nil))
	assertPhase3ACursors(t, durable, bridgeID, fence, 3, 3, 3, 2)

	// A stale full-fence attach must not replace the live stream.
	stale := fence
	stale.RuntimeGeneration++
	_, err = handler.Attach(ctx, stale)
	require.Error(t, err)
	third := `{"jsonrpc":"2.0","method":"echo","id":"third","params":{"value":3}}`
	thirdCanonical := `{"id":"third","jsonrpc":"2.0","method":"echo","params":{"value":3}}`
	thirdHash, err := managedbridge.HashPayload([]byte(third))
	require.NoError(t, err)
	_, err = durable.QueueManagedBridgeCommand(ctx, store.ManagedBridgeCommandInput{BridgeID: bridgeID, RequestID: "123e4567-e89b-42d3-a456-426614174203", Name: managedbridge.CommandRPC, Payload: []byte(third), PayloadHash: thirdHash, Fence: phase3AStoreFence(fence)})
	require.NoError(t, err)
	handler.Wake(config.Agent, bridgeID)
	awaitSignal(t, admitted)
	release <- struct{}{}
	require.Equal(t, thirdCanonical+"\n", readRPCLine(t, stream3, len(thirdCanonical)+1, nil))

	// The stream boundary rejects malformed, multiline, oversized, and
	// conflicting JSON-RPC records before another command is admitted.
	_, err = stream3.Write([]byte("{\n"))
	require.ErrorIs(t, err, managedbridge.ErrRPCStreamInvalid)
	_, err = stream3.Write([]byte(first + "\n" + second + "\n"))
	require.ErrorIs(t, err, managedbridge.ErrRPCStreamInvalid)
	_, err = stream3.Write(bytes.Repeat([]byte{'x'}, managedbridge.MaxRPCStreamRecordBytes+2))
	require.ErrorIs(t, err, managedbridge.ErrRPCStreamInvalid)
	// The stream derives request IDs from the complete payload, so a changed
	// payload with the same JSON-RPC ID is a distinct request. Exercise the
	// durable idempotency conflict with the original request ID directly.
	commands, err := durable.ReplayManagedBridgeCommands(ctx, bridgeID, 1, phase3AStoreFence(fence))
	require.NoError(t, err)
	require.Len(t, commands, 3)
	conflictPayload := []byte(`{"jsonrpc":"2.0","method":"echo","id":"same","params":{"value":9}}`)
	conflictHash, err := managedbridge.HashPayload(conflictPayload)
	require.NoError(t, err)
	_, err = durable.QueueManagedBridgeCommand(ctx, store.ManagedBridgeCommandInput{
		BridgeID: bridgeID, RequestID: commands[0].RequestID, Name: managedbridge.CommandRPC,
		Payload: conflictPayload, PayloadHash: conflictHash, Fence: phase3AStoreFence(fence),
	})
	require.ErrorIs(t, err, store.ErrManagedBridgeSequenceConflict)

	// The bridge endpoint is direct-loopback only, has no Origin support, and
	// the reverse proxy never exposes it. Existing health and UI paths remain
	// independently routed.
	health, err := http.Get(server.URL + "/v1/health")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, health.StatusCode)
	require.NoError(t, health.Body.Close())
	ui, err := http.Get(server.URL + "/v1/ui-live")
	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, ui.StatusCode)
	require.NoError(t, ui.Body.Close())
	unsafe := httptest.NewRequest(http.MethodGet, "/v1/managed-bridge", nil)
	unsafe.RemoteAddr = "192.0.2.1:1234"
	unsafe.Header.Set("Connection", "Upgrade")
	unsafe.Header.Set("Upgrade", "websocket")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, unsafe)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	originCtx, originCancel := context.WithTimeout(ctx, time.Second)
	_, originResponse, originErr := websocket.Dial(originCtx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/managed-bridge", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{"https://untrusted.example"}}})
	originCancel()
	require.Error(t, originErr)
	if originResponse != nil && originResponse.Body != nil {
		require.NoError(t, originResponse.Body.Close())
	}
	readDone := make(chan error, 1)
	go func() {
		_, readErr := stream3.Read(make([]byte, 1))
		readDone <- readErr
	}()
	closeCtx, closeCancel := context.WithTimeout(ctx, 2*time.Second)
	require.NoError(t, router.Close(closeCtx))
	closeCancel()
	select {
	case readErr := <-readDone:
		require.ErrorIs(t, readErr, io.EOF)
	case <-time.After(2 * time.Second):
		t.Fatal("RPC stream reader was not joined by Router.Close")
	}
	require.NoError(t, stream3.Close())
	cancel()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Phase1 bridge did not join")
	}
}

func awaitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("expected deterministic bridge signal")
	}
}

func readRPCLine(t *testing.T, reader io.Reader, length int, beforeFinal func()) string {
	t.Helper()
	result := make([]byte, 0, length)
	for len(result) < length-1 {
		want := length - 1 - len(result)
		if want > 5 {
			want = 5
		}
		chunk := make([]byte, want)
		n, err := reader.Read(chunk)
		require.NoError(t, err)
		require.Greater(t, n, 0)
		result = append(result, chunk[:n]...)
	}
	if beforeFinal != nil {
		beforeFinal()
	}
	last := make([]byte, 1)
	n, err := reader.Read(last)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	result = append(result, last...)
	return string(result)
}

func assertPhase3ACursors(t *testing.T, durable *store.Store, bridgeID string, fence managedbridge.RuntimeFence, commandNext, eventApplied, eventAdmitted, replayFrom uint64) {
	t.Helper()
	cursors, err := durable.ManagedBridgeCursors(context.Background(), bridgeID)
	require.NoError(t, err)
	require.Equal(t, commandNext, cursors.CommandNext)
	require.Equal(t, commandNext-1, cursors.CommandAcked)
	require.Equal(t, eventAdmitted, cursors.EventAdmittedNext)
	require.Equal(t, eventApplied, cursors.EventAppliedNext)
	events, err := durable.ReplayManagedBridgeEvents(context.Background(), bridgeID, replayFrom, phase3AStoreFence(fence))
	require.NoError(t, err)
	require.Len(t, events, 1)
}

func phase3AStoreFence(fence managedbridge.RuntimeFence) store.ManagedRuntimeFence {
	return store.ManagedRuntimeFence{AgentID: fence.Agent, PiSessionID: fence.PiSessionID, SessionEpoch: int64(fence.PiSessionEpoch), ProcessPID: int(fence.PiPID), ProcessStartMarker: fence.PiStartMarker, ProcessIncarnation: fence.PiIncarnation, RuntimeGeneration: strconv.FormatUint(fence.RuntimeGeneration, 10), ManifestRevision: int(fence.ManifestRevision), ManifestHash: fence.ManifestHash}
}
