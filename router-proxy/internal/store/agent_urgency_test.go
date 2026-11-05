package store

import (
	"context"
	"database/sql"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
	"time"
)

func urgencyLaunch(t *testing.T, s *Store, id, op string) {
	t.Helper()
	require.NoError(t, s.ReserveManagedLaunch(context.Background(), ManagedAgentRecord{AgentID: id, Name: id, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai", ID: "test"}, ManifestRevision: 1, ManifestHash: testManifestHash}, ManagedOperation{OperationID: op, Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: op, CanonicalBodyHash: "body", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}))
}
func TestAgentUrgencyMigrationFreshAndUpgrade(t *testing.T) {
	ctx := context.Background()
	for _, upgrade := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "upgrade"}[upgrade], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "router.db")
			if upgrade {
				migrations := DefaultMigrations()
				old, err := Open(ctx, path, Options{Migrations: migrations[:migrationIndex(t, migrations, "124_agent_urgency")]})
				require.NoError(t, err)
				urgencyLaunch(t, old, "legacy", "launch")
				_, err = old.db.Exec(`DELETE FROM agents WHERE agent_id='legacy'`)
				require.NoError(t, err)
				require.NoError(t, old.Close())
			}
			s, err := Open(ctx, path, Options{})
			require.NoError(t, err)
			if !upgrade {
				urgencyLaunch(t, s, "legacy", "launch")
			}
			var urgent, active bool
			require.NoError(t, s.db.QueryRow(`SELECT urgent,identity_active FROM agents WHERE agent_id='legacy'`).Scan(&urgent, &active))
			require.False(t, urgent)
			require.True(t, active)
			_, err = s.db.Exec(`UPDATE agents SET identity_active=2`)
			require.Error(t, err)
			_, err = s.db.Exec(`UPDATE agents SET identity_active=NULL`)
			require.Error(t, err)
			_, err = s.db.Exec(`UPDATE agents SET urgent=2`)
			require.Error(t, err)
			_, err = s.db.Exec(`UPDATE agents SET urgent=NULL`)
			require.Error(t, err)
			saved, err := s.SetAgentUrgency(ctx, "legacy", true)
			require.NoError(t, err)
			require.True(t, saved)
			require.NoError(t, s.Close())
			s, err = Open(ctx, path, Options{})
			require.NoError(t, err)
			defer s.Close()
			require.NoError(t, s.db.QueryRow(`SELECT urgent FROM agents WHERE agent_id='legacy'`).Scan(&urgent))
			require.True(t, urgent)
		})
	}
}
func TestAgentUrgencyManagedReadPathsAndReplacement(t *testing.T) {
	s, fence := readinessSourceFixture(t)
	ctx := context.Background()
	version, err := s.GetProjectionVersion(ctx)
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		saved, err := s.SetAgentUrgency(ctx, fence.AgentID, true)
		require.NoError(t, err)
		require.True(t, saved)
	}
	next, err := s.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Greater(t, next, version)
	one, err := s.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.True(t, one.Urgent)
	all, err := s.ManagedAgents(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.True(t, all[0].Urgent)
	projection, err := s.ManagedAgentsProjectionSnapshot(ctx)
	require.NoError(t, err)
	require.Len(t, projection.Agents, 1)
	require.True(t, projection.Agents[0].Urgent)
	snapshot, err := s.ManagedAgentSnapshot(ctx, fence.AgentID)
	require.NoError(t, err)
	require.True(t, snapshot.Agent.Urgent)
	require.NoError(t, s.MarkManagedFailed(ctx, fence.AgentID, "FAILED", "test"))
	urgencyLaunch(t, s, fence.AgentID, "replacement")
	one, err = s.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.True(t, one.Urgent)
	saved, err := s.SetAgentUrgency(ctx, fence.AgentID, false)
	require.NoError(t, err)
	require.False(t, saved)
}
func TestAgentUrgencyConnectedSnapshotAndPruning(t *testing.T) {
	s, fence := readinessSourceFixture(t)
	ctx := context.Background()
	_, err := s.db.Exec(`INSERT INTO agents(agent_id,first_seen_at) VALUES ('tmux','2000-01-01T00:00:00.000Z')`)
	require.NoError(t, err)
	for _, id := range []string{fence.AgentID, "tmux"} {
		_, err = s.SetAgentUrgency(ctx, id, true)
		require.NoError(t, err)
	}
	snapshot, err := s.ConnectedAgentSnapshot(ctx, []string{fence.AgentID, "tmux", "missing", "tmux"}, false)
	require.NoError(t, err)
	require.Equal(t, map[string]bool{fence.AgentID: true, "tmux": true}, snapshot.Urgency)
	require.Empty(t, snapshot.Lifecycle)
	_, err = s.db.Exec(`UPDATE agents SET first_seen_at='2000-01-01T00:00:00.000Z'`)
	require.NoError(t, err)
	require.NoError(t, s.runImmediate(ctx, func() error {
		_, err := s.pruneStaleAgents(ctx, formatTimestamp(time.Now().Add(2*time.Hour)))
		return err
	}))
	_, err = s.SetAgentUrgency(ctx, "tmux", true)
	require.ErrorIs(t, err, ErrAgentNotFound)
	_, err = s.SetAgentUrgency(ctx, fence.AgentID, true)
	require.NoError(t, err)
	_, err = s.SetAgentUrgency(ctx, "unknown", true)
	require.ErrorIs(t, err, ErrAgentNotFound)
}
func TestAgentUrgencyReminderCleanupProtectsManagedID(t *testing.T) {
	for _, connected := range []bool{false, true} {
		t.Run(map[bool]string{false: "never", true: "stale"}[connected], func(t *testing.T) {
			s, fence := readinessSourceFixture(t)
			ctx := context.Background()
			_, err := s.db.Exec(`DELETE FROM recipient_sources`)
			require.NoError(t, err)
			if connected {
				_, err = s.db.Exec(`UPDATE agents SET last_connected_at='2000-01-01T00:00:00.000Z'`)
				require.NoError(t, err)
			}
			_, err = s.SetAgentUrgency(ctx, fence.AgentID, true)
			require.NoError(t, err)
			interval := int64(60000)
			require.NoError(t, s.runImmediate(ctx, func() error {
				return s.fireRecurring(ctx, &domain.Reminder{Target: fence.AgentID, IntervalMS: &interval, NextDueAt: "2026-01-01T00:00:00.000Z"}, "2026-01-01T01:00:00.000Z", nil, nil)
			}))
			var urgent bool
			require.NoError(t, s.db.QueryRow(`SELECT urgent FROM agents WHERE agent_id=?`, fence.AgentID).Scan(&urgent))
			require.True(t, urgent)
		})
	}
}
func TestAgentUrgencyFinalDeletion(t *testing.T) {
	s, ctx, agent, action := terminationActionAtDurableWrite(t)
	_, err := s.SetAgentUrgency(ctx, agent.AgentID, true)
	require.NoError(t, err)
	s.managedTerminationFault = func(stage string) error {
		if stage == "before_terminalize" {
			return sql.ErrTxDone
		}
		return nil
	}
	require.Error(t, s.CompleteManagedTermination(ctx, action.ActionID))
	var urgent bool
	require.NoError(t, s.db.QueryRow(`SELECT urgent FROM agents WHERE agent_id=?`, agent.AgentID).Scan(&urgent))
	require.True(t, urgent)
	s.managedTerminationFault = nil
	require.NoError(t, s.CompleteManagedTermination(ctx, action.ActionID))
	require.NoError(t, s.db.QueryRow(`SELECT urgent FROM agents WHERE agent_id=?`, agent.AgentID).Scan(&urgent))
	require.False(t, urgent)
	_, err = s.SetAgentUrgency(ctx, agent.AgentID, true)
	require.ErrorIs(t, err, ErrAgentNotFound)
	urgencyLaunch(t, s, agent.AgentID, "new-identity")
	saved, err := s.SetAgentUrgency(ctx, agent.AgentID, false)
	require.NoError(t, err)
	require.False(t, saved)
}

