//go:build linux

package managed

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestOrphanRetryProceedsWhileInitialNoticeBlocked(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	recovered := make(chan struct{}, 1)
	jobs := NewBackgroundBashManager(func(j BackgroundBashJob) {
		if j.Status == "uncertain" {
			close(entered)
			<-release
		} else {
			recovered <- struct{}{}
		}
	})
	var attempts atomic.Int32
	jobs.removeOrphan = func(path string, id ProcessIdentity) error {
		if attempts.Add(1) == 1 {
			return errors.New("injected removal failure")
		}
		return removeClaudeCodeOrphan(path, id)
	}
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = jobs.CloseContext(ctx)
	}()
	root := t.TempDir()
	job, err := jobs.Launch("blocked-notice", BackgroundBashLaunch{Command: "true", Dir: root, claudeOrphanRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("initial notice not entered")
	}
	deadline := time.After(3 * time.Second)
	for {
		jobs.mu.Lock()
		status := jobs.jobs[job.ID].job.Status
		jobs.mu.Unlock()
		if status == "succeeded" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("cleanup retry blocked by notice")
		case <-time.After(10 * time.Millisecond):
		}
	}
	select {
	case <-recovered:
		t.Fatal("recovery notice overtook initial notice")
	default:
	}
	close(release)
	select {
	case <-recovered:
	case <-time.After(3 * time.Second):
		t.Fatal("missing recovery notice")
	}
}
