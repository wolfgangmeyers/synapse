package transport

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
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

// Test-only contract probe: the provider must see and call the fixed SDK tool.
// Run the root first and stop at the first failed assertion; do not treat a
// Store-only reminder mutation as evidence of SDK admission.
func TestEmbeddedG5ReminderCRUDRecurringSDKRED(t *testing.T) {
	for _, role := range []string{"root", "independent-worker"} {
		ctx := context.Background()
		root := t.TempDir()
		path := filepath.Join(root, "router.db")
		start := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
		clock := deterministic.NewFixedClock(start)
		due := start.Add(time.Second)
		dueText := due.Format("2006-01-02T15:04:05.000Z")
		secondDue := start.Add(2 * time.Second).Format("2006-01-02T15:04:05.000Z")
		later := start.Add(4 * time.Second).Format("2006-01-02T15:04:05.000Z")
		db, err := store.Open(ctx, path, store.Options{Clock: clock})
		require.NoError(t, err)
		manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
		require.NoError(t, err)
		reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
		const tool = "mecha_reminder"
		type observed struct {
			tools  []ai.Tool
			result *ai.ToolResultMessage
		}
		seen := make(chan observed, 12)
		ids := make(chan string, 2)
		allowUpdate := make(chan struct{})
		releaseUpdate := sync.OnceFunc(func() { close(allowUpdate) })
		allowDelete := make(chan struct{})
		releaseDelete := sync.OnceFunc(func() { close(allowDelete) })
		var turns atomic.Int32
		result := func(h ai.TranscriptContext, id string) (o observed) {
			for _, msg := range h.Messages {
				if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == id {
					copy := r
					o.result = &copy
				}
			}
			return
		}
		step := func(id, next string, args func() map[string]any) providers.FauxResponseStep {
			return func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
				turns.Add(1)
				seen <- result(h, id)
				return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall(tool, args(), next)}, ai.StopToolUse)
			}
		}
		create := map[string]any{"action": "create", "body": "one-shot own reminder", "due_at": dueText}
		recurring := map[string]any{"action": "create", "body": "recurring own reminder", "due_at": dueText, "interval_ms": int64(1000)}
		reg.SetResponses([]providers.FauxResponseStep{
			func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
				turns.Add(1)
				var first observed
				for _, msg := range h.Messages {
					if s, ok := msg.(ai.SystemMessage); ok {
						first.tools = append(first.tools, s.ToolsAdded...)
					}
				}
				seen <- first
				return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall(tool, create, "g5-one-create")}, ai.StopToolUse)
			},
			step("g5-one-create", "g5-one-list", func() map[string]any { return map[string]any{"action": "list"} }),
			step("g5-one-list", "g5-rec-create", func() map[string]any { return recurring }),
			func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
				turns.Add(1)
				seen <- result(h, "g5-rec-create")
				return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "ready"}}, ai.StopStop)
			},
			func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
				turns.Add(1)
				return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "received first sweep reminder"}}, ai.StopStop)
			},
			func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
				turns.Add(1)
				return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "received first sweep reminder"}}, ai.StopStop)
			},
			func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
				<-allowUpdate // Keep the second recurring occurrence intact until its durable mail and ACK are checked.
				turns.Add(1)
				return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall(tool, map[string]any{"action": "update", "id": <-ids, "body": "recurring updated", "due_at": later, "interval_ms": int64(2000)}, "g5-rec-update")}, ai.StopToolUse)
			},
			step("g5-rec-update", "g5-rec-list", func() map[string]any { return map[string]any{"action": "list"} }),
			step("g5-rec-list", "g5-rec-delete", func() map[string]any {
				<-allowDelete // Preserve the updated row until its durable fields are asserted.
				return map[string]any{"action": "delete", "id": <-ids}
			}),
			func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
				turns.Add(1)
				seen <- result(h, "g5-rec-delete")
				return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "ready"}}, ai.StopStop)
			},
		})
		makeManager := func(s *store.Store) *managed.Manager {
			m, e := managed.NewManager(managed.ManagerOptions{Store: s, Manifest: manifest, CodexCredentialStore: testfixture.CodexAdmission(t),
				Config:                managed.Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
				CatalogSource:         managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
				EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
			})
			require.NoError(t, e)
			return m
		}
		m := makeManager(db)
		router, err := New(Options{Store: db, Managed: m, Clock: clock})
		require.NoError(t, err)
		defer func() {
			releaseUpdate()
			releaseDelete()
			_ = router.Close(ctx)
			_ = m.Close(ctx)
			_ = db.Close()
			reg.Unregister()
		}()
		request := managed.LaunchRequest{Name: "g5-reminder-" + role, Cwd: root, Runtime: "embedded", InitialInstruction: "manage my own reminders", RequestID: "b5555555-5555-4555-8555-555555555555"}
		var launched managed.LaunchResult
		if role == "root" {
			launched, err = m.LaunchEmbeddedRoot(ctx, request)
		} else {
			launched, err = m.Launch(ctx, request)
		}
		require.NoError(t, err)
		binding := store.EmbeddedBinding{AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
		savedRole, err := db.EmbeddedSessionRole(ctx, binding)
		require.NoError(t, err)
		expectedRole := role
		if role == "independent-worker" {
			expectedRole = "worker"
		}
		require.Equal(t, expectedRole, savedRole)
		wait := func() observed {
			t.Helper()
			select {
			case o := <-seen:
				return o
			case <-time.After(4 * time.Second):
				t.Fatal("SDK provider turn not reached")
				return observed{}
			}
		}
		first := wait()
		var fixed *ai.Tool
		for i := range first.tools {
			if first.tools[i].Name == tool {
				fixed = &first.tools[i]
			}
		}
		require.NotNil(t, fixed, "fixed SDK mecha_reminder registration absent")
		require.True(t, fixed.Parameters.Check(create), "one-shot create must be provider-callable")
		require.True(t, fixed.Parameters.Check(recurring), "recurring create must be provider-callable")
		require.True(t, fixed.Parameters.Check(map[string]any{"action": "update", "id": "own-reminder", "due_at": later, "interval_ms": int64(2000)}), "schedule/interval update must be provider-callable")
		require.True(t, fixed.Parameters.Check(map[string]any{"action": "delete", "id": "own-reminder"}), "delete must be provider-callable")
		for _, key := range []string{"target", "creator", "recipient", "owner", "role"} {
			require.False(t, fixed.Parameters.Check(map[string]any{"action": "create", "body": "x", "due_at": dueText, key: "other-agent"}), "caller may not choose %s", key)
		}
		check, e := sql.Open("sqlite", path)
		require.NoError(t, e)
		defer check.Close()
		expectResult := func(id string) {
			t.Helper()
			o := wait()
			require.NotNil(t, o.result, id)
			require.Equal(t, id, o.result.ToolCallID)
			require.False(t, o.result.IsError, id)
		}
		expectResult("g5-one-create")
		var oneID, target, creator string
		require.NoError(t, check.QueryRowContext(ctx, `SELECT id,target,creator FROM reminders WHERE body='one-shot own reminder'`).Scan(&oneID, &target, &creator))
		require.Equal(t, binding.AgentID, target)
		require.Equal(t, binding.AgentID, creator)
		expectResult("g5-one-list")
		expectResult("g5-rec-create")
		var recID string
		var interval int64
		require.NoError(t, check.QueryRowContext(ctx, `SELECT id,interval_ms FROM reminders WHERE body='recurring own reminder'`).Scan(&recID, &interval))
		require.EqualValues(t, 1000, interval)
		ids <- recID
		ids <- recID
		require.Eventually(t, func() bool { return turns.Load() == 4 }, 3*time.Second, 10*time.Millisecond)
		countMail := func() int {
			t.Helper()
			var n int
			require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE source_kind='reminder' AND recipient=?`, binding.AgentID).Scan(&n))
			return n
		}
		require.Zero(t, countMail())
		require.NoError(t, clock.Advance(time.Second-time.Nanosecond))
		fired, failed := router.sweepReminders(ctx)
		require.Zero(t, fired)
		require.Zero(t, failed)
		require.Zero(t, countMail())
		require.NoError(t, clock.Advance(time.Nanosecond))
		fired, failed = router.sweepReminders(ctx)
		require.Equal(t, 2, fired, "one-shot and recurring occurrence zero must fire together")
		require.Zero(t, failed)
		require.Equal(t, 2, countMail())
		oneMessageID := "reminder-" + oneID + "-0"
		recMessageIDs := []string{"reminder-" + recID + "-0", "reminder-" + recID + "-1"}
		assertAcknowledged := func(messageID, expectedBody string) {
			t.Helper()
			var sender, recipient, mailBody, source string
			require.NoError(t, check.QueryRowContext(ctx, `SELECT sender,recipient,body,source_kind FROM messages WHERE id=?`, messageID).Scan(&sender, &recipient, &mailBody, &source))
			require.Equal(t, "reminder", source)
			require.Equal(t, "reminders", sender)
			require.Equal(t, binding.AgentID, recipient)
			require.Equal(t, expectedBody, mailBody)
			var attemptID, kind string
			var body []byte
			require.Eventually(t, func() bool {
				return check.QueryRowContext(ctx, `SELECT a.attempt_id,e.kind,e.body FROM embedded_attempts a JOIN embedded_acks k ON k.attempt_id=a.attempt_id AND k.message_id=a.message_id AND k.agent_id=a.agent_id AND k.session_id=a.session_id AND k.epoch=a.epoch AND k.generation=a.generation JOIN embedded_entries e ON e.agent_id=k.agent_id AND e.session_id=k.session_id AND e.epoch=k.epoch AND e.seq=k.entry_seq WHERE a.message_id=? AND a.agent_id=? AND a.session_id=? AND a.epoch=? AND a.generation=?`, messageID, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation).Scan(&attemptID, &kind, &body) == nil
			}, 4*time.Second, 10*time.Millisecond)
			require.Equal(t, "user", kind)
			require.Contains(t, string(body), attemptID)
			require.Contains(t, string(body), expectedBody)
			message, e := db.MessageFor(ctx, messageID)
			require.NoError(t, e)
			require.Equal(t, domain.MessageAcknowledged, message.State)
		}
		assertAcknowledged(oneMessageID, "one-shot own reminder")
		assertAcknowledged(recMessageIDs[0], "recurring own reminder")
		var nextDue string
		var linked sql.NullString
		var occurrence int
		require.NoError(t, check.QueryRowContext(ctx, `SELECT next_due_at,linked_message_id,occurrence_number FROM reminders WHERE id=?`, recID).Scan(&nextDue, &linked, &occurrence))
		require.Equal(t, secondDue, nextDue)
		require.False(t, linked.Valid, "acknowledged recurrence must be unlinked for the next occurrence")
		require.Equal(t, 1, occurrence)
		fired, failed = router.sweepReminders(ctx)
		require.Zero(t, fired)
		require.Zero(t, failed)
		require.NoError(t, clock.Advance(time.Second))
		fired, failed = router.sweepReminders(ctx)
		require.Equal(t, 1, fired, "second scheduled occurrence must fire before update/delete")
		require.Zero(t, failed)
		require.Equal(t, 3, countMail())
		assertAcknowledged(recMessageIDs[1], "recurring own reminder")
		require.NoError(t, check.QueryRowContext(ctx, `SELECT next_due_at,linked_message_id,occurrence_number FROM reminders WHERE id=?`, recID).Scan(&nextDue, &linked, &occurrence))
		require.Equal(t, start.Add(3*time.Second).Format("2006-01-02T15:04:05.000Z"), nextDue)
		require.False(t, linked.Valid, "second acknowledged recurrence must be unlinked")
		require.Equal(t, 2, occurrence)
		releaseUpdate()
		require.Eventually(t, func() bool { return turns.Load() >= 7 }, 3*time.Second, 10*time.Millisecond)
		expectResult("g5-rec-update")
		var changedBody, changedDue string
		require.NoError(t, check.QueryRowContext(ctx, `SELECT body,next_due_at,interval_ms FROM reminders WHERE id=?`, recID).Scan(&changedBody, &changedDue, &interval))
		require.Equal(t, "recurring updated", changedBody)
		require.Equal(t, later, changedDue)
		require.EqualValues(t, 2000, interval)
		releaseDelete()
		expectResult("g5-rec-list")
		expectResult("g5-rec-delete")
		var count int
		require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM reminders WHERE id=?`, recID).Scan(&count))
		require.Zero(t, count)
		require.Eventually(t, func() bool { return turns.Load() == 10 }, 3*time.Second, 10*time.Millisecond)
		require.NoError(t, clock.Advance(4*time.Second))
		fired, failed = router.sweepReminders(ctx)
		require.Zero(t, fired)
		require.Zero(t, failed)
		require.Equal(t, 3, countMail())
		require.NoError(t, router.Close(ctx))
		require.NoError(t, m.Close(ctx))
		require.NoError(t, db.Close())
		db, err = store.Open(ctx, path, store.Options{Clock: clock})
		require.NoError(t, err)
		m = makeManager(db)
		router, err = New(Options{Store: db, Managed: m, Clock: clock})
		require.NoError(t, err)
		fired, failed = router.sweepReminders(ctx)
		require.Zero(t, fired)
		require.Zero(t, failed)
		require.Equal(t, 3, countMail())
		for _, messageID := range append([]string{oneMessageID}, recMessageIDs...) {
			require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_acks k JOIN embedded_attempts a ON a.attempt_id=k.attempt_id AND a.message_id=k.message_id WHERE a.message_id=?`, messageID).Scan(&count))
			require.Equal(t, 1, count, messageID)
		}
		require.EqualValues(t, 10, turns.Load())
	}
}
