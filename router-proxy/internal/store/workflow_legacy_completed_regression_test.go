package store

import (
 "context"
 "os"
 "testing"

 "github.com/stretchr/testify/require"
)

func TestLegacyMigrationDoesNotFlagCompletedContinuation(t *testing.T) {
 s, b, k := evaluationFixture(t)
 defer s.Close()
 ctx := context.Background()
 receiptForEvaluation(t, s, b, &k)
 _, _, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
 require.NoError(t, err)
 _, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
 require.NoError(t, err)
 markerWorkflow(t, s, b, "legacy-completed")
 _, started, err := s.StartEmbeddedGoalContinuation(ctx, b, k)
 require.NoError(t, err)
 require.True(t, started)
 token, err := s.EnterEmbeddedSDKExecution(ctx, b, &k)
 require.NoError(t, err)
 require.NoError(t, s.ClearEmbeddedExecution(ctx, b, token))
 _, err = s.db.Exec(`DELETE FROM workflow_continuation_start_identity; DELETE FROM embedded_continuation_prompt_identity; DELETE FROM legacy_continuation_ambiguity`)
 require.NoError(t, err)
 migration, err := os.ReadFile("migrations/139_legacy_continuation_ambiguity.sql")
 require.NoError(t, err)
 _, err = s.db.Exec(string(migration))
 require.NoError(t, err)
 var n int
 require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM legacy_continuation_ambiguity WHERE agent_id=?`, b.AgentID).Scan(&n))
 require.Zero(t, n, "cleanly settled legacy continuation must not produce an interruption warning")
}
