package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/sky-valley/pi/coding"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// The first gate is fixed-tool registration. Subsequent gates deliberately
// depend on it: this test never substitutes a test tool or calls Compact from
// Execute. Both roles must exercise the real Manager -> SDK path.
func TestEmbeddedG5OwnSessionCompactSDKRED(t *testing.T) {
	for _, tc := range []struct {
		name string
		root bool
		id   string
	}{
		{"root", true, "a5555555-5555-4555-8555-555555555591"},
		{"independent-worker", false, "a5555555-5555-4555-8555-555555555592"},
	} {
		if !t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			base := t.TempDir()
			path := filepath.Join(base, "router.db")
			db, err := store.Open(ctx, path, store.Options{})
			require.NoError(t, err)
			defer db.Close()
			manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
			require.NoError(t, err)
			reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
			defer reg.Unregister()
			const callID = "g5-own-compact-call"
			const summary = "## Goal\nKeep working on this session.\n"
			type observation struct {
				tools           []ai.Tool
				resultID        string
				hasResult       bool
				resultError     bool
				kind            string // summary, continuation, or unclassified
				shape           string // fixed metadata only; never provider text
				estimatedTokens int
			}
			seen := make(chan observation, 3)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			reg.SetResponses([]providers.FauxResponseStep{
				func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					var o observation
					o.estimatedTokens = coding.EstimateContextTokens(h.Messages)
					for _, msg := range h.Messages {
						if system, ok := msg.(ai.SystemMessage); ok {
							o.tools = append(o.tools, system.ToolsAdded...)
						}
					}
					seen <- o
					return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("mecha_context", map[string]any{"action": "compact"}, callID)}, ai.StopToolUse)
				},
				// SDK compaction uses a dedicated summarization system prompt,
				// one user message, no tools/results, and cache retention none.
				// A normal post-tool continuation has the saved tool result and
				// live tools. Never treat that request's response as a summary.
				func(h ai.TranscriptContext, opts *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					classified := classifyG5CompactionRequest(h, opts, callID)
					o := observation{tools: classified.tools, resultID: classified.resultID, hasResult: classified.hasResult, resultError: classified.resultError, kind: classified.kind, shape: classified.shape}
					seen <- o
					if o.kind != "summary" {
						return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "ordinary continuation"}}, ai.StopStop)
					}
					<-release // only a real summarizer waits for checkpoint inspection
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: summary}}, ai.StopStop)
				},
				func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					var o observation
					o.estimatedTokens = coding.EstimateContextTokens(h.Messages)
					for _, msg := range h.Messages {
						if result, ok := msg.(ai.ToolResultMessage); ok && result.ToolCallID == callID {
							o.resultID = result.ToolCallID
							o.hasResult = true
							o.resultError = result.IsError
						}
					}
					seen <- o
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "continued after checkpoint"}}, ai.StopStop)
				},
			})
			m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest,
				Config:                Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{base}},
				CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
				EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
				CodexCredentialStore:  testfixture.CodexAdmission(t),
			})
			require.NoError(t, err)
			defer func() { unblock(); _ = m.Close(ctx) }()
			req := LaunchRequest{Name: "g5-compact-" + tc.name, Cwd: base, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "compact your own session: " + strings.Repeat("P", 4096), RequestID: tc.id}
			var launched LaunchResult
			if tc.root {
				launched, err = m.LaunchEmbeddedRoot(ctx, req)
			} else {
				launched, err = m.Launch(ctx, req) // independent worker, not child or root
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
			var first observation
			select {
			case first = <-seen:
			case <-time.After(4 * time.Second):
				t.Fatal("initial real SDK provider request not reached")
			}
			var fixed *ai.Tool
			for i := range first.tools {
				if first.tools[i].Name == "mecha_context" {
					fixed = &first.tools[i]
				}
			}
			require.NotNil(t, fixed, "G5 compaction first RED: fixed own-session SDK tool absent; all later gates unexecuted")
			require.True(t, fixed.Parameters.Check(map[string]any{"action": "compact"}))
			for _, forbidden := range []string{"target", "agent_id", "session_id", "owner_token", "epoch"} {
				require.False(t, fixed.Parameters.Check(map[string]any{"action": "compact", forbidden: "other-session"}), "closed tool arguments must reject %s", forbidden)
			}
			var next observation
			select {
			case next = <-seen:
			case <-time.After(4 * time.Second):
				t.Fatal("compaction tool blocked the SDK continuation (possible reentrant Compact in Execute)")
			}
			if next.kind != "summary" {
				t.Fatalf("G5 compaction RED: missing safe pre-provider barrier; second request kind=%s shape=%s (summary gate and third call unexecuted)", next.kind, next.shape)
			}
			require.False(t, next.hasResult, "summarizer is a separate request, not an SDK tool result")
			// Independent reader: intent is checked for the *saved caller*, not
			// an address supplied by the model. Claim precedes the real summary;
			// no checkpoint or in-memory context swap may exist yet.
			check, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
			require.NoError(t, err)
			defer check.Close()
			var agentID, sessionID, generation, state string
			var epoch int64
			var intent []byte
			require.NoError(t, check.QueryRowContext(ctx, `SELECT agent_id,session_id,epoch,generation,intent,state FROM embedded_effects WHERE effect_id=?`, callID).Scan(&agentID, &sessionID, &epoch, &generation, &intent, &state))
			require.Equal(t, binding.AgentID, agentID)
			require.Equal(t, binding.SessionID, sessionID)
			require.Equal(t, binding.Epoch, epoch)
			require.Equal(t, binding.Generation, generation)
			var call ai.ToolCall
			require.NoError(t, json.Unmarshal(intent, &call))
			require.Equal(t, "mecha_context", call.Name)
			require.Equal(t, map[string]any{"action": "compact"}, call.Arguments)
			// Checked pending/claimed request is distinct from effect truth:
			// phase-one effect state can still be unknown at the mid-summary
			// callback. Only reject a premature terminal status here.
			require.NotEqual(t, "observed", state, "status must not complete before checkpoint")
			var requestCount, claimedCount int
			require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(claimed),0) FROM embedded_own_compaction_requests WHERE call_id=? AND agent_id=? AND session_id=? AND epoch=?`, callID, agentID, sessionID, epoch).Scan(&requestCount, &claimedCount))
			require.Equal(t, 1, requestCount, "own checked pending request must exist")
			require.Equal(t, 1, claimedCount, "checked pending request must be claimed before summary")
			var checkpoints int
			require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_checkpoints WHERE agent_id=? AND session_id=? AND epoch=?`, agentID, sessionID, epoch).Scan(&checkpoints))
			require.Zero(t, checkpoints, "Execute must not compact or swap live history")
			unblock()
			var continued observation
			select {
			case continued = <-seen:
			case <-time.After(4 * time.Second):
				g5CompactionTimeoutMetadata(t, check, binding.AgentID, callID)
				t.Fatal("summary finished but continuation never reached provider")
			}
			require.True(t, continued.hasResult, "deferred tool result must be correlated")
			// Now the checked SDK tool result has reached the provider. The
			// checkpoint must already be committed, not merely scheduled.
			require.Equal(t, callID, continued.resultID)
			require.False(t, continued.resultError)
			t.Logf("role=%s SDK estimated context tokens before=%d after=%d", tc.name, first.estimatedTokens, continued.estimatedTokens)
			require.Less(t, continued.estimatedTokens, first.estimatedTokens, "own-session compaction must strictly reduce actual SDK provider context")
			binding = inspectManagedOwnerBinding(t, path, binding)
			saved, err := db.ReopenEmbedded(ctx, binding)
			require.NoError(t, err)
			require.NotNil(t, saved.Checkpoint, "checked ordered checkpoint precedes context swap")
			var checkpoint struct {
				ThroughSequence int64  `json:"ThroughSequence"`
				Summary         string `json:"Summary"`
			}
			require.NoError(t, json.Unmarshal(saved.Checkpoint.Body, &checkpoint))
			require.Equal(t, saved.Checkpoint.Sequence, checkpoint.ThroughSequence)
			require.Equal(t, saved.Entries[checkpoint.ThroughSequence-1].Digest, saved.Checkpoint.AncestorDigest)
			require.Equal(t, summary, checkpoint.Summary)
			require.NoError(t, check.QueryRowContext(ctx, `SELECT state FROM embedded_effects WHERE agent_id=? AND session_id=? AND epoch=? AND effect_id=?`, agentID, sessionID, epoch, callID).Scan(&state))
			require.Equal(t, "observed", state, "status may leave unknown only after checkpoint commit")
			// The same ordering must hold for a partial summary, injected commit
			// fault, or owner/head conflict: no successful checkpoint means an
			// externally visible unknown request, not an automatic Execute replay.
		}) {
			return // stop at the first genuine RED; later role is unexecuted
		}
	}
}

