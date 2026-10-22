package managed

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedWorkerListTmuxIntegration(t *testing.T) {
	ctx := context.Background()
	m, db, provider, caller, root := requiredToolsFixture(t, "root")

	// 1. Register an embedded worker directly in store
	embeddedWorkerName := "embedded-worker-charlie"
	agent := store.ManagedAgentRecord{
		AgentID:           embeddedWorkerName,
		Name:              embeddedWorkerName,
		SlotHeld:          true,
		Cwd:               root,
		SelectedModel:     store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		Runtime:           "embedded",
		DesiredState:      "running",
		LifecycleState:    "running",
		ActivityState:     "idle",
		PiSessionID:       embeddedWorkerName + "-session",
		SessionEpoch:      1,
		RuntimeGeneration: "gen-1",
		ManifestRevision:  1,
		ManifestHash:      strings.Repeat("a", 64),
	}
	op := store.ManagedOperation{
		OperationID:       embeddedWorkerName + "-launch",
		Method:            "POST",
		CanonicalPath:     "/v1/managed-agents",
		RequestID:         embeddedWorkerName + "-launch",
		CanonicalBodyHash: strings.Repeat("a", 64),
		ResponseStatus:    202,
	}
	require.NoError(t, db.ReserveManagedEmbeddedLaunch(ctx, agent, op, 32, "worker"))

	// 2. Register a tmux worker in recipient_sources and acquire an active lease
	tmuxWorkerName := "tmux-worker-delta"
	tmuxSourceID := "tmux-source-v1:" + tmuxWorkerName
	_, err := db.ReserveTmuxRecipientSource(ctx, tmuxWorkerName, tmuxSourceID)
	require.NoError(t, err)
	require.NoError(t, db.ActivateTmuxRecipientSource(ctx, tmuxWorkerName, tmuxSourceID))

	// Acquire lease for tmuxWorkerName so it has lifecycle_state = 'running'
	_, err = db.AcquireAgentLease(ctx, store.LeaseRequest{
		AgentID:       tmuxWorkerName,
		InstanceID:    "inst-" + tmuxWorkerName,
		LeaseID:       "lease-" + tmuxWorkerName,
		LeaseDuration: 30 * time.Second,
	})
	require.NoError(t, err)

	// Register another tmux worker without an active lease (should report lifecycle_state = 'idle')
	idleTmuxWorkerName := "tmux-worker-idle"
	idleTmuxSourceID := "tmux-source-v1:" + idleTmuxWorkerName
	_, err = db.ReserveTmuxRecipientSource(ctx, idleTmuxWorkerName, idleTmuxSourceID)
	require.NoError(t, err)
	require.NoError(t, db.ActivateTmuxRecipientSource(ctx, idleTmuxWorkerName, idleTmuxSourceID))

	// 3. Register a reserved / non-worker source (e.g. 'supervisor') which should be filtered out
	supervisorSourceID := "tmux-source-v1:supervisor"
	_, err = db.ReserveTmuxRecipientSource(ctx, "supervisor", supervisorSourceID)
	require.NoError(t, err)
	require.NoError(t, db.ActivateTmuxRecipientSource(ctx, "supervisor", supervisorSourceID))

	// 4. Call mecha_worker action: "list" via real SDK requiredToolsCall
	callID := "test-worker-list-tmux-call"
	result := requiredToolsCall(t, m, db, provider, caller, "mecha_worker", callID, map[string]any{
		"action": "list",
	})

	// 5. Inspect returned result
	require.False(t, result.IsError)
	payload := requiredToolsPayload(t, result)
	require.Contains(t, payload, "workers")

	type workerEntry struct {
		WorkerID       string `json:"worker_id"`
		Kind           string `json:"kind"`
		LifecycleState string `json:"lifecycle_state"`
		DesiredState   string `json:"desired_state"`
	}

	var workers []workerEntry
	require.NoError(t, json.Unmarshal(payload["workers"], &workers))

	foundMap := make(map[string]workerEntry)
	for _, w := range workers {
		foundMap[w.WorkerID] = w
	}

	// Verify embedded worker exists with kind: "embedded"
	embeddedEntry, found := foundMap[embeddedWorkerName]
	require.True(t, found, "embedded worker must be present in workers list")
	require.Equal(t, "embedded", embeddedEntry.Kind)
	require.Equal(t, "running", embeddedEntry.LifecycleState)

	// Verify running tmux worker exists with kind: "tmux" and lifecycle_state: "running"
	tmuxEntry, found := foundMap[tmuxWorkerName]
	require.True(t, found, "active tmux worker must be present in workers list")
	require.Equal(t, "tmux", tmuxEntry.Kind)
	require.Equal(t, "running", tmuxEntry.LifecycleState)

	// Verify idle tmux worker exists with kind: "tmux" and lifecycle_state: "idle"
	idleTmuxEntry, found := foundMap[idleTmuxWorkerName]
	require.True(t, found, "idle tmux worker must be present in workers list")
	require.Equal(t, "tmux", idleTmuxEntry.Kind)
	require.Equal(t, "idle", idleTmuxEntry.LifecycleState)

	// Verify reserved sources (supervisor, etc.) are NOT in workers
	_, supervisorFound := foundMap["supervisor"]
	require.False(t, supervisorFound, "reserved identity 'supervisor' must not be in workers list")
	_, callerFound := foundMap[caller]
	require.False(t, callerFound, "root caller must not be listed as worker")
}
