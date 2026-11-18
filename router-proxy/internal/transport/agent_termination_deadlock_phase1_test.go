package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

// TestHealthCheckUnderRouterMuLockContention verifies that GET /v1/health
// and readiness checks do not block even when r.mu is held by a long-running writer.
func TestHealthCheckUnderRouterMuLockContention(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()

	router, err := New(Options{Store: durable})
	require.NoError(t, err)

	// Artificially hold r.mu.Lock() in a separate goroutine
	lockAcquired := make(chan struct{})
	releaseLock := make(chan struct{})

	go func() {
		router.mu.Lock()
		close(lockAcquired)
		<-releaseLock
		router.mu.Unlock()
	}()

	<-lockAcquired
	defer close(releaseLock)

	// Verify readiness() does not block while r.mu is locked
	readinessCh := make(chan string, 1)
	go func() {
		readinessCh <- router.readiness()
	}()

	select {
	case status := <-readinessCh:
		require.Equal(t, "ok", status)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("router.readiness() deadlocked while router.mu was locked")
	}

	// Verify ServeHTTP with /v1/health does not block on r.readiness()
	// Note: /v1/health calls r.store.MigrationIDs() when ready, which does not acquire r.mu.
	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	rec := httptest.NewRecorder()

	servedCh := make(chan struct{}, 1)
	go func() {
		router.ServeHTTP(rec, req)
		close(servedCh)
	}()

	select {
	case <-servedCh:
		require.Equal(t, http.StatusOK, rec.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, "ok", body["status"])
	case <-time.After(1 * time.Second):
		t.Fatal("ServeHTTP /v1/health deadlocked while router.mu was locked")
	}
}

// TestWriteManagedRawDoesNotBlockOnSlowUILive verifies that writeManagedRaw flushes
// immediately even if uiLive.publish takes a long time.
func TestWriteManagedRawDoesNotBlockOnSlowUILive(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()

	router, err := New(Options{Store: durable})
	require.NoError(t, err)

	// Block uiLive.publishGate to simulate a stuck/slow uiLive publisher
	router.uiLive.publishGate <- struct{}{}
	defer func() {
		select {
		case <-router.uiLive.publishGate:
		default:
		}
	}()

	req := httptest.NewRequest(http.MethodGet, "/v1/managed-agents", nil)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	start := time.Now()
	go func() {
		router.writeManagedJSON(rec, req, http.StatusOK, map[string]any{"ok": true}, nil)
		close(done)
	}()

	select {
	case <-done:
		elapsed := time.Since(start)
		require.Less(t, elapsed, 500*time.Millisecond, "writeManagedJSON should return immediately without waiting for uiLive.publish")
		require.Equal(t, http.StatusOK, rec.Code)
	case <-time.After(1 * time.Second):
		t.Fatal("writeManagedRaw blocked on slow uiLive.publish")
	}
}

// TestGoalControlAndResultDecoupledFromRouterMu verifies goalControl and goalControlResult
// execute without deadlocking or holding r.mu across store calls.
func TestGoalControlAndResultDecoupledFromRouterMu(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()

	router, err := New(Options{Store: durable})
	require.NoError(t, err)
	router.goalAvailable = true

	// Create source session (user_interaction) and target session
	sourceSent := make(chan map[string]any, 10)
	targetSent := make(chan map[string]any, 10)

	sourceSession := &session{
		router:           router,
		agentID:          "user-agent",
		instanceID:       "user-inst-1",
		deliveryInstance: "user-inst-1",
		leaseID:          "user-lease-1",
		uiLease:          true,
		uiConnectedAt:    time.Now().UTC(),
		uiExpiresAt:      time.Now().UTC().Add(10 * time.Minute),
		role:             "user_interaction",
		sendOverride: func(msg map[string]any) error {
			sourceSent <- msg
			return nil
		},
	}
	targetSession := &session{
		router:           router,
		agentID:          "worker-agent",
		instanceID:       "worker-inst-1",
		deliveryInstance: "worker-inst-1",
		leaseID:          "worker-lease-1",
		role:             "worker",
		sendOverride: func(msg map[string]any) error {
			targetSent <- msg
			return nil
		},
	}

	router.mu.Lock()
	router.uiSessions[sourceSession] = struct{}{}
	router.sessions[targetSession.agentID] = targetSession
	router.mu.Unlock()

	// Acquire durable lease for target
	_, err = durable.AcquireAgentLease(ctx, store.LeaseRequest{
		AgentID:       targetSession.agentID,
		InstanceID:    targetSession.instanceID,
		LeaseID:       targetSession.leaseID,
		LeaseDuration: 10 * time.Minute,
	})
	require.NoError(t, err)

	// Execute goalControl from source to target
	goalControlFrame := map[string]json.RawMessage{
		"frame_id":           json.RawMessage(`"frame-1"`),
		"agent_id":           json.RawMessage(`"user-agent"`),
		"instance_id":        json.RawMessage(`"user-inst-1"`),
		"lease_id":           json.RawMessage(`"user-lease-1"`),
		"control_id":         json.RawMessage(`"ctrl-1"`),
		"target_agent_id":    json.RawMessage(`"worker-agent"`),
		"target_instance_id": json.RawMessage(`"worker-inst-1"`),
		"action":             json.RawMessage(`"set"`),
		"goal":               json.RawMessage(`"find deadlock"`),
	}

	err = router.goalControl(sourceSession, goalControlFrame)
	require.NoError(t, err)

	// Verify target received goal_control frame
	select {
	case msg := <-targetSent:
		require.Equal(t, "goal_control", msg["type"])
		require.Equal(t, "ctrl-1", msg["control_id"])
		require.Equal(t, int64(1), msg["revision"])
	default:
		t.Fatal("target session did not receive goal_control frame")
	}

	// Verify store has the desired goal control
	desired, err := durable.GoalControlDesiredFor(ctx, targetSession.agentID, targetSession.instanceID)
	require.NoError(t, err)
	require.NotNil(t, desired)
	require.Equal(t, int64(1), desired.Revision)

	// Now execute goalControlResult from target session
	resultFrame := map[string]json.RawMessage{
		"frame_id":        json.RawMessage(`"frame-2"`),
		"agent_id":        json.RawMessage(`"worker-agent"`),
		"instance_id":     json.RawMessage(`"worker-inst-1"`),
		"lease_id":        json.RawMessage(`"worker-lease-1"`),
		"source_agent_id": json.RawMessage(`"user-agent"`),
		"control_id":      json.RawMessage(`"ctrl-1"`),
		"revision":        json.RawMessage(`1`),
		"accepted":        json.RawMessage(`true`),
		"goal_state":      json.RawMessage(`{"enabled":true,"goal":"find deadlock"}`),
	}

	err = router.goalControlResult(targetSession, resultFrame)
	require.NoError(t, err)

	// Verify source received goal_control_result frame
	select {
	case msg := <-sourceSent:
		require.Equal(t, "goal_control_result", msg["type"])
		require.Equal(t, "ctrl-1", msg["control_id"])
		require.Equal(t, int64(1), msg["revision"])
		require.Equal(t, true, msg["accepted"])
	default:
		t.Fatal("source session did not receive goal_control_result frame")
	}

	// Verify desired was consumed in store
	desiredAfter, err := durable.GoalControlDesiredFor(ctx, targetSession.agentID, targetSession.instanceID)
	require.NoError(t, err)
	require.Nil(t, desiredAfter)
}
