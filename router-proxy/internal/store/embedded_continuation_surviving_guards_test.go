package store

import (
	"context"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// Retires the old blanket UNKNOWN/DOMAIN-lineage hold, not UNKNOWN no-replay.
// Even matching DOMAIN/SDK results do not observe a generic intent.
func TestEmbeddedContinuationSurvivingUnknownNoReplay(t *testing.T) {
	for _, head := range []string{"original", "neutral"} {
		for _, tool := range []string{"goal", "goal_snooze", "bash"} {
			t.Run(head+"/"+tool, func(t *testing.T) {
				ctx := context.Background()
				s, b, k, clock, id := unknownContinuationFixture(t, tool, head)
				effect, err := s.RecoverEmbeddedEffect(ctx, b, id)
				require.NoError(t, err)
				require.Equal(t, "unknown", effect.State)
				require.False(t, effect.ShouldReplay)
				evaluation, err := s.EmbeddedEvaluationRow(ctx, b, k)
				require.NoError(t, err)
				reservation, err := s.EmbeddedGoalContinuationFor(ctx, b, k)
				require.NoError(t, err)
				require.NoError(t, clock.Advance(time.Second-time.Millisecond))
				_, started, err := s.StartEmbeddedGoalContinuation(ctx, b, k)
				require.NoError(t, err)
				require.False(t, started)
				beforeDue, err := s.EmbeddedGoalContinuationFor(ctx, b, k)
				require.NoError(t, err)
				require.Equal(t, reservation, beforeDue)
				require.NoError(t, clock.Advance(time.Millisecond))
				for attempt := 0; attempt < 2; attempt++ {
					c, started, err := s.StartEmbeddedGoalContinuation(ctx, b, k)
					require.NoError(t, err)
					require.Equal(t, attempt == 0, started)
					require.Equal(t, "started", c.State)
					require.Equal(t, "unknown", c.Disposition)
					after, err := s.RecoverEmbeddedEffect(ctx, b, id)
					require.NoError(t, err)
					require.Equal(t, effect, after, "later start cannot resolve or replay historical UNKNOWN")
					saved, err := s.EmbeddedEvaluationRow(ctx, b, k)
					require.NoError(t, err)
					require.Equal(t, evaluation, saved)
				}
			})
		}
	}
}

func TestEmbeddedContinuationSurvivingStartFences(t *testing.T) {
	for _, change := range []string{"owner", "session", "epoch", "generation", "final-key", "corrupt-UNKNOWN", "corrupt-DONE", "corrupt-incomplete"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			s, b, k, clock, _ := unknownContinuationFixture(t, "", "original")
			before, err := s.EmbeddedGoalContinuationFor(ctx, b, k)
			require.NoError(t, err)
			require.NoError(t, clock.Advance(time.Second))
			caller, key := b, k
			switch change {
			case "owner":
				caller.OwnerToken++
			case "session":
				caller.SessionID = "other"
			case "epoch":
				caller.Epoch++
			case "generation":
				caller.Generation = "other"
			case "final-key":
				key.FinalDigest = "other"
			default:
				// Deliberate database corruption only: production immutable-result guard
				// otherwise makes a completed NOT_DONE impossible to change this way.
				_, err = s.db.Exec(`DROP TRIGGER embedded_goal_evaluation_immutable_result`)
				require.NoError(t, err)
				switch change {
				case "corrupt-UNKNOWN":
					_, err = s.db.Exec(`UPDATE embedded_goal_evaluations SET state='UNKNOWN',completed_at=NULL,reason=''`)
				case "corrupt-DONE":
					_, err = s.db.Exec(`UPDATE embedded_goal_evaluations SET state='DONE'`)
				case "corrupt-incomplete":
					_, err = s.db.Exec(`UPDATE embedded_goal_evaluations SET completed_at=''`)
				}
				require.NoError(t, err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				_, started, err := s.StartEmbeddedGoalContinuation(ctx, caller, key)
				require.Error(t, err)
				require.False(t, started)
				after, err := s.EmbeddedGoalContinuationFor(ctx, b, k)
				require.NoError(t, err)
				require.Equal(t, before, after, "denial must not consume original CAS")
			}
		})
	}
}

