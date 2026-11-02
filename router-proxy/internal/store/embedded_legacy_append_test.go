package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// Historical fixtures must use the pre-121 INSERT, not today's retention-aware
// append API. Keep the real owner fence, chain digest and installed SQL guards.
func legacySDKAppend(t *testing.T, s *Store, b EmbeddedBinding, message ai.Message) EmbeddedEntry {
	t.Helper()
	body, err := json.Marshal(message)
	require.NoError(t, err)
	require.NoError(t, embeddedBody(body))
	ctx := context.Background()
	var entry EmbeddedEntry
	require.NoError(t, s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		seq, parent, err := s.embeddedHead(ctx, b)
		if err != nil {
			return err
		}
		entry = EmbeddedEntry{Sequence: seq + 1, ParentDigest: parent, Kind: string(message.MessageRole()), Body: body}
		entry.Digest = embeddedEntryDigest(b, entry)
		_, err = s.db.ExecContext(ctx, `INSERT INTO embedded_entries(agent_id,session_id,epoch,generation,seq,parent_digest,kind,body,digest) VALUES(?,?,?,?,?,?,?,?,?)`, b.AgentID, b.SessionID, b.Epoch, b.Generation, entry.Sequence, entry.ParentDigest, entry.Kind, entry.Body, entry.Digest)
		return err
	}))
	return entry
}

// Pre-retention snooze SQL/result format. Current replay APIs also require the
// later execution_json column, so installed-schema fixtures cannot call them.
func legacySnoozePreference(t *testing.T, s *Store, b EmbeddedBinding, turn EmbeddedGoalSnoozeTurn, callID string, durationMS int64) EmbeddedGoalSnoozePreference {
	t.Helper()
	var role string
	require.NoError(t, s.db.QueryRow(`SELECT role FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&role))
	p := EmbeddedGoalSnoozePreference{EmbeddedGoalSnoozeTurn: turn, AgentID: b.AgentID, SessionID: b.SessionID, Epoch: b.Epoch, Generation: b.Generation, Role: role, PolicyVersion: 1, CallID: callID, DueAt: formatTimestamp(s.clock.Now().Add(time.Duration(durationMS) * time.Millisecond)), DurationMS: durationMS, Active: true}
	result, err := json.Marshal(p)
	require.NoError(t, err)
	args, err := json.Marshal(EmbeddedGoalSnoozeArguments{DurationMS: durationMS})
	require.NoError(t, err)
	require.NoError(t, s.runImmediate(context.Background(), func() error {
		if err := s.embeddedFence(context.Background(), b); err != nil {
			return err
		}
		_, err := s.db.Exec(`INSERT INTO embedded_goal_snooze_calls(call_id,agent_id,session_id,epoch,generation,role,user_seq,user_digest,goal_revision,args_json,result_json,policy_version) VALUES(?,?,?,?,?,?,?,?,?,?,?,1)`, callID, b.AgentID, b.SessionID, b.Epoch, b.Generation, role, turn.UserSeq, turn.UserDigest, turn.GoalRevision, string(args), string(result))
		if err != nil {
			return err
		}
		_, err = s.db.Exec(`INSERT INTO embedded_goal_snooze_preferences(agent_id,session_id,epoch,generation,role,user_seq,user_digest,goal_revision,policy_version,call_id,due_at,active) VALUES(?,?,?,?,?,?,?,?,1,?,?,1)`, b.AgentID, b.SessionID, b.Epoch, b.Generation, role, turn.UserSeq, turn.UserDigest, turn.GoalRevision, callID, p.DueAt)
		return err
	}))
	return p
}

// Build the original 082 receipt/verdict/continuation with its installed
// transition/FK guards, after goalSDKCommand's checked SDK policy producer.
func legacyGoalEvaluationHistory(t *testing.T, s *Store, b EmbeddedBinding, user EmbeddedEntry, policy *ManagedGoalPolicy, id string) {
	t.Helper()
	ctx := context.Background()
	now := formatTimestamp(s.clock.Now())
	var role string
	require.NoError(t, s.db.QueryRow(`SELECT role FROM embedded_sessions WHERE agent_id=?`, b.AgentID).Scan(&role))
	_, err := s.db.Exec(`INSERT INTO embedded_goal_applied_receipts(turn_key,agent_id,session_id,epoch,generation,role,turn_entry_seq,turn_entry_digest,goal_revision,goal_enabled,goal_text,applied_at) VALUES(?,?,?,?,?,?,?,?,?,1,?,?)`, id+"-turn", b.AgentID, b.SessionID, b.Epoch, b.Generation, role, user.Sequence, user.Digest, policy.DesiredRevision, policy.Config.Goal, now)
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE managed_agent_policies SET applied_revision=desired_revision,projection_state='applied' WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	final := legacySDKAppend(t, s, b, ai.AssistantMessage{StopReason: ai.StopStop})
	_, err = s.db.Exec(`INSERT INTO embedded_goal_evaluations(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,owner_token,final_seq,final_digest,state,claimed_at) VALUES(?,?,?,?,?,?,?,?,?,?,'UNKNOWN',?)`, b.AgentID, b.SessionID, b.Epoch, b.Generation, user.Sequence, user.Digest, policy.DesiredRevision, b.OwnerToken, final.Sequence, final.Digest, now)
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE embedded_goal_evaluations SET state='NOT_DONE',reason='test verdict',completed_at=? WHERE agent_id=?`, now, b.AgentID)
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `INSERT INTO embedded_goal_continuations(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,state,disposition,reserved_at,due_at) SELECT agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,'reserved','pending',?,due_at FROM embedded_goal_snooze_preferences WHERE agent_id=?`, now, b.AgentID)
	require.NoError(t, err)
}

// The historical replay only returns the immutable saved result; it cannot
// reactivate a preference. Used against migration120 without later execution SQL.
func legacySnoozeReplay(s *Store, b EmbeddedBinding, turn EmbeddedGoalSnoozeTurn, callID string, args []byte) (EmbeddedGoalSnoozePreference, bool, error) {
	var p EmbeddedGoalSnoozePreference
	var savedArgs, result string
	err := s.db.QueryRow(`SELECT args_json,result_json FROM embedded_goal_snooze_calls WHERE call_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=? AND user_seq=? AND user_digest=? AND goal_revision=?`, callID, b.AgentID, b.SessionID, b.Epoch, b.Generation, turn.UserSeq, turn.UserDigest, turn.GoalRevision).Scan(&savedArgs, &result)
	if err != nil {
		return p, false, err
	}
	if savedArgs != string(args) {
		return p, false, fmt.Errorf("conflicting historical snooze args")
	}
	err = json.Unmarshal([]byte(result), &p)
	return p, err == nil, err
}
