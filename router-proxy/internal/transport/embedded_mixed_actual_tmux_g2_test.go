package transport

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
	"net/http"
)

// This is deliberately not the sidecar CLI: its default injector uses REST and its
// window monitor addresses the shared tmux server. The real sidecar/client/injector
// are imported by a private Node runner, with every tmux command bound to -S.
const actualTmuxSidecar = `
import {execFileSync} from 'node:child_process';
import {appendFileSync} from 'node:fs';
import {pathToFileURL} from 'node:url';
const {createClaudeSidecar, createTmuxInjector} = await import(pathToFileURL(process.env.SIDECAR).href);
const {createMailboxClient} = await import(pathToFileURL(process.env.MAILBOX_CLIENT).href);
const emit = (event) => appendFileSync(process.env.EVENTS, JSON.stringify(event)+'\n', {mode:0o600});
const tmux = (args) => execFileSync('tmux', ['-S',process.env.TMUX_SOCKET,...args], {encoding:'utf8',timeout:2000,stdio:['ignore','pipe','pipe']});
let sidecar;
const client = createMailboxClient({agentId:process.env.LEGACY_ID,routerUrl:process.env.ROUTER_URL,
  webSocketFactory:(url)=>{const ws=new WebSocket(url);const send=ws.send.bind(ws);
    // Test-local negotiation adapter: the stock client does not request correlated_v1.
    ws.send=(data)=>{const f=JSON.parse(data);if(f.type==='register')f.ack_confirmation='correlated_v1';send(JSON.stringify(f));};
    ws.addEventListener('message',({data})=>{const f=JSON.parse(data);if(f.type==='delivery_acknowledged')emit({type:'confirmed',id:f.message_id,attempt:f.attempt_number,incarnation:f.message_incarnation_id});});return ws;},
  onEnvelope: async (message,delivery) => {
    emit({type:'envelope',id:message.id,attempt:delivery.attemptNumber,incarnation:delivery.items[0].messageIncarnationId});
    await sidecar.onEnvelope(message,delivery);
    emit({type:'injected',id:message.id,attempt:delivery.attemptNumber});
  }, log:()=>{}});
sidecar = createClaudeSidecar({agentId:process.env.LEGACY_ID,client,
  inject:createTmuxInjector({tmux,session:process.env.TMUX_SESSION,enterDelayMs:250})});
process.on('SIGTERM',()=>{client.close();process.exit(0)});
await client.connect();
emit({type:'connected'});
await new Promise(()=>{});
`

