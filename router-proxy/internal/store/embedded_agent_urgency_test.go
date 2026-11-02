package store

import (
	"context"
	"encoding/json"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func urgencyInvocation(t *testing.T) (*Store, EmbeddedBinding, int64, ai.ToolCall, *EmbeddedToolInvocation, EmbeddedEntry) {
	t.Helper()
	s, _ := embeddedRedStore(t)
	return urgencyInvocationOnStore(t, s)
}

func urgencyInvocationOnStore(t *testing.T, s *Store) (*Store, EmbeddedBinding, int64, ai.ToolCall, *EmbeddedToolInvocation, EmbeddedEntry) {
	t.Helper()
	ctx := context.Background()
	b := goalBind(t, s, "urgency-caller", "urgency-session", 1, "urgency-runtime", "root")
	require.NoError(t, s.ensureManagedGoalPolicyTx(ctx, b.AgentID, "2026-01-01T00:00:00Z"))
	_, err := s.AcquireAgentLease(ctx, LeaseRequest{AgentID: b.AgentID, InstanceID: "fixture", LeaseID: "fixture", LeaseDuration: time.Minute})
	require.NoError(t, err)
	g, err := s.MintEmbeddedProcessGeneration(ctx, "urgency-process")
	require.NoError(t, err)
	user := receiptSDKAppend(t, s, b, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "mark urgent"}}})
	require.NoError(t, s.EnterEmbeddedNonMailProviderTurn(ctx, b, user.Sequence, user.Digest))
	call := providers.FauxToolCall("mecha_agent_urgency", map[string]any{"urgent": true}, "urgency-original")
	producer := receiptSDKAppend(t, s, b, *providers.FauxAssistantMessage(ai.ContentList{call}, ai.StopToolUse))
	w, err := s.RecordEmbeddedToolInvocation(ctx, b, g, user.Sequence, user.Digest, "", call)
	require.NoError(t, err)
	require.NoError(t, s.StartEmbeddedToolInvocation(ctx, w, call))
	return s, b, g, call, w, producer
}

func TestEmbeddedAgentUrgencyFreshAdmission(t *testing.T) { urgencyInvocation(t) }

func TestEmbeddedAgentUrgencyFenceAndReplay(t *testing.T) {
	for _, change := range []string{"none", "owner", "process", "user"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			s, b, g, call, w, producer := urgencyInvocation(t)
			switch change {
			case "owner":
				_, err := s.db.Exec(`UPDATE embedded_sessions SET current=0 WHERE agent_id=?`, b.AgentID)
				require.NoError(t, err)
			case "process":
				_, err := s.MintEmbeddedProcessGeneration(ctx, "replacement")
				require.NoError(t, err)
			case "user":
				receiptProviderUser(t, s, b, "newer request")
			}
			result, err := s.SetEmbeddedAgentUrgency(ctx, w)
			if change != "none" {
				require.Error(t, err)
				var urgent bool
				require.NoError(t, s.db.QueryRow(`SELECT urgent FROM agents WHERE agent_id=?`, b.AgentID).Scan(&urgent))
				require.False(t, urgent)
				return
			}
			require.NoError(t, err)
			require.Equal(t, b.AgentID, result.AgentID)
			require.True(t, result.Urgent)
			checked := addressedStoreResult(t, w)
			require.NoError(t, s.CompleteEmbeddedToolInvocation(ctx, w, checked))
			body, err := json.Marshal(checked)
			require.NoError(t, err)
			_, err = s.AppendEmbeddedToolInvocationResult(ctx, w, producer.Sequence, producer.Digest, "urgency-result", checked, body)
			require.NoError(t, err)
			_, err = s.SetAgentUrgency(ctx, b.AgentID, false)
			require.NoError(t, err)
			_, err = s.SetEmbeddedAgentUrgency(ctx, w)
			require.Error(t, err)
			_, err = s.RecordEmbeddedToolInvocation(ctx, b, g, w.origin.userSeq, w.origin.userDigest, "", call)
			require.Error(t, err)
			var urgent bool
			require.NoError(t, s.db.QueryRow(`SELECT urgent FROM agents WHERE agent_id=?`, b.AgentID).Scan(&urgent))
			require.False(t, urgent)
		})
	}
}

