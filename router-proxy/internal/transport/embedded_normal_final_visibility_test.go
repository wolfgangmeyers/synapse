package transport

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// M3/A1: two accepted senders share one serial SDK lane, not a latest-sender
// variable. Check the unchanged REST and real conversation broadcast surfaces.
// Reminder no-auto-reply coverage remains separate in
// TestEmbeddedIndependentWorkerReminderRealRouterSweep. Explicit-mail behavior
// is outside this test; ordinary finals do not exercise mecha_mailbox_send.
func TestEmbeddedQueuedNormalFinalConversationRESTAndEvents(t *testing.T) {
	ctx := context.Background()
	f := newEmbeddedHTTPG2Fixture(t) // Private Cwd and offline CodexAdmission credential.
	info, err := os.Stat(f.root)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0700), info.Mode().Perm())
	const agentID = "mail-root"
	ids := [2]string{"d7111111-1111-4111-8111-111111111111", "d7222222-2222-4222-8222-222222222222"}
	senders := [2]string{"normal-sender-a", "normal-sender-b"}
	bodies := [2]string{"accepted ordinary A", "accepted ordinary B"}
	finals := [2]string{"ordinary final for A only", "ordinary final for B only"}
	type origin struct {
		Version, AttemptID, MessageID, IncarnationID, Sender, Recipient, Body string
	}
	type observedTurn struct {
		origin origin
		err    error
	}
	entered := make(chan observedTurn, 2)
	releaseA, releasePartial, releaseB := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var onceA, oncePartial, onceB sync.Once
	defer onceA.Do(func() { close(releaseA) })
	defer oncePartial.Do(func() { close(releasePartial) })
	defer onceB.Do(func() { close(releaseB) })
	var calls atomic.Int32
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{MinTokenSize: 1, MaxTokenSize: 1})
	t.Cleanup(reg.Unregister)
	steps := make([]providers.FauxResponseStep, 2)
	for i := range steps {
		index := i
		steps[i] = func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			calls.Add(1)
			var o origin
			var decodeErr error
			for _, message := range h.Messages {
				if user, ok := message.(ai.UserMessage); ok && len(user.Content) == 1 {
					if text, ok := user.Content[0].(ai.TextContent); ok {
						decodeErr = json.Unmarshal([]byte(text.Text), &o)
					}
				}
			}
			entered <- observedTurn{o, decodeErr}
			if index == 0 {
				<-releaseA
			} else {
				<-releaseB
			}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: finals[index]}}, ai.StopStop)
		}
	}
	reg.SetResponses(steps)
	// Keep the registered faux provider, but pause its actual first text delta
	// before allowing any terminal event. No tool or synthetic final is injected.
	provider, ok := ai.GetApiProvider(reg.GetModel().Api)
	require.True(t, ok)
	originalStream := provider.StreamSimple
	partial := make(chan ai.AssistantMessageEvent, 1)
	var streamCalls atomic.Int32
	provider.StreamSimple = func(c context.Context, model *ai.Model, transcript ai.TranscriptContext, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		first := streamCalls.Add(1) == 1
		upstream := originalStream(c, model, transcript, opts)
		downstream := ai.NewAssistantMessageEventStream()
		go func() {
			defer downstream.End()
			paused := false
			for event := range upstream.Events() {
				downstream.Push(event)
				if first && !paused && event.Type == ai.EventTextDelta {
					paused = true
					partial <- event
					select {
					case <-releasePartial:
					case <-c.Done():
						return
					}
				}
			}
		}()
		return downstream
	}
	ai.RegisterApiProvider(provider, t.Name())
	t.Cleanup(func() { ai.UnregisterApiProviders(t.Name()) })
	f.model = reg.GetModel() // Existing fixture resolver reads this before launch.

	require.NoError(t, f.router.Listen(ctx))
	httpURL, _ := f.router.URLs()
	// Reuse the existing event-client seam, not a fake publisher. afterEnqueue
	// calls broadcastConversation, which sends these real payloads to convos.
	updates := [2]chan domain.ConversationMessage{make(chan domain.ConversationMessage, 16), make(chan domain.ConversationMessage, 16)}
	var subscribers [2]*session
	f.router.mu.Lock()
	for i, sender := range senders {
		index := i
		subscriber := &session{router: f.router, sendOverride: func(fields map[string]any) error {
			if fields["type"] == "conversation_message" {
				updates[index] <- fields["message"].(domain.ConversationMessage)
			}
			return nil
		}}
		subscribers[i] = subscriber
		f.router.convos[sender] = map[string]*session{"normal-final-observer": subscriber}
	}
	f.router.mu.Unlock()
	// Registered after fixture construction: LIFO cleanup removes only our
	// nil-connection event subscribers before the fixture calls Router.Close.
	t.Cleanup(func() {
		f.router.mu.Lock()
		defer f.router.mu.Unlock()
		for i, sender := range senders {
			listeners := f.router.convos[sender]
			if listeners["normal-final-observer"] == subscribers[i] {
				delete(listeners, "normal-final-observer")
				if len(listeners) == 0 {
					delete(f.router.convos, sender)
				}
			}
		}
	})
	_, err = f.manager.LaunchEmbeddedRoot(ctx, managed.LaunchRequest{Name: agentID, Cwd: f.root, Runtime: "embedded", RequestID: "d7000000-0000-4000-8000-000000000000"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		row, e := f.db.ManagedAgent(ctx, agentID)
		return e == nil && row.LifecycleState == "running" && row.AcceptingPrompts
	}, 3*time.Second, 10*time.Millisecond)
	saved, err := f.db.ManagedAgent(ctx, agentID)
	require.NoError(t, err)
	check, err := sql.Open("sqlite", f.dbPath)
	require.NoError(t, err)
	defer check.Close()

	payloads := [2]map[string]any{}
	accept := func(i int, expectedStatus int) {
		t.Helper()
		payloads[i] = map[string]any{"id": ids[i], "from": senders[i], "to": agentID, "body": bodies[i], "created_at": "2026-09-27T00:00:00.000Z"}
		response := postJSON(t, httpURL+"/v1/messages", payloads[i])
		defer response.Body.Close()
		require.Equal(t, expectedStatus, response.StatusCode)
		var result struct {
			Message domain.Message `json:"message"`
		}
		require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
		require.Equal(t, ids[i], result.Message.ID)
		require.Equal(t, senders[i], result.Message.From)
		require.Equal(t, agentID, result.Message.To)
		require.Equal(t, bodies[i], result.Message.Body)
	}
	view := func(i int) []domain.ConversationMessage {
		t.Helper()
		response, e := http.Get(httpURL + "/v1/conversations?owner=" + senders[i] + "&peer=" + agentID)
		require.NoError(t, e)
		defer response.Body.Close()
		require.Equal(t, http.StatusOK, response.StatusCode)
		var result struct {
			Messages []domain.ConversationMessage `json:"messages"`
		}
		require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
		return result.Messages
	}
	readUpdate := func(i int) domain.ConversationMessage {
		t.Helper()
		select {
		case message := <-updates[i]:
			return message
		case <-time.After(3 * time.Second):
			t.Fatal("missing real conversation_message update for " + senders[i])
			return domain.ConversationMessage{}
		}
	}
	assertUpdate := func(expected, actual domain.ConversationMessage) {
		t.Helper()
		// DeliveryMessageID is internal (json:"-"); compare the public payload.
		want, e := json.Marshal(expected)
		require.NoError(t, e)
		got, e := json.Marshal(actual)
		require.NoError(t, e)
		require.JSONEq(t, string(want), string(got), "real event payload must match REST")
	}
	readTurn := func(i int) origin {
		t.Helper()
		select {
		case turn := <-entered:
			require.NoError(t, turn.err)
			require.Equal(t, "embedded-mail/v1", turn.origin.Version)
			require.Equal(t, ids[i], turn.origin.MessageID)
			require.Equal(t, senders[i], turn.origin.Sender, "current sender belongs to this accepted turn")
			require.Equal(t, agentID, turn.origin.Recipient)
			require.Equal(t, bodies[i], turn.origin.Body)
			require.NotEmpty(t, turn.origin.AttemptID)
			require.NotEmpty(t, turn.origin.IncarnationID)
			return turn.origin
		case <-time.After(3 * time.Second):
			t.Fatal("SDK did not enter ordered turn")
			return origin{}
		}
	}
	accept(0, http.StatusCreated)
	originA := readTurn(0) // Barrier: A has entered, B has not been accepted yet.
	accept(1, http.StatusCreated)
	for i := range ids {
		messages := view(i)
		require.Len(t, messages, 1, "acceptance/streaming is not a final reply")
		require.Equal(t, ids[i], messages[0].ID)
		assertUpdate(messages[0], readUpdate(i))
	}
	queued, err := f.db.MessageFor(ctx, ids[1])
	require.NoError(t, err)
	require.Equal(t, domain.MessageQueued, queued.State)
	var prematureAttempts, prematureReplies int
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_attempts WHERE message_id=?`, ids[1]).Scan(&prematureAttempts))
	require.Zero(t, prematureAttempts, "B cannot enter A's serial lane")
	require.EqualValues(t, 1, calls.Load())
	onceA.Do(func() { close(releaseA) })
	select {
	case event := <-partial:
		require.NotEmpty(t, event.Delta)
		require.NotNil(t, event.Partial)
		require.NotEqual(t, finals[0], event.Partial.Content[0].(ai.TextContent).Text)
	case <-time.After(3 * time.Second):
		t.Fatal("actual faux stream did not emit a partial text delta")
	}
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_reply_outbox`).Scan(&prematureReplies))
	require.Zero(t, prematureReplies, "streamed partial text is not a reply")
	for i := range ids {
		require.Len(t, view(i), 1)
		select {
		case message := <-updates[i]:
			t.Fatalf("premature conversation reply for %s: %+v", senders[i], message)
		default:
		}
	}
	oncePartial.Do(func() { close(releasePartial) })
	originB := readTurn(1) // A must settle and publish before B's provider enters.
	require.NotEqual(t, originA.AttemptID, originB.AttemptID)
	require.NotEqual(t, originA.Sender, originB.Sender)
	require.Len(t, view(0), 2)
	require.Len(t, view(1), 1, "B is still blocked before its normal final")
	onceB.Do(func() { close(releaseB) })

	origins := [2]origin{originA, originB}
	var replyIDs [2]string
	for i := range ids {
		var replyID, replyFrom, replyTo, replyBody, createdAt string
		var finalBody []byte
		var finalSeq, ackSeq int64
		require.Eventually(t, func() bool {
			return check.QueryRowContext(ctx, `SELECT o.reply_id,o.sender,o.recipient,o.body,o.created_at,o.entry_seq,k.entry_seq,e.body FROM embedded_reply_outbox o JOIN embedded_attempts a ON a.attempt_id=o.attempt_id JOIN embedded_acks k ON k.attempt_id=a.attempt_id AND k.agent_id=a.agent_id AND k.session_id=a.session_id AND k.epoch=a.epoch AND k.generation=a.generation JOIN embedded_entries e ON e.agent_id=a.agent_id AND e.session_id=a.session_id AND e.epoch=a.epoch AND e.generation=a.generation AND e.seq=o.entry_seq AND e.digest=o.entry_digest WHERE a.message_id=? AND a.attempt_id=? AND a.sender=? AND a.accepted_body=? AND a.agent_id=? AND a.session_id=? AND a.epoch=? AND a.generation=? AND o.state='delivered'`, ids[i], origins[i].AttemptID, senders[i], bodies[i], agentID, saved.PiSessionID, saved.SessionEpoch, saved.RuntimeGeneration).Scan(&replyID, &replyFrom, &replyTo, &replyBody, &createdAt, &finalSeq, &ackSeq, &finalBody) == nil
		}, 3*time.Second, 10*time.Millisecond)
		require.Greater(t, finalSeq, ackSeq)
		require.Equal(t, agentID, replyFrom)
		require.Equal(t, senders[i], replyTo)
		require.Equal(t, finals[i], replyBody)
		message, e := ai.UnmarshalMessage(finalBody)
		require.NoError(t, e)
		assistant, ok := message.(ai.AssistantMessage)
		require.True(t, ok)
		require.Equal(t, ai.StopStop, assistant.StopReason, "normal final is mandatory")
		require.Equal(t, finals[i], assistant.Content[0].(ai.TextContent).Text)
		messages := view(i)
		require.Len(t, messages, 2, "exactly one accepted inbound and one final")
		reply := messages[1]
		require.Equal(t, replyID, reply.ID)
		require.Equal(t, agentID, reply.From)
		require.Equal(t, senders[i], reply.To)
		require.Equal(t, finals[i], reply.Body)
		require.Equal(t, createdAt, reply.CreatedAt)
		require.Equal(t, "agent", reply.Source.Kind)
		require.NotEmpty(t, reply.ID)
		assertUpdate(reply, readUpdate(i))
		replyIDs[i] = reply.ID
	}
	require.NotEqual(t, replyIDs[0], replyIDs[1])
	// Synchronous frozen-outbox recovery and identical HTTP acceptance replays
	// cannot rerun a model, append a second final, or broadcast a duplicate.
	require.NoError(t, f.manager.RecoverEmbeddedReplies(ctx))
	require.NoError(t, f.manager.RecoverEmbeddedReplies(ctx))
	for i := range ids {
		accept(i, http.StatusOK)
		require.Len(t, view(i), 2)
		select {
		case duplicate := <-updates[i]:
			t.Fatalf("duplicate/cross-sender conversation update for %s: %+v", senders[i], duplicate)
		default:
		}
	}
	var replies, attempts, acks int
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_reply_outbox`).Scan(&replies))
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_attempts WHERE agent_id=?`, agentID).Scan(&attempts))
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_acks WHERE agent_id=?`, agentID).Scan(&acks))
	require.Equal(t, 2, replies)
	require.Equal(t, 2, attempts)
	require.Equal(t, 2, acks)
	require.EqualValues(t, 2, calls.Load())
}
