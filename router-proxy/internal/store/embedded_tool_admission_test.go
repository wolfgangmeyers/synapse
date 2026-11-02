package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func addressedStoreProducer(t *testing.T, s *Store, b EmbeddedBinding, id string, args map[string]any, entered bool) (ai.ToolCall, EmbeddedEntry, EmbeddedEntry) {
	t.Helper()
	ctx := context.Background()
	user := receiptSDKAppend(t, s, b, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "ordinary addressed work"}}})
	if entered {
		require.NoError(t, s.EnterEmbeddedNonMailProviderTurn(ctx, b, user.Sequence, user.Digest))
	}
	call := providers.FauxToolCall("mecha_mailbox_send", args, id)
	producer := receiptSDKAppend(t, s, b, *providers.FauxAssistantMessage(ai.ContentList{call}, ai.StopToolUse))
	return call, user, producer
}

func addressedStoreInvocation(t *testing.T) (*Store, EmbeddedBinding, int64, ai.ToolCall, *EmbeddedToolInvocation, EmbeddedEntry) {
	t.Helper()
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	b := goalBind(t, s, "addressed-store", "addressed-session", 1, "addressed-runtime", "root")
	g, err := s.MintEmbeddedProcessGeneration(ctx, "addressed-store-process")
	require.NoError(t, err)
	call, user, producer := addressedStoreProducer(t, s, b, "addressed-original", map[string]any{"recipient": "separate-recipient", "body": "independent body"}, true)
	w, err := s.RecordEmbeddedToolInvocation(ctx, b, g, user.Sequence, user.Digest, "", call)
	require.NoError(t, err)
	require.NoError(t, s.StartEmbeddedToolInvocation(ctx, w, call))
	return s, b, g, call, w, producer
}

func addressedStoreResult(t *testing.T, w *EmbeddedToolInvocation) ai.ToolResultMessage {
	t.Helper()
	content, err := EmbeddedToolResultContent(w)
	require.NoError(t, err)
	return ai.ToolResultMessage{ToolCallID: w.call.ID, ToolName: w.call.Name, Content: ai.ContentList{ai.TextContent{Text: string(content)}}, Timestamp: 1}
}

func TestEmbeddedAddressedToolCheckedResultAndOriginalOnce(t *testing.T) {
	ctx := context.Background()
	s, b, g, call, w, producer := addressedStoreInvocation(t)
	accepted, err := s.EnqueueEmbeddedToolMail(ctx, w)
	require.NoError(t, err)
	require.True(t, accepted.Created)
	require.Equal(t, b.AgentID, accepted.Message.From)
	require.Equal(t, "separate-recipient", accepted.Message.To)
	result := addressedStoreResult(t, w)
	require.NoError(t, s.CompleteEmbeddedToolInvocation(ctx, w, result))
	body, err := json.Marshal(result)
	require.NoError(t, err)
	entry, err := s.AppendEmbeddedToolInvocationResult(ctx, w, producer.Sequence, producer.Digest, "addressed-checked", result, body)
	require.NoError(t, err)
	require.Equal(t, producer.Sequence+1, entry.Sequence)
	effect, err := s.RecoverEmbeddedEffect(ctx, b, call.ID)
	require.NoError(t, err)
	require.Equal(t, "observed", effect.State)
	require.Equal(t, body, effect.Result)
	_, err = s.EnqueueEmbeddedToolMail(ctx, w)
	require.Error(t, err, "same live witness cannot send twice")
	_, err = s.RecordEmbeddedToolInvocation(ctx, b, g, w.origin.userSeq, w.origin.userDigest, "", call)
	require.Error(t, err, "saved original call/result cannot mint fresh authority")
	require.Equal(t, 1, embeddedRedCount(t, s, "messages"))
}

func TestEmbeddedAddressedMailRejectsSystemRecipient(t *testing.T) {
	for _, recipient := range []string{"system", "System", "SYSTEM"} {
		ctx := context.Background()
		s, _, _, _, w, _ := addressedStoreInvocation(t)
		w.call.Arguments["recipient"] = recipient
		_, err := s.EnqueueEmbeddedToolMail(ctx, w)
		require.EqualError(t, err, "failed to send message: 'system' is an automated system notifier and does not accept incoming messages. Do not reply to system notifications.")
	}
}

