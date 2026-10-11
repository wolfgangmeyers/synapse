package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func receiptSDKAppend(t *testing.T, s *Store, b EmbeddedBinding, message ai.Message) EmbeddedEntry {
	t.Helper()
	body, err := json.Marshal(message)
	require.NoError(t, err)
	entry, err := s.AppendEmbeddedEntry(context.Background(), b, string(message.MessageRole()), body)
	require.NoError(t, err)
	return entry
}

func receiptProviderUser(t *testing.T, s *Store, b EmbeddedBinding, text string) EmbeddedEntry {
	t.Helper()
	user := receiptSDKAppend(t, s, b, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: text}}})
	require.NoError(t, s.EnterEmbeddedNonMailProviderTurn(context.Background(), b, user.Sequence, user.Digest))
	return user
}

func receiptRequest(key string, user EmbeddedEntry, revision int64) EmbeddedGoalAppliedReceiptRequest {
	return EmbeddedGoalAppliedReceiptRequest{TurnKey: key, TurnEntrySeq: user.Sequence, TurnEntryDigest: user.Digest, GoalRevision: revision}
}

// Use the real fenced API policy writer, not an invented goal receipt or
// projection row. Process fields below are only the fixture's runtime fence.
func receiptAPIPolicy(t *testing.T, s *Store, b EmbeddedBinding, enabled bool) *ManagedGoalPolicy {
	t.Helper()
	_, err := s.db.Exec(`UPDATE managed_agents SET process_pid=123,process_start_marker='receipt-start',process_incarnation='receipt-process' WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	fence := ManagedRuntimeFence{AgentID: b.AgentID, PiSessionID: b.SessionID, SessionEpoch: b.Epoch, RuntimeGeneration: b.Generation, ProcessPID: 123, ProcessStartMarker: "receipt-start", ProcessIncarnation: "receipt-process", ManifestRevision: 1, ManifestHash: testManifestHash}
	config := ManagedGoalConfig{Enabled: enabled, Settings: DefaultManagedGoalSettings()}
	if enabled {
		config.Goal = "current goal"
	}
	policy, err := s.UpdateManagedGoalPolicy(context.Background(), fence, config)
	require.NoError(t, err)
	return policy
}

func receiptSDKPolicy(t *testing.T, s *Store, b EmbeddedBinding, id string) (*ManagedGoalPolicy, ai.ToolResultMessage) {
	t.Helper()
	call := providers.FauxToolCall("goal", map[string]any{"action": "set", "goal": "current goal"}, id)
	assistant := providers.FauxAssistantMessage(ai.ContentList{call}, ai.StopToolUse)
	receiptSDKAppend(t, s, b, *assistant)
	intent, err := json.Marshal(call)
	require.NoError(t, err)
	require.NoError(t, s.RecordEmbeddedEffectIntent(context.Background(), b, id, intent))
	policy, replay, err := s.ApplyFreshEmbeddedGoalCall(context.Background(), b, id, EmbeddedGoalCommand{Action: "set", Goal: "current goal", Settings: DefaultManagedGoalSettings()})
	require.NoError(t, err)
	require.False(t, replay)
	payload, err := json.Marshal(policy)
	require.NoError(t, err)
	result := ai.ToolResultMessage{ToolCallID: id, ToolName: "goal", Content: ai.ContentList{ai.TextContent{Text: string(payload)}}, Timestamp: 1}
	return policy, result
}

func TestEmbeddedGoalReceiptCurrentUserObservationNoProjectionWrite(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	b := goalBind(t, s, "receipt-observe", "session", 1, "generation", "worker")
	first := receiptProviderUser(t, s, b, "first API work")
	policy := receiptAPIPolicy(t, s, b, true)
	initial, replay, err := s.CommitEmbeddedGoalAppliedReceipt(ctx, b, receiptRequest("first", first, policy.DesiredRevision))
	require.NoError(t, err, "legitimate API-origin pending policy must apply")
	require.False(t, replay)
	require.True(t, initial.GoalEnabled)
	before, err := s.ManagedGoalPolicyFor(ctx, b.AgentID)
	require.NoError(t, err)
	version, err := s.GetProjectionVersion(ctx)
	require.NoError(t, err)
	receiptSDKAppend(t, s, b, *providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "first final"}}, ai.StopStop))
	second := receiptProviderUser(t, s, b, "effect-free ordinary work")
	request := receiptRequest("second", second, policy.DesiredRevision)
	observed, replay, err := s.CommitEmbeddedGoalAppliedReceipt(ctx, b, request)
	require.NoError(t, err)
	require.False(t, replay)
	require.Equal(t, second.Sequence, observed.TurnEntrySeq)
	require.Equal(t, second.Digest, observed.TurnEntryDigest)
	require.Equal(t, initial.GoalRevision, observed.GoalRevision)
	require.True(t, observed.GoalEnabled)
	after, err := s.ManagedGoalPolicyFor(ctx, b.AgentID)
	require.NoError(t, err)
	require.Equal(t, before, after, "observation must not rewrite any policy field")
	afterVersion, err := s.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, version, afterVersion)
	again, replay, err := s.CommitEmbeddedGoalAppliedReceipt(ctx, b, request)
	require.NoError(t, err)
	require.True(t, replay)
	require.Equal(t, observed, again)
	_, _, err = s.CommitEmbeddedGoalAppliedReceipt(ctx, b, receiptRequest("old-user", first, policy.DesiredRevision))
	require.Error(t, err, "an earlier ordinary user cannot substitute for the current user")
	wrong := request
	wrong.TurnKey = "bad-user"
	wrong.TurnEntryDigest = "not-the-checked-user"
	_, _, err = s.CommitEmbeddedGoalAppliedReceipt(ctx, b, wrong)
	require.Error(t, err)
	stale := b
	stale.Epoch++
	_, _, err = s.CommitEmbeddedGoalAppliedReceipt(ctx, stale, request)
	require.Error(t, err)
	wrongBinding := b
	wrongBinding.Generation = "other-runtime"
	_, _, err = s.CommitEmbeddedGoalAppliedReceipt(ctx, wrongBinding, request)
	require.Error(t, err)
	third := receiptSDKAppend(t, s, b, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "not yet dispatched"}}})
	_, _, err = s.CommitEmbeddedGoalAppliedReceipt(ctx, b, receiptRequest("unentered", third, policy.DesiredRevision))
	require.ErrorContains(t, err, "current checked provider user")
	require.NoError(t, s.EnterEmbeddedNonMailProviderTurn(ctx, b, third.Sequence, third.Digest))
	newer := receiptAPIPolicy(t, s, b, false)
	_, _, err = s.CommitEmbeddedGoalAppliedReceipt(ctx, b, receiptRequest("superseded", third, policy.DesiredRevision))
	require.ErrorContains(t, err, "stale embedded goal revision")
	off, _, err := s.CommitEmbeddedGoalAppliedReceipt(ctx, b, receiptRequest("off", third, newer.DesiredRevision))
	require.NoError(t, err, "pending API off still applies normally")
	require.False(t, off.GoalEnabled)
	fourth := receiptProviderUser(t, s, b, "off is not active work")
	_, _, err = s.CommitEmbeddedGoalAppliedReceipt(ctx, b, receiptRequest("off-observation", fourth, newer.DesiredRevision))
	require.Error(t, err, "already applied off cannot produce an active-goal observation")
}

func TestEmbeddedGoalReceiptPendingSDKExactCheckedResult(t *testing.T) {
	for _, negative := range []string{"missing", "error", "payload", "call-id", "intent", "role", "intervening-user"} {
		t.Run(negative, func(t *testing.T) {
			ctx := context.Background()
			s, _ := embeddedRedStore(t)
			b := goalBind(t, s, "receipt-sdk-"+negative, "session", 1, "generation", "root")
			user := receiptProviderUser(t, s, b, "set own goal")
			policy, result := receiptSDKPolicy(t, s, b, "checked-set")
			original, err := json.Marshal(policy)
			require.NoError(t, err)
			switch negative {
			case "error":
				result.IsError = true
			case "payload":
				result.Content = ai.ContentList{ai.TextContent{Text: "{}"}}
			case "call-id":
				result.ToolCallID = "different-call"
			case "intent":
				// Controlled corrupt intent, not a forged positive receipt.
				_, err = s.db.Exec(`UPDATE embedded_effects SET intent=? WHERE effect_id=?`, []byte(`{"id":"checked-set","name":"goal","arguments":{"action":"off"}}`), "checked-set")
				require.NoError(t, err)
			case "role":
				_, err = s.db.Exec(`UPDATE embedded_sessions SET role='worker' WHERE agent_id=?`, b.AgentID)
				require.NoError(t, err)
			case "intervening-user":
				user = receiptProviderUser(t, s, b, "different producer turn")
			}
			if negative != "missing" {
				receiptSDKAppend(t, s, b, result)
			}
			version, err := s.GetProjectionVersion(ctx)
			require.NoError(t, err)
			_, _, err = s.CommitEmbeddedGoalAppliedReceipt(ctx, b, receiptRequest("denied", user, policy.DesiredRevision))
			require.ErrorIs(t, err, ErrEmbeddedGoalSDKResultUnchecked)
			after, err := s.ManagedGoalPolicyFor(ctx, b.AgentID)
			require.NoError(t, err)
			require.Zero(t, after.AppliedRevision)
			require.Equal(t, "pending", after.ProjectionState)
			afterVersion, err := s.GetProjectionVersion(ctx)
			require.NoError(t, err)
			require.Equal(t, version, afterVersion)
			require.Zero(t, embeddedRedCount(t, s, "embedded_goal_applied_receipts"))
			var saved string
			require.NoError(t, s.db.QueryRow(`SELECT result_json FROM embedded_goal_calls WHERE call_id='checked-set'`).Scan(&saved))
			require.Equal(t, string(original), saved, "domain result stays complete and immutable")
		})
	}
	t.Run("checked-original-authorizes", func(t *testing.T) {
		ctx := context.Background()
		s, _ := embeddedRedStore(t)
		b := goalBind(t, s, "receipt-sdk-success", "session", 1, "generation", "root")
		user := receiptProviderUser(t, s, b, "set own goal")
		policy, result := receiptSDKPolicy(t, s, b, "checked-set")
		payload := result.Content[0].(ai.TextContent).Text
		require.NoError(t, s.CheckEmbeddedGoalResult(ctx, b, result.ToolCallID, []byte(payload)))
		receiptSDKAppend(t, s, b, result)
		receipt, replay, err := s.CommitEmbeddedGoalAppliedReceipt(ctx, b, receiptRequest("checked", user, policy.DesiredRevision))
		require.NoError(t, err)
		require.False(t, replay)
		require.Equal(t, user.Digest, receipt.TurnEntryDigest)
		require.True(t, receipt.GoalEnabled)
		after, err := s.ManagedGoalPolicyFor(ctx, b.AgentID)
		require.NoError(t, err)
		require.Equal(t, policy.DesiredRevision, after.AppliedRevision)
	})
}