// Classify from SDK request structure, never log or retain prompt bodies.
// compaction.go builds a dedicated system prompt and a single user message;
// normal post-tool continuation carries the correlated tool result and tools.
func classifyG5CompactionRequest(h ai.TranscriptContext, opts *ai.SimpleStreamOptions, callID string) (o struct {
	tools       []ai.Tool
	resultID    string
	hasResult   bool
	resultError bool
	kind        string
	shape       string
}) {
	var systemCount, userCount, resultCount, toolCount int
	var summarySystem bool
	for _, msg := range h.Messages {
		switch v := msg.(type) {
		case ai.SystemMessage:
			systemCount++
			toolCount += len(v.ToolsAdded)
			o.tools = append(o.tools, v.ToolsAdded...)
			if text, ok := v.StringContent(); ok && text == "You are a context summarization assistant. Your task is to read a conversation between a user and an AI assistant, then produce a structured summary following the exact format specified.\n\nDo NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the structured summary." {
				summarySystem = true
			}
		case ai.UserMessage:
			userCount++
		case ai.ToolResultMessage:
			resultCount++
			if v.ToolCallID == callID {
				o.resultID = v.ToolCallID
				o.hasResult = true
				o.resultError = v.IsError
			}
		}
	}
	cacheNone := opts != nil && opts.CacheRetention == ai.CacheNone && opts.MaxTokens != nil && opts.SessionID != ""
	o.shape = fmt.Sprintf("systems=%d users=%d tool_results=%d tools=%d summary_system=%t summary_options=%t correlated_result=%t", systemCount, userCount, resultCount, toolCount, summarySystem, cacheNone, o.hasResult)
	switch {
	case summarySystem && systemCount == 1 && userCount == 1 && len(h.Messages) == 2 && resultCount == 0 && toolCount == 0 && cacheNone:
		o.kind = "summary"
	case !summarySystem && o.hasResult && toolCount > 0:
		o.kind = "continuation"
	default:
		o.kind = "unclassified"
	}
	return o
}

