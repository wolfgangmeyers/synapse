package managed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// Seed a completed Claude foreground turn while retaining the real manager and store.
// A Pi launch alone cannot exercise Claude evaluator selection, regardless of default.
func evaluateClaudeFixture(t *testing.T, f *failureFixture, owner embeddedOwner) {
	t.Helper()
	ctx := context.Background()
	_, err := f.raw.Exec(`UPDATE managed_agent_policies SET goal_enabled=1,goal_text='original',desired_revision=1,applied_revision=1,projection_state='applied' WHERE agent_id=?`, owner.binding.AgentID)
	require.NoError(t, err)
	body, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "work"}}})
	require.NoError(t, err)
	user, err := f.db.AppendEmbeddedEntry(ctx, owner.binding, "user", body)
	require.NoError(t, err)
	require.NoError(t, f.db.EnterEmbeddedNonMailProviderTurn(ctx, owner.binding, user.Sequence, user.Digest))
	receipt, _, err := f.db.CommitEmbeddedGoalAppliedReceipt(ctx, owner.binding, store.EmbeddedGoalAppliedReceiptRequest{TurnKey: "claude-fixture", TurnEntrySeq: user.Sequence, TurnEntryDigest: user.Digest, GoalRevision: 1})
	require.NoError(t, err)
	body, err = json.Marshal(ai.AssistantMessage{Content: ai.ContentList{ai.TextContent{Text: "progress"}}, StopReason: ai.StopStop})
	require.NoError(t, err)
	final, err := f.db.AppendEmbeddedEntry(ctx, owner.binding, "assistant", body)
	require.NoError(t, err)
	p := &embeddedPersistence{turnSeq: user.Sequence, turnDigest: user.Digest, seq: final.Sequence, digest: final.Digest}
	require.NoError(t, f.m.evaluateEmbeddedTurn(ctx, owner.binding, p, &receipt, &ai.Model{Provider: claudeCodeProvider, ID: "sonnet"}, nil))
}

