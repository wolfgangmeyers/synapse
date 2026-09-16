package managedbridge

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	ErrInvalidConfig      = errors.New("managed bridge configuration is invalid")
	ErrReservedStatusPath = errors.New("managed bridge status path uses a reserved basename")
	ErrProcessStart       = errors.New("managed bridge process could not start")
	ErrProcessExit        = errors.New("managed bridge Pi process exited")
	ErrStdoutLimit        = errors.New("managed bridge Pi output exceeded its bound")
	ErrBridgeStarted      = errors.New("managed bridge run is already finished or active")
)

// ProcessIdentity binds a live process across PID reuse.
type ProcessIdentity struct {
	PID         int    `json:"pid"`
	StartMarker string `json:"start_marker"`
	Incarnation string `json:"incarnation"`
}

// processIdentity remains the internal spelling used by the Phase 1 code.
type processIdentity = ProcessIdentity

func (ProcessIdentity) String() string   { return "managed bridge process identity [redacted]" }
func (ProcessIdentity) GoString() string { return "managed bridge process identity [redacted]" }
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

type processOSOps struct {
	kill        func(int, syscall.Signal) error
	getpgid     func(int) (int, error)
	startMarker func(int) (string, error)
	listGroup   func(int) ([]int, error)
}

var nativeProcessOSOps = processOSOps{
	kill:        syscall.Kill,
	getpgid:     syscall.Getpgid,
	startMarker: processStartMarker,
	listGroup:   listProcessGroup,
}

func normalizeProcessOSOps(ops processOSOps) processOSOps {
	if ops.kill == nil {
		ops.kill = nativeProcessOSOps.kill
	}
	if ops.getpgid == nil {
		ops.getpgid = nativeProcessOSOps.getpgid
	}
	if ops.startMarker == nil {
		ops.startMarker = nativeProcessOSOps.startMarker
	}
	if ops.listGroup == nil {
		ops.listGroup = nativeProcessOSOps.listGroup
	}
	return ops
}

type childProcess struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	stderr   io.ReadCloser
	identity processIdentity
	ops      processOSOps

	waitOnce sync.Once
	waitErr  error
	waitDone chan struct{}

	closeInputOnce   sync.Once
	closeOutputsOnce sync.Once

	stopMu         sync.Mutex
	stopStarted    bool
	stopDone       chan struct{}
	stopErr        error
	groupOwned     bool
	processGroupID int
	managedGroup   bool
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
	return startChildWithOps(ctx, config, nativeProcessOSOps)
}

