package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConnectedAgentSnapshotBatches29AgentsAndPreservesLifecycleProjection(t *testing.T) {
	durable := openTestStore(t)
	defer durable.Close()
	ctx := context.Background()
	ids := make([]string, 0, 29)
	for index := 0; index < 29; index++ {
		agentID := fmt.Sprintf("agent-%02d", index)
		ids = append(ids, agentID)
		require.NoError(t, execTestData(durable, `INSERT INTO agents (agent_id, first_seen_at, last_connected_at) VALUES (?, ?, ?)`,
			agentID, "2026-09-18T12:00:00.000Z", "2026-09-18T12:00:00.000Z"))
		require.NoError(t, execTestData(durable, `INSERT INTO agent_leases (lease_id, agent_id, instance_id, acquired_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
			"lease-"+agentID, agentID, "instance-"+agentID, "2026-09-18T12:00:00.000Z", "2026-09-18T12:01:00.000Z"))
		require.NoError(t, execTestData(durable, `INSERT INTO subagent_lifecycle_producers (
			parent_agent_id, instance_id, parent_session_id, producer_epoch, lease_id,
			highest_client_sequence, authority, last_seen_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, 'v2', ?, ?, ?);`,
			agentID, "instance-"+agentID, "session-"+agentID, fmt.Sprintf("123e4567-e89b-42d3-a456-426614174%03d", index),
			"lease-"+agentID, index+1, "2026-09-18T12:00:00.000Z", "2026-09-18T12:00:00.000Z", "2026-09-18T12:00:00.000Z"))
		if index < 28 {
			require.NoError(t, execTestData(durable, `INSERT INTO subagent_lifecycle_state (
				parent_agent_id, subagent_id, instance_id, parent_session_id, producer_epoch,
				type, description, status, state, updated_at, terminal_at, terminal_reason, router_version
			) VALUES (?, ?, ?, ?, ?, 'worker', 'finished', 'completed', 'terminal', ?, ?, 'completed', ?);`,
				agentID, "child-"+agentID, "instance-"+agentID, "session-"+agentID,
				fmt.Sprintf("123e4567-e89b-42d3-a456-426614174%03d", index),
				"2026-09-18T12:00:00.000Z", "2026-09-18T12:00:01.000Z", index+1))
		}
	}
	require.NoError(t, execTestData(durable, `INSERT INTO subagent_lifecycle_state (
		parent_agent_id, subagent_id, instance_id, parent_session_id, producer_epoch,
		type, description, status, state, updated_at, router_version
	) VALUES ('agent-28', 'running-child', 'instance-agent-28', 'session-agent-28',
		'123e4567-e89b-42d3-a456-426614174028', 'worker', 'active child', 'running', 'running', ?, 100);`,
		"2026-09-18T12:00:02.000Z"))

	snapshot, err := durable.ConnectedAgentSnapshot(ctx, ids, true)
	require.NoError(t, err)
	require.Len(t, snapshot.Leases, 29)
	require.Len(t, snapshot.Lifecycle, 29)
	for index := 0; index < 28; index++ {
		entry := snapshot.Lifecycle[ids[index]]
		require.Empty(t, entry.Agent.Subagents, ids[index])
		require.Equal(t, int64(index+1), entry.Agent.SubagentVersion, ids[index])
	}
	last := snapshot.Lifecycle["agent-28"]
	require.Equal(t, int64(100), last.Agent.SubagentVersion)
	require.Equal(t, []LifecycleChild{{ID: "running-child", Type: "worker", Description: "active child", Status: "running"}}, last.Agent.Subagents)
}

func BenchmarkConnectedAgentSnapshot29(b *testing.B) {
	durable, err := Open(context.Background(), filepath.Join(b.TempDir(), "router.db"), Options{})
	if err != nil {
		b.Fatal(err)
	}
	defer durable.Close()
	ids := make([]string, 29)
	for index := range ids {
		ids[index] = fmt.Sprintf("agent-%02d", index)
		if _, err := durable.db.Exec(`INSERT INTO agents (agent_id, first_seen_at) VALUES (?, ?)`, ids[index], "2026-09-18T12:00:00.000Z"); err != nil {
			b.Fatal(err)
		}
		if _, err := durable.db.Exec(`INSERT INTO agent_leases (lease_id, agent_id, instance_id, acquired_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
			"lease-"+ids[index], ids[index], "instance-"+ids[index], "2026-09-18T12:00:00.000Z", "2026-09-18T12:01:00.000Z"); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := durable.ConnectedAgentSnapshot(context.Background(), ids, true); err != nil {
			b.Fatal(err)
		}
	}
}
