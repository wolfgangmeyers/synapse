package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTimeoutStuckLaunchesImmunityForActiveAndProgressingAgents(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	now := s.clock.Now()
	threeMinsAgo := formatTimestamp(now.Add(-3 * time.Minute))
	thirtySecsAgo := formatTimestamp(now.Add(-30 * time.Second))

	// 1. Launch an agent in starting with created_at older than timeout (3 mins ago), but with activity_state = 'active'
	activeAgent := ManagedAgentRecord{
		AgentID:           "active-starting-agent",
		Name:              "active-starting-agent",
		SlotHeld:          true,
		Cwd:               "/tmp",
		SelectedModel:     ManagedModel{Provider: "antigravity", ID: "gemini-3.8-flash"},
		DesiredState:      "running",
		LifecycleState:    "starting",
		ActivityState:     "active",
		PiSessionID:       "session-active",
		SessionEpoch:      1,
		RuntimeGeneration: "gen-active",
		ManifestRevision:  1,
		ManifestHash:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Runtime:           "embedded",
		CreatedAt:         threeMinsAgo,
		UpdatedAt:         threeMinsAgo,
	}
	activeOp := ManagedOperation{
		OperationID:       "op-active-1",
		Method:            "POST",
		CanonicalPath:     "/v1/managed-agents",
		RequestID:         "op-active-1",
		CanonicalBodyHash: "hash-active",
		State:             "pending",
		ResponseStatus:    202,
		ResponseBody:      []byte(`{}`),
		CreatedAt:         threeMinsAgo,
	}
	require.NoError(t, s.ReserveManagedEmbeddedLaunch(ctx, activeAgent, activeOp, 32, "worker"))
	// Overwrite timestamps and activity_state to simulate pre-existing active starting agent
	_, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET activity_state = 'active', created_at = ?, updated_at = ? WHERE agent_id = ?`, threeMinsAgo, threeMinsAgo, "active-starting-agent")
	require.NoError(t, err)

	// 2. Launch another agent in starting with created_at older than timeout (3 mins ago), but with updated_at recent (30s ago)
	progressingAgent := ManagedAgentRecord{
		AgentID:           "progressing-starting-agent",
		Name:              "progressing-starting-agent",
		SlotHeld:          true,
		Cwd:               "/tmp",
		SelectedModel:     ManagedModel{Provider: "antigravity", ID: "gemini-3.8-flash"},
		DesiredState:      "running",
		LifecycleState:    "starting",
		ActivityState:     "starting",
		PiSessionID:       "session-progressing",
		SessionEpoch:      1,
		RuntimeGeneration: "gen-progressing",
		ManifestRevision:  1,
		ManifestHash:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Runtime:           "embedded",
		CreatedAt:         threeMinsAgo,
		UpdatedAt:         thirtySecsAgo,
	}
	progressingOp := ManagedOperation{
		OperationID:       "op-progressing-1",
		Method:            "POST",
		CanonicalPath:     "/v1/managed-agents",
		RequestID:         "op-progressing-1",
		CanonicalBodyHash: "hash-progressing",
		State:             "pending",
		ResponseStatus:    202,
		ResponseBody:      []byte(`{}`),
		CreatedAt:         threeMinsAgo,
	}
	require.NoError(t, s.ReserveManagedEmbeddedLaunch(ctx, progressingAgent, progressingOp, 32, "worker"))
	_, err = s.db.ExecContext(ctx, `UPDATE managed_agents SET created_at = ?, updated_at = ? WHERE agent_id = ?`, threeMinsAgo, thirtySecsAgo, "progressing-starting-agent")
	require.NoError(t, err)

	// 3. Launch a third agent in starting with created_at and updated_at older than timeout and activity_state = 'starting' (stuck)
	stuckAgent := ManagedAgentRecord{
		AgentID:           "stuck-starting-agent",
		Name:              "stuck-starting-agent",
		SlotHeld:          true,
		Cwd:               "/tmp",
		SelectedModel:     ManagedModel{Provider: "antigravity", ID: "gemini-3.8-flash"},
		DesiredState:      "running",
		LifecycleState:    "starting",
		ActivityState:     "starting",
		PiSessionID:       "session-stuck-3",
		SessionEpoch:      1,
		RuntimeGeneration: "gen-stuck-3",
		ManifestRevision:  1,
		ManifestHash:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Runtime:           "embedded",
		CreatedAt:         threeMinsAgo,
		UpdatedAt:         threeMinsAgo,
	}
	stuckOp := ManagedOperation{
		OperationID:       "op-stuck-3",
		Method:            "POST",
		CanonicalPath:     "/v1/managed-agents",
		RequestID:         "op-stuck-3",
		CanonicalBodyHash: "hash-stuck-3",
		State:             "pending",
		ResponseStatus:    202,
		ResponseBody:      []byte(`{}`),
		CreatedAt:         threeMinsAgo,
	}
	require.NoError(t, s.ReserveManagedEmbeddedLaunch(ctx, stuckAgent, stuckOp, 32, "worker"))
	_, err = s.db.ExecContext(ctx, `UPDATE managed_agents SET created_at = ?, updated_at = ? WHERE agent_id = ?`, threeMinsAgo, threeMinsAgo, "stuck-starting-agent")
	require.NoError(t, err)

	// 4. Call TimeoutStuckLaunches(ctx, 2*time.Minute)
	timedOutCount, err := s.TimeoutStuckLaunches(ctx, 2*time.Minute)
	require.NoError(t, err)
	require.Equal(t, 1, timedOutCount)

	// 5. Assert that only the third (stuck) agent timed out and failed. The first two remain in starting.
	activeRec, err := s.ManagedAgent(ctx, "active-starting-agent")
	require.NoError(t, err)
	require.Equal(t, "starting", activeRec.LifecycleState)
	require.Equal(t, "active", activeRec.ActivityState)

	progressingRec, err := s.ManagedAgent(ctx, "progressing-starting-agent")
	require.NoError(t, err)
	require.Equal(t, "starting", progressingRec.LifecycleState)
	require.Equal(t, "starting", progressingRec.ActivityState)

	stuckRec, err := s.ManagedAgent(ctx, "stuck-starting-agent")
	require.NoError(t, err)
	require.Equal(t, "failed", stuckRec.LifecycleState)
	require.Equal(t, "failed", stuckRec.ActivityState)
	require.Equal(t, "LAUNCH_TIMEOUT", stuckRec.LastErrorCode)

	stuckOpRec, err := s.ManagedOperation(ctx, "op-stuck-3")
	require.NoError(t, err)
	require.Equal(t, "terminal", stuckOpRec.State)
}

func TestSubagentPrefixRootAgentSnapshotAndListing(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// 1. Root agent with "subagent-" prefix in name (like "subagent-max-age")
	rootName := "subagent-max-age"
	rootAgent := ManagedAgentRecord{
		AgentID:           rootName,
		Name:              rootName,
		SlotHeld:          true,
		Cwd:               "/tmp",
		SelectedModel:     ManagedModel{Provider: "google-vertex", ID: "gemini-3.8-flash"},
		DesiredState:      "running",
		LifecycleState:    "running",
		ActivityState:     "idle",
		PiSessionID:       "session-root",
		SessionEpoch:      1,
		RuntimeGeneration: "gen-root",
		ManifestRevision:  1,
		ManifestHash:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Runtime:           "embedded",
		CreatedAt:         formatTimestamp(s.clock.Now()),
		UpdatedAt:         formatTimestamp(s.clock.Now()),
	}
	rootOp := ManagedOperation{
		OperationID:       "op-" + rootName,
		Method:            "POST",
		CanonicalPath:     "/v1/managed-agents",
		RequestID:         "op-" + rootName,
		CanonicalBodyHash: "hash-root",
		State:             "pending",
		ResponseStatus:    202,
		ResponseBody:      []byte(`{}`),
		CreatedAt:         formatTimestamp(s.clock.Now()),
	}
	require.NoError(t, s.ReserveManagedEmbeddedLaunch(ctx, rootAgent, rootOp, 32, "worker"))

	// 2. Child native subagent with parent_agent_id
	parentBinding := EmbeddedBinding{
		AgentID:    rootName,
		SessionID:  rootAgent.PiSessionID,
		Epoch:      1,
		Generation: rootAgent.RuntimeGeneration,
	}
	childID := "subagent-max-age-child-1"
	childRecord := ManagedSubagentRecord{
		AgentID:       childID,
		ParentAgentID: rootName,
		Status:        "queued",
		MaxTurns:      5,
		SubagentType:  "general-purpose",
		Description:   "explore task",
	}
	require.NoError(t, s.CreateManagedSubagent(ctx, parentBinding, childRecord))

	// Verify ManagedAgents returns the root agent but NOT the child subagent
	agents, err := s.ManagedAgents(ctx)
	require.NoError(t, err)
	require.Len(t, agents, 1)
	require.Equal(t, rootName, agents[0].AgentID)
	require.Equal(t, rootName, agents[0].Name)

	// Verify ManagedAgentsProjectionSnapshot includes root agent
	snapshot, err := s.ManagedAgentsProjectionSnapshot(ctx)
	require.NoError(t, err)
	require.Len(t, snapshot.Agents, 1)
	require.Equal(t, rootName, snapshot.Agents[0].AgentID)

	// Verify ManagedAgentSnapshot finds the root agent
	single, err := s.ManagedAgentSnapshot(ctx, rootName)
	require.NoError(t, err)
	require.NotNil(t, single.Agent)
	require.Equal(t, rootName, single.Agent.AgentID)

	// Verify child subagent is not accessible via top-level ManagedAgentSnapshot
	childSingle, err := s.ManagedAgentSnapshot(ctx, childID)
	require.NoError(t, err)
	require.Nil(t, childSingle.Agent)
}

func TestStuckLaunchTimeoutAndRelaunchOverwrite(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	agentName := "stuck-launch-agent"
	now := s.clock.Now()

	// 1. Create a launch stuck in 'starting' older than 2 minutes
	stuckTime := formatTimestamp(now.Add(-3 * time.Minute))
	stuckAgent := ManagedAgentRecord{
		AgentID:           agentName,
		Name:              agentName,
		SlotHeld:          true,
		Cwd:               "/tmp",
		SelectedModel:     ManagedModel{Provider: "antigravity", ID: "gemini-3.8-flash"},
		DesiredState:      "running",
		LifecycleState:    "starting",
		ActivityState:     "starting",
		PiSessionID:       "session-stuck",
		SessionEpoch:      1,
		RuntimeGeneration: "gen-stuck",
		ManifestRevision:  1,
		ManifestHash:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Runtime:           "embedded",
		CreatedAt:         stuckTime,
		UpdatedAt:         stuckTime,
	}
	stuckOp := ManagedOperation{
		OperationID:       "op-stuck-1",
		Method:            "POST",
		CanonicalPath:     "/v1/managed-agents",
		RequestID:         "op-stuck-1",
		CanonicalBodyHash: "hash-stuck",
		State:             "pending",
		ResponseStatus:    202,
		ResponseBody:      []byte(`{}`),
		CreatedAt:         stuckTime,
	}
	require.NoError(t, s.ReserveManagedEmbeddedLaunch(ctx, stuckAgent, stuckOp, 32, "worker"))

	// 2. Run TimeoutStuckLaunches sweep
	timedOutCount, err := s.TimeoutStuckLaunches(ctx, 2*time.Minute)
	require.NoError(t, err)
	require.Equal(t, 1, timedOutCount)

	// Verify the agent transitioned to failed with LAUNCH_TIMEOUT diagnostic
	record, err := s.ManagedAgent(ctx, agentName)
	require.NoError(t, err)
	require.Equal(t, "failed", record.LifecycleState)
	require.Equal(t, "failed", record.ActivityState)
	require.False(t, record.SlotHeld)
	require.Equal(t, "LAUNCH_TIMEOUT", record.LastErrorCode)
	require.Contains(t, record.LastErrorMessage, "timed out")

	// Verify the operation was terminalized
	op, err := s.ManagedOperation(ctx, "op-stuck-1")
	require.NoError(t, err)
	require.Equal(t, "terminal", op.State)

	// 3. Test re-launch ability: Re-launching the failed agent with the same name must succeed (overwriting the failed record)
	freshTime := formatTimestamp(now)
	freshAgent := ManagedAgentRecord{
		AgentID:           agentName,
		Name:              agentName,
		SlotHeld:          true,
		Cwd:               "/tmp",
		SelectedModel:     ManagedModel{Provider: "antigravity", ID: "gemini-3.8-flash"},
		DesiredState:      "running",
		LifecycleState:    "starting",
		ActivityState:     "starting",
		PiSessionID:       "session-fresh",
		SessionEpoch:      1,
		RuntimeGeneration: "gen-fresh",
		ManifestRevision:  1,
		ManifestHash:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Runtime:           "embedded",
		CreatedAt:         freshTime,
		UpdatedAt:         freshTime,
	}
	freshOp := ManagedOperation{
		OperationID:       "op-fresh-2",
		Method:            "POST",
		CanonicalPath:     "/v1/managed-agents",
		RequestID:         "op-fresh-2",
		CanonicalBodyHash: "hash-fresh",
		State:             "pending",
		ResponseStatus:    202,
		ResponseBody:      []byte(`{}`),
		CreatedAt:         freshTime,
	}
	require.NoError(t, s.ReserveManagedEmbeddedLaunch(ctx, freshAgent, freshOp, 32, "worker"))

	// Verify the agent is now fresh in 'starting'
	freshRecord, err := s.ManagedAgent(ctx, agentName)
	require.NoError(t, err)
	require.Equal(t, "starting", freshRecord.LifecycleState)
	require.Equal(t, "op-fresh-2", freshRecord.LaunchOperationID)
	require.True(t, freshRecord.SlotHeld)

	// 4. Verify an active running agent cannot be overwritten
	_, _ = s.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state='running', activity_state='idle' WHERE agent_id=?`, agentName)
	conflictAgent := freshAgent
	conflictAgent.PiSessionID = "session-conflict"
	conflictOp := ManagedOperation{
		OperationID:       "op-conflict-3",
		Method:            "POST",
		CanonicalPath:     "/v1/managed-agents",
		RequestID:         "op-conflict-3",
		CanonicalBodyHash: "hash-conflict",
		State:             "pending",
		ResponseStatus:    202,
		ResponseBody:      []byte(`{}`),
		CreatedAt:         freshTime,
	}
	var nameConflict *ManagedNameConflictError
	conflictErr := s.ReserveManagedEmbeddedLaunch(ctx, conflictAgent, conflictOp, 32, "worker")
	require.ErrorAs(t, conflictErr, &nameConflict)
}
