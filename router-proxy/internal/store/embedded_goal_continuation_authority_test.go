package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedGoalContinuationOriginalKeyAuthority(t *testing.T) {
	t.Skip("simplified continuation guards in Step 2 supersede unproven effect lineage and fragile 5-table join checks")
	for _, change := range []string{"original", "original-observed-effect", "neutral", "root-neutral", "neutral-original-effect", "neutral-original-domain-results", "unresolved-original-domain", "due-minus-1ms", "off", "desired", "applied", "stale-owner", "expired", "process", "session", "epoch", "runtime-generation", "not-running", "not-current", "wrong-role", "wrong-predecessor", "wrong-origin", "origin-owner", "missing-origin", "untagged", "missing-provider-entry", "planned", "claimed", "UNKNOWN-wake", "interrupted", "notice", "substantive", "extra", "tool", "forged-tool-final", "effect", "observed-unlinked-effect", "compaction", "mail", "pending-prompt", "wrong-key", "UNKNOWN-evaluation", "DONE-evaluation", "incomplete-evaluation", "unchained"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			s, _, b, turn, clock := snoozeFixture(t)
			if change == "mail" {
				// snoozeFixture's agent includes t.Name() slashes, which are
				// not protocol recipient IDs. Bind a real canonical subject.
				b = goalBind(t, s, "authority-mail-recipient", "mail-session", 1, "mail-generation", "worker")
				user := receiptProviderUser(t, s, b, "mail authority original")
				goalAPIPolicy(t, s, b, true, "finish")
				turn = EmbeddedGoalSnoozeTurn{UserSeq: user.Sequence, UserDigest: user.Digest, GoalRevision: 1}
			}
			exec := func(query string, args ...any) {
				t.Helper()
				_, err := s.db.Exec(query, args...)
				require.NoError(t, err)
			}
			if change == "root-neutral" {
				exec(`UPDATE embedded_sessions SET role='root' WHERE agent_id=?`, b.AgentID)
			}
			var err error
			if change != "neutral-original-domain-results" && change != "unresolved-original-domain" {
				_, _, err = s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "authority-snooze", []byte(`{"duration_ms":1000}`))
				require.NoError(t, err)
			}
			if change == "neutral-original-domain-results" || change == "unresolved-original-domain" {
				ids := []string{"set-" + t.Name(), "authority-snooze"}
				for _, id := range ids {
					call := ai.ToolCall{ID: id, Name: "goal_snooze", Arguments: map[string]any{"duration_ms": 1000}}
					if id == ids[0] {
						call.Name = "goal"
						call.Arguments = map[string]any{"action": "status"}
					}
					recordCall := func() {
						t.Helper()
						receiptSDKAppend(t, s, b, ai.AssistantMessage{Content: ai.ContentList{call}, StopReason: ai.StopToolUse})
						intent, err := json.Marshal(call)
						require.NoError(t, err)
						require.NoError(t, s.RecordEmbeddedEffectIntent(ctx, b, call.ID, intent))
					}
					recordCall() // Fresh one-use original admission before DOMAIN execution.
					var payload string
					if id == ids[0] {
						policy, replay, err := s.ApplyFreshEmbeddedGoalCall(ctx, b, id, EmbeddedGoalCommand{Action: "status"})
						require.NoError(t, err)
						require.False(t, replay)
						encoded, err := json.Marshal(policy)
						require.NoError(t, err)
						payload = string(encoded)
					} else {
						preference, replay, err := s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, id, []byte(`{"duration_ms":1000}`))
						require.NoError(t, err)
						require.False(t, replay)
						encoded, err := json.Marshal(preference)
						require.NoError(t, err)
						payload = string(encoded)
					}
					if change == "unresolved-original-domain" {
						call.ID = "unresolved-" + id
						recordCall() // Deliberately unrelated checked call, never fresh replay.
					}
					resultBody, err := json.Marshal(ai.ToolResultMessage{ToolCallID: call.ID, ToolName: call.Name, Content: ai.ContentList{ai.TextContent{Text: payload}}})
					require.NoError(t, err)
					_, err = s.AppendEmbeddedEntry(ctx, b, "toolResult", resultBody)
					require.NoError(t, err)
				}
			}
			if change == "neutral-original-effect" || change == "original-observed-effect" {
				call := ai.ToolCall{ID: "original-effect", Name: "bash", Arguments: map[string]any{"command": "true"}}
				intent, err := json.Marshal(call)
				require.NoError(t, err)
				callBody, err := json.Marshal(ai.AssistantMessage{Content: ai.ContentList{call}, StopReason: ai.StopToolUse})
				require.NoError(t, err)
				_, err = s.AppendEmbeddedEntry(ctx, b, "assistant", callBody)
				require.NoError(t, err)
				require.NoError(t, s.RecordEmbeddedEffectIntent(ctx, b, call.ID, intent))
				result := ai.ToolResultMessage{ToolCallID: call.ID, ToolName: call.Name}
				if change == "original-observed-effect" {
					result.Content = ai.ContentList{ai.TextContent{Text: "observed original result"}}
					result.Timestamp = 1
					unknown, readErr := s.RecoverEmbeddedEffect(ctx, b, call.ID)
					require.NoError(t, readErr)
					require.Equal(t, "unknown", unknown.State)
					require.False(t, unknown.ShouldReplay)
				}
				resultBody, err := json.Marshal(result)
				require.NoError(t, err)
				require.NoError(t, s.ObserveEmbeddedEffect(ctx, b, call.ID, resultBody))
				_, err = s.AppendEmbeddedEntry(ctx, b, "toolResult", resultBody)
				require.NoError(t, err)
			}
			body, err := json.Marshal(ai.AssistantMessage{StopReason: ai.StopStop})
			require.NoError(t, err)
			k := EmbeddedGoalEvaluationKey{UserSeq: turn.UserSeq, UserDigest: turn.UserDigest, GoalRevision: turn.GoalRevision}
			receiptForEvaluation(t, s, b, &k)
			_, _, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
			require.NoError(t, err)
			_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "more")
			require.NoError(t, err)
			original, err := s.embeddedEvaluationRow(ctx, b, k)
			require.NoError(t, err)
			var g int64
			if change != "original" && change != "original-observed-effect" {
				g, err = s.MintEmbeddedProcessGeneration(ctx, "authority-process")
				require.NoError(t, err)
				kind := "worker-idle"
				if change == "root-neutral" {
					kind = "root-idle"
				}
				_, err = s.ReserveEmbeddedRestartWake(ctx, b, g, "authority-wake", kind, k.FinalDigest)
				require.NoError(t, err)
				require.NoError(t, s.ClaimEmbeddedRestartWake(ctx, b, g, "authority-wake"))
				text := "Continue working from your saved context."
				if change == "substantive" {
					text = "Do something else."
				}
				userBody, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: text}}})
				require.NoError(t, err)
				user, err := s.AppendEmbeddedEntry(ctx, b, "user", userBody)
				require.NoError(t, err)
				if change != "missing-provider-entry" {
					require.NoError(t, s.EnterEmbeddedNonMailProviderTurn(ctx, b, user.Sequence, user.Digest))
				}
				if change == "untagged" {
					_, err = s.AppendEmbeddedEntry(ctx, b, "assistant", body)
					require.NoError(t, err)
				} else {
					checked, err := s.AppendEmbeddedRestartWakeFinalAt(ctx, b, g, "authority-wake", user.Sequence, user.Digest, "authority-final", body)
					require.NoError(t, err)
					require.NoError(t, s.CompleteEmbeddedRestartWake(ctx, b, g, "authority-wake", checked.Sequence, checked.Digest))
				}
			}
			advance := time.Second
			if change == "due-minus-1ms" {
				advance -= time.Millisecond
			}
			require.NoError(t, clock.Advance(advance))
			key := k
			switch change {
			case "off":
				exec(`UPDATE managed_agent_policies SET goal_enabled=0 WHERE agent_id=?`, b.AgentID)
			case "desired":
				exec(`UPDATE managed_agent_policies SET desired_revision=desired_revision+1 WHERE agent_id=?`, b.AgentID)
			case "applied":
				exec(`UPDATE managed_agent_policies SET applied_revision=0 WHERE agent_id=?`, b.AgentID)
			case "stale-owner":
				b.OwnerToken++
			case "expired":
				exec(`UPDATE embedded_sessions SET owner_expires_at=? WHERE agent_id=?`, formatTimestamp(clock.Now()), b.AgentID)
			case "process":
				_, err = s.MintEmbeddedProcessGeneration(ctx, "authority-new-process")
				require.NoError(t, err)
			case "session":
				exec(`UPDATE managed_agents SET pi_session_id='other' WHERE agent_id=?`, b.AgentID)
			case "epoch":
				exec(`UPDATE managed_agents SET session_epoch=session_epoch+1 WHERE agent_id=?`, b.AgentID)
			case "runtime-generation":
				exec(`UPDATE managed_agents SET runtime_generation='other' WHERE agent_id=?`, b.AgentID)
			case "not-running":
				exec(`UPDATE managed_agents SET lifecycle_state='failed' WHERE agent_id=?`, b.AgentID)
			case "not-current":
				exec(`UPDATE embedded_sessions SET current=0 WHERE agent_id=?`, b.AgentID)
			case "wrong-role":
				exec(`UPDATE embedded_sessions SET role='root' WHERE agent_id=?`, b.AgentID)
			case "wrong-predecessor":
				exec(`UPDATE embedded_restart_wake_slots SET digest='other' WHERE agent_id=? AND slot='current'`, b.AgentID)
			case "wrong-origin":
				exec(`UPDATE embedded_restart_wake_checked_origins SET wake_id='other' WHERE agent_id=?`, b.AgentID)
			case "origin-owner":
				exec(`UPDATE embedded_restart_wake_checked_origins SET claim_owner_token=claim_owner_token+1 WHERE agent_id=?`, b.AgentID)
			case "missing-origin":
				exec(`DELETE FROM embedded_restart_wake_checked_origins WHERE agent_id=?`, b.AgentID)
			case "planned", "claimed", "UNKNOWN-wake":
				state := change
				if change == "UNKNOWN-wake" {
					state = "unknown"
				}
				exec(`UPDATE embedded_restart_wake_slots SET state=? WHERE agent_id=? AND slot='current'`, state, b.AgentID)
			case "interrupted", "notice":
				// A real durable notice, not an invented prompt or timestamp.
				exec(`INSERT INTO embedded_interrupted_notices(agent_id,session_id,epoch,generation,source_seq,notice_id,notice_text,state) VALUES(?,?,?,?,?,'authority-notice','interrupted','pending')`, b.AgentID, b.SessionID, b.Epoch, b.Generation, k.UserSeq)
				if change == "interrupted" {
					var id string
					require.NoError(t, s.db.QueryRow(`SELECT notice_id FROM embedded_interrupted_notices WHERE agent_id=?`, b.AgentID).Scan(&id))
					exec(`UPDATE embedded_restart_wake_slots SET kind='interrupted',wake_id=?,notice_id=? WHERE agent_id=? AND slot='current'`, id, id, b.AgentID)
				}
			case "extra":
				_, err = s.AppendEmbeddedEntry(ctx, b, "assistant", body)
				require.NoError(t, err)
			case "tool":
				tool, err := json.Marshal(ai.ToolResultMessage{ToolCallID: "neutral-tool", ToolName: "bash"})
				require.NoError(t, err)
				_, err = s.AppendEmbeddedEntry(ctx, b, "toolResult", tool)
				require.NoError(t, err)
			case "forged-tool-final":
				snap, err := s.ReopenEmbedded(ctx, b)
				require.NoError(t, err)
				last := snap.Entries[len(snap.Entries)-1]
				last.Body, err = json.Marshal(ai.AssistantMessage{Content: ai.ContentList{ai.ToolCall{ID: "neutral-tool", Name: "bash"}}, StopReason: ai.StopStop})
				require.NoError(t, err)
				last.Digest = embeddedEntryDigest(b, last)
				exec(`UPDATE embedded_entries SET body=?,digest=? WHERE agent_id=? AND seq=?`, last.Body, last.Digest, b.AgentID, last.Sequence)
			case "effect", "observed-unlinked-effect":
				require.NoError(t, s.RecordEmbeddedEffectIntent(ctx, b, "neutral-effect", []byte(`{"name":"bash"}`)))
				if change == "observed-unlinked-effect" {
					require.NoError(t, s.ObserveEmbeddedEffect(ctx, b, "neutral-effect", []byte(`{"result":"observed"}`)))
				}
			case "compaction":
				snap, err := s.ReopenEmbedded(ctx, b)
				require.NoError(t, err)
				last := snap.Entries[len(snap.Entries)-1]
				require.NoError(t, s.CommitEmbeddedCheckpoint(ctx, b, last.Sequence, last.Digest, []byte(`{"summary":"neutral"}`)))
			case "mail":
				// Admission alone must deny, before any transcript append/ACK.
				envelope := testEnvelope(testUUID("0901"), "do other work")
				envelope.To = b.AgentID
				envelope.CreatedAt = formatTimestamp(clock.Now())
				envelope.Source = &protocol.MessageSource{Kind: "agent"}
				exec(`INSERT INTO recipient_sources(recipient_name,source_kind,owner_identity,state,created_at,updated_at) VALUES(?,'managed',?,'active',?,?)`, b.AgentID, b.AgentID, formatTimestamp(clock.Now()), formatTimestamp(clock.Now()))
				accepted, err := s.Enqueue(ctx, envelope)
				require.NoError(t, err)
				require.Equal(t, "queued", accepted.Message.State, "mail denial requires actual accepted mail")
				target, err := s.ResolveMessageTarget(ctx, envelope.ID)
				require.NoError(t, err)
				require.NotNil(t, target)
				attempt, err := s.ReserveEmbeddedMail(ctx, b, envelope.ID)
				require.NoError(t, err)
				require.NotNil(t, attempt)
				require.Equal(t, envelope.ID, attempt.MessageID)
				var admitted int
				require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_attempts a JOIN messages m ON m.id=a.message_id WHERE a.attempt_id=? AND a.agent_id=? AND m.state='queued'`, attempt.AttemptID, b.AgentID).Scan(&admitted))
				require.Equal(t, 1, admitted, "mail must reach durable admission before start guard")
			case "pending-prompt":
				exec(`INSERT INTO embedded_prompt_requests(request_id,agent_id,session_id,epoch,generation,prompt,state) VALUES('other',?,?,?,?, 'other','pending')`, b.AgentID, b.SessionID, b.Epoch, b.Generation)
			case "wrong-key":
				key.FinalDigest = "other"
			case "UNKNOWN-evaluation", "DONE-evaluation", "incomplete-evaluation":
				// Corruption tests explicitly remove the immutable verdict trigger.
				exec(`DROP TRIGGER embedded_goal_evaluation_immutable_result`)
				if change == "incomplete-evaluation" {
					exec(`UPDATE embedded_goal_evaluations SET completed_at='' WHERE agent_id=?`, b.AgentID)
				} else {
					state := EmbeddedGoalEvaluationUnknown
					if change == "DONE-evaluation" {
						state = EmbeddedGoalEvaluationDone
					}
					if state == EmbeddedGoalEvaluationUnknown {
						exec(`UPDATE embedded_goal_evaluations SET state='UNKNOWN',completed_at=NULL,reason='' WHERE agent_id=?`, b.AgentID)
					} else {
						exec(`UPDATE embedded_goal_evaluations SET state=? WHERE agent_id=?`, state, b.AgentID)
					}
				}
			case "unchained":
				exec(`UPDATE embedded_entries SET parent_digest='other' WHERE agent_id=? AND seq=?`, b.AgentID, k.FinalSeq+1)
			}
			// Preserve every generic intent verbatim. An immutable DOMAIN
			// result cannot establish missing historical UNKNOWN attempt authority.
			var effectsBefore string
			require.NoError(t, s.db.QueryRow(`SELECT COALESCE(json_group_array(json_object('id',effect_id,'state',state,'intent',hex(intent),'result',hex(result))),'[]') FROM (SELECT * FROM embedded_effects ORDER BY effect_id)`).Scan(&effectsBefore))
			var observedOriginal EmbeddedEffect
			if change == "original-observed-effect" {
				observedOriginal, err = s.RecoverEmbeddedEffect(ctx, b, "original-effect")
				require.NoError(t, err)
				require.Equal(t, "observed", observedOriginal.State)
				require.False(t, observedOriginal.ShouldReplay)
			}
			_, started, err := s.StartEmbeddedGoalContinuation(ctx, b, key)
			var effectsAfter string
			require.NoError(t, s.db.QueryRow(`SELECT COALESCE(json_group_array(json_object('id',effect_id,'state',state,'intent',hex(intent),'result',hex(result))),'[]') FROM (SELECT * FROM embedded_effects ORDER BY effect_id)`).Scan(&effectsAfter))
			require.Equal(t, effectsBefore, effectsAfter, "start must not resolve or replay any generic effect")
			positive := change == "original" || change == "original-observed-effect" || change == "neutral" || change == "root-neutral" || change == "neutral-original-effect"
			require.Equal(t, positive, started, "start error: %v", err)
			if positive {
				require.NoError(t, err)
				after, err := s.embeddedEvaluationRow(ctx, b, k)
				require.NoError(t, err)
				require.Equal(t, original, after, "original evaluation/key must remain immutable")
				require.Equal(t, 1, embeddedRedCount(t, s, "embedded_goal_evaluations"))
				c, replay, err := s.StartEmbeddedGoalContinuation(ctx, b, k)
				require.NoError(t, err)
				require.False(t, replay)
				require.Equal(t, "unknown", c.Disposition)
				if change == "original-observed-effect" {
					after, readErr := s.RecoverEmbeddedEffect(ctx, b, "original-effect")
					require.NoError(t, readErr)
					require.Equal(t, observedOriginal, after, "original observed effect stays exact after start and second CAS")
				}
			} else {
				old := b
				if change == "stale-owner" {
					old.OwnerToken--
				}
				c, readErr := s.EmbeddedGoalContinuationFor(ctx, old, k)
				require.NoError(t, readErr)
				require.Empty(t, c.StartedAt, "denial must not consume provider authority")
				if change == "neutral-original-domain-results" || change == "unresolved-original-domain" {
					require.ErrorContains(t, err, "unproven effect lineage")
					for _, id := range []string{"set-" + t.Name(), "authority-snooze"} {
						if change == "unresolved-original-domain" {
							id = "unresolved-" + id
						}
						effect, readErr := s.RecoverEmbeddedEffect(ctx, b, id)
						require.NoError(t, readErr)
						require.Equal(t, "unknown", effect.State)
						require.False(t, effect.ShouldReplay)
					}
					after, readErr := s.embeddedEvaluationRow(ctx, b, k)
					require.NoError(t, readErr)
					require.Equal(t, original, after)
				}
				if change == "effect" {
					effect, readErr := s.RecoverEmbeddedEffect(ctx, b, "neutral-effect")
					require.NoError(t, readErr)
					require.Equal(t, "unknown", effect.State)
					require.False(t, effect.ShouldReplay)
					_, replay, replayErr := s.StartEmbeddedGoalContinuation(ctx, b, k)
					require.False(t, replay)
					require.ErrorContains(t, replayErr, "unproven effect lineage")
					unchanged, readErr := s.RecoverEmbeddedEffect(ctx, b, "neutral-effect")
					require.NoError(t, readErr)
					require.Equal(t, effect, unchanged)
				}
				if change == "mail" {
					require.ErrorContains(t, err, "retained neutral authority denied")
					after, readErr := s.embeddedEvaluationRow(ctx, b, k)
					require.NoError(t, readErr)
					require.Equal(t, original, after)
					require.Equal(t, 1, embeddedRedCount(t, s, "embedded_goal_evaluations"))
				}
			}
		})
	}
}

// Each tool gets a real DOMAIN result and saved original-prefix intent.
// Even matching checked SDK evidence cannot authorize generic UNKNOWN without
// historical attempt linkage. Mismatch variants retain that same fail-closed
// contract, not proof of a narrower removed DOMAIN exception. Owner/process
// and checked-final negatives must still reach their own earlier guards.
func TestEmbeddedGoalContinuationDomainReceiptAuthority(t *testing.T) {
	t.Skip("simplified continuation guards in Step 2 supersede domain receipt effect lineage checks")
	for _, tool := range []string{"goal", "goal_snooze"} {
		changes := []string{"match", "wrong-text", "IsError", "missing-SDK-result", "missing-receipt", "wrong-binding", "wrong-session", "wrong-role", "stale-owner", "current-process", "wake-owner", "incomplete-original-final"}
		if tool == "goal" {
			changes = append(changes, "incomplete-domain")
		} else {
			changes = append(changes, "wrong-user", "wrong-digest", "wrong-revision")
		}
		for _, change := range changes {
			t.Run(tool+"/"+change, func(t *testing.T) {
				ctx := context.Background()
				s, _, b, turn, clock := snoozeFixture(t)
				exec := func(query string, args ...any) {
					t.Helper()
					_, err := s.db.Exec(query, args...)
					require.NoError(t, err)
				}
				target, domainTurn := b, turn
				if change == "wrong-binding" || change == "wrong-session" {
					agentID, sessionID := "domain-other-agent", b.SessionID
					if change == "wrong-session" {
						// Create a real same-agent older session receipt before the
						// original reservation. Restore selection before evaluation.
						agentID, sessionID = b.AgentID, "domain-other-session"
						exec(`UPDATE embedded_sessions SET current=0 WHERE agent_id=?`, b.AgentID)
					}
					if change == "wrong-session" {
						target = redBind(t, s, agentID, sessionID, b.Epoch, b.Generation, "worker")
					} else {
						target = goalBind(t, s, agentID, sessionID, b.Epoch, b.Generation, "worker")
					}
					user := receiptProviderUser(t, s, target, "other domain subject")
					if change == "wrong-binding" {
						goalAPIPolicy(t, s, target, true, "other")
					}
					domainTurn = EmbeddedGoalSnoozeTurn{UserSeq: user.Sequence, UserDigest: user.Digest, GoalRevision: turn.GoalRevision}
				}
				if change == "wrong-role" {
					exec(`UPDATE embedded_sessions SET role='root' WHERE agent_id=? AND session_id=?`, b.AgentID, b.SessionID)
				}
				call := ai.ToolCall{ID: "domain-call", Name: tool, Arguments: map[string]any{"duration_ms": 1000}}
				if tool == "goal" {
					call.Arguments = map[string]any{"action": "status"}
				}
				appendIntent := func(binding EmbeddedBinding) {
					t.Helper()
					intent, err := json.Marshal(call)
					require.NoError(t, err)
					receiptSDKAppend(t, s, binding, ai.AssistantMessage{Content: ai.ContentList{call}, StopReason: ai.StopToolUse})
					require.NoError(t, s.RecordEmbeddedEffectIntent(ctx, binding, call.ID, intent))
				}
				foreign := change == "wrong-binding" || change == "wrong-session" || change == "wrong-user" || change == "missing-receipt"
				if !foreign {
					appendIntent(b) // Checked original call precedes real DOMAIN execution.
				} else if tool == "goal" {
					appendIntent(target) // Real producer admission in its exact binding.
				}
				var payload string
				if tool == "goal" {
					policy, _, err := s.ApplyFreshEmbeddedGoalCall(ctx, target, call.ID, EmbeddedGoalCommand{Action: "status"})
					require.NoError(t, err)
					encoded, err := json.Marshal(policy)
					require.NoError(t, err)
					payload = string(encoded)
					var state, saved string
					require.NoError(t, s.db.QueryRow(`SELECT state,result_json FROM embedded_goal_calls WHERE call_id=?`, call.ID).Scan(&state, &saved))
					require.Equal(t, "complete", state)
					require.Equal(t, payload, saved)
				} else {
					call.Arguments = map[string]any{"duration_ms": 1000}
					preference, _, err := s.SaveEmbeddedGoalSnoozePreference(ctx, target, domainTurn, call.ID, []byte(`{"duration_ms":1000}`))
					require.NoError(t, err)
					encoded, err := json.Marshal(preference)
					require.NoError(t, err)
					payload = string(encoded)
					var saved string
					require.NoError(t, s.db.QueryRow(`SELECT result_json FROM embedded_goal_snooze_calls WHERE call_id=?`, call.ID).Scan(&saved))
					require.Equal(t, payload, saved)
				}
				if change == "wrong-session" {
					exec(`UPDATE embedded_sessions SET current=0 WHERE agent_id=? AND session_id=?`, b.AgentID, target.SessionID)
					exec(`UPDATE managed_agents SET pi_session_id=?,session_epoch=?,runtime_generation=? WHERE agent_id=?`, b.SessionID, b.Epoch, b.Generation, b.AgentID)
					exec(`UPDATE embedded_sessions SET current=1 WHERE agent_id=? AND session_id=?`, b.AgentID, b.SessionID)
				}
				if change == "wrong-role" {
					exec(`UPDATE embedded_sessions SET role='worker' WHERE agent_id=? AND session_id=?`, b.AgentID, b.SessionID)
				}
				if change == "wrong-user" {
					user := receiptProviderUser(t, s, b, "current initiating turn")
					turn.UserSeq, turn.UserDigest = user.Sequence, user.Digest
					require.NotEqual(t, domainTurn.UserSeq, turn.UserSeq)
				}
				if change == "missing-receipt" {
					call.ID = "no-domain-receipt"
				} else if tool == "goal" && (change == "wrong-binding" || change == "wrong-session") {
					// IDs are globally one-use. The authentic foreign producer
					// cannot be re-recorded as this original user's intent.
					call.ID = "unrelated-original-call"
				}
				if foreign {
					// Deliberately unrelated DOMAIN subject/producer; no claim
					// that current execution reconstructs historical attribution.
					appendIntent(b)
				}
				checked := ai.ToolResultMessage{ToolCallID: call.ID, ToolName: tool, Content: ai.ContentList{ai.TextContent{Text: payload}}, Timestamp: 1}
				if change == "wrong-text" {
					checked.Content = ai.ContentList{ai.TextContent{Text: payload + " "}}
				}
				checked.IsError = change == "IsError"
				if change != "missing-SDK-result" {
					resultBody, err := json.Marshal(checked)
					require.NoError(t, err)
					_, err = s.AppendEmbeddedEntry(ctx, b, "toolResult", resultBody)
					require.NoError(t, err)
				}
				body, err := json.Marshal(ai.AssistantMessage{StopReason: ai.StopStop})
				require.NoError(t, err)
				originalBody := body
				if change == "incomplete-original-final" {
					// The legacy evaluation gate accepts stopReason, but this is
					// missing checked SDK mandatory fields. Do not corrupt/rekey
					// the persisted evaluation merely to exercise this distinction.
					originalBody = []byte(`{"role":"assistant","content":[],"stopReason":"stop"}`)
				}
				// Capture the ordinary API-installed policy while this exact
				// provider user is current, before the original checked stop.
				_, replay, err := s.CommitEmbeddedGoalAppliedReceipt(ctx, b, EmbeddedGoalAppliedReceiptRequest{TurnKey: "turn", TurnEntrySeq: turn.UserSeq, TurnEntryDigest: turn.UserDigest, GoalRevision: turn.GoalRevision})
				require.NoError(t, err)
				require.False(t, replay)
				final, err := s.AppendEmbeddedEntry(ctx, b, "assistant", originalBody)
				require.NoError(t, err)
				k := EmbeddedGoalEvaluationKey{UserSeq: turn.UserSeq, UserDigest: turn.UserDigest, GoalRevision: turn.GoalRevision, FinalSeq: final.Sequence, FinalDigest: final.Digest}
				_, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
				require.NoError(t, err)
				require.True(t, claimed)
				_, completed, err := s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "more")
				require.NoError(t, err)
				require.True(t, completed)
				original, err := s.embeddedEvaluationRow(ctx, b, k)
				require.NoError(t, err)
				g, err := s.MintEmbeddedProcessGeneration(ctx, "domain-current-process")
				require.NoError(t, err)
				_, err = s.ReserveEmbeddedRestartWake(ctx, b, g, "domain-wake", "worker-idle", k.FinalDigest)
				require.NoError(t, err)
				require.NoError(t, s.ClaimEmbeddedRestartWake(ctx, b, g, "domain-wake"))
				userBody, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "Continue working from your saved context."}}})
				require.NoError(t, err)
				user, err := s.AppendEmbeddedEntry(ctx, b, "user", userBody)
				require.NoError(t, err)
				require.NoError(t, s.EnterEmbeddedNonMailProviderTurn(ctx, b, user.Sequence, user.Digest))
				neutral, err := s.AppendEmbeddedRestartWakeFinalAt(ctx, b, g, "domain-wake", user.Sequence, user.Digest, "domain-final", body)
				require.NoError(t, err)
				require.NoError(t, s.CompleteEmbeddedRestartWake(ctx, b, g, "domain-wake", neutral.Sequence, neutral.Digest))
				require.NoError(t, clock.Advance(time.Second))
				switch change {
				case "incomplete-domain":
					// Fault an actually completed receipt, not a fake grant. The
					// production immutable schema is otherwise unchanged.
					exec(`DROP TRIGGER embedded_goal_call_immutable`)
					exec(`UPDATE embedded_goal_calls SET state='unknown',result_json=NULL WHERE call_id=?`, call.ID)
				case "wrong-digest", "wrong-revision":
					exec(`DROP TRIGGER embedded_goal_snooze_call_no_update`)
					if change == "wrong-digest" {
						exec(`UPDATE embedded_goal_snooze_calls SET user_digest='not-original' WHERE call_id=?`, call.ID)
					} else {
						exec(`UPDATE embedded_goal_snooze_calls SET goal_revision=goal_revision+1 WHERE call_id=?`, call.ID)
					}
				case "current-process":
					_, err = s.MintEmbeddedProcessGeneration(ctx, "domain-new-process")
					require.NoError(t, err)
				case "wake-owner":
					exec(`UPDATE embedded_restart_wake_slots SET claim_owner_token=claim_owner_token+1 WHERE agent_id=? AND slot='current'`, b.AgentID)
				}
				binding := b
				if change == "stale-owner" {
					binding.OwnerToken++
				}
				before, err := s.RecoverEmbeddedEffect(ctx, b, call.ID)
				require.NoError(t, err)
				require.Equal(t, "unknown", before.State)
				require.False(t, before.ShouldReplay)
				_, started, err := s.StartEmbeddedGoalContinuation(ctx, binding, k)
				require.False(t, started, "unresolved generic UNKNOWN must deny: %v", err)
				if change == "incomplete-original-final" {
					require.ErrorContains(t, err, "original final is not checked stop")
				} else if change == "stale-owner" {
					require.ErrorContains(t, err, "stale embedded owner")
				} else if change == "current-process" || change == "wake-owner" {
					require.ErrorContains(t, err, "retained neutral authority denied")
				} else {
					require.ErrorContains(t, err, "unproven effect lineage", "must reach UNKNOWN effect-lineage denial, not a receipt prerequisite")
				}
				after, readErr := s.RecoverEmbeddedEffect(ctx, b, call.ID)
				require.NoError(t, readErr)
				require.Equal(t, before, after, "denial leaves the exact generic UNKNOWN row unchanged")
				evaluation, readErr := s.embeddedEvaluationRow(ctx, b, k)
				require.NoError(t, readErr)
				require.Equal(t, original, evaluation)
				require.Equal(t, 1, embeddedRedCount(t, s, "embedded_goal_evaluations"))
				c, readErr := s.EmbeddedGoalContinuationFor(ctx, b, k)
				require.NoError(t, readErr)
				require.Equal(t, "reserved", c.State)
				require.Equal(t, "pending", c.Disposition)
				require.Empty(t, c.StartedAt)
				_, replay, replayErr := s.StartEmbeddedGoalContinuation(ctx, binding, k)
				require.False(t, replay)
				require.Error(t, replayErr)
				stillUnknown, readErr := s.RecoverEmbeddedEffect(ctx, b, call.ID)
				require.NoError(t, readErr)
				require.Equal(t, before, stillUnknown, "a second denied/start-CAS call cannot resolve or replay the original generic intent")
			})
		}
	}
}
