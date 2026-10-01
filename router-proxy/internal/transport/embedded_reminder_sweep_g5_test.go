package transport

import (
	"context"
	"database/sql"
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

// The independent worker creates its own reminder through the fixed SDK tool.
// Only the real Router sweep may enqueue it; only the Manager SDK lane may ACK it.
func TestEmbeddedIndependentWorkerReminderRealRouterSweep(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	start := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
	clock := deterministic.NewFixedClock(start)
	due := start.Add(1 * time.Second)
	dueText := due.Format("2006-01-02T15:04:05.000Z")
	body := "check my own work"
	db, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	const tool = "mecha_reminder"
	const callID = "sweep-create-call"
	create := map[string]any{"action": "create", "body": body, "due_at": dueText}
	type observation struct {
		tools  []ai.Tool
		result *ai.ToolResultMessage
	}
	seen := make(chan observation, 4)
	var providerCalls atomic.Int32
	resultFor := func(h ai.TranscriptContext, id string) (out observation) {
		for _, msg := range h.Messages {
			if result, ok := msg.(ai.ToolResultMessage); ok && result.ToolCallID == id {
				copy := result
				out.result = &copy
			}
		}
		return
	}
	reg.SetResponses([]providers.FauxResponseStep{
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			providerCalls.Add(1)
			var first observation
			for _, msg := range h.Messages {
				if system, ok := msg.(ai.SystemMessage); ok {
					first.tools = append(first.tools, system.ToolsAdded...)
				}
			}
			seen <- first
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall(tool, create, callID)}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			providerCalls.Add(1)
			seen <- resultFor(h, callID)
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall(tool, map[string]any{"action": "list"}, "sweep-list-call")}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			providerCalls.Add(1)
			seen <- resultFor(h, "sweep-list-call")
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "ready"}}, ai.StopStop)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			providerCalls.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "received"}}, ai.StopStop)
		},
	})
	newManager := func(s *store.Store) *managed.Manager {
		m, e := managed.NewManager(managed.ManagerOptions{Store: s, Manifest: manifest, CodexCredentialStore: testfixture.CodexAdmission(t),
			Config:                managed.Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
			CatalogSource:         managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
			EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		})
		require.NoError(t, e)
		return m
	}
	m := newManager(db)
	r, err := New(Options{Store: db, Managed: m, Clock: clock})
	require.NoError(t, err)
	defer func() { _ = r.Close(ctx); _ = m.Close(ctx); _ = db.Close() }()
	_, err = m.LaunchEmbeddedRoot(ctx, managed.LaunchRequest{Name: "sweep-root", Cwd: root, Runtime: "embedded", RequestID: "b1111111-1111-4111-8111-111111111111"})
	require.NoError(t, err)
	launched, err := m.Launch(ctx, managed.LaunchRequest{Name: "sweep-worker", Cwd: root, Runtime: "embedded", InitialInstruction: "set a reminder for yourself", RequestID: "b2222222-2222-4222-8222-222222222222"})
	require.NoError(t, err)
	binding := store.EmbeddedBinding{AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
	role, err := db.EmbeddedSessionRole(ctx, binding)
	require.NoError(t, err)
	require.Equal(t, "worker", role)
	wait := func() observation {
		t.Helper()
		select {
		case item := <-seen:
			return item
		case <-time.After(4 * time.Second):
			t.Fatal("SDK provider did not reach reminder turn")
			return observation{}
		}
	}
	first := wait()
	var fixed *ai.Tool
	for i := range first.tools {
		if first.tools[i].Name == tool {
			fixed = &first.tools[i]
		}
	}
	require.NotNil(t, fixed, "real SDK did not register fixed reminder tool")
	require.True(t, fixed.Parameters.Check(create))
	for _, forbidden := range []string{"target", "creator", "owner", "recipient"} {
		require.False(t, fixed.Parameters.Check(map[string]any{"action": "create", "body": body, "due_at": dueText, forbidden: "sweep-root"}))
	}
	created := wait()
	require.NotNil(t, created.result)
	require.False(t, created.result.IsError)
	listed := wait()
	require.NotNil(t, listed.result)
	require.False(t, listed.result.IsError)
	check, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer check.Close()
	var reminderID, target, creator, storedDue string
	require.NoError(t, check.QueryRowContext(ctx, `SELECT id,target,creator,next_due_at FROM reminders WHERE body=?`, body).Scan(&reminderID, &target, &creator, &storedDue))
	require.Equal(t, binding.AgentID, target)
	require.Equal(t, binding.AgentID, creator)
	require.Equal(t, dueText, storedDue)
	require.Contains(t, listed.result.Content[0].(ai.TextContent).Text, reminderID)
	var effectAgent, effectSession, effectGeneration string
	var effectEpoch int64
	require.NoError(t, check.QueryRowContext(ctx, `SELECT agent_id,session_id,epoch,generation FROM embedded_effects WHERE effect_id=?`, callID).Scan(&effectAgent, &effectSession, &effectEpoch, &effectGeneration))
	require.Equal(t, binding.AgentID, effectAgent)
	require.Equal(t, binding.SessionID, effectSession)
	require.Equal(t, binding.Epoch, effectEpoch)
	require.Equal(t, binding.Generation, effectGeneration)
	require.Eventually(t, func() bool { return providerCalls.Load() == 3 }, 3*time.Second, 10*time.Millisecond)

	countMessages := func() int {
		var n int
		require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE source_kind='reminder' AND recipient=?`, binding.AgentID).Scan(&n))
		return n
	}
	require.Zero(t, countMessages())
	require.NoError(t, clock.Advance(1*time.Second-time.Nanosecond))
	fired, failed := r.sweepReminders(ctx)
	require.Zero(t, fired)
	require.Zero(t, failed)
	require.Zero(t, countMessages())
	require.NoError(t, clock.Advance(time.Nanosecond))
	fired, failed = r.sweepReminders(ctx)
	require.Equal(t, 1, fired)
	require.Zero(t, failed)
	require.Equal(t, 1, countMessages())
	var messageID, source, recipient, sender, state string
	require.NoError(t, check.QueryRowContext(ctx, `SELECT id,source_kind,recipient,sender,state FROM messages WHERE source_kind='reminder' AND recipient=?`, binding.AgentID).Scan(&messageID, &source, &recipient, &sender, &state))
	require.Equal(t, "reminder", source)
	require.Equal(t, binding.AgentID, recipient)
	require.Equal(t, "reminders", sender)
	// Queue acceptance is not an ACK. The processing ACK must join the exact
	// reserved attempt to a checked SDK user entry, regardless of current state.
	require.Contains(t, []string{domain.MessageQueued, domain.MessageAcknowledged}, state)
	var attemptID, entryKind string
	var entryBody []byte
	require.Eventually(t, func() bool {
		return check.QueryRowContext(ctx, `SELECT a.attempt_id,e.kind,e.body FROM embedded_attempts a JOIN embedded_acks k ON k.attempt_id=a.attempt_id JOIN embedded_entries e ON e.agent_id=k.agent_id AND e.session_id=k.session_id AND e.epoch=k.epoch AND e.seq=k.entry_seq WHERE a.message_id=?`, messageID).Scan(&attemptID, &entryKind, &entryBody) == nil
	}, 4*time.Second, 10*time.Millisecond, "reminder queue admission did not become a checked SDK processing ACK")
	require.Equal(t, "user", entryKind)
	require.True(t, strings.Contains(string(entryBody), attemptID))
	require.True(t, strings.Contains(string(entryBody), "reminders"))
	require.True(t, strings.Contains(string(entryBody), body))
	message, err := db.MessageFor(ctx, messageID)
	require.NoError(t, err)
	require.Equal(t, domain.MessageAcknowledged, message.State)
	require.Eventually(t, func() bool { return providerCalls.Load() == 4 }, 3*time.Second, 10*time.Millisecond)
	var finalBody []byte
	require.Eventually(t, func() bool {
		return check.QueryRowContext(ctx, `SELECT body FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND kind='assistant' ORDER BY seq DESC LIMIT 1`, binding.AgentID, binding.SessionID, binding.Epoch).Scan(&finalBody) == nil
	}, 3*time.Second, 10*time.Millisecond)
	finalMessage, err := ai.UnmarshalMessage(finalBody)
	require.NoError(t, err)
	finalAssistant, ok := finalMessage.(ai.AssistantMessage)
	require.True(t, ok)
	require.Equal(t, ai.StopStop, finalAssistant.StopReason)
	var replies, outbox int
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_reply_outbox WHERE attempt_id=?`, attemptID).Scan(&outbox))
	require.Zero(t, outbox)
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE sender=? AND recipient='reminders'`, binding.AgentID).Scan(&replies))
	require.Zero(t, replies, "system-origin reminder must not produce a sender-bound reply")
	fired, failed = r.sweepReminders(ctx)
	require.Zero(t, fired)
	require.Zero(t, failed)
	require.NoError(t, r.Close(ctx))
	require.NoError(t, m.Close(ctx))
	require.NoError(t, db.Close())
	// A clean Store, Manager and Router over the same DB cannot repeat the
	// occurrence, SDK ACK or provider turn. No second scheduler is involved.
	db, err = store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	m = newManager(db)
	r, err = New(Options{Store: db, Managed: m, Clock: clock})
	require.NoError(t, err)
	fired, failed = r.sweepReminders(ctx)
	require.Zero(t, fired)
	require.Zero(t, failed)
	require.Equal(t, 1, countMessages())
	var acks int
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_acks k JOIN embedded_attempts a ON a.attempt_id=k.attempt_id WHERE a.message_id=?`, messageID).Scan(&acks))
	require.Equal(t, 1, acks)
	require.EqualValues(t, 4, providerCalls.Load())
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE sender=? AND recipient='reminders'`, binding.AgentID).Scan(&replies))
	require.Zero(t, replies)
}
