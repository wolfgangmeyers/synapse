package managed

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// SessionLaunchMode is a router-owned proof boundary. Pi accepts the same
// argv for an existing or new session ID, so callers must state which durable
// precondition they satisfied before ProcessFactory may start it.
type SessionLaunchMode string

const (
	CreateNew      SessionLaunchMode = "create_new"
	ResumeExisting SessionLaunchMode = "resume_existing"
)

type CommandSpec struct {
	PiExecutable         string
	SessionRoot          string
	SessionPath          string
	SessionID            string
	LaunchMode           SessionLaunchMode
	Cwd                  string
	Provider             string
	ModelID              string
	MechaRPCExtension    string
	PiSubagentsExtension string
	CursorExtension      string
	TestOnlyExtensions   []string
	Environment          []string
}

func (s CommandSpec) validLaunchMode() bool {
	return s.LaunchMode == CreateNew || s.LaunchMode == ResumeExisting
}

func (s CommandSpec) effectiveSessionPath() string {
	if s.SessionPath != "" {
		return s.SessionPath
	}
	return s.SessionID
}

func (s CommandSpec) Args() []string {
	arguments := []string{"--mode", "rpc", "--session-dir", s.SessionRoot, "--session", s.effectiveSessionPath(), "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--offline", "--provider", s.Provider, "--model", s.ModelID, "-e", s.MechaRPCExtension, "-e", s.PiSubagentsExtension}
	if s.CursorExtension != "" {
		arguments = append(arguments, "-e", s.CursorExtension)
	}
	for _, extension := range s.TestOnlyExtensions {
		arguments = append(arguments, "-e", extension)
	}
	return arguments
}

type ProcessIdentity struct {
	PID         int
	StartMarker string
	Incarnation string
}

type Process interface {
	Identity() ProcessIdentity
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Wait() error
	Stop(context.Context) error
}

type ProcessFactory interface {
	Start(context.Context, CommandSpec) (Process, error)
}

// StartNoChildError is the only error contract that proves Start returned
// without creating a child. Lifecycle restart may clear its durable pre-start
// marker only for this case; every other error is fail-closed because a target
// could exist without a durable fence.
type StartNoChildError interface {
	error
	NoChildStarted() bool
}

func StartErrorProvesNoChild(err error) bool {
	var proof StartNoChildError
	return errors.As(err, &proof) && proof.NoChildStarted()
}

type ProcessFactoryFunc func(context.Context, CommandSpec) (Process, error)

func (f ProcessFactoryFunc) Start(ctx context.Context, spec CommandSpec) (Process, error) {
	return f(ctx, spec)
}

type ProcessProbeState string

const (
	ProcessOwned    ProcessProbeState = "owned"
	ProcessAbsent   ProcessProbeState = "absent"
	ProcessMismatch ProcessProbeState = "mismatch"
	ProcessUnknown  ProcessProbeState = "unknown"
	ProcessPresent  ProcessProbeState = "present"
)

type ProcessProbe struct{ State ProcessProbeState }

// ProcessCleanupError reports a controlled-stop failure without reducing an
// unsafe ownership result to a generic error. The cause is retained so callers
// can distinguish deadline and syscall failures with errors.Is.
type ProcessCleanupError struct {
	Reason string
	State  ProcessProbeState
	Cause  error
}

func (err *ProcessCleanupError) Error() string {
	if err == nil {
		return "managed process cleanup failed"
	}
	message := "managed process cleanup failed"
	if err.Reason != "" {
		message += ": " + err.Reason
	}
	if err.State != "" {
		message += " (state " + string(err.State) + ")"
	}
	if err.Cause != nil {
		message += ": " + err.Cause.Error()
	}
	return message
}

func (err *ProcessCleanupError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Cause
}

// ProcessStopError is an expressive alias for callers that name the operation
// instead of the cleanup boundary.
type ProcessStopError = ProcessCleanupError

func newProcessCleanupError(reason string, state ProcessProbeState, cause error) error {
	return &ProcessCleanupError{Reason: reason, State: state, Cause: cause}
}

