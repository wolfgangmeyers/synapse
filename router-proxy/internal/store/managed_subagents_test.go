package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"

	"github.com/stretchr/testify/require"
)

// These exercise the real SQLite migration, managed parent reservation, and
// projection path. They intentionally do not create a test-only shadow schema.
func subagentParent(t *testing.T, s *Store, id string) EmbeddedBinding {
	t.Helper()
	b := embeddedManagedAgent(t, s, id)
	require.NoError(t, s.BindEmbeddedSession(context.Background(), b, "worker"))
	return claimEmbeddedFixture(t, s, b)
}

func TestManagedSubagentDurableParentResultAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "existing.db")
	migrations := DefaultMigrations()
	prior := migrations[:migrationIndex(t, migrations, "084_managed_subagents")]
	s, err := Open(ctx, path, Options{Migrations: prior})
	require.NoError(t, err)
	parent := subagentParent(t, s, "parent")
	require.NoError(t, s.Close())
	s, err = Open(ctx, path, Options{})
	require.NoError(t, err)
	child := ManagedSubagentRecord{
		AgentID: "subagent-parent-one", ParentAgentID: parent.AgentID,
		Description: "check storage", SubagentType: "general-purpose",
		Status: "queued", MaxTurns: 7,
	}
	require.NoError(t, s.CreateManagedSubagent(ctx, parent, child))
	saved, err := s.ManagedSubagent(ctx, parent, child.AgentID)
	require.NoError(t, err)
	require.Equal(t, parent.SessionID, saved.ParentSessionID)
	require.Equal(t, parent.Epoch, saved.ParentEpoch)
	require.Equal(t, parent.Generation, saved.ParentGeneration)
	require.Equal(t, "queued", saved.Status)
	require.Equal(t, 7, saved.MaxTurns)
	var fkFailures string
	require.ErrorIs(t, s.db.QueryRowContext(ctx, `PRAGMA foreign_key_check`).Scan(&fkFailures), sql.ErrNoRows, "migration must preserve FK integrity")
	require.NoError(t, s.UpdateManagedSubagent(ctx, parent, child.AgentID, "queued", ManagedSubagentResult{Status: "running", TurnCount: 2}))
	require.NoError(t, s.UpdateManagedSubagent(ctx, parent, child.AgentID, "running", ManagedSubagentResult{Status: "completed", TurnCount: 3, ToolUseCount: 4, TotalTokens: 120, ResultText: "full final answer"}))
	require.NoError(t, s.Close())
	s, err = Open(ctx, path, Options{})
	require.NoError(t, err)
	defer s.Close()
	saved, err = s.ManagedSubagent(ctx, parent, child.AgentID)
	require.NoError(t, err)
	require.Equal(t, "completed", saved.Status)
	require.Equal(t, "full final answer", saved.ResultText)
	require.Equal(t, 3, saved.TurnCount)
	require.Equal(t, 4, saved.ToolUseCount)
	require.Equal(t, 120, saved.TotalTokens)
	require.Equal(t, "check storage", saved.Description)
	require.Equal(t, "general-purpose", saved.SubagentType)
}

func TestManagedSubagentRejectsForeignAndStaleParent(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	parent := subagentParent(t, s, "owner")
	foreign := subagentParent(t, s, "foreign")
	child := ManagedSubagentRecord{AgentID: "subagent-owner-one", ParentAgentID: parent.AgentID, Description: "owned", Status: "queued", MaxTurns: 2}
	require.Error(t, s.CreateManagedSubagent(ctx, foreign, child))
	require.NoError(t, s.CreateManagedSubagent(ctx, parent, child))
	_, err := s.ManagedSubagent(ctx, foreign, child.AgentID)
	require.Error(t, err)
	require.Error(t, s.UpdateManagedSubagent(ctx, foreign, child.AgentID, "queued", ManagedSubagentResult{Status: "running"}))
	stale := parent
	stale.Epoch++
	require.Error(t, s.CreateManagedSubagent(ctx, stale, ManagedSubagentRecord{AgentID: "subagent-owner-stale", ParentAgentID: parent.AgentID, Status: "queued", MaxTurns: 2}))
	stale = parent
	stale.Generation = "previous-generation"
	require.Error(t, s.UpdateManagedSubagent(ctx, stale, child.AgentID, "queued", ManagedSubagentResult{Status: "running"}))
	_, err = s.ManagedSubagent(ctx, stale, child.AgentID)
	require.Error(t, err)
}

