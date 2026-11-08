package managed

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// Force the exact interleaving: durable NOT_DONE, idle sweep, then evaluator dispatch.
func TestGoalIdleKickoffCannotBypassReservedPacing(t *testing.T) {
	ctx := context.Background()
	f := newFailureFixture(t)
	f.m.goalContinuationDelay = 10 * time.Second
	f.reg.SetResponses([]providers.FauxResponseStep{f.response("continuation")})
	owner := f.launch(t, "")
	f.m.startWG.Wait()
	_, err := f.db.UpdateManagedGoalConfig(ctx, owner.binding.AgentID, store.ManagedGoalConfig{Enabled: true, Goal: "finish", Settings: store.DefaultManagedGoalSettings()})
	require.NoError(t, err)
	policy, err := f.db.ManagedGoalPolicyFor(ctx, owner.binding.AgentID)
	require.NoError(t, err)
	userBody, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "work"}}})
	require.NoError(t, err)
	user, err := f.db.AppendEmbeddedEntry(ctx, owner.binding, "user", userBody)
	require.NoError(t, err)
	require.NoError(t, f.db.EnterEmbeddedNonMailProviderTurn(ctx, owner.binding, user.Sequence, user.Digest))
	_, _, err = f.db.CommitEmbeddedGoalAppliedReceipt(ctx, owner.binding, store.EmbeddedGoalAppliedReceiptRequest{TurnKey: "pacing", TurnEntrySeq: user.Sequence, TurnEntryDigest: user.Digest, GoalRevision: policy.DesiredRevision})
	require.NoError(t, err)
	finalBody, err := json.Marshal(ai.AssistantMessage{Content: ai.ContentList{ai.TextContent{Text: "progress"}}, StopReason: ai.StopStop})
	require.NoError(t, err)
	final, err := f.db.AppendEmbeddedEntry(ctx, owner.binding, "assistant", finalBody)
	require.NoError(t, err)
	key := store.EmbeddedGoalEvaluationKey{UserSeq: user.Sequence, UserDigest: user.Digest, GoalRevision: policy.DesiredRevision, FinalSeq: final.Sequence, FinalDigest: final.Digest}
	_, claimed, err := f.db.ClaimEmbeddedGoalEvaluation(ctx, owner.binding, key)
	require.NoError(t, err)
	require.True(t, claimed)
	_, changed, err := f.db.CompleteEmbeddedGoalEvaluation(ctx, owner.binding, key, store.EmbeddedGoalEvaluationNotDone, "more work")
	require.NoError(t, err)
	require.True(t, changed)
	f.m.kickoffIdleGoalContinuation(owner.binding)
	f.m.startWG.Wait()
	reservation, err := f.db.EmbeddedGoalContinuationFor(ctx, owner.binding, key)
	require.NoError(t, err)
	require.Equal(t, "reserved", reservation.State, "idle sweep overtook delayed dispatch")
	f.m.dispatchEmbeddedGoalContinuation(owner.binding, key)
	require.NoError(t, f.clock.Advance(9*time.Second))
	f.m.kickoffIdleGoalContinuation(owner.binding)
	f.m.startWG.Wait()
	reservation, err = f.db.EmbeddedGoalContinuationFor(ctx, owner.binding, key)
	require.NoError(t, err)
	require.Equal(t, "reserved", reservation.State)
	require.NoError(t, f.clock.Advance(time.Second))
	require.Eventually(t, func() bool {
		r, e := f.db.EmbeddedGoalContinuationFor(ctx, owner.binding, key)
		return e == nil && r.State == "started"
	}, 5*time.Second, 10*time.Millisecond)
}

