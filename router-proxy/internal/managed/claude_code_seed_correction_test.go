package managed

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestClaudeSeedMixedWakeOrder(t *testing.T) {
	r, _, _, cfg := claudeRuntimeFixture(t, `{}`)
	r.seedHistory = []agent.AgentMessage{ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "history"}}}}
	r.instruction, r.notes = "mandatory instruction", "mandatory note"
	content := ai.ContentList{ai.TextContent{Text: "first"}, ai.ImageContent{Data: "eA==", MimeType: "image/png"}, ai.TextContent{Text: "last"}}
	_, err := r.RunMessages(context.Background(), []agent.AgentMessage{ai.UserMessage{Content: content}})
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(cfg.Cwd, "record.jsonl"))
	require.NoError(t, err)
	found := false
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var row struct{ Kind, Line string }
		require.NoError(t, json.Unmarshal([]byte(line), &row))
		if row.Kind != "stdin" {
			continue
		}
		var input struct {
			Type    string
			Message struct{ Content []struct{ Type, Text string } }
		}
		require.NoError(t, json.Unmarshal([]byte(row.Line), &input))
		if input.Type != "user" {
			continue
		}
		require.Len(t, input.Message.Content, 4)
		require.Contains(t, input.Message.Content[0].Text, "mandatory instruction")
		require.Contains(t, input.Message.Content[0].Text, "mandatory note")
		require.Equal(t, "first", input.Message.Content[1].Text)
		require.Equal(t, "image", input.Message.Content[2].Type)
		require.Equal(t, "last", input.Message.Content[3].Text)
		found = true
	}
	require.True(t, found)
}

func TestClaudeSeedRejectedMailRemainsEligible(t *testing.T) {
	ctx := context.Background()
	r, p, _, cfg := claudeRuntimeFixture(t, `{}`)
	require.NoError(t, p.db.ReconcileManagedRecipientSources(ctx))
	_, err := p.db.Enqueue(ctx, protocol.Envelope{ID: "seed-reject", From: "sender", To: p.binding.AgentID, Body: "retry safely", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")}, store.EnqueueOptions{})
	require.NoError(t, err)
	_, err = p.db.ResolveMessageTarget(ctx, "seed-reject")
	require.NoError(t, err)
	attempt, err := p.db.ReserveEmbeddedMail(ctx, p.binding, "seed-reject")
	require.NoError(t, err)
	p.mail = attempt
	r.seedHistory = []agent.AgentMessage{ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "history"}}}}
	r.instruction = strings.Repeat("🧭", 200000)
	user, err := store.EmbeddedMailUserMessage(attempt, time.Now().UnixMilli())
	require.NoError(t, err)
	_, err = r.RunMessages(ctx, []agent.AgentMessage{user})
	require.ErrorContains(t, err, "exceed")
	snap, err := p.db.ReopenEmbedded(ctx, p.binding)
	require.NoError(t, err)
	require.Empty(t, snap.Entries)
	data, err := os.ReadFile(filepath.Join(cfg.Cwd, "record.jsonl"))
	require.NoError(t, err)
	require.NotContains(t, string(data), `\"type\":\"user\"`)
	held, err := p.db.ClaudeMailHeld(ctx, attempt.MessageID)
	require.NoError(t, err)
	require.False(t, held)
	require.NoError(t, p.db.ReleaseEmbeddedUnconsumedAttempt(ctx, p.binding, attempt.AttemptID))
	id, err := p.db.NextEmbeddedMail(ctx, p.binding.AgentID)
	require.NoError(t, err)
	require.Equal(t, attempt.MessageID, id)
}

