package managed

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/attachments"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func appendTestEntry(t *testing.T, s *store.Store, b store.EmbeddedBinding, message ai.Message) store.EmbeddedEntry {
	t.Helper()
	body, err := json.Marshal(message)
	require.NoError(t, err)
	entry, err := s.AppendEmbeddedEntry(context.Background(), b, string(message.MessageRole()), body)
	require.NoError(t, err)
	return entry
}

func TestEmbeddedSendImage(t *testing.T) {
	// Tiny 1x1 PNG bytes in base64
	const tinyPNGData = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	pngBytes, err := base64.StdEncoding.DecodeString(tinyPNGData)
	require.NoError(t, err)

	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))

	setupManagerAndTool := func(t *testing.T) (*Manager, *store.Store, *attachments.Store, embeddedOwner, string) {
		tempDir := t.TempDir()
		dbPath := filepath.Join(tempDir, "router.db")
		s, err := store.Open(ctx, dbPath, store.Options{Clock: clock})
		require.NoError(t, err)
		t.Cleanup(func() { _ = s.Close() })

		attDir := filepath.Join(tempDir, "attachments")
		attStore, err := attachments.New(attachments.Options{
			Directory: attDir,
			Clock:     clock,
			Referenced: func(id string) bool {
				referenced, _ := s.ConversationAttachmentReferenced(ctx, id)
				return referenced
			},
		})
		require.NoError(t, err)

		manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
		require.NoError(t, err)

		mgr, err := NewManager(ManagerOptions{
			Store:       s,
			Attachments: attStore,
			Manifest:    manifest,
			Clock:       clock,
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = mgr.Close(ctx) })

		binding := subagentTestParent(t, s, "image-test-agent")
		owner := embeddedOwner{binding: binding, identity: "fixture:" + binding.AgentID}

		return mgr, s, attStore, owner, tempDir
	}

	t.Run("schema rejection when neither path nor data_url is provided", func(t *testing.T) {
		mgr, _, _, owner, _ := setupManagerAndTool(t)
		tool := mgr.embeddedSendImageTool(owner, func(string) *store.EmbeddedToolInvocation {
			t.Fatal("unexpected invocation admission")
			return nil
		}, func(string, *store.EmbeddedToolInvocation) {})

		ownerCtx := context.WithValue(ctx, embeddedOwnerContextKey{}, owner)

		// Test empty map
		_, err := tool.Execute(ownerCtx, "call-1", map[string]any{}, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "at least one of 'path' or 'data_url' must be provided")

		// Test with only message and recipient but neither path nor data_url
		_, err = tool.Execute(ownerCtx, "call-2", map[string]any{
			"message":   "Here is an image",
			"recipient": "user",
		}, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "at least one of 'path' or 'data_url' must be provided")

		// Test with empty path and whitespace data_url
		_, err = tool.Execute(ownerCtx, "call-3", map[string]any{
			"path":     "   ",
			"data_url": "",
		}, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "at least one of 'path' or 'data_url' must be provided")
	})

	t.Run("rejection when recipient == 'system'", func(t *testing.T) {
		mgr, _, _, owner, _ := setupManagerAndTool(t)
		tool := mgr.embeddedSendImageTool(owner, func(string) *store.EmbeddedToolInvocation {
			t.Fatal("unexpected invocation admission")
			return nil
		}, func(string, *store.EmbeddedToolInvocation) {})

		ownerCtx := context.WithValue(ctx, embeddedOwnerContextKey{}, owner)

		for _, sysRecipient := range []string{"system", "System", "SYSTEM"} {
			_, err := tool.Execute(ownerCtx, "call-sys", map[string]any{
				"path":      "/path/to/img.png",
				"recipient": sysRecipient,
			}, nil)
			require.Error(t, err)
			require.Contains(t, err.Error(), "'system' is an automated system notifier and does not accept incoming messages.")
		}
	})

	t.Run("successful execution with a test PNG image on disk", func(t *testing.T) {
		mgr, s, _, owner, tempDir := setupManagerAndTool(t)

		// Write a small 1x1 PNG to disk
		imgPath := filepath.Join(tempDir, "test.png")
		require.NoError(t, os.WriteFile(imgPath, pngBytes, 0644))

		// Prepare a live invocation witness
		call := ai.ToolCall{
			ID:   "call-img-path",
			Name: "mecha_send_image",
			Arguments: map[string]any{
				"path":    imgPath,
				"message": "check this out",
			},
		}

		user := appendTestEntry(t, s, owner.binding, ai.UserMessage{
			Content: ai.ContentList{ai.TextContent{Text: "please send image"}},
		})
		require.NoError(t, s.EnterEmbeddedNonMailProviderTurn(ctx, owner.binding, user.Sequence, user.Digest))
		_ = appendTestEntry(t, s, owner.binding, ai.AssistantMessage{
			Content: ai.ContentList{call},
		})

		procGen, err := s.MintEmbeddedProcessGeneration(ctx, "test-proc")
		require.NoError(t, err)

		inv, err := s.RecordEmbeddedToolInvocation(ctx, owner.binding, procGen, user.Sequence, user.Digest, "", call)
		require.NoError(t, err)

		var admittedCalled, completedCalled bool
		tool := mgr.embeddedSendImageTool(owner, func(id string) *store.EmbeddedToolInvocation {
			require.Equal(t, call.ID, id)
			admittedCalled = true
			return inv
		}, func(id string, completedInv *store.EmbeddedToolInvocation) {
			require.Equal(t, call.ID, id)
			require.Equal(t, inv, completedInv)
			completedCalled = true
		})

		ownerCtx := context.WithValue(ctx, embeddedOwnerContextKey{}, owner)

		result, err := tool.Execute(ownerCtx, call.ID, call.Arguments, nil)
		require.NoError(t, err)
		require.True(t, admittedCalled)
		require.True(t, completedCalled)
		require.NotEmpty(t, result.Content)

		textContent, ok := result.Content[0].(ai.TextContent)
		require.True(t, ok)

		var resMap map[string]any
		require.NoError(t, json.Unmarshal([]byte(textContent.Text), &resMap))
		require.Equal(t, "accepted", resMap["status"])
		require.NotEmpty(t, resMap["message_id"])
		require.NotEmpty(t, resMap["attachment_id"])
		require.NotEmpty(t, resMap["url"])
		require.Equal(t, "user", resMap["recipient"])

		// Verify message was enqueued in store
		msgID, ok := resMap["message_id"].(string)
		require.True(t, ok)
		history, err := s.ConversationHistory(ctx, owner.binding.AgentID, "user")
		require.NoError(t, err)
		require.NotEmpty(t, history)

		var found bool
		for _, msg := range history {
			if msg.ID == msgID {
				found = true
				require.Equal(t, owner.binding.AgentID, msg.From)
				require.Equal(t, "user", msg.To)
				require.Contains(t, msg.Body, "check this out")
				require.Contains(t, msg.Body, "Attached image:")
				break
			}
		}
		require.True(t, found, "enqueued message must be present in conversation history")

		// Verify conversation attachment is referenced
		attID, ok := resMap["attachment_id"].(string)
		require.True(t, ok)
		ref, err := s.ConversationAttachmentReferenced(ctx, attID)
		require.NoError(t, err)
		require.True(t, ref)
	})

	t.Run("successful execution with a valid data_url", func(t *testing.T) {
		mgr, s, _, owner, _ := setupManagerAndTool(t)

		dataURL := "data:image/png;base64," + tinyPNGData
		call := ai.ToolCall{
			ID:   "call-img-dataurl",
			Name: "mecha_send_image",
			Arguments: map[string]any{
				"data_url":  dataURL,
				"message":   "data url image",
				"recipient": "other-agent",
			},
		}

		user := appendTestEntry(t, s, owner.binding, ai.UserMessage{
			Content: ai.ContentList{ai.TextContent{Text: "please send data url image"}},
		})
		require.NoError(t, s.EnterEmbeddedNonMailProviderTurn(ctx, owner.binding, user.Sequence, user.Digest))
		_ = appendTestEntry(t, s, owner.binding, ai.AssistantMessage{
			Content: ai.ContentList{call},
		})

		procGen, err := s.MintEmbeddedProcessGeneration(ctx, "test-proc-2")
		require.NoError(t, err)

		inv, err := s.RecordEmbeddedToolInvocation(ctx, owner.binding, procGen, user.Sequence, user.Digest, "", call)
		require.NoError(t, err)

		var admittedCalled, completedCalled bool
		tool := mgr.embeddedSendImageTool(owner, func(id string) *store.EmbeddedToolInvocation {
			require.Equal(t, call.ID, id)
			admittedCalled = true
			return inv
		}, func(id string, completedInv *store.EmbeddedToolInvocation) {
			require.Equal(t, call.ID, id)
			require.Equal(t, inv, completedInv)
			completedCalled = true
		})

		ownerCtx := context.WithValue(ctx, embeddedOwnerContextKey{}, owner)

		result, err := tool.Execute(ownerCtx, call.ID, call.Arguments, nil)
		require.NoError(t, err)
		require.True(t, admittedCalled)
		require.True(t, completedCalled)
		require.NotEmpty(t, result.Content)

		textContent, ok := result.Content[0].(ai.TextContent)
		require.True(t, ok)

		var resMap map[string]any
		require.NoError(t, json.Unmarshal([]byte(textContent.Text), &resMap))
		require.Equal(t, "accepted", resMap["status"])
		require.NotEmpty(t, resMap["message_id"])
		require.NotEmpty(t, resMap["attachment_id"])
		require.NotEmpty(t, resMap["url"])
		require.Equal(t, "other-agent", resMap["recipient"])

		// Verify message was enqueued in store
		msgID, ok := resMap["message_id"].(string)
		require.True(t, ok)
		history, err := s.ConversationHistory(ctx, owner.binding.AgentID, "other-agent")
		require.NoError(t, err)
		require.NotEmpty(t, history)

		var found bool
		for _, msg := range history {
			if msg.ID == msgID {
				found = true
				require.Equal(t, owner.binding.AgentID, msg.From)
				require.Equal(t, "other-agent", msg.To)
				require.Contains(t, msg.Body, "data url image")
				require.Contains(t, msg.Body, "Attached image:")
				break
			}
		}
		require.True(t, found, "enqueued message must be present in conversation history")

		// Verify conversation attachment is referenced
		attID, ok := resMap["attachment_id"].(string)
		require.True(t, ok)
		ref, err := s.ConversationAttachmentReferenced(ctx, attID)
		require.NoError(t, err)
		require.True(t, ref)
	})
}
