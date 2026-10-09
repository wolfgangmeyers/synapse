package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedMailPoisonPillDoesNotSpin(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	path := filepath.Join(t.TempDir(), "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var turnAttempts atomic.Int32
	step := func(h ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
		turnAttempts.Add(1)
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "ok"}}, ai.StopStop)
	}
	reg.SetResponses([]providers.FauxResponseStep{step, step, step, step, step})

	manager, err := NewManager(ManagerOptions{
		Store:                        db,
		Manifest:                     manifest,
		Config:                       Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:                CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver:        func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		CodexCredentialStore:         testfixture.CodexAdmission(t),
		EmbeddedMailBackoff:          10 * time.Millisecond,
	})
	require.NoError(t, err)
	defer manager.Close(ctx)

	launched, err := manager.Launch(ctx, LaunchRequest{Name: "poison-lane", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, RequestID: "b1111111-1111-4111-8111-111111111112"})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return e == nil && a.LifecycleState == "running" && a.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	// Simulate turn execution error on mail append by attaching a trigger on embedded_acks
	rawDB, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer rawDB.Close()
	_, err = rawDB.Exec(`CREATE TRIGGER fail_mail_ack BEFORE INSERT ON embedded_acks BEGIN SELECT RAISE(ABORT, 'simulated append failure'); END;`)
	require.NoError(t, err)

	msgID := "b2222222-2222-4222-8222-222222222223"
	_, err = db.Enqueue(ctx, protocol.Envelope{
		ID:        msgID,
		From:      "sender",
		To:        launched.Agent.AgentID,
		Body:      "this message triggers execution error",
		CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}, store.EnqueueOptions{})
	require.NoError(t, err)

	target, err := db.ResolveMessageTarget(ctx, msgID)
	require.NoError(t, err)
	require.Equal(t, "managed", target.SourceKind)

	manager.OfferDirect(launched.Agent.AgentID)

	// Asserts that retries are bounded and the message is dead-lettered after max retries (3)
	require.Eventually(t, func() bool {
		msg, e := db.MessageFor(ctx, msgID)
		return e == nil && msg.State == domain.MessageDeadLetter
	}, 3*time.Second, 10*time.Millisecond)

	// Agent transitions cleanly back to idle
	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return e == nil && a.ActivityState == "idle"
	}, 2*time.Second, 10*time.Millisecond)

	// Ensure it does NOT cycle endlessly between active and idle
	time.Sleep(50 * time.Millisecond)
	a, err := db.ManagedAgent(ctx, launched.Agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", a.ActivityState)

	msg, err := db.MessageFor(ctx, msgID)
	require.NoError(t, err)
	require.Equal(t, domain.MessageDeadLetter, msg.State)
}