func TestStaleEvaluatorCASDoesNotRecordActivityOrChangeCounters(t *testing.T) {
	for _, tc := range []struct {
		name, outcome string
		enabled       bool
	}{
		{"DONE", store.EmbeddedGoalEvaluationDone, false},
		{"NOT_DONE", store.EmbeddedGoalEvaluationNotDone, false},
		{"error", "error", false},
		{"DONE_newer_enabled", store.EmbeddedGoalEvaluationDone, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outcome := tc.outcome
			f := newFailureFixture(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblockEvaluator := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(unblockEvaluator) // Run before the fixture closes the manager.
			var acknowledged string
			f.m.embeddedGoalEvaluator = func(context.Context, store.EmbeddedGoalEvaluationKey) (string, string, error) {
				close(entered)
				<-release
				return outcome, "stale verdict", nil
			}
			f.reg.SetResponses([]providers.FauxResponseStep{f.response("progress")})
			owner := f.launch(t, "")
			if outcome == "error" {
				f.m.embeddedGoalEvaluator = nil
				bin := filepath.Join(t.TempDir(), "claude")
				marker := filepath.Join(t.TempDir(), "entered")
				unblock := filepath.Join(t.TempDir(), "release")
				acknowledged = filepath.Join(t.TempDir(), "acknowledged")
				require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nif [ \"$1\" = auth ]; then echo '{\"loggedIn\":true}'; exit 0; fi\ntouch "+marker+"\nwhile [ ! -f "+unblock+" ]; do sleep 0.02; done\ntouch "+acknowledged+"\nexit 7\n"), 0700))
				t.Setenv("MECHA_CLAUDE_BIN", bin)
				require.NoError(t, f.db.SetDefaultModel(context.Background(), &store.ManagedModel{Provider: "claude-code", ID: "haiku"}))
				cleanupCtx, cancelCleanup := context.WithCancel(context.Background())
				t.Cleanup(func() {
					// Release the CLI before stopping the bridge and closing the manager.
					_ = os.WriteFile(unblock, []byte("go"), 0600)
					unblockEvaluator()
					cancelCleanup()
				})
				go func() {
					ticker := time.NewTicker(10 * time.Millisecond)
					defer ticker.Stop()
					for {
						if _, err := os.Stat(marker); err == nil {
							close(entered)
							select {
							case <-release:
								_ = os.WriteFile(unblock, []byte("go"), 0600)
							case <-cleanupCtx.Done():
							}
							return
						}
						select {
						case <-ticker.C:
						case <-cleanupCtx.Done():
							return
						}
					}
				}()
			}
			f.m.startWG.Wait()
			ctx := context.Background()
			_, err := f.db.UpdateManagedGoalConfig(ctx, owner.binding.AgentID, store.ManagedGoalConfig{Enabled: true, Goal: "original", Settings: store.DefaultManagedGoalSettings()})
			require.NoError(t, err)
			lane := f.m.laneFor(owner.binding.AgentID)
			evaluateClaudeFixture(t, f, owner)
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("evaluator did not enter")
			}
			lane.goalAttempt.Store(7)
			lane.consecutiveContinuations.Store(3)
			newConfig := store.ManagedGoalConfig{Enabled: tc.enabled, Goal: "newer policy", Settings: store.DefaultManagedGoalSettings()}
			newPolicy, err := f.db.UpdateManagedGoalConfig(ctx, owner.binding.AgentID, newConfig)
			require.NoError(t, err)
			require.Greater(t, newPolicy.DesiredRevision, int64(1))
			unblockEvaluator()
			done := make(chan struct{})
			go func() { f.m.startWG.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("evaluator did not settle")
			}
			settledPolicy, err := f.db.ManagedGoalPolicyFor(ctx, owner.binding.AgentID)
			require.NoError(t, err)
			require.Equal(t, newPolicy.DesiredRevision, settledPolicy.DesiredRevision)
			require.Equal(t, newConfig, settledPolicy.Config, "stale verdict must not change the newer policy")
			require.Equal(t, newPolicy.LastOutcome, settledPolicy.LastOutcome, "stale verdict must not change the outcome")
			require.Equal(t, newPolicy.EvaluatorReason, settledPolicy.EvaluatorReason, "stale verdict must not change the reason")
			require.Equal(t, newPolicy.EvaluatorState, settledPolicy.EvaluatorState, "stale verdict must not change evaluator state")
			require.Equal(t, newPolicy.EvaluatorID, settledPolicy.EvaluatorID, "stale verdict must not change evaluator identity")
			require.Equal(t, newPolicy.EvaluatorGoalRevision, settledPolicy.EvaluatorGoalRevision)
			require.Equal(t, newPolicy.EvaluatorSettledSequence, settledPolicy.EvaluatorSettledSequence)
			require.Equal(t, newPolicy.AppliedRevision, settledPolicy.AppliedRevision)
			require.Equal(t, newPolicy.ProjectionState, settledPolicy.ProjectionState, "stale verdict must not change projection state")
			if outcome == "error" {
				// Direct store update supersedes the key without the API cancellation path.
				// The actual CLI consumed the release and reached its scripted failure.
				_, err := os.Stat(acknowledged)
				require.NoError(t, err)
			}
			require.EqualValues(t, 7, lane.goalAttempt.Load())
			require.EqualValues(t, 3, lane.consecutiveContinuations.Load())
			require.Zero(t, f.count(t, `SELECT count(*) FROM managed_complete_history WHERE agent_id=? AND category='managed_goal_evaluation'`, owner.binding.AgentID), "stale verdict must not publish normal evaluation activity")
			require.Zero(t, f.count(t, `SELECT count(*) FROM managed_complete_history WHERE agent_id=? AND category IN ('managed_goal_evaluation','managed_goal') AND level='error'`, owner.binding.AgentID))
			require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_goal_continuations WHERE agent_id=?`, owner.binding.AgentID))
			require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_goal_evaluations WHERE agent_id=? AND state='UNKNOWN'`, owner.binding.AgentID))
		})
	}
}

