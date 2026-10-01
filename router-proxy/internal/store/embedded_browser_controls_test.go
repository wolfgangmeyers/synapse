package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func browserStoreFixture(t *testing.T) (*Store, EmbeddedBinding, EmbeddedGoalEvaluationKey) {
	t.Helper()
	s, b, k := evaluationFixture(t)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	_, err := s.db.Exec(`UPDATE managed_agents SET accepting_prompts=1,slot_held=1 WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	return s, b, k
}
func browserOperation(id string) *ManagedOperation {
	return &ManagedOperation{OperationID: id, RequestID: id, Method: "PUT", CanonicalPath: "/v1/managed-agents/browser/goal", CanonicalBodyHash: testManifestHash, ResponseStatus: 200, ResponseHeaders: map[string]string{"Content-Type": "application/json"}}
}
func browserGoalResponse(p *ManagedGoalPolicy) ([]byte, error) { return json.Marshal(p.Config) }

func TestEmbeddedBrowserGoalOffCancelsPendingContinuationAtomically(t *testing.T) {
	s, b, k := browserStoreFixture(t)
	ctx := context.Background()
	receiptForEvaluation(t, s, b, &k)
	_, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	require.True(t, claimed)
	_, changed, err := s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "more")
	require.NoError(t, err)
	require.True(t, changed)
	operation := browserOperation("browser-off")
	policy, err := s.UpdateEmbeddedBrowserGoal(ctx, b, ManagedGoalConfig{Settings: DefaultManagedGoalSettings()}, operation, browserGoalResponse)
	require.NoError(t, err)
	require.False(t, policy.Config.Enabled)
	continuation, err := s.EmbeddedGoalContinuationFor(ctx, b, k)
	require.NoError(t, err)
	require.Equal(t, "invalidated", continuation.State)
	_, started, err := s.StartEmbeddedGoalContinuation(ctx, b, k)
	require.NoError(t, err)
	require.False(t, started)
	saved, err := s.ManagedOperation(ctx, operation.OperationID)
	require.NoError(t, err)
	require.Equal(t, operation.ResponseBody, saved.ResponseBody)
	require.NotEmpty(t, saved.ExpiresAt)
	var count int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_goal_calls WHERE call_id=?`, operation.OperationID).Scan(&count))
	require.Zero(t, count, "a browser mutation cannot masquerade as an SDK goal call")
}

func TestEmbeddedBrowserControlReceiptFailureRollsBackMutation(t *testing.T) {
	s, b, _ := browserStoreFixture(t)
	ctx := context.Background()
	before, err := s.ManagedGoalPolicyFor(ctx, b.AgentID)
	require.NoError(t, err)
	embeddedRedFault(t, s, "browser_receipt_fault", "managed_http_operations")
	_, err = s.UpdateEmbeddedBrowserGoal(ctx, b, ManagedGoalConfig{Enabled: true, Goal: "must roll back", Settings: DefaultManagedGoalSettings()}, browserOperation("failed-browser-set"), browserGoalResponse)
	var conflict *ManagedIdempotencyConflictError
	require.ErrorAs(t, err, &conflict)
	after, err := s.ManagedGoalPolicyFor(ctx, b.AgentID)
	require.NoError(t, err)
	require.Equal(t, before, after)
	_, err = s.ManagedOperation(ctx, "failed-browser-set")
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestEmbeddedBrowserControlsRejectStaleOwnerAndPolicyCAS(t *testing.T) {
	s, b, _ := browserStoreFixture(t)
	ctx := context.Background()
	previous := DefaultManagedReadinessPolicy()
	_, err := s.db.Exec(`INSERT INTO managed_readiness_sources(agent_id,desired_policy_canonical,desired_policy_sha256) VALUES(?,?,?)`, b.AgentID, previous.Canonical, previous.SHA256)
	require.NoError(t, err)
	stale := b
	stale.OwnerToken++
	_, err = s.UpdateEmbeddedBrowserGoal(ctx, stale, ManagedGoalConfig{Enabled: true, Goal: "denied", Settings: DefaultManagedGoalSettings()}, browserOperation("stale-browser-goal"), browserGoalResponse)
	require.Error(t, err)
	require.Error(t, s.UpdateEmbeddedBrowserPolicy(ctx, stale, previous, previous, browserOperation("stale-browser-inline")))
	_, err = s.UpdateEmbeddedBrowserGoal(ctx, b, ManagedGoalConfig{Enabled: true, Goal: "stale browser revision", Settings: DefaultManagedGoalSettings()}, browserOperation("cas-browser-goal"), browserGoalResponse, 0)
	require.Error(t, err, "an older desired revision cannot overwrite current goal/settings")
	wrong := previous
	wrong.SHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	require.Error(t, s.UpdateEmbeddedBrowserPolicy(ctx, b, wrong, previous, browserOperation("cas-browser-inline")))
	current, err := s.LoadManagedDesiredPolicy(ctx, b.AgentID)
	require.NoError(t, err)
	require.Equal(t, previous, current)
	var count int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM managed_http_operations WHERE operation_id IN ('stale-browser-goal','stale-browser-inline','cas-browser-inline','cas-browser-goal')`).Scan(&count))
	require.Zero(t, count)
	_, err = s.db.Exec(`UPDATE managed_agents SET session_epoch=session_epoch+1 WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	_, err = s.UpdateEmbeddedBrowserGoal(ctx, b, ManagedGoalConfig{Enabled: true, Goal: "old session", Settings: DefaultManagedGoalSettings()}, nil, browserGoalResponse)
	require.Error(t, err)
}
