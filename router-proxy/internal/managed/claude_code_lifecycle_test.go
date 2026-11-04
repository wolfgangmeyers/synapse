package managed

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestClaudeSeedUnicodeOrderAndNewestWholeTurn(t *testing.T) {
	oldest := "OLDEST:" + strings.Repeat("古🦊", 60000)
	newest := "NEWEST:" + strings.Repeat("新🧭", 60000) + ":TAIL"
	history := []agent.AgentMessage{
		ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "old request"}}},
		ai.AssistantMessage{Content: ai.ContentList{ai.TextContent{Text: oldest}}},
		ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "new request"}}},
		ai.AssistantMessage{Content: ai.ContentList{ai.TextContent{Text: newest}}},
	}
	seed, err := claudeCodeSeed("instruction 🧭", "note 紫", history, "wake", 200000)
	require.NoError(t, err)
	require.True(t, utf8.ValidString(seed))
	require.LessOrEqual(t, len([]rune(seed)), 200000)
	require.NotContains(t, seed, "OLDEST:")
	require.Contains(t, seed, newest)
	require.Less(t, strings.Index(seed, "instruction"), strings.Index(seed, "note"))
	require.Less(t, strings.Index(seed, "note"), strings.Index(seed, "NEWEST:"))
	require.NotContains(t, seed, "wake")
}

func TestClaudeRuntimeResumeMissingReseedsOnceBeforeConsumption(t *testing.T) {
	r, p, pool, cfg := claudeRuntimeFixture(t, `{"turns":[[{"text":"remember jade 玉"}]]}`)
	_, err := r.Run(context.Background(), "original instruction 🧭")
	require.NoError(t, err)
	r.Close()
	pool.stop(p.binding.AgentID)
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Cwd, "script.json"), []byte(`{"resume_missing":true,"turns":[[{"text":"reseed complete"}]]}`), 0600))
	next, err := newClaudeCodeRuntime(context.Background(), claudeCodeRuntimeOptions{Pool: pool, Config: cfg, Persistence: p, BeforeSubmit: r.beforeSubmit, AfterEcho: r.afterEcho})
	require.NoError(t, err)
	defer next.Close()
	_, err = next.Run(context.Background(), "next input")
	require.NoError(t, err)
	require.Equal(t, "reseed complete", next.Snapshot().LastAssistantText)
	data, err := os.ReadFile(filepath.Join(cfg.Cwd, "record.jsonl"))
	require.NoError(t, err)
	starts := 0
	resumes := 0
	users := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var row struct {
			Kind string
			Argv []string
			Line string
		}
		require.NoError(t, json.Unmarshal([]byte(line), &row))
		if row.Kind == "start" && len(row.Argv) > 0 && row.Argv[0] == "-p" {
			starts++
			for _, arg := range row.Argv {
				if arg == "--resume" {
					resumes++
				}
			}
		}
		if row.Kind == "stdin" && strings.Contains(row.Line, `"type":"user"`) {
			users++
			if users == 2 {
				require.Contains(t, row.Line, "remember jade 玉")
				require.Contains(t, row.Line, "next input")
			}
		}
	}
	require.Equal(t, 3, starts)
	require.Equal(t, 1, resumes)
	require.Equal(t, 2, users)
}

