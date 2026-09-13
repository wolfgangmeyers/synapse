package managed

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
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

func (s CommandSpec) Args() []string {
	arguments := []string{"--mode", "rpc", "--session-dir", s.SessionRoot, "--session-id", s.SessionID, "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--approve", "--offline", "--provider", s.Provider, "--model", s.ModelID, "-e", s.MechaRPCExtension, "-e", s.PiSubagentsExtension}
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

// ProcessGroupOps is the narrow ownership boundary used by restart execution.
// It never exposes a positive-PID signal fallback.
type ProcessGroupOps interface {
	Probe(ProcessIdentity) ProcessProbe
	CloseProcessInput(Process) error
	SignalVerifiedProcessGroup(ProcessIdentity, syscall.Signal) error
	WaitForProcessGroupEmpty(context.Context, ProcessIdentity, <-chan error) ProcessProbe
}

type defaultProcessGroupOps struct{}

func DefaultProcessGroupOps() ProcessGroupOps { return defaultProcessGroupOps{} }

type execProcessFactory struct{}

func DefaultProcessFactory() ProcessFactory { return execProcessFactory{} }
func (execProcessFactory) Start(ctx context.Context, spec CommandSpec) (Process, error) {
	if spec.PiExecutable == "" || spec.SessionRoot == "" || spec.SessionID == "" || spec.Cwd == "" || !spec.validLaunchMode() {
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
	command.Env = append([]string(nil), spec.Environment...)
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
	process := &execProcess{cmd: command, stdin: stdin, stdout: stdout, stderr: stderr, stderrTail: stderrTail, identity: ProcessIdentity{PID: command.Process.Pid, StartMarker: marker, Incarnation: incarnation}, waitDone: make(chan struct{}), stopDone: make(chan struct{})}
	go stderrTail.drain(stderr)
	return process, nil
}

type execProcess struct {
	cmd            *exec.Cmd
	stdin          io.WriteCloser
	stdout, stderr io.ReadCloser
	stderrTail     *managedStderrTail
	identity       ProcessIdentity
	waitOnce       sync.Once
	waitErr        error
	waitDone       chan struct{}
	stopOnce       sync.Once
	stopDone       chan struct{}
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
		ctx = context.Background()
	}
	var result error
	p.stopOnce.Do(func() {
		if p.cmd.Process == nil {
			close(p.stopDone)
			return
		}
		select {
		case <-p.waitDone:
			result = p.Wait()
			close(p.stopDone)
			return
		default:
		}
		ops := DefaultProcessGroupOps()
		if ops.Probe(p.identity).State != ProcessOwned {
			result = errors.New("managed process identity no longer matches")
			close(p.stopDone)
			return
		}
		if err := ops.SignalVerifiedProcessGroup(p.identity, syscall.SIGTERM); err != nil {
			result = err
			close(p.stopDone)
			return
		}
		done := make(chan error, 1)
		go func() { done <- p.Wait() }()
		probe := ops.WaitForProcessGroupEmpty(ctx, p.identity, done)
		if probe.State != ProcessAbsent {
			if ops.Probe(p.identity).State == ProcessOwned {
				_ = ops.SignalVerifiedProcessGroup(p.identity, syscall.SIGKILL)
				probe = ops.WaitForProcessGroupEmpty(context.Background(), p.identity, done)
			}
		}
		if probe.State != ProcessAbsent {
			result = errors.New("managed process group did not stop")
		}
		close(p.stopDone)
	})
	<-p.stopDone
	return result
}

func signalOwnedProcessGroup(pid int, signal syscall.Signal) error {
	if pid <= 0 {
		return errors.New("managed process group identity is invalid")
	}
	return syscall.Kill(-pid, signal)
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

func (ops defaultProcessGroupOps) WaitForProcessGroupEmpty(ctx context.Context, identity ProcessIdentity, leaderDone <-chan error) ProcessProbe {
	if ctx == nil {
		ctx = context.Background()
	}
	interval := time.NewTicker(10 * time.Millisecond)
	defer interval.Stop()
	leaderObserved := false
	for {
		probe := ops.Probe(identity)
		if probe.State == ProcessAbsent {
			return probe
		}
		select {
		case <-leaderDone:
			leaderObserved = true
			if probe.State == ProcessPresent {
				// The leader exited, but the group remains. Keep waiting for all
				// descendants, never treating leader exit as a success.
				continue
			}
		case <-ctx.Done():
			if leaderObserved && probe.State == ProcessPresent {
				return probe
			}
			return ProcessProbe{State: ProcessUnknown}
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
