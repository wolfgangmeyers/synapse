package managed

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBackgroundBashRecoveryNoticesUseDistinctStoreIDs(t *testing.T) {
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	waitGateStarts(t, f.m)
	now := time.Date(2026, 9, 27, 23, 59, 59, 0, time.UTC)
	job := BackgroundBashJob{ID: "recovery-store", Owner: backgroundBashOwnerKey(owner), Status: "uncertain", FinishedAt: now}
	f.m.backgroundBashCompleted(job)
	job.Status = "succeeded"
	f.m.backgroundBashCompleted(job)
	uncertain, err := f.db.MessageFor(context.Background(), "background-bash-completed-recovery-store-uncertain")
	require.NoError(t, err)
	require.NotNil(t, uncertain)
	recovered, err := f.db.MessageFor(context.Background(), "background-bash-completed-recovery-store")
	require.NoError(t, err)
	require.NotNil(t, recovered)
	require.Contains(t, uncertain.Body, "uncertain")
	require.Contains(t, recovered.Body, "succeeded")
}