// A cold manager must recover a durable snoozed reservation, without an idle
// sweep replacing its due time with the default pacing delay.
func TestGoalReservedSnoozeSurvivesManagerRestart(t *testing.T) {
	ctx := context.Background()
	f := newFailureFixture(t)
	f.reg.SetResponses([]providers.FauxResponseStep{f.response("continuation")})
	owner := f.launch(t, "")
	f.m.startWG.Wait()
	_, err := f.db.UpdateManagedGoalConfig(ctx, owner.binding.AgentID, store.ManagedGoalConfig{Enabled: true, Goal: "finish", Settings: store.DefaultManagedGoalSettings()})
	require.NoError(t, err)
	policy, err := f.db.ManagedGoalPolicyFor(ctx, owner.binding.AgentID)
	require.NoError(t, err)
	userBody, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "work"}}})
	require.NoError(t, err)
	user, err := f.db.AppendEmbeddedEntry(ctx, owner.binding, "user", userBody)
	require.NoError(t, err)
	require.NoError(t, f.db.EnterEmbeddedNonMailProviderTurn(ctx, owner.binding, user.Sequence, user.Digest))
	_, _, err = f.db.CommitEmbeddedGoalAppliedReceipt(ctx, owner.binding, store.EmbeddedGoalAppliedReceiptRequest{TurnKey: "restart-pacing", TurnEntrySeq: user.Sequence, TurnEntryDigest: user.Digest, GoalRevision: policy.DesiredRevision})
	require.NoError(t, err)
	due := f.clock.Now().Add(30 * time.Second)
	require.NoError(t, f.db.SetManagedGoalSnoozeUntil(ctx, owner.binding.AgentID, due.UTC().Format("2006-01-02T15:04:05.000Z")))
	finalBody, err := json.Marshal(ai.AssistantMessage{Content: ai.ContentList{ai.TextContent{Text: "progress"}}, StopReason: ai.StopStop})
	require.NoError(t, err)
	final, err := f.db.AppendEmbeddedEntry(ctx, owner.binding, "assistant", finalBody)
	require.NoError(t, err)
	key := store.EmbeddedGoalEvaluationKey{UserSeq: user.Sequence, UserDigest: user.Digest, GoalRevision: policy.DesiredRevision, FinalSeq: final.Sequence, FinalDigest: final.Digest}
	_, claimed, err := f.db.ClaimEmbeddedGoalEvaluation(ctx, owner.binding, key)
	require.NoError(t, err)
	require.True(t, claimed)
	_, changed, err := f.db.CompleteEmbeddedGoalEvaluation(ctx, owner.binding, key, store.EmbeddedGoalEvaluationNotDone, "more work")
	require.NoError(t, err)
	require.True(t, changed)
	reservation, err := f.db.EmbeddedGoalContinuationFor(ctx, owner.binding, key)
	require.NoError(t, err)
	require.True(t, reservation.DueAt.Valid)
	require.Equal(t, due.UTC().Format("2006-01-02T15:04:05.000Z"), reservation.DueAt.String)
	require.NoError(t, f.m.Close(ctx))
	_, err = f.raw.ExecContext(ctx, `UPDATE embedded_sessions SET owner_id=NULL, owner_expires_at=NULL WHERE agent_id=?`, owner.binding.AgentID)
	require.NoError(t, err)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	f.m, err = NewManager(ManagerOptions{Store: f.db, Manifest: manifest, Clock: f.clock, Config: Config{SessionRoot: filepath.Join(f.root, "sessions"), AllowedWorkingDirectoryRoots: []string{f.root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-6-sol", Available: true}), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return f.reg.GetModel() }, CodexCredentialStore: testfixture.CodexAdmission(t)})
	require.NoError(t, err)
	f.m.goalContinuationDelay = 10 * time.Second
	generation, err := f.db.MintEmbeddedProcessGeneration(ctx, "restart-pacing")
	require.NoError(t, err)
	require.NoError(t, f.m.SetEmbeddedStartupGeneration(generation))
	saved, err := f.db.ManagedAgent(ctx, owner.binding.AgentID)
	require.NoError(t, err)
	f.m.startEmbeddedRoot(*saved, saved.LaunchOperationID, true)
	require.Eventually(t, func() bool {
		f.m.mu.Lock()
		defer f.m.mu.Unlock()
		return f.m.embeddedGoalTimers[owner.binding.AgentID] != nil
	}, 5*time.Second, time.Millisecond)
	require.NoError(t, f.clock.Advance(29*time.Second))
	f.m.startWG.Wait()
	reservation, err = f.db.EmbeddedGoalContinuationFor(ctx, owner.binding, key)
	require.NoError(t, err)
	require.Equal(t, "reserved", reservation.State)
	require.NoError(t, f.clock.Advance(time.Second))
	require.Eventually(t, func() bool {
		r, e := f.db.EmbeddedGoalContinuationFor(ctx, owner.binding, key)
		return e == nil && r.State == "started"
	}, 5*time.Second, 10*time.Millisecond)
}