// ProcessGroupOps is the narrow ownership boundary used by restart execution.
// It never exposes a positive-PID signal fallback.
type ProcessGroupOps interface {
	Probe(ProcessIdentity) ProcessProbe
	CloseProcessInput(Process) error
	SignalVerifiedProcessGroup(ProcessIdentity, syscall.Signal) error
	// SignalVerifiedProcessGroupAfterLeaderExit escalates a group whose leader
	// was already proven owned and terminated during this stop attempt. It may
	// signal only a still-present leaderless group, never a mismatched target.
	SignalVerifiedProcessGroupAfterLeaderExit(ProcessIdentity) error
	WaitForProcessGroupEmpty(context.Context, ProcessIdentity, <-chan error) ProcessProbe
}

type defaultProcessGroupOps struct{}

func DefaultProcessGroupOps() ProcessGroupOps { return defaultProcessGroupOps{} }

type execProcessFactory struct{}

func DefaultProcessFactory() ProcessFactory { return execProcessFactory{} }
func (execProcessFactory) Start(ctx context.Context, spec CommandSpec) (Process, error) {
	if spec.PiExecutable == "" || spec.SessionRoot == "" || spec.effectiveSessionPath() == "" || spec.Cwd == "" || !spec.validLaunchMode() {
		return nil, errors.New("incomplete managed process specification")
	}
	// The supervisor owns controlled termination. CommandContext would kill the
	// leader as soon as the manager context is canceled, racing Stop's process
	// group signal and leaving the durable row on the failed path. The context
	// still gates startup; after Start, Stop is the only termination path.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	command := exec.Command(spec.PiExecutable, spec.Args()...)
	command.Dir = spec.Cwd
	// Legacy recipes may retain obsolete extension environment. Never replay it.
	for _, entry := range spec.Environment {
		key, _, _ := strings.Cut(entry, "=")
		if key == "MECHA_ANTIGRAVITY_EXTENSION" || key == "ROUTER_MANAGED_ANTIGRAVITY_EXTENSION" {
			continue
		}
		command.Env = append(command.Env, entry)
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, err
	}
	var randomIncarnation [16]byte
	if _, err := rand.Read(randomIncarnation[:]); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, err
	}
	incarnation := hex.EncodeToString(randomIncarnation[:])
	marker, markerErr := processStartMarker(command.Process.Pid)
	if markerErr != nil {
		// A child without a real identity cannot enter managed ownership. Kill
		// its group and wait here; failure to prove cleanup makes startup fail.
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		_ = command.Wait()
		if (defaultProcessGroupOps{}).Probe(ProcessIdentity{PID: command.Process.Pid, StartMarker: "unavailable", Incarnation: incarnation}).State != ProcessAbsent {
			return nil, errors.New("managed process start marker is unavailable")
		}
		return nil, errors.New("managed process start marker is unavailable")
	}
	stderrTail := newManagedStderrTail()
	process := &execProcess{cmd: command, stdin: stdin, stdout: stdout, stderr: stderr, stderrTail: stderrTail, identity: ProcessIdentity{PID: command.Process.Pid, StartMarker: marker, Incarnation: incarnation}, waitDone: make(chan struct{})}
	go stderrTail.drain(stderr)
	return process, nil
}

const (
	processStopInternalGrace = 5 * time.Second
	// A leaderless group can disappear shortly after the owning caller reports
	// it as Present. Observe that natural cleanup briefly without taking a
	// signal action or changing the ownership proof.
	processPresentDrainGrace = 100 * time.Millisecond
)

type execProcess struct {
	cmd            *exec.Cmd
	stdin          io.WriteCloser
	stdout, stderr io.ReadCloser
	stderrTail     *managedStderrTail
	identity       ProcessIdentity
	waitOnce       sync.Once
	waitErr        error
	waitDone       chan struct{}
	waitResultOnce sync.Once
	waitResult     chan error
	stopGateOnce   sync.Once
	stopGate       chan struct{}
	stopStateMu    sync.Mutex
	termSent       bool
	processOps     ProcessGroupOps
}

