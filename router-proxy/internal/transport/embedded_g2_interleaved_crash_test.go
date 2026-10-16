package transport

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// setTransportEmbeddedRecoveryGeneration mirrors Listen's authority ordering
// for direct Manager recovery: owned bind, Store mint, then Manager set.
func setTransportEmbeddedRecoveryGeneration(t *testing.T, ctx context.Context, db *store.Store, manager *managed.Manager) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	generation, err := db.MintEmbeddedProcessGeneration(ctx, "transport-recovery-test-"+listener.Addr().String())
	require.NoError(t, err)
	require.NoError(t, manager.SetEmbeddedStartupGeneration(generation))
}

// B waits at its provider barrier. The child dies after A's final and outbox
// commit, before ordinary reply publication. No production interception.
type interleavedG2Fixture struct {
	*embeddedHTTPG2Fixture
	projects    [2]string // A, B
	blockedB    chan struct{}
	releaseB    chan struct{}
	writeResult chan *ai.ToolResultMessage
}

func newInterleavedG2Fixture(t *testing.T) *interleavedG2Fixture {
	return newInterleavedG2FixtureWithListen(t, false)
}

func newInterleavedG2FixtureWithListen(t *testing.T, listen bool) *interleavedG2Fixture {
	t.Helper()
	ctx := context.Background()
	root := os.Getenv("MECHA_G2_CRASH_ROOT")
	if root == "" {
		root = t.TempDir()
	}
	f := &interleavedG2Fixture{
		embeddedHTTPG2Fixture: &embeddedHTTPG2Fixture{root: root},
		blockedB:              make(chan struct{}), releaseB: make(chan struct{}),
		writeResult: make(chan *ai.ToolResultMessage, 1),
	}
	for i, name := range []string{"project-a", "project-b"} {
		f.projects[i] = filepath.Join(f.root, name)
		require.NoError(t, os.Mkdir(f.projects[i], 0700))
	}
	f.dbPath = filepath.Join(f.root, "router.db")
	opts := store.Options{}
	if opts.Migrations == nil {
		if err := store.CloneTemplateDB(t, f.dbPath); err == nil {
			opts.Migrations = []store.Migration{}
		}
	}
	var err error
	f.db, err = store.Open(ctx, f.dbPath, opts)
	require.NoError(t, err)
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	a := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	b := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	t.Cleanup(a.Unregister)
	t.Cleanup(b.Unregister)
	ready := func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "ready"}}, ai.StopStop)
	}
	a.SetResponses([]providers.FauxResponseStep{
		ready,
		func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("write", map[string]any{"path": "effect.txt", "content": "A wrote once\n"}, "g2-a-write-once")}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			var result *ai.ToolResultMessage
			for _, msg := range h.Messages {
				if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == "g2-a-write-once" {
					copy := r
					result = &copy
				}
			}
			f.writeResult <- result
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "A wrote once"}}, ai.StopStop)
		},
	})
	b.SetResponses([]providers.FauxResponseStep{
		ready,
		func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
			// A checked mail ACK is not proof that the provider was entered.
			// This marker is written only inside B's provider callback.
			file, err := os.OpenFile(filepath.Join(f.root, "b-provider-entered"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				panic(err)
			}
			if _, err = file.WriteString("B provider entered\n"); err != nil {
				panic(err)
			}
			if err = file.Sync(); err != nil {
				panic(err)
			}
			if err = file.Close(); err != nil {
				panic(err)
			}
			close(f.blockedB)
			<-f.releaseB
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "B finished"}}, ai.StopStop)
		},
	})
	f.manager, err = managed.NewManager(managed.ManagerOptions{CodexCredentialStore: testfixture.CodexAdmission(t),
		Store: f.db, Manifest: manifest,
		Config:        managed.Config{SessionRoot: filepath.Join(f.root, "sessions"), AllowedWorkingDirectoryRoots: []string{f.root}},
		CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(row store.ManagedAgentRecord) *ai.Model {
			if row.AgentID == "g2-worker-a" {
				return a.GetModel()
			}
			return b.GetModel()
		},
	})
	require.NoError(t, err)
	f.router, err = New(Options{Store: f.db, Managed: f.manager, Host: "127.0.0.1", Port: 0, DefaultEmbeddedRoot: true})
	require.NoError(t, err)
	if listen {
		require.NoError(t, f.router.Listen(ctx))
	} else {
		f.server = httptest.NewServer(f.router)
	}
	t.Cleanup(func() {
		if !listen {
			f.server.Close()
		}
		_ = f.router.Close(ctx)
		_ = f.db.Close()
	})
	// Release the blocked provider before the router's shutdown cleanup.
	if !listen {
		t.Cleanup(func() { close(f.releaseB) })
	}
	return f
}