func TestClaudeMissingTranscriptUsesFreshHeadroom(t *testing.T) {
	for _, missing := range []bool{true, false} {
		t.Run(fmt.Sprintf("missing=%t", missing), func(t *testing.T) {
			ctx := context.Background()
			r, p, pool, cfg := claudeRuntimeFixture(t, `{}`)
			_, err := r.Run(ctx, "old input")
			require.NoError(t, err)
			require.NoError(t, p.AppendCheckedMessage(ctx, ai.AssistantMessage{
				Content: ai.ContentList{ai.TextContent{Text: "old history"}},
				Usage:   ai.Usage{Input: 990000}, StopReason: ai.StopStop,
			}))
			r.Close()
			pool.stop(p.binding.AgentID)
			script := fmt.Sprintf(`{"resume_missing":%t}`, missing)
			require.NoError(t, os.WriteFile(filepath.Join(cfg.Cwd, "script.json"), []byte(script), 0600))
			next, err := newClaudeCodeRuntime(ctx, claudeCodeRuntimeOptions{Pool: pool, Config: cfg, Persistence: p, BeforeSubmit: r.beforeSubmit, AfterEcho: r.afterEcho})
			require.NoError(t, err)
			defer next.Close()
			require.Equal(t, 990000, next.Snapshot().ContextTokens)
			_, err = next.Run(ctx, "new input")
			if missing {
				require.NoError(t, err)
				data, readErr := os.ReadFile(filepath.Join(cfg.Cwd, "record.jsonl"))
				require.NoError(t, readErr)
				require.Contains(t, string(data), "old history")
			} else {
				require.ErrorContains(t, err, "headroom")
			}
		})
	}
}

func TestClaudeRuntimeUnicodeSeedFromCheckedStore(t *testing.T) {
	r, p, pool, cfg := claudeRuntimeFixture(t, `{}`)
	ctx := context.Background()
	oldest := "OLDEST_DROP_CANDIDATE:" + strings.Repeat("古🦊", 60000)
	newest := "NEWEST_PRESERVE:" + strings.Repeat("新🧭", 60000) + ":TAIL_SENTINEL"
	for _, message := range []agent.AgentMessage{
		ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "old request"}}},
		ai.AssistantMessage{Content: ai.ContentList{ai.TextContent{Text: oldest}}, StopReason: ai.StopStop},
		ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "new request"}}},
		ai.AssistantMessage{Content: ai.ContentList{ai.TextContent{Text: newest}}, StopReason: ai.StopStop},
	} {
		body, err := json.Marshal(message)
		require.NoError(t, err)
		_, err = p.db.AppendEmbeddedEntry(ctx, p.binding, string(message.MessageRole()), body)
		require.NoError(t, err)
	}
	next, err := newClaudeCodeRuntime(ctx, claudeCodeRuntimeOptions{Pool: pool, Config: cfg, Persistence: p, BeforeSubmit: r.beforeSubmit, AfterEcho: r.afterEcho})
	require.NoError(t, err)
	defer next.Close()
	next.instruction = "Keep instruction 🧭"
	next.notes = "Scratch memory 紫"
	_, err = next.Run(ctx, "Context reset completed")
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(cfg.Cwd, "record.jsonl"))
	require.NoError(t, err)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var row struct {
			Kind string
			Line string
		}
		require.NoError(t, json.Unmarshal([]byte(line), &row))
		if row.Kind != "stdin" {
			continue
		}
		var input struct {
			Type    string
			Message struct{ Content []struct{ Text string } }
		}
		require.NoError(t, json.Unmarshal([]byte(row.Line), &input))
		if input.Type != "user" {
			continue
		}
		text := input.Message.Content[0].Text
		require.LessOrEqual(t, len([]rune(text)), 200000)
		require.True(t, utf8.ValidString(text))
		require.Contains(t, text, newest)
		require.NotContains(t, text, "OLDEST_DROP_CANDIDATE:")
		require.GreaterOrEqual(t, len(input.Message.Content), 2)
		require.Equal(t, "Context reset completed", input.Message.Content[1].Text)
	}
}

func TestClaudeSeedRequiresMandatoryContextAndWake(t *testing.T) {
	history := []agent.AgentMessage{ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "old"}}}}
	seed, err := claudeCodeSeed(strings.Repeat("紫", 1000), "small note", history, strings.Repeat("🧭", 199000), 200000)
	require.ErrorContains(t, err, "exceed")
	require.Empty(t, seed)
	seed, err = claudeCodeSeed("instruction", "notes", history, strings.Repeat("🧭", 199800), 200000)
	require.NoError(t, err)
	require.Contains(t, seed, "instruction")
	require.Contains(t, seed, "notes")
}

