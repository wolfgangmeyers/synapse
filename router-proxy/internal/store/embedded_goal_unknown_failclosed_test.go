package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// Normal Store APIs produce the policy, exact provider user, receipt before
// final, NOT_DONE evaluation and due reservation. No effect is made observed
// merely because a completed DOMAIN result has matching SDK text.
func unknownContinuationFixture(t *testing.T, tool, head string) (*Store, EmbeddedBinding, EmbeddedGoalEvaluationKey, *deterministic.FixedClock, string) {
	t.Helper()
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	s, err := Open(ctx, filepath.Join(t.TempDir(), "unknown.db"), Options{Clock: clock})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	b := goalBind(t, s, "unknown-subject", "session", 1, "generation", "worker")
	user := receiptProviderUser(t, s, b, "work on own goal")
	var policy *ManagedGoalPolicy
	unknownID := ""
	if tool == "goal" {
		var result ai.ToolResultMessage
		policy, result = receiptSDKPolicy(t, s, b, "original-goal")
		payload := result.Content[0].(ai.TextContent).Text
		require.NoError(t, s.CheckEmbeddedGoalResult(ctx, b, result.ToolCallID, []byte(payload)))
		receiptSDKAppend(t, s, b, result)
		unknownID = result.ToolCallID
		var saved string
		require.NoError(t, s.db.QueryRow(`SELECT result_json FROM embedded_goal_calls WHERE call_id=? AND state='complete'`, unknownID).Scan(&saved))
		require.Equal(t, payload, saved)
	} else {
		policy = receiptAPIPolicy(t, s, b, true)
	}
	turn := EmbeddedGoalSnoozeTurn{UserSeq: user.Sequence, UserDigest: user.Digest, GoalRevision: policy.DesiredRevision}
	if tool == "goal_snooze" {
		call := providers.FauxToolCall(tool, map[string]any{"duration_ms": 1000}, "original-snooze")
		receiptSDKAppend(t, s, b, *providers.FauxAssistantMessage(ai.ContentList{call}, ai.StopToolUse))
		intent, err := json.Marshal(call)
		require.NoError(t, err)
		require.NoError(t, s.RecordEmbeddedEffectIntent(ctx, b, call.ID, intent))
		unknownID = call.ID
	}
	// A preference is not a generic effect receipt. The effect-free fixture
	// exercises the normal reservation APIs without manufacturing effect rows.
	preference, replay, err := s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "original-snooze", []byte(`{"duration_ms":1000}`))
	require.NoError(t, err)
	require.False(t, replay)
	if tool == "goal_snooze" {
		payload, err := json.Marshal(preference)
		require.NoError(t, err)
		var saved string
		require.NoError(t, s.db.QueryRow(`SELECT result_json FROM embedded_goal_snooze_calls WHERE call_id=?`, unknownID).Scan(&saved))
		require.Equal(t, string(payload), saved)
		receiptSDKAppend(t, s, b, ai.ToolResultMessage{ToolCallID: unknownID, ToolName: tool, Content: ai.ContentList{ai.TextContent{Text: saved}}, Timestamp: 1})
	}
	if tool == "bash" {
		call := providers.FauxToolCall(tool, map[string]any{"command": "true"}, "original-bash")
		receiptSDKAppend(t, s, b, *providers.FauxAssistantMessage(ai.ContentList{call}, ai.StopToolUse))
		intent, err := json.Marshal(call)
		require.NoError(t, err)
		require.NoError(t, s.RecordEmbeddedEffectIntent(ctx, b, call.ID, intent))
		receiptSDKAppend(t, s, b, ai.ToolResultMessage{ToolCallID: call.ID, ToolName: tool, Content: ai.ContentList{ai.TextContent{Text: "ok"}}, Timestamp: 1})
		unknownID = call.ID
	}
	receipt, replay, err := s.CommitEmbeddedGoalAppliedReceipt(ctx, b, receiptRequest("original", user, policy.DesiredRevision))
	require.NoError(t, err)
	require.False(t, replay)
	require.True(t, receipt.GoalEnabled)
	finalMessage := providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "more work remains"}}, ai.StopStop)
	final := receiptSDKAppend(t, s, b, *finalMessage)
	k := EmbeddedGoalEvaluationKey{UserSeq: user.Sequence, UserDigest: user.Digest, GoalRevision: policy.DesiredRevision, FinalSeq: final.Sequence, FinalDigest: final.Digest}
	_, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	require.True(t, claimed)
	_, completed, err := s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "more")
	require.NoError(t, err)
	require.True(t, completed)
	if head == "neutral" {
		g, err := s.MintEmbeddedProcessGeneration(ctx, "current-process")
		require.NoError(t, err)
		_, err = s.ReserveEmbeddedRestartWake(ctx, b, g, "current-wake", "worker-idle", k.FinalDigest)
		require.NoError(t, err)
		require.NoError(t, s.ClaimEmbeddedRestartWake(ctx, b, g, "current-wake"))
		neutralUser := receiptProviderUser(t, s, b, "Continue working from your saved context.")
		body, err := json.Marshal(finalMessage)
		require.NoError(t, err)
		checked, err := s.AppendEmbeddedRestartWakeFinalAt(ctx, b, g, "current-wake", neutralUser.Sequence, neutralUser.Digest, "current-final", body)
		require.NoError(t, err)
		require.NoError(t, s.CompleteEmbeddedRestartWake(ctx, b, g, "current-wake", checked.Sequence, checked.Digest))
	}
	return s, b, k, clock, unknownID
}

