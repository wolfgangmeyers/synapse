package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/sky-valley/pi/coding"
	"github.com/stretchr/testify/require"
)

// Only the external provider/model boundary is replaced. No evaluator or
// compaction implementation is injected into this production composition.
// SDK sessions and owned dispatch retain a validated pinned Codex identity.
// Only token/window metadata and the external provider adapter are synthetic.
func productionPolicyManager(t *testing.T, db *store.Store, root string, reg *providers.FauxProviderRegistration) *Manager {
	t.Helper()
	// Snapshot/restore the external API adapter, without replacing selection or
	// weakening the admission owner's Codex guard. Resolve request-scoped auth
	// through the real private credential callback before the deterministic double.
	previous, ok := ai.GetApiProvider(ai.APIOpenAICodexResponses)
	require.True(t, ok)
	mock, ok := ai.GetApiProvider(reg.GetModel().Api)
	require.True(t, ok)
	ai.RegisterApiProvider(ai.ApiProvider{Api: ai.APIOpenAICodexResponses, StreamSimple: func(ctx context.Context, model *ai.Model, h ai.TranscriptContext, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		require.NoError(t, validateEmbeddedCodexModel(model))
		pinned := ai.GetModel("openai-codex", "gpt-5.6-terra")
		require.NotNil(t, pinned)
		require.Equal(t, pinned.MaxTokens, model.MaxTokens, "do not hide SDK summary budgeting with model metadata substitution")
		require.NotNil(t, opts)
		require.NotNil(t, opts.CodexAuth)
		if _, err := opts.CodexAuth(ctx); err != nil {
			return ai.ErrorStream(model, err)
		}
		copy := *opts
		copy.CodexAuth = nil
		copy.HTTPClient = nil
		return mock.StreamSimple(ctx, reg.GetModel(), h, &copy)
	}})
	t.Cleanup(func() { ai.RegisterApiProvider(previous) })
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest,
		Config:                 Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:          CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		GoalContinuationDelay:  20 * time.Millisecond,
		EmbeddedModelResolver: func(saved store.ManagedAgentRecord) *ai.Model {
			pinned := ai.GetModel(saved.SelectedModel.Provider, saved.SelectedModel.ID)
			require.NotNil(t, pinned)
			copy := *pinned
			copy.ContextWindow = reg.GetModel().ContextWindow // Keep the pinned MaxTokens unchanged.
			require.NoError(t, validateEmbeddedCodexModel(&copy))
			return &copy
		},
		CodexCredentialStore: testfixture.CodexAdmission(t),
	})
	require.NoError(t, err)
	setEmbeddedRecoveryGeneration(t, context.Background(), db, m)
	return m
}

func embeddedIsGoalEvaluation(h ai.TranscriptContext) bool {
	for _, message := range h.Messages {
		if system, ok := message.(ai.SystemMessage); ok {
			text := ai.GetSystemMessageText(system)
			if strings.HasPrefix(text, "goal: ") || text == embeddedGoalEvaluationSystem {
				return true
			}
		}
	}
	return false
}

