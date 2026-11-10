//go:build linux

package managed

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"testing"
	"time"
)

// These exercise the same owner-scoped primitive that the subagent wait tool uses.
func TestBackgroundBashWaitOwnerAndCompletion(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	defer m.Close()
	dir := t.TempDir()
	job, err := m.Launch("child", BackgroundBashLaunch{Command: `while [ ! -f release ]; do sleep .01; done; exit 7`, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Wait(context.Background(), "other", job.ID); err == nil {
		t.Fatal("foreign owner waited on job")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan BackgroundBashJob, 1)
	failures := make(chan error, 1)
	go func() {
		j, err := m.Wait(ctx, "child", job.ID)
		if err != nil {
			failures <- err
			return
		}
		result <- j
	}()
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-failures:
		t.Fatal(err)
	case j := <-result:
		if j.ID != job.ID || j.Status != "failed" || j.ExitCode == nil || *j.ExitCode != 7 {
			t.Fatalf("unexpected result: %+v", j)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Completion before registration must return immediately with the same outcome.
	j, err := m.Wait(ctx, "child", job.ID)
	if err != nil || j.Status != "failed" || j.ExitCode == nil || *j.ExitCode != 7 {
		t.Fatalf("late wait: %+v, %v", j, err)
	}
}

func TestBackgroundBashWaitCancellationAndKill(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	defer m.Close()
	job, err := m.Launch("child", BackgroundBashLaunch{Command: "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Wait(ctx, "child", job.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan BackgroundBashJob, 1)
	failures := make(chan error, 1)
	go func() {
		j, err := m.Wait(ctx, "child", job.ID)
		if err != nil {
			failures <- err
			return
		}
		result <- j
	}()
	if _, err := m.Kill("child", job.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-failures:
		t.Fatal(err)
	case j := <-result:
		if j.Status != "killed" {
			t.Fatalf("unexpected result: %+v", j)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestBackgroundBashWaitToolAndShutdown(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	job, err := m.Launch("child", BackgroundBashLaunch{Command: "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	tool := backgroundBashWaitTool(m, "child")
	foreign := backgroundBashWaitTool(m, "other")
	if _, err := foreign.Execute(context.Background(), "foreign", map[string]any{"job_id": job.ID}, nil); err == nil {
		t.Fatal("foreign tool accessed job")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan agent.AgentToolResult, 1)
	failures := make(chan error, 1)
	go func() {
		r, e := tool.Execute(ctx, "wait", map[string]any{"job_id": job.ID}, nil)
		if e != nil {
			failures <- e
			return
		}
		result <- r
	}()
	m.Close()
	select {
	case err := <-failures:
		t.Fatal(err)
	case r := <-result:
		text, ok := r.Content[0].(ai.TextContent)
		if !ok || !strings.Contains(text.Text, job.ID) || !strings.Contains(text.Text, "killed") {
			t.Fatalf("unexpected tool result: %+v", r)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