func (p *execProcess) Identity() ProcessIdentity { return p.identity }
func (p *execProcess) Stdin() io.WriteCloser     { return p.stdin }
func (p *execProcess) Stdout() io.ReadCloser     { return p.stdout }
func (p *execProcess) Wait() error {
	p.waitOnce.Do(func() {
		p.waitErr = p.cmd.Wait()
		_ = p.stdout.Close()
		if p.stderrTail != nil {
			<-p.stderrTail.done
			if p.waitErr != nil {
				p.waitErr = &managedProcessExitError{cause: p.waitErr, detail: p.stderrTail.summary()}
			}
		}
		_ = p.stderr.Close()
		close(p.waitDone)
	})
	return p.waitErr
}
func (p *execProcess) Stop(ctx context.Context) error {
	if ctx == nil {
		return newProcessCleanupError("stop requires a context", ProcessUnknown, errors.New("nil context"))
	}
	if p == nil || p.cmd == nil {
		return newProcessCleanupError("process handle is unavailable", ProcessUnknown, errors.New("nil process"))
	}
	if p.cmd.Process == nil {
		return nil
	}
	p.stopGateOnce.Do(func() {
		p.stopGate = make(chan struct{}, 1)
		p.stopGate <- struct{}{}
	})
	select {
	case <-ctx.Done():
		return newProcessCleanupError("stop context ended before cleanup", ProcessUnknown, ctx.Err())
	case <-p.stopGate:
		defer func() { p.stopGate <- struct{}{} }()
	}

	// Bound the complete stop attempt, not just graceful termination. Deriving
	// this from the caller preserves an earlier caller deadline while ensuring
	// Background callers cannot hold the stop gate forever.
	stopCtx, stopCancel := processStopContext(ctx)
	defer stopCancel()

	ops := p.processOps
	if ops == nil {
		ops = DefaultProcessGroupOps()
	}
	if err := stopCtx.Err(); err != nil {
		return newProcessCleanupError("stop context ended before probing", ProcessUnknown, err)
	}
	p.stopStateMu.Lock()
	termSent := p.termSent
	p.stopStateMu.Unlock()
	probe := ops.Probe(p.identity)
	if termSent {
		if probe.State == ProcessAbsent {
			return p.awaitProcessWait(stopCtx, p.startWaiter(), probe.State)
		}
		// A later Stop call cannot inherit the prior call's TERM proof. This
		// keeps a leaderless ProcessPresent result from authorizing a new KILL.
		return newProcessCleanupError("process cleanup requires the original stop attempt", probe.State, nil)
	}
	if err := stopCtx.Err(); err != nil {
		return newProcessCleanupError("stop context ended after probing", probe.State, err)
	}
	if probe.State == ProcessAbsent {
		// A group probe, not waitDone, proves that descendants are gone. Reap
		// the leader through the shared waiter, bounded by the stop context.
		return p.awaitProcessWait(stopCtx, p.startWaiter(), probe.State)
	}
	if probe.State == ProcessPresent {
		// Present on the initial probe is not ownership. The process may still
		// be finishing cleanup, so observe absence briefly without signaling.
		drainCtx, drainCancel := context.WithTimeout(stopCtx, processPresentDrainGrace)
		probe = waitForProcessGroupAbsent(drainCtx, ops, p.identity)
		drainCancel()
		if probe.State == ProcessAbsent {
			return p.awaitProcessWait(stopCtx, p.startWaiter(), probe.State)
		}
		if err := ctx.Err(); err != nil {
			return newProcessCleanupError("stop context ended while observing cleanup", probe.State, err)
		}
	}
	if probe.State != ProcessOwned {
		return newProcessCleanupError("process identity is not owned", probe.State, nil)
	}

	if err := signalProcessGroupAfterOwnedProofWithContext(stopCtx, ops, p.identity); err != nil {
		return err
	}
	p.stopStateMu.Lock()
	p.termSent = true
	p.stopStateMu.Unlock()
	done := p.startWaiter()

	graceCtx, cancel := processGraceContext(stopCtx)
	probe = ops.WaitForProcessGroupEmpty(graceCtx, p.identity, done)
	cancel()
	if probe.State == ProcessAbsent {
		return p.awaitProcessWait(stopCtx, done, probe.State)
	}
	if err := stopCtx.Err(); err != nil {
		return newProcessCleanupError("stop context ended before escalation", probe.State, err)
	}
	// The TERM sent under this invocation's Owned proof authorizes escalation.
	// A leaderless ProcessPresent result cannot be re-proven as Owned, so use
	// the explicit continuation seam. A still-live leader keeps the ordinary
	// Owned signal path used by the existing bounded stop contract.
	switch probe.State {
	case ProcessOwned:
		if err := signalProcessGroupWithContext(stopCtx, ops, p.identity, syscall.SIGKILL); err != nil {
			return err
		}
	case ProcessPresent:
		if err := signalProcessGroupAfterLeaderExitWithContext(stopCtx, ops, p.identity); err != nil {
			return err
		}
	default:
		return newProcessCleanupError("process identity is not owned for escalation", probe.State, nil)
	}
	probe = ops.WaitForProcessGroupEmpty(stopCtx, p.identity, done)
	if probe.State != ProcessAbsent {
		cause := stopCtx.Err()
		if cause == nil {
			cause = errors.New("process group remains present")
		}
		return newProcessCleanupError("managed process group did not stop", probe.State, cause)
	}
	return p.awaitProcessWait(stopCtx, done, probe.State)
}

func processStopContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, processStopInternalGrace)
}

func (p *execProcess) startWaiter() <-chan error {
	p.waitResultOnce.Do(func() {
		p.waitResult = make(chan error, 1)
		go func() {
			err := p.Wait()
			p.waitResult <- err
			close(p.waitResult)
		}()
	})
	return p.waitResult
}

func processGraceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return ctx, func() {}
		}
		return context.WithTimeout(ctx, remaining/2)
	}
	// Background callers still get an escalation bound, while cancellation is
	// always inherited from the caller and the final wait uses that same context.
	return context.WithTimeout(ctx, processStopInternalGrace)
}

func signalProcessGroupAfterLeaderExitWithContext(ctx context.Context, ops ProcessGroupOps, identity ProcessIdentity) error {
	if err := ctx.Err(); err != nil {
		return newProcessCleanupError("stop context ended before leaderless escalation", ProcessPresent, err)
	}
	if signalErr := ops.SignalVerifiedProcessGroupAfterLeaderExit(identity); signalErr != nil {
		// EPERM can race the last descendant's natural exit. The failed signal
		// is not cleanup proof; accept it only after the same stop attempt
		// passively observes the group become absent.
		drainCtx, drainCancel := context.WithTimeout(ctx, processPresentDrainGrace)
		probe := waitForProcessGroupAbsent(drainCtx, ops, identity)
		drainCancel()
		cause := signalErr
		if contextErr := ctx.Err(); contextErr != nil {
			cause = errors.Join(signalErr, contextErr)
		}
		if probe.State == ProcessAbsent {
			return nil
		}
		return newProcessCleanupError("leaderless process group signal failed", probe.State, cause)
	}
	return nil
}

func signalProcessGroupWithContext(ctx context.Context, ops ProcessGroupOps, identity ProcessIdentity, signal syscall.Signal) error {
	if err := ctx.Err(); err != nil {
		return newProcessCleanupError("stop context ended before signaling", ProcessUnknown, err)
	}
	probe := ops.Probe(identity)
	if err := ctx.Err(); err != nil {
		return newProcessCleanupError("stop context ended after signal probe", probe.State, err)
	}
	if probe.State != ProcessOwned {
		return newProcessCleanupError("process identity is not owned for signal", probe.State, nil)
	}
	if err := ops.SignalVerifiedProcessGroup(identity, signal); err != nil {
		return newProcessCleanupError("process group signal failed", probe.State, err)
	}
	return nil
}

