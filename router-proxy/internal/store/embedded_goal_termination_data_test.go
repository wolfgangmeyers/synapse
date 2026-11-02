package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

var goalTerminationTables = []string{"embedded_goal_applied_receipts", "embedded_goal_evaluations", "embedded_goal_continuations", "embedded_goal_snooze_calls", "embedded_goal_snooze_preferences"}

// Only the existing managed-runtime fixture is synthetic. Every goal history
// row is produced through its ordinary Store/checked SDK creation path.
func goalTerminationHistory(t *testing.T, s *Store, id, outcome string, snooze, started bool) EmbeddedBinding {
	t.Helper()
	return goalTerminationHistoryWithAppend(t, s, id, outcome, snooze, started, receiptSDKAppend, false)
}

func goalTerminationHistoryWithAppend(t *testing.T, s *Store, id, outcome string, snooze, started bool, appendMessage func(*testing.T, *Store, EmbeddedBinding, ai.Message) EmbeddedEntry, legacy bool) EmbeddedBinding {
	t.Helper()
	ctx := context.Background()
	b := lifecycleEmbeddedFixture(t, s, id)
	user := appendMessage(t, s, b, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "finish work"}}})
	require.NoError(t, s.EnterEmbeddedNonMailProviderTurn(ctx, b, user.Sequence, user.Digest))
	policy := goalSDKCommandWithAppend(t, s, b, id+"-set", EmbeddedGoalCommand{Action: "set", Goal: "finish", Settings: DefaultManagedGoalSettings()}, appendMessage)
	if started {
		// The checked original result exists, but the legacy fixture leaves its
		// effect UNKNOWN. A started continuation requires actual observation.
		snapshot, err := s.ReopenEmbedded(ctx, b)
		require.NoError(t, err)
		result := snapshot.Entries[len(snapshot.Entries)-1]
		require.Equal(t, "toolResult", result.Kind)
		require.NoError(t, s.ObserveEmbeddedEffect(ctx, b, id+"-set", result.Body))
		effect, err := s.RecoverEmbeddedEffect(ctx, b, id+"-set")
		require.NoError(t, err)
		require.Equal(t, "observed", effect.State)
		require.Equal(t, result.Body, effect.Result)
	}
	key := EmbeddedGoalEvaluationKey{UserSeq: user.Sequence, UserDigest: user.Digest, GoalRevision: policy.DesiredRevision}
	if snooze {
		if legacy {
			legacySnoozePreference(t, s, b, EmbeddedGoalSnoozeTurn{UserSeq: key.UserSeq, UserDigest: key.UserDigest, GoalRevision: key.GoalRevision}, id+"-snooze", 1)
		} else {
			_, replay, err := s.SaveEmbeddedGoalSnoozePreference(ctx, b, EmbeddedGoalSnoozeTurn{UserSeq: key.UserSeq, UserDigest: key.UserDigest, GoalRevision: key.GoalRevision}, id+"-snooze", []byte(`{"duration_ms":1}`))
			require.NoError(t, err)
			require.False(t, replay)
		}
	}
	if outcome == "snooze-only" {
		return b
	}
	if legacy {
		legacyGoalEvaluationHistory(t, s, b, user, policy, id)
		return b
	}
	_, replay, err := s.CommitEmbeddedGoalAppliedReceipt(ctx, b, EmbeddedGoalAppliedReceiptRequest{TurnKey: id + "-turn", TurnEntrySeq: key.UserSeq, TurnEntryDigest: key.UserDigest, GoalRevision: key.GoalRevision})
	require.NoError(t, err)
	require.False(t, replay)
	if outcome == "receipt-only" {
		return b
	}
	final := appendMessage(t, s, b, ai.AssistantMessage{StopReason: ai.StopStop})
	key.FinalSeq, key.FinalDigest = final.Sequence, final.Digest
	_, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, key)
	require.NoError(t, err)
	require.True(t, claimed)
	if outcome != EmbeddedGoalEvaluationUnknown {
		_, changed, err := s.CompleteEmbeddedGoalEvaluation(ctx, b, key, outcome, "test verdict")
		require.NoError(t, err)
		require.True(t, changed)
	}
	if started {
		_, changed, err := s.StartEmbeddedGoalContinuation(ctx, b, key)
		require.NoError(t, err)
		require.True(t, changed)
	}
	return b
}

func goalTerminationCounts(t *testing.T, s *Store, id string) []int {
	t.Helper()
	counts := make([]int, len(goalTerminationTables))
	for i, table := range goalTerminationTables {
		require.NoError(t, s.db.QueryRow("SELECT count(*) FROM "+table+" WHERE agent_id=?", id).Scan(&counts[i]))
	}
	return counts
}

