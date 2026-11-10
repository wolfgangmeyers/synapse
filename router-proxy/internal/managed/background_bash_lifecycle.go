package managed

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Limits apply across the manager, not per caller. Terminal records are pruned
// on launch, get, list, completion, and periodic sweep; running and uncertain
// records are never pruned by ordinary retention.
const (
	BackgroundBashMaxRunning     = 32
	BackgroundBashMaxRecords     = 256
	BackgroundBashMaxOutput      = 64 * 1024
	BackgroundBashRetention      = time.Hour
	BackgroundBashDefaultTimeout = 600 * time.Second
)

type BackgroundBashLaunch struct {
	Command          string
	Dir              string
	Env              []string
	TimeoutSeconds   int64
	claudeOrphanRoot string
}
type BackgroundBashJob struct {
	ID         string    `json:"id"`
	Owner      string    `json:"owner"`
	Command    string    `json:"command"`
	Status     string    `json:"status"`
	Output     string    `json:"output"`
	Truncated  bool      `json:"truncated"`
	ExitCode   *int      `json:"exitCode,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	Deadline   time.Time `json:"deadline"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
	Error      string    `json:"error,omitempty"`
}
type backgroundBashEntry struct {
	reapMu            sync.Mutex
	foreground        bool // hidden until promotion or uncertainty
	foregroundSlot    bool // separate bounded ownership until promotion or cleanup
	reaping           bool
	signalActive      *backgroundBashSignal // protected by manager mutex
	job               BackgroundBashJob
	cmd               *exec.Cmd
	identity          ProcessIdentity
	orphanPath        string
	done              chan struct{}
	waitFinished      chan struct{}
	launchReady       chan struct{}
	launchError       string
	cleanupUncertain  bool  // terminal failure retained only as long as the job record
	orphanWaitErr     error // sole wait result, independent of orphan-record removal
	initialNoticeDone chan struct{}
	stopReason        string
	output            backgroundBashOutput
	outputRef         *backgroundBashOutput // foreground adoption retains its original writer
}
type backgroundBashSignal struct {
	done chan struct{}
	err  error
}
type backgroundBashOutput struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
	overflow  chan struct{}
}

func (w *backgroundBashOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	remaining := BackgroundBashMaxOutput - len(w.data)
	if remaining < n {
		if !w.truncated && w.overflow != nil {
			close(w.overflow)
		}
		w.truncated = true
	}
	if remaining > n {
		remaining = n
	}
	if remaining > 0 {
		w.data = append(w.data, p[:remaining]...)
	}
	return n, nil
}
func (w *backgroundBashOutput) snapshot() (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.data), w.truncated
}

type BackgroundBashManager struct {
	mu                      sync.Mutex
	jobs                    map[string]*backgroundBashEntry
	notify                  func(BackgroundBashJob)
	change                  func(context.Context) error
	projectionCtx           context.Context
	projectionCancel        context.CancelFunc
	closed                  bool
	callbackCount           int
	callbackIdle            chan struct{}
	projectionDirty         bool
	projectionWorker        bool
	sweepStop               chan struct{}
	sweepDone               chan struct{}
	signal                  func(ProcessIdentity, syscall.Signal) error
	identify                func(int) (string, error)
	startCommand            func(*exec.Cmd) error
	killGroup               func(int, syscall.Signal) error
	removeOrphan            func(string, ProcessIdentity) error
	waitCommand             func(*exec.Cmd, ProcessIdentity, func(func() error) error) error
	waitUnidentifiedCommand func(*exec.Cmd) error
	unidentifiedExited      func(int) (bool, error)
}

func NewBackgroundBashManager(notify func(BackgroundBashJob)) *BackgroundBashManager {
	m := &BackgroundBashManager{jobs: make(map[string]*backgroundBashEntry), notify: notify, sweepStop: make(chan struct{}), sweepDone: make(chan struct{}), callbackIdle: closedBackgroundChannel()}
	m.projectionCtx, m.projectionCancel = context.WithCancel(context.Background())
	go m.sweep()
	return m
}

func closedBackgroundChannel() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

