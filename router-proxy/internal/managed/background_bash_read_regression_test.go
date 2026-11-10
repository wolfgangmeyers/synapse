package managed

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestBackgroundBashMetadataReadsAvoidOutput(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	defer m.Close()
	exit := 3
	e := &backgroundBashEntry{job: BackgroundBashJob{ID: "id", Owner: "owner", Command: "secret", Status: "failed", ExitCode: &exit, FinishedAt: time.Now()}}
	e.output.Write([]byte(strings.Repeat("x", BackgroundBashMaxOutput+1)))
	m.mu.Lock()
	m.jobs["id"] = e
	m.mu.Unlock()
	list := m.List("owner")
	if len(list) != 1 || list[0].Output != "" || !list[0].Truncated || *list[0].ExitCode != 3 {
		t.Fatalf("bad metadata: %+v", list)
	}
	projection := m.projectionJobs("owner")
	if len(projection) != 1 || projection[0].Output != "" || projection[0].Command != "" || projection[0].Owner != "" {
		t.Fatalf("bad projection: %+v", projection)
	}
	full, err := m.Get("owner", "id")
	if err != nil || len(full.Output) != BackgroundBashMaxOutput || !full.Truncated {
		t.Fatalf("bad full read: %+v %v", full, err)
	}
}

func TestBackgroundBashPruningReadsDoNotWaitButCloseJoinsCallback(t *testing.T) {
	for _, method := range []string{"get", "list"} {
		t.Run(method, func(t *testing.T) {
			m := NewBackgroundBashManager(nil)
			entered, release := make(chan struct{}), make(chan struct{})
			m.SetChangeCallback(func() { close(entered); <-release })
			m.mu.Lock()
			m.jobs["old"] = &backgroundBashEntry{job: BackgroundBashJob{ID: "old", Owner: "owner", Status: "succeeded", FinishedAt: time.Now().Add(-2 * BackgroundBashRetention)}}
			m.mu.Unlock()
			returned := make(chan struct{})
			go func() {
				if method == "get" {
					_, _ = m.Get("owner", "old")
				} else {
					m.List("owner")
				}
				close(returned)
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("callback not entered")
			}
			select {
			case <-returned:
			case <-time.After(time.Second):
				close(release)
				t.Fatal("read blocked on callback")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			if err := m.CloseContext(ctx); err == nil {
				close(release)
				t.Fatal("close did not join callback")
			}
			close(release)
			ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
			defer cancel2()
			if err := m.CloseContext(ctx2); err != nil {
				t.Fatal(err)
			}
		})
	}
}