func goalTerminationAdmission(t *testing.T, s *Store, b EmbeddedBinding) string {
	t.Helper()
	ctx := context.Background()
	agent, err := s.ManagedAgent(ctx, b.AgentID)
	require.NoError(t, err)
	fence := restartFenceFromAgent(agent)
	id := lifecycleUUID(991)
	op := ManagedOperation{OperationID: id, Method: "DELETE", CanonicalPath: "/v1/managed-agents/" + b.AgentID, RequestID: id, CanonicalBodyHash: testManifestHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	admission, err := s.ReserveManagedLifecycle(ctx, ManagedLifecycleReservation{AgentID: b.AgentID, Kind: "terminate", Source: "direct", Method: op.Method, CanonicalPath: op.CanonicalPath, RequestID: id, CanonicalBodyHash: op.CanonicalBodyHash, Fence: fence, Operation: op})
	require.NoError(t, err)
	require.NoError(t, s.AdvanceManagedTermination(ctx, id, "terminating", "abort", "terminating", "terminate_reap", &fence))
	require.NoError(t, s.AdvanceManagedTermination(ctx, id, "terminating", "terminate_reap", "cleanup", "direct_mail_retire", &fence))
	require.NoError(t, s.RetireManagedRecipientSourceForTermination(ctx, b.AgentID, agent.Name))
	require.NoError(t, s.AdvanceManagedTermination(ctx, id, "cleanup", "direct_mail_retire", "cleanup", "agent_cleanup", &fence))
	require.NoError(t, s.AdvanceManagedTermination(ctx, id, "cleanup", "agent_cleanup", "cleanup", "durable_write", &fence))
	return admission.Action.ActionID
}

func requireGoalForeignKeys(t *testing.T, s *Store) {
	t.Helper()
	var enabled, violations int
	require.NoError(t, s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled))
	require.Equal(t, 1, enabled)
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations))
	require.Zero(t, violations)
}