// callbackAddLocked is called while m.mu is held.
func (m *BackgroundBashManager) callbackAddLocked() {
	if m.callbackCount == 0 {
		m.callbackIdle = make(chan struct{})
	}
	m.callbackCount++
}
func (m *BackgroundBashManager) callbackDone() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callbackCount--
	if m.callbackCount == 0 {
		close(m.callbackIdle)
	}
}

// SetChangeCallback installs a global projection invalidation hook.
func (m *BackgroundBashManager) SetChangeCallback(callback func()) {
	if callback == nil {
		m.SetChangeCallbackContext(nil)
		return
	}
	m.SetChangeCallbackContext(func(context.Context) error { callback(); return nil })
}
func (m *BackgroundBashManager) SetChangeCallbackContext(callback func(context.Context) error) {
	m.mu.Lock()
	m.change = callback
	m.mu.Unlock()
}

// Invalidation is admitted under the lock and joined by CloseContext. A change
// during publication causes another pass; no per-owner state is retained.
func (m *BackgroundBashManager) changedAsyncLocked(owners map[string]struct{}) {
	if m.closed || m.change == nil || len(owners) == 0 {
		return
	}
	m.projectionDirty = true
	if m.projectionWorker {
		return
	}
	m.projectionWorker = true
	m.callbackAddLocked()
	go m.drainChanges()
}
func (m *BackgroundBashManager) drainChanges() {
	for {
		m.mu.Lock()
		if !m.projectionDirty {
			m.projectionWorker = false
			m.callbackCount--
			if m.callbackCount == 0 {
				close(m.callbackIdle)
			}
			m.mu.Unlock()
			return
		}
		m.projectionDirty = false
		callback := m.change
		m.mu.Unlock()
		if callback != nil {
			err := callback(m.projectionCtx)
			if err != nil && m.projectionCtx.Err() == nil {
				timer := time.NewTimer(100 * time.Millisecond)
				select {
				case <-timer.C:
				case <-m.projectionCtx.Done():
					timer.Stop()
				}
				m.mu.Lock()
				if !m.closed {
					m.projectionDirty = true
				}
				m.mu.Unlock()
			}
		}
	}
}
func (m *BackgroundBashManager) sweep() {
	defer close(m.sweepDone)
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			m.sweepOnce()
		case <-m.sweepStop:
			return
		}
	}
}
func (m *BackgroundBashManager) sweepOnce() {
	m.mu.Lock()
	owners := m.pruneAsyncLocked(time.Now())
	m.changedAsyncLocked(owners)
	m.mu.Unlock()
}

func (m *BackgroundBashManager) pruneLocked(now time.Time) map[string]struct{} {
	return m.pruneToLocked(now, BackgroundBashMaxRecords)
}
func (m *BackgroundBashManager) pruneAsyncLocked(now time.Time) map[string]struct{} {
	return m.pruneLocked(now)
}
func (m *BackgroundBashManager) pruneToLocked(now time.Time, target int) map[string]struct{} {
	owners := make(map[string]struct{})
	var terminal []*backgroundBashEntry
	for id, e := range m.jobs {
		if !e.foregroundSlot && e.job.Status != "running" && e.job.Status != "uncertain" {
			if now.Sub(e.job.FinishedAt) >= BackgroundBashRetention {
				delete(m.jobs, id)
				owners[e.job.Owner] = struct{}{}
			} else {
				terminal = append(terminal, e)
			}
		}
	}
	sort.Slice(terminal, func(i, j int) bool { return terminal[i].job.FinishedAt.Before(terminal[j].job.FinishedAt) })
	for _, e := range terminal {
		if m.backgroundRecordCountLocked() <= target {
			break
		}
		delete(m.jobs, e.job.ID)
		owners[e.job.Owner] = struct{}{}
	}
	return owners
}

// Counts exclude separately bounded foreground ownership, including cleanup handoffs.
func (m *BackgroundBashManager) backgroundRecordCountLocked() int {
	n := 0
	for _, e := range m.jobs {
		if !e.foregroundSlot {
			n++
		}
	}
	return n
}
func (m *BackgroundBashManager) backgroundRunningLocked() int {
	n := 0
	for _, e := range m.jobs {
		if !e.foregroundSlot && (e.job.Status == "running" || e.job.Status == "uncertain") {
			n++
		}
	}
	return n
}

