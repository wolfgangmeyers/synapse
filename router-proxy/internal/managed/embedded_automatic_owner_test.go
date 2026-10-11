package managed

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// Takeover happens inside the external summary boundary, after the durable
// automatic claim. No old-owner checkpoint or ordinary continuation may escape.
func TestEmbeddedProductionAutomaticCompactionOwnerFence(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	clock := deterministic.NewFixedClock(time.Now())
	db, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer db.Close()
	pinned := ai.GetModel("openai-codex", "gpt-5.6-terra")
	require.NotNil(t, pinned)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{Models: []providers.FauxModelDefinition{{ID: "auto-owner", ContextWindow: 30000, MaxTokens: pinned.MaxTokens, Input: []string{"text"}}}})
	defer reg.Unregister()
	var ordinary, summaries atomic.Int32
	var replacement store.EmbeddedBinding
	respond := func(h ai.TranscriptContext, opts *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		require.NotNil(t, opts.MaxTokens)
		require.Equal(t, 4096, *opts.MaxTokens, "ordinary and summary must retain the host cap with pinned model metadata")
		if classifyG5CompactionRequest(h, opts, "").kind == "summary" {
			summaries.Add(1)
			status, e := db.EmbeddedOwnCompactionStatus(ctx, replacement)
			require.NoError(t, e)
			require.Equal(t, "claimed", status.Outcome, "the automatic ledger claim must commit before summary dispatch")
			effect, e := db.RecoverEmbeddedEffect(ctx, replacement, status.CallID)
			require.NoError(t, e)
			require.Equal(t, "unknown", effect.State)
			require.False(t, effect.ShouldReplay)
			raw, rawErr := sql.Open("sqlite", path)
			require.NoError(t, rawErr)
			var execErr error
			_, execErr = raw.Exec(`UPDATE embedded_sessions SET current=0 WHERE agent_id=?`, replacement.AgentID)
			require.NoError(t, execErr)
			replacement.SessionID = "successor-session"
			replacement.Epoch++
			_, execErr = raw.Exec(`INSERT INTO embedded_sessions(agent_id,session_id,epoch,generation,role,current) VALUES(?,?,?,?,?,1)`, replacement.AgentID, replacement.SessionID, replacement.Epoch, replacement.Generation, "root")
			require.NoError(t, execErr)
			_, execErr = raw.Exec(`UPDATE managed_agents SET pi_session_id=?, session_epoch=? WHERE agent_id=?`, replacement.SessionID, replacement.Epoch, replacement.AgentID)
			require.NoError(t, execErr)
			_ = raw.Close()
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "fitting summary from stale owner"}}, ai.StopStop)
		}
		ordinary.Add(1)
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: strings.Repeat("H", 15000)}}, ai.StopStop)
	}
	reg.SetResponses([]providers.FauxResponseStep{respond, respond, respond})
	m := productionPolicyManager(t, db, root, reg)
	defer m.Close(ctx)
	launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "automatic-owner", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: strings.Repeat("P", 18000), RequestID: "b5555555-5555-4555-8555-555555555583"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		_, ok := m.embeddedOwners[launched.Agent.AgentID]
		return ok
	}, 3*time.Second, 10*time.Millisecond)
	saved, err := db.ManagedAgent(ctx, launched.Agent.AgentID)
	require.NoError(t, err)
	m.mu.Lock()
	owner := m.embeddedOwners[saved.AgentID]
	m.mu.Unlock()
	_, err = db.ReopenEmbedded(ctx, owner.binding)
	require.NoError(t, err)
	replacement = owner.binding
	require.ErrorContains(t, m.runEmbeddedSessionPrompt(context.WithValue(ctx, embeddedOwnerContextKey{}, owner), *saved, strings.Repeat("Q", 20000)), "stale embedded owner", "only post-summary owner rejection satisfies this barrier")
	require.NoError(t, m.Close(ctx)) // Join the old owner's heartbeat before fence/count assertions.
	require.EqualValues(t, 1, ordinary.Load())
	require.EqualValues(t, 1, summaries.Load())
	after, err := db.ReopenEmbedded(ctx, replacement)
	require.NoError(t, err)
	require.Nil(t, after.Checkpoint)
	_, err = db.ReopenEmbedded(ctx, owner.binding)
	require.Error(t, err)
	read, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer read.Close()
	var claims int
	var state string
	require.NoError(t, read.QueryRow(`SELECT count(*) FROM embedded_own_compaction_requests WHERE agent_id=? AND claimed=1`, saved.AgentID).Scan(&claims))
	require.Equal(t, 1, claims)
	require.NoError(t, read.QueryRow(`SELECT state FROM embedded_effects WHERE agent_id=? AND effect_id LIKE 'auto-compact:%'`, saved.AgentID).Scan(&state))
	require.Equal(t, "unknown", state)
}
