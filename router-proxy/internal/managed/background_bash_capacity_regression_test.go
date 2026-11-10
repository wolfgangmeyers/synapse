//go:build linux

package managed

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Synthetic entries isolate admission decisions from unrelated process timing.
func TestForegroundFastCommandWithFullBackgroundCapacity(t *testing.T) {
	jobs := NewBackgroundBashManager(nil)
	defer jobs.Close()
	jobs.mu.Lock()
	for i := 0; i < BackgroundBashMaxRunning; i++ {
		id := fmt.Sprintf("occupied-%d", i)
		jobs.jobs[id] = &backgroundBashEntry{job: BackgroundBashJob{ID: id, Owner: "other", Status: "running"}}
	}
	jobs.mu.Unlock()
	defer func() {
		jobs.mu.Lock()
		for id := range jobs.jobs {
			if strings.HasPrefix(id, "occupied-") {
				delete(jobs.jobs, id)
			}
		}
		jobs.mu.Unlock()
	}()
	m := &Manager{backgroundBash: jobs}
	owner := embeddedOwner{identity: "foreground-capacity", backgroundBash: &backgroundBashOwnerCapability{}}
	result, err := m.executePromotableBash(context.Background(), owner, t.TempDir(), embeddedBashCommand{command: "printf foreground-ok", timeout: time.Second})
	if err != nil || !strings.Contains(fmt.Sprint(result.Content), "foreground-ok") {
		t.Fatalf("fast foreground result=%v error=%v", result, err)
	}
}

func TestRejectedLaunchDoesNotEvictRetainedTerminal(t *testing.T) {
	jobs := NewBackgroundBashManager(nil)
	defer jobs.Close()
	jobs.mu.Lock()
	for i := 0; i < BackgroundBashMaxRecords; i++ {
		id := fmt.Sprintf("occupied-%d", i)
		status := "succeeded"
		if i < BackgroundBashMaxRunning {
			status = "running"
		}
		jobs.jobs[id] = &backgroundBashEntry{job: BackgroundBashJob{ID: id, Owner: "owner", Status: status, FinishedAt: time.Now()}}
	}
	jobs.mu.Unlock()
	defer func() {
		jobs.mu.Lock()
		for id := range jobs.jobs {
			if strings.HasPrefix(id, "occupied-") {
				delete(jobs.jobs, id)
			}
		}
		jobs.mu.Unlock()
	}()
	_, err := jobs.Launch("owner", BackgroundBashLaunch{Command: "true"})
	if err == nil {
		t.Fatal("expected full running capacity to reject launch")
	}
	jobs.mu.Lock()
	defer jobs.mu.Unlock()
	if len(jobs.jobs) != BackgroundBashMaxRecords {
		t.Fatalf("rejected launch evicted retained record: got %d, want %d", len(jobs.jobs), BackgroundBashMaxRecords)
	}
}

func TestPromotionAtFullBackgroundCapacityCleansUp(t *testing.T) {
	jobs := NewBackgroundBashManager(nil)
	defer jobs.Close()
	jobs.mu.Lock()
	for i := 0; i < BackgroundBashMaxRunning; i++ {
		id := fmt.Sprintf("occupied-%d", i)
		jobs.jobs[id] = &backgroundBashEntry{job: BackgroundBashJob{ID: id, Status: "running"}}
	}
	jobs.mu.Unlock()
	defer func() {
		jobs.mu.Lock()
		for id := range jobs.jobs {
			if strings.HasPrefix(id, "occupied-") {
				delete(jobs.jobs, id)
			}
		}
		jobs.mu.Unlock()
	}()
	owner := embeddedOwner{identity: "promotion-full", backgroundBash: &backgroundBashOwnerCapability{}}
	m := &Manager{backgroundBash: jobs}
	_, err := m.executePromotableBash(context.Background(), owner, t.TempDir(), embeddedBashCommand{command: "sleep 30", timeout: 50 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "capacity") {
		t.Fatalf("expected capacity error, got %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := jobs.StopOwnerContext(ctx, backgroundBashOwnerKey(owner)); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}