// signalProcessGroupAfterOwnedProof keeps the initial Stop-call ownership
// proof alive across the two probes performed by SignalVerifiedProcessGroup.
// A leader can exit after the proof, but only that proof permits a leaderless
// continuation. Present, mismatch, and unknown initial probes never reach this
// helper.
func signalProcessGroupAfterOwnedProofWithContext(ctx context.Context, ops ProcessGroupOps, identity ProcessIdentity) error {
	if err := ctx.Err(); err != nil {
		return newProcessCleanupError("stop context ended before signaling", ProcessUnknown, err)
	}
	probe := ops.Probe(identity)
	if err := ctx.Err(); err != nil {
		return newProcessCleanupError("stop context ended after signal probe", probe.State, err)
	}
	switch probe.State {
	case ProcessAbsent:
		return nil
	case ProcessPresent:
		return signalProcessGroupAfterLeaderExitWithContext(ctx, ops, identity)
	case ProcessMismatch, ProcessUnknown:
		return newProcessCleanupError("process identity is not owned for signal", probe.State, nil)
	case ProcessOwned:
	}

	if err := signalProcessGroupWithContext(ctx, ops, identity, syscall.SIGTERM); err == nil {
		return nil
	} else {
		if contextErr := ctx.Err(); contextErr != nil {
			return newProcessCleanupError("stop context ended after signal", ProcessUnknown, contextErr)
		}
		// SignalVerifiedProcessGroup may have observed the leader after this
		// helper's proof. Re-probe once under the same bounded Stop call so a
		// leaderless group can use the explicit continuation seam.
		probe = ops.Probe(identity)
		if contextErr := ctx.Err(); contextErr != nil {
			return newProcessCleanupError("stop context ended after signal re-probe", probe.State, contextErr)
		}
		switch probe.State {
		case ProcessAbsent:
			return nil
		case ProcessPresent:
			return signalProcessGroupAfterLeaderExitWithContext(ctx, ops, identity)
		default:
			return err
		}
	}
}

func waitForProcessGroupAbsent(ctx context.Context, ops ProcessGroupOps, identity ProcessIdentity) ProcessProbe {
	if ctx == nil {
		return ProcessProbe{State: ProcessUnknown}
	}
	interval := time.NewTicker(10 * time.Millisecond)
	defer interval.Stop()
	for {
		probe := ops.Probe(identity)
		if probe.State == ProcessAbsent {
			return probe
		}
		select {
		case <-ctx.Done():
			return probe
		case <-interval.C:
		}
	}
}

func (p *execProcess) awaitProcessWait(ctx context.Context, done <-chan error, state ProcessProbeState) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return newProcessCleanupError("stop context ended while reaping", state, ctx.Err())
	}
}

func signalOwnedProcessGroup(pid int, signal syscall.Signal) error {
	if pid <= 0 {
		return errors.New("managed process group identity is invalid")
	}
	return syscall.Kill(-pid, signal)
}

type linuxProcessStat struct {
	pid         int
	state       byte
	pgrp        int
	startMarker string
}

func parseLinuxProcessStat(data []byte) (linuxProcessStat, error) {
	text := string(data)
	end := strings.LastIndex(text, ") ")
	if end < 0 {
		return linuxProcessStat{}, errors.New("Linux process stat is malformed")
	}
	open := strings.IndexByte(text, '(')
	if open < 1 || open >= end {
		return linuxProcessStat{}, errors.New("Linux process stat has no command")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(text[:open]))
	if err != nil || pid < 1 {
		return linuxProcessStat{}, errors.New("Linux process stat has an invalid pid")
	}
	fields := strings.Fields(text[end+2:])
	if len(fields) < 3 || len(fields[0]) != 1 {
		return linuxProcessStat{}, errors.New("Linux process stat is incomplete")
	}
	group, err := strconv.Atoi(fields[2])
	if err != nil || group < 0 {
		return linuxProcessStat{}, errors.New("Linux process stat has an invalid group")
	}
	startMarker := ""
	if len(fields) > 19 {
		startMarker = fields[19]
	}
	return linuxProcessStat{pid: pid, state: fields[0][0], pgrp: group, startMarker: startMarker}, nil
}

func linuxProcStat(pid int) (byte, int, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, 0, err
	}
	stat, err := parseLinuxProcessStat(data)
	if err != nil {
		return 0, 0, err
	}
	return stat.state, stat.pgrp, nil
}

