package managed

import (
	"context"
	"testing"
	"time"
)

func TestBackgroundBashShutdownNotifiesKilledJob(t *testing.T) {
	notices := make(chan BackgroundBashJob, 2)
	m := NewBackgroundBashManager(func(job BackgroundBashJob) { notices <- job })
	job, err := m.Launch("owner", BackgroundBashLaunch{Command: "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.CloseContext(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case notice := <-notices:
		if notice.ID != job.ID || notice.Owner != "owner" || notice.Status != "killed" {
			t.Fatalf("unexpected notice: %+v", notice)
		}
	default:
		t.Fatal("shutdown suppressed completion notice")
	}
	select {
	case notice := <-notices:
		t.Fatalf("duplicate notice: %+v", notice)
	default:
	}
}