func TestClaudeCurrentEvaluatorPublishesNormalActivity(t *testing.T) {
	f := newFailureFixture(t)
	f.m.embeddedGoalEvaluator = func(context.Context, store.EmbeddedGoalEvaluationKey) (string, string, error) {
		return store.EmbeddedGoalEvaluationDone, "current verdict", nil
	}
	owner := f.launch(t, "")
	f.m.startWG.Wait()
	ctx := context.Background()
	_, err := f.db.UpdateManagedGoalConfig(ctx, owner.binding.AgentID, store.ManagedGoalConfig{Enabled: true, Goal: "original", Settings: store.DefaultManagedGoalSettings()})
	require.NoError(t, err)
	evaluateClaudeFixture(t, f, owner)
	f.m.startWG.Wait()
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM managed_complete_history WHERE agent_id=? AND category='managed_goal_evaluation' AND level='info'`, owner.binding.AgentID))
}

func TestPiEvaluatorFailureFallsBackAndContinues(t *testing.T) {
	f := newFailureFixture(t)
	f.m.embeddedGoalEvaluator = func(context.Context, store.EmbeddedGoalEvaluationKey) (string, string, error) {
		return "", "", errors.New("scripted Pi evaluator failure")
	}
	f.reg.SetResponses([]providers.FauxResponseStep{f.response("progress")})
	owner := f.launch(t, "")
	f.m.startWG.Wait()
	ctx := context.Background()
	_, err := f.db.UpdateManagedGoalConfig(ctx, owner.binding.AgentID, store.ManagedGoalConfig{Enabled: true, Goal: "original", Settings: store.DefaultManagedGoalSettings()})
	require.NoError(t, err)
	require.NoError(t, f.db.SetDefaultModel(ctx, &store.ManagedModel{Provider: "openai-codex", ID: "nonexistent-review-model"}))
	saved, err := f.db.ManagedAgent(ctx, owner.binding.AgentID)
	require.NoError(t, err)
	lane := f.m.laneFor(owner.binding.AgentID)
	lane.serial.Lock()
	err = f.m.runEmbeddedSessionPrompt(context.WithValue(ctx, embeddedOwnerContextKey{}, owner), *saved, "work")
	lane.serial.Unlock()
	require.NoError(t, err)
	f.m.startWG.Wait()
	var state, reason string
	require.NoError(t, f.raw.QueryRow(`SELECT state,reason FROM embedded_goal_evaluations WHERE agent_id=?`, owner.binding.AgentID).Scan(&state, &reason))
	require.Equal(t, store.EmbeddedGoalEvaluationNotDone, state)
	require.Contains(t, reason, "Evaluator inconclusive: scripted Pi evaluator failure")
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_goal_continuations WHERE agent_id=?`, owner.binding.AgentID))
	require.Zero(t, f.count(t, `SELECT count(*) FROM managed_complete_history WHERE agent_id=? AND category='managed_goal' AND level='error'`, owner.binding.AgentID))
}

