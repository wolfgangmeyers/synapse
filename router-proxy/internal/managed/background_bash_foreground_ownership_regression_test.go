//go:build linux

package managed

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Failed foreground cleanup must remain owned and retryable by the manager.
func TestForegroundCleanupFailureRetainsManagerOwnership(t *testing.T) {
	jobs := NewBackgroundBashManager(nil)
	defer jobs.Close()
	jobs.signal = func(ProcessIdentity, syscall.Signal) error { return errors.New("injected signal failure") }
	owner := embeddedOwner{identity: "foreground-owner", backgroundBash: &backgroundBashOwnerCapability{}}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{backgroundBash: jobs, embeddedBashAfterStart: func(int) { cancel() }}
	_, err := m.executePromotableBash(ctx, owner, t.TempDir(), embeddedBashCommand{command: "sleep 1", timeout: time.Minute})
	if err == nil {
		t.Fatal("expected cleanup failure")
	}
	entries := jobs.List(backgroundBashOwnerKey(owner))
	if len(entries) != 1 || entries[0].Status != "uncertain" {
		t.Fatalf("cleanup must remain manager-owned and uncertain, got %+v", entries)
	}
}

func TestForegroundCapacityRejectedBeforeStart(t *testing.T) {
	jobs := NewBackgroundBashManager(nil)
	defer jobs.Close()
	jobs.mu.Lock()
	for i := 0; i < BackgroundBashMaxRunning; i++ {
		id := fmt.Sprint(i)
		jobs.jobs[id] = &backgroundBashEntry{foregroundSlot: true, job: BackgroundBashJob{ID: id, Status: "uncertain"}}
	}
	jobs.mu.Unlock()
	path := filepath.Join(t.TempDir(), "started")
	owner := embeddedOwner{identity: "capacity", backgroundBash: &backgroundBashOwnerCapability{}}
	m := &Manager{backgroundBash: jobs}
	_, err := m.executePromotableBash(context.Background(), owner, t.TempDir(), embeddedBashCommand{command: "touch " + path, timeout: time.Second})
	if err == nil {
		t.Fatal("expected capacity error")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("command started: %v", err)
	}
	jobs.mu.Lock()
	jobs.jobs = make(map[string]*backgroundBashEntry)
	jobs.mu.Unlock()
}

func TestForegroundRetirementStopsBeforePromotion(t *testing.T) {
	jobs := NewBackgroundBashManager(nil)
	defer jobs.Close()
	owner := embeddedOwner{identity: "retiring", backgroundBash: &backgroundBashOwnerCapability{}}
	started := make(chan struct{})
	m := &Manager{backgroundBash: jobs, embeddedBashAfterStart: func(int) { close(started) }}
	result := make(chan error, 1)
	go func() {
		_, err := m.executePromotableBash(context.Background(), owner, t.TempDir(), embeddedBashCommand{command: "sleep 30", timeout: time.Second})
		result <- err
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.retireBackgroundBashOwnerContext(ctx, owner); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("retired foreground returned success")
		}
	case <-ctx.Done():
		t.Fatal("foreground did not stop")
	}
}

func TestForegroundShutdownStopsBeforePromotion(t *testing.T) {
	jobs := NewBackgroundBashManager(nil)
	owner := embeddedOwner{identity: "shutdown", backgroundBash: &backgroundBashOwnerCapability{}}
	started := make(chan struct{})
	m := &Manager{backgroundBash: jobs, embeddedBashAfterStart: func(int) { close(started) }}
	result := make(chan error, 1)
	go func() {
		_, err := m.executePromotableBash(context.Background(), owner, t.TempDir(), embeddedBashCommand{command: "sleep 30", timeout: time.Second})
		result <- err
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := jobs.CloseContext(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("shutdown foreground returned success")
		}
	case <-ctx.Done():
		t.Fatal("foreground did not stop")
	}
}

