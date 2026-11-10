//go:build linux

package managed

import (
	"errors"
	"os/exec"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestUnidentifiedRetriesThenReapsWithoutSignalingUnrelatedProcess(t *testing.T) {
	unrelated := exec.Command("sleep", "30")
	if err := unrelated.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unrelated.Process.Kill(); _ = unrelated.Wait() }()
	cmd := exec.Command("bash", "-c", "sleep 30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	if waitUnidentifiedBackground(cmd, time.Second, func(pid int, sig syscall.Signal) error {
		if pid != -cmd.Process.Pid {
			t.Errorf("wrong group %d", pid)
		}
		if calls.Add(1) == 1 {
			return errors.New("transient")
		}
		return syscall.Kill(pid, sig)
	}, false, 1) {
		t.Fatal("unverified group cleanup reported as confirmed")
	}
	if calls.Load() != 2 || cmd.ProcessState == nil {
		t.Fatal("expected two signals and reap")
	}
	if err := unrelated.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("unrelated process affected: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if calls.Load() != 2 {
		t.Fatal("signal after wait")
	}
}

func TestUnidentifiedSignalFailureIsBounded(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	calls := 0
	start := time.Now()
	confirmed := waitUnidentifiedBackground(cmd, time.Second, func(int, syscall.Signal) error { calls++; return errors.New("injected failure") }, false, 1)
	if confirmed || calls != 2 || cmd.ProcessState == nil {
		t.Fatalf("confirmed=%v calls=%d reaped=%v", confirmed, calls, cmd.ProcessState != nil)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("cleanup not bounded")
	}
}
