package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// Supporting store-boundary regressions; public lifecycle coverage belongs to E2E.
func TestPreferenceRepairSameTurnTransition(t *testing.T) {
	for _, name := range []string{"goal_snooze", "workflow_snooze"} {
		t.Run(name, func(t *testing.T) {
			s, _, b, turn, _ := snoozeFixture(t)
			ctx := context.Background()
			wf, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "repair"})
			require.NoError(t, err)
			firstState, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "first", WorkflowID: &wf.ID})
			require.NoError(t, err)
			nextState, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "next", WorkflowID: &wf.ID})
			require.NoError(t, err)
			require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, b.AgentID, &wf.ID, &firstState.ID, nil))
			require.NoError(t, s.EnterEmbeddedNonMailProviderTurn(ctx, b, turn.UserSeq, turn.UserDigest))
			gen, err := s.MintEmbeddedProcessGeneration(ctx, "repair")
			require.NoError(t, err)
			save := func(id string) (EmbeddedGoalSnoozePreference, *EmbeddedGoalInvocation) {
				raw := []byte(`{"duration_ms":2000}`)
				intent, err := json.Marshal(map[string]any{"id": id, "name": name, "arguments": json.RawMessage(raw)})
				require.NoError(t, err)
				w, err := s.RecordEmbeddedGoalInvocation(ctx, b, gen, turn.UserSeq, turn.UserDigest, "", id, intent)
				require.NoError(t, err)
				require.NoError(t, s.CheckEmbeddedGoalInvocation(ctx, w))
				p, replay, err := s.SaveCurrentEmbeddedGoalSnoozePreference(ctx, b, turn, id, raw, w)
				require.NoError(t, err)
				require.False(t, replay)
				return p, w
			}
			old, _ := save("before")
			require.NoError(t, s.SetManagedAgentActiveWorkflowState(ctx, b.AgentID, &nextState.ID))
			retired, err := s.CurrentEmbeddedGoalSnoozePreference(ctx, b.AgentID)
			require.NoError(t, err)
			require.Nil(t, retired)
			fresh, w := save("after")
			require.Equal(t, old.PolicyVersion+1, fresh.PolicyVersion)
			require.Equal(t, 2, embeddedRedCount(t, s, "embedded_goal_snooze_calls"))
			require.Equal(t, 1, embeddedRedCount(t, s, "embedded_goal_snooze_preferences"))
			_, _, err = s.SaveCurrentEmbeddedGoalSnoozePreference(ctx, b, turn, "after", []byte(`{"duration_ms":2000}`), w)
			require.Error(t, err, "current call cannot replay")
			current, err := s.CurrentEmbeddedGoalSnoozePreference(ctx, b.AgentID)
			require.NoError(t, err)
			require.Equal(t, fresh.CallID, current.CallID)
		})
	}
}