func (m *BackgroundBashManager) snapshot(e *backgroundBashEntry) BackgroundBashJob {
	j := e.job
	output := &e.output
	if e.outputRef != nil {
		output = e.outputRef
	}
	j.Output, j.Truncated = output.snapshot()
	if j.ExitCode != nil {
		v := *j.ExitCode
		j.ExitCode = &v
	}
	return j
}

// metadataSnapshot avoids copying the potentially large captured output.
// The manager lock protects job fields; output has its own lock.
func (m *BackgroundBashManager) metadataSnapshot(e *backgroundBashEntry) BackgroundBashJob {
	j := e.job
	j.Output = ""
	output := &e.output
	if e.outputRef != nil {
		output = e.outputRef
	}
	output.mu.Lock()
	j.Truncated = output.truncated
	output.mu.Unlock()
	if j.ExitCode != nil {
		v := *j.ExitCode
		j.ExitCode = &v
	}
	return j
}
func (m *BackgroundBashManager) Launch(owner string, r BackgroundBashLaunch) (BackgroundBashJob, error) {
	if err := backgroundBashPlatformSupport(); err != nil {
		return BackgroundBashJob{}, err
	}
	if strings.TrimSpace(owner) == "" || strings.TrimSpace(r.Command) == "" || len(r.Command) > 65536 || r.TimeoutSeconds < 0 || r.TimeoutSeconds > int64((time.Duration(1<<63-1))/time.Second) {
		return BackgroundBashJob{}, errors.New("invalid background bash launch")
	}
	timeout := BackgroundBashDefaultTimeout
	if r.TimeoutSeconds > 0 {
		timeout = time.Duration(r.TimeoutSeconds) * time.Second
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return BackgroundBashJob{}, err
	}
	id := hex.EncodeToString(token[:])
	m.mu.Lock()
	owners := make(map[string]struct{})
	defer func() { m.changedAsyncLocked(owners); m.mu.Unlock() }()
	if m.closed {
		return BackgroundBashJob{}, errors.New("background bash manager closed")
	}
	if m.backgroundRunningLocked() >= BackgroundBashMaxRunning {
		return BackgroundBashJob{}, errors.New("background bash capacity reached")
	}
	m.changedAsyncLocked(m.pruneToLocked(time.Now(), BackgroundBashMaxRecords-1))
	if m.backgroundRecordCountLocked() >= BackgroundBashMaxRecords {
		return BackgroundBashJob{}, errors.New("background bash record capacity reached")
	}
	cmd, gate, err := backgroundBashCommand(r.Command, r.claudeOrphanRoot)
	if err != nil {
		return BackgroundBashJob{}, err
	}
	if gate != nil {
		defer gate.Close()
		defer cmd.ExtraFiles[0].Close()
	}
	cmd.Dir = r.Dir
	cmd.Env = r.Env
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	e := &backgroundBashEntry{cmd: cmd, done: make(chan struct{}), waitFinished: make(chan struct{}), launchReady: make(chan struct{})}
	cmd.Stdout = &e.output
	cmd.Stderr = &e.output
	now := time.Now()
	e.job = BackgroundBashJob{ID: id, Owner: owner, Command: r.Command, Status: "uncertain", StartedAt: now, Deadline: now.Add(timeout), Error: "background bash launch pending"}
	m.jobs[id] = e
	m.callbackAddLocked()
	identify := m.identify
	kill := m.killGroup
	start := m.startCommand
	if start == nil {
		start = (*exec.Cmd).Start
	}
	m.mu.Unlock()
	err = start(cmd)
	if err != nil {
		m.mu.Lock()
		delete(m.jobs, id)
		e.job.Status = "failed"
		close(e.launchReady)
		close(e.done)
		m.mu.Unlock()
		m.callbackDone()
		m.mu.Lock()
		return BackgroundBashJob{}, fmt.Errorf("start background bash: %w", err)
	}
	if gate != nil {
		_ = cmd.ExtraFiles[0].Close()
	}
	// The launch-time watchdog may signal only while the child is unreaped.
	// A Start implementation must publish its process by returning before it can
	// be signaled safely; cmd.Process cannot be read concurrently with Start.
	if kill == nil {
		kill = syscall.Kill
	}
	launchSignaling := true // guarded by reapMu
	launchSignalAttempts := 0
	launchSignalSucceeded := false // guarded by reapMu
	launchSignal := func() bool {
		launchSignalAttempts++
		err := kill(-cmd.Process.Pid, syscall.SIGKILL)
		m.mu.Lock()
		if err == nil {
			launchSignalSucceeded = true
			e.stopReason = "timed_out"
		} else {
			e.job.Error = fmt.Sprintf("background bash cleanup uncertain: %v", err)
		}
		m.mu.Unlock()
		return err == nil
	}
	var launchTimer *time.Timer
	e.reapMu.Lock() // Publish the timer before an already-expired deadline can fire.
	launchTimer = time.AfterFunc(time.Until(e.job.Deadline), func() {
		e.reapMu.Lock()
		defer e.reapMu.Unlock()
		if launchSignaling && launchSignalAttempts < 3 && !launchSignal() && launchSignalAttempts < 3 {
			launchTimer.Reset(100 * time.Millisecond)
		}
	})
	e.reapMu.Unlock()
	if identify == nil {
		identify = processStartMarker
	}
	marker, err := identify(cmd.Process.Pid)
	e.reapMu.Lock()
	launchTimer.Stop()
	if launchSignalAttempts == 0 && !time.Now().Before(e.job.Deadline) {
		launchSignal()
	}
	launchSignaling = false
	e.reapMu.Unlock()
	m.mu.Lock()
	if err != nil { // The unreaped child still reserves its PID; kill only this owned group.
		if kill == nil {
			kill = syscall.Kill
		}
		launchErr := fmt.Errorf("identify background bash process: %w", err)
		e.launchError = launchErr.Error()
		e.job.Status = "uncertain"
		e.job.Error = launchErr.Error()
		m.jobs[id] = e
		owners[owner] = struct{}{}
		close(e.launchReady)
		// The child remains unreaped until this signal completes, reserving its PID.
		// Publish the entry first so concurrent shutdown can observe it.
		m.mu.Unlock()
		var killErr error
		if !launchSignalSucceeded && launchSignalAttempts < 3 {
			launchSignalAttempts++
			killErr = kill(-cmd.Process.Pid, syscall.SIGKILL)
		} else if !launchSignalSucceeded {
			killErr = errors.New("group signal attempt limit reached")
		}
		m.mu.Lock()
		if killErr != nil {
			launchErr = errors.Join(launchErr, fmt.Errorf("background bash cleanup uncertain: %w", killErr))
		} else {
			launchErr = errors.Join(launchErr, errors.New("background bash cleanup uncertain: group termination unconfirmed"))
		}
		e.job.Error = launchErr.Error()
		go func() {
			defer m.callbackDone()
			m.waitUnidentified(e, time.Until(e.job.Deadline), kill, killErr == nil, launchSignalAttempts)
		}()
		return BackgroundBashJob{}, launchErr
	}
	e.identity = ProcessIdentity{PID: cmd.Process.Pid, StartMarker: marker, Incarnation: id}
	if gate != nil {
		m.mu.Unlock()
		e.orphanPath, err = releaseTrackedBackgroundBash(gate, r.claudeOrphanRoot, e.identity)
		m.mu.Lock()
		if err != nil {
			e.job.Error = err.Error()
			e.launchError = err.Error()
			close(e.launchReady)
			go func() { defer m.callbackDone(); m.wait(e, time.Until(e.job.Deadline)) }()
			return BackgroundBashJob{}, err
		}
	}
	e.job.Status = "running"
	if e.stopReason == "" {
		e.job.Error = ""
	}
	m.jobs[id] = e
	owners[owner] = struct{}{}
	close(e.launchReady)
	go func() { defer m.callbackDone(); m.wait(e, time.Until(e.job.Deadline)) }()
	return m.snapshot(e), nil
}
func (m *BackgroundBashManager) waitUnidentified(e *backgroundBashEntry, timeout time.Duration, kill func(int, syscall.Signal) error, initialSignalSucceeded bool, attempts int) {
	confirmed := waitUnidentifiedBackgroundWithWait(e.cmd, timeout, kill, initialSignalSucceeded, attempts, m.waitUnidentifiedCommand, m.unidentifiedExited)
	close(e.waitFinished)
	m.mu.Lock()
	// A failed launch is terminal even if group cleanup was not confirmed.
	// Preserve the uncertainty in Error without retaining capacity forever.
	e.job.Status = "failed"
	if !confirmed {
		e.cleanupUncertain = true
		e.job.Error = errors.Join(errors.New(e.launchError), errors.New("background bash cleanup uncertain: process group termination unconfirmed")).Error()
	} else {
		e.job.Error = e.launchError
	}
	// The sole waiter has completed; an unidentified exit remains unknown.
	e.job.FinishedAt = time.Now()
	j := m.snapshot(e)
	close(e.done)
	m.changedAsyncLocked(m.pruneAsyncLocked(time.Now()))
	owners := map[string]struct{}{j.Owner: {}}
	m.changedAsyncLocked(owners)
	m.mu.Unlock()
	m.mu.Lock()
	notify := m.notify
	m.mu.Unlock()
	if notify != nil {
		notify(j)
	}
}
func (m *BackgroundBashManager) removeOrphanRecord(path string, id ProcessIdentity) error {
	if m.removeOrphan != nil {
		return m.removeOrphan(path, id)
	}
	return removeClaudeCodeOrphan(path, id)
}

