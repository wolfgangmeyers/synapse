package managed

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/sky-valley/pi/coding"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// Exercise both the fail-closed oversized initial turn (window = 24384)
// and the post-summary compaction headroom cold reopen (window = 26384).
// In the oversized initial turn case, initial prompt + tool schemas exceeds
// the available budget (24384 - 16384 = 8000 tokens), asserting zero provider
// dispatch (calls.Load() == 0) and fail-closed termination.
// In the compaction headroom case, the initial turn fits (26384 - 16384 = 10000),
// triggers compaction, and the resulting compacted context fails headroom
// fitting after summary reduction, verifying pre/post token bounds, cold reopen,
// and fail-closed post-summary behavior.
func TestEmbeddedG5OwnCompactionHeadroomColdReopenRED(t *testing.T) {
	t.Run("fail-closed-oversized-input", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			root bool
			id   string
		}{
			{"root", true, "a5555555-5555-4555-8555-555555555592"},
			{"independent-worker", false, "a5555555-5555-4555-8555-555555555593"},
		} {
			if !t.Run(tc.name, func(t *testing.T) {
				ctx := context.Background()
				base := t.TempDir()
				path := filepath.Join(base, "router.db")
				clock := deterministic.NewFixedClock(time.Now())
				db, err := store.Open(ctx, path, store.Options{Clock: clock})
				require.NoError(t, err)
				defer db.Close()

				manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
				require.NoError(t, err)

				// Original 24384-window setup: 24384 - 16384 headroom = 8000 token budget.
				// Initial context has 4478 tool schema tokens + 4500 user prompt tokens = 8978 tokens.
				// 8978 > 8000 budget; must fail closed before any provider dispatch.
				const window = 24384
				const maxTokens = 4096
				const budget = window - embeddedContextHeadroom
				require.Equal(t, 8000, budget)

				reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{
					Models: []providers.FauxModelDefinition{{
						ID:            "headroom-fixture",
						ContextWindow: window,
						MaxTokens:     maxTokens,
						Input:         []string{"text"},
					}},
				})
				defer reg.Unregister()

				var calls atomic.Int32
				reg.SetResponses([]providers.FauxResponseStep{
					func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, model *ai.Model) *ai.AssistantMessage {
						calls.Add(1)
						return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "unexpected continuation"}}, ai.StopStop)
					},
				})

				m, err := NewManager(ManagerOptions{
					Store:                 db,
					Manifest:              manifest,
					Config:                Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{base}},
					CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
					EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
					CodexCredentialStore:  testfixture.CodexAdmission(t),
				})
				require.NoError(t, err)
				defer m.Close(ctx)

				prompt := strings.Repeat("P", 18000)
				userMsg := ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: prompt}}}
				userTokens := coding.EstimateMessageTokens(userMsg)
				require.Equal(t, 4500, userTokens, "initial user prompt token estimate")

				// Pre-turn token bounds: tool schemas (4478) + user prompt (4500) = 8978 tokens.
				const expectedInitialTokens = 8978
				require.Greater(t, expectedInitialTokens, budget, "initial input must exceed the 8000-token headroom budget")
				require.Equal(t, 978, expectedInitialTokens-budget, "token budget deficit must equal 978 tokens")

				req := LaunchRequest{
					Name:               "g5-oversized-" + tc.name,
					Cwd:                base,
					Runtime:            "embedded",
					Model:              &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
					InitialInstruction: prompt,
					RequestID:          tc.id,
				}

				var launched LaunchResult
				if tc.root {
					launched, err = m.LaunchEmbeddedRoot(ctx, req)
				} else {
					launched, err = m.Launch(ctx, req)
				}
				require.NoError(t, err)

				binding := store.EmbeddedBinding{
					AgentID:    launched.Agent.AgentID,
					SessionID:  launched.Agent.PiSessionID,
					Epoch:      launched.Agent.SessionEpoch,
					Generation: launched.Agent.RuntimeGeneration,
				}

				require.Eventually(t, func() bool {
					a, e := db.ManagedAgent(ctx, binding.AgentID)
					return e == nil && a.LifecycleState == "failed"
				}, 4*time.Second, 10*time.Millisecond, "oversized initial context must visibly fail closed")

				failed, err := db.ManagedAgent(ctx, binding.AgentID)
				require.NoError(t, err)
				require.Equal(t, "failed", failed.LifecycleState)
				require.Equal(t, "EMBEDDED_START_FAILED", failed.LastErrorCode)
				require.Contains(t, failed.LastErrorMessage, "ordered compaction has no history")

				// Explicit invariant: zero provider calls occur before fail-closed termination
				require.Zero(t, calls.Load(), "zero provider dispatch must occur when initial input exceeds headroom budget")
			}) {
				return
			}
		}
	})

	t.Run("compaction-headroom-cold-reopen", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			root bool
			id   string
		}{
			{"root", true, "a5555555-5555-4555-8555-555555555594"},
			{"independent-worker", false, "a5555555-5555-4555-8555-555555555595"},
		} {
			if !t.Run(tc.name, func(t *testing.T) {
				ctx := context.Background()
				base := t.TempDir()
				path := filepath.Join(base, "router.db")
				clock := deterministic.NewFixedClock(time.Now())
				db, err := store.Open(ctx, path, store.Options{Clock: clock})
				require.NoError(t, err)
				manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
				require.NoError(t, err)
				// Initial context fits the 10000-token replay budget (26384 - 16384 = 10000).
				// The retained manual call carries 3009 text tokens; its 4000-token summary
				// reduces the 4500-token user prefix (by 473 tokens) but cannot fit with tool state.
				const window = 26384
				const maxTokens = 4096
				const summary = 16000
				const initialBudget = window - embeddedContextHeadroom
				require.Equal(t, 10000, initialBudget)

				reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{Models: []providers.FauxModelDefinition{{ID: "headroom-fixture", ContextWindow: window, MaxTokens: maxTokens, Input: []string{"text"}}}})
				defer reg.Unregister()
				const callID = "g5-headroom-compact"
				var requests atomic.Int32
				initial := make(chan []ai.Tool, 1)
				summaryEntered := make(chan string, 1)
				release := make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				reg.SetResponses([]providers.FauxResponseStep{
					func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, model *ai.Model) *ai.AssistantMessage {
						requests.Add(1)
						initialTokens := embeddedProjectedTokens(h.Messages)
						require.LessOrEqual(t, initialTokens+16384, window, "initial request must reach the intended summary barrier safely")
						require.Greater(t, initialTokens, 8000, "initial request exceeds 8000-token budget of 24384 window")
						require.LessOrEqual(t, initialTokens, initialBudget, "initial request must fit within 10000-token budget")
						if model.ContextWindow != window {
							initial <- nil
						} else {
							var tools []ai.Tool
							for _, msg := range h.Messages {
								if system, ok := msg.(ai.SystemMessage); ok {
									tools = append(tools, system.ToolsAdded...)
								}
							}
							initial <- tools
						}
						call := providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: strings.Repeat("K", 12000)}, providers.FauxToolCall("mecha_context", map[string]any{"action": "compact"}, callID)}, ai.StopToolUse)
						require.LessOrEqual(t, coding.EstimateMessageTokens(call), 4096, "retained manual call must respect ordinary output cap")
						return call
					},
					func(h ai.TranscriptContext, opts *ai.SimpleStreamOptions, _ *providers.FauxState, model *ai.Model) *ai.AssistantMessage {
						requests.Add(1)
						kind := classifyG5CompactionRequest(h, opts, callID).kind
						if model.ContextWindow != window || opts == nil || opts.MaxTokens == nil || *opts.MaxTokens != 4096 {
							kind = "wrong-selected-model-or-summary-budget"
						}
						summaryEntered <- kind
						if kind != "summary" {
							return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "unexpected continuation"}}, ai.StopStop)
						}
						<-release
						return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: strings.Repeat("S", summary)}}, ai.StopStop)
					},
				})
				newManager := func(s *store.Store) *Manager {
					t.Helper()
					m, e := NewManager(ManagerOptions{Store: s, Manifest: manifest,
						Config:                Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{base}},
						CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
						EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
						CodexCredentialStore:  testfixture.CodexAdmission(t),
					})
					require.NoError(t, e)
					return m
				}
				m := newManager(db)
				defer func() { unblock(); _ = m.Close(ctx); _ = db.Close() }()
				req := LaunchRequest{Name: "g5-headroom-" + tc.name, Cwd: base, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: strings.Repeat("P", 18000), RequestID: tc.id}
				var launched LaunchResult
				if tc.root {
					launched, err = m.LaunchEmbeddedRoot(ctx, req)
				} else {
					launched, err = m.Launch(ctx, req) // parentless independent worker
				}
				require.NoError(t, err)
				binding := store.EmbeddedBinding{AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
				role, err := db.EmbeddedSessionRole(ctx, binding)
				require.NoError(t, err)
				if tc.root {
					require.Equal(t, "root", role)
				} else {
					require.Equal(t, "worker", role)
				}
				select {
				case tools := <-initial:
					var fixed *ai.Tool
					for i := range tools {
						if tools[i].Name == "mecha_context" {
							fixed = &tools[i]
						}
					}
					require.NotNil(t, fixed, "G5 first RED: fixed SDK tool or selected model missing; later gates unexecuted")
					require.True(t, fixed.Parameters.Check(map[string]any{"action": "compact"}))
				case <-time.After(4 * time.Second):
					t.Fatal("G5 setup blocker: initial SDK provider request not reached")
				}
				select {
				case kind := <-summaryEntered:
					require.Equal(t, "summary", kind, "G5 RED: no selected-model SDK summarizer; later gates unexecuted")
				case <-time.After(4 * time.Second):
					t.Fatal("G5 RED: deferred summarizer not reached; later gates unexecuted")
				}
				// Verify the actual SDK summary framing, reduction and fitting bars
				// against the checked manual call/result pair before releasing summary.
				binding = inspectManagedOwnerBinding(t, path, binding)
				snapshot, err := db.ReopenEmbedded(ctx, binding)
				require.NoError(t, err)
				projection := coding.OrderedSnapshot{SessionID: binding.SessionID}
				for _, entry := range snapshot.Entries {
					message, err := ai.UnmarshalMessage(entry.Body)
					require.NoError(t, err)
					projection.Messages = append(projection.Messages, coding.OrderedMessage{Sequence: entry.Sequence, ParentSequence: entry.Sequence - 1, Message: message})
				}
				selected := *ai.GetModel("openai-codex", "gpt-5.6-terra")
				selected.ContextWindow = window
				candidate := coding.NewSession(coding.SessionOptions{SessionID: binding.SessionID, Model: &selected, Tools: []agent.AgentTool{}})
				require.NoError(t, candidate.RestoreOrdered(ctx, projection))
				beforeTokens := embeddedProjectedTokens(candidate.History())
				seq := snapshot.Entries[len(snapshot.Entries)-1].Sequence
				projection.Checkpoint = &coding.OrderedCheckpoint{ThroughSequence: seq, FirstKeptSequence: seq - 1, Summary: strings.Repeat("S", summary)}
				require.NoError(t, candidate.RestoreOrdered(ctx, projection))
				candidate.EnableCompaction(coding.CompactionSettings{Enabled: false})
				candidateTokens := embeddedProjectedTokens(candidate.Agent.TransformContext(ctx, candidate.History()))

				// Verified pre/post token-bound assertions:
				require.Greater(t, beforeTokens, 11000, "pre-compaction context token estimate")
				require.Greater(t, candidateTokens, 10000, "post-compaction context token estimate")
				require.Less(t, candidateTokens, beforeTokens, "summary reduction must reduce projected tokens")
				require.Greater(t, beforeTokens-candidateTokens, 400, "summary reduction must achieve positive reduction")
				require.Greater(t, candidateTokens+16384, window, "compacted context must exceed window minus headroom")
				require.Greater(t, (candidateTokens+16384)-window, 1000, "post-summary headroom deficit must exceed 1000 tokens")
				require.LessOrEqual(t, coding.EstimateMessageTokens(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: strings.Repeat("S", summary)}}}), 4096)

				read, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
				require.NoError(t, err)
				defer read.Close()
				var claimed, pending, checkpoints int
				var effect string
				require.NoError(t, read.QueryRowContext(ctx, `SELECT claimed FROM embedded_own_compaction_requests WHERE call_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, callID, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation).Scan(&claimed))
				require.NoError(t, read.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND kind='toolResult'`, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation).Scan(&pending))
				require.NoError(t, read.QueryRowContext(ctx, `SELECT state FROM embedded_effects WHERE effect_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, callID, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation).Scan(&effect))
				require.NoError(t, read.QueryRowContext(ctx, `SELECT count(*) FROM embedded_checkpoints WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation).Scan(&checkpoints))
				require.Equal(t, 1, pending, "checked SDK result saved before summary")
				require.Equal(t, 1, claimed, "one-shot checked claim before summary")
				require.Equal(t, "unknown", effect)
				require.Zero(t, checkpoints)
				unblock()
				require.Eventually(t, func() bool {
					a, e := db.ManagedAgent(ctx, binding.AgentID)
					return e == nil && a.LifecycleState == "failed"
				}, 4*time.Second, 10*time.Millisecond, "G5 RED: oversized compacted view must visibly fail, not continue")
				failed, err := db.ManagedAgent(ctx, binding.AgentID)
				require.NoError(t, err)
				require.Equal(t, "EMBEDDED_START_FAILED", failed.LastErrorCode, "bounded visible failure status")
				require.Contains(t, failed.LastErrorMessage, "compacted context has insufficient headroom", "must fail at post-summary fit, not initial fit or reduction")
				require.EqualValues(t, 2, requests.Load(), "no provider continuation after oversized summary")
				require.NoError(t, m.Close(ctx))
				require.NoError(t, db.Close())
				require.NoError(t, clock.Advance(embeddedOwnerTTL+time.Second))
				db, err = store.Open(ctx, path, store.Options{Clock: clock})
				require.NoError(t, err)
				m = newManager(db)
				require.NoError(t, m.RecoverEmbeddedRoots(ctx))
				cold, err := db.ManagedAgent(ctx, binding.AgentID)
				require.NoError(t, err)
				require.Equal(t, "failed", cold.LifecycleState)
				_, err = db.ReopenEmbedded(ctx, binding)
				require.Error(t, err, "failed owner must not reacquire a live fence")
				require.EqualValues(t, 2, requests.Load(), "cold reopen must not automatically retry claimed work")
				var afterClaim, afterPending, afterCheckpoints int
				var afterEffect string
				require.NoError(t, read.QueryRowContext(ctx, `SELECT claimed FROM embedded_own_compaction_requests WHERE call_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, callID, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation).Scan(&afterClaim))
				require.NoError(t, read.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND kind='toolResult'`, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation).Scan(&afterPending))
				require.NoError(t, read.QueryRowContext(ctx, `SELECT state FROM embedded_effects WHERE effect_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, callID, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation).Scan(&afterEffect))
				require.NoError(t, read.QueryRowContext(ctx, `SELECT count(*) FROM embedded_checkpoints WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation).Scan(&afterCheckpoints))
				require.Equal(t, 1, afterClaim)
				require.Equal(t, 1, afterPending)
				require.Equal(t, "unknown", afterEffect, "claimed work is UNKNOWN, never replay permission")
				require.Zero(t, afterCheckpoints, "no ordered checkpoint or context swap")
			}) {
				return // first genuine RED stops before the independent worker
			}
		}
	})
}
