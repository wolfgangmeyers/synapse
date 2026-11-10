//go:build linux

package managed

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

func TestBackgroundLaunchBlockedStartAndIdentificationDeadline(t *testing.T) {
	for _, stage := range []string{"start", "identify"} {
		t.Run(stage, func(t *testing.T) {
			m := NewBackgroundBashManager(nil)
			entered, release := make(chan struct{}), make(chan struct{})
			if stage == "start" {
				m.startCommand = func(cmd *exec.Cmd) error { close(entered); <-release; return cmd.Start() }
			} else {
				m.identify = func(pid int) (string, error) { close(entered); <-release; return processStartMarker(pid) }
			}
			done := make(chan error, 1)
			go func() { _, err := m.Launch("owner", BackgroundBashLaunch{Command: "sleep 30"}); done <- err }()
			<-entered
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			closed := make(chan error, 1)
			go func() { closed <- m.CloseContext(ctx) }()
			select {
			case err := <-closed:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("close: %v", err)
				}
			case <-time.After(time.Second):
				t.Error("close blocked on launch mutex")
			}
			close(release)
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("launch did not finish")
			}
			retry, end := context.WithTimeout(context.Background(), 5*time.Second)
			defer end()
			if err := m.CloseContext(retry); err != nil {
				t.Fatalf("retry close: %v", err)
			}
		})
	}
}

func TestBackgroundCapabilityRevokeWakesWaitingLaunch(t *testing.T) {
	c := &backgroundBashOwnerCapability{}
	entered, release := make(chan struct{}), make(chan struct{})
	go c.launch(func() (BackgroundBashJob, error) { close(entered); <-release; return BackgroundBashJob{}, nil })
	<-entered
	// Install the gate under the mutex so the second launch necessarily waits on it.
	c.mu.Lock()
	c.fenced = make(chan struct{})
	gate := c.fenced
	c.mu.Unlock()
	waiting := make(chan error, 1)
	go func() {
		_, err := c.launch(func() (BackgroundBashJob, error) { return BackgroundBashJob{}, errors.New("unexpected admission") })
		waiting <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	revoked := make(chan error, 1)
	go func() { revoked <- c.revokeContext(ctx, func() error { return nil }) }()
	<-gate
	select {
	case err := <-waiting:
		if !errors.Is(err, errBackgroundBashOwnerRetired) {
			t.Errorf("waiting launch: %v", err)
		}
	case <-time.After(time.Second):
		t.Error("revocation did not wake waiting launch")
	}
	close(release)
	<-revoked
}