// Caller holds m.mu; retry only the durable record, never the reaped process.
func (m *BackgroundBashManager) startOrphanRemovalRetryLocked(e *backgroundBashEntry) {
	if m.closed {
		return
	}
	m.callbackAddLocked()
	go func() {
		defer m.callbackDone()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-m.sweepStop:
				return
			case <-ticker.C:
			}
			err := m.removeOrphanRecord(e.orphanPath, e.identity)
			if err != nil {
				continue
			}
			m.mu.Lock()
			e.cleanupUncertain = false
			e.job.Error = ""
			if e.foregroundSlot {
				delete(m.jobs, e.job.ID)
			} else {
				e.job.Status = "failed"
				if e.orphanWaitErr == nil {
					e.job.Status = "succeeded"
				} else {
					var exitErr *exec.ExitError
					if !errors.As(e.orphanWaitErr, &exitErr) {
						e.job.Error = e.orphanWaitErr.Error()
					}
				}
				if e.stopReason != "" {
					e.job.Status = e.stopReason
				}
			}
			m.changedAsyncLocked(map[string]struct{}{e.job.Owner: {}})
			j := m.snapshot(e)
			notify := m.notify
			foreground := e.foregroundSlot
			initialNoticeDone := e.initialNoticeDone
			m.mu.Unlock()
			if !foreground && notify != nil {
				if initialNoticeDone != nil {
					<-initialNoticeDone
				}
				notify(j)
			}
			return
		}
	}()
}
func (m *BackgroundBashManager) wait(e *backgroundBashEntry, timeout time.Duration) {
	timer := time.AfterFunc(timeout, func() {
		m.timeoutStop(e)
	})
	var removalErr error
	waitCommand := m.waitCommand
	if waitCommand == nil {
		waitCommand = func(cmd *exec.Cmd, id ProcessIdentity, reap func(func() error) error) error {
			err := waitClaudeCodeCommandWithReapHook(cmd, id, func() {
				if e.orphanPath != "" {
					removalErr = m.removeOrphanRecord(e.orphanPath, id)
				}
			}, reap)
			if removalErr != nil {
				e.orphanWaitErr = err
				return errors.Join(errClaudeCodeGroupUnconfirmed, err, removalErr)
			}
			return err
		}
	}
	reap := func(wait func() error) error {
		e.reapMu.Lock()
		e.reaping = true
		e.reapMu.Unlock()
		return wait()
	}
	var err error
	publishedUncertainty := false
	for {
		err = waitCommand(e.cmd, e.identity, reap)
		if err == nil || e.cmd.ProcessState != nil {
			break
		}
		m.mu.Lock()
		e.job.Status = "uncertain"
		e.job.Error = err.Error()
		if !publishedUncertainty {
			publishedUncertainty = true
			m.changedAsyncLocked(map[string]struct{}{e.job.Owner: {}})
		}
		m.mu.Unlock()
		time.Sleep(100 * time.Millisecond)
	}
	close(e.waitFinished) // Publish reaping before contending for the manager lock.
	timer.Stop()
	m.mu.Lock()
	status := "succeeded"
	var code *int
	if e.cmd.ProcessState != nil {
		v := e.cmd.ProcessState.ExitCode()
		if v >= 0 {
			code = &v
		}
	}
	if err != nil {
		status = "failed"
	}
	// A reaped leader plus confirmed group absence resolves transient errors from
	// either the waiter, an explicit stop, or timeout signaling.
	if e.cmd.ProcessState != nil && !errors.Is(err, errClaudeCodeGroupUnconfirmed) {
		e.job.Error = ""
	}
	if e.stopReason != "" {
		status = e.stopReason
	} else if e.job.Error != "" {
		status = "uncertain"
	}
	if errors.Is(err, errClaudeCodeGroupUnconfirmed) || (err != nil && e.cmd.ProcessState == nil) {
		status = "uncertain"
		e.job.Error = err.Error()
	}
	e.job.Status = status
	e.job.ExitCode = code
	e.job.FinishedAt = time.Now()
	j := m.snapshot(e)
	if e.foregroundSlot && status != "uncertain" {
		delete(m.jobs, e.job.ID)
	}
	close(e.done)
	if removalErr != nil && e.cmd.ProcessState != nil {
		e.cleanupUncertain = true
	}
	m.changedAsyncLocked(m.pruneAsyncLocked(time.Now()))
	owners := map[string]struct{}{e.job.Owner: {}}
	m.changedAsyncLocked(owners)
	foreground := e.foregroundSlot
	notify := m.notify
	if removalErr != nil && e.cmd.ProcessState != nil {
		if !foreground && notify != nil {
			e.initialNoticeDone = make(chan struct{})
		}
		m.startOrphanRemovalRetryLocked(e)
	}
	m.mu.Unlock()
	if !foreground && notify != nil {
		notify(j)
		if e.initialNoticeDone != nil {
			close(e.initialNoticeDone)
		}
	}
}

