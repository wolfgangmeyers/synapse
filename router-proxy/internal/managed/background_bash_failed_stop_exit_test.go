//go:build linux

package managed

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestBackgroundFailedStopThenNaturalExitClearsUncertainty(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	defer m.Close()
	gate := filepath.Join(t.TempDir(), "exit")
	j, err := m.Launch("owner", BackgroundBashLaunch{Command: "while [ ! -f '" + gate + "' ]; do sleep 0.05; done; exit 7"})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	entry := m.jobs[j.ID]
	m.signal = func(ProcessIdentity, syscall.Signal) error { return errors.New("injected signal failure") }
	m.mu.Unlock()
	if err := m.stop(entry, "killed"); err == nil {
		t.Fatal("expected stop failure")
	}
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entry.done:
	case <-time.After(5 * time.Second):
		t.Fatal("job did not finish")
	}
	j, err = m.Get("owner", j.ID)
	if err != nil || j.Status != "failed" || j.Error != "" || j.ExitCode == nil || *j.ExitCode != 7 {
		t.Fatalf("natural exit: %+v, %v", j, err)
	}
	if err := m.StopOwner("owner"); err != nil {
		t.Fatalf("owner cleanup: %v", err)
	}
}

func TestBackgroundFailedExplicitStopPublishesUncertaintyAndCanRecover(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	defer m.Close()
	changes := make(chan string, 10)
	m.SetChangeCallback(func() { changes <- "owner" })
	j, err := m.Launch("owner", BackgroundBashLaunch{Command: "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	<-changes // launch notification
	m.mu.Lock()
	entry := m.jobs[j.ID]
	m.signal = func(ProcessIdentity, syscall.Signal) error { return errors.New("injected signal failure") }
	m.mu.Unlock()
	if err := m.stop(entry, "killed"); err == nil {
		t.Fatal("expected stop failure")
	}
	j, err = m.Get("owner", j.ID)
	if err != nil || j.Status != "uncertain" || j.Error == "" {
		t.Errorf("intermediate job: %+v, %v", j, err)
	}
	select {
	case owner := <-changes:
		if owner != "owner" {
			t.Errorf("change owner: %q", owner)
		}
	case <-time.After(time.Second):
		t.Error("no uncertainty change notification")
	}
	m.mu.Lock()
	m.signal = nil
	m.mu.Unlock()
	if _, err := m.Kill("owner", j.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entry.done:
	case <-time.After(5 * time.Second):
		t.Fatal("job did not finish")
	}
	j, err = m.Get("owner", j.ID)
	if err != nil || j.Status != "killed" || j.Error != "" {
		t.Fatalf("recovered job: %+v, %v", j, err)
	}
}

func TestBackgroundTimeoutFailureThenStopRetainsDiagnosticUntilWaiterConfirms(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	gate := make(chan struct{})
	var release sync.Once
	m.waitCommand = func(cmd *exec.Cmd, id ProcessIdentity, reap func(func() error) error) error {
		<-gate
		return waitClaudeCodeCommandWithReapHook(cmd, id, nil, reap)
	}
	m.signal = func(ProcessIdentity, syscall.Signal) error { return errors.New("timeout signal failed") }
	j, err := m.Launch("owner", BackgroundBashLaunch{Command: "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	entry := m.jobs[j.ID]
	m.mu.Unlock()
	defer func() { release.Do(func() { close(gate) }); _ = m.Close() }()
	go m.timeoutStop(entry)
	deadline := time.Now().Add(3 * time.Second)
	for {
		j, err = m.Get("owner", j.ID)
		if err != nil {
			t.Fatal(err)
		}
		if j.Status == "uncertain" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timeout failure not published")
		}
		time.Sleep(time.Millisecond)
	}
	diagnostic := j.Error
	if diagnostic == "" {
		t.Fatal("missing timeout diagnostic")
	}
	m.mu.Lock()
	m.signal = nil
	m.mu.Unlock()
	if err := m.stop(entry, "killed"); err != nil {
		t.Fatal(err)
	}
	j, err = m.Get("owner", j.ID)
	if err != nil || j.Status != "uncertain" || j.Error != diagnostic {
		t.Fatalf("before waiter confirmation: %+v, %v; want diagnostic %q", j, err, diagnostic)
	}
	release.Do(func() { close(gate) })
	select {
	case <-entry.done:
	case <-time.After(5 * time.Second):
		t.Fatal("waiter did not finish")
	}
	j, err = m.Get("owner", j.ID)
	if err != nil || j.Status != "killed" || j.Error != "" {
		t.Fatalf("after waiter confirmation: %+v, %v", j, err)
	}
}
