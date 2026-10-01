package managed

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// Contract gate, not an adapter: only the Manager's fixed real SDK tools are
// allowed to answer. Stop at the first missing tool, before interpreting a
// provider continuation as successful compaction.
func TestEmbeddedG5MechaContextExactContractRED(t *testing.T) {
	for _, tc := range []struct {
		name string
		root bool
		id   string
	}{
		{"root", true, "a5555555-5555-4555-8555-5555555555a1"},
		{"independent-parentless-worker", false, "a5555555-5555-4555-8555-5555555555a2"},
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
			const statusID = "g5-context-status"
			const compactID = "g5-context-compact"
			const committedID = "g5-context-committed-status"
			type observation struct {
				tools     []ai.Tool
				kind      string
				status    *ai.ToolResultMessage
				committed *ai.ToolResultMessage
				compact   *ai.ToolResultMessage
			}
			seen := make(chan observation, 8)
			release := make(chan struct{})
			statusChecked := make(chan struct{})
			var once, statusOnce sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			allowCompact := func() { statusOnce.Do(func() { close(statusChecked) }) }
			respond := func(h ai.TranscriptContext, opts *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
				var o observation
				for _, msg := range h.Messages {
					if system, ok := msg.(ai.SystemMessage); ok {
						o.tools = append(o.tools, system.ToolsAdded...)
					}
					if r, ok := msg.(ai.ToolResultMessage); ok {
						copy := r
						if r.ToolCallID == statusID {
							o.status = &copy
						}
						if r.ToolCallID == committedID {
							o.committed = &copy
						}
						if r.ToolCallID == compactID {
							o.compact = &copy
						}
					}
				}
				o.kind = classifyG5CompactionRequest(h, opts, compactID).kind
				seen <- o
				switch {
				case o.kind == "summary":
					<-release
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "## Goal\nContinue the session.\n"}}, ai.StopStop)
				case o.committed != nil:
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "finished"}}, ai.StopStop)
				case o.compact != nil:
					return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("mecha_context", map[string]any{"action": "status"}, committedID)}, ai.StopToolUse)
				case o.status != nil:
					// Publishing the checked STATUS result is not permission to run
					// the next explicit COMPACT before the parent checks read-only state.
					<-statusChecked
					return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("mecha_context", map[string]any{"action": "compact"}, compactID)}, ai.StopToolUse)
				default:
					return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("mecha_context", map[string]any{"action": "status"}, statusID)}, ai.StopToolUse)
				}
			}
			steps := make([]providers.FauxResponseStep, 6)
			for i := range steps {
				steps[i] = respond
			}
			reg.SetResponses(steps)
			m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest,
				Config:                Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{base}},
				CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
				EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
				CodexCredentialStore:  testfixture.CodexAdmission(t),
			})
			require.NoError(t, err)
			defer func() { allowCompact(); unblock(); _ = m.Close(ctx) }()
			req := LaunchRequest{Name: "g5-context-" + tc.name, Cwd: base, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "Report context status then compact: " + strings.Repeat("P", 4096), RequestID: tc.id}
			var launched LaunchResult
			if tc.root {
				launched, err = m.LaunchEmbeddedRoot(ctx, req)
			} else {
				launched, err = m.Launch(ctx, req)
			}
			require.NoError(t, err, "setup: embedded SDK launch")
			binding := store.EmbeddedBinding{AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
			role, err := db.EmbeddedSessionRole(ctx, binding)
			require.NoError(t, err, "setup: durable SDK role")
			if tc.root {
				require.Equal(t, "root", role)
			} else {
				require.Equal(t, "worker", role)
			}
			next := func() observation {
				t.Helper()
				select {
				case o := <-seen:
					return o
				case <-time.After(4 * time.Second):
					t.Fatal("setup/barrier: bounded SDK provider request not reached")
					return observation{}
				}
			}
			first := next()
			var fixed *ai.Tool
			for i := range first.tools {
				if first.tools[i].Name == "mecha_context" {
					fixed = &first.tools[i]
				}
			}
			require.NotNil(t, fixed, "G5 exact-contract RED: mecha_context missing from real first SDK provider request; status/compact and worker gates unexecuted")
			for _, action := range []string{"status", "compact"} {
				require.True(t, fixed.Parameters.Check(map[string]any{"action": action}), "required action %s", action)
			}
			for _, args := range []map[string]any{{}, {"action": "set_policy"}, {"action": "status", "agent_id": "other"}, {"action": "compact", "session_id": "other"}, {"action": "compact", "target": "other"}} {
				require.False(t, fixed.Parameters.Check(args), "closed action schema")
			}
			// The status call must be read-only: no summarizer, request or checkpoint.
			second := next()
			require.NotEqual(t, "summary", second.kind, "status must not summarize")
			require.NotNil(t, second.status, "status SDK result must be correlated")
			require.False(t, second.status.IsError, "status must answer own-session state")
			require.Equal(t, "mecha_context", second.status.ToolName)
			read, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
			require.NoError(t, err)
			defer read.Close()
			// Verify actual checked SDK bytes and their preceding STATUS call in
			// the durable digest chain, while the COMPACT response is still held.
			statusBody, err := json.Marshal(*second.status)
			require.NoError(t, err)
			statusSaved, err := db.ReopenEmbedded(ctx, inspectManagedOwnerBinding(t, path, binding))
			require.NoError(t, err)
			var statusResults int
			for i, entry := range statusSaved.Entries {
				if !bytes.Equal(entry.Body, statusBody) {
					continue
				}
				statusResults++
				require.Positive(t, i, "persisted STATUS result requires its SDK call")
				prior := statusSaved.Entries[i-1]
				require.Equal(t, "assistant", prior.Kind)
				var call ai.AssistantMessage
				require.NoError(t, json.Unmarshal(prior.Body, &call))
				require.Equal(t, ai.StopToolUse, call.StopReason)
				// Decode the independently constructed expected SDK wire content too:
				// ordered argument metadata must match, not be discarded or copied.
				expectedBody, err := json.Marshal(ai.ContentList{providers.FauxToolCall("mecha_context", map[string]any{"action": "status"}, statusID)})
				require.NoError(t, err)
				var expected ai.ContentList
				require.NoError(t, json.Unmarshal(expectedBody, &expected))
				require.Equal(t, expected, call.Content)
				t.Logf("phase=status-checked next_action=compact-held launch_request_id=%s status_call_id=%s source=%s/%s/%d/%s SDK_call_seq=%d SDK_result_seq=%d SDK_result_digest=%s", tc.id, statusID, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation, prior.Sequence, entry.Sequence, entry.Digest)
			}
			require.Equal(t, 1, statusResults, "exactly one persisted correlated STATUS SDK result before zero-request assertion")
			var before int
			require.NoError(t, read.QueryRowContext(ctx, `SELECT count(*) FROM embedded_own_compaction_requests WHERE agent_id=? AND session_id=? AND epoch=?`, binding.AgentID, binding.SessionID, binding.Epoch).Scan(&before))
			require.Zero(t, before, "status cannot create a compaction request")
			var checkpointBefore int
			require.NoError(t, read.QueryRowContext(ctx, `SELECT count(*) FROM embedded_checkpoints WHERE agent_id=? AND session_id=? AND epoch=?`, binding.AgentID, binding.SessionID, binding.Epoch).Scan(&checkpointBefore))
			require.Zero(t, checkpointBefore, "status cannot checkpoint")
			// Require structured, own-session status, not a generic success string.
			require.Len(t, second.status.Content, 1)
			text, ok := second.status.Content[0].(ai.TextContent)
			require.True(t, ok)
			var status map[string]any
			require.NoError(t, json.Unmarshal([]byte(text.Text), &status), "status must be inspectable structured state")
			require.Equal(t, binding.SessionID, status["session_id"])
			require.Equal(t, "unknown", status["outcome"], "no prior request must not be called committed")
			require.Equal(t, map[string]any{"session_id": binding.SessionID, "policy": "fixed pre-provider headroom", "outcome": "unknown"}, status, "exact initial own-session status fields")
			allowCompact()
			// No reliance on whether the SDK asks for the normal continuation before
			// or after the separate summarizer. Only the latter may create a checkpoint.
			var summarySeen, continued bool
			for i := 0; i < 3 && !continued; i++ {
				o := next()
				if o.kind == "summary" {
					summarySeen = true
					var claimed, count, checkpoints int
					require.NoError(t, read.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(claimed),0) FROM embedded_own_compaction_requests WHERE call_id=? AND agent_id=? AND session_id=? AND epoch=?`, compactID, binding.AgentID, binding.SessionID, binding.Epoch).Scan(&count, &claimed))
					require.Equal(t, 1, count, "checked own pending request")
					require.Equal(t, 1, claimed, "safe deferred barrier claims before summarization")
					require.NoError(t, read.QueryRowContext(ctx, `SELECT count(*) FROM embedded_checkpoints WHERE agent_id=? AND session_id=? AND epoch=?`, binding.AgentID, binding.SessionID, binding.Epoch).Scan(&checkpoints))
					require.Zero(t, checkpoints, "no premature checkpoint")
					unblock()
				} else if o.compact != nil {
					require.True(t, summarySeen, "compact must not continue before safe barrier")
					require.False(t, o.compact.IsError, "checked deferred result")
					require.Equal(t, "mecha_context", o.compact.ToolName)
					continued = true
				} else {
					t.Fatalf("G5 compact RED: unexpected SDK request kind=%s", o.kind)
				}
			}
			require.True(t, continued, "committed compact SDK result not delivered")
			saved, err := db.ReopenEmbedded(ctx, inspectManagedOwnerBinding(t, path, binding))
			require.NoError(t, err)
			require.NotNil(t, saved.Checkpoint, "commit before successful continuation")
			terminal := next()
			require.NotEqual(t, "summary", terminal.kind, "status must not trigger another summary")
			require.NotNil(t, terminal.committed, "post-checkpoint status result correlated")
			require.False(t, terminal.committed.IsError)
			require.Len(t, terminal.committed.Content, 1)
			terminalText, ok := terminal.committed.Content[0].(ai.TextContent)
			require.True(t, ok)
			var terminalStatus map[string]any
			require.NoError(t, json.Unmarshal([]byte(terminalText.Text), &terminalStatus))
			require.Equal(t, binding.SessionID, terminalStatus["session_id"])
			require.Equal(t, "committed", terminalStatus["outcome"])
		}) {
			return /* stop at first RED; do not execute worker after root failure */
		}
	}
}
