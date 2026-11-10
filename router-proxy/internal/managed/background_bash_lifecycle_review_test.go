//go:build linux

package managed

import (
	"context"
	"fmt"
	"github.com/sky-valley/pi/ai"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPromotionCleanupDoesNotSignalLostIdentity(t *testing.T) {
	cmd := exec.Command("true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	err := cleanupForeground(cmd)
	if err == nil || !strings.Contains(err.Error(), "uncertain") {
		t.Fatalf("expected uncertainty, got %v", err)
	}
}

func TestForegroundFastExitCleansDescendant(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pid")
	m := &Manager{}
	result, err := m.executePromotableBash(context.Background(), embeddedOwner{}, dir, embeddedBashCommand{command: fmt.Sprintf("sleep 30 & echo $! > %q; exit 0", path), timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content[0].(ai.TextContent).Text, "exit=0") {
		t.Fatal(result)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	state, _, err := linuxProcStat(pid)
	if err == nil && state != 'Z' {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("descendant still alive: %d state %c", pid, state)
	}
}

func TestForegroundFastTruncatedExitPreservesCode(t *testing.T) {
	// The child exits before observation; the captured overflow must not overwrite its status.
	m := &Manager{embeddedBashAfterStart: func(pid int) {
		for {
			var info unix.Siginfo
			if err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT|unix.WNOHANG, nil); err != nil || info.Signo != 0 {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}}
	r, err := m.executePromotableBash(context.Background(), embeddedOwner{}, t.TempDir(), embeddedBashCommand{command: "head -c 70000 /dev/zero; exit 23", timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	text := r.Content[0].(ai.TextContent).Text
	if !strings.Contains(text, "exit=23") {
		t.Fatalf("lost exit code: %.30s", text)
	}
}

func TestForegroundCleanupRejectsWrongGroup(t *testing.T) {
	cmd := exec.Command("sleep", "30") // Inherits our group, not a group led by its PID.
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	marker, err := processStartMarker(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	err = cleanupForegroundWithIdentity(cmd, ProcessIdentity{PID: cmd.Process.Pid, StartMarker: marker, Incarnation: "foreground"})
	if err == nil || !strings.Contains(err.Error(), "uncertain") {
		t.Fatalf("expected uncertainty for wrong group: %v", err)
	}
	if cmd.ProcessState != nil {
		t.Fatal("unverified child was prematurely reaped")
	}
	// This child is not a group leader; clean up only the direct child.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
}