func TestClaudeCompetingEvaluationCompletionDoesNotPublishLosingVerdict(t *testing.T) {
	f := newFailureFixture(t)
	entered, release := make(chan store.EmbeddedGoalEvaluationKey, 1), make(chan struct{})
	var releaseOnce sync.Once
	unblockEvaluator := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblockEvaluator) // Run before the fixture closes the manager.
	f.m.embeddedGoalEvaluator = func(_ context.Context, k store.EmbeddedGoalEvaluationKey) (string, string, error) {
		entered <- k
		<-release
		return store.EmbeddedGoalEvaluationDone, "losing verdict", nil
	}
	owner := f.launch(t, "")
	f.m.startWG.Wait()
	ctx := context.Background()
	_, err := f.db.UpdateManagedGoalConfig(ctx, owner.binding.AgentID, store.ManagedGoalConfig{Enabled: true, Goal: "original", Settings: store.DefaultManagedGoalSettings()})
	require.NoError(t, err)
	lane := f.m.laneFor(owner.binding.AgentID)
	evaluateClaudeFixture(t, f, owner)
	var key store.EmbeddedGoalEvaluationKey
	select {
	case key = <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("evaluator did not enter")
	}
	_, changed, err := f.db.CompleteEmbeddedGoalEvaluation(ctx, owner.binding, key, store.EmbeddedGoalEvaluationNotDone, "winner")
	require.NoError(t, err)
	require.True(t, changed)
	lane.goalAttempt.Store(7)
	lane.consecutiveContinuations.Store(3)
	unblockEvaluator()
	f.m.startWG.Wait()
	require.EqualValues(t, 7, lane.goalAttempt.Load())
	require.EqualValues(t, 3, lane.consecutiveContinuations.Load())
	require.Zero(t, f.count(t, `SELECT count(*) FROM managed_complete_history WHERE agent_id=? AND category='managed_goal_evaluation'`, owner.binding.AgentID))
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_goal_evaluations WHERE agent_id=? AND state='NOT_DONE' AND reason='winner'`, owner.binding.AgentID))
}

func TestEvaluatorStorageFailuresReportedWithoutSensitiveText(t *testing.T) {
	f := newFailureFixture(t)
	f.reg.SetResponses([]providers.FauxResponseStep{f.response("progress")})
	owner := f.launch(t, "")
	f.m.startWG.Wait()
	ctx := context.Background()
	_, err := f.db.UpdateManagedGoalConfig(ctx, owner.binding.AgentID, store.ManagedGoalConfig{Enabled: true, Goal: "original", Settings: store.DefaultManagedGoalSettings()})
	require.NoError(t, err)
	bin := filepath.Join(t.TempDir(), "claude")
	marker := filepath.Join(t.TempDir(), "entered")
	release := filepath.Join(t.TempDir(), "release")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nif [ \"$1\" = auth ]; then echo '{\"loggedIn\":true}'; exit 0; fi\ntouch "+marker+"\nwhile [ ! -f "+release+" ]; do sleep 0.02; done\necho secret-model-text >&2\nexit 7\n"), 0700))
	t.Setenv("MECHA_CLAUDE_BIN", bin)
	require.NoError(t, f.db.SetDefaultModel(ctx, &store.ManagedModel{Provider: "claude-code", ID: "haiku"}))
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	evaluateClaudeFixture(t, f, owner)
	require.Eventually(t, func() bool { _, e := os.Stat(marker); return e == nil }, 5*time.Second, 10*time.Millisecond)
	_, err = f.raw.Exec(`CREATE TRIGGER test_fail_history BEFORE INSERT ON managed_complete_history BEGIN SELECT RAISE(FAIL, 'secret-storage-text'); END`)
	require.NoError(t, err)
	_, err = f.raw.Exec(`CREATE TRIGGER test_fail_clear BEFORE UPDATE OF execution_json ON embedded_sessions WHEN NEW.execution_json IS NULL BEGIN SELECT RAISE(FAIL, 'secret-storage-text'); END`)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(release, []byte("go"), 0600))
	f.m.startWG.Wait()
	require.Contains(t, logs.String(), "evaluator failure history not recorded")
	require.Contains(t, logs.String(), "evaluator execution not cleared")
	require.NotContains(t, logs.String(), "secret-storage-text")
	require.NotContains(t, logs.String(), "secret-model-text")
	_, err = f.raw.Exec(`DROP TRIGGER test_fail_clear`)
	require.NoError(t, err)
}

func TestClaudeUnusableDefaultStopsContinuation(t *testing.T) {
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	f.m.startWG.Wait()
	ctx := context.Background()
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("HOME", t.TempDir())
	f.m.config.PiAgentDir = t.TempDir()
	f.m.config.ManagedHome = t.TempDir()
	require.NoError(t, f.db.SetDefaultModel(ctx, &store.ManagedModel{Provider: "google", ID: "gemini-2.5-flash"}))
	evaluateClaudeFixture(t, f, owner)
	f.m.startWG.Wait()
	var message string
	require.NoError(t, f.raw.QueryRow(`SELECT message FROM managed_complete_history WHERE agent_id=? AND category='managed_goal' AND level='error'`, owner.binding.AgentID).Scan(&message))
	require.Contains(t, message, "configure an authenticated router default")
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_goal_evaluations WHERE agent_id=? AND state='UNKNOWN'`, owner.binding.AgentID))
	require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_goal_continuations WHERE agent_id=?`, owner.binding.AgentID))
}
