package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// Pending is intentionally unexecuted: there is no test-only barrier between
// the checked SDK tool-result append and the pre-provider transform. The real
// summarizer callback is the first safe observable barrier (already claimed).
func TestEmbeddedG5ContextStatusClaimedAndCommittedSDK(t *testing.T) {
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
	const initial = "g5-phase-root-initial"
	const compact = "g5-phase-root-compact"
	const committed = "g5-phase-root-committed"
	const workerStatus = "g5-phase-worker-status"
	type observation struct {
		kind    string
		results map[string]ai.ToolResultMessage
	}
	seen := make(chan observation, 8)
	release := make(chan struct{})
	firstGate, initialGate, workerGate, resumedGate := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	var calls atomic.Int32
	unblock := func() { once.Do(func() { close(release) }) }
	respond := func(h ai.TranscriptContext, opts *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		step := calls.Add(1)
		o := observation{kind: classifyG5CompactionRequest(h, opts, compact).kind, results: map[string]ai.ToolResultMessage{}}
		for _, msg := range h.Messages {
			if result, ok := msg.(ai.ToolResultMessage); ok {
				o.results[result.ToolCallID] = result
			}
		}
		seen <- o
		switch {
		case o.kind == "summary":
			<-release
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "## Goal\nContinue the session.\n"}}, ai.StopStop)
		case containsResult(o, committed), containsResult(o, workerStatus):
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
		case containsResult(o, compact):
			<-resumedGate
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("mecha_context", map[string]any{"action": "status"}, committed)}, ai.StopToolUse)
		case containsResult(o, initial):
			<-initialGate
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("mecha_context", map[string]any{"action": "compact"}, compact)}, ai.StopToolUse)
		default:
			// The worker starts only while the root summary is held. The faux
			// provider's request counter is shared across both real SDK sessions.
			if step == 4 {
				<-workerGate
				return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("mecha_context", map[string]any{"action": "status"}, workerStatus)}, ai.StopToolUse)
			}
			<-firstGate
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("mecha_context", map[string]any{"action": "status"}, initial)}, ai.StopToolUse)
		}
	}
	steps := make([]providers.FauxResponseStep, 8)
	for i := range steps {
		steps[i] = respond
	}
	reg.SetResponses(steps)
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{base}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() }, CodexCredentialStore: testfixture.CodexAdmission(t)})
	require.NoError(t, err)
	defer func() {
		unblock()
		for _, gate := range []chan struct{}{firstGate, initialGate, workerGate, resumedGate} {
			select {
			case <-gate:
			default:
				close(gate)
			}
		}
		_ = m.Close(ctx)
	}()
	root, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "g5-phase-root", Cwd: base, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "root context status then compact: " + strings.Repeat("P", 4096), RequestID: "a5555555-5555-4555-8555-5555555555b1"})
	require.NoError(t, err)
	binding := func(a LaunchResult) store.EmbeddedBinding {
		return store.EmbeddedBinding{AgentID: a.Agent.AgentID, SessionID: a.Agent.PiSessionID, Epoch: a.Agent.SessionEpoch, Generation: a.Agent.RuntimeGeneration}
	}
	rb := binding(root)
	next := func() observation {
		t.Helper()
		select {
		case o := <-seen:
			return o
		case <-time.After(5 * time.Second):
			t.Fatal("fixture timeout: SDK provider callback not reached")
			return observation{}
		}
	}
	// Counts are scoped to the saved owner; transcript entries are deliberately
	// excluded because a normal SDK status appends its ordinary tool result.
	read, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer read.Close()
	type counts struct{ requests, effects, checkpoints int }
	count := func(b store.EmbeddedBinding) (c counts) {
		t.Helper()
		for _, q := range []struct {
			query string
			dst   *int
		}{
			{`SELECT count(*) FROM embedded_own_compaction_requests WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, &c.requests},
			{`SELECT count(*) FROM embedded_effects WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, &c.effects},
			{`SELECT count(*) FROM embedded_checkpoints WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, &c.checkpoints},
		} {
			require.NoError(t, read.QueryRowContext(ctx, q.query, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(q.dst))
		}
		return
	}
	status := func(b store.EmbeddedBinding) store.EmbeddedOwnCompactionStatus {
		t.Helper()
		b = inspectManagedOwnerBinding(t, path, b)
		s, e := db.EmbeddedOwnCompactionStatus(ctx, b)
		require.NoError(t, e)
		return s
	}
	check := func(o observation, id string, b store.EmbeddedBinding, want string, call string) {
		t.Helper()
		r, ok := o.results[id]
		require.True(t, ok, "missing correlated SDK status result %s (kind=%s, result count=%d)", id, o.kind, len(o.results))
		require.False(t, r.IsError)
		require.Equal(t, "mecha_context", r.ToolName)
		require.Len(t, r.Content, 1)
		text, ok := r.Content[0].(ai.TextContent)
		require.True(t, ok)
		var got store.EmbeddedOwnCompactionStatus
		require.NoError(t, json.Unmarshal([]byte(text.Text), &got))
		require.Equal(t, b.SessionID, got.SessionID)
		require.Equal(t, want, got.Outcome)
		require.Equal(t, call, got.CallID)
		require.Equal(t, status(b), got, "cold fenced Store projection must match SDK status")
	}
	require.Equal(t, "root", mustG5PhaseRole(t, ctx, db, rb))
	first := next()
	require.NotEqual(t, "summary", first.kind)
	before := count(rb)
	close(firstGate)
	second := next()
	check(second, initial, rb, "unknown", "")
	require.Equal(t, before, count(rb), "initial status cannot add compaction effects")
	close(initialGate)
	summary := next()
	require.Equal(t, "summary", summary.kind, "real summarizer barrier required")
	var claimed, n int
	require.NoError(t, read.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(claimed),0) FROM embedded_own_compaction_requests WHERE call_id=? AND agent_id=? AND session_id=? AND epoch=?`, compact, rb.AgentID, rb.SessionID, rb.Epoch).Scan(&n, &claimed))
	require.Equal(t, 1, n)
	require.Equal(t, 1, claimed)
	var effectState string
	require.NoError(t, read.QueryRowContext(ctx, `SELECT state FROM embedded_effects WHERE effect_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, compact, rb.AgentID, rb.SessionID, rb.Epoch, rb.Generation).Scan(&effectState))
	require.Equal(t, "unknown", effectState, "a claimed request has no observed effect before checkpoint")
	require.Equal(t, store.EmbeddedOwnCompactionStatus{SessionID: rb.SessionID, Policy: "fixed pre-provider headroom", Outcome: "claimed", CallID: compact}, status(rb))
	claimedCounts := count(rb)
	require.Equal(t, counts{requests: 1, effects: 1, checkpoints: 0}, claimedCounts)
	worker, err := m.Launch(ctx, LaunchRequest{Name: "g5-phase-worker", Cwd: base, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "worker isolated context status", RequestID: "a5555555-5555-4555-8555-5555555555b2"})
	require.NoError(t, err)
	wb := binding(worker)
	require.Equal(t, "worker", mustG5PhaseRole(t, ctx, db, wb))
	require.NotEqual(t, rb.SessionID, wb.SessionID)
	workerFirst := next()
	require.NotEqual(t, "summary", workerFirst.kind)
	workerBefore := count(wb)
	close(workerGate)
	workerDone := next()
	check(workerDone, workerStatus, wb, "unknown", "")
	require.Equal(t, workerBefore, count(wb))
	require.Equal(t, claimedCounts, count(rb), "worker status cannot touch held root")
	unblock()
	resumed := next()
	require.NotEqual(t, "summary", resumed.kind)
	require.Contains(t, resumed.results, compact)
	require.False(t, resumed.results[compact].IsError)
	committedCounts := count(rb)
	require.Equal(t, counts{requests: 1, effects: 1, checkpoints: 1}, committedCounts)
	require.NoError(t, read.QueryRowContext(ctx, `SELECT state FROM embedded_effects WHERE effect_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, compact, rb.AgentID, rb.SessionID, rb.Epoch, rb.Generation).Scan(&effectState))
	require.Equal(t, "observed", effectState)
	close(resumedGate)
	done := next()
	check(done, committed, rb, "committed", compact)
	require.Equal(t, committedCounts, count(rb), "committed status cannot add compaction effects")
	check(workerDone, workerStatus, wb, "unknown", "")
	require.Equal(t, workerBefore, count(wb))
	require.Equal(t, committedCounts, count(rb))
}

func containsResult(o struct {
	kind    string
	results map[string]ai.ToolResultMessage
}, id string) bool {
	_, ok := o.results[id]
	return ok
}
func mustG5PhaseRole(t *testing.T, ctx context.Context, db *store.Store, b store.EmbeddedBinding) string {
	t.Helper()
	r, e := db.EmbeddedSessionRole(ctx, b)
	require.NoError(t, e)
	return r
}
