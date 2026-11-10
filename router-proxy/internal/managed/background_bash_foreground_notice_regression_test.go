//go:build linux

package managed

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestForegroundCommandsDoNotSendBackgroundCompletionNotice(t *testing.T) {
	var mu sync.Mutex
	var notices []BackgroundBashJob
	jobs := NewBackgroundBashManager(func(job BackgroundBashJob) { mu.Lock(); notices = append(notices, job); mu.Unlock() })
	jobs.mu.Lock()
	for i := 0; i < BackgroundBashMaxRunning; i++ {
		id := fmt.Sprintf("occupied-%d", i)
		jobs.jobs[id] = &backgroundBashEntry{job: BackgroundBashJob{ID: id, Status: "running"}}
	}
	jobs.mu.Unlock()
	owner := embeddedOwner{identity: "foreground-notice", backgroundBash: &backgroundBashOwnerCapability{}}
	m := &Manager{backgroundBash: jobs}
	_, err := m.executePromotableBash(context.Background(), owner, t.TempDir(), embeddedBashCommand{command: "true", timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.executePromotableBash(context.Background(), owner, t.TempDir(), embeddedBashCommand{command: "sleep 30", timeout: 50 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "capacity") {
		t.Fatalf("expected capacity error, got %v", err)
	}
	jobs.mu.Lock()
	for id := range jobs.jobs {
		if strings.HasPrefix(id, "occupied-") {
			delete(jobs.jobs, id)
		}
	}
	jobs.mu.Unlock()
	jobs.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(notices) != 0 {
		t.Fatalf("foreground commands emitted %d background notices", len(notices))
	}
}
