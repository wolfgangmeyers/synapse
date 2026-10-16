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

// One actual Listen endpoint, three independently persisted sessions, and no
// credential or process-wide fake switch. A cold Listen must use the same DB.
func TestEmbeddedCombinedRootWorkersMailColdListenRED(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	manifest := mustEffectManifest(t)
	names := []string{"combined-root", "combined-worker-a", "combined-worker-b"}
	senders := []string{"", "combined-sender-a", "combined-sender-b"}
	ids := []string{"", "e2111111-1111-4111-8111-111111111111", "e2222222-2222-4222-8222-222222222222"}
	var turns [3]atomic.Int32
	models := make([]*ai.Model, 3)
	for i := range models {
		index := i
		reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
		t.Cleanup(reg.Unregister)
		step := func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
			turns[index].Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "answer for " + names[index]}}, ai.StopStop)
		}
		responses := make([]providers.FauxResponseStep, 8)
		for j := range responses {
			responses[j] = step
		}
		reg.SetResponses(responses)
		models[i] = reg.GetModel()
	}
	open := func(bootstrap bool) (*store.Store, *managed.Manager, *Router) {
		t.Helper()
		db, err := store.Open(ctx, path, store.Options{})
		require.NoError(t, err)
		manager, err := managed.NewManager(managed.ManagerOptions{CodexCredentialStore: testfixture.CodexAdmission(t),
			Store: db, Manifest: manifest,
			Config:        managed.Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
			CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
			EmbeddedModelResolver: func(row store.ManagedAgentRecord) *ai.Model {
				for i, name := range names {
					if row.AgentID == name {
						return models[i]
					}
				}
				return nil
			},
		})
		require.NoError(t, err)
		router, err := New(Options{Store: db, Managed: manager, Host: "127.0.0.1", Port: 0, DefaultEmbeddedRoot: true,
			StartupManaged: func(start context.Context, m *managed.Manager) error {
				if !bootstrap {
					return nil
				}
				_, err := m.LaunchEmbeddedRoot(start, managed.LaunchRequest{Name: names[0], Cwd: root, Runtime: "embedded", RequestID: "e2000000-0000-4000-8000-000000000000", Model: &managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "explicit saved root instruction"})
				return err
			},
		})
		require.NoError(t, err)
		require.NoError(t, router.Listen(ctx))
		return db, manager, router
	}
	call := func(router *Router, method, endpoint, id, body string) (int, []byte) {
		t.Helper()
		url, _ := router.URLs()
		req, err := http.NewRequestWithContext(ctx, method, url+endpoint, strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		if id != "" {
			req.Header.Set("X-Request-ID", id)
		}
		client := &http.Client{Timeout: 3 * time.Second}
		response, err := client.Do(req)
		require.NoError(t, err)
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		return response.StatusCode, raw
	}
	closeAll := func(db *store.Store, manager *managed.Manager, router *Router) {
		t.Helper()
		require.NoError(t, router.Close(ctx))
		require.NoError(t, manager.Close(ctx))
		require.NoError(t, db.Close())
	}
	db, manager, router := open(true)
	defer func() {
		if router != nil {
			closeAll(db, manager, router)
		}
	}()
	check, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer check.Close()
	var initialProcess, coldProcess int64
	var initialIncarnation, coldIncarnation string
	require.NoError(t, check.QueryRowContext(ctx, `SELECT generation FROM embedded_process_generation_counter WHERE singleton=1`).Scan(&initialProcess))
	require.NoError(t, check.QueryRowContext(ctx, `SELECT incarnation_key FROM embedded_process_generations WHERE generation=?`, initialProcess).Scan(&initialIncarnation))
	require.Equal(t, router.embeddedProcessGeneration, initialProcess)
	require.Regexp(t, "^router-[0-9a-f]{64}$", initialIncarnation)
	require.Positive(t, initialProcess)
	defer func() {
		if !t.Failed() {
			return
		}
		t.Logf("cold-listen initial_process=%d initial_incarnation=%s cold_process=%d cold_incarnation=%s", initialProcess, initialIncarnation, coldProcess, coldIncarnation)
		for _, query := range []string{
			`SELECT json_group_array(json_object('agent',agent_id,'lifecycle',lifecycle_state,'error',last_error_code,'message',last_error_message)) FROM managed_agents`,
			`SELECT json_group_array(json_object('agent',agent_id,'session',session_id,'epoch',epoch,'runtime',generation,'owner',owner_id,'token',owner_token,'expires',owner_expires_at)) FROM embedded_sessions WHERE current=1`,
			`SELECT json_group_array(json_object('agent',agent_id,'kind',kind,'state',state,'process',process_generation,'token',claim_owner_token)) FROM embedded_restart_wake_slots`,
			`SELECT json_group_array(json_object('agent',agent_id,'session',session_id,'epoch',epoch,'runtime',generation,'seq',seq,'digest',digest,'parent',parent_digest,'kind',kind,'body',CAST(body AS TEXT))) FROM embedded_entries`,
			`SELECT json_group_array(json_object('agent',agent_id,'session',session_id,'epoch',epoch,'runtime',generation,'seq',user_seq,'digest',user_digest)) FROM embedded_nonmail_provider_entries`,
			`SELECT json_group_array(json_object('agent',agent_id,'instruction',pending_initial_instruction,'sent',initial_instruction_sent)) FROM managed_agents`,
			`SELECT json_group_array(json_object('agent',agent_id,'seq',entry_seq,'attempt',attempt_id)) FROM embedded_acks`,
			`SELECT json_group_array(json_object('agent',agent_id,'session',session_id,'epoch',epoch,'runtime',generation,'request',request_id,'state',state,'result_seq',result_seq,'prompt',prompt)) FROM embedded_prompt_requests`,
		} {
			var snapshot string
			queryErr := check.QueryRowContext(ctx, query).Scan(&snapshot)
			t.Logf("cold-listen query=%s result=%s query_error=%v", query, snapshot, queryErr)
		}
	}()
	type binding struct {
		session, generation, role string
		epoch, token              int64
	}
	saved := make([]binding, 3)
	for i := 1; i < 3; i++ {
		payload := fmt.Sprintf(`{"name":%q,"cwd":%q,"model":{"provider":"openai-codex","id":"gpt-5.6-terra"},"initial_instruction":%q}`, names[i], root, "instruction for "+names[i])
		status, raw := call(router, http.MethodPost, "/v1/managed-agents", ids[i], payload)
		require.Equal(t, http.StatusAccepted, status, string(raw))
	}
	for i, name := range names {
		require.Eventually(t, func() bool { row, e := db.ManagedAgent(ctx, name); return e == nil && row.LifecycleState == "running" }, 4*time.Second, 10*time.Millisecond, "ready: %s", name)
		row, err := db.ManagedAgent(ctx, name)
		require.NoError(t, err)
		require.Equal(t, "embedded", row.Runtime)
		require.NoError(t, check.QueryRowContext(ctx, `SELECT role,session_id,epoch,generation,owner_token FROM embedded_sessions WHERE agent_id=? AND current=1`, name).Scan(&saved[i].role, &saved[i].session, &saved[i].epoch, &saved[i].generation, &saved[i].token))
		expected := "worker"
		if i == 0 {
			expected = "root"
		}
		require.Equal(t, expected, saved[i].role)
		require.Equal(t, row.PiSessionID, saved[i].session)
		require.Positive(t, saved[i].token)
	}
	require.NotEqual(t, saved[1].session, saved[2].session)
	require.NotEqual(t, saved[1].generation, saved[2].generation)
	for i := 1; i < 3; i++ {
		body := "addressed mail for " + names[i]
		payload := fmt.Sprintf(`{"id":%q,"from":%q,"to":%q,"body":%q,"created_at":"2026-09-27T00:00:00.000Z"}`, ids[i], senders[i], names[i], body)
		status, raw := call(router, http.MethodPost, "/v1/messages", "", payload)
		require.Equal(t, http.StatusCreated, status, string(raw), "durable enqueue only")
		message, err := db.MessageFor(ctx, ids[i])
		require.NoError(t, err)
		require.Equal(t, ids[i], message.ID)
		require.Eventually(t, func() bool {
			var n int
			err := check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_attempts a JOIN embedded_acks k ON k.attempt_id=a.attempt_id AND k.agent_id=a.agent_id AND k.session_id=a.session_id AND k.epoch=a.epoch AND k.generation=a.generation JOIN embedded_entries e ON e.agent_id=k.agent_id AND e.session_id=k.session_id AND e.epoch=k.epoch AND e.seq=k.entry_seq WHERE a.message_id=? AND a.sender=? AND a.agent_id=? AND a.accepted_body=? AND a.session_id=? AND a.epoch=? AND a.generation=? AND e.kind='user' AND instr(e.body, a.attempt_id)>0`, ids[i], senders[i], names[i], body, saved[i].session, saved[i].epoch, saved[i].generation).Scan(&n)
			if err != nil || n != 1 {
				return false
			}
			message, e := db.MessageFor(ctx, ids[i])
			if e != nil || message.State != domain.MessageAcknowledged {
				return false
			}
			history, e := db.ConversationHistory(ctx, senders[i], names[i])
			return e == nil && len(history) == 2 && history[1].From == names[i] && history[1].To == senders[i] && history[1].Body == "answer for "+names[i]
		}, 4*time.Second, 10*time.Millisecond, "exact ACK and sender-bound completed reply for %s", names[i])
		status, raw = call(router, http.MethodPost, "/v1/messages", "", payload)
		require.Equal(t, http.StatusOK, status, string(raw))
		other := names[3-i]
		history, err := db.ConversationHistory(ctx, senders[i], other)
		require.NoError(t, err)
		require.Empty(t, history)
	}
	require.Eventually(t, func() bool { return turns[0].Load() == 1 && turns[1].Load() == 2 && turns[2].Load() == 2 }, 4*time.Second, 10*time.Millisecond)
	initialEntries := make([][]store.EmbeddedEntry, len(names))
	for i, name := range names {
		snapshot, err := db.ReopenEmbedded(ctx, store.EmbeddedBinding{AgentID: name, SessionID: saved[i].session, Epoch: saved[i].epoch, Generation: saved[i].generation, OwnerToken: saved[i].token})
		require.NoError(t, err)
		wantEntries := 5
		if i == 0 {
			wantEntries = 3
		}
		require.Len(t, snapshot.Entries, wantEntries)
		initialEntries[i] = snapshot.Entries
		require.EqualValues(t, 2, snapshot.Entries[1].Sequence)
		require.Equal(t, "user", snapshot.Entries[1].Kind)
		originalMessage, err := ai.UnmarshalMessage(snapshot.Entries[1].Body)
		require.NoError(t, err)
		originalUser, ok := originalMessage.(ai.UserMessage)
		require.True(t, ok)
		instruction := "instruction for " + name
		if i == 0 {
			instruction = "explicit saved root instruction"
		} else {
			instruction = fmt.Sprintf("You are the '%s' agent.\n\n%s", name, instruction)
		}
		require.Equal(t, ai.ContentList{ai.TextContent{Text: instruction}}, originalUser.Content)
		require.EqualValues(t, 3, snapshot.Entries[2].Sequence)
		require.Equal(t, snapshot.Entries[1].Digest, snapshot.Entries[2].ParentDigest)
		originalFinalMessage, err := ai.UnmarshalMessage(snapshot.Entries[2].Body)
		require.NoError(t, err)
		originalFinal, ok := originalFinalMessage.(ai.AssistantMessage)
		require.True(t, ok)
		require.Equal(t, ai.StopStop, originalFinal.StopReason)
		require.Equal(t, ai.ContentList{ai.TextContent{Text: "answer for " + name}}, originalFinal.Content)
	}
	closeAll(db, manager, router)
	router = nil
	_, err = check.ExecContext(ctx, `UPDATE embedded_sessions SET owner_expires_at='2000-01-01T00:00:00.000Z'`)
	require.NoError(t, err)
	db, manager, router = open(false)
	require.NoError(t, check.QueryRowContext(ctx, `SELECT generation FROM embedded_process_generation_counter WHERE singleton=1`).Scan(&coldProcess))
	require.NoError(t, check.QueryRowContext(ctx, `SELECT incarnation_key FROM embedded_process_generations WHERE generation=?`, coldProcess).Scan(&coldIncarnation))
	require.Equal(t, router.embeddedProcessGeneration, coldProcess)
	require.Regexp(t, "^router-[0-9a-f]{64}$", coldIncarnation)
	require.Greater(t, coldProcess, initialProcess, "router process generation is distinct from the session owner token")
	require.NotEqual(t, initialIncarnation, coldIncarnation)
	coldTokens := make([]int64, len(names))
	for i, name := range names {
		var takeoverErr error
		var observedToken int64
		takeoverReady := false
		require.Eventually(t, func() bool {
			takeoverErr = check.QueryRowContext(ctx, `SELECT s.owner_token,a.lifecycle_state='running' AND a.accepting_prompts=1 FROM embedded_sessions s JOIN managed_agents a ON a.agent_id=s.agent_id WHERE s.agent_id=? AND s.session_id=? AND s.epoch=? AND s.generation=? AND s.current=1 AND s.owner_id IS NOT NULL AND s.owner_expires_at>?`, name, saved[i].session, saved[i].epoch, saved[i].generation, time.Now().UTC().Format("2006-01-02T15:04:05.000Z")).Scan(&observedToken, &takeoverReady)
			return takeoverErr == nil && observedToken >= saved[i].token && takeoverReady
		}, 4*time.Second, 10*time.Millisecond, "persisted takeover and readiness: %s", name)
		row, err := db.ManagedAgent(ctx, name)
		require.NoError(t, err)
		require.Equal(t, saved[i].session, row.PiSessionID)
		var role, session, generation string
		var epoch, token int64
		require.NoError(t, check.QueryRowContext(ctx, `SELECT role,session_id,epoch,generation,owner_token FROM embedded_sessions WHERE agent_id=? AND current=1`, name).Scan(&role, &session, &epoch, &generation, &token))
		require.Equal(t, saved[i].role, role)
		require.Equal(t, saved[i].session, session)
		require.Equal(t, saved[i].epoch, epoch)
		require.Equal(t, saved[i].generation, generation)
		require.GreaterOrEqual(t, token, saved[i].token)
		coldTokens[i] = token
		if i > 0 {
			history, err := db.ConversationHistory(ctx, senders[i], name)
			require.NoError(t, err)
			require.Len(t, history, 2)
			var n int
			require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_attempts WHERE message_id=?`, ids[i]).Scan(&n))
			require.Equal(t, 1, n)
			require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_reply_outbox o JOIN embedded_attempts a ON a.attempt_id=o.attempt_id WHERE a.message_id=?`, ids[i]).Scan(&n))
			require.Equal(t, 1, n)
			status, raw := call(router, http.MethodGet, "/v1/conversations?owner="+senders[i]+"&peer="+name, "", "")
			require.Equal(t, http.StatusOK, status, string(raw))
			var projection struct {
				Messages []json.RawMessage `json:"messages"`
			}
			require.NoError(t, json.Unmarshal(raw, &projection))
			require.Len(t, projection.Messages, 2)
		}
	}
	// Readiness alone is not checked completion of the distinct cold awareness.
	var wakeMilestoneErr error
	var wakeMilestones int
	require.Eventually(t, func() bool {
		wakeMilestoneErr = check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_restart_wake_slots w JOIN embedded_restart_wake_checked_origins o ON o.agent_id=w.agent_id AND o.slot=w.slot AND o.session_id=w.session_id AND o.epoch=w.epoch AND o.runtime_generation=w.runtime_generation AND o.wake_id=w.wake_id AND o.process_generation=w.process_generation AND o.claim_owner_token=w.claim_owner_token JOIN embedded_entries f ON f.agent_id=o.agent_id AND f.session_id=o.session_id AND f.epoch=o.epoch AND f.generation=o.runtime_generation AND f.seq=o.seq JOIN embedded_entries u ON u.agent_id=f.agent_id AND u.session_id=f.session_id AND u.epoch=f.epoch AND u.generation=f.generation AND u.seq=4 AND u.digest=f.parent_digest JOIN embedded_sessions s ON s.agent_id=w.agent_id AND s.session_id=w.session_id AND s.epoch=w.epoch AND s.generation=w.runtime_generation AND s.current=1 AND s.owner_token=w.claim_owner_token AND s.role='root' WHERE w.agent_id=? AND w.session_id=? AND w.epoch=? AND w.runtime_generation=? AND u.parent_digest=w.digest AND w.slot='current' AND w.kind='root-idle' AND w.notice_id IS NULL AND w.state='checked-complete' AND w.process_generation=? AND w.claim_owner_token=? AND w.digest=? AND u.kind='user' AND f.seq=5 AND f.kind='assistant' AND json_extract(f.body,'$.stopReason')='stop'`, names[0], saved[0].session, saved[0].epoch, saved[0].generation, coldProcess, coldTokens[0], initialEntries[0][2].Digest).Scan(&wakeMilestones)
		if wakeMilestoneErr != nil {
			t.Logf("root awareness milestone count=%d query_error=%v", wakeMilestones, wakeMilestoneErr)
		}
		return wakeMilestoneErr == nil && wakeMilestones == 1
	}, 4*time.Second, 10*time.Millisecond, "exact current root awareness checked completion")
	for i, name := range names {
		b := store.EmbeddedBinding{AgentID: name, SessionID: saved[i].session, Epoch: saved[i].epoch, Generation: saved[i].generation, OwnerToken: coldTokens[i]}
		snapshot, err := db.ReopenEmbedded(ctx, b)
		require.NoError(t, err)
		require.Len(t, snapshot.Entries, 5, "no duplicate original or extra awareness entries")
		require.Equal(t, initialEntries[i], snapshot.Entries[:len(initialEntries[i])], "all original and mail bytes, digests and parent links remain immutable")
		providerSources := []store.EmbeddedEntry{snapshot.Entries[1]}
		if i == 0 {
			awareness := snapshot.Entries[3]
			final := snapshot.Entries[4]
			require.EqualValues(t, 4, awareness.Sequence)
			require.Equal(t, "user", awareness.Kind)
			require.Equal(t, initialEntries[i][2].Digest, awareness.ParentDigest)
			expected, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "The mailbox router was restarted. Your session was restored from saved context. Continue working from your saved context."}}})
			require.NoError(t, err)
			require.Equal(t, expected, awareness.Body, "distinct awareness is never a replay of the original accepted input")
			require.NotEqual(t, initialEntries[i][1].Digest, awareness.Digest)
			require.EqualValues(t, 5, final.Sequence)
			require.Equal(t, awareness.Digest, final.ParentDigest)
			message, err := ai.UnmarshalMessage(final.Body)
			require.NoError(t, err)
			checkedFinal, ok := message.(ai.AssistantMessage)
			require.True(t, ok)
			require.Equal(t, ai.StopStop, checkedFinal.StopReason)
			require.Equal(t, ai.ContentList{ai.TextContent{Text: "answer for " + name}}, checkedFinal.Content)
			providerSources = append(providerSources, awareness)
			wake, err := db.EmbeddedRestartWake(ctx, b, coldProcess, fmt.Sprintf("settled:%s:%d:%d", name, initialEntries[i][1].Sequence, coldProcess))
			require.NoError(t, err)
			require.Equal(t, "root-idle", wake.Kind)
			require.Equal(t, "checked-complete", wake.State)
			require.Equal(t, initialEntries[i][2].Digest, wake.Digest)
		}
		var markers, acks, wakes, origins int
		require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_nonmail_provider_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, name, b.SessionID, b.Epoch, b.Generation).Scan(&markers))
		require.Equal(t, len(providerSources), markers)
		for _, source := range providerSources {
			var exact int
			require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_nonmail_provider_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND user_seq=? AND user_digest=?`, name, b.SessionID, b.Epoch, b.Generation, source.Sequence, source.Digest).Scan(&exact))
			require.Equal(t, 1, exact, "only actual checked nonmail provider sources")
		}
		require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_acks WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, name, b.SessionID, b.Epoch, b.Generation).Scan(&acks))
		require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_restart_wake_slots WHERE agent_id=?`, name).Scan(&wakes))
		require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_restart_wake_checked_origins WHERE agent_id=?`, name).Scan(&origins))
		if i == 0 {
			require.Zero(t, acks, "neither original nor awareness is mail")
			require.Equal(t, 1, wakes)
			require.Equal(t, 1, origins)
			var attempts, replies int
			require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_attempts WHERE agent_id=?`, name).Scan(&attempts))
			require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_reply_outbox o JOIN embedded_attempts a ON a.attempt_id=o.attempt_id WHERE a.agent_id=?`, name).Scan(&replies))
			require.Zero(t, attempts)
			require.Zero(t, replies)
		} else {
			require.Equal(t, 1, acks)
			require.Zero(t, wakes, "latest ACKed mail cannot schedule original awareness")
			require.Zero(t, origins)
			var exactACK int
			require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_acks k JOIN embedded_attempts a ON a.attempt_id=k.attempt_id AND a.agent_id=k.agent_id AND a.session_id=k.session_id AND a.epoch=k.epoch AND a.generation=k.generation JOIN embedded_entries e ON e.agent_id=k.agent_id AND e.session_id=k.session_id AND e.epoch=k.epoch AND e.generation=k.generation AND e.seq=k.entry_seq WHERE k.agent_id=? AND k.session_id=? AND k.epoch=? AND k.generation=? AND k.entry_seq=4 AND e.digest=? AND a.message_id=? AND a.sender=?`, name, b.SessionID, b.Epoch, b.Generation, snapshot.Entries[3].Digest, ids[i], senders[i]).Scan(&exactACK))
			require.Equal(t, 1, exactACK)
		}
	}
	// Exactly one original request plus one distinct checked cold awareness.
	require.EqualValues(t, 1+1, turns[0].Load())
	require.EqualValues(t, 2, turns[1].Load())
	require.EqualValues(t, 2, turns[2].Load())
}
