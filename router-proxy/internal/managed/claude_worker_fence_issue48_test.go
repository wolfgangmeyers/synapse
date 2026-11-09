package managed

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestClaudeWorkerFenceIsolationAndGate(t *testing.T) {
	var pool claudeCodeProcessPool
	done, err := pool.admitWorker("a")
	if err != nil {
		t.Fatal(err)
	}
	done()
	pool.fenceWorker("a", errClaudeCodeGroupUnconfirmed)
	if _, err := pool.admitWorker("a"); err == nil {
		t.Fatal("fenced worker admitted")
	}
	ran := false
	if _, err := pool.workerGate("a")(func() (int, error) { ran = true; return 1, nil }); err == nil || ran {
		t.Fatal("fenced gate released")
	}
	other, err := pool.admitWorker("b")
	if err != nil {
		t.Fatal(err)
	}
	other()
	if _, err := pool.workerGate("b")(func() (int, error) { return 1, nil }); err != nil {
		t.Fatal(err)
	}
	pool.closeAdmission()
	if _, err := pool.admitWorker("b"); err == nil {
		t.Fatal("shutdown admitted worker")
	}
	if !errors.Is(pool.stopAll(), errClaudeCodeGroupUnconfirmed) {
		t.Fatal("shutdown lost cleanup error")
	}
}

func TestClaudePostStartWorkerFencePreservesCause(t *testing.T) {
	for _, cleanupFails := range []bool{false, true} {
		t.Run(fmt.Sprint(cleanupFails), func(t *testing.T) {
			pool := &claudeCodeProcessPool{}
			cfg := claudeTestConfig(t, `{}`)
			cause := fmt.Errorf("private/path: %w", syscall.EACCES)
			pool.afterStart = func(proc *ClaudeCodeProcess) {
				if cleanupFails {
					_ = syscall.Kill(-proc.cmd.Process.Pid, syscall.SIGKILL)
					<-proc.Done()
					proc.mu.Lock()
					proc.orphanCleanupErr = &os.PathError{Op: "remove", Path: "/private/cleanup-path", Err: syscall.ENOENT}
					proc.mu.Unlock()
				}
				pool.fenceWorker("a", cause)
			}
			_, err := pool.acquire(context.Background(), store.EmbeddedBinding{AgentID: "a"}, cfg, nil)
			require.ErrorIs(t, err, syscall.EACCES)
			var fenced *ClaudeAdmissionFencedError
			require.ErrorAs(t, err, &fenced)
			require.Contains(t, err.Error(), "permission denied")
			require.NotContains(t, err.Error(), "private/path")
			require.NotContains(t, err.Error(), "pool closed")
			if cleanupFails {
				require.ErrorIs(t, err, syscall.ENOENT)
				require.Contains(t, err.Error(), "no such file or directory")
				require.NotContains(t, err.Error(), "/private/cleanup-path")
			}
			release, err := pool.admitWorker("b")
			require.NoError(t, err)
			release()
		})
	}
}

func TestClaudeFenceDiagnosticsSanitizeCause(t *testing.T) {
	for _, tc := range []struct {
		cause error
		want  string
	}{
		{errClaudeCodeGroupUnconfirmed, "group absence unconfirmed"},
		{fmt.Errorf("secret/path: %w", syscall.EACCES), "permission denied"},
		{errors.New("secret content"), "cleanup or recovery failed"},
		{safeClaudeRecoveryError(fmt.Errorf("secret/path: %w", syscall.ENOTDIR)), "not a directory"},
	} {
		err := &ClaudeAdmissionFencedError{Cause: tc.cause}
		require.Contains(t, err.Error(), tc.want)
		require.NotContains(t, err.Error(), "secret")
	}
}

func TestClaudeWorkerFenceCapacityFailsClosed(t *testing.T) {
	var pool claudeCodeProcessPool
	first := errors.New("first fence")
	pool.fenceWorker("0", first)
	for i := 0; i < 100; i++ {
		pool.fenceWorker("0", errors.New("later fence"))
	}
	require.Same(t, first, pool.fencedWorkers["0"])
	for i := 1; i < MaxManagedAgents; i++ {
		pool.fenceWorker(fmt.Sprint(i), errors.New("fence"))
	}
	release, err := pool.admitWorker("other")
	require.NoError(t, err)
	release()
	pool.fenceWorker("overflow", errors.New("overflow fence"))
	require.Len(t, pool.fencedWorkers, MaxManagedAgents)
	require.Same(t, first, pool.fencedWorkers["0"])
	require.ErrorIs(t, pool.startErr, errClaudeWorkerFenceCapacity)
	capacityErr := pool.startErr
	pool.fenceWorker("overflow", errors.New("again"))
	require.Same(t, capacityErr, pool.startErr)
	_, err = pool.admitWorker("other")
	require.Error(t, err)
	_, err = pool.admitExternal()
	require.Error(t, err)
	_, err = pool.workerGate("other")(func() (int, error) { t.Fatal("gate released"); return 0, nil })
	require.Error(t, err)
	require.ErrorIs(t, pool.stopAll(), first)
}