func TestForegroundFailedSignalRetriesAndReleasesCapacity(t *testing.T) {
	jobs := NewBackgroundBashManager(nil)
	defer jobs.Close()
	var allow atomic.Bool
	jobs.signal = func(id ProcessIdentity, sig syscall.Signal) error {
		if !allow.Load() {
			return errors.New("signal unavailable")
		}
		return DefaultProcessGroupOps().SignalVerifiedProcessGroup(id, sig)
	}
	owner := embeddedOwner{identity: "retry", backgroundBash: &backgroundBashOwnerCapability{}}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{backgroundBash: jobs, embeddedBashAfterStart: func(int) { cancel() }}
	_, err := m.executePromotableBash(ctx, owner, t.TempDir(), embeddedBashCommand{command: "sleep 30", timeout: time.Minute})
	if err == nil {
		t.Fatal("expected cleanup error")
	}
	entries := jobs.List(backgroundBashOwnerKey(owner))
	if len(entries) != 1 || entries[0].Status != "uncertain" {
		t.Fatalf("missing uncertainty: %+v", entries)
	}
	allow.Store(true)
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		entry, err := jobs.Get(backgroundBashOwnerKey(owner), entries[0].ID)
		if err == nil && entry.Status != "running" && entry.Status != "uncertain" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("cleanup was not retried")
}

func TestForegroundCancellationShutdownWaitsForCleanupConfirmation(t *testing.T) {
	jobs := NewBackgroundBashManager(nil)
	entered, release := make(chan struct{}), make(chan struct{})
	jobs.waitCommand = func(cmd *exec.Cmd, id ProcessIdentity, reap func(func() error) error) error {
		close(entered)
		<-release
		return waitClaudeCodeCommandWithReapHook(cmd, id, nil, reap)
	}
	owner := embeddedOwner{identity: "cancellation", backgroundBash: &backgroundBashOwnerCapability{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &Manager{backgroundBash: jobs, embeddedBashAfterStart: func(int) { cancel() }}
	dir := t.TempDir()
	_, err := m.executePromotableBash(ctx, owner, dir, embeddedBashCommand{command: "sleep 30", timeout: time.Minute})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("cleanup waiter did not start")
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	result := make(chan error, 1)
	go func() { result <- jobs.CloseContext(shutdownCtx) }()
	select {
	case err := <-result:
		close(release)
		t.Fatalf("shutdown returned before cleanup confirmation: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-shutdownCtx.Done():
		t.Fatal("shutdown did not finish after cleanup confirmation")
	}
}

func TestForegroundWaitDelayDoesNotStartSecondWaiter(t *testing.T) {
	jobs := NewBackgroundBashManager(nil)
	defer jobs.Close()
	owner := embeddedOwner{identity: "drain", backgroundBash: &backgroundBashOwnerCapability{}}
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "pid")
	m := &Manager{backgroundBash: jobs}
	_, err := m.executePromotableBash(context.Background(), owner, dir, embeddedBashCommand{command: fmt.Sprintf("setsid sh -c 'echo $$ > %s; exec sleep 10' & exit 0", pidfile), timeout: time.Minute})
	data, _ := os.ReadFile(pidfile)
	if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("expected wait delay error, got %v", err)
	}
	if got := jobs.List(backgroundBashOwnerKey(owner)); len(got) != 0 {
		t.Fatalf("reaped process must not be handed off: %+v", got)
	}
}

func TestForegroundIdentityFailureShutdownStopsRetryWorker(t *testing.T) {
	jobs := NewBackgroundBashManager(nil)
	var attempts atomic.Int32
	jobs.identify = func(int) (string, error) { attempts.Add(1); return "", errors.New("identity unavailable") }
	owner := embeddedOwner{identity: "identity-failure", backgroundBash: &backgroundBashOwnerCapability{}}
	m := &Manager{backgroundBash: jobs}
	_, err := m.executePromotableBash(context.Background(), owner, t.TempDir(), embeddedBashCommand{command: "sleep 30", timeout: time.Minute})
	if err == nil {
		t.Fatal("expected identity error")
	}
	jobs.mu.Lock()
	var entry *backgroundBashEntry
	for _, e := range jobs.jobs {
		entry = e
	}
	jobs.mu.Unlock()
	if entry == nil {
		t.Fatal("missing owned child")
	}
	defer func() { _ = syscall.Kill(-entry.cmd.Process.Pid, syscall.SIGKILL); _ = entry.cmd.Wait() }()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := jobs.CloseContext(ctx); err == nil {
		t.Fatal("shutdown must report uncertain cleanup")
	}
	before := attempts.Load()
	time.Sleep(250 * time.Millisecond)
	if after := attempts.Load(); after != before {
		t.Fatalf("retry continued after shutdown: %d -> %d", before, after)
	}
	jobs.mu.Lock()
	count := jobs.callbackCount
	status := entry.job.Status
	jobs.mu.Unlock()
	if count != 0 || status != "uncertain" {
		t.Fatalf("retry not retired or ownership lost: callbacks=%d status=%s", count, status)
	}
}
