package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func TestManagedProgress(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "router.db")
	require.NoError(t, CloneTemplateDB(t, dbPath))
	s, err := Open(ctx, dbPath, Options{Clock: clock, Migrations: []Migration{}})
	require.NoError(t, err)
	defer s.Close()

	// Verify migration 106 was applied
	var count int
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE id='106_agent_progress'`).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count, "migration 106_agent_progress must be applied")

	agentID := "worker-progress-test"
	launchRecord := ManagedAgentRecord{
		AgentID:          agentID,
		Name:             "Worker Progress Test",
		Cwd:              t.TempDir(),
		SelectedModel:    ManagedModel{Provider: "google", ID: "gemini-3.8-flash"},
		Runtime:          "embedded",
		DesiredState:     "running",
		LifecycleState:   "running",
		ActivityState:    "idle",
		ManifestRevision: 1,
		ManifestHash:     "manifest-sha",
		CreatedAt:        "2026-09-01T12:00:00Z",
		UpdatedAt:        "2026-09-01T12:00:00Z",
	}
	op := ManagedOperation{
		OperationID:       "op-launch-progress",
		Method:            "POST",
		CanonicalPath:     "/v1/managed-agents",
		RequestID:         "req-launch-progress",
		CanonicalBodyHash: "hash-launch-progress",
		State:             "terminal",
		ResponseStatus:    200,
		CreatedAt:         "2026-09-01T12:00:00Z",
	}
	require.NoError(t, s.ReserveManagedLaunch(ctx, launchRecord, op))

	t.Run("initial progress is unset", func(t *testing.T) {
		percent, note, updatedAt, totalUpdates, err := s.GetAgentProgress(ctx, agentID)
		require.NoError(t, err)
		require.Nil(t, percent)
		require.Empty(t, note)
		require.Empty(t, updatedAt)
		require.Equal(t, 0, totalUpdates)

		history, err := s.GetAgentProgressHistory(ctx, agentID)
		require.NoError(t, err)
		require.Empty(t, history)

		agent, err := s.ManagedAgent(ctx, agentID)
		require.NoError(t, err)
		require.Nil(t, agent.ProgressPercent)
		require.Empty(t, agent.ProgressNote)
		require.Empty(t, agent.ProgressUpdatedAt)
	})

	t.Run("setting progress updates snapshot and history", func(t *testing.T) {
		err := s.SetAgentProgress(ctx, agentID, 25, "First milestone")
		require.NoError(t, err)

		percent, note, updatedAt, totalUpdates, err := s.GetAgentProgress(ctx, agentID)
		require.NoError(t, err)
		require.NotNil(t, percent)
		require.Equal(t, 25, *percent)
		require.Equal(t, "First milestone", note)
		require.NotEmpty(t, updatedAt)
		require.Equal(t, 1, totalUpdates)

		// Also check ManagedAgent record loader
		agent, err := s.ManagedAgent(ctx, agentID)
		require.NoError(t, err)
		require.NotNil(t, agent.ProgressPercent)
		require.Equal(t, 25, *agent.ProgressPercent)
		require.Equal(t, "First milestone", agent.ProgressNote)
		require.Equal(t, updatedAt, agent.ProgressUpdatedAt)

		// Second update
		err = s.SetAgentProgress(ctx, agentID, 50, "Halfway done")
		require.NoError(t, err)

		percent, note, updatedAt, totalUpdates, err = s.GetAgentProgress(ctx, agentID)
		require.NoError(t, err)
		require.NotNil(t, percent)
		require.Equal(t, 50, *percent)
		require.Equal(t, "Halfway done", note)
		require.Equal(t, 2, totalUpdates)

		history, err := s.GetAgentProgressHistory(ctx, agentID)
		require.NoError(t, err)
		require.Len(t, history, 2)
		require.Equal(t, 25, history[0].Percent)
		require.Equal(t, "First milestone", history[0].Note)
		require.NotEmpty(t, history[0].Timestamp)
		require.Equal(t, 50, history[1].Percent)
		require.Equal(t, "Halfway done", history[1].Note)
		require.NotEmpty(t, history[1].Timestamp)
	})

	t.Run("clearing progress unsets current snapshot but preserves history", func(t *testing.T) {
		err := s.ClearAgentProgress(ctx, agentID)
		require.NoError(t, err)

		percent, note, updatedAt, totalUpdates, err := s.GetAgentProgress(ctx, agentID)
		require.NoError(t, err)
		require.Nil(t, percent)
		require.Empty(t, note)
		require.Empty(t, updatedAt)
		require.Equal(t, 2, totalUpdates)

		agent, err := s.ManagedAgent(ctx, agentID)
		require.NoError(t, err)
		require.Nil(t, agent.ProgressPercent)
		require.Empty(t, agent.ProgressNote)
		require.Empty(t, agent.ProgressUpdatedAt)

		history, err := s.GetAgentProgressHistory(ctx, agentID)
		require.NoError(t, err)
		require.Len(t, history, 2)
	})

	t.Run("validation bounds", func(t *testing.T) {
		// Negative percent
		err := s.SetAgentProgress(ctx, agentID, -1, "negative")
		require.Error(t, err)
		require.Contains(t, err.Error(), "between 0 and 100")

		// Percent > 100
		err = s.SetAgentProgress(ctx, agentID, 101, "too high")
		require.Error(t, err)
		require.Contains(t, err.Error(), "between 0 and 100")

		// Note > 120 chars
		longNote := strings.Repeat("a", 121)
		err = s.SetAgentProgress(ctx, agentID, 10, longNote)
		require.Error(t, err)
		require.Contains(t, err.Error(), "maximum length of 120 characters")

		// Note exactly 120 chars is allowed
		validNote120 := strings.Repeat("b", 120)
		err = s.SetAgentProgress(ctx, agentID, 10, validNote120)
		require.NoError(t, err)

		// 0 and 100 percent are allowed
		err = s.SetAgentProgress(ctx, agentID, 0, "starting")
		require.NoError(t, err)

		err = s.SetAgentProgress(ctx, agentID, 100, "done")
		require.NoError(t, err)
	})

	t.Run("non-existent agent error", func(t *testing.T) {
		err := s.SetAgentProgress(ctx, "non-existent-agent", 10, "note")
		require.Error(t, err)

		err = s.ClearAgentProgress(ctx, "non-existent-agent")
		require.Error(t, err)

		_, _, _, _, err = s.GetAgentProgress(ctx, "non-existent-agent")
		require.Error(t, err)

		_, err = s.GetAgentProgressHistory(ctx, "non-existent-agent")
		require.Error(t, err)
	})

	t.Run("FIFO pruning beyond 1000 entries", func(t *testing.T) {
		fifoAgentID := "worker-fifo-test"
		fifoRecord := ManagedAgentRecord{
			AgentID:          fifoAgentID,
			Name:             "Worker FIFO Test",
			Cwd:              t.TempDir(),
			SelectedModel:    ManagedModel{Provider: "google", ID: "gemini-3.8-flash"},
			Runtime:          "embedded",
			DesiredState:     "running",
			LifecycleState:   "running",
			ActivityState:    "idle",
			ManifestRevision: 1,
			ManifestHash:     "manifest-sha",
			CreatedAt:        "2026-09-01T12:00:00Z",
			UpdatedAt:        "2026-09-01T12:00:00Z",
		}
		fifoOp := ManagedOperation{
			OperationID:       "op-launch-fifo",
			Method:            "POST",
			CanonicalPath:     "/v1/managed-agents",
			RequestID:         "req-launch-fifo",
			CanonicalBodyHash: "hash-launch-fifo",
			State:             "terminal",
			ResponseStatus:    200,
			CreatedAt:         "2026-09-01T12:00:00Z",
		}
		require.NoError(t, s.ReserveManagedLaunch(ctx, fifoRecord, fifoOp))

		// Insert 1005 entries
		for i := 1; i <= 1005; i++ {
			err := s.SetAgentProgress(ctx, fifoAgentID, i%101, fmt.Sprintf("Step %d", i))
			require.NoError(t, err)
		}

		history, err := s.GetAgentProgressHistory(ctx, fifoAgentID)
		require.NoError(t, err)
		require.Len(t, history, 1000, "must retain exactly 1000 entries after pruning")

		// First entry in history should be Step 6 (since Steps 1..5 were pruned FIFO)
		require.Equal(t, "Step 6", history[0].Note)
		require.Equal(t, "Step 1005", history[len(history)-1].Note)

		// Total count in GetAgentProgress should also reflect 1000 retained
		_, _, _, totalUpdates, err := s.GetAgentProgress(ctx, fifoAgentID)
		require.NoError(t, err)
		require.Equal(t, 1000, totalUpdates)
	})
}