func TestEmbeddedProductionGoalEvaluationBounded(t *testing.T) {
	for _, verdict := range []string{"COMPLETED", "DONE", "NOT_DONE", "OFF", "invalid"} {
		t.Run(verdict, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			path := filepath.Join(root, "router.db")
			db, err := store.Open(ctx, path, store.Options{})
			require.NoError(t, err)
			defer db.Close()
			reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
			defer reg.Unregister()
			var ordinary, evaluations, wakes atomic.Int32
			evalSeen := make(chan ai.TranscriptContext, 1)
			respond := func(h ai.TranscriptContext, opts *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
				if embeddedIsGoalEvaluation(h) {
					evaluations.Add(1)
					select {
					case evalSeen <- h:
					default:
					}
					require.NotNil(t, opts.MaxTokens)
					require.Equal(t, embeddedGoalEvaluationMaxTokens, *opts.MaxTokens)
					require.Zero(t, opts.MaxRetries)
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: verdict}}, ai.StopStop)
				}
				for _, msg := range h.Messages {
					if u, ok := msg.(ai.UserMessage); ok && len(u.Content) == 1 {
						if text, ok := u.Content[0].(ai.TextContent); ok && (text.Text == "Continue working on the current goal." || strings.HasPrefix(text.Text, "NOT_DONE: Continue ") || strings.HasPrefix(text.Text, "NOT_DONE: ")) {
							wakes.Add(1)
							return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "neutral final"}}, ai.StopStop)
						}
					}
				}
				n := ordinary.Add(1)
				if n == 1 {
					return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal", map[string]any{"action": "set", "goal": "finish the fixture"}, "production-set")}, ai.StopToolUse)
				}
				if verdict == "OFF" && n == 2 {
					return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal", map[string]any{"action": "off"}, "production-off")}, ai.StopToolUse)
				}
				return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "settled fixture final"}}, ai.StopStop)
			}
			steps := make([]providers.FauxResponseStep, 24)
			for i := range steps {
				steps[i] = respond
			}
			reg.SetResponses(steps)
			m := productionPolicyManager(t, db, root, reg)
			defer m.Close(ctx)
			launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "production-goal", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "set the fixture goal", RequestID: "b5555555-5555-4555-8555-555555555581"})
			require.NoError(t, err)
			read, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
			require.NoError(t, err)
			defer read.Close()
			if verdict == "OFF" {
				require.Eventually(t, func() bool { return ordinary.Load() == 3 }, 3*time.Second, 10*time.Millisecond)
			} else {
				require.Eventually(t, func() bool { return evaluations.Load() == 1 }, 3*time.Second, 10*time.Millisecond)
				h := <-evalSeen
				require.Len(t, h.Messages, 2)
				user := h.Messages[1].(ai.UserMessage)
				payload := user.Content[0].(ai.TextContent).Text
				require.Contains(t, payload, "Goal: finish the fixture")
				require.Contains(t, payload, "Active conversation:")
				require.Contains(t, payload, "Return exactly two lines: VERDICT: COMPLETED|NOT_DONE and REASON: <concise rationale>.")
				require.Contains(t, payload, "settled fixture final")
				var state string
				want := verdict
				if verdict == "COMPLETED" {
					want = "DONE"
				}
				if verdict == "invalid" {
					want = store.EmbeddedGoalEvaluationNotDone
				}
				require.Eventually(t, func() bool {
					return read.QueryRow(`SELECT state FROM embedded_goal_evaluations WHERE agent_id=?`, launched.Agent.AgentID).Scan(&state) == nil && state == want
				}, 3*time.Second, 10*time.Millisecond)
				if verdict == "NOT_DONE" || verdict == "invalid" {
					require.Eventually(t, func() bool { return wakes.Load() == 1 }, 3*time.Second, 10*time.Millisecond)
					require.Eventually(t, func() bool {
						var n int
						return read.QueryRow(`SELECT count(*) FROM embedded_entries WHERE agent_id=? AND kind='assistant' AND CAST(body AS TEXT) LIKE '%neutral final%'`, launched.Agent.AgentID).Scan(&n) == nil && n == 1
					}, 3*time.Second, 10*time.Millisecond)
				}
			}
			if verdict == "invalid" {
				m.mu.Lock()
				owner := m.embeddedOwners[launched.Agent.AgentID]
				m.mu.Unlock()
				var key store.EmbeddedGoalEvaluationKey
				require.NoError(t, read.QueryRow(`SELECT user_seq,user_digest,goal_revision,final_seq,final_digest FROM embedded_goal_evaluations WHERE agent_id=?`, launched.Agent.AgentID).Scan(&key.UserSeq, &key.UserDigest, &key.GoalRevision, &key.FinalSeq, &key.FinalDigest))
				evaluation, claimed, e := db.ClaimEmbeddedGoalEvaluation(ctx, owner.binding, key)
				require.NoError(t, e)
				require.False(t, claimed)
				require.Equal(t, store.EmbeddedGoalEvaluationNotDone, evaluation.State)
			}
			require.NoError(t, m.Close(ctx)) // joins evaluator/continuation; exact counts after settlement
			require.EqualValues(t, map[bool]int{true: 3, false: 2}[verdict == "OFF"], ordinary.Load())
			if verdict == "OFF" {
				require.Zero(t, evaluations.Load())
			} else if verdict == "NOT_DONE" || verdict == "invalid" {
				require.EqualValues(t, 2, evaluations.Load())
			} else {
				require.EqualValues(t, 1, evaluations.Load())
			}
			if verdict == "NOT_DONE" {
				require.EqualValues(t, 1, wakes.Load())
			} else if verdict == "invalid" {
				require.Positive(t, wakes.Load())
			} else {
				require.Zero(t, wakes.Load())
			}
			var continuations, evaluatorEntries int
			require.NoError(t, read.QueryRow(`SELECT count(*) FROM embedded_goal_continuations WHERE agent_id=?`, launched.Agent.AgentID).Scan(&continuations))
			wantContinuations := 0
			if verdict == "NOT_DONE" || verdict == "invalid" {
				wantContinuations = 2
			}
			require.Equal(t, wantContinuations, continuations)
			require.NoError(t, read.QueryRow(`SELECT count(*) FROM embedded_entries WHERE agent_id=? AND kind='assistant' AND CAST(body AS TEXT) LIKE '%"text":"NOT_DONE"%'`, launched.Agent.AgentID).Scan(&evaluatorEntries))
			require.Zero(t, evaluatorEntries, "evaluation is not an ordinary assistant/mail reply")
		})
	}
}

