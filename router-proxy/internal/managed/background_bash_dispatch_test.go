package managed

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/sky-valley/pi/agent"

	"github.com/stretchr/testify/require"
)

// Exercises registration and checked SDK dispatch with a real managed session;
// the model is the deterministic faux provider from requiredToolsFixture.
func TestBackgroundBashRegisteredAndDispatchedForOwners(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("background bash launch is supported only on Linux")
	}
	for _, role := range []string{"root", "worker"} {
		t.Run(role, func(t *testing.T) {
			m, db, provider, caller, _ := requiredToolsFixture(t, role)
			for _, tc := range []struct {
				name string
				args map[string]any
			}{
				{"launch_background_bash", map[string]any{"command": "sleep 1"}},
				{"list_background_bash_jobs", map[string]any{}},
			} {
				result := requiredToolsCall(t, m, db, provider, caller, tc.name, "background-"+role+tc.name, tc.args)
				require.False(t, result.IsError, "%s: %+v", tc.name, result)
			}
		})
	}
}

// An unresolved cleanup remains live even if its process wait has finished.
// It must not bypass the same polling limit as a running job.
func TestBackgroundBashUncertainGetConsumesBudgetTerminalGetFree(t *testing.T) {
	m, _, _, caller, _ := requiredToolsFixture(t, "root")
	m.mu.Lock()
	owner := m.embeddedOwners[caller]
	jobs := m.backgroundBash
	m.mu.Unlock()
	require.NotNil(t, jobs)
	key := backgroundBashOwnerKey(owner)
	jobs.mu.Lock()
	jobs.jobs["uncertain-test"] = &backgroundBashEntry{job: BackgroundBashJob{ID: "uncertain-test", Owner: key, Status: "uncertain", FinishedAt: time.Now()}}
	jobs.jobs["terminal-test"] = &backgroundBashEntry{job: BackgroundBashJob{ID: "terminal-test", Owner: key, Status: "succeeded", FinishedAt: time.Now()}}
	jobs.mu.Unlock()
	t.Cleanup(func() {
		jobs.mu.Lock()
		delete(jobs.jobs, "uncertain-test")
		delete(jobs.jobs, "terminal-test")
		jobs.mu.Unlock()
	})
	var get agent.AgentTool
	for _, tool := range m.backgroundBashTools(owner, t.TempDir()) {
		if tool.Name == "get_background_bash_job" {
			get = tool
			break
		}
	}
	require.NotNil(t, get.Execute)
	for i := 0; i < 3; i++ {
		_, err := get.Execute(context.Background(), "get", map[string]any{"job_id": "uncertain-test"}, nil)
		require.NoError(t, err)
	}
	_, err := get.Execute(context.Background(), "get", map[string]any{"job_id": "uncertain-test"}, nil)
	require.ErrorContains(t, err, backgroundBashReadExhausted)
	_, err = get.Execute(context.Background(), "get", map[string]any{"job_id": "terminal-test"}, nil)
	require.NoError(t, err)
}
