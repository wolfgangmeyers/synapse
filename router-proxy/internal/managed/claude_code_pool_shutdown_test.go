package managed

import (
	"context"
	"errors"
	"io"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

type poolTestStdin struct{}

func (poolTestStdin) Write(b []byte) (int, error) { return len(b), nil }
func (poolTestStdin) Close() error                { return nil }

var _ io.WriteCloser = poolTestStdin{}

func waitPoolShutdownClosed(t *testing.T, pool *claudeCodeProcessPool) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		pool.mu.Lock()
		closed := pool.closed
		pool.mu.Unlock()
		if closed {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("shutdown did not close pool")
		case <-tick.C:
		}
	}
}

func waitPoolShutdownError(t *testing.T, ch <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return nil
	}
}

func TestClaudePoolShutdownJoinsAdmittedStart(t *testing.T) {
	pool := &claudeCodeProcessPool{}
	entered, release := make(chan struct{}), make(chan struct{})
	acquired := make(chan error, 1)
	go func() {
		_, err := pool.acquire(context.Background(), store.EmbeddedBinding{AgentID: "a"}, ClaudeCodeProcessConfig{}, func() (claudeCodeProcessGrant, error) {
			close(entered)
			select {
			case <-release:
			case <-time.After(10 * time.Second):
				return claudeCodeProcessGrant{}, errors.New("timed out waiting for grant release")
			}
			return claudeCodeProcessGrant{}, errors.New("grant failed")
		})
		acquired <- err
	}()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("start did not reach grant")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- pool.stopAll() }()
	waitPoolShutdownClosed(t, pool)
	select {
	case <-stopped:
		t.Fatal("shutdown returned before start joined")
	default:
	}
	close(release)
	if err := waitPoolShutdownError(t, acquired, "acquire"); err == nil {
		t.Fatal("expected grant error")
	}
	if err := waitPoolShutdownError(t, stopped, "shutdown"); err != nil {
		t.Fatal(err)
	}
}

func TestClaudePoolShutdownReportsUnconfirmedStopRepeatedly(t *testing.T) {
	done := make(chan struct{})
	close(done)
	p := &ClaudeCodeProcess{stdin: poolTestStdin{}, done: done, exited: done, closeDone: make(chan struct{}), waitErr: errClaudeCodeGroupUnconfirmed}
	revoked := false
	pool := &claudeCodeProcessPool{entries: map[string]*claudeCodeProcessEntry{"a": {process: p, revoke: func() { revoked = true }}}}
	for i := 0; i < 2; i++ {
		stopped := make(chan error, 1)
		go func() { stopped <- pool.stopAll() }()
		if err := waitPoolShutdownError(t, stopped, "shutdown"); err == nil || !strings.Contains(err.Error(), errClaudeCodeGroupUnconfirmed.Error()) {
			t.Fatalf("shutdown %d: %v", i, err)
		}
	}
	if !revoked {
		t.Fatal("grant not revoked")
	}
}

func TestClaudeShutdownDuringStartRetainsScopedCleanupFailure(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "resumed"}[resumed], func(t *testing.T) {
			ctx := context.Background()
			r, p, pool, _ := claudeRuntimeFixture(t, `{}`)
			if resumed {
				if err := p.db.MarkClaudeBindingStarted(ctx, p.binding); err != nil {
					t.Fatal(err)
				}
			}
			token, err := p.db.EnterEmbeddedSDKExecution(ctx, p.binding, nil)
			if err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			pool.afterStart = func(proc *ClaudeCodeProcess) {
				// Inject the wait confirmation failure after the real child has exited,
				// before the shutdown branch runs the real Close path.
				_ = syscall.Kill(-proc.cmd.Process.Pid, syscall.SIGKILL)
				select {
				case <-proc.Done():
				case <-time.After(10 * time.Second):
					return
				}
				proc.recordWaitError(errClaudeCodeGroupUnconfirmed)
				close(entered)
				select {
				case <-release:
				case <-time.After(10 * time.Second):
				}
			}
			result := make(chan error, 1)
			go func() { _, err := r.Run(ctx, "input"); result <- err }()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("start did not reach insertion boundary")
			}
			stopped := make(chan error, 1)
			go func() { stopped <- pool.stopAll() }()
			waitPoolShutdownClosed(t, pool)
			close(release)
			if err := waitPoolShutdownError(t, result, "run"); !errors.Is(err, errClaudeCodeGroupUnconfirmed) {
				t.Fatalf("run: %v", err)
			}
			if err := waitPoolShutdownError(t, stopped, "shutdown"); !errors.Is(err, errClaudeCodeGroupUnconfirmed) {
				t.Fatalf("shutdown: %v", err)
			}
			closed := make(chan error, 1)
			go func() { closed <- r.closeScoped() }()
			closeErr := waitPoolShutdownError(t, closed, "scoped close")
			if closeErr == nil {
				_ = p.db.ClearEmbeddedExecution(ctx, p.binding, token)
			}
			if !errors.Is(closeErr, errClaudeCodeGroupUnconfirmed) {
				t.Fatalf("scoped close: %v", closeErr)
			}
			if _, err := p.db.EnterEmbeddedSDKExecution(ctx, p.binding, nil); err == nil {
				t.Fatal("execution was settled")
			}
		})
	}
}
