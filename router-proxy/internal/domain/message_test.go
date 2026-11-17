package domain

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/stretchr/testify/require"
)

func TestBatchFormattingPreservesSenderSubjectAndReplyHint(t *testing.T) {
	subject := "status"
	first := Message{From: "alice", To: "agent", Subject: &subject, Body: "one"}
	second := Message{From: "bob", To: "agent", Body: "two"}
	body := BatchedBody(first, second)
	require.True(t, IsBatchedBody(body))
	require.Contains(t, body, "From: alice\nSubject: status")
	require.Contains(t, body, "From: bob")
	require.Contains(t, body, "[Reply via mailbox to bob.]")
	built := BuildDeliveryMessage([]Message{first, second})
	require.Equal(t, body, built.Body)
	require.Equal(t, "agent", built.Envelope().To)
}

// TestMailboxFooterBatchFixture exports the real builder for cross-runtime tests.
func TestMailboxFooterBatchFixture(t *testing.T) {
	subject, empty := "status", ""
	rows := []Message{
		{ID: "batch-1", From: "wolfgang", To: "batch-target", Body: "first body", CreatedAt: "2026-06-01T00:00:00Z", MessageIncarnationID: "inc-1", Source: protocol.MessageSource{Kind: "human"}},
		{ID: "batch-2", From: "supervisor", To: "batch-target", Subject: &empty, Body: "second\n雪", CreatedAt: "2026-06-01T00:00:01Z", MessageIncarnationID: "inc-2", Source: protocol.MessageSource{Kind: "agent"}},
		{ID: "batch-3", From: "worker-a17", To: "batch-target", Subject: &subject, Body: "third body\n[existing hint]", CreatedAt: "2026-06-01T00:00:02Z", MessageIncarnationID: "inc-3", Source: protocol.MessageSource{Kind: "agent"}},
	}
	message := BuildDeliveryMessage(rows)
	expected := "Mailbox message batch\n\nFrom: wolfgang\n\nfirst body\n\n[Reply via mailbox to wolfgang.]\n\n---\n\nFrom: supervisor\nSubject: \n\nsecond\n雪\n\n[Reply via mailbox to supervisor.]\n\n---\n\nFrom: worker-a17\nSubject: status\n\nthird body\n[existing hint]\n\n[Reply via mailbox to worker-a17.]"
	require.Equal(t, expected, message.Body)
	if path := os.Getenv("MAILBOX_FOOTER_BATCH_OUTPUT"); path != "" {
		items := []DeliveryItem{{MessageID: "batch-1", MessageIncarnationID: "inc-1", AttemptNumber: 1}, {MessageID: "batch-2", MessageIncarnationID: "inc-2", AttemptNumber: 2}, {MessageID: "batch-3", MessageIncarnationID: "inc-3", AttemptNumber: 3}}
		data, err := json.Marshal(map[string]any{"message": message.Envelope(), "items": items})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, data, 0600))
	}
}

func TestBatchContentDoesNotNestAnExistingBatchHeader(t *testing.T) {
	first := Message{From: "alice", To: "agent", Body: "one"}
	second := Message{From: "bob", To: "agent", Body: "two"}
	existing := first
	existing.Body = BatchedBody(first, second)
	third := Message{From: "carol", To: "agent", Body: "three", Sentinel: &protocol.SentinelDescriptor{Model: "m"}}
	result := BatchedBody(existing, third)
	require.Equal(t, 1, strings.Count(result, BatchHeader))
	require.Contains(t, result, "From: carol")
}

func TestMailboxContentSystemNotice(t *testing.T) {
	// Without existing footer
	body := "Subagent subagent-1 finished with status completed: done."
	result := MailboxContent(body, "system")
	require.Equal(t, body+"\n\n"+SystemNotificationForwardGuidanceFooter, result)

	// With existing new footer - should not duplicate
	result2 := MailboxContent(result, "system")
	require.Equal(t, result, result2)

	// With legacy footer - should not duplicate
	legacyBody := body + "\n\n---\n*Automated notification from system. Do not reply to this message — messages to 'system' are not accepted.*"
	legacyResult := MailboxContent(legacyBody, "system")
	require.Equal(t, legacyBody, legacyResult)

	// Non-system sender
	userResult := MailboxContent(body, "wolfgang")
	require.Equal(t, body+"\n\nReply via mailbox to wolfgang", userResult)
}

func TestBatchItemSystemNotice(t *testing.T) {
	subject := "[System Notice] Subagent Completed: subagent-1"
	msg := Message{
		From:    "system",
		To:      "worker",
		Subject: &subject,
		Body:    "Subagent completed successfully.",
		Source:  protocol.MessageSource{Kind: "system"},
	}
	item := BatchItem(msg)
	require.Contains(t, item, "From: system")
	require.Contains(t, item, "Subject: [System Notice] Subagent Completed: subagent-1")
	require.Contains(t, item, "["+SystemNotificationForwardGuidanceFooter+"]")
	require.NotContains(t, item, "Reply via mailbox to system")

	// Deduplication test when footer already present in body
	msgWithFooter := Message{
		From:    "system",
		To:      "worker",
		Subject: &subject,
		Body:    "Subagent completed successfully.\n\n" + SystemNotificationForwardGuidanceFooter,
		Source:  protocol.MessageSource{Kind: "system"},
	}
	itemWithFooter := BatchItem(msgWithFooter)
	require.Equal(t, 1, strings.Count(itemWithFooter, "Messages to 'system' are not accepted"))

	// Deduplication test when legacy footer is in body
	legacyMsg := Message{
		From:    "system",
		To:      "worker",
		Subject: &subject,
		Body:    "Subagent completed successfully.\n\n---\n*Automated notification from system. Do not reply to this message — messages to 'system' are not accepted.*",
		Source:  protocol.MessageSource{Kind: "system"},
	}
	legacyItem := BatchItem(legacyMsg)
	require.Equal(t, 0, strings.Count(legacyItem, "Messages to 'system' are not accepted"))
}

// The retired RPC fixture exercised these renderer boundaries before passing
// text to Pi. Keep the renderer cases without claiming an embedded-runtime path.
func TestMailboxContentSenderAndBodyBoundaries(t *testing.T) {
	for _, tc := range []struct{ from, body, expected string }{
		{"wolfgang", "hello", "hello\n\nReply via mailbox to wolfgang"},
		{"supervisor", "", "\n\nReply via mailbox to supervisor"},
		{"worker-a17", "line one\n雪\n[existing hint]", "line one\n雪\n[existing hint]\n\nReply via mailbox to worker-a17"},
		{"self", "self body", "self body\n\nReply via mailbox to self"},
	} {
		t.Run(tc.from, func(t *testing.T) {
			require.Equal(t, tc.expected, MailboxContent(tc.body, tc.from))
		})
	}
	for _, size := range []int{protocol.DefaultMaxBodyBytes, 16 * 1024} {
		body := strings.Repeat("雪", size/3) + strings.Repeat("x", size%3)
		from := strings.Repeat("雪", 128)
		require.NoError(t, protocol.ValidateEnvelope(protocol.Envelope{ID: "message-footer", From: from, To: "supervisor", Body: body, CreatedAt: "2026-06-01T00:00:00.000Z", Source: &protocol.MessageSource{Kind: "agent"}}, size))
		got := MailboxContent(body, from)
		require.Equal(t, body+"\n\nReply via mailbox to "+from, got)
		require.Len(t, got, size+407)
	}
}
