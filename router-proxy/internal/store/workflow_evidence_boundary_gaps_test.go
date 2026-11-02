package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

var boundaryEvidenceTables = []string{
	"embedded_goal_snooze_preferences", "embedded_goal_snooze_calls",
	"embedded_goal_continuations", "embedded_goal_evaluations", "embedded_goal_applied_receipts",
}

// Compare every saved column, including full identities, D, R and call dependencies.
// JSON avoids driver-owned byte buffers and preserves SQL NULL distinctly.
func boundaryRows(t *testing.T, s *Store, query string, args ...any) []string {
	t.Helper()
	rows, err := s.db.Query(query, args...)
	require.NoError(t, err)
	defer rows.Close()
	columns, err := rows.Columns()
	require.NoError(t, err)
	result := []string{}
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		require.NoError(t, rows.Scan(dest...))
		for i, v := range values {
			if b, ok := v.([]byte); ok {
				values[i] = string(b)
			}
		}
		encoded, err := json.Marshal(values)
		require.NoError(t, err)
		result = append(result, string(encoded))
	}
	require.NoError(t, rows.Err())
	return result
}

func boundaryClosure(t *testing.T, s *Store, b EmbeddedBinding, k EmbeddedGoalEvaluationKey) map[string][]string {
	t.Helper()
	result := map[string][]string{}
	for _, table := range boundaryEvidenceTables {
		seq, digest := "user_seq", "user_digest"
		if table == "embedded_goal_applied_receipts" {
			seq, digest = "turn_entry_seq", "turn_entry_digest"
		}
		query := `SELECT * FROM ` + table + ` WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND ` + seq + `=? AND ` + digest + `=? AND goal_revision=?`
		args := []any{b.AgentID, b.SessionID, b.Epoch, b.Generation, k.UserSeq, k.UserDigest, k.GoalRevision}
		if table == "embedded_goal_evaluations" {
			query += ` AND final_seq=? AND final_digest=?`
			args = append(args, k.FinalSeq, k.FinalDigest)
		}
		result[table] = boundaryRows(t, s, query, args...)
	}
	return result
}

func boundaryAssertEmpty(t *testing.T, closure map[string][]string) {
	t.Helper()
	for _, table := range boundaryEvidenceTables {
		require.Empty(t, closure[table], table)
	}
}

// Denials may advance only the monotonic cutoff; everything else is byte-exact.
func boundaryDeniedState(t *testing.T, s *Store, b EmbeddedBinding) map[string][]string {
	t.Helper()
	result := map[string][]string{}
	for _, table := range append(append([]string{}, boundaryEvidenceTables...), "managed_agent_policies", "embedded_entries", "embedded_effects", "embedded_nonmail_provider_entries") {
		result[table] = boundaryRows(t, s, `SELECT * FROM `+table+` WHERE agent_id=? ORDER BY rowid`, b.AgentID)
	}
	// Include every session column except the explicitly permitted cutoff.
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info('embedded_sessions') WHERE name<>'evidence_expired_through_ms' ORDER BY cid`)
	require.NoError(t, err)
	columns := ""
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		if columns != "" {
			columns += ","
		}
		columns += name
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	result["binding"] = boundaryRows(t, s, `SELECT `+columns+` FROM embedded_sessions WHERE agent_id=?`, b.AgentID)
	return result
}

func boundaryCutoff(t *testing.T, s *Store, b EmbeddedBinding) int64 {
	t.Helper()
	var cutoff int64
	require.NoError(t, s.db.QueryRow(`SELECT evidence_expired_through_ms FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&cutoff))
	return cutoff
}

func boundaryDeny(t *testing.T, s *Store, b EmbeddedBinding, call func() error) {
	t.Helper()
	// A live owner is a prerequisite, not inferred from the returned error.
	require.NoError(t, s.runImmediate(context.Background(), func() error { return s.embeddedFence(context.Background(), b) }))
	before := boundaryDeniedState(t, s, b)
	cutoff := boundaryCutoff(t, s, b)
	want := max(cutoff, s.clock.Now().UnixMilli()-embeddedEvidenceAgeMS)
	require.ErrorIs(t, call(), ErrEmbeddedEvidenceExpired)
	require.Equal(t, before, boundaryDeniedState(t, s, b), "denial must not mutate evidence, policy/D, transcript, effects or binding")
	require.Equal(t, want, boundaryCutoff(t, s, b), "only expected monotonic cutoff advancement is allowed")
	var n int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&n))
	require.Zero(t, n)
}

func boundarySweep(t *testing.T, s *Store) {
	t.Helper()
	_, err := s.SweepLifecycle(context.Background(), DefaultRetentionPolicy)
	require.NoError(t, err)
}