func (f *interleavedG2Fixture) call(method, path, id, body string) (int, []byte) {
	base := ""
	if f.server != nil {
		base = f.server.URL
	} else {
		base, _ = f.router.URLs()
	}
	req, err := http.NewRequest(method, base+path, strings.NewReader(body))
	if err != nil {
		panic(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if id != "" {
		req.Header.Set("X-Request-ID", id)
	}
	resp, err := (&http.Client{Timeout: time.Second}).Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}
	return resp.StatusCode, raw
}

func (f *interleavedG2Fixture) launch(t *testing.T) [2]store.ManagedAgentRecord {
	t.Helper()
	ctx := context.Background()
	var rows [2]store.ManagedAgentRecord
	for i, name := range []string{"g2-worker-a", "g2-worker-b"} {
		status, raw := f.call(http.MethodPost, "/v1/managed-agents", fmt.Sprintf("c6111111-1111-4111-8111-%012d", i+1),
			fmt.Sprintf(`{"name":%q,"cwd":%q,"model":{"provider":"openai-codex","id":"gpt-5.6-terra"},"initial_instruction":%q}`, name, f.projects[i], "independent instruction for "+name))
		require.Equal(t, http.StatusAccepted, status, string(raw))
	}
	var states [2]string
	require.Eventually(t, func() bool {
		ready := true
		for i, name := range []string{"g2-worker-a", "g2-worker-b"} {
			row, err := f.db.ManagedAgent(ctx, name)
			if err != nil {
				states[i] = fmt.Sprint(err)
				ready = false
				continue
			}
			states[i] = row.LifecycleState + " / " + row.LastErrorMessage
			if row.LifecycleState != "running" {
				ready = false
			}
			rows[i] = *row
		}
		return ready
	}, 4*time.Second, 10*time.Millisecond, "launch states: %v", &states)
	for _, row := range rows {
		binding := store.EmbeddedBinding{AgentID: row.AgentID, SessionID: row.PiSessionID, Epoch: row.SessionEpoch, Generation: row.RuntimeGeneration}
		role, err := f.db.EmbeddedSessionRole(ctx, binding)
		require.NoError(t, err)
		require.Equal(t, "worker", role, "durable session role for %s", row.AgentID)
		require.Equal(t, "embedded", row.Runtime)
		require.NotEmpty(t, row.PiSessionID)
		require.NotEmpty(t, row.RuntimeGeneration)
	}
	require.NotEqual(t, rows[0].AgentID, rows[1].AgentID)
	require.NotEqual(t, rows[0].PiSessionID, rows[1].PiSessionID)
	require.NotEqual(t, rows[0].RuntimeGeneration, rows[1].RuntimeGeneration)
	return rows
}

// Check the saved attempt and ACK together, including its SDK user append.
// A queue response or a provider callback alone does not prove processing.
func (f *interleavedG2Fixture) assertExactACK(t *testing.T, id, sender, recipient, body string, row store.ManagedAgentRecord) string {
	t.Helper()
	ctx := context.Background()
	check, err := sql.Open("sqlite", f.dbPath)
	require.NoError(t, err)
	defer check.Close()
	var attempt, gotSender, gotRecipient, session, generation, gotBody, kind, state string
	var epoch int64
	var sdkBody []byte
	require.Eventually(t, func() bool {
		err := check.QueryRowContext(ctx, `SELECT a.attempt_id,a.sender,a.agent_id,a.session_id,a.epoch,a.generation,a.accepted_body,e.kind,e.body,m.state
			FROM embedded_attempts a JOIN embedded_acks k ON k.attempt_id=a.attempt_id AND k.agent_id=a.agent_id AND k.session_id=a.session_id AND k.epoch=a.epoch AND k.generation=a.generation
			JOIN embedded_entries e ON e.agent_id=k.agent_id AND e.session_id=k.session_id AND e.epoch=k.epoch AND e.seq=k.entry_seq
			JOIN messages m ON m.id=a.message_id WHERE a.message_id=?`, id).
			Scan(&attempt, &gotSender, &gotRecipient, &session, &epoch, &generation, &gotBody, &kind, &sdkBody, &state)
		return err == nil
	}, 4*time.Second, 10*time.Millisecond, "exact processing ACK for %s", id)
	require.NotEmpty(t, attempt)
	require.Equal(t, sender, gotSender)
	require.Equal(t, recipient, gotRecipient)
	require.Equal(t, row.PiSessionID, session)
	require.Equal(t, row.SessionEpoch, epoch)
	require.Equal(t, row.RuntimeGeneration, generation)
	require.Equal(t, body, gotBody)
	require.Equal(t, "user", kind)
	require.Contains(t, string(sdkBody), attempt)
	require.Contains(t, string(sdkBody), sender)
	require.Contains(t, string(sdkBody), body)
	require.Equal(t, string(domain.MessageAcknowledged), state)
	return attempt
}

// A live Router.Listen must keep serving health and A's published reply while
// B is inside its provider callback. This does not exercise Bash or CPU isolation.
func TestEmbeddedG2BlockedProviderSiblingHTTPProgress(t *testing.T) {
	f := newInterleavedG2FixtureWithListen(t, true)
	defer close(f.releaseB) // release before router shutdown, including on failure
	rows := f.launch(t)
	base, _ := f.router.URLs()
	client := &http.Client{Timeout: time.Second}
	call := func(method, path, body string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, base+path, strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		require.NoError(t, err, "bounded HTTP %s", path)
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, raw
	}
	send := func(id, sender, recipient, body string) {
		t.Helper()
		status, raw := call(http.MethodPost, "/v1/messages", fmt.Sprintf(`{"id":%q,"from":%q,"to":%q,"body":%q,"created_at":"2026-09-27T00:00:00.000Z"}`, id, sender, recipient, body))
		require.Equal(t, http.StatusCreated, status, string(raw))
	}
	const bID, aID = "c7222222-2222-4222-8222-222222222222", "c7333333-3333-4333-8333-333333333333"
	send(bID, "external-b", "g2-worker-b", "B addressed turn")
	select {
	case <-f.blockedB:
	case <-time.After(4 * time.Second):
		t.Fatal("B did not enter its provider barrier")
	}
	entered, err := os.ReadFile(filepath.Join(f.root, "b-provider-entered"))
	require.NoError(t, err)
	require.Equal(t, "B provider entered\n", string(entered))
	attemptB := f.assertExactACK(t, bID, "external-b", "g2-worker-b", "B addressed turn", rows[1])
	status, raw := call(http.MethodGet, "/v1/health", "")
	require.Equal(t, http.StatusOK, status, string(raw))
	send(aID, "external-a", "g2-worker-a", "A addressed turn")
	attemptA := f.assertExactACK(t, aID, "external-a", "g2-worker-a", "A addressed turn", rows[0])
	select {
	case result := <-f.writeResult:
		require.NotNil(t, result)
		require.Equal(t, "g2-a-write-once", result.ToolCallID)
		require.False(t, result.IsError)
	case <-time.After(4 * time.Second):
		t.Fatal("A's write result did not reach its provider")
	}
	written, err := os.ReadFile(filepath.Join(f.projects[0], "effect.txt"))
	require.NoError(t, err)
	require.Equal(t, "A wrote once\n", string(written))
	require.NoFileExists(t, filepath.Join(f.projects[1], "effect.txt"))
	var replyID string
	require.Eventually(t, func() bool {
		status, raw := call(http.MethodGet, "/v1/conversations?owner=external-a&peer=g2-worker-a", "")
		var result struct {
			Messages []struct{ ID, From, To, Body string } `json:"messages"`
		}
		if status != http.StatusOK || json.Unmarshal(raw, &result) != nil || len(result.Messages) != 2 {
			return false
		}
		reply := result.Messages[1]
		replyID = reply.ID
		return result.Messages[0].ID == aID && reply.ID != "" && reply.From == "g2-worker-a" && reply.To == "external-a" && reply.Body == "A wrote once"
	}, 4*time.Second, 10*time.Millisecond, "A's final sender-bound reply must be retrievable while B is blocked")
	var finalA, finalB, replyB, deliveredA int
	check, err := sql.Open("sqlite", f.dbPath)
	require.NoError(t, err)
	defer check.Close()
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM embedded_entries WHERE agent_id='g2-worker-a' AND kind='assistant' AND seq=(SELECT entry_seq FROM embedded_reply_outbox WHERE attempt_id=?)`, attemptA).Scan(&finalA))
	require.Equal(t, 1, finalA)
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM embedded_reply_outbox WHERE attempt_id=? AND reply_id=? AND state='delivered'`, attemptA, replyID).Scan(&deliveredA))
	require.Equal(t, 1, deliveredA)
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM embedded_entries WHERE agent_id='g2-worker-b' AND kind='assistant' AND seq>(SELECT entry_seq FROM embedded_acks WHERE attempt_id=?)`, attemptB).Scan(&finalB))
	require.Zero(t, finalB, "B cannot finish before provider release")
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM embedded_reply_outbox WHERE attempt_id=?`, attemptB).Scan(&replyB))
	require.Zero(t, replyB)
	status, raw = call(http.MethodGet, "/v1/conversations?owner=external-b&peer=g2-worker-b", "")
	require.Equal(t, http.StatusOK, status, string(raw))
	var bHistory struct {
		Messages []struct{ ID, From, To, Body string } `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(raw, &bHistory))
	require.Len(t, bHistory.Messages, 1, "B cannot publish a reply before provider release")
	require.Equal(t, bID, bHistory.Messages[0].ID)
}

// This is one test in two processes. The child reaches a synced marker only
// after the committed final/outbox exists. The parent kills it before publish.
func TestEmbeddedG2InterleavedCrashScaffold(t *testing.T) {
	if root := os.Getenv("MECHA_G2_CRASH_ROOT"); root != "" {
		runInterleavedG2Child(t, root)
		return
	}
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestEmbeddedG2InterleavedCrashScaffold$", "-test.timeout=15s")
	cmd.Env = append(os.Environ(), "MECHA_G2_CRASH_ROOT="+root)
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
	marker := filepath.Join(root, "outbox-before-publish")
	require.Eventually(t, func() bool {
		select {
		case err := <-waited:
			t.Fatalf("child exited before durable marker: %v; output: %s", err, output.String())
		default:
		}
		data, err := os.ReadFile(marker)
		return err == nil && string(data) == "outbox committed\n"
	}, 8*time.Second, 10*time.Millisecond, "child did not reach outbox-before-publish marker: %s", &output)
	entered, err := os.ReadFile(filepath.Join(root, "b-provider-entered"))
	require.NoError(t, err, "B's ACK alone cannot establish provider entry")
	require.Equal(t, "B provider entered\n", string(entered))
	require.NoError(t, cmd.Process.Kill())
	require.Error(t, <-waited, "child should have been killed at the barrier")
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(root, "router.db"), store.Options{Clock: deterministic.NewFixedClock(time.Now().Add(2 * time.Minute))})
	require.NoError(t, err)
	defer db.Close()
	manifest := mustEffectManifest(t)
	var bRequests atomic.Int32
	bContexts := make(chan ai.TranscriptContext, 2)
	bProvider := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer bProvider.Unregister()
	bProvider.SetResponses([]providers.FauxResponseStep{func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		bRequests.Add(1)
		bContexts <- h
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "recovery observed"}}, ai.StopStop)
	}})
	manager, err := managed.NewManager(managed.ManagerOptions{Store: db, Manifest: manifest, Config: managed.Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CodexCredentialStore: testfixture.CodexAdmission(t),
		CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(row store.ManagedAgentRecord) *ai.Model {
			if row.AgentID == "g2-worker-b" {
				return bProvider.GetModel()
			}
			return nil // A's saved outbox is recovered, not a new provider turn.
		},
	})
	require.NoError(t, err)
	defer manager.Close(ctx)
	router, err := New(Options{Store: db, Managed: manager, DefaultEmbeddedRoot: true})
	require.NoError(t, err)
	defer router.Close(ctx)
	const aID, bID = "c6333333-3333-4333-8333-333333333333", "c6222222-2222-4222-8222-222222222222"
	var attemptA, attemptB string
	raw, err := sql.Open("sqlite", filepath.Join(root, "router.db"))
	require.NoError(t, err)
	defer raw.Close()
	for i, item := range []struct{ id, sender, agent, body string }{
		{aID, "external-a", "g2-worker-a", "A addressed turn"},
		{bID, "external-b", "g2-worker-b", "B addressed turn"},
	} {
		row, err := db.ManagedAgent(ctx, item.agent)
		require.NoError(t, err)
		require.Equal(t, "embedded", row.Runtime)
		var attempt, sender, agent, session, generation, body, kind, state string
		var epoch int64
		var sdkBody []byte
		err = raw.QueryRowContext(ctx, `SELECT a.attempt_id,a.sender,a.agent_id,a.session_id,a.epoch,a.generation,a.accepted_body,e.kind,e.body,m.state FROM embedded_attempts a JOIN embedded_acks k ON k.attempt_id=a.attempt_id AND k.agent_id=a.agent_id AND k.session_id=a.session_id AND k.epoch=a.epoch AND k.generation=a.generation JOIN embedded_entries e ON e.agent_id=k.agent_id AND e.session_id=k.session_id AND e.epoch=k.epoch AND e.seq=k.entry_seq JOIN messages m ON m.id=a.message_id WHERE a.message_id=?`, item.id).Scan(&attempt, &sender, &agent, &session, &epoch, &generation, &body, &kind, &sdkBody, &state)
		require.NoError(t, err)
		require.NotEmpty(t, attempt)
		require.Equal(t, item.sender, sender)
		require.Equal(t, item.agent, agent)
		require.Equal(t, row.PiSessionID, session)
		require.Equal(t, row.SessionEpoch, epoch)
		require.Equal(t, row.RuntimeGeneration, generation)
		require.Equal(t, item.body, body)
		require.Equal(t, "user", kind)
		require.Contains(t, string(sdkBody), attempt)
		require.Contains(t, string(sdkBody), sender)
		require.Contains(t, string(sdkBody), body)
		require.Equal(t, string(domain.MessageAcknowledged), state, "processing ACK, not HTTP acceptance")
		if i == 0 {
			attemptA = attempt
		} else {
			attemptB = attempt
		}
	}
	written, err := os.ReadFile(filepath.Join(root, "project-a", "effect.txt"))
	require.NoError(t, err)
	require.Equal(t, "A wrote once\n", string(written))
	require.NoFileExists(t, filepath.Join(root, "project-b", "effect.txt"))
	var effectState string
	var effectCount, resultCount, finalCount, bFinalCount, outboxCount int
	require.NoError(t, raw.QueryRow(`SELECT count(*),max(state) FROM embedded_effects WHERE effect_id='g2-a-write-once' AND agent_id='g2-worker-a'`).Scan(&effectCount, &effectState))
	require.Equal(t, 1, effectCount)
	require.Equal(t, "unknown", effectState, "SDK write has a saved result but its file intent remains conservatively uncertain")
	aRow, err := db.ManagedAgent(ctx, "g2-worker-a")
	require.NoError(t, err)
	binding := store.EmbeddedBinding{AgentID: aRow.AgentID, SessionID: aRow.PiSessionID, Epoch: aRow.SessionEpoch, Generation: aRow.RuntimeGeneration}
	claim, err := db.AcquireEmbeddedOwner(ctx, binding, "g2-cold-recovery", 30*time.Second)
	require.NoError(t, err, "old owner lease must expire on the recovery clock")
	binding.OwnerToken = claim
	effect, err := db.RecoverEmbeddedEffect(ctx, binding, "g2-a-write-once")
	require.NoError(t, err)
	require.Equal(t, "unknown", effect.State)
	require.False(t, effect.ShouldReplay, "never rerun uncertain SDK write")
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_entries WHERE agent_id='g2-worker-a' AND kind='toolResult'`).Scan(&resultCount))
	require.Equal(t, 1, resultCount)
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_entries WHERE agent_id='g2-worker-a' AND kind='assistant' AND seq=(SELECT entry_seq FROM embedded_reply_outbox WHERE attempt_id=?)`, attemptA).Scan(&finalCount))
	require.Equal(t, 1, finalCount)
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_entries WHERE agent_id='g2-worker-a' AND kind='assistant' AND seq>(SELECT entry_seq FROM embedded_acks WHERE attempt_id=?)`, attemptA).Scan(&finalCount))
	require.Equal(t, 2, finalCount, "one tool call and one final after A's addressed user entry")
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_entries WHERE agent_id='g2-worker-b' AND kind='assistant' AND seq>(SELECT entry_seq FROM embedded_acks WHERE attempt_id=?)`, attemptB).Scan(&bFinalCount))
	require.Zero(t, bFinalCount, "B has no addressed-turn assistant")
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_reply_outbox`).Scan(&outboxCount))
	require.Equal(t, 1, outboxCount)
	pending, err := db.PendingEmbeddedReplies(ctx, "g2-worker-a")
	require.NoError(t, err)
	require.Len(t, pending, 1)
	frozen := pending[0]
	require.Equal(t, attemptA, frozen.AttemptID)
	require.Equal(t, aID, frozen.MessageID)
	require.Equal(t, "g2-worker-a", frozen.From)
	require.Equal(t, "external-a", frozen.To)
	require.Equal(t, "A wrote once", frozen.Body)
	require.NotEmpty(t, frozen.ReplyID)
	require.NotEmpty(t, frozen.CreatedAt)
	bPending, err := db.PendingEmbeddedReplies(ctx, "g2-worker-b")
	require.NoError(t, err)
	require.Empty(t, bPending)
	require.NotEqual(t, attemptA, attemptB)
	beforeA, err := db.ConversationHistory(ctx, "external-a", "g2-worker-a")
	require.NoError(t, err)
	require.Len(t, beforeA, 1, "marker must be before ordinary publish")
	beforeB, err := db.ConversationHistory(ctx, "external-b", "g2-worker-b")
	require.NoError(t, err)
	require.Len(t, beforeB, 1)
	for i := 0; i < 2; i++ {
		require.NoError(t, manager.RecoverEmbeddedReplies(ctx))
	}
	afterA, err := db.ConversationHistory(ctx, "external-a", "g2-worker-a")
	require.NoError(t, err)
	require.Len(t, afterA, 2)
	require.Equal(t, frozen.ReplyID, afterA[1].ID)
	require.Equal(t, frozen.CreatedAt, afterA[1].CreatedAt)
	require.Equal(t, frozen.Body, afterA[1].Body)
	require.Equal(t, frozen.From, afterA[1].From)
	require.Equal(t, frozen.To, afterA[1].To)
	afterB, err := db.ConversationHistory(ctx, "external-b", "g2-worker-b")
	require.NoError(t, err)
	require.Len(t, afterB, 1)
	for _, pair := range [][2]string{{"external-a", "g2-worker-b"}, {"external-b", "g2-worker-a"}} {
		h, err := db.ConversationHistory(ctx, pair[0], pair[1])
		require.NoError(t, err)
		require.Empty(t, h)
	}
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_reply_outbox WHERE attempt_id=? AND state='delivered'`, attemptA).Scan(&outboxCount))
	require.Equal(t, 1, outboxCount)
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM messages WHERE id=? AND sender=? AND recipient=? AND created_at=?`, frozen.ReplyID, frozen.From, frozen.To, frozen.CreatedAt).Scan(&outboxCount))
	require.Equal(t, 1, outboxCount, "one ordinary enqueue across both recoveries")
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_entries WHERE agent_id='g2-worker-a' AND kind='toolResult'`).Scan(&resultCount))
	require.Equal(t, 1, resultCount)
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_entries WHERE agent_id='g2-worker-b' AND kind='assistant' AND seq>(SELECT entry_seq FROM embedded_acks WHERE attempt_id=?)`, attemptB).Scan(&bFinalCount))
	require.Zero(t, bFinalCount)
	written, err = os.ReadFile(filepath.Join(root, "project-a", "effect.txt"))
	require.NoError(t, err)
	require.Equal(t, "A wrote once\n", string(written))
	// Cold-start the saved workers with the expired lease. An ACKed B user
	// entry with a proven entered provider must not be replayed. The only
	// permitted new provider call is one epoch-bound internal recovery notice.
	setTransportEmbeddedRecoveryGeneration(t, ctx, db, manager)
	require.NoError(t, manager.RecoverEmbeddedRoots(ctx))
	select {
	case history := <-bContexts:
		var addressed, notices int
		for _, message := range history.Messages {
			user, ok := message.(ai.UserMessage)
			if !ok {
				continue
			}
			text := fmt.Sprint(user.Content)
			if strings.Contains(text, "B addressed turn") {
				addressed++
			}
			if strings.Contains(strings.ToLower(text), "interrupted") {
				notices++
			}
		}
		require.Equal(t, 1, addressed, "committed B mail must remain in restored context")
		require.Equal(t, 1, notices, "one non-mail recovery notice must be in context")
	case <-time.After(3 * time.Second):
		t.Fatal("RED: entered B provider turn was not replayed, but no recovery-notice request was issued")
	}
	require.EqualValues(t, 1, bRequests.Load(), "exactly one new B provider request")
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_entries WHERE agent_id='g2-worker-b' AND session_id=(SELECT session_id FROM embedded_attempts WHERE attempt_id=?) AND epoch=(SELECT epoch FROM embedded_attempts WHERE attempt_id=?) AND kind='user'`, attemptB, attemptB).Scan(&resultCount))
	require.Equal(t, 3, resultCount, "initial instruction, addressed mail and one checked notice")
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_reply_outbox WHERE attempt_id=?`, attemptB).Scan(&outboxCount))
	require.Zero(t, outboxCount)
	require.Eventually(t, func() bool {
		return raw.QueryRow(`SELECT count(*) FROM embedded_entries WHERE agent_id='g2-worker-b' AND kind='assistant' AND seq>(SELECT entry_seq FROM embedded_acks WHERE attempt_id=?)`, attemptB).Scan(&bFinalCount) == nil && bFinalCount == 1
	}, 3*time.Second, 10*time.Millisecond, "only the new recovery turn may complete")
	afterB, err = db.ConversationHistory(ctx, "external-b", "g2-worker-b")
	require.NoError(t, err)
	require.Len(t, afterB, 1, "notice must not create a mail reply")
}

