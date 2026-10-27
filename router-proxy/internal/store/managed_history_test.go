package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/stretchr/testify/require"
)

func TestManagedHistoryKeepsQuietEligibilityAtWriteAndCapsComplete(t *testing.T) {
	durable := openTestStore(t)
	ctx := context.Background()
	body := "quiet message"
	source := protocol.MessageSource{Kind: "agent"}
	result, err := durable.Enqueue(ctx, protocol.Envelope{ID: "quiet-message", From: "worker", To: "supervisor", Body: body, CreatedAt: "2030-01-01T00:00:00.000Z", Source: &source}, EnqueueOptions{Conversation: &ConversationOptions{Body: &body, Source: &source, QuietEligible: true}})
	require.NoError(t, err)
	require.True(t, result.ConversationCreated)
	page, err := durable.ManagedHistoryPage(ctx, "worker", "quiet", 0, 10)
	require.NoError(t, err)
	require.Equal(t, []ManagedHistoryEntry{{ID: "quiet-message", AgentID: "worker", At: result.ConversationMessage.RecordedAt, Level: "message", Message: body}}, page.Entries)

	for index := 0; index < maxManagedHistoryEntries+1; index++ {
		_, err := durable.AppendManagedCompleteHistory(ctx, "worker", "test", "info", fmt.Sprintf("event-%04d", index))
		require.NoError(t, err)
	}
	complete, err := durable.ManagedHistoryPage(ctx, "worker", "complete", 0, maxManagedHistoryEntries-1)
	require.NoError(t, err)
	require.Len(t, complete.Entries, maxManagedHistoryEntries-1)
	require.Equal(t, fmt.Sprintf("event-%04d", maxManagedHistoryEntries), complete.Entries[0].Message)
	require.Equal(t, "event-0002", complete.Entries[len(complete.Entries)-1].Message)
	require.NotNil(t, complete.NextBefore)
	_, err = durable.ManagedHistoryPage(ctx, "worker", "complete", *complete.NextBefore, 10)
	require.NoError(t, err)
	quietAfterCompleteCap, err := durable.ManagedHistoryPage(ctx, "worker", "quiet", 0, 10)
	require.NoError(t, err)
	require.Equal(t, page.Entries, quietAfterCompleteCap.Entries, "complete-history retention must never evict quiet history")
}