func TestWorkflowRetentionBoundaryEightPendingExactR(t *testing.T) {
	s, b, _ := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	clock := deterministic.NewFixedClock(s.clock.Now().UTC().Truncate(time.Millisecond))
	s.clock = clock
	a := clock.Now().UnixMilli()
	d := clock.Now().Add(24 * time.Hour)
	r := max(a+embeddedEvidenceAgeMS, d.UnixMilli()) + embeddedEvidenceAgeMS
	var keys []EmbeddedGoalEvaluationKey
	for i := 0; i < 8; i++ {
		user := receiptProviderUser(t, s, b, "pending")
		turn := EmbeddedGoalSnoozeTurn{UserSeq: user.Sequence, UserDigest: user.Digest, GoalRevision: 1}
		p, replay, err := s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, fmt.Sprintf("boundary-call-%d", i), []byte(`{"duration_ms":86400000}`))
		require.NoError(t, err)
		require.False(t, replay)
		require.Equal(t, formatTimestamp(d), p.DueAt)
		_, _, err = s.CommitEmbeddedGoalAppliedReceipt(ctx, b, EmbeddedGoalAppliedReceiptRequest{TurnKey: fmt.Sprintf("boundary-turn-%d", i), TurnEntrySeq: user.Sequence, TurnEntryDigest: user.Digest, GoalRevision: 1})
		require.NoError(t, err)
		final := receiptSDKAppend(t, s, b, ai.AssistantMessage{StopReason: ai.StopStop})
		k := EmbeddedGoalEvaluationKey{UserSeq: user.Sequence, UserDigest: user.Digest, GoalRevision: 1, FinalSeq: final.Sequence, FinalDigest: final.Digest}
		_, _, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
		require.NoError(t, err)
		_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
		require.NoError(t, err)
		var savedA, savedR int64
		var savedD string
		require.NoError(t, s.db.QueryRow(`SELECT retention_admitted_at_ms FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND seq=? AND digest=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, k.UserSeq, k.UserDigest).Scan(&savedA))
		require.Equal(t, a, savedA)
		require.NoError(t, s.db.QueryRow(`SELECT due_at,retention_expires_at_ms FROM embedded_goal_continuations WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND user_seq=? AND user_digest=? AND goal_revision=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, k.UserSeq, k.UserDigest, k.GoalRevision).Scan(&savedD, &savedR))
		require.Equal(t, formatTimestamp(d), savedD)
		require.Equal(t, r, savedR)
		keys = append(keys, k)
	}
	policy := boundaryRows(t, s, `SELECT * FROM managed_agent_policies WHERE agent_id=?`, b.AgentID)
	transcript := boundaryRows(t, s, `SELECT * FROM embedded_entries WHERE agent_id=? ORDER BY seq`, b.AgentID)
	// All eight are genuinely pending, with complete dependency graphs.
	for _, k := range keys {
		for table, rows := range boundaryClosure(t, s, b, k) {
			require.Len(t, rows, 1, table)
		}
	}
	require.NoError(t, clock.Advance(d.Sub(clock.Now())))
	_, started, err := s.StartEmbeddedGoalContinuation(ctx, b, keys[0])
	require.NoError(t, err)
	require.True(t, started)
	token, err := s.EnterEmbeddedSDKExecution(ctx, b, &keys[0])
	require.NoError(t, err)
	require.Positive(t, token)
	snapshots := make([]map[string][]string, len(keys))
	for i, k := range keys {
		snapshots[i] = boundaryClosure(t, s, b, k)
	}
	execution := boundaryRows(t, s, `SELECT execution_json FROM embedded_sessions WHERE agent_id=?`, b.AgentID)
	require.NoError(t, clock.Advance(time.UnixMilli(r-1).Sub(clock.Now())))
	boundarySweep(t, s)
	require.Equal(t, r-1-embeddedEvidenceAgeMS, boundaryCutoff(t, s, b))
	for i, k := range keys {
		require.Equal(t, snapshots[i], boundaryClosure(t, s, b, k), "R-1 full key %d; original D/R/dependencies unchanged", i)
	}
	require.NoError(t, clock.Advance(time.Millisecond))
	boundarySweep(t, s)
	require.Equal(t, r-embeddedEvidenceAgeMS, boundaryCutoff(t, s, b))
	for i, k := range keys {
		if i == 0 {
			require.Equal(t, snapshots[i], boundaryClosure(t, s, b, k), "actual running source stays exact at R")
		} else {
			boundaryAssertEmpty(t, boundaryClosure(t, s, b, k))
		}
	}
	require.Equal(t, execution, boundaryRows(t, s, `SELECT execution_json FROM embedded_sessions WHERE agent_id=?`, b.AgentID))
	require.Equal(t, policy, boundaryRows(t, s, `SELECT * FROM managed_agent_policies WHERE agent_id=?`, b.AgentID))
	require.Equal(t, transcript, boundaryRows(t, s, `SELECT * FROM embedded_entries WHERE agent_id=? ORDER BY seq`, b.AgentID))
	require.NoError(t, s.ClearEmbeddedExecution(ctx, b, token))
	boundarySweep(t, s)
	for _, k := range keys {
		boundaryAssertEmpty(t, boundaryClosure(t, s, b, k))
		boundaryDeny(t, s, b, func() error { _, _, err := s.StartEmbeddedGoalContinuation(ctx, b, k); return err })
		boundaryDeny(t, s, b, func() error {
			_, _, err := s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
			return err
		})
	}
	for _, table := range boundaryEvidenceTables {
		require.Empty(t, boundaryRows(t, s, `SELECT * FROM `+table+` WHERE agent_id=?`, b.AgentID), table)
	}
	require.Equal(t, policy, boundaryRows(t, s, `SELECT * FROM managed_agent_policies WHERE agent_id=?`, b.AgentID))
}

