package store

import (
	"context"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/stretchr/testify/require"
)

// Identity transitions must be serialized independently of clocks and audit retention.
func TestAgentUrgencyIdentityReplacement(t *testing.T) {
	s, fence := readinessSourceFixture(t)
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Now().UTC().Add(3 * time.Minute))
	s.clock = clock
	_, err := s.SetAgentUrgency(ctx, fence.AgentID, true)
	require.NoError(t, err)
	require.NoError(t, s.ReserveManagedLaunch(ctx, ManagedAgentRecord{AgentID: "replacement-id", Name: fence.AgentID, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai", ID: "test"}, ManifestRevision: 1, ManifestHash: testManifestHash}, ManagedOperation{OperationID: "replacement-review", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "replacement-review", CanonicalBodyHash: "body", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}))
	var count int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM managed_agents WHERE agent_id=?`, fence.AgentID).Scan(&count))
	require.Zero(t, count)
	var urgent bool
	require.NoError(t, s.db.QueryRow(`SELECT urgent FROM agents WHERE agent_id=?`, fence.AgentID).Scan(&urgent))
	require.False(t, urgent)
	saved, err := s.SetAgentUrgency(ctx, fence.AgentID, true)
	t.Logf("retired replacement ID write: saved=%v error=%v", saved, err)
	require.ErrorIs(t, err, ErrAgentNotFound)
}

func TestAgentUrgencyIdentityAuditExpiry(t *testing.T) {
	s, ctx, agent, action := terminationActionAtDurableWrite(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	s.clock = deterministic.NewFixedClock(now)
	require.NoError(t, s.CompleteManagedTermination(ctx, action.ActionID))
	_, err := s.SetAgentUrgency(ctx, agent.AgentID, true)
	require.ErrorIs(t, err, ErrAgentNotFound)
	// Ordinary mail admission preserves the retained shared row; it is not a
	// legitimate managed admission or transport connection for this retired ID.
	_, err = s.Enqueue(ctx, protocol.Envelope{ID: "123e4567-e89b-42d3-a456-426614170991", From: "sender", To: agent.AgentID, Body: "retained unrelated ownership", CreatedAt: formatTimestamp(now)})
	require.NoError(t, err)
	_, err = s.SetAgentUrgency(ctx, agent.AgentID, true)
	require.ErrorIs(t, err, ErrAgentNotFound)
	// Keep accepted mailbox ownership beyond both audit horizons. This fixture
	// adjusts only mail expiry, never the reviewed identity predicate/evidence.
	_, err = s.db.Exec(`UPDATE messages SET expires_at=? WHERE recipient=?`, formatTimestamp(now.Add(60*24*time.Hour)), agent.AgentID)
	require.NoError(t, err)
	later := now.Add(31 * 24 * time.Hour)
	s.clock = deterministic.NewFixedClock(later)
	require.NoError(t, s.runImmediate(ctx, func() error {
		_, err := s.pruneStaleAgents(ctx, formatTimestamp(later))
		if err != nil {
			return err
		}
		_, err = s.sweepManagedOperations(ctx, formatTimestamp(later))
		if err != nil {
			return err
		}
		_, err = pruneManagedArchivesTx(ctx, s.db, later)
		return err
	}))
	var count int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM managed_lifecycle_actions WHERE agent_id=?`, agent.AgentID).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM managed_agent_archives WHERE agent_id=?`, agent.AgentID).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM agents WHERE agent_id=?`, agent.AgentID).Scan(&count))
	require.Equal(t, 1, count)
	saved, err := s.SetAgentUrgency(ctx, agent.AgentID, true)
	t.Logf("retired ID after real audit pruning: saved=%v error=%v", saved, err)
	require.ErrorIs(t, err, ErrAgentNotFound)
}

func TestAgentUrgencyIdentityEqualTimestampConnection(t *testing.T) {
	s, ctx, agent, action := terminationActionAtDurableWrite(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	clock := deterministic.NewFixedClock(now)
	s.clock = clock
	require.NoError(t, s.CompleteManagedTermination(ctx, action.ActionID))
	request := LeaseRequest{AgentID: agent.AgentID, InstanceID: "review-instance", LeaseID: "123e4567-e89b-42d3-a456-426614170992", LeaseDuration: time.Hour}
	lease, err := s.AcquireAgentLease(ctx, request)
	require.NoError(t, err)
	var completed string
	require.NoError(t, s.db.QueryRow(`SELECT completed_at FROM managed_lifecycle_actions WHERE action_id=?`, action.ActionID).Scan(&completed))
	require.Equal(t, completed, lease.AcquiredAt)
	snapshot, err := s.ConnectedAgentSnapshot(ctx, []string{agent.AgentID}, false)
	require.NoError(t, err)
	require.False(t, snapshot.Urgency[agent.AgentID])
	saved, err := s.SetAgentUrgency(ctx, agent.AgentID, true)
	t.Logf("new lease at completion time %s: saved=%v error=%v", completed, saved, err)
	// A renewal after wall time advances does not repair last_connected_at.
	require.NoError(t, clock.Advance(time.Second))
	_, renewErr := s.RenewAgentLease(ctx, request)
	require.NoError(t, renewErr)
	_, retryErr := s.SetAgentUrgency(ctx, agent.AgentID, true)
	t.Logf("after later renewal: error=%v", retryErr)
	require.NoError(t, err)
	require.True(t, saved)
}

func TestAgentUrgencyIdentityRenewalDoesNotReadmitRetiredManagedID(t *testing.T) {
	s, ctx, agent, action := terminationActionAtDurableWrite(t)
	request := LeaseRequest{AgentID: agent.AgentID, InstanceID: "old-instance", LeaseID: "123e4567-e89b-42d3-a456-426614170994", LeaseDuration: time.Hour}
	_, err := s.AcquireAgentLease(ctx, request)
	require.NoError(t, err)
	_, err = s.SetAgentUrgency(ctx, agent.AgentID, true)
	require.NoError(t, err)
	require.NoError(t, s.CompleteManagedTermination(ctx, action.ActionID))
	_, err = s.RenewAgentLease(ctx, request)
	require.NoError(t, err)
	_, err = s.SetAgentUrgency(ctx, agent.AgentID, true)
	require.ErrorIs(t, err, ErrAgentNotFound)
	// Acquisition is explicit admission, unlike an old connection's renewal.
	_, err = s.AcquireAgentLease(ctx, request)
	require.NoError(t, err)
	snapshot, err := s.ConnectedAgentSnapshot(ctx, []string{agent.AgentID}, false)
	require.NoError(t, err)
	require.False(t, snapshot.Urgency[agent.AgentID])
	_, err = s.SetAgentUrgency(ctx, agent.AgentID, true)
	require.NoError(t, err)
}

func TestAgentUrgencyIdentityManagedReadmissionStartsFalse(t *testing.T) {
	s, ctx, agent, action := terminationActionAtDurableWrite(t)
	_, err := s.SetAgentUrgency(ctx, agent.AgentID, true)
	require.NoError(t, err)
	require.NoError(t, s.CompleteManagedTermination(ctx, action.ActionID))
	urgencyLaunch(t, s, agent.AgentID, "readmit-retired-id")
	current, err := s.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.False(t, current.Urgent)
	_, err = s.SetAgentUrgency(ctx, agent.AgentID, true)
	require.NoError(t, err)
}
