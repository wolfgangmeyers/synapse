package managed

import (
	"fmt"
	"testing"
	"testing/synctest"
	"time"
)

func TestBackgroundBashRetentionEvictsOldestTerminalFirst(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	m := &BackgroundBashManager{jobs: make(map[string]*backgroundBashEntry)}
	// The running record counts toward capacity but cannot be evicted.
	m.jobs["running"] = &backgroundBashEntry{job: BackgroundBashJob{ID: "running", Owner: "live", Status: "running"}}
	for i := 0; i < BackgroundBashMaxRecords+2; i++ {
		id := fmt.Sprintf("terminal-%03d", i)
		m.jobs[id] = &backgroundBashEntry{job: BackgroundBashJob{ID: id, Owner: "terminal", Status: "succeeded", FinishedAt: now.Add(-time.Duration(BackgroundBashMaxRecords+2-i) * time.Second)}}
	}
	owners := m.pruneLocked(now)
	if len(m.jobs) != BackgroundBashMaxRecords {
		t.Fatalf("got %d records, want %d", len(m.jobs), BackgroundBashMaxRecords)
	}
	for i := 0; i < BackgroundBashMaxRecords+2; i++ {
		id := fmt.Sprintf("terminal-%03d", i)
		_, present := m.jobs[id]
		if want := i >= 3; present != want {
			t.Errorf("%s present=%v, want %v", id, present, want)
		}
	}
	if _, ok := m.jobs["running"]; !ok {
		t.Error("running record was evicted")
	}
	if len(owners) != 1 {
		t.Fatalf("changed owners = %v", owners)
	}
	if _, ok := owners["terminal"]; !ok {
		t.Errorf("terminal owner not reported: %v", owners)
	}
}

func TestBackgroundBashAutomaticSweepTicker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewBackgroundBashManager(nil)
		defer m.Close()
		now := time.Now()
		m.mu.Lock()
		m.jobs["expired"] = &backgroundBashEntry{job: BackgroundBashJob{ID: "expired", Owner: "owner", Status: "succeeded", FinishedAt: now.Add(-BackgroundBashRetention)}}
		m.jobs["fresh"] = &backgroundBashEntry{job: BackgroundBashJob{ID: "fresh", Owner: "owner", Status: "succeeded", FinishedAt: now}}
		m.jobs["running"] = &backgroundBashEntry{job: BackgroundBashJob{ID: "running", Owner: "owner", Status: "running", FinishedAt: now.Add(-2 * BackgroundBashRetention)}}
		m.mu.Unlock()
		synctest.Wait() // Ensure the real sweep loop has installed its ticker.
		time.Sleep(time.Minute - time.Nanosecond)
		synctest.Wait()
		m.mu.Lock()
		_, before := m.jobs["expired"]
		m.mu.Unlock()
		if !before {
			t.Fatal("record removed before first tick")
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		m.mu.Lock()
		defer m.mu.Unlock()
		for id, want := range map[string]bool{"expired": false, "fresh": true, "running": true} {
			_, present := m.jobs[id]
			if present != want {
				t.Errorf("%s present=%v, want %v", id, present, want)
			}
		}
	})
}
