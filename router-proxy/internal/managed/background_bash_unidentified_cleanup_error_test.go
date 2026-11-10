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

func TestBackgroundUnidentifiedFailedGroupSignalReportedOnCleanup(t *testing.T) {
	for _, operation := range []string{"owner", "close"} {
		t.Run(operation, func(t *testing.T) {
			m := NewBackgroundBashManager(nil)
			m.identify = func(int) (string, error) { return "", errors.New("identity unavailable") }
			m.killGroup = func(int, syscall.Signal) error { return errors.New("signal denied") }
			_, err := m.Launch("owner", BackgroundBashLaunch{Command: "exec sleep 30"})
			if err == nil {
				t.Fatal("expected launch error")
			}
			m.mu.Lock()
			var entry *backgroundBashEntry
			for _, e := range m.jobs {
				entry = e
			}
			m.mu.Unlock()
			if entry == nil {
				t.Fatal("missing launch record")
			}
			select {
			case <-entry.done:
			case <-time.After(5 * time.Second):
				t.Fatal("cleanup did not finish")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if operation == "owner" {
				err = m.StopOwnerContext(ctx, "owner")
				defer m.Close()
			} else {
				err = m.CloseContext(ctx)
			}
			if err == nil || !strings.Contains(err.Error(), "uncertain") {
				t.Fatalf("%s claimed successful cleanup: %v", operation, err)
			}
			if operation == "owner" {
				if err = m.StopOwnerContext(ctx, "owner"); err == nil {
					t.Fatal("repeated cleanup lost uncertainty")
				}
				if err = m.StopOwnerContext(ctx, "other"); err != nil {
					t.Fatalf("uncertainty leaked across owners: %v", err)
				}
				m.mu.Lock()
				entry.job.FinishedAt = time.Now().Add(-BackgroundBashRetention)
				m.pruneLocked(time.Now())
				m.mu.Unlock()
				if err = m.StopOwnerContext(ctx, "owner"); err != nil {
					t.Fatalf("expired record still blocks cleanup: %v", err)
				}
			}
		})
	}
}