// signalAttemptLocked shares one in-flight signal per job. The reaping fence
// stays held until the result is published, so a successful signal cannot race
// the waiter publishing a terminal state without its stop reason.
func (m *BackgroundBashManager) signalAttemptLocked(e *backgroundBashEntry, reason string) *backgroundBashSignal {
	if e.signalActive != nil {
		return e.signalActive
	}
	attempt := &backgroundBashSignal{done: make(chan struct{})}
	e.signalActive = attempt
	signal := m.signal
	if signal == nil {
		signal = DefaultProcessGroupOps().SignalVerifiedProcessGroup
	}
	go func() {
		e.reapMu.Lock()
		var err error
		if e.reaping {
			err = errors.New("leader reap in progress")
		} else {
			err = signal(e.identity, syscall.SIGKILL)
		}
		m.mu.Lock()
		attempt.err = err
		if err == nil && e.stopReason == "" {
			e.stopReason = reason
		}
		e.signalActive = nil
		close(attempt.done)
		m.mu.Unlock()
		e.reapMu.Unlock()
	}()
	return attempt
}

// timeoutStop retries failed verified signals until the waiter finishes.
func (m *BackgroundBashManager) timeoutStop(e *backgroundBashEntry) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	published := false
	for {
		m.mu.Lock()
		select {
		case <-e.waitFinished:
			m.mu.Unlock()
			return
		default:
		}
		if e.stopReason != "" || (e.job.Status != "running" && e.job.Status != "uncertain") {
			m.mu.Unlock()
			return
		}
		attempt := m.signalAttemptLocked(e, "timed_out")
		m.mu.Unlock()
		<-attempt.done
		m.mu.Lock()
		if e.stopReason != "" {
			m.mu.Unlock()
			return
		}
		err := attempt.err
		select {
		case <-e.waitFinished:
			m.mu.Unlock()
			return
		default:
		}
		e.job.Status = "uncertain"
		e.job.Error = fmt.Sprintf("background bash cleanup uncertain: %v", err)
		if !published {
			published = true
			m.changedAsyncLocked(map[string]struct{}{e.job.Owner: {}})
		}
		m.mu.Unlock()
		select {
		case <-e.waitFinished:
			return
		case <-ticker.C:
		}
	}
}
func (m *BackgroundBashManager) stop(e *backgroundBashEntry, reason string) error {
	return m.stopContext(context.Background(), e, reason)
}