func TestWorkflowRetentionBoundaryReceiptRecreationAfterPrune(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	clock := deterministic.NewFixedClock(s.clock.Now())
	s.clock = clock
	request := EmbeddedGoalAppliedReceiptRequest{TurnKey: "boundary-receipt", TurnEntrySeq: k.UserSeq, TurnEntryDigest: k.UserDigest, GoalRevision: k.GoalRevision}
	_, replay, err := s.CommitEmbeddedGoalAppliedReceipt(ctx, b, request)
	require.NoError(t, err)
	require.False(t, replay)
	_, replay, err = s.CommitEmbeddedGoalAppliedReceipt(ctx, b, request)
	require.NoError(t, err)
	require.True(t, replay)
	require.NoError(t, clock.Advance(30*24*time.Hour))
	boundarySweep(t, s)
	boundaryAssertEmpty(t, boundaryClosure(t, s, b, k))
	// No final or later user: this is still the checked provider user/current policy.
	require.NoError(t, s.embeddedGoalReceiptProviderTurn(ctx, b, EmbeddedEntry{Sequence: k.UserSeq, Digest: k.UserDigest, Body: func() []byte {
		var body []byte
		require.NoError(t, s.db.QueryRow(`SELECT body FROM embedded_entries WHERE agent_id=? AND seq=?`, b.AgentID, k.UserSeq).Scan(&body))
		return body
	}()}))
	for _, id := range []string{request.TurnKey, "boundary-recreate"} {
		request.TurnKey = id
		boundaryDeny(t, s, b, func() error { _, _, err := s.CommitEmbeddedGoalAppliedReceipt(ctx, b, request); return err })
	}
}

// Real Record/Check calls mint one-use process-local witnesses. Never fabricate
// admissions or reset their consumed flags to make an expiry assertion pass.
func boundaryInvocation(t *testing.T, s *Store, b EmbeddedBinding, turn EmbeddedGoalSnoozeTurn, process int64, id string) *EmbeddedGoalInvocation {
	t.Helper()
	intent, err := json.Marshal(map[string]any{"id": id, "name": "goal_snooze", "arguments": map[string]any{"duration_ms": 1000}})
	require.NoError(t, err)
	w, err := s.RecordEmbeddedGoalInvocation(context.Background(), b, process, turn.UserSeq, turn.UserDigest, "", id, intent)
	require.NoError(t, err)
	require.NoError(t, s.CheckEmbeddedGoalInvocation(context.Background(), w))
	return w
}

func TestWorkflowRetentionBoundaryCheckedSnoozeAfterPrune(t *testing.T) {
	for _, id := range []string{"same-call", "new-call"} {
		t.Run(id, func(t *testing.T) {
			s, _, b, turn, clock := snoozeFixture(t)
			ctx := context.Background()
			process, err := s.MintEmbeddedProcessGeneration(ctx, "boundary")
			require.NoError(t, err)
			w := boundaryInvocation(t, s, b, turn, process, id)
			// Save original domain evidence without consuming the checked witness.
			// Same-call probes replay gate order; new-call probes fresh admission.
			_, _, err = s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "same-call", []byte(`{"duration_ms":1000}`))
			require.NoError(t, err)
			require.NoError(t, clock.Advance(30*24*time.Hour))
			boundarySweep(t, s)
			k := EmbeddedGoalEvaluationKey{UserSeq: turn.UserSeq, UserDigest: turn.UserDigest, GoalRevision: turn.GoalRevision}
			boundaryAssertEmpty(t, boundaryClosure(t, s, b, k))
			require.Equal(t, b, w.binding)
			require.Equal(t, process, w.process)
			require.True(t, w.executing)
			require.False(t, w.ready)
			require.False(t, w.appended)
			require.False(t, w.consumed)
			// Persisted intent is still fresh unknown; no incidental consumed-witness denial.
			var state string
			require.NoError(t, s.db.QueryRow(`SELECT state FROM embedded_effects WHERE effect_id=?`, id).Scan(&state))
			require.Equal(t, "unknown", state)
			boundaryDeny(t, s, b, func() error {
				_, _, err := s.SaveCurrentEmbeddedGoalSnoozePreference(ctx, b, turn, id, []byte(`{"duration_ms":1000}`), w)
				return err
			})
			// Also exercise admission itself after pruning, not only a held witness.
			for _, callID := range []string{"same-call", "new-after-prune"} {
				intent, err := json.Marshal(map[string]any{"id": callID, "name": "goal_snooze", "arguments": map[string]any{"duration_ms": 1000}})
				require.NoError(t, err)
				boundaryDeny(t, s, b, func() error {
					got, err := s.RecordEmbeddedGoalInvocation(ctx, b, process, turn.UserSeq, turn.UserDigest, "", callID, intent)
					require.Nil(t, got)
					return err
				})
			}
		})
	}
}
