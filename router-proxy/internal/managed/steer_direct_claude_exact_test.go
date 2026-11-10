package managed

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestClaudeDirectSteerExactPromptNoMailEnvelope(t *testing.T) {
	db, b, _, _ := wakePersistenceFixture(t)
	stdin := &steerRecordStdin{}
	proc := &ClaudeCodeProcess{stdin: stdin, inputs: make(map[string]claudeCodePendingInput), done: make(chan struct{}), closeDone: make(chan struct{}), notify: make(chan struct{}, 1)}
	r := &claudeCodeRuntime{p: &embeddedPersistence{db: db, binding: b, digest: "genesis"}, process: proc, beforeSubmit: func(context.Context, int) error { return nil }, active: true, steerOpen: true, snapshot: embeddedSnapshot{ContextWindow: 1000000}}
	prompt := "direct instruction: keep this literal"
	outcome, err := r.steerInput(context.Background(), ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: prompt}}, Timestamp: time.Now().UnixMilli()}, nil, "direct-exact")
	require.NoError(t, err)
	require.Equal(t, claudeSteerAccepted, outcome)
	require.Len(t, stdin.writes, 1)
	var wire struct {
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	require.NoError(t, json.Unmarshal(stdin.writes[0], &wire))
	require.Len(t, wire.Message.Content, 1)
	require.Equal(t, "text", wire.Message.Content[0].Type)
	require.Equal(t, prompt, wire.Message.Content[0].Text)
}
