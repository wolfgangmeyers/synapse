package managed

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// CC-012: commit NOT_DONE before a real Claude restart, then let the
// existing timer or an idle sweep deliver the reserved continuation.
func TestClaudeRestartCommittedContinuation(t *testing.T) {
	for _, mode := range []string{"timer", "idle", "snooze-timer", "snooze-idle"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			cfg := claudeTestConfig(t, `{"turns":[[{"text":"continued"}]]}`)
			t.Setenv("MECHA_CLAUDE_BIN", cfg.Binary)
			t.Setenv("FAKE_CLAUDE_SCRIPT", filepath.Join(cfg.Cwd, "script.json"))
			t.Setenv("FAKE_CLAUDE_RECORD", filepath.Join(cfg.Cwd, "record.jsonl"))
			f := newFailureFixture(t)
			f.m.goalContinuationDelay = 10 * time.Second
			f.m.embeddedGoalEvaluator = func(context.Context, store.EmbeddedGoalEvaluationKey) (string, string, error) {
				return store.EmbeddedGoalEvaluationDone, "done", nil
			}
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
			_, _, err = f.db.CommitEmbeddedGoalAppliedReceipt(ctx, owner.binding, store.EmbeddedGoalAppliedReceiptRequest{TurnKey: "restart-continuation", TurnEntrySeq: user.Sequence, TurnEntryDigest: user.Digest, GoalRevision: policy.DesiredRevision})
			require.NoError(t, err)
			delay := 10 * time.Second
			if strings.HasPrefix(mode, "snooze") {
				delay = 30 * time.Second
				raw, e := json.Marshal(map[string]any{"duration_ms": int64(delay / time.Millisecond)})
				require.NoError(t, e)
				intent, e := json.Marshal(map[string]any{"id": "snooze-1", "name": "goal_snooze", "arguments": map[string]any{"duration_ms": int64(delay / time.Millisecond)}})
				require.NoError(t, e)
				gen, e := f.db.MintEmbeddedProcessGeneration(ctx, "snooze-test")
				require.NoError(t, e)
				invocation, e := f.db.RecordEmbeddedGoalInvocation(ctx, owner.binding, gen, user.Sequence, user.Digest, "", "snooze-1", intent)
				require.NoError(t, e)
				require.NoError(t, f.db.CheckEmbeddedGoalInvocation(ctx, invocation))
				pref, replay, e := f.db.SaveCurrentEmbeddedGoalSnoozePreference(ctx, owner.binding, store.EmbeddedGoalSnoozeTurn{UserSeq: user.Sequence, UserDigest: user.Digest, GoalRevision: policy.DesiredRevision}, "snooze-1", raw, invocation)
				require.NoError(t, e)
				require.False(t, replay)
				require.Equal(t, f.clock.Now().Add(delay).UTC().Format("2006-01-02T15:04:05.000Z"), pref.DueAt)
			}
			finalBody, err := json.Marshal(ai.AssistantMessage{Content: ai.ContentList{ai.TextContent{Text: "progress"}}, StopReason: ai.StopStop})
			require.NoError(t, err)
			final, err := f.db.AppendEmbeddedEntry(ctx, owner.binding, "assistant", finalBody)
			require.NoError(t, err)
			key := store.EmbeddedGoalEvaluationKey{UserSeq: user.Sequence, UserDigest: user.Digest, GoalRevision: policy.DesiredRevision, FinalSeq: final.Sequence, FinalDigest: final.Digest}
			_, claimed, err := f.db.ClaimEmbeddedGoalEvaluation(ctx, owner.binding, key)
			require.NoError(t, err)
			require.True(t, claimed)
			_, changed, err := f.db.CompleteEmbeddedGoalEvaluation(ctx, owner.binding, key, store.EmbeddedGoalEvaluationNotDone, "more")
			require.NoError(t, err)
			require.True(t, changed)
			f.m.dispatchEmbeddedGoalContinuation(owner.binding, key)
			_, err = f.raw.Exec(`UPDATE managed_agents SET selected_model_provider=?, selected_model_id=? WHERE agent_id=?`, claudeCodeProvider, "sonnet", owner.binding.AgentID)
			require.NoError(t, err)
			_, err = f.m.ReserveRestart(ctx, RestartRequest{AgentID: owner.binding.AgentID, Method: "POST", CanonicalPath: "/restart", RequestID: "restart-committed", CanonicalBodyHash: "hash"})
			require.NoError(t, err)
			f.m.startWG.Wait()
			reservation, err := f.db.EmbeddedGoalContinuationFor(ctx, owner.binding, key)
			require.NoError(t, err)
			require.Equal(t, "reserved", reservation.State)
			if strings.HasSuffix(mode, "idle") {
				f.m.retireEmbeddedGoalTimer(owner.binding.AgentID)
				f.m.kickoffIdleGoalContinuation(owner.binding)
				f.m.startWG.Wait()
			}
			require.NoError(t, f.clock.Advance(delay-time.Second))
			f.m.kickoffIdleGoalContinuation(owner.binding)
			f.m.startWG.Wait()
			reservation, err = f.db.EmbeddedGoalContinuationFor(ctx, owner.binding, key)
			require.NoError(t, err)
			require.Equal(t, "reserved", reservation.State)
			require.NoError(t, f.clock.Advance(time.Second))
			if strings.HasSuffix(mode, "idle") {
				f.m.kickoffIdleGoalContinuation(owner.binding)
				f.m.startWG.Wait()
				// Dispatch at the due time installs a zero-delay FixedClock timer.
				require.NoError(t, f.clock.Advance(0))
			}
			require.Eventually(t, func() bool {
				r, e := f.db.EmbeddedGoalContinuationFor(ctx, owner.binding, key)
				return e == nil && r.State == "started"
			}, 5*time.Second, 10*time.Millisecond)
			require.Eventually(t, func() bool {
				data, _ := os.ReadFile(filepath.Join(cfg.Cwd, "record.jsonl"))
				return strings.Contains(string(data), "NOT_DONE")
			}, 5*time.Second, 10*time.Millisecond)
			// The record is written on process input, before the turn completes.
			// The continuation holds the lane until its final and evaluator settle.
			f.laneBarrier(t, owner.binding.AgentID)
			require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_entries WHERE agent_id=? AND kind='assistant' AND CAST(body AS TEXT) LIKE '%continued%'`, owner.binding.AgentID))
			f.m.startWG.Wait()
		})
	}
}