func TestEmbeddedAddressedMailKeepsDistinctAcceptedRequestOnDeliveryDedup(t *testing.T) {
	ctx := context.Background()
	s, b, g, call, w, producer := addressedStoreInvocation(t)
	first, err := s.EnqueueEmbeddedToolMail(ctx, w)
	require.NoError(t, err)
	result := addressedStoreResult(t, w)
	require.NoError(t, s.CompleteEmbeddedToolInvocation(ctx, w, result))
	body, err := json.Marshal(result)
	require.NoError(t, err)
	_, err = s.AppendEmbeddedToolInvocationResult(ctx, w, producer.Sequence, producer.Digest, "first-dedup-result", result, body)
	require.NoError(t, err)
	call.ID = "second-distinct-mail-call"
	receiptSDKAppend(t, s, b, *providers.FauxAssistantMessage(ai.ContentList{call}, ai.StopToolUse))
	secondInvocation, err := s.RecordEmbeddedToolInvocation(ctx, b, g, w.origin.userSeq, w.origin.userDigest, "", call)
	require.NoError(t, err)
	require.NoError(t, s.StartEmbeddedToolInvocation(ctx, secondInvocation, call))
	second, err := s.EnqueueEmbeddedToolMail(ctx, secondInvocation)
	require.NoError(t, err)
	require.True(t, second.Deduped)
	content, err := EmbeddedToolResultContent(secondInvocation)
	require.NoError(t, err)
	var accepted struct {
		RequestID string `json:"request_id"`
		MessageID string `json:"message_id"`
	}
	require.NoError(t, json.Unmarshal(content, &accepted))
	require.Equal(t, first.Message.ID, accepted.MessageID)
	require.NotEqual(t, accepted.MessageID, accepted.RequestID)
	hasReceipt, err := s.HasAcceptedSend(ctx, accepted.RequestID)
	require.NoError(t, err)
	require.True(t, hasReceipt, "the explicit tool request retains its own bounded accepted receipt")
	require.Equal(t, 1, embeddedRedCount(t, s, "messages"))
}

func TestEmbeddedAddressedToolAdmissionFenceInsideMailTransaction(t *testing.T) {
	for _, change := range []string{"owner", "process", "current-user", "source"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			s, b, g, _, w, _ := addressedStoreInvocation(t)
			switch change {
			case "owner":
				_, err := s.db.Exec(`UPDATE embedded_sessions SET current=0 WHERE agent_id=?`, b.AgentID)
				require.NoError(t, err)
			case "process":
				_, err := s.MintEmbeddedProcessGeneration(ctx, "replacement-process")
				require.NoError(t, err)
			case "current-user":
				receiptProviderUser(t, s, b, "newer ordinary work")
			case "source":
				_, err := s.ReserveEmbeddedRestartWake(ctx, b, g, "addressed-control", "root-idle", "control-digest")
				require.NoError(t, err)
				require.NoError(t, s.ClaimEmbeddedRestartWake(ctx, b, g, "addressed-control"))
			}
			_, err := s.EnqueueEmbeddedToolMail(ctx, w)
			require.Error(t, err, "renewal at Execute entry cannot authorize a later changed transaction fence")
			require.Zero(t, embeddedRedCount(t, s, "messages"))
			require.True(t, w.admitted, "failed transaction consumes domain admission")
			require.Empty(t, w.domainContent)
		})
	}
}

func TestEmbeddedAddressedToolRequiresCheckedUserAndSDKProducer(t *testing.T) {
	for _, missing := range []string{"provider-entry", "sdk-producer", "original-arguments"} {
		t.Run(missing, func(t *testing.T) {
			ctx := context.Background()
			s, _ := embeddedRedStore(t)
			b := goalBind(t, s, "addressed-missing", "session", 1, "runtime", "worker")
			g, err := s.MintEmbeddedProcessGeneration(ctx, "missing-process")
			require.NoError(t, err)
			call, user, _ := addressedStoreProducer(t, s, b, "missing-original", map[string]any{"recipient": "recipient", "body": "body"}, missing != "provider-entry")
			if missing == "sdk-producer" {
				call.ID = "invented-id"
			}
			if missing == "original-arguments" {
				call.Arguments["body"] = "changed body"
			}
			_, err = s.RecordEmbeddedToolInvocation(ctx, b, g, user.Sequence, user.Digest, "", call)
			require.Error(t, err)
			require.Zero(t, embeddedRedCount(t, s, "embedded_effects"))
		})
	}
}