func TestManagedSubagentExcludedFromTopLevelProjection(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	parent := subagentParent(t, s, "visible-parent")
	child := ManagedSubagentRecord{AgentID: "subagent-visible-parent-one", ParentAgentID: parent.AgentID, Description: "not a top-level agent", Status: "queued", MaxTurns: 5}
	require.NoError(t, s.CreateManagedSubagent(ctx, parent, child))
	rows, err := s.ManagedAgents(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, parent.AgentID, rows[0].AgentID)
	snapshot, err := s.ManagedAgentsProjectionSnapshot(ctx)
	require.NoError(t, err)
	require.Len(t, snapshot.Agents, 1)
	require.Equal(t, parent.AgentID, snapshot.Agents[0].AgentID)
	children, err := s.ManagedSubagents(ctx, parent)
	require.NoError(t, err)
	require.Len(t, children, 1)
	require.Equal(t, child.AgentID, children[0].AgentID)
	individual, err := s.ManagedAgentSnapshot(ctx, child.AgentID)
	require.NoError(t, err)
	require.Nil(t, individual.Agent, "direct top-level projection must not expose a child")
	require.NoError(t, s.ReconcileManagedRecipientSources(ctx))
	var childSource int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recipient_sources WHERE owner_identity=?`, child.AgentID).Scan(&childSource))
	require.Zero(t, childSource, "child must not become an independent recipient")
	stale := parent
	stale.Epoch++
	_, err = s.ManagedSubagents(ctx, stale)
	require.Error(t, err)
	stale = parent
	stale.Generation = "stale-generation"
	_, err = s.ManagedSubagents(ctx, stale)
	require.Error(t, err)
}

func TestManagedSubagentRoleCannotEscalateOrNest(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	parent := subagentParent(t, s, "role-parent")
	childID := "subagent-role-parent-one"
	require.NoError(t, s.CreateManagedSubagent(ctx, parent, ManagedSubagentRecord{AgentID: childID, ParentAgentID: parent.AgentID, Status: "queued"}))
	child := EmbeddedBinding{AgentID: childID, SessionID: childID + "-session", Epoch: 1, Generation: parent.Generation}
	require.Error(t, s.EnsureEmbeddedSession(ctx, child), "root bootstrap cannot promote child")
	require.NoError(t, s.BindEmbeddedSession(ctx, child, "subagent"))
	child = claimEmbeddedFixture(t, s, child)
	require.Error(t, s.CreateManagedSubagent(ctx, child, ManagedSubagentRecord{AgentID: "nested-subagent", ParentAgentID: child.AgentID, Status: "queued"}))
	launch := ManagedAgentRecord{AgentID: "invalid-top-level", Name: "invalid-top-level", Runtime: "embedded", Cwd: t.TempDir(), SelectedModel: ManagedModel{Provider: "google", ID: "model"}, PiSessionID: "invalid-session", SessionEpoch: 1, RuntimeGeneration: "generation", ManifestRevision: 1, ManifestHash: testManifestHash}
	operation := ManagedOperation{OperationID: "invalid-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "invalid-launch", CanonicalBodyHash: "hash"}
	require.ErrorContains(t, s.ReserveManagedEmbeddedLaunch(ctx, launch, operation, 32, "subagent"), "invalid embedded launch role")
	var count int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_agents WHERE agent_id IN ('invalid-top-level','nested-subagent')`).Scan(&count))
	require.Zero(t, count)
}

func TestManagedSubagentTransitionsRejectInvalidOrSecondTerminal(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	parent := subagentParent(t, s, "transitions-parent")
	child := ManagedSubagentRecord{AgentID: "subagent-transitions-one", ParentAgentID: parent.AgentID, Status: "queued", MaxTurns: 0}
	require.NoError(t, s.CreateManagedSubagent(ctx, parent, child)) // zero means unlimited
	require.Error(t, s.UpdateManagedSubagent(ctx, parent, child.AgentID, "running", ManagedSubagentResult{Status: "completed"}), "CAS must match previous status")
	require.Error(t, s.UpdateManagedSubagent(ctx, parent, child.AgentID, "queued", ManagedSubagentResult{Status: "steered"}), "queued cannot settle")
	require.NoError(t, s.UpdateManagedSubagent(ctx, parent, child.AgentID, "queued", ManagedSubagentResult{Status: "running"}))
	require.Error(t, s.UpdateManagedSubagent(ctx, parent, child.AgentID, "running", ManagedSubagentResult{Status: "queued"}), "no backwards transition")
	_, err := s.db.ExecContext(ctx, `UPDATE managed_subagents SET status='bogus' WHERE agent_id=?`, child.AgentID)
	require.Error(t, err, "SQLite status constraint")
	require.NoError(t, s.UpdateManagedSubagent(ctx, parent, child.AgentID, "running", ManagedSubagentResult{Status: "error", Error: "failed"}))
	require.Error(t, s.UpdateManagedSubagent(ctx, parent, child.AgentID, "error", ManagedSubagentResult{Status: "completed", ResultText: "late"}))
	saved, err := s.ManagedSubagent(ctx, parent, child.AgentID)
	require.NoError(t, err)
	require.Equal(t, "error", saved.Status)
	require.Equal(t, "failed", saved.Error)
	require.Empty(t, saved.ResultText)
}

