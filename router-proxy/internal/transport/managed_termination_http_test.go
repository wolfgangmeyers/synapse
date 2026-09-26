package transport

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

type issue42ProcessOps struct {
	target  managed.ProcessIdentity
	state   managed.ProcessProbeState
	signals atomic.Int32
}

func (o *issue42ProcessOps) Probe(identity managed.ProcessIdentity) managed.ProcessProbe {
	if identity != o.target {
		return managed.ProcessProbe{State: managed.ProcessMismatch}
	}
	return managed.ProcessProbe{State: o.state}
}

func (o *issue42ProcessOps) CloseProcessInput(managed.Process) error { return nil }

func (o *issue42ProcessOps) SignalVerifiedProcessGroup(managed.ProcessIdentity, syscall.Signal) error {
	o.signals.Add(1)
	return syscall.ESRCH
}

func (o *issue42ProcessOps) SignalVerifiedProcessGroupAfterLeaderExit(managed.ProcessIdentity) error {
	o.signals.Add(1)
	return syscall.ESRCH
}

func (o *issue42ProcessOps) WaitForProcessGroupEmpty(context.Context, managed.ProcessIdentity, <-chan error) managed.ProcessProbe {
	return managed.ProcessProbe{State: o.state}
}

type issue42HTTPResponse struct {
	status  int
	body    []byte
	headers http.Header
	err     error
}