func TestEmbeddedAddressedToolUnknownWithoutProducerCompletion(t *testing.T) {
	ctx := context.Background()
	s, b, g, call, w, producer := addressedStoreInvocation(t)
	_, err := s.EnqueueEmbeddedToolMail(ctx, w)
	require.NoError(t, err)
	result := addressedStoreResult(t, w) // Domain result exists, Host completion absent.
	body, err := json.Marshal(result)
	require.NoError(t, err)
	_, err = s.AppendEmbeddedToolInvocationResult(ctx, w, producer.Sequence, producer.Digest, "invented-completion", result, body)
	require.Error(t, err)
	effect, err := s.RecoverEmbeddedEffect(ctx, b, call.ID)
	require.NoError(t, err)
	require.Equal(t, "unknown", effect.State)
	require.Empty(t, effect.Result)
	_, err = s.RecordEmbeddedToolInvocation(ctx, b, g, w.origin.userSeq, w.origin.userDigest, "", call)
	require.Error(t, err)
	require.Equal(t, 1, embeddedRedCount(t, s, "messages"), "unknown original effect is not repeated")
}

func TestEmbeddedAddressedToolRejectsForgedSDKResult(t *testing.T) {
	ctx := context.Background()
	s, b, _, call, w, producer := addressedStoreInvocation(t)
	_, err := s.EnqueueEmbeddedToolMail(ctx, w)
	require.NoError(t, err)
	result := addressedStoreResult(t, w)
	require.NoError(t, s.CompleteEmbeddedToolInvocation(ctx, w, result))
	result.Content = ai.ContentList{ai.TextContent{Text: `{"status":"accepted","message_id":"forged"}`}}
	body, err := json.Marshal(result)
	require.NoError(t, err)
	_, err = s.AppendEmbeddedToolInvocationResult(ctx, w, producer.Sequence, producer.Digest, "forged-sdk-result", result, body)
	require.Error(t, err)
	require.Len(t, redReopen(t, s, b).Entries, 2)
	effect, err := s.RecoverEmbeddedEffect(ctx, b, call.ID)
	require.NoError(t, err)
	require.Equal(t, "unknown", effect.State)
	require.True(t, w.consumed)
}

func TestEmbeddedAddressedToolRejectedMailAdmissionHasNoReceipt(t *testing.T) {
	ctx := context.Background()
	s, b, _, call, w, _ := addressedStoreInvocation(t)
	s.maxQueuedMessages = 0 // Exhaust the existing bounded queue, not a new ledger.
	_, err := s.EnqueueEmbeddedToolMail(ctx, w)
	var capacity *QueueLimitError
	require.ErrorAs(t, err, &capacity)
	require.Zero(t, embeddedRedCount(t, s, "messages"))
	require.Empty(t, w.domainContent)
	effect, err := s.RecoverEmbeddedEffect(ctx, b, call.ID)
	require.NoError(t, err)
	require.Equal(t, "unknown", effect.State, "failed admission cannot fabricate an SDK success")
}

