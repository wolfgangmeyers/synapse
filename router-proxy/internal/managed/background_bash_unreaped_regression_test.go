//go:build linux

package managed

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestBackgroundUnidentifiedRetainsReservationUntilWaitCompletes(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	m.unidentifiedExited = func(int) (bool, error) { return false, nil }
	m.waitUnidentifiedCommand = func(cmd *exec.Cmd) error { close(entered); <-release; return cmd.Wait() }
	m.identify = func(int) (string, error) { return "", errors.New("identity unavailable") }
	m.killGroup = func(int, syscall.Signal) error { return nil }
	_, _ = m.Launch("owner", BackgroundBashLaunch{Command: "sleep 30"})
	m.mu.Lock()
	var entry *backgroundBashEntry
	for _, e := range m.jobs {
		entry = e
	}
	m.mu.Unlock()
	if entry == nil {
		t.Fatal("missing entry")
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("wait not entered")
	}
	time.Sleep(50 * time.Millisecond)
	job, err := m.Get("owner", entry.job.ID)
	if err != nil || job.Status != "uncertain" {
		t.Fatalf("unreaped job must remain uncertain: %+v, %v", job, err)
	}
	m.mu.Lock()
	entry.job.FinishedAt = time.Now().Add(-2 * BackgroundBashRetention)
	m.pruneLocked(time.Now())
	_, present := m.jobs[entry.job.ID]
	m.mu.Unlock()
	if !present {
		t.Fatal("unreaped reservation pruned")
	}
	m.mu.Lock()
	for i := 0; i < BackgroundBashMaxRunning-1; i++ {
		id := fmt.Sprintf("reserved-%d", i)
		m.jobs[id] = &backgroundBashEntry{job: BackgroundBashJob{ID: id, Status: "running"}}
	}
	m.mu.Unlock()
	if _, err := m.Launch("other", BackgroundBashLaunch{Command: "true"}); err == nil {
		t.Fatal("unreaped job did not consume capacity")
	}
	m.mu.Lock()
	for i := 0; i < BackgroundBashMaxRunning-1; i++ {
		delete(m.jobs, fmt.Sprintf("reserved-%d", i))
	}
	m.mu.Unlock()
	ownerCtx, ownerCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	if err := m.StopOwnerContext(ownerCtx, "owner"); err == nil {
		t.Fatal("owner retirement succeeded while wait unresolved")
	}
	ownerCancel()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := m.CloseContext(ctx); err == nil {
		t.Fatal("shutdown succeeded while wait unresolved")
	}
	close(release)
	select {
	case <-entry.done:
	case <-time.After(3 * time.Second):
		t.Fatal("wait did not settle")
	}
	job, err = m.Get("owner", entry.job.ID)
	if err != nil || job.Status != "failed" {
		t.Fatalf("reaped job: %+v %v", job, err)
	}
}
