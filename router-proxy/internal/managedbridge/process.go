package managedbridge

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	ErrInvalidConfig = errors.New("managed bridge configuration is invalid")
	ErrProcessStart  = errors.New("managed bridge process could not start")
	ErrProcessExit   = errors.New("managed bridge Pi process exited")
	ErrStdoutLimit   = errors.New("managed bridge Pi output exceeded its bound")
	ErrBridgeStarted = errors.New("managed bridge run is already finished or active")
)

type processIdentity struct {
	PID                      int
	StartMarker, Incarnation string
}

func (processIdentity) String() string   { return "managed bridge process identity [redacted]" }
func (processIdentity) GoString() string { return "managed bridge process identity [redacted]" }
func (RuntimeFence) String() string      { return "managed bridge runtime fence [redacted]" }
func (RuntimeFence) GoString() string    { return "managed bridge runtime fence [redacted]" }

type processProbeState uint8

const (
	processOwned processProbeState = iota + 1
	processAbsent
	processPresent
	processMismatch
	processUnknown
)

type childProcess struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	stderr   io.ReadCloser
	identity processIdentity

	waitOnce sync.Once
	waitErr  error
	waitDone chan struct{}

	closeInputOnce   sync.Once
	closeOutputsOnce sync.Once

	stopMu      sync.Mutex
	stopStarted bool
	stopDone    chan struct{}
	stopErr     error
	groupOwned  bool
}

func (p *childProcess) String() string   { return "managed bridge process [redacted]" }
func (p *childProcess) GoString() string { return "managed bridge process [redacted]" }

const (
	processStopInternalGrace = 5 * time.Second
	processPresentDrainGrace = 100 * time.Millisecond
)

func newUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", ErrProcessStart
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

func startChild(ctx context.Context, config Config) (*childProcess, error) {
	if ctx == nil || ctx.Err() != nil || config.validate() != nil {
		return nil, ErrProcessStart
	}
	cmd := exec.Command(config.PiExecutable, config.PiArgs...)
	cmd.Dir = config.PiDir
	if config.PiEnv != nil {
		cmd.Env = append([]string(nil), config.PiEnv...)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, ErrProcessStart
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, ErrProcessStart
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, ErrProcessStart
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, ErrProcessStart
	}
	marker, err := processStartMarker(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, ErrProcessStart
	}
	incarnation, err := newUUID()
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, ErrProcessStart
	}
	identity := processIdentity{PID: cmd.Process.Pid, StartMarker: marker, Incarnation: incarnation}
	group, groupErr := syscall.Getpgid(identity.PID)
	if groupErr != nil || group != identity.PID {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, ErrProcessStart
	}
	return &childProcess{cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr, identity: identity, waitDone: make(chan struct{}), groupOwned: true}, nil
}

func processStartMarker(pid int) (string, error) {
	if pid <= 0 {
		return "", ErrProcessStart
	}
	output, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", ErrProcessStart
	}
	marker := strings.TrimSpace(string(output))
	if marker == "" {
		return "", ErrProcessStart
	}
	// The platform start-time text contains spaces. Hashing it gives the
	// protocol a bounded, opaque marker while retaining PID-reuse identity.
	digest := sha256.Sum256([]byte(marker))
	return hex.EncodeToString(digest[:]), nil
}

func probeProcess(identity processIdentity) processProbeState {
	if identity.PID <= 0 || identity.StartMarker == "" {
		return processUnknown
	}
	if err := syscall.Kill(-identity.PID, 0); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return processAbsent
		}
		if errors.Is(err, syscall.EPERM) {
			if _, groupErr := syscall.Getpgid(identity.PID); errors.Is(groupErr, syscall.ESRCH) {
				return processPresent
			}
		}
		return processUnknown
	}
	marker, err := processStartMarker(identity.PID)
	if err != nil {
		if _, groupErr := syscall.Getpgid(identity.PID); errors.Is(groupErr, syscall.ESRCH) {
			return processPresent
		}
		return processUnknown
	}
	if marker != identity.StartMarker {
		return processMismatch
	}
	group, err := syscall.Getpgid(identity.PID)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return processPresent
		}
		return processUnknown
	}
	if group != identity.PID {
		return processMismatch
	}
	return processOwned
}