func runInterleavedG2Child(t *testing.T, root string) {
	f := newInterleavedG2Fixture(t)
	rows := f.launch(t)
	f.manager.SetEmbeddedReplyPublisher(func(ctx context.Context, reply store.EmbeddedReply) error {
		pending, err := f.db.PendingEmbeddedReplies(ctx, "g2-worker-a")
		if err != nil || len(pending) != 1 || pending[0] != reply {
			return fmt.Errorf("final/outbox not durable before publisher: %v, %v", pending, err)
		}
		file, err := os.OpenFile(filepath.Join(root, "outbox-before-publish"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, err = file.WriteString("outbox committed\n")
		if err == nil {
			err = file.Sync()
		}
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		select {} // parent SIGKILLs before Router.persistEmbeddedReply
	})
	const bID, aID = "c6222222-2222-4222-8222-222222222222", "c6333333-3333-4333-8333-333333333333"
	send := func(id, sender, recipient, body string) {
		t.Helper()
		status, raw := f.call(http.MethodPost, "/v1/messages", "", fmt.Sprintf(`{"id":%q,"from":%q,"to":%q,"body":%q,"created_at":"2026-09-27T00:00:00.000Z"}`, id, sender, recipient, body))
		require.Equal(t, http.StatusCreated, status, string(raw))
	}
	send(bID, "external-b", "g2-worker-b", "B addressed turn")
	select {
	case <-f.blockedB:
	case <-time.After(4 * time.Second):
		t.Fatal("B did not reach the pre-assistant provider barrier")
	}
	f.assertExactACK(t, bID, "external-b", "g2-worker-b", "B addressed turn", rows[1])
	send(aID, "external-a", "g2-worker-a", "A addressed turn")
	f.assertExactACK(t, aID, "external-a", "g2-worker-a", "A addressed turn", rows[0])
	select {
	case result := <-f.writeResult:
		require.NotNil(t, result)
		require.Equal(t, "g2-a-write-once", result.ToolCallID)
		require.Equal(t, "write", result.ToolName)
		require.False(t, result.IsError)
		require.NotEmpty(t, result.Content)
		require.Contains(t, result.Content[0].(ai.TextContent).Text, "Successfully wrote")
	case <-time.After(4 * time.Second):
		t.Fatal("A's SDK write continuation did not arrive")
	}
	written, err := os.ReadFile(filepath.Join(f.projects[0], "effect.txt"))
	require.NoError(t, err)
	require.Equal(t, "A wrote once\n", string(written))
	// Ensure B remains parked while A advances; neither worker borrowed the
	// other's project directory or sender-bound addressed mail.
	require.NoFileExists(t, filepath.Join(f.projects[1], "effect.txt"))
	history, err := f.db.ConversationHistory(context.Background(), "external-b", "g2-worker-a")
	require.NoError(t, err)
	require.Empty(t, history)
	history, err = f.db.ConversationHistory(context.Background(), "external-a", "g2-worker-b")
	require.NoError(t, err)
	require.Empty(t, history)
	// Keep the child alive until the parent observes the synced marker and kills it.
	select {}
}