// Fixed metadata only: do not emit saved bodies, provider messages or diagnostics
// that can contain arbitrary provider text.
func g5CompactionTimeoutMetadata(t *testing.T, db *sql.DB, agentID, callID string) {
	t.Helper()
	var lifecycle, code, message string
	if err := db.QueryRow(`SELECT lifecycle_state,coalesce(last_error_code,''),coalesce(last_error_message,'') FROM managed_agents WHERE agent_id=?`, agentID).Scan(&lifecycle, &code, &message); err != nil {
		t.Logf("lifecycle metadata query failed: %T", err)
	} else {
		category := "other/redacted"
		if message == "compaction SDK result has no fresh checked request" {
			category = "compaction-result-not-checked"
		} else if message == "compaction summarization failed" {
			category = "summarization-failed"
		} else if message == "" {
			category = "none"
		}
		t.Logf("lifecycle=%q code=%q message_category=%q", lifecycle, code, category)
	}
	var effectState string
	if err := db.QueryRow(`SELECT state FROM embedded_effects WHERE effect_id=?`, callID).Scan(&effectState); err != nil {
		t.Logf("effect state query failed: %T", err)
	} else {
		t.Logf("effect_state=%q", effectState)
	}
	var requestCount, claimedCount, checkpointCount, headSeq int64
	if err := db.QueryRow(`SELECT count(*),coalesce(sum(claimed),0) FROM embedded_own_compaction_requests WHERE call_id=?`, callID).Scan(&requestCount, &claimedCount); err != nil {
		t.Logf("request metadata query failed: %T", err)
	} else {
		t.Logf("requests=%d claimed=%d", requestCount, claimedCount)
	}
	if err := db.QueryRow(`SELECT count(*) FROM embedded_checkpoints WHERE agent_id=?`, agentID).Scan(&checkpointCount); err != nil {
		t.Logf("checkpoint count query failed: %T", err)
	} else {
		t.Logf("checkpoints=%d", checkpointCount)
	}
	if err := db.QueryRow(`SELECT coalesce(max(seq),0) FROM embedded_entries WHERE agent_id=?`, agentID).Scan(&headSeq); err != nil {
		t.Logf("head query failed: %T", err)
	} else {
		t.Logf("head_sequence=%d", headSeq)
	}
}
