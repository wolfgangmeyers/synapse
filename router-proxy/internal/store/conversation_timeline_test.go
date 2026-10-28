package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/stretchr/testify/require"
)

func TestConversationTimeline_CRUDAndPagination(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	agentID := "test-agent-timeline"

	// 1. Initially empty
	events, nextBefore, err := s.GetConversationTimeline(ctx, agentID, 50, nil)
	require.NoError(t, err)
	require.Empty(t, events)
	require.Nil(t, nextBefore)

	// 2. Insert via RecordConversationTimelineTx
	content1 := "Hello world"
	roleUser := "user"
	meta1, _ := json.Marshal(map[string]any{"source": "test"})
	ev1, err := s.RecordConversationTimelineTx(ctx, "evt-1", agentID, "message", &roleUser, &content1, meta1, 1000)
	require.NoError(t, err)
	require.Equal(t, int64(1), ev1.Seq)

	content2 := "Running bash tool"
	ev2, err := s.RecordConversationTimelineTx(ctx, "evt-2", agentID, "tool", nil, &content2, nil, 2000)
	require.NoError(t, err)
	require.Equal(t, int64(2), ev2.Seq)

	content3 := "All done!"
	roleAssistant := "assistant"
	ev3, err := s.RecordConversationTimelineTx(ctx, "evt-3", agentID, "message", &roleAssistant, &content3, nil, 3000)
	require.NoError(t, err)
	require.Equal(t, int64(3), ev3.Seq)

	// 3. Query all
	events, nextBefore, err = s.GetConversationTimeline(ctx, agentID, 50, nil)
	require.NoError(t, err)
	require.Len(t, events, 3)
	require.Nil(t, nextBefore)
	require.Equal(t, int64(1), events[0].Seq)
	require.Equal(t, int64(2), events[1].Seq)
	require.Equal(t, int64(3), events[2].Seq)

	// 4. Query with limit 2 (returns most recent 2: seq 2 and 3, nextBefore = 2)
	events, nextBefore, err = s.GetConversationTimeline(ctx, agentID, 2, nil)
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.NotNil(t, nextBefore)
	require.Equal(t, int64(2), *nextBefore)
	require.Equal(t, int64(2), events[0].Seq)
	require.Equal(t, int64(3), events[1].Seq)

	// 6. Test concurrent inserts to ensure sequence numbers do not collide
	concurrentAgent := "agent-concurrent-timeline"
	const numConcurrent = 20
	errCh := make(chan error, numConcurrent)
	for i := 0; i < numConcurrent; i++ {
		go func(idx int) {
			content := fmt.Sprintf("Concurrent message %d", idx)
			_, err := s.RecordConversationTimelineTx(ctx, "", concurrentAgent, "message", &roleUser, &content, nil, int64(10000+idx))
			errCh <- err
		}(i)
	}
	for i := 0; i < numConcurrent; i++ {
		require.NoError(t, <-errCh)
	}
	cEvents, _, err := s.GetConversationTimeline(ctx, concurrentAgent, 100, nil)
	require.NoError(t, err)
	require.Len(t, cEvents, numConcurrent)
	seqMap := make(map[int64]bool)
	for _, ev := range cEvents {
		require.False(t, seqMap[ev.Seq], "sequence number should be unique")
		seqMap[ev.Seq] = true
	}
	for seq := int64(1); seq <= numConcurrent; seq++ {
		require.True(t, seqMap[seq], "sequence numbers should be contiguous 1..N")
	}
}

