package domain

import (
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