func TestEmbeddedAgentUrgencyUnknownCannotReplayOverNewerWrite(t *testing.T) {
	ctx := context.Background()
	s, b, g, call, w, _ := urgencyInvocation(t)
	_, err := s.SetEmbeddedAgentUrgency(ctx, w)
	require.NoError(t, err)
	_, err = s.SetAgentUrgency(ctx, b.AgentID, false)
	require.NoError(t, err)
	// No Complete/Append: loss of the original checked result is not retry authority.
	_, err = s.RecordEmbeddedToolInvocation(ctx, b, g, w.origin.userSeq, w.origin.userDigest, "", call)
	require.Error(t, err)
	conflict := call
	conflict.Arguments = map[string]any{"urgent": false}
	_, err = s.RecordEmbeddedToolInvocation(ctx, b, g, w.origin.userSeq, w.origin.userDigest, "", conflict)
	require.Error(t, err)
	_, err = s.SetEmbeddedAgentUrgency(ctx, w)
	require.Error(t, err)
	var urgent bool
	require.NoError(t, s.db.QueryRow(`SELECT urgent FROM agents WHERE agent_id=?`, b.AgentID).Scan(&urgent))
	require.False(t, urgent)
	effect, err := s.RecoverEmbeddedEffect(ctx, b, call.ID)
	require.NoError(t, err)
	require.Equal(t, "unknown", effect.State)
}