// classifyLinuxZombieGroup handles the one state a replacement router cannot
// reap itself: its predecessor's child has been reparented and is a zombie.
// The caller has already verified the exact leader start marker. A zombie-only
// group owns no executable process; any live member keeps the group present.
func classifyLinuxZombieGroup(identity ProcessIdentity) (ProcessProbeState, bool) {
	if runtime.GOOS != "linux" {
		return ProcessUnknown, false
	}
	state, _, err := linuxProcStat(identity.PID)
	if err != nil || state != 'Z' {
		return ProcessUnknown, false
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return ProcessUnknown, true
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		memberState, group, err := linuxProcStat(pid)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			group, groupErr := syscall.Getpgid(pid)
			if errors.Is(groupErr, syscall.ESRCH) || groupErr == nil && group != identity.PID {
				continue
			}
			return ProcessUnknown, true
		}
		if group == identity.PID && memberState != 'Z' && memberState != 'X' {
			return ProcessPresent, true
		}
	}
	return ProcessAbsent, true
}

func (defaultProcessGroupOps) Probe(identity ProcessIdentity) ProcessProbe {
	if ValidateIdentity(identity) != nil {
		return ProcessProbe{State: ProcessUnknown}
	}
	// A negative zero signal observes the group even after its leader has been
	// reaped. That distinction prevents a remaining descendant from authorizing
	// target startup.
	if err := syscall.Kill(-identity.PID, 0); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return ProcessProbe{State: ProcessAbsent}
		}
		if errors.Is(err, syscall.EPERM) {
			// A leaderless group can briefly report EPERM while its last
			// descendant is being reaped. The group exists, but its leader
			// cannot establish ownership anymore.
			if _, groupErr := syscall.Getpgid(identity.PID); errors.Is(groupErr, syscall.ESRCH) {
				return ProcessProbe{State: ProcessPresent}
			}
		}
		return ProcessProbe{State: ProcessUnknown}
	}
	marker, err := processStartMarker(identity.PID)
	if err != nil {
		// ps does not preserve syscall.ESRCH on every supported platform. A
		// direct group-id lookup distinguishes a reaped leader with descendants
		// from an indeterminate marker probe.
		if _, groupErr := syscall.Getpgid(identity.PID); errors.Is(groupErr, syscall.ESRCH) {
			return ProcessProbe{State: ProcessPresent}
		}
		return ProcessProbe{State: ProcessUnknown}
	}
	if marker != identity.StartMarker {
		return ProcessProbe{State: ProcessMismatch}
	}
	if state, classified := classifyLinuxZombieGroup(identity); classified {
		return ProcessProbe{State: state}
	}
	group, err := syscall.Getpgid(identity.PID)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return ProcessProbe{State: ProcessPresent}
		}
		return ProcessProbe{State: ProcessUnknown}
	}
	if group != identity.PID {
		return ProcessProbe{State: ProcessMismatch}
	}
	return ProcessProbe{State: ProcessOwned}
}

func (defaultProcessGroupOps) CloseProcessInput(process Process) error {
	if process == nil || process.Stdin() == nil {
		return errors.New("managed process input is unavailable")
	}
	return process.Stdin().Close()
}

func (ops defaultProcessGroupOps) SignalVerifiedProcessGroup(identity ProcessIdentity, signal syscall.Signal) error {
	if ops.Probe(identity).State != ProcessOwned {
		return errors.New("managed process identity is not owned")
	}
	return signalOwnedProcessGroup(identity.PID, signal)
}

func (ops defaultProcessGroupOps) SignalVerifiedProcessGroupAfterLeaderExit(identity ProcessIdentity) error {
	if ValidateIdentity(identity) != nil {
		return errors.New("managed process identity is incomplete")
	}
	probe := ops.Probe(identity)
	if probe.State == ProcessAbsent {
		// The group can disappear between the bounded wait and this re-probe.
		// That is already successful cleanup and must not become an error.
		return nil
	}
	if probe.State != ProcessPresent {
		return errors.New("managed process group is not a verified leaderless group")
	}
	return signalOwnedProcessGroup(identity.PID, syscall.SIGKILL)
}

