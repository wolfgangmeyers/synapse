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

// A summary can fit the selected window and still increase context usage.
// The provider supplies a successful non-reducing candidate; neither the
// checkpoint nor the failure is test-injected.
func TestEmbeddedG5OwnCompactionNonreducingColdReopenRED(t *testing.T) {
	for _, tc := range []struct {
		name string
		root bool
		id   string
	}{
		{"root", true, "a5555555-5555-4555-8555-555555555596"},
		{"independent-worker", false, "a5555555-5555-4555-8555-555555555597"},
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
			// Reserve 16384; the candidate fits both the summary output
			// budget and the selected context window while increasing usage.
			const window = 32768
			const summary = 12000
			const maxTokens = 8192
			reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{Models: []providers.FauxModelDefinition{{ID: "nonreducing-fixture", ContextWindow: window, MaxTokens: maxTokens, Input: []string{"text"}}}})
			defer reg.Unregister()
			const callID = "g5-nonreducing-compact"
			var requests atomic.Int32
			initial := make(chan []ai.Tool, 1)
			initialTokens := make(chan int, 1)
			summaryEntered := make(chan string, 1)
			continuation := make(chan int, 1)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			reg.SetResponses([]providers.FauxResponseStep{
				func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, model *ai.Model) *ai.AssistantMessage {
					requests.Add(1)
					initialTokens <- coding.EstimateContextTokens(h.Messages)
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
					return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("mecha_context", map[string]any{"action": "compact"}, callID)}, ai.StopToolUse)
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
				func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					requests.Add(1)
					continuation <- coding.EstimateContextTokens(h.Messages)
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "unexpected continuation"}}, ai.StopStop)
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
			req := LaunchRequest{Name: "g5-nonreducing-" + tc.name, Cwd: base, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: strings.Repeat("P", 4096), RequestID: tc.id}
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
			original := <-initialTokens
			candidate := coding.EstimateMessageTokens(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: strings.Repeat("S", summary)}}})
			require.LessOrEqual(t, candidate, 4096, "summary candidate must fit capped output budget")
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
			projector := coding.NewSession(coding.SessionOptions{SessionID: binding.SessionID, Model: &selected, Tools: []agent.AgentTool{}})
			require.NoError(t, projector.RestoreOrdered(ctx, projection))
			beforeTokens := embeddedProjectedTokens(projector.History())
			seq := snapshot.Entries[len(snapshot.Entries)-1].Sequence
			projection.Checkpoint = &coding.OrderedCheckpoint{ThroughSequence: seq, FirstKeptSequence: seq - 1, Summary: strings.Repeat("S", summary)}
			require.NoError(t, projector.RestoreOrdered(ctx, projection))
			projector.EnableCompaction(coding.CompactionSettings{Enabled: false})
			candidateTokens := embeddedProjectedTokens(projector.Agent.TransformContext(ctx, projector.History()))
			require.Greater(t, candidateTokens, beforeTokens, "actual SDK candidate must increase replayable context")
			require.Less(t, candidateTokens+16384, window, "candidate must fit fixed headroom so reduction is the rejection")
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
			deadline := time.After(4 * time.Second)
			poll := time.NewTicker(10 * time.Millisecond)
			defer poll.Stop()
			for waiting := true; waiting; {
				select {
				case got := <-continuation:
					t.Fatalf("G5 RED: non-reducing candidate continued to provider (original=%d candidate=%d continuation=%d); checkpoint/context swap must not occur", original, candidate, got)
				case <-poll.C:
					a, e := db.ManagedAgent(ctx, binding.AgentID)
					waiting = e != nil || a.LifecycleState != "failed"
				case <-deadline:
					t.Fatal("G5 RED: non-reducing compaction did not fail within bounded lifecycle")
				}
			}
			select {
			case got := <-continuation:
				t.Fatalf("G5 RED: provider continuation after failed compaction (tokens=%d)", got)
			default:
			}
			failed, err := db.ManagedAgent(ctx, binding.AgentID)
			require.NoError(t, err)
			require.Equal(t, "EMBEDDED_START_FAILED", failed.LastErrorCode, "bounded visible failure status")
			require.Equal(t, store.RedactedManagedDiagnostic("compacted context did not reduce token estimate"), failed.LastErrorMessage, "require the distinct non-reduction cause through the public sanitizer")
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
}
