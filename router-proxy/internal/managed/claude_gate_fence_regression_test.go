package managed

import (
	"context"
	"errors"
	"os"
	"time"

	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// The durable admission callback is the last point before the exec gate. A
// concurrent cleanup failure can fence the pool while that callback runs.
func TestClaudePoolFenceDuringAdmissionPreventsGateRelease(t *testing.T) {
	var pool claudeCodeProcessPool
	cfg := claudeTestConfig(t, `{"turns":[]}`)
	released := false
	cfg.writeRelease = func(f *os.File) (int, error) { released = true; return f.Write([]byte("ready\n")) }
	cfg.BeforeRelease = func() error { pool.fence(); return nil }
	proc, err := pool.acquire(context.Background(), store.EmbeddedBinding{AgentID: "gate-race"}, cfg, nil)
	if proc != nil {
		defer proc.Close()
	}
	if err == nil {
		t.Fatal("fenced pool returned a process")
	}
	if released {
		t.Fatal("gate released after fence")
	}
}

func TestClaudeManagerCloseFencesBeforeCancellation(t *testing.T) {
	var m Manager
	admitted := false
	sentinel := &struct{}{}
	m.cancel = func() {
		done, err := m.claudeCodeProcesses.admitExternal()
		if err == nil {
			admitted = true
			done()
		}
		panic(sentinel)
	}
	func() {
		defer func() {
			if recover() != sentinel {
				t.Fatal("unexpected panic")
			}
		}()
		_ = m.Close(context.Background())
	}()
	if admitted {
		t.Fatal("pool admitted work after manager shutdown started")
	}
}

func TestClaudeShortCommandFenceBeforeRelease(t *testing.T) {
	var pool claudeCodeProcessPool
	cfg := claudeTestConfig(t, `{"turns":[]}`)
	cfg.releaseGate = pool.releaseGate
	pool.fence()
	if _, err := PreflightClaudeCode(context.Background(), cfg); err == nil {
		t.Fatal("fenced preflight succeeded")
	}
	if _, err := runClaudeGoalEvaluator(context.Background(), cfg, "sonnet", "evaluate"); err == nil {
		t.Fatal("fenced evaluator succeeded")
	}
}

func TestClaudeGateFailureCleanupMayFenceWithoutDeadlock(t *testing.T) {
	var pool claudeCodeProcessPool
	cfg := claudeTestConfig(t, `{"turns":[]}`)
	cfg.releaseGate = pool.releaseGate
	cfg.writeRelease = func(*os.File) (int, error) { return 0, errors.New("write failed") }
	cfg.afterFailedRelease = func() error { pool.fence(); return nil }
	done := make(chan error, 1)
	go func() { _, err := StartClaudeCodeProcess(context.Background(), cfg); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected write failure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup deadlocked")
	}
}
