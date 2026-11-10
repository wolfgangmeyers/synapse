//go:build linux

package managed

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func awaitBackground(t *testing.T, m *BackgroundBashManager, owner, id string) BackgroundBashJob {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, e := m.Get(owner, id)
		if e != nil {
			t.Fatal(e)
		}
		if j.Status != "running" {
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return BackgroundBashJob{}
}

func TestBackgroundLifecycleSuccessAndOwnership(t *testing.T) {
	notices := make(chan BackgroundBashJob, 2)
	m := NewBackgroundBashManager(func(j BackgroundBashJob) { notices <- j })
	defer m.Close()
	j, e := m.Launch("owner", BackgroundBashLaunch{Command: "printf hi; printf err >&2; exit 7"})
	if e != nil {
		t.Fatal(e)
	}
	if j.Deadline.Sub(j.StartedAt) != 600*time.Second {
		t.Fatal(j.Deadline)
	}
	if _, e = m.Get("other", j.ID); e == nil {
		t.Fatal("cross owner read")
	}
	j = awaitBackground(t, m, "owner", j.ID)
	if j.Status != "failed" || j.ExitCode == nil || *j.ExitCode != 7 || !strings.Contains(j.Output, "hi") || !strings.Contains(j.Output, "err") {
		t.Fatalf("%+v", j)
	}
	select {
	case n := <-notices:
		if n.ID != j.ID {
			t.Fatal(n)
		}
	case <-time.After(time.Second):
		t.Fatal("no notice")
	}
}
func TestBackgroundLifecycleKillTimeoutAndOutput(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	defer m.Close()
	j, e := m.Launch("a", BackgroundBashLaunch{Command: "head -c 100000 /dev/zero; sleep 30", TimeoutSeconds: 1})
	if e != nil {
		t.Fatal(e)
	}
	j = awaitBackground(t, m, "a", j.ID)
	if j.Status != "timed_out" || !j.Truncated || len(j.Output) != BackgroundBashMaxOutput {
		t.Fatalf("%+v output=%d", j.Status, len(j.Output))
	}
	k, e := m.Launch("a", BackgroundBashLaunch{Command: "sleep 30"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Kill("b", k.ID); e == nil {
		t.Fatal("cross owner kill")
	}
	if _, e = m.Kill("a", k.ID); e != nil {
		t.Fatal(e)
	}
	k = awaitBackground(t, m, "a", k.ID)
	if k.Status != "killed" {
		t.Fatal(k.Status)
	}
	if _, e = m.Kill("a", k.ID); e != nil {
		t.Fatal(e)
	}
}
func TestBackgroundLifecycleInvalidAndClose(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	for _, r := range []BackgroundBashLaunch{{Command: " "}, {Command: "true", TimeoutSeconds: -1}, {Command: "true", Dir: "/not-a-real-directory"}} {
		if _, e := m.Launch("a", r); e == nil {
			t.Fatal("expected error", r)
		}
	}
	j, e := m.Launch("a", BackgroundBashLaunch{Command: "sleep 30"})
	if e != nil {
		t.Fatal(e)
	}
	m.Close()
	j, e = m.Get("a", j.ID)
	if e != nil || j.Status == "running" {
		t.Fatal(j, e)
	}
	if _, e = m.Launch("a", BackgroundBashLaunch{Command: "true"}); e == nil {
		t.Fatal("launch after close")
	}
}

func TestBackgroundLifecycleRetentionAndCapacity(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	defer m.Close()
	m.mu.Lock()
	for i := 0; i < BackgroundBashMaxRecords+10; i++ {
		id := fmt.Sprint(i)
		m.jobs[id] = &backgroundBashEntry{job: BackgroundBashJob{ID: id, Owner: "a", Status: "succeeded", FinishedAt: time.Now()}}
	}
	m.pruneLocked(time.Now())
	if len(m.jobs) != BackgroundBashMaxRecords {
		t.Fatal(len(m.jobs))
	}
	for _, e := range m.jobs {
		e.job.FinishedAt = time.Now().Add(-BackgroundBashRetention)
	}
	m.pruneLocked(time.Now())
	if len(m.jobs) != 0 {
		t.Fatal(len(m.jobs))
	}
	m.mu.Unlock()
	for i := 0; i < BackgroundBashMaxRunning; i++ {
		if _, e := m.Launch("a", BackgroundBashLaunch{Command: "sleep 30"}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := m.Launch("a", BackgroundBashLaunch{Command: "true"}); e == nil {
		t.Fatal("capacity not enforced")
	}
	if err := m.StopOwner("a"); err != nil {
		t.Fatal(err)
	}
	if _, e := m.Launch("new-owner", BackgroundBashLaunch{Command: "true"}); e != nil {
		t.Fatal(e)
	}
}

func TestBackgroundLifecycleChangeCallbackCanReadAndSeesRemoval(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	defer m.Close()
	events := make(chan int, 10)
	m.SetChangeCallback(func() { events <- len(m.List("a")) })
	j, err := m.Launch("a", BackgroundBashLaunch{Command: "sleep 0.1"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-events:
		if n != 1 {
			t.Fatal(n)
		}
	case <-time.After(time.Second):
		t.Fatal("launch callback blocked")
	}
	awaitBackground(t, m, "a", j.ID)
	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("terminal callback blocked")
	}
	m.mu.Lock()
	m.jobs[j.ID].job.FinishedAt = time.Now().Add(-BackgroundBashRetention)
	m.mu.Unlock()
	m.Get("a", j.ID)
	select {
	case n := <-events:
		if n != 0 {
			t.Fatal(n)
		}
	case <-time.After(time.Second):
		t.Fatal("prune callback blocked")
	}
}

func TestBackgroundLifecycleLeaderExitCleansChild(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	defer m.Close()
	// Redirect the child away from the capture pipe so leader completion is not delayed by pipe EOF.
	j, err := m.Launch("a", BackgroundBashLaunch{Command: "sleep 30 >/dev/null 2>&1 & echo $!"})
	if err != nil {
		t.Fatal(err)
	}
	j = awaitBackground(t, m, "a", j.ID)
	var pid int
	if _, err := fmt.Sscan(j.Output, &pid); err != nil {
		t.Fatal(j, err)
	}
	state, _, err := linuxProcStat(pid)
	if err == nil && state != 'Z' {
		t.Fatalf("child %d still running after status %s", pid, j.Status)
	}
}

func TestBackgroundLifecycleFailedStopCanRetry(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	defer m.Close()
	j, err := m.Launch("a", BackgroundBashLaunch{Command: "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	e := m.jobs[j.ID]
	m.signal = func(ProcessIdentity, syscall.Signal) error { return errors.New("injected signal failure") }
	m.mu.Unlock()
	if err = m.stop(e, "timed_out"); err == nil {
		t.Fatal("expected signal failure")
	}
	m.mu.Lock()
	m.signal = nil
	m.mu.Unlock()
	if _, err = m.Kill("a", j.ID); err != nil {
		t.Fatalf("retry: %v", err)
	}
	j = awaitBackground(t, m, "a", j.ID)
	if j.Status != "killed" {
		t.Fatal(j)
	}
}

func TestBackgroundLifecycleOwnerCleanupReportsFailure(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	j, err := m.Launch("a", BackgroundBashLaunch{Command: "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.signal = func(ProcessIdentity, syscall.Signal) error { return errors.New("injected cleanup failure") }
	m.mu.Unlock()
	if err = m.StopOwner("a"); err == nil || !strings.Contains(err.Error(), "injected cleanup failure") {
		t.Fatalf("cleanup error: %v", err)
	}
	snapshot, err := m.Get("a", j.ID)
	if err != nil || !strings.Contains(snapshot.Error, "injected cleanup failure") {
		t.Fatalf("snapshot: %+v %v", snapshot, err)
	}
	m.mu.Lock()
	m.signal = nil
	m.mu.Unlock()
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBackgroundUncertainCleanupSurvivesRetention(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	done := make(chan struct{})
	close(done)
	m.mu.Lock()
	m.jobs["uncertain"] = &backgroundBashEntry{job: BackgroundBashJob{ID: "uncertain", Owner: "a", Status: "uncertain", Error: "group unconfirmed", FinishedAt: time.Now().Add(-2 * BackgroundBashRetention)}, done: done}
	m.pruneLocked(time.Now())
	m.mu.Unlock()
	if err := m.StopOwner("a"); err == nil || !strings.Contains(err.Error(), "group unconfirmed") {
		t.Fatalf("owner cleanup: %v", err)
	}
	if err := m.Close(); err == nil || !strings.Contains(err.Error(), "group unconfirmed") {
		t.Fatalf("close: %v", err)
	}
	if err := m.Close(); err == nil {
		t.Fatal("repeated close claimed success")
	}
}

func TestBackgroundSignalFailureWaitsForConcurrentReap(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	defer m.Close()
	j, err := m.Launch("a", BackgroundBashLaunch{Command: "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.signal = func(id ProcessIdentity, sig syscall.Signal) error {
		if err := DefaultProcessGroupOps().SignalVerifiedProcessGroup(id, sig); err != nil {
			return err
		}
		return errors.New("identity disappeared during completion")
	}
	m.mu.Unlock()
	result, err := m.Kill("a", j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status == "uncertain" || result.Status == "running" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Error != "" {
		t.Fatalf("stale error: %+v", result)
	}
}

func TestBackgroundIdentityAndCleanupFailureRetainsAndReaps(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	m.identify = func(int) (string, error) { return "", errors.New("injected identity failure") }
	m.killGroup = func(int, syscall.Signal) error { return errors.New("injected group failure") }
	_, err := m.Launch("a", BackgroundBashLaunch{Command: "sleep 0.2"})
	if err == nil || !strings.Contains(err.Error(), "injected group failure") {
		t.Fatalf("launch: %v", err)
	}
	jobs := m.List("a")
	if len(jobs) != 1 || jobs[0].Status != "uncertain" {
		t.Fatalf("untracked: %+v", jobs)
	}
	m.mu.Lock()
	entry := m.jobs[jobs[0].ID]
	for i := 1; i < BackgroundBashMaxRunning; i++ {
		id := fmt.Sprint(i)
		m.jobs[id] = &backgroundBashEntry{job: BackgroundBashJob{ID: id, Status: "uncertain"}}
	}
	m.mu.Unlock()
	if _, err := m.Launch("b", BackgroundBashLaunch{Command: "true"}); err == nil || !strings.Contains(err.Error(), "capacity") {
		t.Fatalf("capacity: %v", err)
	}
	m.mu.Lock()
	for i := 1; i < BackgroundBashMaxRunning; i++ {
		delete(m.jobs, fmt.Sprint(i))
	}
	m.mu.Unlock()
	select {
	case <-entry.done:
	case <-time.After(5 * time.Second):
		t.Fatal("child not reaped")
	}
	if entry.cmd.ProcessState == nil {
		t.Fatal("no process wait")
	}
	if err := m.StopOwner("a"); err == nil {
		t.Fatal("owner cleanup hid uncertainty")
	}
	if err := m.Close(); err == nil {
		t.Fatal("close hid uncertainty")
	}
}

func TestBackgroundIdentityFailureSignalIsNotConfirmationAndDeadlineRetries(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("deadline retry requires Linux non-reaping wait")
	}
	m := NewBackgroundBashManager(nil)
	m.identify = func(int) (string, error) { return "", errors.New("identity unavailable") }
	calls := make(chan int, 8)
	m.killGroup = func(pid int, sig syscall.Signal) error {
		calls <- pid
		if len(calls) == 1 {
			return nil
		} // Signal accepted but process remains alive.
		return syscall.Kill(pid, sig)
	}
	returned := make(chan error, 1)
	go func() {
		_, err := m.Launch("a", BackgroundBashLaunch{Command: "sleep 30", TimeoutSeconds: 1})
		returned <- err
	}()
	select {
	case err := <-returned:
		if err == nil {
			t.Fatal("expected identity failure")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("launch blocked on child wait")
	}
	jobs := m.List("a")
	if len(jobs) != 1 || jobs[0].Status != "uncertain" {
		t.Fatalf("missing uncertain job: %+v", jobs)
	}
	m.mu.Lock()
	entry := m.jobs[jobs[0].ID]
	m.mu.Unlock()
	select {
	case <-entry.done:
	case <-time.After(4 * time.Second):
		t.Fatal("deadline did not retry cleanup")
	}
	if len(calls) < 2 {
		t.Fatal("no deadline retry")
	}
	if j, _ := m.Get("a", jobs[0].ID); j.Status != "failed" {
		t.Fatalf("failed launch reported as %s", j.Status)
	}
	if err := m.Close(); err == nil {
		t.Fatal("unconfirmed group cleanup must remain uncertain")
	}
}

func TestBackgroundTimeoutSignalFailureRetriesAndPublishesUncertainty(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	defer m.Close()
	calls := make(chan struct{}, 4)
	release := make(chan struct{})
	m.signal = func(id ProcessIdentity, sig syscall.Signal) error {
		calls <- struct{}{}
		select {
		case <-release:
			return DefaultProcessGroupOps().SignalVerifiedProcessGroup(id, sig)
		default:
			return errors.New("injected timeout failure")
		}
	}
	j, err := m.Launch("a", BackgroundBashLaunch{Command: "sleep 30", TimeoutSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(release); m.mu.Lock(); m.signal = nil; m.mu.Unlock(); _, _ = m.Kill("a", j.ID) }()
	select {
	case <-calls:
	case <-time.After(2 * time.Second):
		t.Fatal("no timeout signal")
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		j, err = m.Get("a", j.ID)
		if err != nil {
			t.Fatal(err)
		}
		if j.Status == "uncertain" && strings.Contains(j.Error, "injected timeout failure") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("uncertainty not published: %+v", j)
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-calls:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("no prompt verified retry")
	}
}

func TestBackgroundUnidentifiedInitialSignalFailureRetriesPromptly(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux pinned child")
	}
	m := NewBackgroundBashManager(nil)
	m.identify = func(int) (string, error) { return "", errors.New("identity unavailable") }
	calls := make(chan int, 4)
	var attempts atomic.Int32
	m.killGroup = func(pid int, sig syscall.Signal) error {
		calls <- pid
		if attempts.Add(1) == 1 {
			return errors.New("injected failure")
		}
		return syscall.Kill(pid, sig)
	}
	_, err := m.Launch("a", BackgroundBashLaunch{Command: "sleep 30"})
	if err == nil {
		t.Fatal("expected launch error")
	}
	<-calls
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("failed initial signal was not retried promptly")
	}
	_ = m.Close()
}

func TestBackgroundSignalPinsLeaderUntilSignalReturns(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux non-reaping wait")
	}
	m := NewBackgroundBashManager(nil)
	defer m.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	observed := make(chan error, 1)
	m.signal = func(id ProcessIdentity, sig syscall.Signal) error {
		close(entered)
		<-release
		if err := syscall.Kill(id.PID, 0); err != nil {
			observed <- err
			return fmt.Errorf("leader reaped during signal: %w", err)
		}
		observed <- nil
		return syscall.Kill(-id.PID, sig)
	}
	j, err := m.Launch("a", BackgroundBashLaunch{Command: "sleep 0.2"})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := m.Kill("a", j.ID); result <- err }()
	<-entered
	time.Sleep(500 * time.Millisecond)
	close(release)
	if err := <-observed; err != nil {
		t.Errorf("leader reaped during verified signal: %v", err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestBackgroundTimeoutReentrantCallbackDoesNotBlockRetry(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	defer m.Close()
	var attempts atomic.Int32
	m.signal = func(id ProcessIdentity, sig syscall.Signal) error {
		if attempts.Add(1) == 1 {
			return errors.New("first signal fails")
		}
		return DefaultProcessGroupOps().SignalVerifiedProcessGroup(id, sig)
	}
	var id atomic.Value
	m.SetChangeCallback(func() {
		owner := "a"
		v := id.Load()
		if v == nil {
			return
		}
		j, err := m.Get(owner, v.(string))
		if err == nil && j.Status == "uncertain" {
			_, _ = m.Kill(owner, j.ID)
		}
	})
	j, err := m.Launch("a", BackgroundBashLaunch{Command: "sleep 30", TimeoutSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	id.Store(j.ID)
	deadline := time.After(4 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("cleanup retry blocked by callback")
		default:
		}
		if attempts.Load() >= 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBackgroundSweepCallbackCanClose(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	m.mu.Lock()
	m.jobs["expired"] = &backgroundBashEntry{job: BackgroundBashJob{ID: "expired", Owner: "a", Status: "succeeded", FinishedAt: time.Now().Add(-2 * BackgroundBashRetention)}}
	m.mu.Unlock()
	done := make(chan struct{})
	m.SetChangeCallback(func() { _ = m.Close(); close(done) })
	m.sweepOnce()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reentrant close deadlocked")
	}
}

func TestBackgroundUnconfirmedWaitRetriesUntilReaped(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	var calls atomic.Int32
	m.waitCommand = func(cmd *exec.Cmd, id ProcessIdentity, reap func(func() error) error) error {
		if calls.Add(1) == 1 {
			return errClaudeCodeGroupUnconfirmed
		}
		return waitClaudeCodeCommandWithReapHook(cmd, id, nil, reap)
	}
	j, err := m.Launch("a", BackgroundBashLaunch{Command: "exit 0"})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("unconfirmed cleanup never retried")
		default:
		}
		m.mu.Lock()
		e := m.jobs[j.ID]
		select {
		case <-e.done:
			m.mu.Unlock()
			if e.job.Status != "succeeded" || e.job.Error != "" {
				t.Fatalf("successful retry retained uncertainty: %+v", e.job)
			}
			if calls.Load() < 2 || e.cmd.ProcessState == nil {
				t.Fatal("leader not reaped after retry")
			}
			return
		default:
		}
		m.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBackgroundTimeoutRetryPreservesUncertaintyUntilCleanup(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	release := make(chan struct{})
	entered := make(chan struct{})
	m.waitCommand = func(cmd *exec.Cmd, id ProcessIdentity, reap func(func() error) error) error {
		close(entered)
		<-release
		return waitClaudeCodeCommandWithReapHook(cmd, id, nil, reap)
	}
	var attempts atomic.Int32
	retried := make(chan struct{})
	m.signal = func(id ProcessIdentity, sig syscall.Signal) error {
		if attempts.Add(1) == 1 {
			return errors.New("injected timeout failure")
		}
		err := DefaultProcessGroupOps().SignalVerifiedProcessGroup(id, sig)
		close(retried)
		return err
	}
	j, err := m.Launch("a", BackgroundBashLaunch{Command: "sleep 30", TimeoutSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer func() { unblock(); _ = m.Close() }()
	<-entered
	select {
	case <-retried:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout retry did not signal")
	}
	// Synchronize with timeoutStop after its signal callback returns.
	m.mu.Lock()
	got := m.snapshot(m.jobs[j.ID])
	m.mu.Unlock()
	if got.Status != "uncertain" || !strings.Contains(got.Error, "injected timeout failure") {
		t.Fatalf("signal success prematurely cleared uncertainty: %+v", got)
	}
	unblock()
	deadline := time.After(5 * time.Second)
	for {
		got, err = m.Get("a", j.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == "timed_out" {
			if got.Error != "" {
				t.Fatalf("cleanup retained error: %+v", got)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("cleanup not confirmed: %+v", got)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
