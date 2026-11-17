package managed

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// TestClaudeResumeNoticeSkippedBeforeAdmission tests Scenario 1:
// Claude worker resumes with a background shell command notice (or notice assistant + result)
// before echoing the user input. The runtime must skip the notice turn, admit the delivered message,
// and complete successfully with the real answer to user input.
func TestClaudeResumeNoticeSkippedBeforeAdmission(t *testing.T) {
	// Script emits an unadmitted resume notice turn (assistant + result) before the actual turn.
	script := `{"turns":[
		[{"text":"Notice: background bash command killed"},{"text":" cleaned up"}],
		[{"text":"real answer to user prompt"}]
	]}`
	r, p, _, _ := claudeRuntimeFixture(t, script)
	ctx := context.Background()

	original := ai.UserMessage{
		Content:   ai.ContentList{ai.TextContent{Text: "actual user prompt"}},
		Timestamp: time.Now().UnixMilli(),
	}

	out, err := r.RunMessages(ctx, []agent.AgentMessage{original})
	require.NoError(t, err)
	require.Equal(t, ai.StopStop, out.StopReason)
	require.Equal(t, "real answer to user prompt", r.Snapshot().LastAssistantText)

	// Verify persistence: the user prompt was admitted and answered
	snap, err := p.db.ReopenEmbedded(ctx, p.binding)
	require.NoError(t, err)
	require.NotEmpty(t, snap.Entries)
}

// TestClaudePreAdmissionFailureClassifiedAsUnconsumed tests Scenario 2:
// When a Claude worker encounters a failure before user prompt admission / echo,
// the error must be classified as unconsumed (ErrClaudeUnconsumed), and claude_stdin_started
// must be reset to 0 so the mail is eligible for redelivery/retry rather than held as outcome uncertain.
func TestClaudePreAdmissionFailureClassifiedAsUnconsumed(t *testing.T) {
	ctx := context.Background()
	// Script exits or crashes before echoing the user message
	r, p, pool, _ := claudeRuntimeFixture(t, `{"turns":[[{"crash":1}]]}`)
	require.NoError(t, p.db.ReconcileManagedRecipientSources(ctx))

	messageID := "test-pre-admission-fail"
	_, err := p.db.Enqueue(ctx, protocol.Envelope{
		ID:        messageID,
		From:      "sender",
		To:        p.binding.AgentID,
		Body:      "important task",
		CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	})
	require.NoError(t, err)

	_, err = p.db.ResolveMessageTarget(ctx, messageID)
	require.NoError(t, err)

	attempt, err := p.db.ReserveEmbeddedMail(ctx, p.binding, messageID)
	require.NoError(t, err)
	p.mail = attempt

	user, err := store.EmbeddedMailUserMessage(attempt, time.Now().UnixMilli())
	require.NoError(t, err)
	p.mailMessage, err = json.Marshal(user)
	require.NoError(t, err)

	_, runErr := r.RunMessages(ctx, []agent.AgentMessage{user})
	require.Error(t, runErr)
	require.ErrorIs(t, runErr, ErrClaudeUnconsumed, "pre-admission failure must be classified as ErrClaudeUnconsumed")

	// Ensure claude_stdin_started is reset to 0 (not held as outcome uncertain)
	held, err := p.db.ClaudeMailHeld(ctx, messageID)
	require.NoError(t, err)
	require.False(t, held, "unconsumed message must not be held as outcome uncertain")

	// Message should remain eligible for redelivery/retry
	require.NoError(t, p.db.ReleaseEmbeddedUnconsumedAttempt(ctx, p.binding, attempt.AttemptID))
	nextID, err := p.db.NextEmbeddedMail(ctx, p.binding.AgentID)
	require.NoError(t, err)
	require.Equal(t, messageID, nextID, "message must be immediately re-eligible in mailbox queue")

	_ = pool
}