func TestManagedTerminateHTTPDeletesFailedRestartConcurrentlyAndRelaunches(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, databasePath, store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = durable.Close() })
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	root := t.TempDir()
	sessionRoot := filepath.Join(root, "sessions")
	require.NoError(t, os.MkdirAll(sessionRoot, 0o700))

	const createdAt = "2026-09-08T00:00:00.000Z"
	launch := store.ManagedOperation{OperationID: "issue42-launch", Method: http.MethodPost, CanonicalPath: "/v1/managed-agents", RequestID: "issue42-launch", CanonicalBodyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", State: "pending", ResponseStatus: http.StatusAccepted, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`), CreatedAt: createdAt}
	agent := store.ManagedAgentRecord{AgentID: "issue42-agent", Name: "issue42-agent", SlotHeld: true, Cwd: root, SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-sol"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "issue42-session", SessionEpoch: 1, RuntimeGeneration: "123e4567-e89b-42d3-a456-426614174905", ManifestRevision: 1, ManifestHash: manifest.CanonicalSHA256, CreatedAt: createdAt, UpdatedAt: createdAt}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, agent, launch))
	require.NoError(t, durable.ReserveManagedPiSession(ctx, store.ManagedPiSessionReservation{ReservationID: "issue42-reservation", AgentID: agent.AgentID, SessionEpoch: agent.SessionEpoch, LaunchOperationID: launch.OperationID, RootRelativePath: "issue42-stock.jsonl"}))
	targetIdentity := managed.ProcessIdentity{PID: 4242, StartMarker: "issue42-target-marker", Incarnation: "issue42-target-incarnation"}
	require.NoError(t, durable.UpdateManagedAgentStarting(ctx, agent.AgentID, agent.PiSessionID, store.ProcessIdentityRecord{PID: targetIdentity.PID, StartMarker: targetIdentity.StartMarker, Incarnation: targetIdentity.Incarnation}, agent.RuntimeGeneration))
	storedAgent, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	targetFence := store.ManagedRuntimeFence{AgentID: storedAgent.AgentID, PiSessionID: storedAgent.PiSessionID, SessionEpoch: storedAgent.SessionEpoch, ProcessPID: storedAgent.ProcessPID, ProcessStartMarker: storedAgent.ProcessStartMarker, ProcessIncarnation: storedAgent.ProcessIncarnation, RuntimeGeneration: storedAgent.RuntimeGeneration, ManifestRevision: storedAgent.ManifestRevision, ManifestHash: storedAgent.ManifestHash}
	require.NoError(t, durable.BindManagedPiSession(ctx, "issue42-reservation", launch.OperationID, agent.PiSessionID, "issue42-stock.jsonl", targetFence))
	require.NoError(t, durable.MarkManagedReady(ctx, agent.AgentID))
	require.NoError(t, durable.MarkManagedOperationTerminal(ctx, launch.OperationID))
	artifact := store.ManagedSessionArtifact{ArtifactID: "123e4567-e89b-42d3-a456-426614174901", AgentID: agent.AgentID, PiSessionID: agent.PiSessionID, SessionEpoch: 1, RootRelativePath: "issue42-artifact.jsonl", PathSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", DiscoveryState: "not_materialized", CleanupState: "retained", DiscoveredAt: createdAt, CleanupUpdatedAt: createdAt}
	require.NoError(t, durable.RecordManagedSessionArtifact(ctx, artifact))

	failedID := "123e4567-e89b-42d3-a456-426614174902"
	sourceFence := targetFence
	sourceFence.ProcessPID = 4141
	sourceFence.ProcessStartMarker = "issue42-source-marker"
	sourceFence.ProcessIncarnation = "issue42-source-incarnation"
	sourceFence.RuntimeGeneration = "123e4567-e89b-42d3-a456-426614174906"
	failedOperation := store.ManagedOperation{OperationID: failedID, Method: http.MethodPost, CanonicalPath: "/v1/managed-agents/issue42-agent/restart", RequestID: failedID, CanonicalBodyHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", State: "terminal", ResponseStatus: http.StatusAccepted, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{"operation_id":"` + failedID + `"}`), CreatedAt: createdAt, TerminalAt: createdAt, ExpiresAt: "2026-09-09T00:00:00.000Z"}
	require.NoError(t, durable.CreateManagedLifecycleAction(ctx, store.ManagedLifecycleActionInput{ActionID: failedID, AgentID: agent.AgentID, Kind: "restart", Source: "direct", ExecutionOwner: "lifecycle", State: "failed", Stage: "target_ready", SourceFence: &sourceFence, TargetFence: &targetFence, PlannedTargetRuntimeGeneration: targetFence.RuntimeGeneration, TerminalCode: "RESTART_TARGET_REAP_FAILED", TerminalMessage: "managed restart replacement process cleanup failed", Retryable: true, CreatedAt: createdAt, UpdatedAt: createdAt, Operation: failedOperation}))
	failedBefore, err := durable.ManagedLifecycleAction(ctx, failedID)
	require.NoError(t, err)

	cleanupEntered := make(chan struct{})
	cleanupRelease := make(chan struct{})
	var cleanupOnce sync.Once
	var cleanupCalls atomic.Int32
	processOps := &issue42ProcessOps{target: targetIdentity, state: managed.ProcessMismatch}
	newProcess := &managedTestProcess{identity: managed.ProcessIdentity{PID: 4343, StartMarker: "issue42-new-marker", Incarnation: "issue42-new-incarnation"}, stopped: make(chan struct{})}
	newSession := ""
	manager, err := managed.NewManager(managed.ManagerOptions{
		Store: durable, Config: managed.Config{SessionRoot: sessionRoot, AllowedWorkingDirectoryRoots: []string{root}, Limits: managed.Limits{GracefulStop: time.Millisecond, TermStop: time.Millisecond}}, Manifest: manifest,
		ProcessFactory: managedTestProcessFactory{process: newProcess, session: &newSession}, RPCFactory: managedTestRPCFactory{rpc: managedTestRPC{session: &newSession, active: expectedManagedActiveToolsForTransportTest(manifest)}},
		CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-sol", Available: true}), ProcessGroupLeaderVerifier: func(managed.ProcessIdentity) bool { return true }, ProcessGroupOps: processOps,
		BeforeManagedTerminationArtifactCleanup: func(context.Context, string) error {
			if cleanupCalls.Add(1) == 1 {
				close(cleanupEntered)
			}
			select {
			case <-cleanupRelease:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupOnce.Do(func() { close(cleanupRelease) })
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer closeCancel()
		_ = manager.Close(closeCtx)
	})
	router, err := New(Options{Store: durable, Managed: manager})
	require.NoError(t, err)
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer closeCancel()
		_ = router.Close(closeCtx)
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	requestID := "123e4567-e89b-42d3-a456-426614174903"
	start := make(chan struct{})
	ready := make(chan struct{}, 2)
	responses := make(chan issue42HTTPResponse, 2)
	for range 2 {
		go func() {
			ready <- struct{}{}
			<-start
			request, requestErr := http.NewRequestWithContext(ctx, http.MethodDelete, server.URL+"/v1/managed-agents/issue42-agent", nil)
			if requestErr != nil {
				responses <- issue42HTTPResponse{err: requestErr}
				return
			}
			request.Header.Set("X-Request-ID", requestID)
			response, requestErr := http.DefaultClient.Do(request)
			if requestErr != nil {
				responses <- issue42HTTPResponse{err: requestErr}
				return
			}
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			responses <- issue42HTTPResponse{status: response.StatusCode, body: body, headers: response.Header.Clone(), err: readErr}
		}()
	}
	<-ready
	<-ready
	close(start)
	first, second := <-responses, <-responses
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	require.Equal(t, http.StatusAccepted, first.status)
	require.Equal(t, first.status, second.status)
	require.Equal(t, first.body, second.body)
	require.Equal(t, requestID, first.headers.Get("X-Request-ID"))
	require.Equal(t, first.headers.Get("X-Request-ID"), second.headers.Get("X-Request-ID"))
	select {
	case <-cleanupEntered:
	case <-ctx.Done():
		t.Fatal("termination did not reach its cleanup barrier")
	}

	failedAfterAdmission, err := durable.ManagedLifecycleAction(ctx, failedID)
	require.NoError(t, err)
	require.Equal(t, failedBefore, failedAfterAdmission)
	database, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	defer database.Close()
	var operations, actions int
	require.NoError(t, database.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_http_operations WHERE method='DELETE' AND request_id=?`, requestID).Scan(&operations))
	require.NoError(t, database.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_lifecycle_actions WHERE agent_id=? AND kind='terminate'`, agent.AgentID).Scan(&actions))
	require.Equal(t, 1, operations)
	require.Equal(t, 1, actions)
	require.Zero(t, processOps.signals.Load())
	cleanupOnce.Do(func() { close(cleanupRelease) })

	require.Eventually(t, func() bool {
		action, actionErr := durable.ManagedLifecycleAction(ctx, requestID)
		_, agentErr := durable.ManagedAgent(ctx, agent.AgentID)
		return actionErr == nil && action.State == "succeeded" && errors.Is(agentErr, sql.ErrNoRows)
	}, 10*time.Second, time.Millisecond)
	require.Equal(t, int32(1), cleanupCalls.Load())
	var reservations, artifacts, foreignKeyFailures int
	require.NoError(t, database.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_stock_pi_session_reservations WHERE agent_id=?`, agent.AgentID).Scan(&reservations))
	require.NoError(t, database.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_session_artifacts WHERE agent_id=?`, agent.AgentID).Scan(&artifacts))
	require.NoError(t, database.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignKeyFailures))
	require.Zero(t, reservations)
	require.Zero(t, artifacts)
	require.Zero(t, foreignKeyFailures)
	terminationAction, err := durable.ManagedLifecycleAction(ctx, requestID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", terminationAction.State)
	require.NotEmpty(t, terminationAction.ArchiveKey)
	_, err = durable.ManagedAgentArchive(ctx, terminationAction.ArchiveKey)
	require.NoError(t, err)
	storedDelete, err := durable.FindManagedOperation(ctx, http.MethodDelete, requestID)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, storedDelete.ResponseStatus)
	require.Equal(t, first.body, storedDelete.ResponseBody)

	launchBody, err := json.Marshal(map[string]string{"name": agent.Name, "cwd": root})
	require.NoError(t, err)
	relaunch, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/managed-agents", bytes.NewReader(launchBody))
	require.NoError(t, err)
	relaunch.Header.Set("Content-Type", "application/json")
	relaunch.Header.Set("X-Request-ID", "123e4567-e89b-42d3-a456-426614174904")
	relaunchResponse, err := http.DefaultClient.Do(relaunch)
	require.NoError(t, err)
	defer relaunchResponse.Body.Close()
	require.Equal(t, http.StatusAccepted, relaunchResponse.StatusCode)
	require.Eventually(t, func() bool {
		replacement, readErr := durable.ManagedAgent(ctx, agent.AgentID)
		return readErr == nil && replacement.LifecycleState == "running" && replacement.RuntimeGeneration != targetFence.RuntimeGeneration
	}, 10*time.Second, time.Millisecond)
	require.NoError(t, database.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_stock_pi_session_reservations WHERE agent_id=?`, agent.AgentID).Scan(&reservations))
	require.Equal(t, 1, reservations)
}

func TestManagedTerminateHTTPIsStrictReplayableAndNoStore(t *testing.T) {
	server, durable := newManagedHTTPTestServer(t)
	ctx := context.Background()
	launch := store.ManagedOperation{OperationID: "launch-termination-http", Method: http.MethodPost, CanonicalPath: "/v1/managed-agents", RequestID: "launch-termination-http", CanonicalBodyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", State: "pending", ResponseStatus: http.StatusAccepted, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, store.ManagedAgentRecord{AgentID: "termination-http", Name: "termination-http", SlotHeld: true, Cwd: "/tmp", SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", ManifestRevision: 1, ManifestHash: "manifest"}, launch))

	request := func(id string, body []byte) (int, []byte, http.Header) {
		req, err := http.NewRequest(http.MethodDelete, server.URL+"/v1/managed-agents/termination-http", bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("X-Request-ID", id)
		response, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer response.Body.Close()
		payload, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		return response.StatusCode, payload, response.Header.Clone()
	}
	status, first, headers := request("123e4567-e89b-42d3-a456-426614174301", nil)
	require.Equal(t, http.StatusAccepted, status)
	require.Equal(t, "no-store", headers.Get("Cache-Control"))
	require.Equal(t, "123e4567-e89b-42d3-a456-426614174301", headers.Get("X-Request-ID"))
	status, replay, headers := request("123e4567-e89b-42d3-a456-426614174301", nil)
	require.Equal(t, http.StatusAccepted, status)
	require.Equal(t, first, replay)
	require.Equal(t, "no-store", headers.Get("Cache-Control"))

	bad, err := http.NewRequest(http.MethodDelete, server.URL+"/v1/managed-agents/termination-http", bytes.NewBufferString(`{}`))
	require.NoError(t, err)
	bad.Header.Set("X-Request-ID", "123e4567-e89b-42d3-a456-426614174302")
	response, err := http.DefaultClient.Do(bad)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusBadRequest, response.StatusCode)
	require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
}
