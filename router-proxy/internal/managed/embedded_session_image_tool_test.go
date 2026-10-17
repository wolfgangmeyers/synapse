package managed_test

import (
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// TestEmbeddedSession_ReadImageToolTurn proves that embedded agent sessions
// complete multi-step turns when invoking the `read` tool on image files.
//
// The test verifies:
//  1. Turn 1 (Assistant): calls `read` with the path to a 1x1 PNG.
//  2. The tool executes, reads the PNG, and returns text and an ai.ImageContent attachment.
//  3. Turn 2 (Assistant): receives the updated conversation transcript containing the
//     image tool result, does not terminate early, and returns the final assistant text
//     "I have read the image successfully." with ai.StopStop.
//  4. Inbound mailbox message triggers the turn.
//  5. The session settles back to "idle".
//  6. An outbox reply message is recorded with the final assistant response text.
//  7. The persisted transcript (embedded_entries) contains the user message, tool call,
//     tool result with ImageContent, and final assistant message.
func TestEmbeddedSession_ReadImageToolTurn(t *testing.T) {
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{
		Models: []providers.FauxModelDefinition{{
			ID:    "gemini-3.8-flash",
			Input: []string{"text", "image"},
		}},
	})
	defer reg.Unregister()

	// 1x1 transparent PNG bytes
	const tinyPNGData = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	pngBytes, err := base64.StdEncoding.DecodeString(tinyPNGData)
	require.NoError(t, err)

	var turn1Called atomic.Bool
	var turn2Called atomic.Bool
	var turn2SawImageContent atomic.Bool

	const imageFilename = "test_image.png"
	const expectedFinalText = "I have read the image successfully."

	// Turn 1: Assistant calls the `read` tool with the PNG file path.
	step1 := func(tc ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		turn1Called.Store(true)
		return providers.FauxAssistantMessage(ai.ContentList{
			providers.FauxToolCall("read", map[string]any{"path": imageFilename}, "call-read-img-1"),
		}, ai.StopToolUse)
	}

	// Turn 2: Assistant receives the updated conversation transcript containing the image tool result.
	step2 := func(tc ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		turn2Called.Store(true)
		for _, msg := range tc.Messages {
			if tr, ok := msg.(ai.ToolResultMessage); ok {
				for _, c := range tr.Content {
					if img, ok := c.(ai.ImageContent); ok && len(img.Data) > 0 {
						turn2SawImageContent.Store(true)
					}
				}
			}
		}
		return providers.FauxAssistantMessage(ai.ContentList{
			ai.TextContent{Text: expectedFinalText},
		}, ai.StopStop)
	}

	reg.SetResponses([]providers.FauxResponseStep{step1, step2})

	h := newBehavioralHarness(t, func(rec store.ManagedAgentRecord) *ai.Model {
		return reg.GetModel()
	})

	// Write real tiny 1x1 PNG file to a known path in the harness root (agent Cwd)
	imagePath := filepath.Join(h.root, imageFilename)
	require.NoError(t, os.WriteFile(imagePath, pngBytes, 0644))

	launched, err := h.manager.Launch(h.ctx, managed.LaunchRequest{
		Name:      "image-reader-worker",
		Cwd:       h.root,
		Runtime:   "embedded",
		Model:     &managed.Model{Provider: "google-vertex", ID: "gemini-3.8-flash"},
		RequestID: "11111111-2222-3333-4444-555555555599",
	})
	require.NoError(t, err)

	// Wait for agent to reach running and idle state
	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.LifecycleState == "running" && agent.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	// Send inbound mailbox message to trigger the turn
	status := h.postMessage(t, "msg-read-img-1", "user-requester", launched.Agent.AgentID, "Please inspect test_image.png")
	require.Equal(t, http.StatusCreated, status)

	// Wait for worker to finish both turns and settle back in idle state
	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.LifecycleState == "running" && agent.ActivityState == "idle" && turn2Called.Load()
	}, 5*time.Second, 20*time.Millisecond)

	require.True(t, turn1Called.Load(), "turn 1 assistant tool call must be invoked")
	require.True(t, turn2Called.Load(), "turn 2 assistant response must be called (loop did not terminate early)")
	require.True(t, turn2SawImageContent.Load(), "turn 2 transcript must contain image tool result with ImageContent")

	// Verify an outbox reply message is emitted with the assistant's final response text
	rawDB := h.sqlDB(t)
	var replyBody string
	require.Eventually(t, func() bool {
		err := rawDB.QueryRowContext(h.ctx, `SELECT body FROM embedded_reply_outbox WHERE sender=? ORDER BY rowid DESC LIMIT 1`, launched.Agent.AgentID).Scan(&replyBody)
		return err == nil && strings.Contains(replyBody, expectedFinalText)
	}, 3*time.Second, 20*time.Millisecond, "outbox reply must contain assistant's final response text")

	// Verify transcript in embedded_entries contains user message, tool call, tool result with image content, and final assistant response
	rows, err := rawDB.QueryContext(h.ctx, `SELECT seq, kind, body FROM embedded_entries WHERE agent_id=? ORDER BY seq ASC`, launched.Agent.AgentID)
	require.NoError(t, err)
	defer rows.Close()

	var sawUser bool
	var sawToolCall bool
	var sawToolResultWithImage bool
	var sawFinalAssistant bool

	for rows.Next() {
		var seq int64
		var kind string
		var rawBody []byte
		require.NoError(t, rows.Scan(&seq, &kind, &rawBody))

		msg, err := ai.UnmarshalMessage(rawBody)
		require.NoError(t, err, "persisted entry body must unmarshal as ai.Message")

		switch kind {
		case "user":
			if u, ok := msg.(ai.UserMessage); ok {
				for _, c := range u.Content {
					if txt, ok := c.(ai.TextContent); ok && strings.Contains(txt.Text, "test_image.png") {
						sawUser = true
					}
				}
			}
		case "assistant":
			if asst, ok := msg.(ai.AssistantMessage); ok {
				for _, c := range asst.Content {
					if tc, ok := c.(ai.ToolCall); ok && tc.Name == "read" {
						sawToolCall = true
					}
					if txt, ok := c.(ai.TextContent); ok && strings.Contains(txt.Text, expectedFinalText) {
						sawFinalAssistant = true
					}
				}
			}
		case "toolResult":
			if tr, ok := msg.(ai.ToolResultMessage); ok && tr.ToolName == "read" {
				for _, c := range tr.Content {
					if img, ok := c.(ai.ImageContent); ok && len(img.Data) > 0 {
						sawToolResultWithImage = true
					}
				}
			}
		}
	}

	require.True(t, sawUser, "transcript must contain inbound user message")
	require.True(t, sawToolCall, "transcript must contain assistant's read tool call")
	require.True(t, sawToolResultWithImage, "transcript must contain tool result with image content")
	require.True(t, sawFinalAssistant, "transcript must contain final assistant response")
}
