package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func evaluationFixture(t *testing.T) (*Store, EmbeddedBinding, EmbeddedGoalEvaluationKey) {
	t.Helper()
	s, _ := embeddedRedStore(t)
	b := goalBind(t, s, "eval-"+t.Name(), "session", 1, "gen", "root")
	user := receiptProviderUser(t, s, b, "go")
	goalAPIPolicy(t, s, b, true, "finish")
	return s, b, EmbeddedGoalEvaluationKey{UserSeq: user.Sequence, UserDigest: user.Digest, GoalRevision: 1}
}

// API-installed policy has no SDK mutation producer. Only the runtime fence
// is SQL fixture data; policy and successful receipt use the real Store APIs.
func goalAPIPolicy(t *testing.T, s *Store, b EmbeddedBinding, enabled bool, goal string) *ManagedGoalPolicy {
	t.Helper()
	_, err := s.db.Exec(`UPDATE managed_agents SET process_pid=123,process_start_marker='receipt-start',process_incarnation='receipt-process' WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	fence := ManagedRuntimeFence{AgentID: b.AgentID, PiSessionID: b.SessionID, SessionEpoch: b.Epoch, RuntimeGeneration: b.Generation, ProcessPID: 123, ProcessStartMarker: "receipt-start", ProcessIncarnation: "receipt-process", ManifestRevision: 1, ManifestHash: testManifestHash}
	config := ManagedGoalConfig{Enabled: enabled, Settings: DefaultManagedGoalSettings()}
	if enabled {
		config.Goal = goal
	}
	policy, err := s.UpdateManagedGoalPolicy(context.Background(), fence, config)
	require.NoError(t, err)
	return policy
}

func receiptForEvaluation(t *testing.T, s *Store, b EmbeddedBinding, k *EmbeddedGoalEvaluationKey) {
	t.Helper()
	require.Zero(t, k.FinalSeq, "receipt must precede the original checked stop")
	_, replay, err := s.CommitEmbeddedGoalAppliedReceipt(context.Background(), b, EmbeddedGoalAppliedReceiptRequest{TurnKey: "turn", TurnEntrySeq: k.UserSeq, TurnEntryDigest: k.UserDigest, GoalRevision: k.GoalRevision})
	require.NoError(t, err)
	require.False(t, replay)
	body, err := json.Marshal(ai.AssistantMessage{StopReason: ai.StopStop})
	require.NoError(t, err)
	last, err := s.AppendEmbeddedEntry(context.Background(), b, "assistant", body)
	require.NoError(t, err)
	k.FinalSeq, k.FinalDigest = last.Sequence, last.Digest
}

func TestEmbeddedEvaluationClaimCompleteReplay(t *testing.T) {
	missing, missingBinding, missingKey := evaluationFixture(t)
	ctx := context.Background()
	final := receiptSDKAppend(t, missing, missingBinding, ai.AssistantMessage{StopReason: ai.StopStop})
	missingKey.FinalSeq, missingKey.FinalDigest = final.Sequence, final.Digest
	_, claimed, err := missing.ClaimEmbeddedGoalEvaluation(ctx, missingBinding, missingKey)
	require.ErrorContains(t, err, "embedded goal not currently applied and enabled")
	require.False(t, claimed, "pending/missing receipt cannot evaluate a real final")
	require.NoError(t, missing.Close())
	// A completed turn cannot capture a new receipt retroactively.
	s, b, k := evaluationFixture(t)
	defer s.Close()
	receiptForEvaluation(t, s, b, &k)
	initial, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, EmbeddedGoalEvaluationUnknown, initial.State)
	duplicate, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	require.False(t, claimed)
	require.Equal(t, initial, duplicate)
	verdict, changed, err := s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "more work")
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "more work", verdict.Reason)
	redAppend(t, s, b, "user", "later turn")
	replay, changed, err := s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationDone, "contradictory")
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, verdict, replay)
	again, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	require.False(t, claimed)
	require.Equal(t, verdict, again)
}

func TestEmbeddedEvaluationInterruptedUnknownAndFences(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	receiptForEvaluation(t, s, b, &k)
	_, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	require.True(t, claimed)
	replacement := b
	replacement.OwnerToken++
	_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, replacement, k, EmbeddedGoalEvaluationDone, "")
	require.Error(t, err)
	pending, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	require.False(t, claimed)
	require.Equal(t, EmbeddedGoalEvaluationUnknown, pending.State)
	_, _, err = s.ApplyEmbeddedGoalCall(ctx, b, "off", EmbeddedGoalCommand{Action: "off"})
	require.NoError(t, err)
	_, _, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.Error(t, err)
	_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationDone, "")
	require.Error(t, err)
	_, _, err = s.ApplyEmbeddedGoalCall(ctx, b, "set-again", EmbeddedGoalCommand{Action: "set", Goal: "again", Settings: DefaultManagedGoalSettings()})
	require.NoError(t, err)
	_, _, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.Error(t, err, "revision changed")
	stale := b
	stale.Epoch++
	_, _, err = s.ClaimEmbeddedGoalEvaluation(ctx, stale, k)
	require.Error(t, err)
	_, err = s.db.Exec(`UPDATE embedded_sessions SET owner_expires_at='2000-01-01T00:00:00Z' WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationDone, "")
	require.Error(t, err)
}

func TestEmbeddedEvaluationRequiresCheckedCurrentFinal(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	receiptForEvaluation(t, s, b, &k)
	bad := k
	bad.FinalDigest = "wrong"
	_, _, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, bad)
	require.Error(t, err)
	redAppend(t, s, b, "user", "next turn")
	_, _, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.Error(t, err, "old final is not current head")
}

func TestEmbeddedGoalDiagnostics(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()

	// Initial empty diagnostics
	counts, last, err := s.EmbeddedGoalDiagnostics(ctx, b.AgentID)
	require.NoError(t, err)
	require.Equal(t, int64(0), counts["COMPLETED"])
	require.Equal(t, int64(0), counts["NOT_DONE"])
	require.Nil(t, last)

	// Claim and complete NOT_DONE
	receiptForEvaluation(t, s, b, &k)
	_, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	require.True(t, claimed)
	_, changed, err := s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "need more progress")
	require.NoError(t, err)
	require.True(t, changed)

	counts, last, err = s.EmbeddedGoalDiagnostics(ctx, b.AgentID)
	require.NoError(t, err)
	require.Equal(t, int64(0), counts["COMPLETED"])
	require.Equal(t, int64(1), counts["NOT_DONE"])
	require.NotNil(t, last)
	require.Equal(t, "NOT_DONE", last.Verdict)
	require.Equal(t, "need more progress", last.Reason)
	require.NotEmpty(t, last.CompletedAt)
	require.Equal(t, k.GoalRevision, last.GoalRevision)
}

