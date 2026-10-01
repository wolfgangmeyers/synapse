package managed

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// setEmbeddedRecoveryGeneration matches Router.Listen for direct Manager tests:
// bind a real listener, mint a Store incarnation, then set it before recovery.
func setEmbeddedRecoveryGeneration(t *testing.T, ctx context.Context, db *store.Store, m *Manager) int64 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	generation, err := db.MintEmbeddedProcessGeneration(ctx, "recovery-test-"+listener.Addr().String())
	require.NoError(t, err)
	require.NoError(t, m.SetEmbeddedStartupGeneration(generation))
	return generation
}

// The child blocks inside the checked SDK append callback, after SQLite commit
// and before the SDK can invoke StreamFn. The parent kills that actual process.
func TestEmbeddedPreEntryInitialSIGKILLRED(t *testing.T) {
	testEmbeddedPreEntrySIGKILL(t, false)
}

func TestEmbeddedC3BOriginalContinueThenNeutralWake(t *testing.T) {
	testEmbeddedPreEntrySIGKILL(t, false)
}

func TestEmbeddedPreEntryExplicitPromptSIGKILLRED(t *testing.T) {
	testEmbeddedPreEntrySIGKILL(t, true)
}

func testEmbeddedPreEntrySIGKILL(t *testing.T, explicit bool) {
	root := os.Getenv("MECHA_PREENTRY_CRASH_ROOT")
	if root != "" {
		runPreEntryChild(t, root)
		return
	}
	var err error
	root, err = os.MkdirTemp("/srv/disk-tmp/manager-focused-g1.dRRwDxZ4", "preentry-private-")
	require.NoError(t, err)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("private failed fixture retained at %s", root)
			return
		}
		_ = os.RemoveAll(root)
	})
	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.timeout=20s")
	mode := "initial"
	if explicit {
		mode = "explicit"
	}
	cmd.Env = append(os.Environ(), "MECHA_PREENTRY_CRASH_ROOT="+root, "MECHA_PREENTRY_CRASH_MODE="+mode)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	require.NoError(t, cmd.Start())
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			<-waited
		}
	}()
	marker := filepath.Join(root, "checked-user-before-entry")
	require.Eventually(t, func() bool {
		select {
		case err := <-waited:
			t.Fatalf("child exited before barrier: %v; %s", err, output.String())
		default:
		}
		b, e := os.ReadFile(marker)
		return e == nil && string(b) == "checked user committed\n"
	}, 8*time.Second, 10*time.Millisecond, "child failed to reach checked append: %s", &output)
	raw, err := sql.Open("sqlite", filepath.Join(root, "router.db"))
	require.NoError(t, err)
	defer raw.Close()
	var userBody []byte
	var originalSeq, checkedFinalSeq int64
	var originalDigest, checkedFinalDigest string
	var users, entries, effects, acks, outbox int
	prompt := "initial pre-entry instruction"
	wantUsers, wantEntries, wantFinals := 1, 0, 1
	if explicit {
		prompt = "explicit pre-entry prompt"
		wantUsers, wantEntries, wantFinals = 2, 1, 2
	}
	require.NoError(t, raw.QueryRow(`SELECT count(*),max(CASE WHEN CAST(body AS TEXT) LIKE '%' || ? || '%' THEN body END) FROM embedded_entries WHERE kind='user'`, prompt).Scan(&users, &userBody))
	require.Equal(t, wantUsers, users)
	require.Contains(t, string(userBody), prompt)
	require.NoError(t, raw.QueryRow(`SELECT seq,digest FROM embedded_entries WHERE kind='user' AND body=?`, userBody).Scan(&originalSeq, &originalDigest))
	for _, q := range []struct {
		sql    string
		result *int
	}{
		{`SELECT count(*) FROM embedded_nonmail_provider_entries`, &entries},
		{`SELECT count(*) FROM embedded_effects`, &effects},
		{`SELECT count(*) FROM embedded_acks`, &acks},
		{`SELECT count(*) FROM embedded_reply_outbox`, &outbox},
	} {
		require.NoError(t, raw.QueryRow(q.sql).Scan(q.result))
		if q.result == &entries {
			require.Equal(t, wantEntries, entries, "only the completed initial turn may have entered")
		} else {
			require.Zero(t, *q.result)
		}
	}
	var notices int
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_interrupted_notices`).Scan(&notices))
	require.Zero(t, notices)
	var savedRole, savedSession, savedGeneration string
	var savedEpoch, savedToken int64
	require.NoError(t, raw.QueryRow(`SELECT s.role,s.session_id,s.epoch,s.generation,s.owner_token FROM embedded_sessions s WHERE s.current=1`).Scan(&savedRole, &savedSession, &savedEpoch, &savedGeneration, &savedToken))
	require.Equal(t, "root", savedRole)
	require.NotEmpty(t, savedSession)
	require.Positive(t, savedEpoch)
	require.NotEmpty(t, savedGeneration)
	require.Positive(t, savedToken)
	if explicit {
		var initialFinals int
		require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_entries WHERE kind='assistant'`).Scan(&initialFinals))
		require.Equal(t, 1, initialFinals, "initial turn must finish before explicit prompt barrier")
	}
	require.NoError(t, cmd.Process.Kill())
	killErr := <-waited
	require.Error(t, killErr)
	require.Contains(t, killErr.Error(), "killed", "child must die from actual SIGKILL")
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(root, "router.db"), store.Options{Clock: deterministic.NewFixedClock(time.Now().Add(2 * time.Minute))})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	var calls, wakeCalls atomic.Int32
	var expectedGeneration atomic.Int64
	neutralBody, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "Continue working from your saved context."}}})
	require.NoError(t, err)
	neutralAnswer := func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		var scoped int
		e := raw.QueryRow(`SELECT count(*) FROM embedded_sessions s JOIN embedded_restart_wake_slots w ON w.agent_id=s.agent_id AND w.session_id=s.session_id AND w.epoch=s.epoch AND w.runtime_generation=s.generation AND w.claim_owner_token=s.owner_token JOIN embedded_entries u ON u.agent_id=s.agent_id AND u.session_id=s.session_id AND u.epoch=s.epoch AND u.generation=s.generation JOIN embedded_nonmail_provider_entries p ON p.agent_id=u.agent_id AND p.session_id=u.session_id AND p.epoch=u.epoch AND p.generation=u.generation AND p.user_seq=u.seq AND p.user_digest=u.digest JOIN embedded_entries f ON f.agent_id=u.agent_id AND f.session_id=u.session_id AND f.epoch=u.epoch AND f.generation=u.generation AND f.seq=u.seq-1 WHERE s.current=1 AND s.agent_id='preentry-root' AND s.owner_token>? AND w.slot='current' AND w.process_generation=? AND w.kind='root-idle' AND w.state='claimed' AND w.notice_id IS NULL AND w.digest=f.digest AND f.kind='assistant' AND json_extract(f.body,'$.stopReason')='stop' AND u.kind='user' AND u.body=? AND u.seq=(SELECT max(seq) FROM embedded_entries WHERE agent_id=s.agent_id)`, savedToken, expectedGeneration.Load(), neutralBody).Scan(&scoped)
		if e != nil || scoped != 1 || len(h.Messages) == 0 || !bytes.Equal(mustMessageJSON(h.Messages[len(h.Messages)-1]), neutralBody) {
			panic("neutral provider entry requires exact current owner, process generation and checked predecessor")
		}
		wakeCalls.Add(1)
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "neutral recovery complete"}}, ai.StopStop)
	}
	reg.SetResponses([]providers.FauxResponseStep{func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		calls.Add(1)
		var originals, notices int
		for _, msg := range h.Messages {
			if u, ok := msg.(ai.UserMessage); ok {
				text := strings.ToLower(strings.TrimSpace(string(mustMessageJSON(u))))
				if strings.Contains(text, prompt) {
					originals++
				}
				if strings.Contains(text, "interrupted") {
					notices++
				}
			}
		}
		if originals != 1 || notices != 0 {
			panic("must resume original checked turn, not a notice or duplicate user")
		}
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "resumed once"}}, ai.StopStop)
	}, neutralAnswer, neutralAnswer})
	options := ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-sol", Available: true}), CodexCredentialStore: testfixture.CodexAdmission(t), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() }}
	// Match Router.Listen: only a successfully bound listener may mint this
	// process incarnation, and the Manager receives it before recovery.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	generation, err := db.MintEmbeddedProcessGeneration(ctx, "preentry-"+listener.Addr().String())
	require.NoError(t, err)
	var m *Manager
	if strings.Contains(t.Name(), "C3B") {
		var runner *Manager
		options.EmbeddedRunnerFactory = func(runCtx context.Context, saved store.ManagedAgentRecord) error {
			return runner.runEmbeddedSessionOriginalRestart(runCtx, saved, generation)
		}
		runner, err = NewManager(options)
		require.NoError(t, err)
		m = runner
	} else {
		m, err = NewManager(options)
		require.NoError(t, err)
	}
	defer m.Close(ctx)
	require.NoError(t, m.SetEmbeddedStartupGeneration(generation))
	expectedGeneration.Store(generation)
	require.NoError(t, m.RecoverEmbeddedRoots(ctx))
	// Acquisition is a separate gate: if it never happens, a missing callback
	// says nothing about the original-turn Continue path.
	require.Eventually(t, func() bool {
		var role, session, runtimeGeneration string
		var epoch, token int64
		e := raw.QueryRow(`SELECT role,session_id,epoch,generation,owner_token FROM embedded_sessions WHERE current=1`).Scan(&role, &session, &epoch, &runtimeGeneration, &token)
		return e == nil && role == savedRole && session == savedSession && epoch == savedEpoch && runtimeGeneration == savedGeneration && token > savedToken
	}, 35*time.Second, 20*time.Millisecond, "recovered Manager did not acquire saved root owner lease")
	require.Eventually(t, func() bool { return calls.Load() == 1 }, 5*time.Second, 10*time.Millisecond, "owner acquired but checked original turn did not reach provider exactly once")
	if !strings.Contains(t.Name(), "C3B") {
		// A settled owner retains its heartbeat in startWG. First prove the
		// exact generation's checked wake, then abruptly cancel/join this
		// fixture context without Close releasing the saved owner or intent.
		require.Eventually(t, func() bool {
			var checked int
			return wakeCalls.Load() == 1 && raw.QueryRow(`SELECT count(*) FROM embedded_restart_wake_slots WHERE agent_id='preentry-root' AND process_generation=? AND state='checked-complete'`, generation).Scan(&checked) == nil && checked == 1
		}, 5*time.Second, 10*time.Millisecond)
		m.cancel()
	}
	m.startWG.Wait()
	firstWakes := 1
	if strings.Contains(t.Name(), "C3B") {
		firstWakes = 0 // This seam plans awareness but does not dispatch it.
	}
	require.EqualValues(t, firstWakes, wakeCalls.Load())
	var restoredBody []byte
	require.NoError(t, raw.QueryRow(`SELECT count(*),max(CASE WHEN CAST(body AS TEXT) LIKE '%' || ? || '%' THEN body END) FROM embedded_entries WHERE kind='user'`, prompt).Scan(&users, &restoredBody))
	require.Equal(t, wantUsers+firstWakes, users)
	require.Equal(t, userBody, restoredBody, "the original checked SDK user stays byte-identical")
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_nonmail_provider_entries`).Scan(&entries))
	require.Equal(t, wantEntries+1+firstWakes, entries)
	require.Zero(t, effects)
	require.Zero(t, acks)
	require.Zero(t, outbox)
	require.Eventually(t, func() bool {
		var finals int
		return raw.QueryRow(`SELECT count(*) FROM embedded_entries WHERE kind='assistant'`).Scan(&finals) == nil && finals == wantFinals+firstWakes
	}, 5*time.Second, 10*time.Millisecond, "resumed turn must commit its final")
	if explicit {
		require.NoError(t, raw.QueryRow(`SELECT seq,digest FROM embedded_entries WHERE kind='assistant' AND seq>? ORDER BY seq LIMIT 1`, originalSeq).Scan(&checkedFinalSeq, &checkedFinalDigest))
	} else {
		require.NoError(t, raw.QueryRow(`SELECT seq,digest FROM embedded_entries WHERE kind='assistant' AND seq=?`, originalSeq+1).Scan(&checkedFinalSeq, &checkedFinalDigest))
	}
	if firstWakes == 1 {
		assertPreEntryNeutralWake(t, raw, generation, checkedFinalSeq, checkedFinalDigest, savedToken)
	}
	if strings.Contains(t.Name(), "C3B") {
		require.Eventually(t, func() bool {
			var kind, state string
			var found int
			e := raw.QueryRow(`SELECT count(*),coalesce(max(kind),''),coalesce(max(state),'') FROM embedded_restart_wake_slots WHERE process_generation=?`, generation).Scan(&found, &kind, &state)
			return e == nil && found == 1 && kind == "root-idle" && state == "planned"
		}, 3*time.Second, 10*time.Millisecond, "neutral wake only after checked original final")
	}
	require.NoError(t, m.Close(ctx))
	require.NoError(t, db.Close())
	cold, err := store.Open(ctx, filepath.Join(root, "router.db"), store.Options{Clock: deterministic.NewFixedClock(time.Now().Add(4 * time.Minute))})
	require.NoError(t, err)
	defer cold.Close()
	again, err := NewManager(ManagerOptions{Store: cold, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-sol", Available: true}), CodexCredentialStore: testfixture.CodexAdmission(t), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() }})
	require.NoError(t, err)
	defer again.Close(ctx)
	secondGeneration := setEmbeddedRecoveryGeneration(t, ctx, cold, again)
	expectedGeneration.Store(secondGeneration)
	require.NoError(t, again.RecoverEmbeddedRoots(ctx))
	require.Eventually(t, func() bool {
		var checked int
		err := raw.QueryRow(`SELECT count(*) FROM embedded_restart_wake_slots WHERE process_generation=? AND state='checked-complete'`, secondGeneration).Scan(&checked)
		return err == nil && checked == 1 && wakeCalls.Load() == int32(firstWakes+1)
	}, 5*time.Second, 10*time.Millisecond, "cold reopen must finish its checked neutral turn")
	require.EqualValues(t, 1, calls.Load(), "cold reopen must not resume a second time")
	require.EqualValues(t, firstWakes+1, wakeCalls.Load(), "one distinct awareness turn per dispatched generation")
	predecessorSeq, predecessorDigest := checkedFinalSeq, checkedFinalDigest
	if firstWakes == 1 {
		require.NoError(t, raw.QueryRow(`SELECT seq,digest FROM embedded_entries WHERE seq=?`, checkedFinalSeq+2).Scan(&predecessorSeq, &predecessorDigest))
	}
	assertPreEntryNeutralWake(t, raw, secondGeneration, predecessorSeq, predecessorDigest, savedToken)
	require.NoError(t, raw.QueryRow(`SELECT count(*),max(CASE WHEN CAST(body AS TEXT) LIKE '%' || ? || '%' THEN body END) FROM embedded_entries WHERE kind='user'`, prompt).Scan(&users, &restoredBody))
	logPreEntryAttribution(t, raw, userBody, originalSeq, originalDigest, checkedFinalSeq, checkedFinalDigest)
	if explicit {
		assertExplicitPreEntryUsers(t, raw, userBody, originalSeq, checkedFinalSeq, checkedFinalDigest, firstWakes+1)
		var checkedFinals int
		require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_entries WHERE seq=? AND kind='assistant' AND digest=?`, checkedFinalSeq, checkedFinalDigest).Scan(&checkedFinals))
		require.Equal(t, 1, checkedFinals, "checked original final must survive cold reopen")
	} else {
		require.Equal(t, wantUsers+firstWakes+1, users)
		var finalCount int
		require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_entries WHERE kind='assistant'`).Scan(&finalCount))
		require.Equal(t, wantFinals+firstWakes+1, finalCount)
	}
	var originalUsers, originalEntries, originalFinals int
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_entries WHERE kind='user' AND body=? AND seq=? AND digest=?`, userBody, originalSeq, originalDigest).Scan(&originalUsers))
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_nonmail_provider_entries WHERE user_seq=? AND user_digest=?`, originalSeq, originalDigest).Scan(&originalEntries))
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_entries WHERE kind='assistant' AND seq=? AND digest=? AND json_extract(body,'$.stopReason')='stop'`, checkedFinalSeq, checkedFinalDigest).Scan(&originalFinals))
	require.Equal(t, 1, originalUsers)
	require.Equal(t, 1, originalEntries, "original provider entry remains unique")
	require.Equal(t, 1, originalFinals, "immutable checked original stop final survives recovery")
	require.Equal(t, userBody, restoredBody, "second cold reopen must preserve the original checked prompt")
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_nonmail_provider_entries`).Scan(&entries))
	require.Equal(t, wantEntries+1+firstWakes+1, entries)
	for _, table := range []string{"embedded_effects", "embedded_acks", "embedded_reply_outbox", "embedded_interrupted_notices"} {
		var count int
		require.NoError(t, raw.QueryRow(`SELECT count(*) FROM `+table).Scan(&count))
		require.Zero(t, count, table)
	}
}

// Each extra user is a distinct checked neutral wake, never an original replay.
func assertExplicitPreEntryUsers(t *testing.T, raw *sql.DB, original []byte, originalSeq, finalSeq int64, finalDigest string, wantNeutrals int) {
	t.Helper()
	neutral, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "Continue working from your saved context."}}})
	require.NoError(t, err)
	rows, err := raw.Query(`SELECT seq,body FROM embedded_entries WHERE kind='user' ORDER BY seq`)
	require.NoError(t, err)
	defer rows.Close()
	var initial, originals, neutrals int
	for rows.Next() {
		var seq int64
		var body []byte
		require.NoError(t, rows.Scan(&seq, &body))
		switch {
		case seq == 2 && !bytes.Equal(body, original) && !bytes.Equal(body, neutral):
			initial++
		case seq == originalSeq && bytes.Equal(body, original):
			originals++
		case seq > finalSeq && bytes.Equal(body, neutral):
			neutrals++
		default:
			t.Fatalf("unexpected or duplicate user entry at seq=%d", seq)
		}
	}
	require.NoError(t, rows.Err())
	require.Equal(t, 1, initial)
	require.Equal(t, 1, originals, "original checked user must occur exactly once")
	require.Equal(t, wantNeutrals, neutrals, "only the precisely attributed recovery users are permitted")
}

func assertPreEntryNeutralWake(t *testing.T, raw *sql.DB, generation, predecessorSeq int64, predecessorDigest string, oldOwner int64) {
	t.Helper()
	var scoped int
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_restart_wake_slots w JOIN embedded_sessions s ON s.agent_id=w.agent_id AND s.session_id=w.session_id AND s.epoch=w.epoch AND s.generation=w.runtime_generation JOIN embedded_entries u ON u.agent_id=w.agent_id AND u.session_id=w.session_id AND u.epoch=w.epoch AND u.generation=w.runtime_generation AND u.seq=? JOIN embedded_nonmail_provider_entries p ON p.agent_id=u.agent_id AND p.session_id=u.session_id AND p.epoch=u.epoch AND p.generation=u.generation AND p.user_seq=u.seq AND p.user_digest=u.digest JOIN embedded_restart_wake_checked_origins o ON o.agent_id=w.agent_id AND o.slot=w.slot AND o.session_id=w.session_id AND o.epoch=w.epoch AND o.runtime_generation=w.runtime_generation AND o.wake_id=w.wake_id AND o.process_generation=w.process_generation AND o.claim_owner_token=w.claim_owner_token JOIN embedded_entries f ON f.agent_id=o.agent_id AND f.session_id=o.session_id AND f.epoch=o.epoch AND f.generation=o.runtime_generation AND f.seq=o.seq WHERE w.agent_id='preentry-root' AND s.current=1 AND w.slot='current' AND w.claim_owner_token=s.owner_token AND w.claim_owner_token>? AND w.process_generation=? AND w.kind='root-idle' AND w.state='checked-complete' AND w.notice_id IS NULL AND w.digest=? AND w.wake_id='settled:preentry-root:' || (? - 1) || ':' || ? AND u.kind='user' AND json_extract(u.body,'$.content[0].text')='Continue working from your saved context.' AND f.seq=u.seq+1 AND f.kind='assistant' AND json_extract(f.body,'$.stopReason')='stop'`, predecessorSeq+1, oldOwner, generation, predecessorDigest, predecessorSeq, generation).Scan(&scoped))
	require.Equal(t, 1, scoped, "neutral requires its exact fenced wake, provider entry and checked-origin stop final")
}