func TestPreferenceRepairPriorLongNewShort(t *testing.T) {
	s, _, b, turn, _ := snoozeFixture(t)
	ctx := context.Background()
	old, _, err := s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "long", []byte(`{"duration_ms":60000}`))
	require.NoError(t, err)
	user := receiptProviderUser(t, s, b, "unsnoozed steering")
	policy, err := s.ManagedGoalPolicyFor(ctx, b.AgentID)
	require.NoError(t, err)
	require.Equal(t, old.DueAt, policy.SnoozedUntil, "new unsnoozed turn preserves policy D")
	current, err := s.CurrentEmbeddedGoalSnoozePreference(ctx, b.AgentID)
	require.NoError(t, err)
	require.Equal(t, old.CallID, current.CallID)
	turn.UserSeq, turn.UserDigest = user.Sequence, user.Digest
	fresh, _, err := s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "short", []byte(`{"duration_ms":2000}`))
	require.NoError(t, err)
	current, err = s.CurrentEmbeddedGoalSnoozePreference(ctx, b.AgentID)
	require.NoError(t, err)
	require.Equal(t, fresh.CallID, current.CallID, "idle reader must not select superseded long preference")
	var active int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_goal_snooze_preferences WHERE agent_id=? AND active=1`, b.AgentID).Scan(&active))
	require.Equal(t, 1, active)
}

func TestPreferenceRepairCorruptSavedEvidenceAtomic(t *testing.T) {
	for _, mode := range []string{"identity", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			s, b, k := evaluationFixture(t)
			defer s.Close()
			ctx := context.Background()
			p, _, err := s.SaveEmbeddedGoalSnoozePreference(ctx, b, EmbeddedGoalSnoozeTurn{UserSeq: k.UserSeq, UserDigest: k.UserDigest, GoalRevision: k.GoalRevision}, "corrupt", []byte(`{"duration_ms":2000}`))
			require.NoError(t, err)
			// Corruption only: bypass call UPDATE guard, not reservation/verdict guards.
			_, err = s.db.Exec(`DROP TRIGGER embedded_goal_snooze_call_no_update`)
			require.NoError(t, err)
			if mode == "identity" {
				p.UserDigest = "wrong"
			} else {
				p.DueAt = "2026-01-01T00:00:00Z"
			}
			raw, err := json.Marshal(p)
			require.NoError(t, err)
			_, err = s.db.Exec(`UPDATE embedded_goal_snooze_calls SET result_json=? WHERE call_id='corrupt'`, string(raw))
			require.NoError(t, err)
			receiptForEvaluation(t, s, b, &k)
			_, _, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
			require.NoError(t, err)
			_, changed, err := s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
			require.Error(t, err)
			require.False(t, changed)
			require.Zero(t, embeddedRedCount(t, s, "embedded_goal_continuations"))
			var state string
			require.NoError(t, s.db.QueryRow(`SELECT state FROM embedded_goal_evaluations WHERE agent_id=?`, b.AgentID).Scan(&state))
			require.Equal(t, EmbeddedGoalEvaluationUnknown, state)
		})
	}
}

// An installed 119 database retains its historical row and every unrelated
// trigger. Only the preference transition guard changes, once, across reopen.
func TestPreferenceRepairForwardMigrationAndGuards(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "installed.db")
	migrations := DefaultMigrations()
	end119 := migrationIndex(t, migrations, "119_structured_workflow_schedules") + 1
	s, err := Open(ctx, path, Options{Migrations: migrations[:end119]})
	require.NoError(t, err)
	b := goalBind(t, s, "forward-repair", "session", 1, "gen", "worker")
	user := legacySDKAppend(t, s, b, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "work"}}})
	require.NoError(t, s.EnterEmbeddedNonMailProviderTurn(ctx, b, user.Sequence, user.Digest))
	goalAPIPolicy(t, s, b, true, "finish")
	turn := EmbeddedGoalSnoozeTurn{UserSeq: user.Sequence, UserDigest: user.Digest, GoalRevision: 1}
	p := legacySnoozePreference(t, s, b, turn, "original", 2000)
	_, err = s.db.Exec(`UPDATE embedded_goal_snooze_preferences SET active=0`)
	require.NoError(t, err)
	triggers := func(s *Store) map[string]string {
		rows, err := s.db.Query(`SELECT name,sql FROM sqlite_master WHERE type='trigger' AND name<>'embedded_goal_snooze_preference_transition'`)
		require.NoError(t, err)
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var name, sql string
			require.NoError(t, rows.Scan(&name, &sql))
			out[name] = sql
		}
		require.NoError(t, rows.Err())
		return out
	}
	before := triggers(s)
	require.NoError(t, s.Close())
	for reopen := 0; reopen < 2; reopen++ {
		s, err = Open(ctx, path, Options{Migrations: migrations[:end119+1]})
		require.NoError(t, err)
		require.Equal(t, before, triggers(s))
		var installed int
		require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE id='120_embedded_goal_snooze_preference_reactivation'`).Scan(&installed))
		require.Equal(t, 1, installed)
		var active int
		require.NoError(t, s.db.QueryRow(`SELECT active FROM embedded_goal_snooze_preferences WHERE call_id='original'`).Scan(&active))
		require.Zero(t, active)
		// The historical API can replay its result, but cannot restore activity.
		_, replay, err := legacySnoozeReplay(s, b, turn, "original", []byte(`{"duration_ms":2000}`))
		require.NoError(t, err)
		require.True(t, replay)
		require.NoError(t, s.db.QueryRow(`SELECT active FROM embedded_goal_snooze_preferences WHERE call_id='original'`).Scan(&active))
		require.Zero(t, active)
		// Create immutable raw call evidence for both accepted and rejected updates.
		if reopen == 0 {
			for _, fixture := range []struct {
				id, digest, due string
				version         int
			}{
				{"matching", turn.UserDigest, p.DueAt, 2},
				{"stale", "stale", p.DueAt, 2},
				{"wrong-version", turn.UserDigest, p.DueAt, 9},
				{"wrong-due", turn.UserDigest, "wrong", 2},
			} {
				_, err = s.db.Exec(`INSERT INTO embedded_goal_snooze_calls SELECT ?,agent_id,session_id,epoch,generation,role,user_seq,?,goal_revision,args_json,json_set(result_json,'$.DueAt',?),? FROM embedded_goal_snooze_calls WHERE call_id='original'`, fixture.id, fixture.digest, fixture.due, fixture.version)
				require.NoError(t, err)
			}
		}
		if reopen == 0 {
			_, err = s.db.Exec(`INSERT INTO embedded_goal_snooze_calls SELECT 'missing-due',agent_id,session_id,epoch,generation,role,user_seq,user_digest,goal_revision,args_json,'{}',2 FROM embedded_goal_snooze_calls WHERE call_id='original'`)
			require.NoError(t, err)
			_, err = s.db.Exec(`INSERT INTO embedded_goal_snooze_calls SELECT 'malformed',agent_id,session_id,epoch,generation,role,user_seq,user_digest,goal_revision,args_json,'{',2 FROM embedded_goal_snooze_calls WHERE call_id='original'`)
			require.Error(t, err, "malformed result JSON is rejected by existing CHECK")
		}
		for _, startActive := range []bool{false, true} {
			if startActive {
				// Valid new evidence activates; rollback restores the original fixture.
				_, err = s.db.Exec(`BEGIN; UPDATE embedded_goal_snooze_preferences SET active=1,policy_version=2,call_id='matching';`)
				require.NoError(t, err)
				// Test replacements relative to version 2 with version-3 fixtures.
				_, err = s.db.Exec(`INSERT INTO embedded_goal_snooze_calls SELECT 'replacement-'||call_id,agent_id,session_id,epoch,generation,role,user_seq,user_digest,goal_revision,args_json,result_json,CASE WHEN policy_version=2 THEN 3 ELSE policy_version END FROM embedded_goal_snooze_calls WHERE call_id IN ('stale','wrong-version','wrong-due','missing-due')`)
				require.NoError(t, err)
			}
			for _, set := range []string{
				`active=1`,
				`active=1,policy_version=2`,
				`active=1,policy_version=3,call_id='matching'`,
				`active=1,policy_version=2,call_id='missing'`,
				`active=1,policy_version=2,call_id='stale'`,
				`active=1,policy_version=2,call_id='wrong-version'`,
				`active=1,policy_version=2,call_id='wrong-due'`,
				`active=1,policy_version=2,call_id='missing-due'`,
				`active=1,policy_version=2,call_id='matching',generation='stale'`,
				`active=1,policy_version=2,call_id='matching',due_at='wrong'`,
			} {
				if startActive {
					set = strings.ReplaceAll(set, "policy_version=2", "policy_version=3")
					for _, id := range []string{"stale", "wrong-version", "wrong-due", "missing-due"} {
						set = strings.ReplaceAll(set, "call_id='"+id+"'", "call_id='replacement-"+id+"'")
					}
				}
				_, err := s.db.Exec(`UPDATE embedded_goal_snooze_preferences SET ` + set)
				require.Error(t, err, set)
			}
			if startActive {
				_, err = s.db.Exec(`ROLLBACK`)
				require.NoError(t, err)
			}
		}
		_, err = s.db.Exec(`UPDATE embedded_goal_snooze_calls SET policy_version=2 WHERE call_id='original'`)
		require.Error(t, err)
		_, err = s.db.Exec(`DELETE FROM embedded_goal_snooze_calls WHERE call_id='original'`)
		require.Error(t, err)
		_, err = s.db.Exec(`DELETE FROM embedded_goal_snooze_preferences`)
		require.Error(t, err)
		require.Equal(t, "original", p.CallID)
		require.NoError(t, s.Close())
	}
	// A new fully fenced call, not replay, can reactivate the retained row.
	s, err = Open(ctx, path, Options{})
	require.NoError(t, err)
	fresh, replay, err := s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "fresh", []byte(`{"duration_ms":1000}`))
	require.NoError(t, err)
	require.False(t, replay)
	require.Equal(t, int64(2), fresh.PolicyVersion)
	current, err := s.CurrentEmbeddedGoalSnoozePreference(ctx, b.AgentID)
	require.NoError(t, err)
	require.Equal(t, fresh.CallID, current.CallID)
	require.NoError(t, s.Close())
	s, err = Open(ctx, path, Options{})
	require.NoError(t, err)
	defer s.Close()
	current, err = s.CurrentEmbeddedGoalSnoozePreference(ctx, b.AgentID)
	require.NoError(t, err)
	require.Equal(t, fresh.CallID, current.CallID)
}