func TestEmbeddedProductionAutomaticFitPolicy(t *testing.T) {
	for _, mode := range []string{"ordinary", "repeat-checkpoint-fault", "tool-continuation", "summary-failed", "summary-noop", "summary-nonfitting", "checkpoint-fault", "unknown-window", "insufficient-window"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			path := filepath.Join(root, "router.db")
			db, err := store.Open(ctx, path, store.Options{})
			require.NoError(t, err)
			defer db.Close()
			window := 30000
			if mode == "unknown-window" {
				window = 0
			}
			if mode == "insufficient-window" {
				window = 10000
			}
			pinned := ai.GetModel("openai-codex", "gpt-5.6-terra")
			require.NotNil(t, pinned)
			reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{Models: []providers.FauxModelDefinition{{ID: "auto-fit", ContextWindow: window, MaxTokens: pinned.MaxTokens, Input: []string{"text"}}}})
			defer reg.Unregister()
			if mode == "unknown-window" {
				reg.GetModel().ContextWindow = 0
			} // Faux catalog defaults zero to a known window.
			var ordinary, summaries atomic.Int32
			var nonfitBinding store.EmbeddedBinding
			respond := func(h ai.TranscriptContext, opts *ai.SimpleStreamOptions, _ *providers.FauxState, model *ai.Model) *ai.AssistantMessage {
				require.NotNil(t, opts.MaxTokens)
				require.Equal(t, 4096, *opts.MaxTokens, "ordinary and summary dispatch must both use the host cap")
				if classifyG5CompactionRequest(h, opts, "").kind == "summary" {
					summaries.Add(1)
					if mode == "summary-failed" {
						return providers.FauxAssistantMessage(nil, ai.StopError)
					}
					text := "short checked summary"
					if mode == "summary-noop" {
						text = strings.Repeat("N", 100000)
					}
					if mode == "summary-nonfitting" {
						text = strings.Repeat("N", 8000)
					}
					if mode == "summary-nonfitting" {
						// Use the SDK's actual checkpoint projection and summary framing to
						// prove this candidate reduces context but fails only the fitting bar.
						snapshot, e := db.ReopenEmbedded(ctx, nonfitBinding)
						require.NoError(t, e)
						projection := coding.OrderedSnapshot{SessionID: nonfitBinding.SessionID}
						for _, entry := range snapshot.Entries {
							message, e := ai.UnmarshalMessage(entry.Body)
							require.NoError(t, e)
							projection.Messages = append(projection.Messages, coding.OrderedMessage{Sequence: entry.Sequence, ParentSequence: entry.Sequence - 1, Message: message})
						}
						selected := *pinned
						selected.ContextWindow = window
						candidate := coding.NewSession(coding.SessionOptions{SessionID: nonfitBinding.SessionID, Model: &selected, Tools: []agent.AgentTool{}})
						require.NoError(t, candidate.RestoreOrdered(ctx, projection))
						beforeTokens := embeddedProjectedTokens(candidate.History())
						seq := snapshot.Entries[len(snapshot.Entries)-1].Sequence
						projection.Checkpoint = &coding.OrderedCheckpoint{ThroughSequence: seq, FirstKeptSequence: seq, Summary: text}
						require.NoError(t, candidate.RestoreOrdered(ctx, projection))
						candidate.EnableCompaction(coding.CompactionSettings{Enabled: false})
						candidateTokens := embeddedProjectedTokens(candidate.Agent.TransformContext(ctx, candidate.History()))
						require.Less(t, candidateTokens, beforeTokens, "nonfitting must not be a non-reduction fixture")
						require.Greater(t, candidateTokens+embeddedContextHeadroom, window)
						require.LessOrEqual(t, coding.EstimateMessageTokens(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: text}}}), *opts.MaxTokens)
					}
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: text}}, ai.StopStop)
				}
				require.LessOrEqual(t, coding.EstimateContextTokens(h.Messages)+embeddedContextHeadroom, model.ContextWindow, "every ordinary/tool continuation must fit before dispatch")
				n := ordinary.Add(1)
				if mode == "tool-continuation" && n == 1 {
					return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("read", map[string]any{"path": "fixture.txt"}, "auto-read")}, ai.StopToolUse)
				}
				text := "fitting final"
				if (n == 1 || ((mode == "ordinary" || mode == "repeat-checkpoint-fault") && n == 3)) && mode != "tool-continuation" {
					text = strings.Repeat("H", 15000)
				}
				return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: text}}, ai.StopStop)
			}
			steps := make([]providers.FauxResponseStep, 12)
			for i := range steps {
				steps[i] = respond
			}
			reg.SetResponses(steps)
			if mode == "tool-continuation" {
				require.NoError(t, os.WriteFile(filepath.Join(root, "fixture.txt"), []byte(strings.Repeat("R", 40000)), 0600))
			}
			m := productionPolicyManager(t, db, root, reg)
			defer m.Close(ctx)
			prompt := strings.Repeat("P", 18000)
			launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "production-fit", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: prompt, RequestID: "b5555555-5555-4555-8555-555555555582"})
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				a, e := db.ManagedAgent(ctx, launched.Agent.AgentID)
				if e != nil {
					return false
				}
				if a.LifecycleState == "failed" {
					return true
				}
				m.mu.Lock()
				_, ready := m.embeddedOwners[a.AgentID]
				m.mu.Unlock()
				return a.LifecycleState == "running" && ready
			}, 3*time.Second, 10*time.Millisecond)
			if mode == "unknown-window" || mode == "insufficient-window" {
				require.Zero(t, ordinary.Load())
				require.Zero(t, summaries.Load())
				failed, e := db.ManagedAgent(ctx, launched.Agent.AgentID)
				require.NoError(t, e)
				require.Equal(t, "EMBEDDED_START_FAILED", failed.LastErrorCode)
				require.Contains(t, failed.LastErrorMessage, "window unknown or insufficient")
				return
			}
			saved, err := db.ManagedAgent(ctx, launched.Agent.AgentID)
			require.NoError(t, err)
			m.mu.Lock()
			owner := m.embeddedOwners[saved.AgentID]
			m.mu.Unlock()
			binding := owner.binding
			nonfitBinding = binding
			if mode == "tool-continuation" {
				// Owner readiness precedes settlement of the launch prompt.
				// Join its real checked final before reading compaction evidence.
				require.Eventually(t, func() bool {
					snapshot, readErr := db.ReopenEmbedded(ctx, binding)
					if readErr != nil || len(snapshot.Entries) == 0 {
						return false
					}
					last := snapshot.Entries[len(snapshot.Entries)-1]
					message, decodeErr := ai.UnmarshalMessage(last.Body)
					final, ok := message.(ai.AssistantMessage)
					return decodeErr == nil && ok && final.StopReason == ai.StopStop && summaries.Load() == 1 && ordinary.Load() == 2
				}, 3*time.Second, 10*time.Millisecond)
			}
			before, err := db.ReopenEmbedded(ctx, binding)
			require.NoError(t, err)
			if mode == "checkpoint-fault" {
				fault, err := sql.Open("sqlite", path)
				require.NoError(t, err)
				defer fault.Close()
				_, err = fault.Exec(`CREATE TRIGGER reject_auto_checkpoint BEFORE INSERT ON embedded_checkpoints BEGIN SELECT RAISE(ABORT,'fixture checkpoint fault'); END`)
				require.NoError(t, err)
			}
			if mode != "tool-continuation" {
				retained := strings.Repeat("Q", 20000)
				if mode == "summary-nonfitting" {
					retained = strings.Repeat("Q", 48000)
				}
				err = m.runEmbeddedSessionPrompt(context.WithValue(ctx, embeddedOwnerContextKey{}, owner), *saved, retained)
				if mode == "ordinary" || mode == "repeat-checkpoint-fault" {
					require.NoError(t, err)
				} else if mode == "summary-nonfitting" {
					require.ErrorContains(t, err, "compacted context has insufficient headroom")
				} else {
					require.Error(t, err)
				}
			}
			after, err := db.ReopenEmbedded(ctx, binding)
			require.NoError(t, err)
			for i, entry := range before.Entries {
				require.Equal(t, entry, after.Entries[i], "compaction preserves immutable conversation")
			}
			require.EqualValues(t, 1, summaries.Load())
			success := mode == "ordinary" || mode == "repeat-checkpoint-fault" || mode == "tool-continuation"
			if !success {
				require.Nil(t, after.Checkpoint)
				require.EqualValues(t, 1, ordinary.Load())
				status, e := db.EmbeddedOwnCompactionStatus(ctx, binding)
				require.NoError(t, e)
				require.Equal(t, "claimed", status.Outcome)
				effect, e := db.RecoverEmbeddedEffect(ctx, binding, status.CallID)
				require.NoError(t, e)
				require.Equal(t, "unknown", effect.State)
				return
			}
			require.NotNil(t, after.Checkpoint)
			require.EqualValues(t, 2, ordinary.Load())
			status, err := db.EmbeddedOwnCompactionStatus(ctx, binding)
			require.NoError(t, err)
			require.Equal(t, "committed", status.Outcome)
			// A fresh SDK session reopens the same fenced Store checkpoint. It does
			// not require automatic SDK compaction or the fallback checkpoint writer.
			codex := *ai.GetModel("openai-codex", "gpt-5.6-terra")
			codex.ContextWindow = window
			require.NoError(t, validateEmbeddedCodexModel(&codex))
			cold, e := store.Open(ctx, path, store.Options{})
			require.NoError(t, e)
			defer cold.Close()
			reopened, e := cold.ReopenEmbedded(ctx, binding)
			require.NoError(t, e)
			require.Equal(t, after.Checkpoint, reopened.Checkpoint)
			fresh := coding.NewSession(coding.SessionOptions{SessionID: binding.SessionID, Model: &codex, Tools: []agent.AgentTool{}, OrderedPersistence: &embeddedPersistence{db: cold, binding: binding}})
			p := &embeddedPersistence{db: cold, binding: binding}
			require.NoError(t, p.restore(ctx, fresh))
			fresh.EnableCompaction(coding.CompactionSettings{Enabled: false})
			view := fresh.Agent.TransformContext(ctx, fresh.History())
			require.LessOrEqual(t, coding.EstimateContextTokens(view)+embeddedContextHeadroom, window)
			var checkpoint coding.OrderedCheckpoint
			require.NoError(t, json.Unmarshal(after.Checkpoint.Body, &checkpoint))
			require.Equal(t, after.Entries[checkpoint.ThroughSequence-1].Digest, after.Checkpoint.AncestorDigest)
			if mode == "ordinary" || mode == "repeat-checkpoint-fault" {
				require.NoError(t, m.runEmbeddedSessionPrompt(context.WithValue(ctx, embeddedOwnerContextKey{}, owner), *saved, "after reopen"))
				require.EqualValues(t, 3, ordinary.Load(), "one additional fitting request")
				require.EqualValues(t, 1, summaries.Load())
				if mode == "repeat-checkpoint-fault" {
					fault, err := sql.Open("sqlite", path)
					require.NoError(t, err)
					defer fault.Close()
					_, err = fault.Exec(`CREATE TRIGGER reject_second_auto_checkpoint BEFORE INSERT ON embedded_checkpoints BEGIN SELECT RAISE(ABORT,'fixture second checkpoint fault'); END`)
					require.NoError(t, err)
				}
				err = m.runEmbeddedSessionPrompt(context.WithValue(ctx, embeddedOwnerContextKey{}, owner), *saved, strings.Repeat("Q", 18000))
				if mode == "ordinary" {
					require.NoError(t, err)
					require.EqualValues(t, 4, ordinary.Load())
				} else {
					require.Error(t, err)
					require.EqualValues(t, 3, ordinary.Load())
				}
				require.EqualValues(t, 2, summaries.Load())
				repeated, e := db.ReopenEmbedded(ctx, binding)
				require.NoError(t, e)
				for i, entry := range after.Entries {
					require.Equal(t, entry, repeated.Entries[i])
				}
				if mode == "ordinary" {
					require.Greater(t, repeated.Checkpoint.Sequence, after.Checkpoint.Sequence)
				} else {
					require.Equal(t, after.Checkpoint, repeated.Checkpoint)
				}
				status, e = db.EmbeddedOwnCompactionStatus(ctx, binding)
				require.NoError(t, e)
				if mode == "ordinary" {
					require.Equal(t, "committed", status.Outcome)
				} else {
					require.Equal(t, "claimed", status.Outcome)
				}
			}
		})
	}
}