func TestEmbeddedGoalHistorySupportedTerminationAtomicAndImmutable(t *testing.T) {
	for _, tc := range []struct {
		name, outcome               string
		snooze, started, off, reset bool
	}{
		{name: "receipt", outcome: "receipt-only"},
		{name: "unknown", outcome: EmbeddedGoalEvaluationUnknown},
		{name: "done", outcome: EmbeddedGoalEvaluationDone},
		{name: "not-done-reserved", outcome: EmbeddedGoalEvaluationNotDone},
		{name: "not-done-started", outcome: EmbeddedGoalEvaluationNotDone, started: true},
		{name: "snooze-without-receipt", outcome: "snooze-only", snooze: true},
		{name: "off-history", outcome: EmbeddedGoalEvaluationNotDone, snooze: true, off: true},
		{name: "reset-history", outcome: EmbeddedGoalEvaluationNotDone, snooze: true, reset: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, _ := embeddedRedStore(t)
			b := goalTerminationHistory(t, s, "terminate-goal", tc.outcome, tc.snooze, tc.started)
			other := goalTerminationHistory(t, s, "unrelated-goal", EmbeddedGoalEvaluationNotDone, true, false)
			before := goalTerminationCounts(t, s, b.AgentID)
			otherBefore := goalTerminationCounts(t, s, other.AgentID)
			if tc.off {
				_, _, err := s.ApplyEmbeddedGoalCall(ctx, b, "goal-off", EmbeddedGoalCommand{Action: "off"})
				require.NoError(t, err)
			}
			if tc.reset {
				next := b
				next.SessionID, next.Epoch, next.Generation, next.OwnerToken = "next-session", b.Epoch+1, "next-generation", 0
				_, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET pi_session_id=?,session_epoch=?,runtime_generation=? WHERE agent_id=?`, next.SessionID, next.Epoch, next.Generation, b.AgentID)
				require.NoError(t, err)
				require.NoError(t, s.ResetEmbeddedSession(ctx, b, next, "root"))
				b = next
			}
			require.Equal(t, before, goalTerminationCounts(t, s, b.AgentID), "off/reset preserves historical rows")
			id := goalTerminationAdmission(t, s, b)
			// desired_state is already terminated, but the actual owner remains.
			updates := map[string]string{
				"embedded_goal_applied_receipts":   "goal_text='rewritten'",
				"embedded_goal_evaluations":        "claimed_at='rewritten'",
				"embedded_goal_continuations":      "reserved_at='rewritten'",
				"embedded_goal_snooze_calls":       "result_json='{}'",
				"embedded_goal_snooze_preferences": "due_at='rewritten'",
			}
			for i, table := range goalTerminationTables {
				if before[i] != 0 {
					_, err := s.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE agent_id=?", b.AgentID)
					require.ErrorContains(t, err, "immutable", table)
					_, err = s.db.ExecContext(ctx, "UPDATE "+table+" SET "+updates[table]+" WHERE agent_id=?", b.AgentID)
					require.Error(t, err, "history update remains guarded: "+table)
				}
			}
			require.Equal(t, before, goalTerminationCounts(t, s, b.AgentID))
			s.managedTerminationFault = func(stage string) error {
				if stage == "before_terminalize" {
					return sql.ErrTxDone
				}
				return nil
			}
			require.ErrorIs(t, s.CompleteManagedTermination(ctx, id), sql.ErrTxDone)
			s.managedTerminationFault = nil
			_, err := s.ManagedAgent(ctx, b.AgentID)
			require.NoError(t, err)
			require.Equal(t, before, goalTerminationCounts(t, s, b.AgentID), "late fault restores all history")
			requireGoalForeignKeys(t, s)
			require.NoError(t, s.CompleteManagedTermination(ctx, id))
			_, err = s.ManagedAgent(ctx, b.AgentID)
			require.ErrorIs(t, err, sql.ErrNoRows)
			require.Equal(t, make([]int, len(goalTerminationTables)), goalTerminationCounts(t, s, b.AgentID))
			require.Equal(t, otherBefore, goalTerminationCounts(t, s, other.AgentID))
			requireGoalForeignKeys(t, s)
		})
	}
}

func TestEmbeddedGoalHistoryMigration083PreservesRowsRollbackRetry(t *testing.T) {
	ctx := context.Background()
	migrations := DefaultMigrations()
	idx := migrationIndex(t, migrations, "083_embedded_goal_owner_cascade")
	path := filepath.Join(t.TempDir(), "migration.db")
	s, err := Open(ctx, path, Options{Migrations: migrations[:idx]})
	require.NoError(t, err)
	b := goalTerminationHistoryWithAppend(t, s, "migration-goal", EmbeddedGoalEvaluationNotDone, true, false, legacySDKAppend, true)
	// Snapshot every original column, including immutable history and transition
	// state. Later retention migrations add columns with their own coverage.
	originalColumns := map[string]string{}
	snapshot := func(s *Store) []string {
		var result []string
		for _, table := range goalTerminationTables {
			columnsSQL := originalColumns[table]
			if columnsSQL == "" {
				columnsSQL = "*"
			}
			rows, err := s.db.Query("SELECT "+columnsSQL+" FROM "+table+" WHERE agent_id=?", b.AgentID)
			require.NoError(t, err)
			columns, err := rows.Columns()
			require.NoError(t, err)
			originalColumns[table] = strings.Join(columns, ",")
			for rows.Next() {
				values := make([]any, len(columns))
				pointers := make([]any, len(columns))
				for i := range values {
					pointers[i] = &values[i]
				}
				require.NoError(t, rows.Scan(pointers...))
				data, err := json.Marshal(values)
				require.NoError(t, err)
				result = append(result, table+":"+string(data))
			}
			require.NoError(t, rows.Err())
			require.NoError(t, rows.Close())
		}
		return result
	}
	before := snapshot(s)
	require.NoError(t, s.Close())
	broken := append([]Migration(nil), migrations...)
	broken[idx].SQL += "; INSERT INTO absent_083_fault VALUES(1);"
	_, err = Open(ctx, path, Options{Migrations: broken})
	require.ErrorContains(t, err, "083_embedded_goal_owner_cascade")
	s, err = Open(ctx, path, Options{Migrations: migrations[:idx]})
	require.NoError(t, err)
	require.Equal(t, before, snapshot(s))
	require.Zero(t, embeddedRedCount(t, s, "schema_migrations WHERE id='083_embedded_goal_owner_cascade'"))
	requireGoalForeignKeys(t, s)
	require.NoError(t, s.Close())
	for range 2 {
		s, err = Open(ctx, path, Options{})
		require.NoError(t, err)
		require.Equal(t, before, snapshot(s))
		require.Equal(t, 1, embeddedRedCount(t, s, "schema_migrations WHERE id='083_embedded_goal_owner_cascade'"))
		requireGoalForeignKeys(t, s)
		require.NoError(t, s.Close())
	}
	s, err = Open(ctx, path, Options{})
	require.NoError(t, err)
	defer s.Close()
	id := goalTerminationAdmission(t, s, b)
	require.NoError(t, s.CompleteManagedTermination(ctx, id))
	require.Equal(t, make([]int, len(goalTerminationTables)), goalTerminationCounts(t, s, b.AgentID))
	requireGoalForeignKeys(t, s)
}
