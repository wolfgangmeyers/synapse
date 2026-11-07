package managed

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClaudeDiagnosticRetryAfterWriteFailure(t *testing.T) {
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	waitGateStarts(t, f.m)
	_, err := f.raw.Exec(`UPDATE managed_agents SET selected_model_provider='claude-code', selected_model_id='sonnet' WHERE agent_id=?`, owner.binding.AgentID)
	require.NoError(t, err)
	token, err := f.db.EnterEmbeddedSDKExecution(context.Background(), owner.binding, nil)
	require.NoError(t, err)
	defer f.db.ClearEmbeddedExecution(context.Background(), owner.binding, token)
	id := "e9999999-7777-4777-8777-777777777777"
	f.enqueue(t, id)
	_, err = f.raw.Exec(`CREATE TRIGGER diagnostic_fault BEFORE INSERT ON managed_complete_history WHEN NEW.message = 'Claude mail could not be classified safely and is blocked from automatic retry. Inspect the router logs and conversation before retrying or restarting the router.' BEGIN SELECT RAISE(ABORT,'diagnostic fault'); END`)
	require.NoError(t, err)
	f.m.dispatchEmbeddedMail(owner.binding.AgentID)
	lane := f.m.laneFor(owner.binding.AgentID)
	lane.serial.Lock()
	scheduled := lane.claudeRetryScheduled
	lane.serial.Unlock()
	require.True(t, scheduled, "failed diagnostic must schedule retry")
	require.NoError(t, f.db.ClearEmbeddedExecution(context.Background(), owner.binding, token))
	_, err = f.raw.Exec(`DROP TRIGGER diagnostic_fault`)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return f.count(t, `SELECT count(*) FROM managed_complete_history WHERE message=?`, claudeUnclassifiedDiagnostic) == 1
	}, 7*time.Second, 20*time.Millisecond)
	f.m.dispatchEmbeddedMail(owner.binding.AgentID)
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM managed_complete_history WHERE message=?`, claudeUnclassifiedDiagnostic))
	require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_acks WHERE message_id=?`, id))
	held, err := f.db.ClaudeMailHeld(context.Background(), id)
	require.NoError(t, err)
	require.False(t, held)
}

func TestClaudeDiagnosticRetryDropsStaleOwner(t *testing.T) {
	ctx := context.Background()
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	waitGateStarts(t, f.m)
	_, err := f.raw.Exec(`UPDATE managed_agents SET selected_model_provider='claude-code', selected_model_id='sonnet' WHERE agent_id=?`, owner.binding.AgentID)
	require.NoError(t, err)
	token, err := f.db.EnterEmbeddedSDKExecution(ctx, owner.binding, nil)
	require.NoError(t, err)
	id := "e9999999-7777-4777-8777-777777777777"
	f.enqueue(t, id)
	_, err = f.raw.Exec(`CREATE TRIGGER diagnostic_fault BEFORE INSERT ON managed_complete_history WHEN NEW.message = 'Claude mail could not be classified safely and is blocked from automatic retry. Inspect the router logs and conversation before retrying or restarting the router.' BEGIN SELECT RAISE(ABORT,'diagnostic fault'); END`)
	require.NoError(t, err)
	f.m.dispatchEmbeddedMail(owner.binding.AgentID)
	lane := f.m.laneFor(owner.binding.AgentID)
	require.NoError(t, f.db.ClearEmbeddedExecution(ctx, owner.binding, token))
	_, err = f.raw.Exec(`DROP TRIGGER diagnostic_fault`)
	require.NoError(t, err)
	lane.serial.Lock()
	require.NotNil(t, lane.claudePendingDiagnostic)
	require.NoError(t, f.m.resetEmbeddedContextAfterDrain(ctx, owner.binding, "stale diagnostic"))
	lane.admission.Lock()
	lane.resetPending = true
	lane.admission.Unlock()
	lane.serial.Unlock()
	f.m.dispatchEmbeddedMail(owner.binding.AgentID)
	lane.serial.Lock()
	require.Nil(t, lane.claudePendingDiagnostic)
	lane.serial.Unlock()
	require.Zero(t, f.count(t, `SELECT count(*) FROM managed_complete_history WHERE message=?`, claudeUnclassifiedDiagnostic))
	require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_acks WHERE message_id=?`, id))
}
