//go:build linux

package managed

import (
	"context"
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBackgroundUnidentifiedConfirmedCleanupReleasesCapacity(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	identifyErr := errors.New("identity unavailable")
	m.identify = func(int) (string, error) { return "", identifyErr }
	m.killGroup = func(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) }
	_, err := m.Launch("owner", BackgroundBashLaunch{Command: "sleep 30"})
	if !errors.Is(err, identifyErr) {
		t.Fatalf("launch error: %v", err)
	}
	m.mu.Lock()
	var entry *backgroundBashEntry
	for _, e := range m.jobs {
		entry = e
	}
	m.mu.Unlock()
	if entry == nil {
		t.Fatal("missing failed launch record")
	}
	select {
	case <-entry.done:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not finish")
	}
	job, err := m.Get("owner", entry.job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "failed" || job.FinishedAt.IsZero() || !strings.Contains(job.Error, identifyErr.Error()) || job.ExitCode != nil {
		t.Fatalf("unexpected terminal record: %+v", job)
	}
	// A full manager of active reservations must admit another launch after this
	// one resolves; terminal records do not consume capacity.
	m.mu.Lock()
	for i := 0; i < BackgroundBashMaxRunning-1; i++ {
		m.jobs[string(rune(i+1))] = &backgroundBashEntry{job: BackgroundBashJob{Status: "running"}}
	}
	m.identify = nil
	m.mu.Unlock()
	next, err := m.Launch("owner", BackgroundBashLaunch{Command: "exit 0"})
	if err != nil {
		t.Fatalf("resolved job retained capacity: %v", err)
	}
	m.mu.Lock()
	for i := 0; i < BackgroundBashMaxRunning-1; i++ {
		delete(m.jobs, string(rune(i+1)))
	}
	nextEntry := m.jobs[next.ID]
	m.mu.Unlock()
	select {
	case <-nextEntry.done:
	case <-time.After(5 * time.Second):
		t.Fatal("next job did not finish")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.CloseContext(ctx); err == nil {
		t.Fatal("unconfirmed group cleanup must remain uncertain")
	}
}

func TestBackgroundUnidentifiedSignalFailureReleasesCapacity(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	m.identify = func(int) (string, error) { return "", errors.New("identity unavailable") }
	m.killGroup = func(int, syscall.Signal) error { return errors.New("signal denied") }
	_, err := m.Launch("owner", BackgroundBashLaunch{Command: "exec sleep 30"})
	if err == nil {
		t.Fatal("expected launch failure")
	}
	m.mu.Lock()
	var entry *backgroundBashEntry
	for _, e := range m.jobs {
		entry = e
	}
	m.mu.Unlock()
	select {
	case <-entry.done:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup not bounded")
	}
	job, err := m.Get("owner", entry.job.ID)
	if err != nil || job.Status != "failed" || !strings.Contains(job.Error, "uncertain") {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	m.mu.Lock()
	for i := 0; i < BackgroundBashMaxRunning-1; i++ {
		m.jobs[string(rune(i+1))] = &backgroundBashEntry{job: BackgroundBashJob{Status: "running"}}
	}
	m.identify = nil
	m.killGroup = nil
	m.mu.Unlock()
	next, err := m.Launch("owner", BackgroundBashLaunch{Command: "exit 0"})
	if err != nil {
		t.Fatalf("capacity retained: %v", err)
	}
	m.mu.Lock()
	for i := 0; i < BackgroundBashMaxRunning-1; i++ {
		delete(m.jobs, string(rune(i+1)))
	}
	e := m.jobs[next.ID]
	m.mu.Unlock()
	select {
	case <-e.done:
	case <-time.After(5 * time.Second):
		t.Fatal("next job not finished")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.CloseContext(ctx); err == nil || !strings.Contains(err.Error(), "uncertain") {
		t.Fatalf("close should report uncertain cleanup: %v", err)
	}
}