func (p *childProcess) wait() error {
	p.waitOnce.Do(func() {
		p.waitErr = p.cmd.Wait()
		close(p.waitDone)
	})
	return p.waitErr
}

func (p *childProcess) startWaiter() <-chan error {
	result := make(chan error, 1)
	go func() {
		result <- p.wait()
		close(result)
	}()
	return result
}

func (p *childProcess) closeInput() {
	p.closeInputOnce.Do(func() { _ = p.stdin.Close() })
}

func (p *childProcess) closeOutputs() {
	p.closeOutputsOnce.Do(func() {
		_ = p.stdout.Close()
		_ = p.stderr.Close()
	})
}

// joinOutputs waits for both readers and closes the descriptors if a
// descendant keeps an inherited pipe open. Closing the local descriptors makes
// the bounded join independent of descendant behavior.
func (p *childProcess) joinOutputs(ctx context.Context, stdoutDone, stderrDone <-chan struct{}) {
	if ctx == nil {
		ctx = context.Background()
	}
	joined := make(chan struct{})
	go func() {
		select {
		case <-stdoutDone:
			select {
			case <-stderrDone:
			case <-ctx.Done():
				p.closeOutputs()
				<-stderrDone
			}
		case <-stderrDone:
			select {
			case <-stdoutDone:
			case <-ctx.Done():
				p.closeOutputs()
				<-stdoutDone
			}
		case <-ctx.Done():
			p.closeOutputs()
			<-stdoutDone
			<-stderrDone
		}
		close(joined)
	}()
	select {
	case <-joined:
	case <-ctx.Done():
		p.closeOutputs()
		<-joined
	}
}

