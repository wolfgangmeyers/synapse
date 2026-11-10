//go:build linux

package managed

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestIdentityRetryCleanupContinuesThroughShutdown(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "waiter", true: "shutdown"}[shutdown], func(t *testing.T) {
			jobs := NewBackgroundBashManager(nil)
			var calls atomic.Int32
			retry := make(chan struct{})
			jobs.identify = func(pid int) (string, error) {
				if calls.Add(1) == 1 {
					return "", errors.New("injected")
				}
				<-retry
				return processStartMarker(pid)
			}
			owner := embeddedOwner{identity: "retry", backgroundBash: &backgroundBashOwnerCapability{}}
			m := &Manager{backgroundBash: jobs}
			_, err := m.executePromotableBash(context.Background(), owner, t.TempDir(), embeddedBashCommand{command: "sleep 30", timeout: time.Minute})
			if err == nil {
				t.Fatal("expected identity failure")
			}
			entries := jobs.List(backgroundBashOwnerKey(owner))
			if len(entries) != 1 {
				t.Fatalf("missing owned entry: %+v", entries)
			}
			jobs.mu.Lock()
			e := jobs.jobs[entries[0].ID]
			jobs.mu.Unlock()
			t.Cleanup(func() { _ = e.cmd.Process.Kill() })
			if shutdown {
				jobs.Close()
			}
			close(retry)
			select {
			case <-e.done:
			case <-time.After(3 * time.Second):
				t.Fatal("identity retry did not complete owned cleanup")
			}
			if e.cmd.ProcessState == nil {
				t.Fatal("child not reaped")
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := jobs.CloseContext(ctx); err != nil {
				t.Fatalf("close: %v", err)
			}
		})
	}
}

func TestIdentityRetryStartsWaiterDespiteSignalFailure(t *testing.T) {
	jobs := NewBackgroundBashManager(nil)
	var calls atomic.Int32
	jobs.identify = func(pid int) (string, error) {
		if calls.Add(1) == 1 {
			return "", errors.New("injected")
		}
		return processStartMarker(pid)
	}
	jobs.signal = func(ProcessIdentity, syscall.Signal) error { return errors.New("signal unavailable") }
	owner := embeddedOwner{identity: "retry-signal", backgroundBash: &backgroundBashOwnerCapability{}}
	m := &Manager{backgroundBash: jobs}
	_, err := m.executePromotableBash(context.Background(), owner, t.TempDir(), embeddedBashCommand{command: "sleep 0.3", timeout: time.Minute})
	if err == nil {
		t.Fatal("expected identity failure")
	}
	entries := jobs.List(backgroundBashOwnerKey(owner))
	if len(entries) != 1 {
		t.Fatal("missing entry")
	}
	jobs.mu.Lock()
	e := jobs.jobs[entries[0].ID]
	jobs.mu.Unlock()
	t.Cleanup(func() { _ = e.cmd.Process.Kill() })
	select {
	case <-e.done:
	case <-time.After(2 * time.Second):
		t.Fatal("waiter blocked behind signal retry")
	}
}

func TestForegroundOrphanRemovalFailureRetainsOwnership(t *testing.T) {
	jobs := NewBackgroundBashManager(nil)
	root := t.TempDir()
	owner := embeddedOwner{identity: "orphan-removal", backgroundBash: &backgroundBashOwnerCapability{}, claudeOrphanRoot: root}
	m := &Manager{backgroundBash: jobs, embeddedBashAfterStart: func(int) {
		jobs.mu.Lock()
		defer jobs.mu.Unlock()
		for _, e := range jobs.jobs {
			if e.orphanPath == "" {
				t.Error("missing orphan record")
				return
			}
			if err := os.WriteFile(e.orphanPath, []byte("invalid"), 0600); err != nil {
				t.Error(err)
			}
		}
	}}
	_, err := m.executePromotableBash(context.Background(), owner, t.TempDir(), embeddedBashCommand{command: "true", timeout: time.Second})
	if err == nil {
		t.Fatal("expected orphan removal error")
	}
	entries := jobs.List(backgroundBashOwnerKey(owner))
	if len(entries) != 1 || entries[0].Status != "uncertain" {
		t.Fatalf("lost unresolved ownership: %+v", entries)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := jobs.CloseContext(ctx); err == nil {
		t.Fatal("shutdown must report orphan removal failure")
	}
}