func TestEmbeddedToolKnownCommitPermissionErrorRetainsActualAdmission(t *testing.T) {
	for _, action := range []string{"mail"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			s, _ := embeddedRedStore(t)
			b := goalBind(t, s, "commit-caller", "commit-session", 1, "commit-runtime", "root")
			g, err := s.MintEmbeddedProcessGeneration(ctx, "commit-process")
			require.NoError(t, err)
			user := receiptProviderUser(t, s, b, "original committed operation")
			name := "mecha_mailbox_send"
			args := map[string]any{"recipient": "commit-recipient", "body": "committed body"}
			call := providers.FauxToolCall(name, args, "commit-original")
			producer := receiptSDKAppend(t, s, b, *providers.FauxAssistantMessage(ai.ContentList{call}, ai.StopToolUse))
			w, err := s.RecordEmbeddedToolInvocation(ctx, b, g, user.Sequence, user.Digest, "", call)
			require.NoError(t, err)
			require.NoError(t, s.StartEmbeddedToolInvocation(ctx, w, call))
			originalPath := s.path
			blocker := filepath.Join(t.TempDir(), "regular-file")
			require.NoError(t, os.WriteFile(blocker, []byte("fixture"), 0600))
			s.path = filepath.Join(blocker, "db") // ENOTDIR after the real COMMIT; connection is unchanged.
			t.Cleanup(func() { s.path = originalPath })
			admit := func() error {
				_, err := s.EnqueueEmbeddedToolMail(ctx, w)
				return err
			}
			err = admit()
			require.ErrorContains(t, err, "stat SQLite state file")
			s.path = originalPath
			require.True(t, w.domainCommitted)
			require.True(t, w.domainConclusive)
			content, err := EmbeddedToolResultContent(w)
			require.NoError(t, err)
			require.Contains(t, string(content), `"status":"accepted"`)
			require.Equal(t, 1, embeddedRedCount(t, s, "messages"))
			require.True(t, ClaimEmbeddedToolHandoff(w))
			require.False(t, ClaimEmbeddedToolHandoff(w), "original handoff is once-only")
			result := addressedStoreResult(t, w)
			require.Error(t, s.CompleteEmbeddedToolInvocation(ctx, w, result), "permission error cannot become checked SDK success")
			body, err := json.Marshal(result)
			require.NoError(t, err)
			_, err = s.AppendEmbeddedToolInvocationResult(ctx, w, producer.Sequence, producer.Digest, "no-forged-commit-result", result, body)
			require.ErrorContains(t, err, "stat SQLite state file")
			require.Error(t, admit(), "original witness cannot admit again")
			after, err := EmbeddedToolResultContent(w)
			require.NoError(t, err)
			require.Equal(t, content, after, "reuse cannot overwrite the actual admitted result")
			effect, err := s.RecoverEmbeddedEffect(ctx, b, call.ID)
			require.NoError(t, err)
			require.Equal(t, "unknown", effect.State)
			require.Empty(t, effect.Result)
		})
	}
}

func TestEmbeddedToolRollbackAndInconclusiveCommitHaveNoAcceptance(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "inconclusive"}[uncertain], func(t *testing.T) {
			ctx := context.Background()
			s, b, _, _, w, _ := addressedStoreInvocation(t)
			if uncertain {
				envelope := protocol.Envelope{ID: "uncertain-original", From: b.AgentID, To: "separate-recipient", Body: "independent body", CreatedAt: formatTimestamp(s.clock.Now())}
				// Deterministically lose the COMMIT/ROLLBACK conclusion on the real
				// connection. This fixture-only callback ends the outer transaction.
				_, err := s.enqueue(ctx, envelope, &EnqueueOptions{OnFreshAdmission: func() (func(bool), error) { _, err := s.db.ExecContext(ctx, "COMMIT"); return nil, err }}, w)
				require.Error(t, err)
				require.False(t, w.domainConclusive)
			} else {
				s.maxQueuedMessages = 0
				_, err := s.EnqueueEmbeddedToolMail(ctx, w)
				require.Error(t, err)
				require.True(t, w.domainConclusive)
				require.Zero(t, embeddedRedCount(t, s, "messages"))
			}
			require.False(t, w.domainCommitted)
			require.True(t, w.admitted)
			_, err := EmbeddedToolResultContent(w)
			require.Error(t, err)
			require.False(t, ClaimEmbeddedToolHandoff(w))
			_, err = s.EnqueueEmbeddedToolMail(ctx, w)
			require.Error(t, err, "no original admission retry after rollback or uncertainty")
		})
	}
}

func TestEmbeddedToolRejectsRetiredWorkerLaunch(t *testing.T) {
	s := &Store{}
	call := providers.FauxToolCall("mecha_worker", map[string]any{"action": "launch", "name": "forbidden", "cwd": "/project", "instruction": "task"}, "stale-launch")
	w, err := s.RecordEmbeddedToolInvocation(context.Background(), EmbeddedBinding{}, 0, 0, "", "", call)
	require.Nil(t, w)
	require.EqualError(t, err, "invalid managed tool call")
}