func (m *BackgroundBashManager) stopContext(ctx context.Context, e *backgroundBashEntry, reason string) error {
	m.mu.Lock()
	if (e.job.Status != "running" && (e.job.Status != "uncertain" || e.identity.StartMarker == "")) || e.stopReason != "" {
		m.mu.Unlock()
		return nil
	}
	attempt := m.signalAttemptLocked(e, reason)
	m.mu.Unlock()
	select {
	case <-attempt.done:
	case <-ctx.Done():
		return fmt.Errorf("background bash cleanup: %w", ctx.Err())
	}
	m.mu.Lock()
	err := attempt.err
	if err == nil || e.stopReason != "" {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	select {
	case <-e.waitFinished:
		return nil
	case <-time.After(3 * time.Second):
	case <-ctx.Done():
		return fmt.Errorf("background bash cleanup: %w", ctx.Err())
	}
	m.mu.Lock()
	select {
	case <-e.waitFinished:
		m.mu.Unlock()
		return nil
	default:
	}
	if e.stopReason != "" || (e.job.Status != "running" && e.job.Status != "uncertain") {
		m.mu.Unlock()
		return nil
	}
	e.job.Status = "uncertain"
	e.job.Error = fmt.Sprintf("background bash cleanup uncertain: %v", err)
	stopErr := errors.New(e.job.Error)
	m.changedAsyncLocked(map[string]struct{}{e.job.Owner: {}})
	m.mu.Unlock()
	return stopErr
}
func (m *BackgroundBashManager) Get(owner, id string) (BackgroundBashJob, error) {
	m.mu.Lock()
	owners := m.pruneAsyncLocked(time.Now())
	defer func() { m.changedAsyncLocked(owners); m.mu.Unlock() }()
	e := m.jobs[id]
	if e == nil || e.job.Owner != owner || backgroundBashExpired(e, time.Now()) {
		return BackgroundBashJob{}, errors.New("background bash job not found")
	}
	return m.snapshot(e), nil
}
func (m *BackgroundBashManager) List(owner string) []BackgroundBashJob {
	m.mu.Lock()
	owners := m.pruneAsyncLocked(time.Now())
	defer func() { m.changedAsyncLocked(owners); m.mu.Unlock() }()
	jobs := make([]BackgroundBashJob, 0)
	now := time.Now()
	for _, e := range m.jobs {
		if e.job.Owner == owner && !e.foreground && !backgroundBashExpired(e, now) {
			jobs = append(jobs, m.metadataSnapshot(e))
		}
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].StartedAt.Before(jobs[j].StartedAt) })
	return jobs
}

