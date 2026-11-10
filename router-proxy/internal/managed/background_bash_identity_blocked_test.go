//go:build linux

package managed

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestForegroundBlockedIdentityInitialAndRetry(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(map[bool]string{false: "initial", true: "retry"}[retry], func(t *testing.T) {
			jobs := NewBackgroundBashManager(nil)
			entered := make(chan int, 4)
			release := make(chan struct{})
			calls := 0
			jobs.identify = func(pid int) (string, error) {
				calls++
				entered <- pid
				if retry && calls == 1 {
					return "", errors.New("injected failure")
				}
				<-release
				return "", errors.New("unavailable")
			}
			owner := embeddedOwner{identity: "blocked", backgroundBash: &backgroundBashOwnerCapability{}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m := &Manager{backgroundBash: jobs}
			done := make(chan error, 1)
			go func() {
				_, err := m.executePromotableBash(ctx, owner, t.TempDir(), embeddedBashCommand{command: "sleep 30", timeout: time.Minute})
				done <- err
			}()
			<-entered
			if retry {
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("retry did not start")
				}
			}
			cancel()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("expected error")
				}
			case <-time.After(2 * time.Second):
				close(release)
				t.Fatal("blocked identity stalled caller")
			}
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			err := jobs.CloseContext(closeCtx)
			closeCancel()
			if err == nil {
				close(release)
				t.Fatal("shutdown must report unresolved cleanup")
			}
			entries := jobs.List(backgroundBashOwnerKey(owner))
			if len(entries) != 1 || entries[0].Status != "uncertain" {
				close(release)
				t.Fatalf("lost ownership: %+v", entries)
			}
			jobs.mu.Lock()
			entry := jobs.jobs[entries[0].ID]
			callbacks := jobs.callbackCount
			jobs.mu.Unlock()
			if entry.cmd.ProcessState != nil || callbacks == 0 {
				close(release)
				t.Fatal("lookup not tracked or child reaped")
			}
			// A permanently failing identity lookup stays owned after shutdown.
			// Permit the next retry to identify the pinned child and finish cleanup.
			close(release)
			jobs.mu.Lock()
			jobs.identify = processStartMarker
			jobs.mu.Unlock()
			deadline := time.Now().Add(3 * time.Second)
			for {
				jobs.mu.Lock()
				n := jobs.callbackCount
				jobs.mu.Unlock()
				if n == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("lookup did not retire")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if entry.cmd.ProcessState == nil {
				t.Fatal("child not reaped")
			}
		})
	}
}
