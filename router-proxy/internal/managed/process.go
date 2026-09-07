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
)

type CommandSpec struct {
	PiExecutable         string
	SessionRoot          string
	SessionID            string
	Cwd                  string
	Provider             string
	ModelID              string
	MechaRPCExtension    string
	PiSubagentsExtension string
	CursorExtension      string
	Environment          []string
}

func (s CommandSpec) Args() []string {
	return []string{"--mode", "rpc", "--session-dir", s.SessionRoot, "--session-id", s.SessionID, "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--no-builtin-tools", "--approve", "--offline", "--provider", s.Provider, "--model", s.ModelID, "-e", s.MechaRPCExtension, "-e", s.PiSubagentsExtension, "-e", s.CursorExtension}
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

type execProcessFactory struct{}

func DefaultProcessFactory() ProcessFactory { return execProcessFactory{} }
func (execProcessFactory) Start(ctx context.Context, spec CommandSpec) (Process, error) {
	if spec.PiExecutable == "" || spec.SessionRoot == "" || spec.SessionID == "" || spec.Cwd == "" {
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
	marker := processStartMarker(command.Process.Pid)
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
		if processStartMarker(p.cmd.Process.Pid) != p.identity.StartMarker {
			result = errors.New("managed process identity no longer matches")
			close(p.stopDone)
			return
		}
		if err := signalOwnedProcessGroup(p.cmd.Process.Pid, syscall.SIGTERM); err != nil {
			result = err
			close(p.stopDone)
			return
		}
		done := make(chan error, 1)
		go func() { done <- p.Wait() }()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		select {
		case result = <-done:
		case <-timer.C:
			_ = signalOwnedProcessGroup(p.cmd.Process.Pid, syscall.SIGKILL)
			result = <-done
		case <-ctx.Done():
			// Shutdown callers must not leave a managed child behind when their
			// outer deadline expires; the process identity is still ours here.
			_ = signalOwnedProcessGroup(p.cmd.Process.Pid, syscall.SIGKILL)
			result = <-done
		}
		// A signal-terminated child is the expected result of a successful
		// controlled stop. Control failures (identity, signaling, or reaping)
		// remain errors so the manager can publish a failed projection.
		var exitErr *exec.ExitError
		if errors.As(result, &exitErr) {
			result = nil
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
	// Setpgid: true makes the child the leader of its own group. A negative PID
	// is therefore scoped to this managed child and its descendants. Some
	// platforms can reject the group signal after the group has already begun
	// exiting; the leader fallback still uses the identity check in Stop and
	// lets the waiting goroutine reap the child.
	if err := syscall.Kill(-pid, signal); err == nil {
		return nil
	} else if leaderErr := syscall.Kill(pid, signal); leaderErr == nil {
		return nil
	} else {
		return err
	}
}

func drainStderr(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 64*1024)
	for scanner.Scan() {
	}
}

func processStartMarker(pid int) string {
	output, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err == nil && strings.TrimSpace(string(output)) != "" {
		return strings.TrimSpace(string(output))
	}
	return fmt.Sprintf("%d", time.Now().UnixNano())
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