func backgroundBashExpired(e *backgroundBashEntry, now time.Time) bool {
	return e.job.Status != "running" && e.job.Status != "uncertain" && now.Sub(e.job.FinishedAt) >= BackgroundBashRetention
}

// projectionJobs never prunes or publishes. UI snapshots can be sampled while
// the live hub holds its subscription lock; a pruning callback would recurse
// into that hub. The sweeper and tool reads perform normal retention pruning.
func (m *BackgroundBashManager) projectionJobs(owner string) []BackgroundBashJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	jobs := make([]BackgroundBashJob, 0)
	for _, entry := range m.jobs {
		if entry.job.Owner == owner && !entry.foreground && (entry.job.Status == "running" || entry.job.Status == "uncertain" || now.Sub(entry.job.FinishedAt) < BackgroundBashRetention) {
			jobs = append(jobs, BackgroundBashJob{ID: entry.job.ID, Status: entry.job.Status, StartedAt: entry.job.StartedAt})
		}
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].StartedAt.Equal(jobs[j].StartedAt) {
			return jobs[i].ID < jobs[j].ID
		}
		return jobs[i].StartedAt.Before(jobs[j].StartedAt)
	})
	return jobs
}
func (m *BackgroundBashManager) Kill(owner, id string) (BackgroundBashJob, error) {
	m.mu.Lock()
	e := m.jobs[id]
	if e == nil || e.job.Owner != owner {
		m.mu.Unlock()
		return BackgroundBashJob{}, errors.New("background bash job not found")
	}
	m.mu.Unlock()
	if err := m.stop(e, "killed"); err != nil {
		return BackgroundBashJob{}, err
	}
	// A successful signal is not proof of termination. Wait for the reaper to
	// establish the outcome, but do not hang indefinitely on an unresponsive process.
	select {
	case <-e.done:
		return m.Get(owner, id)
	case <-time.After(5 * time.Second):
		return BackgroundBashJob{}, errors.New("background bash cleanup uncertain: termination not confirmed")
	}
}
func (m *BackgroundBashManager) StopOwner(owner string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return m.StopOwnerContext(ctx, owner)
}