func TestManagedHistoryPruneIsScopedToAgent(t *testing.T) {
	durable := openTestStore(t)
	ctx := context.Background()

	// Seed agent-b with some entries
	for i := 0; i < 10; i++ {
		_, err := durable.AppendManagedCompleteHistory(ctx, "agent-b", "test", "info", fmt.Sprintf("beta-%03d", i))
		require.NoError(t, err)
	}

	// Fill agent-a to the cap and beyond
	for i := 0; i < maxManagedHistoryEntries+5; i++ {
		_, err := durable.AppendManagedCompleteHistory(ctx, "agent-a", "test", "info", fmt.Sprintf("alpha-%03d", i))
		require.NoError(t, err)
	}

	// agent-a should be capped at maxManagedHistoryEntries
	var countA int
	require.NoError(t, durable.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM managed_complete_history WHERE agent_id='agent-a'").Scan(&countA))
	require.Equal(t, maxManagedHistoryEntries, countA)

	// agent-b MUST NOT be pruned when agent-a generates history!
	var countB int
	require.NoError(t, durable.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM managed_complete_history WHERE agent_id='agent-b'").Scan(&countB))
	require.Equal(t, 10, countB, "pruning for agent-a must never evict agent-b history")
}

func TestManagedHistoryPageScopesFactsAndQuietMailToItsAgent(t *testing.T) {
	durable := openTestStore(t)
	ctx := context.Background()
	_, err := durable.AppendManagedCompleteHistory(ctx, "managed-alpha", "lifecycle", "info", "Alpha entered running")
	require.NoError(t, err)
	_, err = durable.AppendManagedCompleteHistory(ctx, "managed-beta", "lifecycle", "info", "Beta entered running")
	require.NoError(t, err)
	_, err = durable.AppendManagedCompleteHistory(ctx, "managed-alpha", "managed_http", "info", "Managed POST completed")
	require.NoError(t, err)
	_, err = durable.AppendManagedCompleteHistory(ctx, "managed-alpha", "message", "info", "Message sent to another worker")
	require.NoError(t, err)

	alpha, err := durable.ManagedHistoryPage(ctx, "managed-alpha", "complete", 0, 10)
	require.NoError(t, err)
	require.Len(t, alpha.Entries, 1)
	require.Equal(t, "managed-alpha", alpha.Entries[0].AgentID)
	require.Equal(t, "Alpha entered running", alpha.Entries[0].Message)

	body := "alpha quiet mail"
	source := protocol.MessageSource{Kind: "agent"}
	_, err = durable.Enqueue(ctx, protocol.Envelope{ID: "alpha-quiet", From: "managed-alpha", To: "supervisor", Body: body, CreatedAt: "2030-01-01T00:00:00.000Z", Source: &source}, EnqueueOptions{Conversation: &ConversationOptions{Body: &body, Source: &source, QuietEligible: true}})
	require.NoError(t, err)
	quiet, err := durable.ManagedHistoryPage(ctx, "managed-beta", "quiet", 0, 10)
	require.NoError(t, err)
	require.Empty(t, quiet.Entries)
}

func TestManagedQuietSendAuthorizationIsSingleUseAndCompleteHistoryProducerIsIdempotent(t *testing.T) {
	durable := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, durable.ReserveManagedQuietSendAuthorization(ctx, "control-1", "managed-agent", "supervisor"))
	consumed, err := durable.ConsumeManagedQuietSendAuthorization(ctx, "control-1", "managed-agent", "supervisor")
	require.NoError(t, err)
	require.True(t, consumed)
	consumed, err = durable.ConsumeManagedQuietSendAuthorization(ctx, "control-1", "managed-agent", "supervisor")
	require.NoError(t, err)
	require.False(t, consumed)
	first, err := durable.RecordManagedCompleteHistory(ctx, "managed-agent", "control", "info", "Managed tool requested", "control:1")
	require.NoError(t, err)
	second, err := durable.RecordManagedCompleteHistory(ctx, "managed-agent", "control", "info", "Managed tool requested", "control:1")
	require.NoError(t, err)
	require.Equal(t, first, second)
	page, err := durable.ManagedHistoryPage(ctx, "managed-agent", "complete", 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
}

func TestManagedHistoryProjectsClosedPresentationMetadata(t *testing.T) {
	durable := openTestStore(t)
	ctx := context.Background()
	presentation := &ManagedHistoryPresentation{Kind: "tool", ToolName: "functions.bash", Outcome: "success", Summary: "Completed successfully"}
	entry, err := durable.RecordManagedCompleteHistoryWithPresentation(ctx, "worker", "managed_control", "info", "Ran bash", "tool:1", presentation)
	require.NoError(t, err)
	require.Equal(t, presentation, entry.Presentation)

	page, err := durable.ManagedHistoryPage(ctx, "worker", "complete", 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	require.Equal(t, presentation, page.Entries[0].Presentation)
	require.NotContains(t, fmt.Sprintf("%+v", page.Entries[0]), "arguments")

	_, err = durable.RecordManagedCompleteHistoryWithPresentation(ctx, "worker", "managed_control", "info", "Ran bash", "tool:unsafe", &ManagedHistoryPresentation{Kind: "tool", ToolName: "bash", Summary: "token=not-safe"})
	require.Error(t, err, "presentation text cannot become a secret-bearing diagnostic channel")
	_, err = durable.RecordManagedCompleteHistoryWithPresentation(ctx, "worker", "managed_control", "info", "Ran bash", "tool:bad-kind", &ManagedHistoryPresentation{Kind: "unknown"})
	require.Error(t, err)
	_, err = durable.RecordManagedCompleteHistory(ctx, "worker", "managed_control", "info", "stdout: private output", "tool:raw-output")
	require.Error(t, err, "raw process output cannot become Complete history")
}

func TestManagedHistorySynthesizesStructuredPresentationForLegacyRows(t *testing.T) {
	durable := openTestStore(t)
	ctx := context.Background()
	_, err := durable.db.ExecContext(ctx, `INSERT INTO managed_complete_history(agent_id,category,level,message,occurred_at,event_key,presentation_json) VALUES (?,?,?,?,?,?,NULL)`, "legacy-worker", "managed_parent_compaction", "info", "Context compacted", "2030-01-01T00:00:00.000Z", "legacy-compaction")
	require.NoError(t, err)

	page, err := durable.ManagedHistoryPage(ctx, "legacy-worker", "complete", 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	require.Equal(t, &ManagedHistoryPresentation{Kind: "compaction", Outcome: "completed"}, page.Entries[0].Presentation)
	require.Nil(t, page.Entries[0].MessageContent)
	require.Nil(t, page.Entries[0].SenderIdentity)
	encoded, err := json.Marshal(page.Entries[0])
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "message_content")
	require.NotContains(t, string(encoded), "sender_identity")
}

func TestMigration052PreservesLegacyHistoryDetailsAsAbsent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	migrations := DefaultMigrations()
	before, err := Open(ctx, path, Options{Migrations: migrations[:migrationIndex(t, migrations, "052_managed_history_message_content")]})
	require.NoError(t, err)
	_, err = before.db.ExecContext(ctx, `INSERT INTO managed_complete_history(agent_id,category,level,message,occurred_at,event_key,presentation_json) VALUES (?,?,?,?,?,?,NULL)`, "legacy-worker", "managed_lifecycle", "info", "Legacy event", "2030-01-01T00:00:00.000Z", "legacy-before-052")
	require.NoError(t, err)
	require.NoError(t, before.Close())

	after, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, after.Close()) })
	var contentNull, senderNull int
	require.NoError(t, after.db.QueryRowContext(ctx, `SELECT message_content IS NULL,sender_identity IS NULL FROM managed_complete_history WHERE event_key='legacy-before-052'`).Scan(&contentNull, &senderNull))
	require.Equal(t, 1, contentNull)
	require.Equal(t, 1, senderNull)
}