func startChildWithOps(ctx context.Context, config Config, rawOps processOSOps) (*childProcess, error) {
	if ctx == nil || ctx.Err() != nil || config.validate() != nil {
		return nil, ErrProcessStart
	}
	cmd := exec.Command(config.PiExecutable, config.PiArgs...)
	cmd.Dir = config.PiDir
	if config.PiEnv != nil {
		cmd.Env = append([]string(nil), config.PiEnv...)
	}
	managedGroup := config.ManagedProcessGroup
	groupID := 0
	ops := normalizeProcessOSOps(rawOps)
	if managedGroup {
		// The launcher owns process-group setup. The library only verifies that
		// the bridge is already the group leader and lets Pi inherit that group.
		groupID = os.Getpid()
		if group, groupErr := ops.getpgid(groupID); groupErr != nil || group != groupID {
			return nil, ErrProcessStart
		}
	} else {
		// Preserve standalone Phase 1 ownership: Pi starts its own group.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
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
	marker, err := ops.startMarker(cmd.Process.Pid)
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
	group, groupErr := ops.getpgid(identity.PID)
	wantGroup := identity.PID
	if managedGroup {
		wantGroup = groupID
	}
	if groupErr != nil || group != wantGroup {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, ErrProcessStart
	}
	return &childProcess{cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr, identity: identity, ops: ops, waitDone: make(chan struct{}), groupOwned: true, processGroupID: int(wantGroup), managedGroup: managedGroup}, nil
}

func captureProcessIdentity() (ProcessIdentity, error) {
	pid := os.Getpid()
	marker, err := processStartMarker(pid)
	if err != nil {
		return ProcessIdentity{}, ErrProcessStart
	}
	incarnation, err := newUUID()
	if err != nil {
		return ProcessIdentity{}, ErrProcessStart
	}
	return ProcessIdentity{PID: pid, StartMarker: marker, Incarnation: incarnation}, nil
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

// probeProcess proves a standalone owner: the recorded process is also the
// leader of its own process group.
func probeProcess(identity processIdentity) processProbeState {
	return probeProcessWithOps(identity, identity.PID, true, nativeProcessOSOps)
}

// probeProcessInGroup retains the existing group-aware probe for callers that
// need to inspect a group without changing the standalone owner semantics.
func probeProcessInGroup(identity processIdentity, expectedGroup int) processProbeState {
	return probeProcessWithOps(identity, expectedGroup, false, nativeProcessOSOps)
}

// probeManagedMember proves a managed Pi member. The outer bridge must supply
// its already-verified group ID; the member itself must match its PID marker,
// remain live, and belong to that group. A member is not required to lead it.
func probeManagedMember(identity processIdentity, expectedOuterGroup int) processProbeState {
	if expectedOuterGroup <= 0 {
		return processUnknown
	}
	return probeProcessWithOps(identity, expectedOuterGroup, false, nativeProcessOSOps)
}

func probeProcessWithOps(identity processIdentity, expectedGroup int, requireLeader bool, ops processOSOps) processProbeState {
	if identity.PID <= 0 || identity.StartMarker == "" {
		return processUnknown
	}
	if err := ops.kill(identity.PID, 0); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return processAbsent
		}
		if errors.Is(err, syscall.EPERM) {
			if _, groupErr := ops.getpgid(identity.PID); errors.Is(groupErr, syscall.ESRCH) {
				return processPresent
			}
		}
		return processUnknown
	}
	marker, err := ops.startMarker(identity.PID)
	if err != nil {
		if _, groupErr := ops.getpgid(identity.PID); errors.Is(groupErr, syscall.ESRCH) {
			return processPresent
		}
		return processUnknown
	}
	if marker != identity.StartMarker {
		return processMismatch
	}
	group, err := ops.getpgid(identity.PID)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return processPresent
		}
		return processUnknown
	}
	if expectedGroup > 0 && group != expectedGroup {
		return processMismatch
	}
	if requireLeader && group != identity.PID {
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

func (p *childProcess) processOps() processOSOps {
	return normalizeProcessOSOps(p.ops)
}

func (p *childProcess) probe() processProbeState {
	return probeProcessWithOps(p.identity, p.processGroupID, !p.managedGroup, p.processOps())
}

type processGroupProbeState uint8

const (
	processGroupOwned processGroupProbeState = iota + 1
	processGroupAbsent
	processGroupUnknown
)

type processGroupProbe struct {
	state   processGroupProbeState
	members []int
	err     error
}

func (p *childProcess) inspectGroup() processGroupProbe {
	ops := p.processOps()
	if p.processGroupID <= 0 {
		return processGroupProbe{state: processGroupUnknown, err: errors.New("invalid process group")}
	}
	if err := ops.kill(-p.processGroupID, 0); err != nil && !errors.Is(err, syscall.EPERM) {
		if errors.Is(err, syscall.ESRCH) {
			return processGroupProbe{state: processGroupAbsent}
		}
		return processGroupProbe{state: processGroupUnknown, err: err}
	}
	members, err := ops.listGroup(p.processGroupID)
	if err != nil {
		return processGroupProbe{state: processGroupUnknown, err: err}
	}
	return processGroupProbe{state: processGroupOwned, members: members}
}

func (p *childProcess) stopInternal(ctx context.Context) error {
	p.closeInput()
	if err := ctx.Err(); err != nil {
		return newProcessCleanupError("stop context ended before probing", processUnknown, err)
	}
	waiter := p.startWaiter()
	probe := p.probe()
	if probe == processMismatch || probe == processUnknown {
		return newProcessCleanupError("process identity is not owned", probe, nil)
	}
	if probe == processAbsent || (!p.managedGroup && probe == processPresent) {
		return p.cleanupGroup(ctx, waiter, syscall.SIGKILL)
	}
	if p.managedGroup {
		if err := signalOwnedProcessWithOps(ctx, p.identity, p.processGroupID, syscall.SIGTERM, p.processOps()); err != nil {
			return err
		}
		return p.finishManagedStop(ctx, waiter)
	}
	if err := signalOwnedGroupWithOps(ctx, p.identity, syscall.SIGTERM, p.processOps()); err != nil {
		return err
	}
	if !waitForProcessExit(ctx, p.waitDone, processGraceDuration(ctx)) {
		if err := ctx.Err(); err != nil {
			return newProcessCleanupError("stop context ended before escalation", processPresent, err)
		}
	} else {
		return p.cleanupGroup(ctx, waiter, syscall.SIGKILL)
	}
	if err := signalOwnedGroupWithOps(ctx, p.identity, syscall.SIGKILL, p.processOps()); err != nil {
		return err
	}
	return p.cleanupGroup(ctx, waiter, syscall.SIGKILL)
}

func (p *childProcess) finishManagedStop(ctx context.Context, waiter <-chan error) error {
	if waitForProcessExit(ctx, p.waitDone, processGraceDuration(ctx)) {
		// Pi can disappear while a descendant retains the dedicated group's
		// pipes. The group must still be drained and proven empty.
		return p.cleanupGroup(ctx, waiter, syscall.SIGKILL)
	}
	if err := ctx.Err(); err != nil {
		return newProcessCleanupError("stop context ended before escalation", processPresent, err)
	}
	if err := signalOwnedProcessWithOps(ctx, p.identity, p.processGroupID, syscall.SIGKILL, p.processOps()); err != nil {
		return err
	}
	if err := p.awaitWait(ctx, waiter, processAbsent); err != nil {
		return err
	}
	return p.cleanupGroup(ctx, waiter, syscall.SIGKILL)
}

func waitForProcessExit(ctx context.Context, done <-chan struct{}, grace time.Duration) bool {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	case <-timer.C:
		return false
	}
}