func TestConversationTimeline_Hooks(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	agentID := "agent-hook-test"

	// Message enqueue hook with attachments
	attJSON := json.RawMessage(`{"id":"att-1","filename":"test.png"}`)
	msg := domain.Message{
		ID:          "msg-hook-1",
		From:        "user-client",
		To:          agentID,
		Body:        "Message from user",
		CreatedAt:   formatTimestamp(time.Now()),
		Attachments: []json.RawMessage{attJSON},
	}
	err := s.recordTimelineMessageTx(ctx, msg, protocol.MessageSource{Kind: "web_ui"})
	require.NoError(t, err)

	// History hook for the same incoming message should NOT be double-inserted
	msgBody := "Message from user"
	userHistEntry := ManagedHistoryEntry{
		ID:             "hist-msg-1",
		At:             formatTimestamp(time.Now()),
		MessageContent: &msgBody,
		Presentation: &ManagedHistoryPresentation{
			Kind: "message",
			From: "user",
			To:   agentID,
		},
	}
	err = s.recordTimelineHistoryTx(ctx, agentID, "managed_message", "info", "User message", userHistEntry)
	require.NoError(t, err)

	// History hook for a tool execution
	histEntry := ManagedHistoryEntry{
		ID: "hist-hook-1",
		At: formatTimestamp(time.Now()),
		Presentation: &ManagedHistoryPresentation{
			Kind: "tool",
		},
	}
	err = s.recordTimelineHistoryTx(ctx, agentID, "managed_tool", "info", "Ran test command", histEntry)
	require.NoError(t, err)

	// Verify timeline contains only 2 events (the message and the tool), avoiding duplicate message
	events, _, err := s.GetConversationTimeline(ctx, agentID, 50, nil)
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, "message", events[0].Kind)
	require.Equal(t, "tool", events[1].Kind)

	// Verify attachments are present in the message event metadata
	var meta map[string]any
	require.NoError(t, json.Unmarshal(events[0].Metadata, &meta))
	require.Contains(t, meta, "attachments")
}

func TestConversationTimeline_ToolLifecycleUpdate(t *testing.T) {
	ctx := context.Background()
	s, fence := builtinToolFixture(t)
	agentID := fence.AgentID

	// 1. Tool starts
	entry, created, err := s.BeginManagedToolInvocation(ctx, fence, "tool-call-1", "bash", map[string]any{"command": "echo test"})
	require.NoError(t, err)
	require.True(t, created)
	require.NotEmpty(t, entry.ID)

	// Timeline should have the tool event
	events, _, err := s.GetConversationTimeline(ctx, agentID, 50, nil)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, "tool", events[0].Kind)
	require.Equal(t, "Running bash", *events[0].Content)

	// 2. Tool completes
	finishedEntry, changed, err := s.FinishManagedToolInvocation(ctx, fence, "tool-call-1", "bash", ManagedBuiltinToolSucceeded)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "Ran bash", finishedEntry.Message)

	// Timeline event should be updated, NOT duplicated
	events, _, err = s.GetConversationTimeline(ctx, agentID, 50, nil)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, "tool", events[0].Kind)
	require.Equal(t, "Ran bash", *events[0].Content)
}

func TestConversationTimeline_Migration(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	agentID := "migrated-agent"

	// Insert message into messages table
	nowStr := formatTimestamp(time.Now())
	_, err := s.db.ExecContext(ctx, `INSERT INTO agents(agent_id, first_seen_at) VALUES (?, ?) ON CONFLICT DO NOTHING`, agentID, nowStr)
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `INSERT INTO messages(id, sender, recipient, body, created_at, expires_at, state) VALUES (?, ?, ?, ?, ?, ?, 'queued')`,
		"m1", "user", agentID, "Hello from migration test", nowStr, nowStr)
	require.NoError(t, err)

	// Insert into conversation_messages with attachments
	attJSON := `[{"id":"att-migrated-1","url":"/v1/attachments/att-migrated-1","mime_type":"image/png","size_bytes":123}]`
	_, err = s.db.ExecContext(ctx, `INSERT INTO conversation_messages(message_id, message_incarnation_id, delivery_message_id, sender, recipient, participant_a, participant_b, body, created_at, recorded_at, attachments_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"m1", "inc-1", "del-1", "user", agentID, "user", agentID, "Hello from migration test", nowStr, nowStr, attJSON)
	require.NoError(t, err)

	// Insert history into managed_complete_history
	_, err = s.db.ExecContext(ctx, `INSERT INTO managed_complete_history(agent_id, category, level, message, occurred_at) VALUES (?, ?, ?, ?, ?)`,
		agentID, "managed_tool", "info", "Tool execution completed", nowStr)
	require.NoError(t, err)

	// Run migration
	err = s.migrateToConversationTimelineTx(ctx)
	require.NoError(t, err)

	events, _, err := s.GetConversationTimeline(ctx, agentID, 50, nil)
	require.NoError(t, err)
	require.Len(t, events, 2)

	var foundMessageEvent bool
	for _, ev := range events {
		if ev.Kind == "message" {
			foundMessageEvent = true
			var meta map[string]any
			require.NoError(t, json.Unmarshal(ev.Metadata, &meta))
			require.Contains(t, meta, "attachments")
		}
	}
	require.True(t, foundMessageEvent, "should have found a message event with attachments")
}