func TestManagedHistoryRejectsInvalidInputs(t *testing.T) {
	durable := openTestStore(t)
	_, err := durable.AppendManagedCompleteHistory(context.Background(), "worker", "", "info", "message")
	require.Error(t, err)
	_, err = durable.AppendManagedCompleteHistory(context.Background(), "", "lifecycle", "info", "message")
	require.Error(t, err)
	_, err = durable.ManagedHistoryPage(context.Background(), "worker", "not-a-view", 0, 1)
	require.Error(t, err)
	_, err = durable.ManagedHistoryPage(context.Background(), "worker", "quiet", 0, maxManagedHistoryEntries+1)
	require.Error(t, err)
}

func TestGoalEvaluationWithSensitiveKeywordsRecordedAndReturned(t *testing.T) {
	durable := openTestStore(t)
	ctx := context.Background()

	// Reasons containing words like "cookie", "token", "argument", "environment"
	reasons := []string{
		"Looked up 5 distinct cookie recipes and selected the best one.",
		"Validated authorization token and refreshed session.",
		"Checked all function arguments and confirmed correct syntax.",
		"Configured local development environment and ran tests.",
	}

	for i, reason := range reasons {
		outcome := "completed"
		if i%2 == 1 {
			outcome = "not_done"
		}
		summary := reason
		eventKey := fmt.Sprintf("goal-evaluation:worker:%d:%d:%s", i+1, i+2, outcome)
		pres := &ManagedHistoryPresentation{
			Kind:    "goal_evaluation",
			Outcome: outcome,
			Summary: summary,
		}
		msg := "Goal Evaluation • " + strings.ToUpper(outcome)
		entry, err := durable.RecordManagedCompleteHistoryWithDetails(ctx, "worker", "managed_goal_evaluation", "info", msg, eventKey, pres, nil, &reason)
		require.NoError(t, err, "goal evaluation presentation must not be rejected for keywords in summary or reason")
		require.Equal(t, pres, entry.Presentation)
		require.NotNil(t, entry.MessageContent)
		require.Equal(t, reason, *entry.MessageContent)
	}

	page, err := durable.ManagedHistoryPage(ctx, "worker", "complete", 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Entries, len(reasons))
	for _, entry := range page.Entries {
		require.Equal(t, "goal_evaluation", entry.Presentation.Kind)
		require.NotEmpty(t, entry.Presentation.Summary)
		require.NotNil(t, entry.MessageContent)
	}
}