func TestManagedSubagentTerminalRetentionCascadesOnlyExpiredChildren(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
	s := openTestStoreWithOptions(t, Options{Clock: clock})
	parent := subagentParent(t, s, "retention-parent")
	for _, name := range []string{"old", "recent", "live"} {
		child := ManagedSubagentRecord{AgentID: "subagent-retention-" + name, ParentAgentID: parent.AgentID, Status: "queued", MaxTurns: 2}
		require.NoError(t, s.CreateManagedSubagent(ctx, parent, child))
		if name != "live" {
			require.NoError(t, s.UpdateManagedSubagent(ctx, parent, child.AgentID, "queued", ManagedSubagentResult{Status: "running"}))
			require.NoError(t, s.UpdateManagedSubagent(ctx, parent, child.AgentID, "running", ManagedSubagentResult{Status: "completed", ResultText: "answer " + name}))
		}
	}
	require.NoError(t, clock.Advance(25*time.Hour))
	parent = claimEmbeddedFixture(t, s, parent)
	recent := ManagedSubagentRecord{AgentID: "subagent-retention-new", ParentAgentID: parent.AgentID, Status: "queued", MaxTurns: 2}
	require.NoError(t, s.CreateManagedSubagent(ctx, parent, recent))
	require.NoError(t, s.UpdateManagedSubagent(ctx, parent, recent.AgentID, "queued", ManagedSubagentResult{Status: "running"}))
	require.NoError(t, s.UpdateManagedSubagent(ctx, parent, recent.AgentID, "running", ManagedSubagentResult{Status: "completed", ResultText: "fresh"}))
	_, err := s.SweepLifecycle(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	_, err = s.ManagedSubagent(ctx, parent, "subagent-retention-old")
	require.Error(t, err)
	_, err = s.ManagedSubagent(ctx, parent, "subagent-retention-recent")
	require.Error(t, err)
	for _, id := range []string{"subagent-retention-live", "subagent-retention-new"} {
		_, err = s.ManagedSubagent(ctx, parent, id)
		require.NoError(t, err)
	}
	var count int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_agents WHERE agent_id=?`, "subagent-retention-old").Scan(&count))
	require.Zero(t, count, "managed identity and dependent embedded history must cascade")
}

func TestManagedSubagentResumeSameIDAndAttemptFences(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	parent := subagentParent(t, s, "resume-parent")
	foreign := subagentParent(t, s, "resume-foreign")
	childID := "subagent-resume-parent-one"
	require.NoError(t, s.CreateManagedSubagent(ctx, parent, ManagedSubagentRecord{AgentID: childID, ParentAgentID: parent.AgentID, Status: "queued", MaxTurns: 3}))
	_, err := s.ResumeManagedSubagent(ctx, parent, childID, "too soon")
	require.Error(t, err)
	require.NoError(t, s.UpdateManagedSubagent(ctx, parent, childID, "queued", ManagedSubagentResult{Status: "running"}))
	require.NoError(t, s.UpdateManagedSubagentProgress(ctx, parent, childID, 1, ManagedSubagentProgress{TurnCount: 2, ToolUseCount: 3, TotalTokens: 9, DurationSeconds: 1.5}))
	require.NoError(t, s.UpdateManagedSubagentAttempt(ctx, parent, childID, 1, "running", ManagedSubagentResult{Status: "completed", TurnCount: 2, ToolUseCount: 3, TotalTokens: 9, DurationSeconds: 1.5, ResultText: "first"}))
	_, err = s.ResumeManagedSubagent(ctx, foreign, childID, "steal")
	require.Error(t, err)
	stale := parent
	stale.Generation = "old-generation"
	_, err = s.ResumeManagedSubagent(ctx, stale, childID, "steal")
	require.Error(t, err)
	resumed, err := s.ResumeManagedSubagent(ctx, parent, childID, "continue")
	require.NoError(t, err)
	require.Equal(t, childID, resumed.AgentID)
	require.Equal(t, 2, resumed.Attempt)
	require.Equal(t, "queued", resumed.Status)
	require.Equal(t, 2, resumed.TurnCount)
	require.Equal(t, 9, resumed.TotalTokens)
	require.Equal(t, 1.5, resumed.DurationSeconds)
	require.Empty(t, resumed.ResultText)
	require.Error(t, s.UpdateManagedSubagent(ctx, parent, childID, "running", ManagedSubagentResult{Status: "completed", ResultText: "late"}))
	require.NoError(t, s.UpdateManagedSubagentAttempt(ctx, parent, childID, 2, "queued", ManagedSubagentResult{Status: "running", TurnCount: 2, ToolUseCount: 3, TotalTokens: 9, DurationSeconds: 1.5}))
	require.Error(t, s.UpdateManagedSubagentAttempt(ctx, parent, childID, 1, "running", ManagedSubagentResult{Status: "completed", ResultText: "late"}))
	require.NoError(t, s.UpdateManagedSubagentProgress(ctx, parent, childID, 2, ManagedSubagentProgress{TurnCount: 4, ToolUseCount: 5, TotalTokens: 20, DurationSeconds: 3.25}))
	require.Error(t, s.UpdateManagedSubagentProgress(ctx, parent, childID, 2, ManagedSubagentProgress{TurnCount: 3, ToolUseCount: 5, TotalTokens: 20, DurationSeconds: 3.25}), "counters cannot decrease")
	require.NoError(t, s.UpdateManagedSubagentAttempt(ctx, parent, childID, 2, "running", ManagedSubagentResult{Status: "completed", TurnCount: 4, ToolUseCount: 5, TotalTokens: 20, DurationSeconds: 3.25, ResultText: "second"}))
	saved, err := s.ManagedSubagent(ctx, parent, childID)
	require.NoError(t, err)
	require.Equal(t, "second", saved.ResultText)
	require.Equal(t, 3.25, saved.DurationSeconds)
}

func TestManagedSubagentResumeMaxTurnsOverridePersistsAndInherits(t *testing.T) {
	ctx := context.Background()
	s, path := embeddedRedStore(t)
	parent := subagentParent(t, s, "limits-parent")
	foreign := subagentParent(t, s, "limits-foreign")
	id := "subagent-limits-parent-one"
	require.NoError(t, s.CreateManagedSubagent(ctx, parent, ManagedSubagentRecord{AgentID: id, ParentAgentID: parent.AgentID, Status: "queued", MaxTurns: 2}))
	require.NoError(t, s.UpdateManagedSubagent(ctx, parent, id, "queued", ManagedSubagentResult{Status: "running"}))
	require.NoError(t, s.UpdateManagedSubagent(ctx, parent, id, "running", ManagedSubagentResult{Status: "completed", ResultText: "first"}))
	_, err := s.ResumeManagedSubagent(ctx, foreign, id, "foreign", 7)
	require.Error(t, err)
	stale := parent
	stale.Epoch++
	_, err = s.ResumeManagedSubagent(ctx, stale, id, "stale", 7)
	require.Error(t, err)
	_, err = s.ResumeManagedSubagent(ctx, parent, id, "bad", 5, 6)
	require.Error(t, err)
	before, err := s.ManagedSubagent(ctx, parent, id)
	require.NoError(t, err)
	require.Equal(t, 2, before.MaxTurns)
	require.Equal(t, 1, before.Attempt)
	resumed, err := s.ResumeManagedSubagent(ctx, parent, id, "second", 5)
	require.NoError(t, err)
	require.Equal(t, 5, resumed.MaxTurns)
	require.Equal(t, 2, resumed.Attempt)
	require.Equal(t, "queued", resumed.Status)
	_, err = s.ResumeManagedSubagent(ctx, parent, id, "too soon", 9)
	require.Error(t, err)
	require.NoError(t, s.UpdateManagedSubagentAttempt(ctx, parent, id, 2, "queued", ManagedSubagentResult{Status: "running"}))
	require.NoError(t, s.UpdateManagedSubagentAttempt(ctx, parent, id, 2, "running", ManagedSubagentResult{Status: "completed", ResultText: "second"}))
	require.NoError(t, s.Close())
	reopened, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer reopened.Close()
	saved, err := reopened.ManagedSubagent(ctx, parent, id)
	require.NoError(t, err)
	require.Equal(t, 5, saved.MaxTurns)
	require.Equal(t, "second", saved.ResultText)
	require.Equal(t, 2, saved.Attempt)
	inherited, err := reopened.ResumeManagedSubagent(ctx, parent, id, "third", 0)
	require.NoError(t, err)
	require.Equal(t, 5, inherited.MaxTurns)
	require.Equal(t, 3, inherited.Attempt)
	require.NoError(t, reopened.UpdateManagedSubagentAttempt(ctx, parent, id, 3, "queued", ManagedSubagentResult{Status: "running"}))
	require.NoError(t, reopened.UpdateManagedSubagentAttempt(ctx, parent, id, 3, "running", ManagedSubagentResult{Status: "completed", ResultText: "third"}))
	omitted, err := reopened.ResumeManagedSubagent(ctx, parent, id, "fourth")
	require.NoError(t, err)
	require.Equal(t, 5, omitted.MaxTurns)
	require.Equal(t, 4, omitted.Attempt)
}

func TestManagedSubagentTranscriptReadAfterReopenWithoutChildLease(t *testing.T) {
	ctx := context.Background()
	s, path := embeddedRedStore(t)
	parent := subagentParent(t, s, "transcript-parent")
	foreign := subagentParent(t, s, "transcript-foreign")
	id := "subagent-transcript-parent-one"
	require.NoError(t, s.CreateManagedSubagent(ctx, parent, ManagedSubagentRecord{AgentID: id, ParentAgentID: parent.AgentID, Status: "queued"}))
	child := EmbeddedBinding{AgentID: id, SessionID: id + "-session", Epoch: 1, Generation: parent.Generation}
	require.NoError(t, s.BindEmbeddedSession(ctx, child, "subagent"))
	child = claimEmbeddedFixture(t, s, child)
	first := redAppend(t, s, child, "user", "question")
	second := redAppend(t, s, child, "assistant", "full answer")
	require.NoError(t, s.CommitEmbeddedCheckpoint(ctx, child, second.Sequence, second.Digest, []byte("summary")))
	require.NoError(t, s.UpdateManagedSubagent(ctx, parent, id, "queued", ManagedSubagentResult{Status: "running"}))
	require.NoError(t, s.UpdateManagedSubagent(ctx, parent, id, "running", ManagedSubagentResult{Status: "completed", ResultText: "full answer"}))
	require.NoError(t, s.Close())
	reopened, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer reopened.Close()
	unowned := child
	unowned.OwnerToken = 0
	_, err = reopened.ReopenEmbedded(ctx, unowned)
	require.Error(t, err, "the runtime reopen path still requires an owner")
	transcript, err := reopened.ManagedSubagentTranscript(ctx, parent, id)
	require.NoError(t, err)
	require.Len(t, transcript.Entries, 2)
	require.Equal(t, first.Digest, transcript.Entries[0].Digest)
	require.Equal(t, []byte("full answer"), transcript.Entries[1].Body)
	require.NotNil(t, transcript.Checkpoint)
	require.Equal(t, []byte("summary"), transcript.Checkpoint.Body)
	_, err = reopened.ManagedSubagentTranscript(ctx, foreign, id)
	require.Error(t, err)
	stale := parent
	stale.Epoch++
	_, err = reopened.ManagedSubagentTranscript(ctx, stale, id)
	require.Error(t, err)
	stale = parent
	stale.Generation = "old-generation"
	_, err = reopened.ManagedSubagentTranscript(ctx, stale, id)
	require.Error(t, err)
}

func TestManagedSubagentRunningInterruptedOnOpen(t *testing.T) {
	ctx := context.Background()
	s, path := embeddedRedStore(t)
	parent := subagentParent(t, s, "crash-parent")
	for _, state := range []string{"running", "completed"} {
		child := ManagedSubagentRecord{AgentID: "subagent-crash-" + state, ParentAgentID: parent.AgentID, Description: state, Status: "queued", MaxTurns: 5}
		require.NoError(t, s.CreateManagedSubagent(ctx, parent, child))
		require.NoError(t, s.UpdateManagedSubagent(ctx, parent, child.AgentID, "queued", ManagedSubagentResult{Status: "running"}))
		if state == "completed" {
			// A terminal result committed before the crash remains readable.
			require.NoError(t, s.UpdateManagedSubagent(ctx, parent, child.AgentID, "running", ManagedSubagentResult{Status: "completed", ResultText: "saved answer"}))
		}
	}
	require.NoError(t, s.Close())
	reopened, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer reopened.Close()
	runningChild, err := reopened.ManagedSubagent(ctx, parent, "subagent-crash-running")
	require.NoError(t, err)
	require.Equal(t, "running", runningChild.Status)
	require.Empty(t, runningChild.Error)
	completed, err := reopened.ManagedSubagent(ctx, parent, "subagent-crash-completed")
	require.NoError(t, err)
	require.Equal(t, "completed", completed.Status)
	require.Equal(t, "saved answer", completed.ResultText)
}