// Semantic-head checks belong to admission of a new evaluation. A saved
// NOT_DONE source is not required to remain the physical transcript head.
func TestEmbeddedContinuationSurvivingEvaluationHead(t *testing.T) {
	for _, change := range []string{"valid", "new-user", "extra-final", "tool-final", "wrong-final-digest"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			s, b, k := evaluationFixture(t)
			defer s.Close()
			user := EmbeddedEntry{Sequence: k.UserSeq, Digest: k.UserDigest}
			_, replay, err := s.CommitEmbeddedGoalAppliedReceipt(ctx, b, receiptRequest("head", user, k.GoalRevision))
			require.NoError(t, err)
			require.False(t, replay)
			stop := ai.StopStop
			if change == "tool-final" {
				stop = ai.StopToolUse
			}
			final := receiptSDKAppend(t, s, b, *providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "more work"}}, stop))
			k.FinalSeq, k.FinalDigest = final.Sequence, final.Digest
			switch change {
			case "new-user":
				receiptProviderUser(t, s, b, "new work")
			case "extra-final":
				receiptSDKAppend(t, s, b, *providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "later"}}, ai.StopStop))
			case "wrong-final-digest":
				k.FinalDigest = "other"
			}
			_, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
			if change == "valid" {
				require.NoError(t, err)
				require.True(t, claimed)
			} else {
				require.Error(t, err)
				require.False(t, claimed)
			}
		})
	}
}

// These are Store-vs-routing boundary controls, NOT permission for Manager to
// ignore queued mail/prompts. Start does not own routing selection. Checked
// wake provenance is enforced by wake APIs, not a historical lineage rejoin.
func TestEmbeddedContinuationSurvivingRoutingBoundary(t *testing.T) {
	for _, change := range []string{"pending-mail", "pending-prompt", "new-user", "new-process"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			s, b, k, clock, _ := unknownContinuationFixture(t, "", "neutral")
			switch change {
			case "pending-mail":
				require.NoError(t, s.ReconcileManagedRecipientSources(ctx))
				envelope := testEnvelope(testUUID("0901"), "new work")
				envelope.To = b.AgentID
				envelope.CreatedAt = formatTimestamp(clock.Now())
				envelope.Source = &protocol.MessageSource{Kind: "agent"}
				accepted, err := s.Enqueue(ctx, envelope)
				require.NoError(t, err)
				require.Equal(t, "queued", accepted.Message.State)
				target, err := s.ResolveMessageTarget(ctx, envelope.ID)
				require.NoError(t, err)
				require.NotNil(t, target)
				next, err := s.NextEmbeddedMail(ctx, b.AgentID)
				require.NoError(t, err)
				require.Equal(t, envelope.ID, next, "Manager's idle routing gate sees actual queued mail")
			case "pending-prompt":
				replay, err := s.ReserveEmbeddedPrompt(ctx, b, "pending-prompt", "new work")
				require.NoError(t, err)
				require.False(t, replay)
				_, err = s.ReserveEmbeddedPrompt(ctx, b, "pending-prompt", "new work")
				require.ErrorContains(t, err, "outcome unknown")
			case "new-user":
				receiptProviderUser(t, s, b, "substantive new input")
			case "new-process":
				_, err := s.MintEmbeddedProcessGeneration(ctx, "replacement-process")
				require.NoError(t, err)
			}
			require.NoError(t, clock.Advance(time.Second))
			_, started, err := s.StartEmbeddedGoalContinuation(ctx, b, k)
			require.NoError(t, err)
			require.True(t, started, "do not invent historical lineage restrictions at Store start")
		})
	}
}