// Attribution prints metadata and in-memory equality only; SDK message bodies stay private.
func logPreEntryAttribution(t *testing.T, raw *sql.DB, original []byte, originalSeq int64, originalDigest string, finalSeq int64, finalDigest string) {
	t.Helper()
	neutral, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "Continue working from your saved context."}}})
	require.NoError(t, err)
	t.Logf("checked original user seq=%d digest=%s; immutable checked final seq=%d digest=%s", originalSeq, originalDigest, finalSeq, finalDigest)
	rows, err := raw.Query(`SELECT seq,digest,kind,body FROM embedded_entries ORDER BY seq`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var seq int64
		var digest, kind string
		var body []byte
		require.NoError(t, rows.Scan(&seq, &digest, &kind, &body))
		if kind == "user" {
			t.Logf("embedded_entries user seq=%d digest=%s kind=%s original_exact=%t neutral_exact=%t after_checked_final=%t", seq, digest, kind, bytes.Equal(body, original), bytes.Equal(body, neutral), seq > finalSeq)
		}
	}
	require.NoError(t, rows.Err())
	providerRows, err := raw.Query(`SELECT user_seq,user_digest FROM embedded_nonmail_provider_entries ORDER BY user_seq`)
	require.NoError(t, err)
	defer providerRows.Close()
	for providerRows.Next() {
		var seq int64
		var digest string
		require.NoError(t, providerRows.Scan(&seq, &digest))
		t.Logf("embedded_nonmail_provider_entries user_seq=%d user_digest=%s", seq, digest)
	}
	require.NoError(t, providerRows.Err())
	wakeRows, err := raw.Query(`SELECT w.slot,w.kind,w.state,w.process_generation,o.seq,e.digest FROM embedded_restart_wake_slots w LEFT JOIN embedded_restart_wake_checked_origins o ON o.agent_id=w.agent_id AND o.slot=w.slot LEFT JOIN embedded_entries e ON e.agent_id=o.agent_id AND e.session_id=o.session_id AND e.epoch=o.epoch AND e.generation=o.runtime_generation AND e.seq=o.seq ORDER BY w.slot`)
	require.NoError(t, err)
	defer wakeRows.Close()
	for wakeRows.Next() {
		var slot, kind, state string
		var generation int64
		var originSeq sql.NullInt64
		var originDigest sql.NullString
		require.NoError(t, wakeRows.Scan(&slot, &kind, &state, &generation, &originSeq, &originDigest))
		t.Logf("wake slot=%s kind=%s state=%s generation=%d origin_seq=%v origin_digest=%v", slot, kind, state, generation, originSeq, originDigest)
	}
	require.NoError(t, wakeRows.Err())
}