// StopOwnerContext bounds retirement and starts cleanup for every owned job.
func (m *BackgroundBashManager) StopOwnerContext(ctx context.Context, owner string) error {
	m.mu.Lock()
	var entries []*backgroundBashEntry
	for _, e := range m.jobs {
		if e.job.Owner == owner && (e.job.Status == "running" || e.job.Status == "uncertain" || e.cleanupUncertain) {
			entries = append(entries, e)
		}
	}
	m.mu.Unlock()
	results := make(chan error, len(entries))
	for _, e := range entries {
		go func(e *backgroundBashEntry) {
			if e.launchReady != nil {
				select {
				case <-e.launchReady:
				case <-ctx.Done():
					results <- ctx.Err()
					return
				}
			}
			if err := m.stopContext(ctx, e, "killed"); err != nil {
				results <- err
				return
			}
			select {
			case <-e.done:
				m.mu.Lock()
				var err error
				if e.job.Status == "uncertain" || e.cleanupUncertain {
					err = fmt.Errorf("background bash cleanup uncertain: %s", e.job.Error)
				}
				m.mu.Unlock()
				results <- err
			case <-ctx.Done():
				results <- fmt.Errorf("background bash cleanup: %w", ctx.Err())
			}
		}(e)
	}
	var result error
	for range entries {
		select {
		case err := <-results:
			result = errors.Join(result, err)
		case <-ctx.Done():
			return errors.Join(result, fmt.Errorf("background bash cleanup: %w", ctx.Err()))
		}
	}
	return result
}

// Close stops admission and initiates cleanup. It does not join callbacks, so
// it is safe to call from a callback. Owners that need a barrier use CloseContext.
func (m *BackgroundBashManager) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return m.close(ctx, false)
}

// CloseContext joins admitted work unless ctx expires. A callback must not call
// this barrier on its own manager; use Close from callbacks instead.
func (m *BackgroundBashManager) CloseContext(ctx context.Context) error {
	return m.close(ctx, true)
}

func (m *BackgroundBashManager) close(ctx context.Context, join bool) error {
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		m.projectionCancel()
		close(m.sweepStop)
	}
	var entries []*backgroundBashEntry
	for _, e := range m.jobs {
		if e.job.Status == "running" || e.job.Status == "uncertain" || e.cleanupUncertain {
			entries = append(entries, e)
		}
	}
	m.mu.Unlock()
	var result error
	// Signal all jobs concurrently so one slow cleanup does not consume the
	// budget before the others have even been signaled.
	results := make(chan error, len(entries))
	for _, e := range entries {
		go func(e *backgroundBashEntry) {
			if e.launchReady != nil {
				select {
				case <-e.launchReady:
				case <-ctx.Done():
					results <- ctx.Err()
					return
				}
			}
			if err := m.stopContext(ctx, e, "killed"); err != nil {
				results <- err
				return
			}
			select {
			case <-e.done:
				m.mu.Lock()
				var err error
				if e.job.Status == "uncertain" || e.cleanupUncertain {
					err = fmt.Errorf("background bash cleanup uncertain: %s", e.job.Error)
				}
				m.mu.Unlock()
				results <- err
			case <-ctx.Done():
				results <- fmt.Errorf("background bash cleanup: %w", ctx.Err())
			}
		}(e)
	}
	for range entries {
		select {
		case err := <-results:
			result = errors.Join(result, err)
		case <-ctx.Done():
			return errors.Join(result, fmt.Errorf("background bash cleanup: %w", ctx.Err()))
		}
	}
	select {
	case <-m.sweepDone:
	case <-ctx.Done():
		return errors.Join(result, ctx.Err())
	}
	if join {
		m.mu.Lock()
		idle := m.callbackIdle
		m.mu.Unlock()
		select {
		case <-idle:
		case <-ctx.Done():
			return errors.Join(result, fmt.Errorf("background bash callback cleanup: %w", ctx.Err()))
		}
	}
	return result
}
