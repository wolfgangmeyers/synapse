//go:build linux

package managed

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"golang.org/x/sys/unix"
)

// A reserved entry exists before Start and remains the sole lifecycle owner until
// either foreground reaping completes or its waiter is handed to the manager.
func (m *Manager) executePromotableBash(ctx context.Context, owner embeddedOwner, cwd string, spec embeddedBashCommand) (agent.AgentToolResult, error) {
	m.mu.Lock()
	if m.backgroundBash == nil && !m.closed {
		if m.store == nil {
			m.backgroundBash = NewBackgroundBashManager(nil)
		} else {
			m.backgroundBash = NewBackgroundBashManager(m.backgroundBashCompleted)
			m.backgroundBash.SetChangeCallbackContext(m.backgroundBashChanged)
		}
	}
	m.mu.Unlock()
	m.mu.Lock()
	jobs := m.backgroundBash
	m.mu.Unlock()
	if jobs == nil {
		return agent.AgentToolResult{}, errors.New("background bash unavailable")
	}
	cmd, gate, err := backgroundBashCommand(spec.command, owner.claudeOrphanRoot)
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	if gate != nil {
		defer gate.Close()
		defer cmd.ExtraFiles[0].Close()
	}
	cmd.Dir, cmd.Env = cwd, os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	out := &backgroundBashOutput{overflow: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = out, out
	var e *backgroundBashEntry
	type identityResult struct {
		marker string
		err    error
	}
	var pendingIdentity <-chan identityResult
	startIdentity := func(pid int) <-chan identityResult {
		result := make(chan identityResult, 1)
		jobs.mu.Lock()
		identify := jobs.identify
		jobs.callbackAddLocked()
		jobs.mu.Unlock()
		if identify == nil {
			identify = processStartMarker
		}
		go func() {
			defer jobs.callbackDone()
			marker, err := identify(pid)
			result <- identityResult{marker, err}
		}()
		return result
	}
	start := func() (BackgroundBashJob, error) {
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			return BackgroundBashJob{}, err
		}
		id := hex.EncodeToString(token[:])
		jobs.mu.Lock()
		if jobs.closed {
			jobs.mu.Unlock()
			return BackgroundBashJob{}, errors.New("background bash manager closed")
		}
		if err := ctx.Err(); err != nil {
			jobs.mu.Unlock()
			return BackgroundBashJob{}, err
		}
		foregroundCount := 0
		for _, entry := range jobs.jobs {
			if entry.foregroundSlot {
				foregroundCount++
			}
		}
		if foregroundCount >= BackgroundBashMaxRunning {
			jobs.mu.Unlock()
			return BackgroundBashJob{}, errors.New("foreground bash capacity reached")
		}
		now := time.Now()
		e = &backgroundBashEntry{cmd: cmd, outputRef: out, foreground: true, foregroundSlot: true, done: make(chan struct{}), waitFinished: make(chan struct{}), launchReady: make(chan struct{})}
		e.job = BackgroundBashJob{ID: id, Owner: backgroundBashOwnerKey(owner), Command: spec.command, Status: "uncertain", StartedAt: now, Error: "foreground launch pending"}
		jobs.jobs[id] = e
		jobs.callbackAddLocked()
		jobs.mu.Unlock()
		if err := cmd.Start(); err != nil {
			jobs.mu.Lock()
			delete(jobs.jobs, id)
			close(e.launchReady)
			close(e.waitFinished)
			close(e.done)
			jobs.mu.Unlock()
			jobs.callbackDone()
			return BackgroundBashJob{}, err
		}
		if gate != nil {
			_ = cmd.ExtraFiles[0].Close()
		}
		// The reserved slot and callback accounting bound a stuck lookup. Never
		// start another lookup until this one completes.
		pendingIdentity = startIdentity(cmd.Process.Pid)
		var marker string
		var err error
		timer := time.NewTimer(time.Second)
		select {
		case result := <-pendingIdentity:
			pendingIdentity = nil
			marker, err = result.marker, result.err
		case <-ctx.Done():
			err = ctx.Err()
		case <-jobs.sweepStop:
			err = errors.New("background bash manager closed")
		case <-timer.C:
			err = errors.New("foreground identity lookup timed out")
		}
		timer.Stop()
		jobs.mu.Lock()
		e.identity = ProcessIdentity{PID: cmd.Process.Pid, StartMarker: marker, Incarnation: id}
		if err == nil && gate != nil {
			jobs.mu.Unlock()
			e.orphanPath, err = releaseTrackedBackgroundBash(gate, owner.claudeOrphanRoot, e.identity)
			jobs.mu.Lock()
		}
		if err == nil {
			e.job.Status = "running"
			e.job.Error = ""
		} else {
			e.job.Error = fmt.Sprintf("foreground identity uncertain: %v", err)
		}
		close(e.launchReady)
		jobs.mu.Unlock()
		return BackgroundBashJob{}, err
	}
	err = nil
	if owner.backgroundBash != nil {
		_, err = owner.backgroundBash.launchContext(ctx, start)
	} else {
		_, err = start()
	}
	if e == nil || cmd.Process == nil {
		return agent.AgentToolResult{}, err
	}
	// Transfer the unreaped process to the manager before returning an error.
	handoff := func(cause error, stop bool) error {
		jobs.mu.Lock()
		e.foreground = false
		e.job.Status = "uncertain"
		e.job.Error = fmt.Sprintf("foreground cleanup uncertain: %v", cause)
		e.job.Deadline = time.Now().Add(BackgroundBashDefaultTimeout)
		jobs.changedAsyncLocked(map[string]struct{}{e.job.Owner: {}})
		if stop {
			jobs.callbackAddLocked()
		}
		// callback ownership was reserved before Start; the manager waiter takes it.
		go func() {
			defer jobs.callbackDone()
			if e.identity.StartMarker == "" {
				for {
					if pendingIdentity == nil {
						pendingIdentity = startIdentity(cmd.Process.Pid)
					}
					select {
					case result := <-pendingIdentity:
						pendingIdentity = nil
						if result.err == nil && result.marker != "" {
							jobs.mu.Lock()
							e.identity.StartMarker = result.marker
							jobs.mu.Unlock()
							jobs.mu.Lock()
							jobs.callbackAddLocked()
							jobs.mu.Unlock()
							go func() { defer jobs.callbackDone(); jobs.timeoutStop(e) }()
							jobs.wait(e, time.Until(e.job.Deadline))
							return
						}
					}
					time.Sleep(100 * time.Millisecond)
				}
			}
			jobs.wait(e, time.Until(e.job.Deadline))
		}()
		jobs.mu.Unlock()
		if stop {
			go func() { defer jobs.callbackDone(); jobs.timeoutStop(e) }()
		}
		return cause
	}
	if err != nil {
		return agent.AgentToolResult{}, handoff(err, false)
	}
	if m.embeddedBashAfterStart != nil {
		m.embeddedBashAfterStart(cmd.Process.Pid)
	}
	cleanup := func(cause error) error {
		jobs.mu.Lock()
		attempt := jobs.signalAttemptLocked(e, "killed")
		jobs.mu.Unlock()
		select {
		case <-attempt.done:
			if attempt.err != nil {
				return handoff(errors.Join(cause, fmt.Errorf("foreground cleanup uncertain: %w", attempt.err)), true)
			}
		case <-time.After(2 * time.Second):
			return handoff(errors.Join(cause, errors.New("foreground cleanup uncertain: signal pending")), true)
		}
		// Manager owns the waiter from here, including retries on failed verification.
		return handoff(cause, true)
	}
	timer := time.NewTimer(spec.timeout)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var info unix.Siginfo
		err := unix.Waitid(unix.P_PID, cmd.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT|unix.WNOHANG, nil)
		if err != nil && err != unix.EINTR {
			return agent.AgentToolResult{}, cleanup(fmt.Errorf("bash outcome unknown: %w", err))
		}
		if info.Signo != 0 {
			e.reapMu.Lock()
			var removalErr error
			err = waitClaudeCodeCommandConfirmed(cmd, e.identity, func() {
				if e.orphanPath != "" {
					removalErr = jobs.removeOrphanRecord(e.orphanPath, e.identity)
				}
			})
			if removalErr != nil {
				err = errors.Join(errClaudeCodeGroupUnconfirmed, err, removalErr)
			}
			if cmd.ProcessState != nil {
				e.reaping = true
			}
			e.reapMu.Unlock()
			if err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) && cmd.ProcessState == nil {
					return agent.AgentToolResult{}, handoff(fmt.Errorf("bash outcome unknown: %w", err), true)
				}
			}
			jobs.mu.Lock()
			stopped := e.stopReason != ""
			if removalErr != nil {
				e.foreground = false
				e.job.Status = "uncertain"
				e.job.Error = err.Error()
				e.cleanupUncertain = true
				e.job.FinishedAt = time.Now()
				jobs.changedAsyncLocked(map[string]struct{}{e.job.Owner: {}})
			} else {
				delete(jobs.jobs, e.job.ID)
			}
			close(e.waitFinished)
			close(e.done)
			if removalErr != nil {
				jobs.startOrphanRemovalRetryLocked(e)
			}
			jobs.mu.Unlock()
			jobs.callbackDone()
			if err != nil {
				var exitErr *exec.ExitError
				if errors.Is(err, errClaudeCodeGroupUnconfirmed) || !errors.As(err, &exitErr) {
					return agent.AgentToolResult{}, fmt.Errorf("bash output incomplete: %w", err)
				}
			}
			if stopped {
				return agent.AgentToolResult{}, errors.New("foreground command stopped by owner or shutdown")
			}
			if err := ctx.Err(); err != nil {
				return agent.AgentToolResult{}, err
			}
			exit := 0
			if cmd.ProcessState != nil {
				exit = cmd.ProcessState.ExitCode()
			}
			text, cut := out.snapshot()
			if cut {
				text += fmt.Sprintf("\n[Output truncated at %d bytes. Narrow your command if full output is needed.]", BackgroundBashMaxOutput)
			}
			return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: fmt.Sprintf("exit=%d\n%s", exit, text)}}}, nil
		}
		select {
		case <-ctx.Done():
			return agent.AgentToolResult{}, cleanup(ctx.Err())
		case <-timer.C:
			if owner.backgroundBash != nil {
				owner.backgroundBash.mu.Lock()
				if owner.backgroundBash.revoked {
					owner.backgroundBash.mu.Unlock()
					return agent.AgentToolResult{}, cleanup(errBackgroundBashOwnerRetired)
				}
			}
			jobs.mu.Lock()
			if ctx.Err() != nil || jobs.closed || e.stopReason != "" {
				jobs.mu.Unlock()
				if owner.backgroundBash != nil {
					owner.backgroundBash.mu.Unlock()
				}
				cause := ctx.Err()
				if cause == nil {
					cause = errors.New("foreground owner or manager stopped")
				}
				return agent.AgentToolResult{}, cleanup(cause)
			}
			if jobs.backgroundRunningLocked() >= BackgroundBashMaxRunning {
				jobs.mu.Unlock()
				if owner.backgroundBash != nil {
					owner.backgroundBash.mu.Unlock()
				}
				return agent.AgentToolResult{}, cleanup(errors.New("background bash capacity reached during promotion"))
			}
			jobs.changedAsyncLocked(jobs.pruneToLocked(time.Now(), BackgroundBashMaxRecords-1))
			if jobs.backgroundRecordCountLocked() >= BackgroundBashMaxRecords {
				jobs.mu.Unlock()
				if owner.backgroundBash != nil {
					owner.backgroundBash.mu.Unlock()
				}
				return agent.AgentToolResult{}, cleanup(errors.New("background bash record capacity reached during promotion"))
			}
			e.foregroundSlot = false
			e.foreground = false
			e.job.Status = "running"
			e.job.Error = ""
			e.job.Deadline = time.Now().Add(BackgroundBashDefaultTimeout)
			job := jobs.snapshot(e)
			jobs.changedAsyncLocked(map[string]struct{}{e.job.Owner: {}})
			go func() { defer jobs.callbackDone(); jobs.wait(e, time.Until(e.job.Deadline)) }()
			jobs.mu.Unlock()
			if owner.backgroundBash != nil {
				owner.backgroundBash.mu.Unlock()
			}
			payload, _ := json.Marshal(map[string]any{"job_id": job.ID, "status": job.Status})
			return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: string(payload)}}}, nil
		case <-ticker.C:
		}
	}
}

// Legacy helper for tests and non-manager callers. It never reaps an unverified group.
func cleanupForeground(cmd *exec.Cmd) error {
	marker, err := processStartMarker(cmd.Process.Pid)
	if err != nil {
		return fmt.Errorf("foreground cleanup uncertain: %w", err)
	}
	return cleanupForegroundWithIdentity(cmd, ProcessIdentity{PID: cmd.Process.Pid, StartMarker: marker, Incarnation: "foreground"})
}
func cleanupForegroundWithIdentity(cmd *exec.Cmd, identity ProcessIdentity) error {
	if err := (defaultProcessGroupOps{}).SignalVerifiedProcessGroup(identity, syscall.SIGKILL); err != nil {
		return fmt.Errorf("foreground cleanup uncertain: %w", err)
	}
	err := waitClaudeCodeCommandConfirmed(cmd, identity, nil)
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return nil
	}
	return err
}
