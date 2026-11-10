//go:build linux

package managed

import (
	"context"
	"errors"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"
)

func TestOrphanRemovalRetryReleasesCapacity(t *testing.T) {
	for _, foreground := range []bool{true, false} {
		name := "background"
		if foreground {
			name = "foreground"
		}
		t.Run(name, func(t *testing.T) {
			jobs := NewBackgroundBashManager(nil)
			var allow atomic.Bool
			var attempts atomic.Int32
			jobs.removeOrphan = func(path string, id ProcessIdentity) error {
				n := attempts.Add(1)
				if n == 1 || !allow.Load() {
					return errors.New("transient removal failure")
				}
				return removeClaudeCodeOrphan(path, id)
			}
			t.Cleanup(func() {
				allow.Store(true)
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_ = jobs.CloseContext(ctx)
			})
			root := t.TempDir()
			owner := embeddedOwner{identity: "retry", backgroundBash: &backgroundBashOwnerCapability{}, claudeOrphanRoot: root}
			if foreground {
				m := &Manager{backgroundBash: jobs}
				_, err := m.executePromotableBash(context.Background(), owner, root, embeddedBashCommand{command: "true", timeout: time.Second})
				if err == nil {
					t.Fatal("expected removal error")
				}
			} else {
				if _, err := jobs.Launch("retry", BackgroundBashLaunch{Command: "true", Dir: root, claudeOrphanRoot: root}); err != nil {
					t.Fatal(err)
				}
			}
			deadline := time.Now().Add(3 * time.Second)
			for attempts.Load() == 0 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if attempts.Load() == 0 {
				t.Fatal("no removal attempt")
			}
			allow.Store(true)
			for time.Now().Before(deadline) {
				jobs.mu.Lock()
				count := len(jobs.jobs)
				running := jobs.backgroundRunningLocked()
				jobs.mu.Unlock()
				if (foreground && count == 0) || (!foreground && running == 0) {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatalf("capacity retained after recovery; attempts=%d", attempts.Load())
		})
	}
}

func TestOrphanRemovalRetryStopsOnShutdown(t *testing.T) {
	jobs := NewBackgroundBashManager(nil)
	var attempts atomic.Int32
	jobs.removeOrphan = func(string, ProcessIdentity) error { attempts.Add(1); return errors.New("unavailable") }
	root := t.TempDir()
	owner := embeddedOwner{identity: "shutdown-retry", backgroundBash: &backgroundBashOwnerCapability{}, claudeOrphanRoot: root}
	m := &Manager{backgroundBash: jobs}
	if _, err := m.executePromotableBash(context.Background(), owner, root, embeddedBashCommand{command: "true", timeout: time.Second}); err == nil {
		t.Fatal("expected removal failure")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := jobs.CloseContext(ctx); err == nil {
		t.Fatal("shutdown must report unresolved removal")
	}
	n := attempts.Load()
	time.Sleep(250 * time.Millisecond)
	if attempts.Load() != n {
		t.Fatal("retry continued after shutdown")
	}
}

type failingDrainWriter struct{}

func (failingDrainWriter) Write([]byte) (int, error) { return 0, errors.New("injected drain failure") }

func TestOrphanRetryPreservesDrainFailureAndNotifiesRecovery(t *testing.T) {
	notices := make(chan BackgroundBashJob, 4)
	jobs := NewBackgroundBashManager(func(j BackgroundBashJob) { notices <- j })
	var allow atomic.Bool
	attempted := make(chan struct{}, 1)
	jobs.removeOrphan = func(path string, id ProcessIdentity) error {
		if !allow.Load() {
			select {
			case attempted <- struct{}{}:
			default:
			}
			return errors.New("injected removal failure")
		}
		return removeClaudeCodeOrphan(path, id)
	}
	jobs.startCommand = func(cmd *exec.Cmd) error { cmd.Stdout = failingDrainWriter{}; return cmd.Start() }
	t.Cleanup(func() {
		allow.Store(true)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = jobs.CloseContext(ctx)
	})
	root := t.TempDir()
	j, err := jobs.Launch("retry-drain", BackgroundBashLaunch{Command: "echo output", Dir: root, claudeOrphanRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-attempted:
	case <-time.After(3 * time.Second):
		t.Fatal("initial removal not attempted")
	}
	select {
	case n := <-notices:
		if n.Status != "uncertain" {
			t.Fatalf("initial notice: %+v", n)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("missing uncertain notice")
	}
	allow.Store(true)
	deadline := time.After(3 * time.Second)
	for {
		jobs.mu.Lock()
		e := jobs.jobs[j.ID]
		snap := jobs.snapshot(e)
		jobs.mu.Unlock()
		if snap.Status != "uncertain" {
			if snap.Status != "failed" {
				t.Fatalf("drain failure lost: %+v", snap)
			}
			select {
			case n := <-notices:
				if n.Status != "failed" {
					t.Fatalf("recovery notice: %+v", n)
				}
			case <-time.After(time.Second):
				t.Fatal("missing recovery notice")
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("recovery timed out")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