func (ops defaultProcessGroupOps) WaitForProcessGroupEmpty(ctx context.Context, identity ProcessIdentity, leaderDone <-chan error) ProcessProbe {
	if ctx == nil {
		return ProcessProbe{State: ProcessUnknown}
	}
	interval := time.NewTicker(10 * time.Millisecond)
	defer interval.Stop()
	for {
		probe := ops.Probe(identity)
		if probe.State == ProcessAbsent {
			return probe
		}
		if err := ctx.Err(); err != nil {
			return probe
		}
		select {
		case <-leaderDone:
			// A leader exit is only an observation. Continue probing until the
			// complete process group, including descendants, is absent.
			leaderDone = nil
		case <-ctx.Done():
			return probe
		case <-interval.C:
		}
	}
}

const managedStderrTailBytes = 2048

type managedStderrTail struct {
	mu       sync.Mutex
	contents string
	done     chan struct{}
}

func newManagedStderrTail() *managedStderrTail {
	return &managedStderrTail{done: make(chan struct{})}
}

// drain retains only a short, redacted tail. Stderr is otherwise discarded,
// but a crashed managed Pi needs a useful public reason instead of a bare exit
// code. Redaction happens before retention so raw credentials never enter the
// durable managed-agent error or survive in this process object.
func (tail *managedStderrTail) drain(reader io.Reader) {
	defer close(tail.done)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 64*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(RedactedStderr(scanner.Text()))
		if line == "" {
			continue
		}
		tail.mu.Lock()
		tail.contents += line + "\n"
		if len(tail.contents) > managedStderrTailBytes {
			tail.contents = tail.contents[len(tail.contents)-managedStderrTailBytes:]
		}
		tail.mu.Unlock()
	}
}

func (tail *managedStderrTail) summary() string {
	if tail == nil {
		return ""
	}
	tail.mu.Lock()
	defer tail.mu.Unlock()
	return strings.TrimSpace(tail.contents)
}

type managedProcessExitError struct {
	cause  error
	detail string
}

func (err *managedProcessExitError) Error() string {
	if err == nil || err.cause == nil {
		return "managed Pi exited"
	}
	if err.detail == "" {
		return err.cause.Error()
	}
	return err.cause.Error() + ": " + err.detail
}

func (err *managedProcessExitError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

// ManagedProcessExitDiagnostic turns a confirmed child exit into the one
// bounded failure message that may be stored and projected. It deliberately
// describes only the OS outcome and the already-redacted stderr tail.
func ManagedProcessExitDiagnostic(err error) string {
	outcome := "managed Pi exited before readiness"
	if err == nil {
		outcome = "managed Pi exited with code 0"
	} else {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				outcome = fmt.Sprintf("managed Pi exited after signal %s", status.Signal())
			} else if code := exitErr.ExitCode(); code >= 0 {
				outcome = fmt.Sprintf("managed Pi exited with code %d", code)
			}
		}
	}
	var processErr *managedProcessExitError
	if !errors.As(err, &processErr) || processErr.detail == "" {
		return outcome
	}
	detail := RedactedStderr(processErr.detail)
	if detail == "" {
		return outcome
	}
	return store.RedactedManagedDiagnostic(outcome + ": " + detail)
}

func processStartMarker(pid int) (string, error) {
	if pid <= 0 {
		return "", errors.New("managed process pid is invalid")
	}
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if err != nil {
			return "", err
		}
		stat, err := parseLinuxProcessStat(data)
		if err != nil || stat.startMarker == "" {
			return "", errors.New("Linux process start marker is unavailable")
		}
		return "linux:" + stat.startMarker, nil
	}
	output, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", err
	}
	marker := strings.TrimSpace(string(output))
	if marker == "" {
		return "", syscall.ESRCH
	}
	return marker, nil
}

func ValidateIdentity(identity ProcessIdentity) error {
	if identity.PID <= 0 || identity.StartMarker == "" || identity.Incarnation == "" {
		return errors.New("managed process identity is incomplete")
	}
	return nil
}

// RedactedStderr uses the durable/public diagnostic sanitizer so the process
// tail cannot preserve a path that would later be rejected at projection time.
func RedactedStderr(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return store.RedactedManagedDiagnostic(value)
}

func ValidateCwd(cwd string) (string, error) { return filepath.Abs(cwd) }