func runPreEntryChild(t *testing.T, root string) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(root, "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	reg.SetResponses([]providers.FauxResponseStep{func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
		panic("provider callback reached before SIGKILL")
	}})
	barrier := func(context.Context) {
		f, e := os.OpenFile(filepath.Join(root, "checked-user-before-entry"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			panic(e)
		}
		if _, e = f.WriteString("checked user committed\n"); e != nil {
			panic(e)
		}
		if e = f.Sync(); e != nil {
			panic(e)
		}
		if e = f.Close(); e != nil {
			panic(e)
		}
		select {}
	}
	var m *Manager
	options := ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-sol", Available: true}), CodexCredentialStore: testfixture.CodexAdmission(t), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() }}
	if os.Getenv("MECHA_PREENTRY_CRASH_MODE") == "explicit" {
		reg.SetResponses([]providers.FauxResponseStep{func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "initial complete"}}, ai.StopStop)
		}})
		options.EmbeddedRunnerFactory = func(runCtx context.Context, saved store.ManagedAgentRecord) error {
			if err := m.runEmbeddedSession(runCtx, saved); err != nil {
				return err
			}
			m.embeddedAfterUserAppend = barrier
			return m.runEmbeddedSessionPrompt(runCtx, saved, "explicit pre-entry prompt")
		}
	}
	m, err = NewManager(options)
	require.NoError(t, err)
	if os.Getenv("MECHA_PREENTRY_CRASH_MODE") != "explicit" {
		m.embeddedAfterUserAppend = barrier
	}
	_, err = m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "preentry-root", Cwd: root, Runtime: "embedded", InitialInstruction: "initial pre-entry instruction", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-sol"}, RequestID: "a2222222-2222-4222-8222-222222222222"})
	require.NoError(t, err)
	select {}
}