func TestClaudeSeedDropsWholeOversizedToolTurn(t *testing.T) {
	history := []agent.AgentMessage{
		ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "old request"}}},
		ai.ToolResultMessage{Content: ai.ContentList{ai.TextContent{Text: strings.Repeat("🦊", 10000) + "TOOL_TAIL"}}},
		ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "new request"}}},
	}
	seed, err := claudeCodeSeed("instruction", "notes", history, "wake", 200)
	require.NoError(t, err)
	require.NotContains(t, seed, "old request")
	require.NotContains(t, seed, "🦊")
	require.Contains(t, seed, "new request")
}

func TestClaudeFreshSeedRendersPublicMailEnvelope(t *testing.T) {
	r, _, _, cfg := claudeRuntimeFixture(t, `{}`)
	defer r.Close()
	r.seedHistory = []agent.AgentMessage{ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "previous"}}}}
	envelope, err := json.Marshal(map[string]string{"Version": "embedded-mail/v1", "Sender": "alice", "Body": "hello 🧭\nEND"})
	require.NoError(t, err)
	_, err = r.RunMessages(context.Background(), []agent.AgentMessage{ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: string(envelope)}}}})
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(cfg.Cwd, "record.jsonl"))
	require.NoError(t, err)
	require.Contains(t, string(data), "[Direct message from alice]: hello 🧭")
	require.NotContains(t, string(data), "embedded-mail/v1")
}

func TestClaudeRuntimeFailedFreshRecoveryDoesNotConsume(t *testing.T) {
	r, p, pool, cfg := claudeRuntimeFixture(t, `{}`)
	_, err := r.Run(context.Background(), "first")
	require.NoError(t, err)
	r.Close()
	pool.stop(p.binding.AgentID)
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Cwd, "script.json"), []byte(`{"resume_missing":true,"startup":[{"crash":2}]}`), 0600))
	next, err := newClaudeCodeRuntime(context.Background(), claudeCodeRuntimeOptions{Pool: pool, Config: cfg, Persistence: p, BeforeSubmit: r.beforeSubmit, AfterEcho: r.afterEcho})
	require.NoError(t, err)
	defer next.Close()
	before, err := p.db.ReopenEmbedded(context.Background(), p.binding)
	require.NoError(t, err)
	_, err = next.Run(context.Background(), "must stay unconsumed")
	require.Error(t, err)
	after, err := p.db.ReopenEmbedded(context.Background(), p.binding)
	require.NoError(t, err)
	require.Len(t, after.Entries, len(before.Entries))
	data, err := os.ReadFile(filepath.Join(cfg.Cwd, "record.jsonl"))
	require.NoError(t, err)
	starts := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var row struct {
			Kind string
			Argv []string
			Line string
		}
		require.NoError(t, json.Unmarshal([]byte(line), &row))
		if row.Kind == "start" && len(row.Argv) > 0 && row.Argv[0] == "-p" {
			starts++
		}
		if row.Kind == "stdin" {
			require.NotContains(t, row.Line, "must stay unconsumed")
		}
	}
	require.Equal(t, 3, starts, "exactly one fresh fallback, no loop")
}

func TestClaudeFreshSeedRejectsOversizedWake(t *testing.T) {
	r, p, _, cfg := claudeRuntimeFixture(t, `{}`)
	defer r.Close()
	r.seedHistory = []agent.AgentMessage{ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "previous"}}}}
	_, err := r.Run(context.Background(), strings.Repeat("🧭", 200001))
	require.ErrorContains(t, err, "exceed")
	snap, err := p.db.ReopenEmbedded(context.Background(), p.binding)
	require.NoError(t, err)
	require.Empty(t, snap.Entries)
	data, err := os.ReadFile(filepath.Join(cfg.Cwd, "record.jsonl"))
	require.NoError(t, err)
	require.NotContains(t, string(data), `\"type\":\"user\"`)
}