// Both the preference write and superseded-row retirement are required parts
// of the call/policy transaction. No false success or partial retirement.
func TestPreferenceRepairWriteFailureRollsBack(t *testing.T) {
	for _, mode := range []string{"ignored-upsert", "retirement"} {
		t.Run(mode, func(t *testing.T) {
			s, _, b, turn, _ := snoozeFixture(t)
			ctx := context.Background()
			old, _, err := s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "old", []byte(`{"duration_ms":60000}`))
			require.NoError(t, err)
			trigger := `CREATE TRIGGER repair_fault BEFORE UPDATE ON embedded_goal_snooze_preferences BEGIN SELECT RAISE(IGNORE); END`
			if mode == "retirement" {
				user := receiptProviderUser(t, s, b, "next")
				turn.UserSeq, turn.UserDigest = user.Sequence, user.Digest
				trigger = `CREATE TRIGGER repair_fault BEFORE UPDATE ON embedded_goal_snooze_preferences WHEN NEW.active=0 BEGIN SELECT RAISE(ABORT,'retirement fault'); END`
			}
			_, err = s.db.Exec(trigger)
			require.NoError(t, err)
			_, replay, err := s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "failed", []byte(`{"duration_ms":1000}`))
			require.Error(t, err)
			require.False(t, replay)
			require.Equal(t, 1, embeddedRedCount(t, s, "embedded_goal_snooze_calls"))
			require.Equal(t, 1, embeddedRedCount(t, s, "embedded_goal_snooze_preferences"))
			policy, err := s.ManagedGoalPolicyFor(ctx, b.AgentID)
			require.NoError(t, err)
			require.Equal(t, old.DueAt, policy.SnoozedUntil)
			current, err := s.CurrentEmbeddedGoalSnoozePreference(ctx, b.AgentID)
			require.NoError(t, err)
			require.Equal(t, old.CallID, current.CallID)
		})
	}
}