func TestEmbeddedMixedActualTmuxSidecarG2(t *testing.T) {
	ctx := context.Background()
	root, err := os.MkdirTemp(os.TempDir(), "g2tmux-")
	require.NoError(t, err)
	require.NoError(t, os.Chmod(root, 0700))
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	// A short socket path also stays below the Unix sockaddr limit.
	socket := filepath.Join(root, "socket")
	const legacyID, workerID, otherID = "g2-legacy-pane", "g2-worker", "g2-other"
	const session = "g2-private"
	paneFile := filepath.Join(root, "pane-bytes")
	python := "import os,tty\ntty.setraw(0)\nf=open(" + strconv.Quote(paneFile) + ",\"ab\",buffering=0)\nwhile True:\n b=os.read(0,4096)\n if not b: break\n f.write(b)\n"
	tmux := func(args ...string) ([]byte, error) {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		return exec.CommandContext(cctx, "tmux", append([]string{"-S", socket}, args...)...).CombinedOutput()
	}
	// tmux starts one real server and one raw-input pane; never touches default TMUX.
	output, err := tmux("new-session", "-d", "-s", session, "-n", legacyID, "python3 -u -c '"+python+"'")
	require.NoError(t, err, "private tmux setup: %s", output)
	t.Cleanup(func() { _, _ = tmux("kill-server") })
	require.Eventually(t, func() bool { _, e := os.Stat(paneFile); return e == nil }, 3*time.Second, 10*time.Millisecond, "pane setup failed")
	dbPath := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	var targetTurns, otherTurns atomic.Int32
	models := map[string]*ai.Model{}
	for _, name := range []string{workerID, otherID} {
		name := name
		reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
		t.Cleanup(reg.Unregister)
		step := func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
			if name == workerID {
				targetTurns.Add(1)
			} else {
				otherTurns.Add(1)
			}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "reply from " + name}}, ai.StopStop)
		}
		steps := make([]providers.FauxResponseStep, 8)
		for i := range steps {
			steps[i] = step
		}
		reg.SetResponses(steps)
		models[name] = reg.GetModel()
	}
	manager, err := managed.NewManager(managed.ManagerOptions{Store: db, Manifest: mustEffectManifest(t), Config: managed.Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), EmbeddedModelResolver: func(row store.ManagedAgentRecord) *ai.Model { return models[row.AgentID] }})
	require.NoError(t, err)
	router, err := New(Options{Store: db, Managed: manager, Host: "127.0.0.1", Port: 0, DefaultEmbeddedRoot: true})
	require.NoError(t, err)
	require.NoError(t, router.Listen(ctx))
	t.Cleanup(func() { _ = router.Close(ctx); _ = manager.Close(ctx); _ = db.Close() })
	base, _ := router.URLs()
	events := filepath.Join(root, "sidecar-events")
	nodeCtx, cancel := context.WithTimeout(ctx, 18*time.Second)
	t.Cleanup(cancel)
	child := exec.CommandContext(nodeCtx, "node", "--input-type=module", "-e", actualTmuxSidecar)
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	child.Env = append(os.Environ(), "SIDECAR="+filepath.Join(repoRoot, "tools/tmux-worker/claude_sidecar.mjs"), "MAILBOX_CLIENT="+filepath.Join(repoRoot, "tools/tmux-worker/mailbox-client.js"), "TMUX_SOCKET="+socket, "TMUX_SESSION="+session, "LEGACY_ID="+legacyID, "ROUTER_URL="+base, "EVENTS="+events, "HOME="+root)
	// No body-bearing child stderr can reach test output or a shared log.
	child.Stdout = io.Discard
	child.Stderr = io.Discard
	require.NoError(t, child.Start(), "node setup failed")
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	eventLines := func() []map[string]any {
		data, e := os.ReadFile(events)
		if e != nil {
			return nil
		}
		var result []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var v map[string]any
			if json.Unmarshal([]byte(line), &v) == nil {
				result = append(result, v)
			}
		}
		return result
	}
	hasEvent := func(kind, id string) bool {
		for _, v := range eventLines() {
			if v["type"] == kind && (id == "" || v["id"] == id) {
				return true
			}
		}
		return false
	}
	require.Eventually(t, func() bool { return hasEvent("connected", "") }, 4*time.Second, 10*time.Millisecond, "sidecar setup: no private connection")
	for i, name := range []string{workerID, otherID} {
		body := fmt.Sprintf(`{"name":%q,"cwd":%q,"model":{"provider":"openai-codex","id":"gpt-5.6-terra"},"initial_instruction":%q}`, name, root, "initial "+name)
		req, e := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/managed-agents", strings.NewReader(body))
		require.NoError(t, e)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Request-ID", fmt.Sprintf("e%07d-1111-4111-8111-111111111111", i+1))
		resp, e := (&http.Client{Timeout: 3 * time.Second}).Do(req)
		require.NoError(t, e)
		raw, e := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.NoError(t, e)
		require.Equal(t, http.StatusAccepted, resp.StatusCode, string(raw))
	}
	var worker *store.ManagedAgentRecord
	require.Eventually(t, func() bool {
		row, e := db.ManagedAgent(ctx, workerID)
		if e != nil || row.LifecycleState != "running" {
			return false
		}
		worker = row
		return true
	}, 4*time.Second, 10*time.Millisecond, "worker launch setup")
	role, err := db.EmbeddedSessionRole(ctx, store.EmbeddedBinding{AgentID: workerID, SessionID: worker.PiSessionID, Epoch: worker.SessionEpoch, Generation: worker.RuntimeGeneration})
	require.NoError(t, err)
	require.Equal(t, "worker", role)
	require.Eventually(t, func() bool { return targetTurns.Load() == 1 && otherTurns.Load() == 1 }, 4*time.Second, 10*time.Millisecond, "provider setup")
	check, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer check.Close()
	const toLegacyID = "e4000000-1111-4111-8111-111111111111"
	status, raw := mixedPostMessage(t, base, toLegacyID, workerID, legacyID, "private pane sentinel", nil)
	require.Equal(t, http.StatusCreated, status, string(raw))
	var firstLegacy map[string]any
	require.NoError(t, json.Unmarshal(raw, &firstLegacy))
	var incarnation string
	var attemptNumber int
	var state string
	require.Eventually(t, func() bool {
		return check.QueryRowContext(ctx, `SELECT m.message_incarnation_id,a.attempt_number,a.state FROM messages m JOIN delivery_attempts a ON a.message_id=m.id WHERE m.id=?`, toLegacyID).Scan(&incarnation, &attemptNumber, &state) == nil && state == "injection_requested"
	}, 4*time.Second, 10*time.Millisecond, "sidecar injection_requested before pane Enter")
	// Duplicate while the original is live: no second delivery/Enter.
	status, raw = mixedPostMessage(t, base, toLegacyID, workerID, legacyID, "private pane sentinel", nil)
	require.Equal(t, http.StatusOK, status, string(raw))
	require.Eventually(t, func() bool { return hasEvent("injected", toLegacyID) && hasEvent("confirmed", toLegacyID) }, 4*time.Second, 10*time.Millisecond, "sidecar pane and correlated ACK confirmation")
	require.NotEmpty(t, incarnation)
	require.Equal(t, 1, attemptNumber)
	var envelope map[string]any
	for _, v := range eventLines() {
		if v["type"] == "envelope" && v["id"] == toLegacyID {
			envelope = v
		}
	}
	require.NotNil(t, envelope)
	require.Equal(t, incarnation, envelope["incarnation"])
	require.EqualValues(t, attemptNumber, envelope["attempt"])
	pane, err := os.ReadFile(paneFile)
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(pane), "private pane sentinel"), "real pane bytes, not a fake WS receipt")
	require.Contains(t, string(pane), "Mailbox message from "+workerID)
	var confirmation map[string]any
	for _, v := range eventLines() {
		if v["type"] == "confirmed" && v["id"] == toLegacyID {
			confirmation = v
		}
	}
	require.NotNil(t, confirmation)
	require.Equal(t, incarnation, confirmation["incarnation"])
	require.EqualValues(t, attemptNumber, confirmation["attempt"])
	// ACK removes the live row and its attempts; conversation history is durable.
	historyToLegacy, err := db.ConversationHistory(ctx, workerID, legacyID)
	require.NoError(t, err)
	require.NotEmpty(t, historyToLegacy)
	var found bool
	for _, entry := range historyToLegacy {
		if entry.ID == toLegacyID {
			found = true
		}
	}
	require.True(t, found)
	// Duplicate live post must not produce another envelope or pane injection.
	var count int
	for _, v := range eventLines() {
		if v["type"] == "envelope" && v["id"] == toLegacyID {
			count++
		}
	}
	require.Equal(t, 1, count)
	pane, err = os.ReadFile(paneFile)
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(pane), "private pane sentinel"))
	// The correlated sidecar ACK removes the delivery row. Its immutable
	// conversation receipt must still make the same send idempotent.
	require.Eventually(t, func() bool {
		var n int
		return check.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE id=?`, toLegacyID).Scan(&n) == nil && n == 0
	}, 4*time.Second, 10*time.Millisecond)
	status, raw = mixedPostMessage(t, base, toLegacyID, workerID, legacyID, "private pane sentinel", nil)
	require.Equal(t, http.StatusOK, status, string(raw))
	var retryLegacy map[string]any
	require.NoError(t, json.Unmarshal(raw, &retryLegacy))
	require.Equal(t, firstLegacy["message"], retryLegacy["message"])
	status, raw = mixedPostMessage(t, base, toLegacyID, workerID, legacyID, "changed pane text", nil)
	require.Equal(t, http.StatusConflict, status, string(raw))
	require.Contains(t, string(raw), "idempotency_conflict")
	var legacyMessages, legacyAttempts, legacyHistory int
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE id=?`, toLegacyID).Scan(&legacyMessages))
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM delivery_attempts WHERE message_id=?`, toLegacyID).Scan(&legacyAttempts))
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM conversation_messages WHERE message_id=?`, toLegacyID).Scan(&legacyHistory))
	require.Zero(t, legacyMessages)
	require.Zero(t, legacyAttempts)
	require.Equal(t, 1, legacyHistory)
	pane, err = os.ReadFile(paneFile)
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(pane), "private pane sentinel"))
	count = 0
	for _, v := range eventLines() {
		if v["type"] == "envelope" && v["id"] == toLegacyID {
			count++
		}
	}
	require.Equal(t, 1, count)

	const toWorkerID = "e5000000-1111-4111-8111-111111111111"
	status, raw = mixedPostMessage(t, base, toWorkerID, legacyID, workerID, "legacy sender addressed worker", nil)
	require.Equal(t, http.StatusCreated, status, string(raw))
	var firstWorker map[string]any
	require.NoError(t, json.Unmarshal(raw, &firstWorker))
	var attempt, sender, agentID, sessionID, generation, accepted, entryKind, entryBody string
	var epoch int64
	require.Eventually(t, func() bool {
		return check.QueryRowContext(ctx, `SELECT a.attempt_id,a.sender,a.agent_id,a.session_id,a.epoch,a.generation,a.accepted_body,e.kind,e.body FROM embedded_attempts a JOIN embedded_acks k ON k.attempt_id=a.attempt_id AND k.message_id=a.message_id AND k.agent_id=a.agent_id AND k.session_id=a.session_id AND k.epoch=a.epoch AND k.generation=a.generation JOIN embedded_entries e ON e.agent_id=k.agent_id AND e.session_id=k.session_id AND e.epoch=k.epoch AND e.seq=k.entry_seq WHERE a.message_id=?`, toWorkerID).Scan(&attempt, &sender, &agentID, &sessionID, &epoch, &generation, &accepted, &entryKind, &entryBody) == nil
	}, 4*time.Second, 10*time.Millisecond, "worker SDK ACK")
	require.NotEmpty(t, attempt)
	require.Equal(t, legacyID, sender)
	require.Equal(t, workerID, agentID)
	require.Equal(t, worker.PiSessionID, sessionID)
	require.Equal(t, worker.SessionEpoch, epoch)
	require.Equal(t, worker.RuntimeGeneration, generation)
	require.Equal(t, "legacy sender addressed worker", accepted)
	require.Equal(t, "user", entryKind)
	require.Contains(t, entryBody, attempt)
	require.Contains(t, entryBody, accepted)
	var replyID, replySender, recipient, replyBody, outboxState string
	require.Eventually(t, func() bool {
		return check.QueryRowContext(ctx, `SELECT reply_id,sender,recipient,body,state FROM embedded_reply_outbox WHERE attempt_id=?`, attempt).Scan(&replyID, &replySender, &recipient, &replyBody, &outboxState) == nil && outboxState == "delivered"
	}, 4*time.Second, 10*time.Millisecond, "frozen reply outbox")
	require.Equal(t, workerID, replySender)
	require.Equal(t, legacyID, recipient)
	require.Equal(t, "reply from "+workerID, replyBody)
	require.Eventually(t, func() bool { return hasEvent("injected", replyID) }, 4*time.Second, 10*time.Millisecond, "worker reply into real pane")
	require.Eventually(t, func() bool { return hasEvent("confirmed", replyID) }, 4*time.Second, 10*time.Millisecond, "reply tmux ACK confirmation")
	var replyEnvelope, replyConfirmation map[string]any
	for _, v := range eventLines() {
		if v["id"] != replyID {
			continue
		}
		if v["type"] == "envelope" {
			replyEnvelope = v
		}
		if v["type"] == "confirmed" {
			replyConfirmation = v
		}
	}
	require.NotNil(t, replyEnvelope)
	require.NotNil(t, replyConfirmation)
	require.NotEmpty(t, replyEnvelope["incarnation"])
	require.Equal(t, replyEnvelope["incarnation"], replyConfirmation["incarnation"])
	require.Equal(t, replyEnvelope["attempt"], replyConfirmation["attempt"])
	var foreign int
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_acks WHERE message_id=? AND (agent_id<>? OR session_id<>?)`, toWorkerID, workerID, worker.PiSessionID).Scan(&foreign))
	require.Zero(t, foreign)
	pane, err = os.ReadFile(paneFile)
	require.NoError(t, err)
	require.Contains(t, string(pane), "reply from "+workerID)
	history, err := db.ConversationHistory(ctx, legacyID, otherID)
	require.NoError(t, err)
	require.Empty(t, history)
	require.EqualValues(t, 2, targetTurns.Load())
	require.EqualValues(t, 1, otherTurns.Load())
	require.Eventually(t, func() bool {
		var n int
		return check.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE id=? AND state='acknowledged'`, toWorkerID).Scan(&n) == nil && n == 1
	}, 4*time.Second, 10*time.Millisecond)
	status, raw = mixedPostMessage(t, base, toWorkerID, legacyID, workerID, "legacy sender addressed worker", nil)
	require.Equal(t, http.StatusOK, status, string(raw))
	var retryWorker map[string]any
	require.NoError(t, json.Unmarshal(raw, &retryWorker))
	require.Equal(t, firstWorker["message"], retryWorker["message"])
	status, raw = mixedPostMessage(t, base, toWorkerID, legacyID, otherID, "legacy sender addressed worker", nil)
	require.Equal(t, http.StatusConflict, status, string(raw))
	require.Contains(t, string(raw), "idempotency_conflict")
	var workerMessages, workerHistory, workerAttempts, workerAcks, workerOutbox, workerEntries, foreignEntries int
	for _, row := range []struct {
		query string
		count *int
	}{
		{`SELECT count(*) FROM messages WHERE id=?`, &workerMessages},
		{`SELECT count(*) FROM conversation_messages WHERE message_id=?`, &workerHistory},
		{`SELECT count(*) FROM embedded_attempts WHERE message_id=?`, &workerAttempts},
		{`SELECT count(*) FROM embedded_acks WHERE message_id=?`, &workerAcks},
		{`SELECT count(*) FROM embedded_reply_outbox WHERE attempt_id=(SELECT attempt_id FROM embedded_attempts WHERE message_id=?)`, &workerOutbox},
		{`SELECT count(*) FROM embedded_entries WHERE kind='user' AND instr(CAST(body AS TEXT), ?) > 0`, &workerEntries},
	} {
		require.NoError(t, check.QueryRowContext(ctx, row.query, toWorkerID).Scan(row.count))
	}
	require.Equal(t, 1, workerMessages)
	require.Equal(t, 1, workerHistory)
	require.Equal(t, 1, workerAttempts)
	require.Equal(t, 1, workerAcks)
	require.Equal(t, 1, workerOutbox)
	require.Equal(t, 1, workerEntries)
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id=? AND kind='user'`, otherID).Scan(&foreignEntries))
	require.Equal(t, 1, foreignEntries, "other worker has only its initial instruction")
	require.EqualValues(t, 2, targetTurns.Load())
	require.EqualValues(t, 1, otherTurns.Load())
}