// Historical blanket UNKNOWN continuation denial is retired. See
// embedded_continuation_surviving_guards_test.go for unchanged/no-replay safety.
func TestEmbeddedGoalContinuationEffectFreeHeadControls(t *testing.T) {
	for _, head := range []string{"original", "neutral"} {
		for _, control := range []string{"due", "off", "revision", "stale-owner"} {
			t.Run(head+"/"+control, func(t *testing.T) {
				ctx := context.Background()
				s, b, k, clock, _ := unknownContinuationFixture(t, "", head)
				require.Zero(t, embeddedRedCount(t, s, "embedded_effects"))
				original, err := s.embeddedEvaluationRow(ctx, b, k)
				require.NoError(t, err)
				reservation, err := s.EmbeddedGoalContinuationFor(ctx, b, k)
				require.NoError(t, err)
				require.NoError(t, clock.Advance(time.Second-time.Millisecond))
				_, started, err := s.StartEmbeddedGoalContinuation(ctx, b, k)
				require.NoError(t, err)
				require.False(t, started, "due-minus-1ms is not provider authority")
				unchanged, err := s.EmbeddedGoalContinuationFor(ctx, b, k)
				require.NoError(t, err)
				require.Equal(t, reservation, unchanged)
				require.NoError(t, clock.Advance(time.Millisecond))
				binding := b
				switch control {
				case "off":
					receiptAPIPolicy(t, s, b, false)
				case "revision":
					newer := receiptAPIPolicy(t, s, b, true)
					require.Greater(t, newer.DesiredRevision, k.GoalRevision)
				case "stale-owner":
					binding.Epoch++ // stale caller only; no durable fence mutation
				}
				for retry := 0; retry < 2; retry++ {
					_, started, err = s.StartEmbeddedGoalContinuation(ctx, binding, k)
					if control == "stale-owner" {
						require.Error(t, err)
					} else {
						require.NoError(t, err)
					}
					require.Equal(t, control == "due" && retry == 0, started)
				}
				continued, err := s.EmbeddedGoalContinuationFor(ctx, b, k)
				require.NoError(t, err)
				if control == "due" {
					require.Equal(t, "started", continued.State)
					require.Equal(t, "unknown", continued.Disposition)
					require.NotEmpty(t, continued.StartedAt)
				} else {
					require.Empty(t, continued.StartedAt)
					if control == "stale-owner" {
						require.Equal(t, reservation, continued)
					} else {
						require.Equal(t, "invalidated", continued.State)
					}
				}
				evaluation, err := s.embeddedEvaluationRow(ctx, b, k)
				require.NoError(t, err)
				require.Equal(t, original, evaluation)
				require.Equal(t, 1, embeddedRedCount(t, s, "embedded_goal_evaluations"))
				require.Zero(t, embeddedRedCount(t, s, "embedded_effects"))
			})
		}
	}
}
