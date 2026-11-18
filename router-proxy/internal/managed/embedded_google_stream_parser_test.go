package managed

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestScanGeminiSSEStream_CapturesThoughtSignature(t *testing.T) {
	t.Run("camelCase thoughtSignature", func(t *testing.T) {
		ssePayload := `data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"foo.txt"}},"thoughtSignature":"sig_camel_123"}]},"finishReason":"STOP"}]}` + "\n\n"

		stream := ai.NewAssistantMessageEventStream()
		go func() {
			for range stream.Events() {
			}
		}()

		msg, err := scanGeminiSSEStream(strings.NewReader(ssePayload), func(name string) string { return "call_1" }, stream)
		require.NoError(t, err)
		require.NotNil(t, msg)
		require.Len(t, msg.Content, 1)

		tc, ok := msg.Content[0].(ai.ToolCall)
		require.True(t, ok)
		require.Equal(t, "call_1", tc.ID)
		require.Equal(t, "read", tc.Name)
		require.Equal(t, "sig_camel_123", tc.ThoughtSignature)
	})

	t.Run("snake_case thought_signature", func(t *testing.T) {
		ssePayload := `data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"write","args":{"path":"bar.txt"}},"thought_signature":"sig_snake_456"}]},"finishReason":"STOP"}]}` + "\n\n"

		stream := ai.NewAssistantMessageEventStream()
		go func() {
			for range stream.Events() {
			}
		}()

		msg, err := scanGeminiSSEStream(strings.NewReader(ssePayload), func(name string) string { return "call_2" }, stream)
		require.NoError(t, err)
		require.NotNil(t, msg)
		require.Len(t, msg.Content, 1)

		tc, ok := msg.Content[0].(ai.ToolCall)
		require.True(t, ok)
		require.Equal(t, "call_2", tc.ID)
		require.Equal(t, "write", tc.Name)
		require.Equal(t, "sig_snake_456", tc.ThoughtSignature)
	})

	t.Run("event stream carries thoughtSignature", func(t *testing.T) {
		ssePayload := `data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"bash","args":{"command":"ls"}},"thoughtSignature":"sig_event_789"}]},"finishReason":"STOP"}]}` + "\n\n"

		stream := ai.NewAssistantMessageEventStream()
		var toolCallStart *ai.ToolCall
		var toolCallEnd *ai.ToolCall
		done := make(chan struct{})
		go func() {
			defer close(done)
			for ev := range stream.Events() {
				if ev.Type == ai.EventToolCallStart {
					toolCallStart = ev.ToolCall
				}
				if ev.Type == ai.EventToolCallEnd {
					toolCallEnd = ev.ToolCall
				}
			}
		}()

		msg, err := scanGeminiSSEStream(strings.NewReader(ssePayload), func(name string) string { return "call_3" }, stream)
		stream.End()
		<-done

		require.NoError(t, err)
		require.NotNil(t, msg)
		require.NotNil(t, toolCallStart)
		require.Equal(t, "sig_event_789", toolCallStart.ThoughtSignature)
		require.NotNil(t, toolCallEnd)
		require.Equal(t, "sig_event_789", toolCallEnd.ThoughtSignature)
	})

	t.Run("empty SSE response returns AssistantMessage with StopStop and no error", func(t *testing.T) {
		// Empty parts, finishReason STOP, empty candidates or empty chunks
		ssePayload := `data: {"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}]}` + "\n\n" +
			`data: {"candidates":[]}` + "\n\n"

		stream := ai.NewAssistantMessageEventStream()
		go func() {
			for range stream.Events() {
			}
		}()

		msg, err := scanGeminiSSEStream(strings.NewReader(ssePayload), nil, stream)
		require.NoError(t, err)
		require.NotNil(t, msg)
		require.Empty(t, msg.Content)
		require.Equal(t, ai.StopStop, msg.StopReason)
	})

	t.Run("tool call with empty or missing thoughtSignature leaves tc.ThoughtSignature empty without error", func(t *testing.T) {
		ssePayload := `data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"bash","args":{"command":"pwd"}}}]},"finishReason":"STOP"}]}` + "\n\n"

		stream := ai.NewAssistantMessageEventStream()
		go func() {
			for range stream.Events() {
			}
		}()

		msg, err := scanGeminiSSEStream(strings.NewReader(ssePayload), func(name string) string { return "call_empty_sig" }, stream)
		require.NoError(t, err)
		require.NotNil(t, msg)
		require.Len(t, msg.Content, 1)

		tc, ok := msg.Content[0].(ai.ToolCall)
		require.True(t, ok)
		require.Equal(t, "call_empty_sig", tc.ID)
		require.Equal(t, "bash", tc.Name)
		require.Equal(t, "", tc.ThoughtSignature)
		require.Equal(t, ai.StopToolUse, msg.StopReason)
	})

	t.Run("thinking block followed by functionCall with thoughtSignature", func(t *testing.T) {
		// Stream emitting a thought block first, then a functionCall with thoughtSignature
		ssePayload := `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Thinking through the problem...","thought":true}]}}]}` + "\n\n" +
			`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"data.json"}},"thoughtSignature":"sig_after_thought_1"}]},"finishReason":"STOP"}]}` + "\n\n"

		stream := ai.NewAssistantMessageEventStream()
		var receivedThinkingEnd bool
		var thinkingContent string
		done := make(chan struct{})
		go func() {
			defer close(done)
			for ev := range stream.Events() {
				if ev.Type == ai.EventThinkingEnd {
					receivedThinkingEnd = true
					thinkingContent = ev.Content
				}
			}
		}()

		msg, err := scanGeminiSSEStream(strings.NewReader(ssePayload), func(name string) string { return "call_after_thought" }, stream)
		stream.End()
		<-done

		require.NoError(t, err)
		require.NotNil(t, msg)
		require.True(t, receivedThinkingEnd)
		require.Equal(t, "Thinking through the problem...", thinkingContent)

		require.Len(t, msg.Content, 2)
		th, okTh := msg.Content[0].(ai.ThinkingContent)
		require.True(t, okTh)
		require.Equal(t, "Thinking through the problem...", th.Thinking)

		tc, okTc := msg.Content[1].(ai.ToolCall)
		require.True(t, okTc)
		require.Equal(t, "call_after_thought", tc.ID)
		require.Equal(t, "read", tc.Name)
		require.Equal(t, "sig_after_thought_1", tc.ThoughtSignature)
		require.Equal(t, ai.StopToolUse, msg.StopReason)
	})

	t.Run("parallel multiple tool calls in one turn retain distinct thought signatures", func(t *testing.T) {
		ssePayload := `data: {"candidates":[{"content":{"role":"model","parts":[` +
			`{"functionCall":{"name":"read","args":{"path":"file1.txt"}},"thoughtSignature":"sig_parallel_1"},` +
			`{"functionCall":{"name":"write","args":{"path":"file2.txt"}},"thought_signature":"sig_parallel_2"}` +
			`]},"finishReason":"STOP"}]}` + "\n\n"

		stream := ai.NewAssistantMessageEventStream()
		go func() {
			for range stream.Events() {
			}
		}()

		callIdx := 0
		msg, err := scanGeminiSSEStream(strings.NewReader(ssePayload), func(name string) string {
			callIdx++
			return strings.ToLower(name) + fmt.Sprintf("_%d", callIdx)
		}, stream)
		require.NoError(t, err)
		require.NotNil(t, msg)
		require.Len(t, msg.Content, 2)

		tc1, ok1 := msg.Content[0].(ai.ToolCall)
		require.True(t, ok1)
		require.Equal(t, "read", tc1.Name)
		require.Equal(t, "read_1", tc1.ID)
		require.Equal(t, "sig_parallel_1", tc1.ThoughtSignature)

		tc2, ok2 := msg.Content[1].(ai.ToolCall)
		require.True(t, ok2)
		require.Equal(t, "write", tc2.Name)
		require.Equal(t, "write_2", tc2.ID)
		require.Equal(t, "sig_parallel_2", tc2.ThoughtSignature)

		require.Equal(t, ai.StopToolUse, msg.StopReason)
	})
}