func (p *childProcess) cleanupGroup(ctx context.Context, waiter <-chan error, signal syscall.Signal) error {
	group := p.inspectGroup()
	if group.state == processGroupUnknown {
		return newProcessCleanupError("process group inspection failed", processUnknown, group.err)
	}
	if group.state == processGroupOwned && len(group.members) != 0 {
		if p.managedGroup {
			if err := signalGroupMembers(ctx, p.processGroupID, os.Getpid(), signal, p.processOps(), group.members); err != nil {
				return err
			}
		} else if err := p.processOps().kill(-p.processGroupID, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
			return newProcessCleanupError("process group signal failed", processPresent, err)
		}
		group = p.inspectGroup()
		if group.state == processGroupUnknown {
			return newProcessCleanupError("process group verification failed", processUnknown, group.err)
		}
		if group.state == processGroupOwned && len(group.members) != 0 {
			if p.managedGroup {
				if err := signalGroupMembers(ctx, p.processGroupID, os.Getpid(), syscall.SIGKILL, p.processOps(), group.members); err != nil {
					return err
				}
			} else if err := p.processOps().kill(-p.processGroupID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return newProcessCleanupError("process group escalation failed", processPresent, err)
			}
			group = p.inspectGroup()
			if group.state == processGroupUnknown {
				return newProcessCleanupError("process group verification failed", processUnknown, group.err)
			}
		}
	}
	if group.state == processGroupOwned && len(group.members) != 0 {
		return newProcessCleanupError("process group is not empty", processPresent, nil)
	}
	return p.awaitWait(ctx, waiter, processAbsent)
}

func signalGroupMembers(ctx context.Context, groupID, keepPID int, signal syscall.Signal, ops processOSOps, members []int) error {
	for _, pid := range members {
		if pid <= 0 || pid == keepPID {
			continue
		}
		if err := ctx.Err(); err != nil {
			return newProcessCleanupError("stop context ended while signaling group", processPresent, err)
		}
		if err := ops.kill(pid, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
			return newProcessCleanupError("process group member signal failed", processPresent, err)
		}
	}
	return nil
}

func managedGroupMembers(groupID, keepPID int) []int {
	if groupID <= 0 {
		return nil
	}
	if err := syscall.Kill(-groupID, 0); errors.Is(err, syscall.ESRCH) {
		return nil
	}
	members, err := listProcessGroup(groupID)
	if err != nil {
		return []int{-1}
	}
	filtered := make([]int, 0, len(members))
	for _, pid := range members {
		if pid != keepPID {
			filtered = append(filtered, pid)
		}
	}
	return filtered
}

func listProcessGroup(groupID int) ([]int, error) {
	cmd := exec.Command("ps", "-axo", "pid=,pgid=")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	members := make([]int, 0)
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		fields := strings.Fields(string(line))
		if len(fields) != 2 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		pgid, pgidErr := strconv.Atoi(fields[1])
		if pidErr == nil && pgidErr == nil && pid > 0 && pgid == groupID && pid != os.Getpid() {
			members = append(members, pid)
		}
	}
	return members, nil
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

func signalOwnedGroupWithOps(ctx context.Context, identity processIdentity, signal syscall.Signal, ops processOSOps) error {
	if err := ctx.Err(); err != nil {
		return newProcessCleanupError("stop context ended before signaling", processUnknown, err)
	}
	probe := probeProcessWithOps(identity, identity.PID, true, ops)
	if probe == processAbsent {
		return nil
	}
	if probe != processOwned {
		return newProcessCleanupError("process identity is not owned for signal", probe, nil)
	}
	if err := ops.kill(-identity.PID, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
		return newProcessCleanupError("process group signal failed", probe, err)
	}
	return nil
}

func signalOwnedProcessWithOps(ctx context.Context, identity processIdentity, expectedGroup int, signal syscall.Signal, ops processOSOps) error {
	if err := ctx.Err(); err != nil {
		return newProcessCleanupError("stop context ended before signaling", processUnknown, err)
	}
	probe := probeProcessWithOps(identity, expectedGroup, false, ops)
	if probe == processAbsent {
		return nil
	}
	if probe != processOwned {
		return newProcessCleanupError("process identity is not owned for signal", probe, nil)
	}
	if err := ops.kill(identity.PID, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
		return newProcessCleanupError("process signal failed", probe, err)
	}
	return nil
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
