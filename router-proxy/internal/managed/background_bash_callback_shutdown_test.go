package managed

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestBackgroundBashCloseJoinsTerminalCompletion(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	m := NewBackgroundBashManager(func(BackgroundBashJob) { close(entered); <-release })
	if _, err := m.Launch("owner", BackgroundBashLaunch{Command: "true"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("callback not entered")
	}
	done := make(chan error, 1)
	go func() { done <- m.CloseContext(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("close returned while callback blocked: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close did not join callback")
	}
}

func TestBackgroundBashCloseRacesStopAndCallback(t *testing.T) {
	for i := 0; i < 20; i++ {
		m := NewBackgroundBashManager(nil)
		m.SetChangeCallback(func() { _ = m.projectionJobs("owner") })
		job, err := m.Launch("owner", BackgroundBashLaunch{Command: "sleep 30"})
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _ = m.StopOwner("owner") }()
		go func() { defer wg.Done(); _ = m.Close() }()
		wg.Wait()
		if _, err := m.Launch("owner", BackgroundBashLaunch{Command: "true"}); err == nil {
			t.Fatal("launch admitted after close")
		}
		if j, err := m.Get("owner", job.ID); err != nil {
			t.Fatal(err)
		} else if j.Status == "running" {
			t.Fatal("job still running")
		}
	}
}

// Exercise the real SQLite admission path while shutdown races a terminal job.
func TestBackgroundBashCloseJoinsBlockedAdmission(t *testing.T) {
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	waitGateStarts(t, f.m)
	entered, release := make(chan struct{}), make(chan struct{})
	jobs := NewBackgroundBashManager(func(job BackgroundBashJob) {
		f.m.enqueueBackgroundBashCompletion(job, func() (func(bool), error) {
			close(entered)
			<-release
			return nil, nil
		})
	})
	if _, err := jobs.Launch(backgroundBashOwnerKey(owner), BackgroundBashLaunch{Command: "true"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("admission not reached")
	}
	done := make(chan error, 1)
	go func() { done <- jobs.CloseContext(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("close returned during admission: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close did not join admission")
	}
}

func TestBackgroundBashCloseContextDeadlineAndRetry(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	m := NewBackgroundBashManager(func(BackgroundBashJob) { close(entered); <-release })
	if _, err := m.Launch("owner", BackgroundBashLaunch{Command: "true"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("callback not entered")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.CloseContext(ctx); err == nil {
		t.Fatal("expected deadline error")
	}
	close(release)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if err := m.CloseContext(ctx2); err != nil {
		t.Fatal(err)
	}
}