func TestAgentUrgencyMutationRollsBackWithProjectionVersion(t *testing.T) {
	s, fence := readinessSourceFixture(t)
	ctx := context.Background()
	before, err := s.GetProjectionVersion(ctx)
	require.NoError(t, err)
	_, err = s.db.Exec(`CREATE TRIGGER reject_urgency_version BEFORE UPDATE OF projection_version ON managed_settings BEGIN SELECT RAISE(ABORT,'controlled failure'); END`)
	require.NoError(t, err)
	saved, err := s.SetAgentUrgency(ctx, fence.AgentID, true)
	require.Error(t, err)
	require.False(t, saved)
	agent, err := s.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.False(t, agent.Urgent)
	after, err := s.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestAgentUrgencyConcurrentFinalDeletion(t *testing.T) {
	s, ctx, agent, action := terminationActionAtDurableWrite(t)
	start := make(chan struct{})
	write := make(chan error, 1)
	deletion := make(chan error, 1)
	go func() { <-start; _, err := s.SetAgentUrgency(ctx, agent.AgentID, true); write <- err }()
	go func() { <-start; deletion <- s.CompleteManagedTermination(ctx, action.ActionID) }()
	close(start)
	require.NoError(t, <-deletion)
	if err := <-write; err != nil {
		require.ErrorIs(t, err, ErrAgentNotFound)
	}
	var urgent bool
	require.NoError(t, s.db.QueryRow(`SELECT urgent FROM agents WHERE agent_id=?`, agent.AgentID).Scan(&urgent))
	require.False(t, urgent)
	_, err := s.SetAgentUrgency(ctx, agent.AgentID, true)
	require.ErrorIs(t, err, ErrAgentNotFound)
}

func TestAgentUrgencyTmuxRecreationAndNewConnection(t *testing.T) {
	s, ctx, agent, action := terminationActionAtDurableWrite(t)
	require.NoError(t, s.CompleteManagedTermination(ctx, action.ActionID))
	// Only a real acquisition establishes a new transport lifetime.
	_, err := s.AcquireAgentLease(ctx, LeaseRequest{AgentID: agent.AgentID, InstanceID: "new-instance", LeaseID: "123e4567-e89b-42d3-a456-426614170993"})
	require.NoError(t, err)
	saved, err := s.SetAgentUrgency(ctx, agent.AgentID, true)
	require.NoError(t, err)
	require.True(t, saved)
	_, err = s.db.Exec(`DELETE FROM agents WHERE agent_id=?`, agent.AgentID)
	require.NoError(t, err)
	_, err = s.db.Exec(`INSERT INTO agents(agent_id,first_seen_at) VALUES (?, '2099-01-01T00:00:00.000Z')`, agent.AgentID)
	require.NoError(t, err)
	var urgent bool
	require.NoError(t, s.db.QueryRow(`SELECT urgent FROM agents WHERE agent_id=?`, agent.AgentID).Scan(&urgent))
	require.False(t, urgent)
	saved, err = s.SetAgentUrgency(ctx, agent.AgentID, true)
	require.NoError(t, err)
	require.True(t, saved)
}

func TestAgentUrgencyBatchHydratesDistinctIDsAndRejectsMissingSharedRows(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	ctx := context.Background()
	urgencyLaunch(t, s, "alpha", "launch-alpha")
	urgencyLaunch(t, s, "beta", "launch-beta")
	_, err := s.SetAgentUrgency(ctx, "beta", true)
	require.NoError(t, err)
	for _, read := range []func() ([]*ManagedAgentRecord, error){
		func() ([]*ManagedAgentRecord, error) { return s.ManagedAgents(ctx) },
		func() ([]*ManagedAgentRecord, error) {
			snapshot, err := s.ManagedAgentsProjectionSnapshot(ctx)
			return snapshot.Agents, err
		},
	} {
		agents, err := read()
		require.NoError(t, err)
		require.Len(t, agents, 2)
		require.Equal(t, "alpha", agents[0].AgentID)
		require.False(t, agents[0].Urgent)
		require.Equal(t, "beta", agents[1].AgentID)
		require.True(t, agents[1].Urgent)
	}
	// Corrupt/missing shared authority must fail reads, never confirm false.
	_, err = s.db.Exec(`DELETE FROM agents WHERE agent_id='beta'`)
	require.NoError(t, err)
	_, err = s.ManagedAgent(ctx, "beta")
	require.ErrorIs(t, err, sql.ErrNoRows)
	_, err = s.ManagedAgents(ctx)
	require.ErrorIs(t, err, sql.ErrNoRows)
	_, err = s.ManagedAgentSnapshot(ctx, "beta")
	require.ErrorIs(t, err, sql.ErrNoRows)
	_, err = s.ManagedAgentsProjectionSnapshot(ctx)
	require.ErrorIs(t, err, sql.ErrNoRows)
}
