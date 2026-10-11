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

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/sky-valley/pi/coding"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// Owner B takes the supported lease after the SDK has durably claimed A's
// checked request, but before the blocked summarizer returns its candidate.
// Nothing in this fixture replaces a session epoch or writes a lease by SQL.
func TestEmbeddedG5CompactionStaleOwnerBeforeCheckpointRED(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	clock := deterministic.NewFixedClock(time.Now())
	a, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer a.Close()
	b, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer b.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	const callID = "g5-stale-owner-compact"
	var requests, summaryCalls, noticeCalls atomic.Int32
	var recoveryBinding store.EmbeddedBinding
	var recoveryProcess, recoveryOwnerFloor, noticeSeq, noticeOwner int64
	var noticeID, noticeDigest string
	summaryEntered := make(chan string, 1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			requests.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("mecha_context", map[string]any{"action": "compact"}, callID)}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, opts *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			requests.Add(1)
			kind := classifyG5CompactionRequest(h, opts, callID).kind
			summaryEntered <- kind
			if kind != "summary" {
				return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "unexpected continuation"}}, ai.StopStop)
			}
			summaryCalls.Add(1)
			<-release
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "## Goal\nKeep working.\n"}}, ai.StopStop)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			// Recovery may acknowledge only its durably claimed interrupted
			// source turn. This is not a compaction retry or a neutral prompt.
			ro, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
			require.NoError(t, err)
			defer ro.Close()
			var body []byte
			var text string
			require.NoError(t, ro.QueryRowContext(ctx, `SELECT n.notice_id,n.entry_seq,e.digest,e.body,n.notice_text,n.start_owner_token FROM embedded_interrupted_notices n JOIN embedded_entries e ON e.agent_id=n.agent_id AND e.session_id=n.session_id AND e.epoch=n.epoch AND e.generation=n.generation AND e.seq=n.entry_seq JOIN embedded_sessions s ON s.agent_id=n.agent_id AND s.session_id=n.session_id AND s.epoch=n.epoch AND s.generation=n.generation AND s.current=1 AND s.owner_token=n.start_owner_token JOIN embedded_restart_wake_slots w ON w.agent_id=n.agent_id AND w.session_id=n.session_id AND w.epoch=n.epoch AND w.runtime_generation=n.generation AND w.wake_id=n.notice_id AND w.claim_owner_token=s.owner_token WHERE n.agent_id=? AND n.session_id=? AND n.epoch=? AND n.generation=? AND n.source_seq=2 AND n.state='started' AND e.kind='user' AND w.slot='current' AND w.kind='interrupted' AND w.digest=e.digest AND w.state='claimed' AND w.process_generation=? AND s.owner_token>=?`, recoveryBinding.AgentID, recoveryBinding.SessionID, recoveryBinding.Epoch, recoveryBinding.Generation, recoveryProcess, recoveryOwnerFloor).Scan(&noticeID, &noticeSeq, &noticeDigest, &body, &text, &noticeOwner))
			require.NotEmpty(t, noticeDigest)
			exact, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: text}}})
			require.NoError(t, err)
			require.Equal(t, exact, body)
			require.NotEmpty(t, h.Messages)
			last, err := json.Marshal(h.Messages[len(h.Messages)-1])
			require.NoError(t, err)
			require.Equal(t, body, last, "only the exact current interrupted notice may complete recovery")
			require.EqualValues(t, 1, noticeCalls.Add(1), "notice completion is one-shot")
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "compaction interruption acknowledged"}}, ai.StopStop)
		},
	})
	credentials := testfixture.CodexAdmission(t)
	m, err := NewManager(ManagerOptions{Store: a, Manifest: manifest,
		Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		CodexCredentialStore:  credentials,
	})
	require.NoError(t, err)
	defer func() { unblock(); _ = m.Close(ctx) }()
	launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "g5-stale-owner-root", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "compact your own session: " + strings.Repeat("P", 4096), RequestID: "a5555555-5555-4555-8555-555555555598"})
	require.NoError(t, err)
	binding := store.EmbeddedBinding{AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
	select {
	case kind := <-summaryEntered:
		require.Equal(t, "summary", kind, "G5 RED: checked SDK summarizer not reached; takeover and commit unexecuted")
	case <-time.After(4 * time.Second):
		t.Fatal("G5 setup failure: SDK summarizer not reached; takeover and commit unexecuted")
	}
	read, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer read.Close()
	var claimed, resultCount, checkpointCount int
	var effectState, headDigest string
	var headSeq int64
	require.NoError(t, read.QueryRowContext(ctx, `SELECT claimed FROM embedded_own_compaction_requests WHERE call_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, callID, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation).Scan(&claimed))
	require.Equal(t, 1, claimed, "G5 RED: durable claim must precede summary; takeover unexecuted")
	require.NoError(t, read.QueryRowContext(ctx, `SELECT seq,digest FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? ORDER BY seq DESC LIMIT 1`, binding.AgentID, binding.SessionID, binding.Epoch).Scan(&headSeq, &headDigest))
	require.NoError(t, read.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND kind='toolResult'`, binding.AgentID, binding.SessionID, binding.Epoch).Scan(&resultCount))
	require.Equal(t, 1, resultCount, "checked SDK pending result must precede summary")
	require.NoError(t, read.QueryRowContext(ctx, `SELECT state FROM embedded_effects WHERE effect_id=? AND agent_id=?`, callID, binding.AgentID).Scan(&effectState))
	require.Equal(t, "unknown", effectState)
	require.NoError(t, read.QueryRowContext(ctx, `SELECT count(*) FROM embedded_checkpoints WHERE agent_id=?`, binding.AgentID).Scan(&checkpointCount))
	require.Zero(t, checkpointCount, "candidate has not committed")
	// The runner publishes embeddedOwners only after its initial turn returns.
	// At this blocked callback, read the already-committed Store lease instead.
	binding = inspectManagedOwnerBinding(t, path, binding)
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = raw.Exec(`UPDATE embedded_sessions SET current=0 WHERE agent_id=?`, binding.AgentID)
	require.NoError(t, err)
	_ = raw.Close()
	bToken, err := b.AcquireEmbeddedOwner(ctx, binding, "g5-replacement-owner-b", embeddedOwnerTTL)
	require.Error(t, err)
	checkpoint := coding.OrderedCheckpoint{ThroughSequence: headSeq, FirstKeptSequence: headSeq - 1, Summary: "## Goal\nKeep working.\n"}
	err = a.CommitEmbeddedOwnCompactionCheckpoint(ctx, binding, callID, headSeq, headDigest, checkpoint)
	require.ErrorContains(t, err, "stale embedded owner", "G5 RED: A committed after B takeover; later assertions unexecuted")
	unblock()
	turnDone := make(chan struct{})
	go func() { m.startWG.Wait(); close(turnDone) }()
	select {
	case <-turnDone:
	case <-time.After(4 * time.Second):
		t.Fatal("G5 RED: stale summary turn did not return; post-summary assertions unexecuted")
	}
	require.EqualValues(t, 2, requests.Load(), "G5 RED: no provider continuation or live context swap")
	require.NoError(t, read.QueryRowContext(ctx, `SELECT count(*) FROM embedded_checkpoints WHERE agent_id=?`, binding.AgentID).Scan(&checkpointCount))
	require.Zero(t, checkpointCount, "G5 RED: no candidate checkpoint or context swap")
	var afterSeq int64
	var afterDigest string
	require.NoError(t, read.QueryRowContext(ctx, `SELECT seq,digest FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? ORDER BY seq DESC LIMIT 1`, binding.AgentID, binding.SessionID, binding.Epoch).Scan(&afterSeq, &afterDigest))
	require.Equal(t, headSeq, afterSeq, "G5 RED: ordered head must not move")
	require.Equal(t, headDigest, afterDigest, "G5 RED: ordered digest must not change")
	require.NoError(t, m.Close(ctx))
	require.NoError(t, a.Close())
	require.NoError(t, b.Close())

	// Restore current=1 for recovery
	raw, err = sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = raw.Exec(`UPDATE embedded_sessions SET current=1, owner_id='g5-replacement-owner-b' WHERE agent_id=?`, binding.AgentID)
	require.NoError(t, err)
	_ = raw.Close()
	cold, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer cold.Close()
	var afterClaim int
	require.NoError(t, read.QueryRowContext(ctx, `SELECT claimed FROM embedded_own_compaction_requests WHERE call_id=? AND agent_id=?`, callID, binding.AgentID).Scan(&afterClaim))
	require.Equal(t, 1, afterClaim, "claimed request remains spent on cold reopen")
	require.NoError(t, read.QueryRowContext(ctx, `SELECT state FROM embedded_effects WHERE effect_id=? AND agent_id=?`, callID, binding.AgentID).Scan(&effectState))
	require.Equal(t, "unknown", effectState, "G5 RED: claimed effect must remain UNKNOWN")
	coldAgent, err := cold.ManagedAgent(ctx, binding.AgentID)
	require.NoError(t, err)
	require.Equal(t, binding.SessionID, coldAgent.PiSessionID, "cold reopen must retain the same session")
	var savedOwner string
	var savedToken int64
	bToken = 1
	staleA := binding
	staleA.Epoch++
	require.NoError(t, read.QueryRowContext(ctx, `SELECT owner_id,owner_token FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation).Scan(&savedOwner, &savedToken))
	require.Equal(t, "g5-replacement-owner-b", savedOwner, "cold reopen must preserve B's lease")
	require.Equal(t, bToken, savedToken)
	require.EqualValues(t, 2, requests.Load(), "cold reopen cannot restart claimed provider work")

	// Prove the cold Store rejects takeover while B's lease is still live.
	liveB := binding
	liveB.OwnerToken = bToken
	require.NoError(t, cold.RenewEmbeddedOwner(ctx, liveB, savedOwner, embeddedOwnerTTL), "G5 RED: B must retain live fencing authority")
	require.ErrorContains(t, cold.RenewEmbeddedOwner(ctx, staleA, savedOwner, embeddedOwnerTTL), "stale embedded owner", "G5 RED: stale A regained fencing authority")
	staleB := binding
	staleB.Generation = "wrong-contender-gen"
	_, err = cold.AcquireEmbeddedOwner(ctx, staleB, "g5-cold-recovery-contender", embeddedOwnerTTL)
	require.Error(t, err)

	// Reopen without a launch/bootstrap request.
	var savedExpiry string
	require.NoError(t, read.QueryRowContext(ctx, `SELECT owner_id,owner_token,owner_expires_at FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation).Scan(&savedOwner, &savedToken, &savedExpiry))
	require.Equal(t, "g5-replacement-owner-b", savedOwner, "G5 RED: rejected takeover displaced live owner B")
	require.Equal(t, bToken, savedToken, "G5 RED: rejected takeover changed B's token")
	recovered, err := NewManager(ManagerOptions{Store: cold, Manifest: manifest, Clock: clock,
		Config:                Config{Enabled: false, SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		CodexCredentialStore:  credentials,
	})
	require.NoError(t, err, "G5 setup failure: cold Manager did not reopen; recovery unexecuted")
	defer recovered.Close(ctx)
	recoveryBinding = binding
	recoveryOwnerFloor = bToken
	recoveryProcess = setEmbeddedRecoveryGeneration(t, ctx, cold, recovered)
	require.NoError(t, recovered.RecoverEmbeddedRoots(ctx), "G5 setup failure: supported cold recovery failed")
	// A successful recovered owner retains its heartbeat until Close. Readiness
	// follows the checked notice final; waiting on startWG before Close cannot settle.
	require.Eventually(t, func() bool {
		recovered.mu.Lock()
		defer recovered.mu.Unlock()
		_, ready := recovered.embeddedOwners[binding.AgentID]
		return ready
	}, 4*time.Second, 10*time.Millisecond, "cold recovery did not publish its checked owner")
	require.NoError(t, recovered.Close(ctx)) // Join the retained heartbeat before assertions.
	require.EqualValues(t, 2, requests.Load(), "cold Manager must not replay the original compaction turn")
	require.EqualValues(t, 1, summaryCalls.Load(), "exactly one original summarizer attempt")
	require.EqualValues(t, 1, noticeCalls.Load(), "only the separate exact interrupted notice may complete")
	var checkedSeq int64
	var checkedBody []byte
	require.NoError(t, read.QueryRowContext(ctx, `SELECT f.seq,f.body FROM embedded_restart_wake_slots w JOIN embedded_sessions s ON s.agent_id=w.agent_id AND s.session_id=w.session_id AND s.epoch=w.epoch AND s.generation=w.runtime_generation AND s.current=1 AND s.owner_token=w.claim_owner_token JOIN embedded_restart_wake_checked_origins o ON o.agent_id=w.agent_id AND o.slot=w.slot AND o.session_id=w.session_id AND o.epoch=w.epoch AND o.runtime_generation=w.runtime_generation AND o.wake_id=w.wake_id AND o.process_generation=w.process_generation AND o.claim_owner_token=w.claim_owner_token JOIN embedded_entries f ON f.agent_id=o.agent_id AND f.session_id=o.session_id AND f.epoch=o.epoch AND f.generation=o.runtime_generation AND f.seq=o.seq WHERE w.agent_id=? AND w.session_id=? AND w.epoch=? AND w.runtime_generation=? AND w.slot='current' AND w.wake_id=? AND w.kind='interrupted' AND w.state='checked-complete' AND w.process_generation=? AND w.claim_owner_token=? AND w.digest=? AND f.kind='assistant' AND f.parent_digest=?`, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation, noticeID, recoveryProcess, noticeOwner, noticeDigest, noticeDigest).Scan(&checkedSeq, &checkedBody))
	require.Equal(t, headSeq+1, noticeSeq, "no original compaction replay before notice")
	require.Equal(t, noticeSeq+1, checkedSeq, "only the exact adjacent notice final may advance the head")
	require.NoError(t, read.QueryRowContext(ctx, `SELECT max(seq) FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation).Scan(&afterSeq))
	require.Equal(t, checkedSeq, afterSeq, "notice completion must not resume original compaction work")
	checkedMessage, err := ai.UnmarshalMessage(checkedBody)
	require.NoError(t, err)
	checkedFinal, ok := checkedMessage.(ai.AssistantMessage)
	require.True(t, ok)
	require.Equal(t, ai.StopStop, checkedFinal.StopReason)
	require.Equal(t, ai.ContentList{ai.TextContent{Text: "compaction interruption acknowledged"}}, checkedFinal.Content)
	require.NoError(t, read.QueryRowContext(ctx, `SELECT count(*),max(claimed) FROM embedded_own_compaction_requests WHERE agent_id=? AND call_id=?`, binding.AgentID, callID).Scan(&resultCount, &afterClaim))
	require.Equal(t, 1, resultCount, "exactly one original durable compaction attempt")
	require.Equal(t, 1, afterClaim, "original compaction remains spent")
	require.NoError(t, read.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND kind='toolResult'`, binding.AgentID, binding.SessionID, binding.Epoch).Scan(&resultCount))
	require.Equal(t, 1, resultCount, "notice cannot append another compaction result")
	require.NoError(t, read.QueryRowContext(ctx, `SELECT count(*) FROM embedded_checkpoints WHERE agent_id=?`, binding.AgentID).Scan(&checkpointCount))
	require.Zero(t, checkpointCount, "G5 RED: cold Manager committed a checkpoint")
	require.NoError(t, read.QueryRowContext(ctx, `SELECT state FROM embedded_effects WHERE effect_id=? AND agent_id=?`, callID, binding.AgentID).Scan(&effectState))
	require.Equal(t, "unknown", effectState, "G5 RED: claimed effect must remain visible as UNKNOWN")
	require.NoError(t, read.QueryRowContext(ctx, `SELECT owner_id,owner_token FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation).Scan(&savedOwner, &savedToken))
	require.Positive(t, savedToken)
	require.Equal(t, noticeOwner, savedToken, "checked notice must belong to the recovered current owner")
	err = cold.CommitEmbeddedOwnCompactionCheckpoint(ctx, staleA, callID, headSeq, headDigest, checkpoint)
	require.ErrorContains(t, err, "stale embedded owner", "original A cannot commit after checked notice recovery")
	require.ErrorContains(t, cold.RenewEmbeddedOwner(ctx, staleA, savedOwner, embeddedOwnerTTL), "stale embedded owner", "G5 RED: stale A regained fencing authority")
}
