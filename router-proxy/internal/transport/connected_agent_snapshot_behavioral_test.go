package transport

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestConnectedAgentsBatchesActiveLeasesAndDurableLifecycleFallback(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC))
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	router, err := New(Options{Store: durable, Clock: clock})
	require.NoError(t, err)
	defer router.Close(context.Background())

	for index := 0; index < 29; index++ {
		agentID := fmt.Sprintf("agent-%02d", index)
		epoch := fmt.Sprintf("123e4567-e89b-42d3-a456-426614174%03d", index)
		candidate := store.LifecycleCandidate{
			ParentAgentID: agentID, InstanceID: "instance-" + agentID, ParentSessionID: "session-" + agentID,
			LeaseID: "lease-" + agentID, ProducerEpoch: epoch, ClientSequence: 1,
			EventID: "snapshot-" + agentID, Snapshot: true,
		}
		if index == 28 {
			candidate.Subagents = []store.LifecycleChild{{ID: "running-child", Type: "worker", Description: "active child", Status: "running"}}
		}
		_, err := durable.ApplySubagentLifecycle(context.Background(), candidate, false)
		require.NoError(t, err)
		lease, err := durable.AcquireAgentLease(context.Background(), store.LeaseRequest{
			AgentID: agentID, InstanceID: candidate.InstanceID, LeaseID: candidate.LeaseID, LeaseDuration: time.Minute,
		})
		require.NoError(t, err)
		router.sessions[agentID] = &session{
			router: router, agentID: agentID, instanceID: candidate.InstanceID,
			deliveryInstance: candidate.InstanceID, leaseID: lease.LeaseID, role: "worker",
			closeOverride: func(websocket.StatusCode, string) error { return nil },
		}
	}

	snapshot := router.connectedAgents(context.Background(), false)
	agents := snapshot["agents"].([]map[string]any)
	require.Len(t, agents, 29)
	for index, item := range agents {
		agentID := item["agent_id"].(string)
		if agentID == "agent-28" {
			require.Equal(t, 1, item["subagent_count"])
			require.Equal(t, []map[string]any{{"id": "running-child", "type": "worker", "description": "active child", "status": "running"}}, item["subagents"])
			continue
		}
		require.Equal(t, fmt.Sprintf("agent-%02d", index), agentID)
		require.Equal(t, 0, item["subagent_count"])
		require.Empty(t, item["subagents"])
	}
}