func TestEmbeddedAgentUrgencyTargetsAndFailures(t *testing.T) {
	for _, target := range []string{"missing", "retired", " exact-id ", "storage-error"} {
		t.Run(target, func(t *testing.T) {
			ctx := context.Background()
			s, b, g, _, old, _ := urgencyInvocation(t)
			call := providers.FauxToolCall("mecha_agent_urgency", map[string]any{"urgent": true, "agent_id": target}, "target-original")
			// A fresh checked SDK producer for the explicit target.
			receiptSDKAppend(t, s, b, *providers.FauxAssistantMessage(ai.ContentList{call}, ai.StopToolUse))
			if target == "retired" {
				_, err := s.db.Exec(`INSERT INTO agents(agent_id,first_seen_at,identity_active) VALUES('retired','2026-01-01',0)`)
				require.NoError(t, err)
			}
			if target == " exact-id " {
				_, err := s.AcquireAgentLease(ctx, LeaseRequest{AgentID: "exact-id", InstanceID: "target", LeaseID: "target", LeaseDuration: time.Minute})
				require.NoError(t, err)
			}
			w, err := s.RecordEmbeddedToolInvocation(ctx, b, g, old.origin.userSeq, old.origin.userDigest, "", call)
			require.NoError(t, err)
			require.NoError(t, s.StartEmbeddedToolInvocation(ctx, w, call))
			if target == "storage-error" {
				require.NoError(t, s.Close())
			}
			_, err = s.SetEmbeddedAgentUrgency(ctx, w)
			require.Error(t, err)
			require.False(t, ClaimEmbeddedToolHandoff(w))
			require.Empty(t, w.domainContent)
			if target != "storage-error" {
				var count int
				require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM agents WHERE agent_id=? AND urgent=1`, target).Scan(&count))
				require.Zero(t, count)
			}
		})
	}
}

func TestEmbeddedAgentUrgencyPostCommitErrorReplayAndFreshRetry(t *testing.T) {
	ctx := context.Background()
	s, b, g, call, w, _ := urgencyInvocation(t)
	originalPath := s.path
	component := filepath.Join(t.TempDir(), "not-directory")
	require.NoError(t, os.WriteFile(component, []byte("fixture"), 0600))
	// Existing state-file permission boundary fails strictly after SQLite COMMIT.
	s.path = filepath.Join(component, "state.db")
	result, err := s.SetEmbeddedAgentUrgency(ctx, w)
	s.path = originalPath
	require.ErrorContains(t, err, "stat SQLite state file")
	require.Equal(t, AgentUrgencyResult{AgentID: b.AgentID, Urgent: true}, result)
	require.True(t, ClaimEmbeddedToolHandoff(w), "committed domain output remains publishable once")
	require.False(t, ClaimEmbeddedToolHandoff(w))
	var urgent bool
	require.NoError(t, s.db.QueryRow(`SELECT urgent FROM agents WHERE agent_id=?`, b.AgentID).Scan(&urgent))
	require.True(t, urgent)
	effect, err := s.RecoverEmbeddedEffect(ctx, b, call.ID)
	require.NoError(t, err)
	require.Equal(t, "unknown", effect.State)
	require.Empty(t, effect.Result)
	_, err = s.SetAgentUrgency(ctx, b.AgentID, false)
	require.NoError(t, err)
	_, err = s.SetEmbeddedAgentUrgency(ctx, w)
	require.Error(t, err)
	_, err = s.RecordEmbeddedToolInvocation(ctx, b, g, w.origin.userSeq, w.origin.userDigest, "", call)
	require.Error(t, err)
	require.NoError(t, s.db.QueryRow(`SELECT urgent FROM agents WHERE agent_id=?`, b.AgentID).Scan(&urgent))
	require.False(t, urgent)
	// New explicit turn/call is authority; neither saved intent nor old witness is.
	user := receiptSDKAppend(t, s, b, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "explicit retry true"}}})
	require.NoError(t, s.EnterEmbeddedNonMailProviderTurn(ctx, b, user.Sequence, user.Digest))
	fresh := providers.FauxToolCall("mecha_agent_urgency", map[string]any{"urgent": true}, "urgency-postcommit-fresh")
	producer := receiptSDKAppend(t, s, b, *providers.FauxAssistantMessage(ai.ContentList{fresh}, ai.StopToolUse))
	retry, err := s.RecordEmbeddedToolInvocation(ctx, b, g, user.Sequence, user.Digest, "", fresh)
	require.NoError(t, err)
	require.NoError(t, s.StartEmbeddedToolInvocation(ctx, retry, fresh))
	_, err = s.SetEmbeddedAgentUrgency(ctx, retry)
	require.NoError(t, err)
	checked := addressedStoreResult(t, retry)
	require.NoError(t, s.CompleteEmbeddedToolInvocation(ctx, retry, checked))
	body, err := json.Marshal(checked)
	require.NoError(t, err)
	_, err = s.AppendEmbeddedToolInvocationResult(ctx, retry, producer.Sequence, producer.Digest, "urgency-postcommit-result", checked, body)
	require.NoError(t, err)
	effect, err = s.RecoverEmbeddedEffect(ctx, b, fresh.ID)
	require.NoError(t, err)
	require.Equal(t, "observed", effect.State)
	require.NoError(t, s.db.QueryRow(`SELECT urgent FROM agents WHERE agent_id=?`, b.AgentID).Scan(&urgent))
	require.True(t, urgent)
}

// A checked known rejection still needs its original producer, exact error,
// current fence and one-use append. It is not a general error-to-success escape.
func TestEmbeddedAgentUrgencyRejectedResultFenceAndReplay(t *testing.T) {
	for _, mode := range []string{"checked-replay", "stale-owner", "forged-success", "forged-error"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s, b, g, _, old, _ := urgencyInvocation(t)
			call := providers.FauxToolCall("mecha_agent_urgency", map[string]any{"urgent": true, "agent_id": "absent"}, "rejected-target")
			producer := receiptSDKAppend(t, s, b, *providers.FauxAssistantMessage(ai.ContentList{call}, ai.StopToolUse))
			w, err := s.RecordEmbeddedToolInvocation(ctx, b, g, old.origin.userSeq, old.origin.userDigest, "", call)
			require.NoError(t, err)
			require.NoError(t, s.StartEmbeddedToolInvocation(ctx, w, call))
			_, err = s.SetEmbeddedAgentUrgency(ctx, w)
			require.ErrorIs(t, err, ErrAgentNotFound)
			require.False(t, ClaimEmbeddedToolHandoff(w))
			checked := ai.ToolResultMessage{ToolCallID: call.ID, ToolName: call.Name, Content: ai.ContentList{ai.TextContent{Text: err.Error()}}, IsError: true, Timestamp: time.Now().UnixMilli()}
			switch mode {
			case "stale-owner":
				_, err = s.db.Exec(`UPDATE embedded_sessions SET owner_token=owner_token+1 WHERE agent_id=?`, b.AgentID)
				require.NoError(t, err)
			case "forged-success":
				checked.IsError = false
			case "forged-error":
				checked.Content = ai.ContentList{ai.TextContent{Text: "different error"}}
			}
			body, err := json.Marshal(checked)
			require.NoError(t, err)
			_, err = s.AppendEmbeddedToolInvocationResult(ctx, w, producer.Sequence, producer.Digest, "rejected-result", checked, body)
			readBinding := b
			if mode == "stale-owner" {
				readBinding.OwnerToken = 0
			} // inspect durable disposition, not stale execution authority
			effect, e := s.RecoverEmbeddedEffect(ctx, readBinding, call.ID)
			require.NoError(t, e)
			if mode != "checked-replay" {
				require.Error(t, err)
				require.Equal(t, "unknown", effect.State)
				require.Empty(t, effect.Result)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "observed", effect.State)
			_, err = s.AppendEmbeddedToolInvocationResult(ctx, w, producer.Sequence, producer.Digest, "rejected-replay", checked, body)
			require.Error(t, err)
			_, err = s.RecordEmbeddedToolInvocation(ctx, b, g, old.origin.userSeq, old.origin.userDigest, "", call)
			require.Error(t, err)
			_, err = s.SetEmbeddedAgentUrgency(ctx, w)
			require.Error(t, err)
			var count int
			require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM agents WHERE agent_id='absent'`).Scan(&count))
			require.Zero(t, count)
		})
	}
}