func TestEmbeddedMailMidTurnSteering(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	path := filepath.Join(t.TempDir(), "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	// Initial launch response
	initialStep := func(h ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "ready"}}, ai.StopStop)
	}

	step1Started := make(chan struct{})
	allowStep1Finish := make(chan struct{})
	var sawSteering atomic.Bool
	var sawAttachmentRef atomic.Bool

	attFile := filepath.Join(root, "steering-test.txt")
	require.NoError(t, os.WriteFile(attFile, []byte("urgent attachment content"), 0644))

	// Multi-step turn responses:
	// Step 1: Tool call that pauses so steering can occur mid-turn
	multiStep1 := func(h ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
		select {
		case step1Started <- struct{}{}:
		default:
		}
		<-allowStep1Finish
		return providers.FauxAssistantMessage(ai.ContentList{
			providers.FauxToolCall("bash", map[string]any{"command": "echo 'step1 complete'"}, "call-step1"),
		}, ai.StopToolUse)
	}

	// Step 2: Next step after tool call, receives steering message in transcript
	multiStep2 := func(hist ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
		for _, msg := range hist.Messages {
			if u, ok := msg.(ai.UserMessage); ok {
				for _, c := range u.Content {
					if tc, ok := c.(ai.TextContent); ok {
						if strings.Contains(tc.Text, "urgent steer directive") {
							sawSteering.Store(true)
						}
						if strings.Contains(tc.Text, attFile) {
							sawAttachmentRef.Store(true)
						}
					}
				}
			}
		}
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "handled steering and finished turn"}}, ai.StopStop)
	}

	reg.SetResponses([]providers.FauxResponseStep{initialStep, initialStep, initialStep})

	manager, err := NewManager(ManagerOptions{
		Store:                 db,
		Manifest:              manifest,
		Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		CodexCredentialStore:  testfixture.CodexAdmission(t),
	})
	require.NoError(t, err)
	defer manager.Close(ctx)

	launched, err := manager.Launch(ctx, LaunchRequest{
		Name:      "steer-lane",
		Cwd:       root,
		Runtime:   "embedded",
		Model:     &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		RequestID: "b4444444-4444-4444-8444-444444444444",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return e == nil && a.LifecycleState == "running" && a.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	reg.SetResponses([]providers.FauxResponseStep{multiStep1, multiStep2})

	// Enqueue first message to start the multi-step turn
	turnMsgID := "b5555555-5555-4555-8555-555555555555"
	_, err = db.Enqueue(ctx, protocol.Envelope{
		ID:        turnMsgID,
		From:      "sender",
		To:        launched.Agent.AgentID,
		Body:      "begin long multi-step turn",
		CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}, store.EnqueueOptions{})
	require.NoError(t, err)
	_, err = db.ResolveMessageTarget(ctx, turnMsgID)
	require.NoError(t, err)

	manager.OfferDirect(launched.Agent.AgentID)

	// Wait for step 1 to start
	select {
	case <-step1Started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for step 1 to start")
	}

	// Verify activeSession is set on the lane mid-turn
	lane := manager.laneFor(launched.Agent.AgentID)
	lane.activeMu.RLock()
	active := lane.activeSession
	lane.activeMu.RUnlock()
	require.NotNil(t, active, "activeSession must be set mid-turn")

	// Enqueue a mid-turn steering message with attachment
	steerMsgID := "b6666666-6666-4666-8666-666666666666"
	singleAtt, _ := json.Marshal(map[string]any{"id": "att-1", "path": attFile})
	_, err = db.Enqueue(ctx, protocol.Envelope{
		ID:        steerMsgID,
		From:      "supervisor",
		To:        launched.Agent.AgentID,
		Body:      "urgent steer directive: please prioritize this",
		CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}, store.EnqueueOptions{
		Conversation: &store.ConversationOptions{
			Attachments: []json.RawMessage{singleAtt},
		},
	})
	require.NoError(t, err)
	_, err = db.ResolveMessageTarget(ctx, steerMsgID)
	require.NoError(t, err)

	// Verify trySteerActiveSession steers the message into activeSession
	handled := manager.trySteerActiveSession(launched.Agent.AgentID, active)
	require.True(t, handled, "trySteerActiveSession should successfully steer message mid-turn")

	// Allow step 1 to complete so the loop executes tool and advances to step 2
	close(allowStep1Finish)

	// Wait for turn to finish and agent to return to idle
	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return e == nil && a.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	// Verify message state was updated to acknowledged
	steeredMsgRecord, err := db.MessageFor(ctx, steerMsgID)
	require.NoError(t, err)
	require.Equal(t, domain.MessageAcknowledged, steeredMsgRecord.State)

	// Verify the message was received by runLoop() as a steering message
	require.True(t, sawSteering.Load(), "steering message must be delivered to runLoop()")
	require.True(t, sawAttachmentRef.Load(), "attachment path must be delivered in steering message")

	// Verify that the final assistant response produces a reply in embedded_reply_outbox for the steered message sender
	rawDB, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer rawDB.Close()

	var replySender, replyRecipient, replyBody string
	err = rawDB.QueryRowContext(ctx, `SELECT sender, recipient, body FROM embedded_reply_outbox WHERE attempt_id=(SELECT attempt_id FROM embedded_attempts WHERE message_id=?)`, steerMsgID).Scan(&replySender, &replyRecipient, &replyBody)
	require.NoError(t, err)
	require.Equal(t, launched.Agent.AgentID, replySender)
	require.Equal(t, "supervisor", replyRecipient)
	require.Contains(t, replyBody, "handled steering and finished turn")
}

// TestEmbeddedMailMultiToolInterruptedByMidTurnSteering tests that an active multi-tool
// batch is interrupted mid-turn by an incoming user steering message, aborts subsequent
// tools, and responds immediately to the user.
func TestEmbeddedMailMultiToolInterruptedByMidTurnSteering(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	path := filepath.Join(t.TempDir(), "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})

	step1ActiveFile := filepath.Join(root, "step1_active.txt")
	step1FinishFile := filepath.Join(root, "step1_finish.txt")
	step2MarkerFile := filepath.Join(root, "step2_should_never_run.txt")

	var sawSteering atomic.Bool
	var sawTool2Aborted atomic.Bool

	// Initial step: worker responds to genesis
	initialStep := func(h ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "ready"}}, ai.StopStop)
	}

	// Step 1: Model issues TWO tool calls in the same turn
	multiStep1 := func(h ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
		step1Cmd := fmt.Sprintf("touch '%s' && while [ ! -f '%s' ]; do sleep 0.02; done && echo 'step1 complete'", step1ActiveFile, step1FinishFile)
		step2Cmd := fmt.Sprintf("touch '%s' && echo 'step2 ran'", step2MarkerFile)
		return providers.FauxAssistantMessage(ai.ContentList{
			providers.FauxToolCall("bash", map[string]any{"command": step1Cmd}, "call-step1"),
			providers.FauxToolCall("bash", map[string]any{"command": step2Cmd}, "call-step2"),
		}, ai.StopToolUse)
	}

	// Step 2: Next step after tool call interruption, receives steering message in transcript
	multiStep2 := func(hist ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
		for _, msg := range hist.Messages {
			if u, ok := msg.(ai.UserMessage); ok {
				for _, c := range u.Content {
					if tc, ok := c.(ai.TextContent); ok {
						if strings.Contains(tc.Text, "urgent steer directive: halt tools and respond") {
							sawSteering.Store(true)
						}
					}
				}
			}
			if tr, ok := msg.(ai.ToolResultMessage); ok {
				if tr.ToolCallID == "call-step2" {
					for _, c := range tr.Content {
						if tc, ok := c.(ai.TextContent); ok {
							if strings.Contains(tc.Text, "interrupted by user steering message") {
								sawTool2Aborted.Store(true)
							}
						}
					}
				}
			}
		}
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "I halted the remaining tools and am responding to your steering message immediately."}}, ai.StopStop)
	}

	reg.SetResponses([]providers.FauxResponseStep{initialStep, initialStep, initialStep})

	manager, err := NewManager(ManagerOptions{
		Store:                 db,
		Manifest:              manifest,
		Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		CodexCredentialStore:  testfixture.CodexAdmission(t),
	})
	require.NoError(t, err)
	defer manager.Close(ctx)

	launched, err := manager.Launch(ctx, LaunchRequest{
		Name:      "steer-multitool-lane",
		Cwd:       root,
		Runtime:   "embedded",
		Model:     &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		RequestID: "c4444444-4444-4444-8444-444444444444",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return e == nil && a.LifecycleState == "running" && a.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	reg.SetResponses([]providers.FauxResponseStep{multiStep1, multiStep2})

	// Enqueue first message to start the multi-step turn
	turnMsgID := "c5555555-5555-4555-8555-555555555555"
	_, err = db.Enqueue(ctx, protocol.Envelope{
		ID:        turnMsgID,
		From:      "sender",
		To:        launched.Agent.AgentID,
		Body:      "begin long multi-tool turn",
		CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}, store.EnqueueOptions{})
	require.NoError(t, err)
	_, err = db.ResolveMessageTarget(ctx, turnMsgID)
	require.NoError(t, err)

	manager.OfferDirect(launched.Agent.AgentID)

	// Wait for tool 1 to start executing (it touches step1ActiveFile)
	require.Eventually(t, func() bool {
		_, statErr := os.Stat(step1ActiveFile)
		return statErr == nil
	}, 3*time.Second, 10*time.Millisecond)

	// Verify activeSession is set on the lane mid-turn
	lane := manager.laneFor(launched.Agent.AgentID)
	lane.activeMu.RLock()
	active := lane.activeSession
	lane.activeMu.RUnlock()
	require.NotNil(t, active, "activeSession must be set mid-turn")

	// Enqueue a mid-turn steering message while tool 1 is running
	steerMsgID := "c6666666-6666-4666-8666-666666666666"
	_, err = db.Enqueue(ctx, protocol.Envelope{
		ID:        steerMsgID,
		From:      "supervisor",
		To:        launched.Agent.AgentID,
		Body:      "urgent steer directive: halt tools and respond",
		CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}, store.EnqueueOptions{})
	require.NoError(t, err)
	_, err = db.ResolveMessageTarget(ctx, steerMsgID)
	require.NoError(t, err)

	// Verify trySteerActiveSession steers the message into activeSession
	handled := manager.trySteerActiveSession(launched.Agent.AgentID, active)
	require.True(t, handled, "trySteerActiveSession should successfully steer message mid-turn")

	// Allow tool 1 to complete so the loop advances to check steering before tool 2
	require.NoError(t, os.WriteFile(step1FinishFile, []byte("ok"), 0644))

	// Wait for turn to finish and agent to return to idle
	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return e == nil && a.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	// Verify tool 2 was NEVER executed
	_, statErr := os.Stat(step2MarkerFile)
	require.True(t, os.IsNotExist(statErr), "tool 2 must NOT have been executed!")

	// Verify message state was updated to acknowledged
	steeredMsgRecord, err := db.MessageFor(ctx, steerMsgID)
	require.NoError(t, err)
	require.Equal(t, domain.MessageAcknowledged, steeredMsgRecord.State)

	// Verify the message was received by runLoop() as a steering message
	require.True(t, sawSteering.Load(), "steering message must be delivered to runLoop()")
	require.True(t, sawTool2Aborted.Load(), "tool 2 must have been aborted with steering error message")

	// Verify that the final assistant response produces a reply in embedded_reply_outbox for the steered message sender
	rawDB, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer rawDB.Close()

	var replySender, replyRecipient, replyBody string
	err = rawDB.QueryRowContext(ctx, `SELECT sender, recipient, body FROM embedded_reply_outbox WHERE attempt_id=(SELECT attempt_id FROM embedded_attempts WHERE message_id=?)`, steerMsgID).Scan(&replySender, &replyRecipient, &replyBody)
	require.NoError(t, err)
	require.Equal(t, launched.Agent.AgentID, replySender)
	require.Equal(t, "supervisor", replyRecipient)
	require.Contains(t, replyBody, "I halted the remaining tools and am responding to your steering message immediately")
}

// TestEmbeddedMailToolsNoSyntheticFallbackText verifies that turns executing tools
// provide their responses without injecting the synthetic fallback string
// "(Action completed without additional text)" into the reply outbox.
func TestEmbeddedMailToolsNoSyntheticFallbackText(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	path := filepath.Join(t.TempDir(), "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	t.Run("ToolExecutionWithAssistantTextEmitsCleanResponse", func(t *testing.T) {
		initialStep := func(h ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "ready"}}, ai.StopStop)
		}
		reg.SetResponses([]providers.FauxResponseStep{initialStep, initialStep})

		manager, err := NewManager(ManagerOptions{
			Store:                        db,
			Manifest:                     manifest,
			Config:                       Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
			CatalogSource:                CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
			EmbeddedModelResolver:        func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
			CodexCredentialStore:         testfixture.CodexAdmission(t),
			EmbeddedMailBackoff:          10 * time.Millisecond,
		})
		require.NoError(t, err)
		defer manager.Close(ctx)

		launched, err := manager.Launch(ctx, LaunchRequest{
			Name: "clean-reply-worker", Cwd: root, Runtime: "embedded",
			Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
			RequestID: "c1111111-1111-4111-8111-111111111111",
		})
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			a, e := db.ManagedAgent(ctx, launched.Agent.AgentID)
			return e == nil && a.LifecycleState == "running" && a.ActivityState == "idle"
		}, 3*time.Second, 10*time.Millisecond)

		step2Finished := make(chan struct{})
		step1 := func(h ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{
				providers.FauxToolCall("bash", map[string]any{"command": "echo synthesized"}, "bash-call-1"),
			}, ai.StopToolUse)
		}
		step2 := func(h ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
			defer close(step2Finished)
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "Here is the synthesized answer based on tool output."},
			}, ai.StopStop)
		}
		reg.SetResponses([]providers.FauxResponseStep{step1, step2})

		turnMsgID := "c2222222-2222-4222-8222-222222222222"
		_, err = db.Enqueue(ctx, protocol.Envelope{
			ID:        turnMsgID,
			From:      "supervisor",
			To:        launched.Agent.AgentID,
			Body:      "execute tool and answer",
			CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		}, store.EnqueueOptions{})
		require.NoError(t, err)
		_, err = db.ResolveMessageTarget(ctx, turnMsgID)
		require.NoError(t, err)

		manager.OfferDirect(launched.Agent.AgentID)

		select {
		case <-step2Finished:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for step2 to finish")
		}

		require.Eventually(t, func() bool {
			a, e := db.ManagedAgent(ctx, launched.Agent.AgentID)
			return e == nil && a.ActivityState == "idle"
		}, 3*time.Second, 10*time.Millisecond)

		// Verify outbox message has the actual synthesized text, never the synthetic fallback
		rawDB, err := sql.Open("sqlite", path)
		require.NoError(t, err)
		defer rawDB.Close()

		var outboxBody string
		err = rawDB.QueryRowContext(ctx, `SELECT body FROM embedded_reply_outbox WHERE sender=? ORDER BY rowid DESC LIMIT 1`, launched.Agent.AgentID).Scan(&outboxBody)
		require.NoError(t, err)
		require.Equal(t, "Here is the synthesized answer based on tool output.", outboxBody)
		require.NotContains(t, outboxBody, "(Action completed without additional text)")

		var fallbackCount int
		require.NoError(t, rawDB.QueryRowContext(ctx, `SELECT count(*) FROM embedded_reply_outbox WHERE body LIKE '%Action completed without additional text%'`).Scan(&fallbackCount))
		require.Zero(t, fallbackCount, "synthetic fallback text must never appear in outbox")
	})

	t.Run("ToolExecutionWithEmptyTextDoesNotEmitFallbackBubble", func(t *testing.T) {
		initialStep := func(h ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "ready"}}, ai.StopStop)
		}
		reg.SetResponses([]providers.FauxResponseStep{initialStep, initialStep})

		manager, err := NewManager(ManagerOptions{
			Store:                        db,
			Manifest:                     manifest,
			Config:                       Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
			CatalogSource:                CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
			EmbeddedModelResolver:        func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
			CodexCredentialStore:         testfixture.CodexAdmission(t),
			EmbeddedMailBackoff:          10 * time.Millisecond,
		})
		require.NoError(t, err)
		defer manager.Close(ctx)

		launched, err := manager.Launch(ctx, LaunchRequest{
			Name: "silent-reply-worker", Cwd: root, Runtime: "embedded",
			Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
			RequestID: "c3333333-3333-4333-8333-333333333333",
		})
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			a, e := db.ManagedAgent(ctx, launched.Agent.AgentID)
			return e == nil && a.LifecycleState == "running" && a.ActivityState == "idle"
		}, 3*time.Second, 10*time.Millisecond)

		step2Finished := make(chan struct{})
		step1 := func(h ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{
				providers.FauxToolCall("bash", map[string]any{"command": "echo silent"}, "bash-call-2"),
			}, ai.StopToolUse)
		}
		step2 := func(h ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
			// Final response completes with no text content
			defer close(step2Finished)
			return providers.FauxAssistantMessage(ai.ContentList{}, ai.StopStop)
		}
		reg.SetResponses([]providers.FauxResponseStep{step1, step2, initialStep, initialStep})

		turnMsgID := "c4444444-4444-4444-8444-444444444444"
		_, err = db.Enqueue(ctx, protocol.Envelope{
			ID:        turnMsgID,
			From:      "supervisor",
			To:        launched.Agent.AgentID,
			Body:      "execute silently",
			CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		}, store.EnqueueOptions{})
		require.NoError(t, err)
		_, err = db.ResolveMessageTarget(ctx, turnMsgID)
		require.NoError(t, err)

		manager.OfferDirect(launched.Agent.AgentID)

		select {
		case <-step2Finished:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for silent step2 to finish")
		}

		require.Eventually(t, func() bool {
			a, e := db.ManagedAgent(ctx, launched.Agent.AgentID)
			return e == nil && a.ActivityState == "idle"
		}, 3*time.Second, 10*time.Millisecond)

		rawDB, err := sql.Open("sqlite", path)
		require.NoError(t, err)
		defer rawDB.Close()

		var fallbackCount int
		require.NoError(t, rawDB.QueryRowContext(ctx, `SELECT count(*) FROM embedded_reply_outbox WHERE body LIKE '%Action completed without additional text%'`).Scan(&fallbackCount))
		require.Zero(t, fallbackCount, "synthetic fallback text must never appear in outbox")
	})

	t.Run("ToolExecutionWithEmptyTextTriggersAnswerSynthesisNudge", func(t *testing.T) {
		initialStep := func(h ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "ready"}}, ai.StopStop)
		}
		reg.SetResponses([]providers.FauxResponseStep{initialStep, initialStep})

		manager, err := NewManager(ManagerOptions{
			Store:                        db,
			Manifest:                     manifest,
			Config:                       Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
			CatalogSource:                CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
			EmbeddedModelResolver:        func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
			CodexCredentialStore:         testfixture.CodexAdmission(t),
			EmbeddedMailBackoff:          10 * time.Millisecond,
		})
		require.NoError(t, err)
		defer manager.Close(ctx)

		launched, err := manager.Launch(ctx, LaunchRequest{
			Name: "nudge-reply-worker", Cwd: root, Runtime: "embedded",
			Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
			RequestID: "d1111111-1111-4111-8111-111111111111",
		})
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			a, e := db.ManagedAgent(ctx, launched.Agent.AgentID)
			return e == nil && a.LifecycleState == "running" && a.ActivityState == "idle"
		}, 3*time.Second, 10*time.Millisecond)

		step1 := func(h ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{
				providers.FauxToolCall("bash", map[string]any{"command": "echo check"}, "bash-call-nudge"),
			}, ai.StopToolUse)
		}
		step2 := func(h ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
			// Model returns only thinking and 0 text, simulating Gemini 3.8 Flash behavior
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.ThinkingContent{Thinking: "Investigated and concluded task in thoughts"},
			}, ai.StopStop)
		}
		var sawNudgePrompt atomic.Bool
		step3Finished := make(chan struct{})
		step3 := func(h ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
			defer close(step3Finished)
			for _, msg := range h.Messages {
				if userMsg, ok := msg.(ai.UserMessage); ok {
					for _, c := range userMsg.Content {
						if tc, ok := c.(ai.TextContent); ok && strings.Contains(tc.Text, "Please provide your direct conversational response and answer to the user") {
							sawNudgePrompt.Store(true)
						}
					}
				}
			}
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "Here is my synthesized conversational response after the nudge."},
			}, ai.StopStop)
		}
		reg.SetResponses([]providers.FauxResponseStep{step1, step2, step3, initialStep})

		err = manager.AdmitPromptWithRequestID(ctx, launched.Agent.AgentID, "run check and answer", "d2222222-2222-4222-8222-222222222222")
		require.NoError(t, err)

		select {
		case <-step3Finished:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for step3 to finish")
		}

		require.True(t, sawNudgePrompt.Load(), "model must receive the answer synthesis nudge prompt")
	})
}