func (p *childProcess) stop(ctx context.Context) error {
	if ctx == nil {
		return newProcessCleanupError("stop context unavailable", processUnknown, context.Canceled)
	}
	p.stopMu.Lock()
	if p.stopStarted {
		done := p.stopDone
		p.stopMu.Unlock()
		select {
		case <-done:
			p.stopMu.Lock()
			err := p.stopErr
			p.stopMu.Unlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	p.stopStarted = true
	p.stopDone = make(chan struct{})
	done := p.stopDone
	p.stopMu.Unlock()
	workCtx, cancel := context.WithTimeout(ctx, processStopInternalGrace)
	defer cancel()
	err := p.stopInternal(workCtx)
	p.stopMu.Lock()
	p.stopErr = err
	close(done)
	p.stopMu.Unlock()
	return err
}

func (p *childProcess) stopInternal(ctx context.Context) error {
	p.closeInput()
	if err := ctx.Err(); err != nil {
		return newProcessCleanupError("stop context ended before probing", processUnknown, err)
	}
	probe := probeProcess(p.identity)
	if probe == processAbsent {
		return p.awaitWait(ctx, p.startWaiter(), probe)
	}
	if probe == processPresent && p.groupOwned {
		// The leader may have exited naturally while descendants retained the
		// process-group pipe. The startup proof authorizes this continuation.
		if absent := waitForAbsent(ctx, p.identity, processPresentDrainGrace); absent {
			return p.awaitWait(ctx, p.startWaiter(), processAbsent)
		}
		if err := killLeaderlessGroup(ctx, p.identity); err != nil {
			return err
		}
		return p.awaitGroupAndWait(ctx, p.startWaiter())
	}
	if probe != processOwned {
		return newProcessCleanupError("process identity is not owned", probe, nil)
	}
	if err := signalOwnedGroup(ctx, p.identity, syscall.SIGTERM); err != nil {
		return err
	}
	waiter := p.startWaiter()
	graceCtx, cancel := context.WithTimeout(ctx, processGraceDuration(ctx))
	probe = waitForGroup(graceCtx, p.identity, waiter)
	cancel()
	if probe == processAbsent {
		return p.awaitWait(ctx, waiter, probe)
	}
	if err := ctx.Err(); err != nil {
		return newProcessCleanupError("stop context ended before escalation", probe, err)
	}
	switch probe {
	case processOwned:
		if err := signalOwnedGroup(ctx, p.identity, syscall.SIGKILL); err != nil {
			return err
		}
	case processPresent:
		if err := killLeaderlessGroup(ctx, p.identity); err != nil {
			return err
		}
	default:
		return newProcessCleanupError("process identity is not owned for escalation", probe, nil)
	}
	probe = waitForGroup(ctx, p.identity, waiter)
	if probe != processAbsent {
		return newProcessCleanupError("managed process group did not stop", probe, ctx.Err())
	}
	return p.awaitWait(ctx, waiter, probe)
}

func (p *childProcess) awaitGroupAndWait(ctx context.Context, waiter <-chan error) error {
	for {
		if probeProcess(p.identity) == processAbsent {
			return p.awaitWait(ctx, waiter, processAbsent)
		}
		select {
		case <-ctx.Done():
			return newProcessCleanupError("stop context ended while waiting for process group", processPresent, ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (p *childProcess) awaitWait(ctx context.Context, waiter <-chan error, state processProbeState) error {
	select {
	case <-waiter:
		return nil
	case <-ctx.Done():
		return newProcessCleanupError("stop context ended while reaping", state, ctx.Err())
	}
}

func processGraceDuration(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0
		}
		return remaining / 2
	}
	return processStopInternalGrace
}

func waitForAbsent(ctx context.Context, identity processIdentity, duration time.Duration) bool {
	grace, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	for {
		if probeProcess(identity) == processAbsent {
			return true
		}
		select {
		case <-grace.Done():
			return false
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func waitForGroup(ctx context.Context, identity processIdentity, leaderDone <-chan error) processProbeState {
	for {
		probe := probeProcess(identity)
		if probe == processAbsent {
			return probe
		}
		if ctx.Err() != nil {
			return probe
		}
		select {
		case <-leaderDone:
			leaderDone = nil
		case <-ctx.Done():
			return probe
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func signalOwnedGroup(ctx context.Context, identity processIdentity, signal syscall.Signal) error {
	if err := ctx.Err(); err != nil {
		return newProcessCleanupError("stop context ended before signaling", processUnknown, err)
	}
	probe := probeProcess(identity)
	if probe == processAbsent {
		return nil
	}
	if probe != processOwned && probe != processPresent {
		return newProcessCleanupError("process identity is not owned for signal", probe, nil)
	}
	for {
		err := syscall.Kill(-identity.PID, signal)
		if err == nil || errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if !errors.Is(err, syscall.EPERM) {
			return newProcessCleanupError("process group signal failed", probe, nil)
		}
		select {
		case <-ctx.Done():
			return newProcessCleanupError("process group signal failed", probe, ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func killLeaderlessGroup(ctx context.Context, identity processIdentity) error {
	if err := ctx.Err(); err != nil {
		return newProcessCleanupError("stop context ended before leaderless escalation", processPresent, err)
	}
	if probe := probeProcess(identity); probe != processPresent {
		if probe == processAbsent {
			return nil
		}
		return newProcessCleanupError("leaderless process group is not verified", probe, nil)
	}
	for {
		err := syscall.Kill(-identity.PID, syscall.SIGKILL)
		if err == nil || errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if !errors.Is(err, syscall.EPERM) {
			return newProcessCleanupError("leaderless process group signal failed", processPresent, nil)
		}
		select {
		case <-ctx.Done():
			return newProcessCleanupError("leaderless process group signal failed", processPresent, ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

type processCleanupError struct {
	reason string
	state  processProbeState
	cause  error
}

func newProcessCleanupError(reason string, state processProbeState, cause error) error {
	return &processCleanupError{reason: reason, state: state, cause: cause}
}

func (err *processCleanupError) Error() string {
	if err == nil {
		return "managed bridge process cleanup failed"
	}
	return "managed bridge process cleanup failed: " + err.reason
}

func (err *processCleanupError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

func drainStderr(reader io.Reader, done chan<- struct{}) {
	defer close(done)
	_, _ = io.Copy(io.Discard, reader)
}

func readStdout(reader io.Reader, max int, emit func([]byte) error) error {
	scanner := bufio.NewScanner(reader)
	initial := 256
	if max+1 < initial {
		initial = max + 1
	}
	scanner.Buffer(make([]byte, initial), max+1)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if err := emit(line); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return ErrStdoutLimit
	}
	return nil
}