func TestManagedAgentCaseInsensitiveLookup(t *testing.T) {
	durable := openTestStore(t)
	ctx := context.Background()

	rec := ManagedAgentRecord{
		AgentID:          "workflows",
		Name:             "workflows",
		Cwd:              t.TempDir(),
		SelectedModel:    ManagedModel{Provider: "antigravity", ID: "gemini-3.8-flash"},
		Runtime:          "embedded",
		DesiredState:     "running",
		LifecycleState:   "running",
		ActivityState:    "idle",
		ManifestRevision: 1,
		ManifestHash:     "manifest-sha",
		CreatedAt:        "2026-10-01T12:00:00Z",
		UpdatedAt:        "2026-10-01T12:00:00Z",
	}
	op := ManagedOperation{
		OperationID:       "op-workflows",
		Method:            "POST",
		CanonicalPath:     "/v1/managed-agents",
		RequestID:         "req-workflows",
		CanonicalBodyHash: "hash-workflows",
		State:             "terminal",
		ResponseStatus:    200,
	}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, rec, op))

	exact, err := durable.ManagedAgent(ctx, "workflows")
	require.NoError(t, err)
	require.Equal(t, "workflows", exact.AgentID)

	upper, err := durable.ManagedAgent(ctx, "Workflows")
	require.NoError(t, err, "lookup with Workflows must find workflows")
	require.Equal(t, "workflows", upper.AgentID)

	mixed, err := durable.ManagedAgent(ctx, "WORKFLOWS")
	require.NoError(t, err)
	require.Equal(t, "workflows", mixed.AgentID)
}

func TestManagedHistoryPresentationAcceptsLongSubagentIDsInFromAndTo(t *testing.T) {
	durable := openTestStore(t)
	ctx := context.Background()

	parentID := "worker-lead-supervisor"
	childID := "subagent-worker-lead-supervisor-3fa85f64-5717-4562-b3fc-2c963f66afa6" // ~68 chars

	pres := &ManagedHistoryPresentation{
		Kind:    "message",
		Outcome: "success",
		From:    parentID,
		To:      childID,
		Summary: "Steering from " + parentID,
	}
	content := "Please focus on step 2"
	entry, err := durable.RecordManagedCompleteHistoryWithFullDetails(
		ctx,
		childID,
		"managed_message",
		"info",
		"Steering from "+parentID,
		"subagent-steer:test-1",
		pres,
		nil,
		&content,
		&parentID,
	)
	require.NoError(t, err, "subagent ID > 48 chars in To must be accepted")
	require.NotNil(t, entry.Presentation)
	require.Equal(t, childID, entry.Presentation.To)
	require.Equal(t, parentID, entry.Presentation.From)

	// Also test subagent in From
	presReverse := &ManagedHistoryPresentation{
		Kind:    "message",
		Outcome: "success",
		From:    childID,
		To:      parentID,
		Summary: "Update from " + childID,
	}
	entry2, err := durable.RecordManagedCompleteHistoryWithFullDetails(
		ctx,
		childID,
		"managed_message",
		"info",
		"Update from "+childID,
		"subagent-steer:test-2",
		presReverse,
		nil,
		&content,
		&childID,
	)
	require.NoError(t, err, "subagent ID > 48 chars in From must be accepted")
	require.NotNil(t, entry2.Presentation)
	require.Equal(t, childID, entry2.Presentation.From)
	require.Equal(t, parentID, entry2.Presentation.To)
}