func TestClaudeSeedRejectsRenderedBoundsWithoutWakeFallback(t *testing.T) {
	for _, mode := range []string{"rendered", "headroom"} {
		t.Run(mode, func(t *testing.T) {
			r, p, _, cfg := claudeRuntimeFixture(t, `{}`)
			r.seedHistory = []agent.AgentMessage{ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "history"}}}}
			r.instruction = strings.Repeat("\x01", 190000)
			content := ai.ContentList{ai.TextContent{Text: "wake"}}
			if mode == "rendered" {
				r.snapshot.ContextWindow = 10000000
				content = append(content, ai.ImageContent{Data: strings.Repeat("A", 5<<20), MimeType: "image/png"})
			} else {
				r.snapshot.ContextWindow = 200000
				r.snapshot.ContextTokens = 180000
			}
			_, err := r.RunMessages(context.Background(), []agent.AgentMessage{ai.UserMessage{Content: content}})
			if mode == "rendered" {
				require.ErrorContains(t, err, "6 MiB")
			} else {
				require.ErrorContains(t, err, "headroom")
			}
			snap, err := p.db.ReopenEmbedded(context.Background(), p.binding)
			require.NoError(t, err)
			require.Empty(t, snap.Entries)
			data, err := os.ReadFile(filepath.Join(cfg.Cwd, "record.jsonl"))
			require.NoError(t, err)
			require.NotContains(t, string(data), `\"type\":\"user\"`)
		})
	}
}

func TestClaudeSeedEmptyHistoryStillIncludesMandatoryContext(t *testing.T) {
	r, _, _, cfg := claudeRuntimeFixture(t, `{}`)
	r.seedHistory = nil
	r.instruction, r.notes = "empty-history instruction", "[Pinned] retained note"
	_, err := r.Run(context.Background(), "wake")
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(cfg.Cwd, "record.jsonl"))
	require.NoError(t, err)
	require.Contains(t, string(data), "empty-history instruction")
	require.Contains(t, string(data), "[Pinned] retained note")
}

func TestClaudeSeedHistoryContainsCompleteStructuredMessages(t *testing.T) {
	history := []agent.AgentMessage{
		ai.UserMessage{Content: ai.ContentList{ai.ImageContent{Data: "YWJj", MimeType: "image/png"}}},
		ai.AssistantMessage{Content: ai.ContentList{ai.ToolCall{ID: "call1", Name: "Read", Arguments: map[string]any{"path": "unique-file"}}}},
		ai.ToolResultMessage{Content: ai.ContentList{ai.TextContent{Text: "complete result"}}},
	}
	seed, err := claudeCodeSeed("", "", history, "wake", 10000)
	require.NoError(t, err)
	for _, part := range []string{"YWJj", "image/png", "unique-file", "complete result"} {
		require.Contains(t, seed, part)
	}
	seed, err = claudeCodeSeed("", "", history, "wake", 100)
	require.NoError(t, err)
	for _, part := range []string{"YWJj", "unique-file", "complete result"} {
		require.NotContains(t, seed, part)
	}
}

func TestClaudeSeedDispatcherRejectsWithoutDeadLetter(t *testing.T) {
	ctx := context.Background()
	cfg := claudeTestConfig(t, `{}`)
	binDir := t.TempDir()
	require.NoError(t, os.Symlink(cfg.Binary, filepath.Join(binDir, "claude")))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_CLAUDE_SCRIPT", filepath.Join(cfg.Cwd, "script.json"))
	t.Setenv("FAKE_CLAUDE_RECORD", filepath.Join(cfg.Cwd, "record.jsonl"))
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	f.m.startWG.Wait()
	_, err := f.raw.Exec(`UPDATE managed_agents SET selected_model_provider=?, selected_model_id=?, pending_initial_instruction=? WHERE agent_id=?`, claudeCodeProvider, "sonnet", strings.Repeat("🧭", 200000), owner.binding.AgentID)
	require.NoError(t, err)
	id := "e7777777-7777-4777-8777-777777777777"
	f.enqueue(t, id)
	for i := 0; i < 4; i++ {
		f.m.dispatchEmbeddedMail(owner.binding.AgentID)
		next, err := f.db.NextEmbeddedMail(ctx, owner.binding.AgentID)
		require.NoError(t, err)
		require.Equal(t, id, next)
		require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_acks WHERE message_id=?`, id))
		require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_attempts WHERE message_id=?`, id))
	}
	data, err := os.ReadFile(filepath.Join(cfg.Cwd, "record.jsonl"))
	require.NoError(t, err)
	require.NotContains(t, string(data), `\"type\":\"user\"`)
}
