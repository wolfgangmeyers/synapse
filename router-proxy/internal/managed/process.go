package managed

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
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
	Environment          []string
}

func (s CommandSpec) validLaunchMode() bool {
	return s.LaunchMode == CreateNew || s.LaunchMode == ResumeExisting
}

func (s CommandSpec) Args() []string {
	return []string{"--mode", "rpc", "--session-dir", s.SessionRoot, "--session-id", s.SessionID, "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--no-builtin-tools", "--approve", "--offline", "--provider", s.Provider, "--model", s.ModelID, "-e", s.MechaRPCExtension, "-e", s.PiSubagentsExtension}
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
	process := &execProcess{cmd: command, stdin: stdin, stdout: stdout, stderr: stderr, identity: ProcessIdentity{PID: command.Process.Pid, StartMarker: marker, Incarnation: incarnation}, waitDone: make(chan struct{}), stopDone: make(chan struct{})}
	go drainStderr(stderr)
	return process, nil
}

type execProcess struct {
	cmd            *exec.Cmd
	stdin          io.WriteCloser
	stdout, stderr io.ReadCloser
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

func drainStderr(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 64*1024)
	for scanner.Scan() {
	}
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

func RedactedStderr(value string) string {
	fields := strings.Fields(value)
	for index, field := range fields {
		lower := strings.ToLower(field)
		if strings.Contains(lower, "token") || strings.Contains(lower, "key") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") {
			fields[index] = "[REDACTED]"
		}
	}
	result := strings.Join(fields, " ")
	if len(result) > 2000 {
		result = result[:2000]
	}
	return result
}
func ValidateCwd(cwd string) (string, error) { return filepath.Abs(cwd) }
