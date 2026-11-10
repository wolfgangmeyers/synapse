//go:build linux

package managed

import (
	"context"
	"encoding/json"

	"fmt"
	"github.com/sky-valley/pi/ai"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestForegroundOverflowDrainsAndCompletes(t *testing.T) {
	jobs := NewBackgroundBashManager(nil)
	defer jobs.Close()
	owner := embeddedOwner{identity: "overflow-contract", backgroundBash: &backgroundBashOwnerCapability{}}
	marker := filepath.Join(t.TempDir(), "completed")
	m := &Manager{backgroundBash: jobs}
	result, err := m.executePromotableBash(context.Background(), owner, t.TempDir(), embeddedBashCommand{
		command: fmt.Sprintf("head -c 70000 /dev/zero; sleep 0.1; touch %q; exit 7", marker), timeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	text := fmt.Sprint(result.Content)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("command did not finish after overflow: %v; result: %s", err, text[:min(len(text), 150)])
	}
	if !strings.Contains(text, "exit=7") || !strings.Contains(text, "Output truncated") {
		t.Fatalf("expected actual exit and truncation, got %.150s", text)
	}
}

func TestForegroundOverflowPromotesSameProcess(t *testing.T) {
	jobs := NewBackgroundBashManager(nil)
	defer jobs.Close()
	owner := embeddedOwner{identity: "overflow-promotion", backgroundBash: &backgroundBashOwnerCapability{}}
	dir := t.TempDir()
	release := filepath.Join(dir, "release")
	starts := filepath.Join(dir, "starts")
	finished := filepath.Join(dir, "finished")
	defer os.WriteFile(release, []byte("release"), 0600)
	m := &Manager{backgroundBash: jobs}
	command := fmt.Sprintf("echo $$ > %q; head -c 70000 /dev/zero; while [ ! -f %q ]; do sleep 0.02; done; echo $$ > %q; exit 7", starts, release, finished)
	result, err := m.executePromotableBash(context.Background(), owner, dir, embeddedBashCommand{command: command, timeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	text, ok := result.Content[0].(ai.TextContent)
	if !ok {
		t.Fatalf("unexpected result: %v", result.Content)
	}
	var payload struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(text.Text), &payload); err != nil || payload.JobID == "" {
		t.Fatalf("expected promotion, got %.150s: %v", text.Text, err)
	}
	job, err := jobs.Get(backgroundBashOwnerKey(owner), payload.JobID)
	if err != nil || !job.Truncated {
		t.Fatalf("expected truncated running job: %+v %v", job, err)
	}
	if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		job, err = jobs.Get(backgroundBashOwnerKey(owner), payload.JobID)
		if err == nil && job.Status != "running" && job.Status != "uncertain" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || job.Status != "failed" || job.ExitCode == nil || *job.ExitCode != 7 {
		t.Fatalf("unexpected terminal job: %+v %v", job, err)
	}
	a, err := os.ReadFile(starts)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(finished)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("shell changed: %q != %q", a, b)
	}
}
