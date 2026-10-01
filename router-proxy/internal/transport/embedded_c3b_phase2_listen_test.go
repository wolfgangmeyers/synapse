package transport

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// A claimed interrupted worker wake may wait inside its provider without
// holding either the sibling's serial lane or the router's ordinary mail lane.
func TestEmbeddedC3BPhase2InterruptedWorkerSiblingAndMailListen(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	manifest := mustEffectManifest(t)
	names := []string{"c3b2-root", "c3b2-worker-a", "c3b2-worker-b"}
	var calls [3]atomic.Int32
	var cold atomic.Bool
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	models := make([]*ai.Model, 3)
	for i := range models {
		index := i
		reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
		t.Cleanup(reg.Unregister)
		step := func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			calls[index].Add(1)
			if index == 1 && cold.Load() {
				select {
				case <-entered:
				default:
					close(entered)
				}
				<-release
			}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "answer from " + names[index]}}, ai.StopStop)
		}
		steps := make([]providers.FauxResponseStep, 8)
		for j := range steps {
			steps[j] = step
		}
		reg.SetResponses(steps)
		models[i] = reg.GetModel()
	}
	open := func(bootstrap bool) (*store.Store, *managed.Manager, *Router) {
		t.Helper()
		db, e := store.Open(ctx, path, store.Options{})
		require.NoError(t, e)
		m, e := managed.NewManager(managed.ManagerOptions{Store: db, Manifest: manifest, Config: managed.Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-sol", Available: true}), CodexCredentialStore: testfixture.CodexAdmission(t), EmbeddedModelResolver: func(row store.ManagedAgentRecord) *ai.Model {
			for i, name := range names {
				if row.AgentID == name {
					return models[i]
				}
			}
			return nil
		}})
		require.NoError(t, e)
		r, e := New(Options{Store: db, Managed: m, Host: "127.0.0.1", Port: 0, DefaultEmbeddedRoot: true, StartupManaged: func(start context.Context, m *managed.Manager) error {
			if !bootstrap {
				return nil
			}
			_, err := m.LaunchEmbeddedRoot(start, managed.LaunchRequest{Name: names[0], Cwd: root, Runtime: "embedded", RequestID: "c3b20000-0000-4000-8000-000000000001", Model: &managed.Model{Provider: "openai-codex", ID: "gpt-5.6-sol"}, InitialInstruction: "root initial"})
			return err
		}})
		require.NoError(t, e)
		require.NoError(t, r.Listen(ctx))
		return db, m, r
	}
	closeAll := func(db *store.Store, m *managed.Manager, r *Router) {
		t.Helper()
		require.NoError(t, r.Close(ctx))
		require.NoError(t, m.Close(ctx))
		require.NoError(t, db.Close())
	}
	call := func(r *Router, route, id, body string) (int, []byte) {
		t.Helper()
		url, _ := r.URLs()
		req, e := http.NewRequest(http.MethodPost, url+route, strings.NewReader(body))
		require.NoError(t, e)
		req.Header.Set("Content-Type", "application/json")
		if id != "" {
			req.Header.Set("X-Request-ID", id)
		}
		resp, e := (&http.Client{Timeout: 2 * time.Second}).Do(req)
		require.NoError(t, e)
		defer resp.Body.Close()
		raw, e := io.ReadAll(resp.Body)
		require.NoError(t, e)
		return resp.StatusCode, raw
	}
	db, m, r := open(true)
	defer func() {
		if r != nil {
			releaseOnce.Do(func() { close(release) })
			closeAll(db, m, r)
		}
	}()
	for i := 1; i < 3; i++ {
		payload := fmt.Sprintf(`{"name":%q,"cwd":%q,"model":{"provider":%q,"id":%q},"initial_instruction":%q}`, names[i], root, "openai-codex", "gpt-5.6-sol", "initial for "+names[i])
		code, raw := call(r, "/v1/managed-agents", fmt.Sprintf("c3b20000-0000-4000-8000-%012d", i+1), payload)
		require.Equal(t, http.StatusAccepted, code, string(raw))
	}
	require.Eventually(t, func() bool { return calls[0].Load() == 1 && calls[1].Load() == 1 && calls[2].Load() == 1 }, 4*time.Second, 10*time.Millisecond, "fixture: all initial turns must settle")
	check, e := sql.Open("sqlite", path)
	require.NoError(t, e)
	defer check.Close()
	bindings := make([]store.EmbeddedBinding, 3)
	for i, name := range names {
		var lastState, lastError string
		require.Eventually(t, func() bool {
			row, e := db.ManagedAgent(ctx, name)
			if e != nil {
				lastError = e.Error()
				return false
			}
			lastState, lastError = row.LifecycleState, row.LastErrorMessage
			return row.LifecycleState == "running"
		}, 4*time.Second, 10*time.Millisecond, "fixture: %s did not become running (state=%q error=%q)", name, lastState, lastError)
		row, e := db.ManagedAgent(ctx, name)
		require.NoError(t, e)
		require.Equal(t, "running", row.LifecycleState)
		var role string
		bindings[i] = store.EmbeddedBinding{AgentID: name, SessionID: row.PiSessionID, Epoch: row.SessionEpoch, Generation: row.RuntimeGeneration}
		require.NoError(t, check.QueryRow(`SELECT role,owner_token FROM embedded_sessions WHERE agent_id=? AND current=1`, name).Scan(&role, &bindings[i].OwnerToken))
		if i == 0 {
			require.Equal(t, "root", role)
		} else {
			require.Equal(t, "worker", role)
		}
		require.NotEmpty(t, bindings[i].SessionID)
	}
	require.NotEqual(t, bindings[1].SessionID, bindings[2].SessionID)
	// Simulate a persisted, provider-entered original whose final never arrived.
	body, e := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "unfinished original A"}}})
	require.NoError(t, e)
	original, e := db.AppendEmbeddedEntry(ctx, bindings[1], "user", body)
	require.NoError(t, e)
	require.NoError(t, db.EnterEmbeddedNonMailProviderTurn(ctx, bindings[1], original.Sequence, original.Digest))
	closeAll(db, m, r)
	r = nil
	_, e = check.Exec(`UPDATE embedded_sessions SET owner_expires_at='2000-01-01T00:00:00.000Z'`)
	require.NoError(t, e)
	cold.Store(true)
	db, m, r = open(false)
	select {
	case <-entered:
	case <-time.After(4 * time.Second):
		t.Fatal("interrupted A never entered fake provider")
	}
	require.Eventually(t, func() bool { return calls[0].Load() == 2 && calls[2].Load() == 2 }, 4*time.Second, 10*time.Millisecond, "idle root and B must finish while A is blocked")
	var noticeID string
	require.NoError(t, check.QueryRow(`SELECT notice_id FROM embedded_interrupted_notices WHERE agent_id=? AND source_seq=?`, names[1], original.Sequence).Scan(&noticeID))
	require.NotEmpty(t, noticeID)
	var claimed, started, notices, acks int
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM embedded_restart_wake_slots WHERE agent_id=? AND process_generation=? AND state='claimed' AND kind='interrupted'`, names[1], r.embeddedProcessGeneration).Scan(&claimed))
	require.Equal(t, 1, claimed, "claim must precede provider")
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM embedded_interrupted_notices WHERE notice_id=? AND state='started'`, noticeID).Scan(&started))
	require.Equal(t, 1, started)
	require.Error(t, db.ClaimEmbeddedRestartWake(ctx, bindings[1], r.embeddedProcessGeneration, noticeID), "old owner may not claim again")
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM embedded_acks`).Scan(&acks))
	require.Zero(t, acks, "awareness is not mail")
	// Ordinary addressed mail to the idle sibling must complete before A unblocks.
	mailID := "c3b29999-0000-4000-8000-000000000001"
	sender := "c3b2-sender"
	text := "independent ordinary mail"
	payload := fmt.Sprintf(`{"id":%q,"from":%q,"to":%q,"body":%q,"created_at":"2026-09-27T00:00:00.000Z"}`, mailID, sender, names[2], text)
	status, raw := call(r, "/v1/messages", "", payload)
	require.Equal(t, http.StatusCreated, status, string(raw))
	require.Eventually(t, func() bool {
		message, e := db.MessageFor(ctx, mailID)
		if e != nil || message.State != domain.MessageAcknowledged {
			return false
		}
		history, e := db.ConversationHistory(ctx, sender, names[2])
		return e == nil && len(history) == 2 && history[1].From == names[2] && history[1].To == sender && history[1].Body == "answer from "+names[2]
	}, 4*time.Second, 10*time.Millisecond, "mail processing ACK and sender-bound reply while A blocks")
	var attempts, mailAcks int
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM embedded_attempts WHERE message_id=? AND sender=? AND agent_id=? AND accepted_body=?`, mailID, sender, names[2], text).Scan(&attempts))
	require.Equal(t, 1, attempts, "acceptance is distinct from processing ACK")
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM embedded_attempts a JOIN embedded_acks k ON k.attempt_id=a.attempt_id AND k.agent_id=a.agent_id AND k.session_id=a.session_id AND k.epoch=a.epoch AND k.generation=a.generation JOIN embedded_entries u ON u.agent_id=k.agent_id AND u.session_id=k.session_id AND u.epoch=k.epoch AND u.seq=k.entry_seq WHERE a.message_id=? AND u.kind='user' AND instr(u.body,a.attempt_id)>0`, mailID).Scan(&mailAcks))
	require.Equal(t, 1, mailAcks)
	require.EqualValues(t, 2, calls[1].Load(), "A's single interrupted wake cannot be replayed")
	releaseOnce.Do(func() { close(release) })
	require.Eventually(t, func() bool {
		var complete, origins int
		e := check.QueryRow(`SELECT count(*) FROM embedded_restart_wake_slots WHERE agent_id=? AND process_generation=? AND state='checked-complete'`, names[1], r.embeddedProcessGeneration).Scan(&complete)
		if e != nil {
			return false
		}
		e = check.QueryRow(`SELECT count(*) FROM embedded_restart_wake_checked_origins WHERE agent_id=? AND process_generation=?`, names[1], r.embeddedProcessGeneration).Scan(&origins)
		return e == nil && complete == 1 && origins == 1
	}, 4*time.Second, 10*time.Millisecond)
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM embedded_interrupted_notices WHERE agent_id=?`, names[1]).Scan(&notices))
	require.Equal(t, 1, notices)
	require.EqualValues(t, 2, calls[1].Load())
	require.Error(t, r.Listen(ctx))
	require.EqualValues(t, 2, calls[1].Load())
}
