package managed

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type noChildStartError struct{ error }

func (noChildStartError) NoChildStarted() bool { return true }

type uncertainStartError struct{ error }

func (uncertainStartError) NoChildStarted() bool { return false }

func TestStartErrorProvesNoChild(t *testing.T) {
	require.True(t, StartErrorProvesNoChild(noChildStartError{errors.New("no child")}))
	require.False(t, StartErrorProvesNoChild(uncertainStartError{errors.New("uncertain")}))
	require.False(t, StartErrorProvesNoChild(errors.New("plain start error")))
}

func TestOwnedProcessGroupSignalReachesManagedDescendant(t *testing.T) {
	command := exec.Command("/bin/sh", "-c", "sleep 30 & wait")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, command.Start())
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	require.NoError(t, signalOwnedProcessGroup(command.Process.Pid, syscall.SIGTERM))
	finished := make(chan error, 1)
	go func() { finished <- command.Wait() }()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("owned process group did not terminate")
	}
	require.Error(t, signalOwnedProcessGroup(0, syscall.SIGTERM))
}

func TestProcessGroupOps(t *testing.T) {
	command := exec.Command("/bin/sh", "-c", "sleep 30 & wait")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, command.Start())
	defer func() { _ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL); _ = command.Wait() }()
	marker, err := processStartMarker(command.Process.Pid)
	require.NoError(t, err)
	identity := ProcessIdentity{PID: command.Process.Pid, StartMarker: marker, Incarnation: "test"}
	ops := DefaultProcessGroupOps()
	require.Equal(t, ProcessOwned, ops.Probe(identity).State)
	require.NoError(t, syscall.Kill(command.Process.Pid, syscall.SIGTERM))
	finished := make(chan error, 1)
	go func() { finished <- command.Wait() }()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("leader did not exit")
	}
	require.Equal(t, ProcessPresent, ops.Probe(identity).State, "a reaped leader cannot prove its descendants are gone")
	require.Error(t, ops.SignalVerifiedProcessGroup(identity, syscall.SIGTERM), "no leader fallback is allowed")
	require.NoError(t, syscall.Kill(-identity.PID, syscall.SIGKILL))
	deadline, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.Equal(t, ProcessAbsent, ops.WaitForProcessGroupEmpty(deadline, identity, finished).State)
}

func TestCommandSpecUsesDirectPiRPCAndRequiredExtensions(t *testing.T) {
	spec := CommandSpec{SessionRoot: "/managed/sessions", SessionID: "session", LaunchMode: CreateNew, Provider: "openai-codex", ModelID: "gpt-5.6-terra", MechaRPCExtension: "rpc.ts", PiSubagentsExtension: "subagents.ts", CursorExtension: "cursor-sdk.js"}
	require.Equal(t, []string{"--mode", "rpc", "--session-dir", "/managed/sessions", "--session-id", "session", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--approve", "--offline", "--provider", "openai-codex", "--model", "gpt-5.6-terra", "-e", "rpc.ts", "-e", "subagents.ts", "-e", "cursor-sdk.js"}, spec.Args())
	env := ChildEnvironmentFor(Config{ManagedHome: "/managed/home", PiAgentDir: "/ordinary/.pi/agent", SessionRoot: filepath.Join("/managed/home", "sessions"), RouterURL: "http://127.0.0.1:18775"}, "123e4567-e89b-42d3-a456-426614174000", 2)
	require.Equal(t, []string{
		"HOME=/managed/home", "PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin", "TMPDIR=/managed/home/tmp", "LANG=C", "LC_ALL=C",
		"PI_CODING_AGENT_DIR=/ordinary/.pi/agent", "MECHA_MANAGED_SESSION_ROOT=/managed/home/sessions", "MECHA_MANAGED_ACTIVE_ONLY=1", "MECHA_ROUTER_URL=http://127.0.0.1:18775",
		"MECHA_RPC_RUNTIME_GENERATION=123e4567-e89b-42d3-a456-426614174000", "MECHA_RPC_SESSION_EPOCH=2",
	}, env)
	require.NotContains(t, env, "OPENAI_API_KEY=secret")
}

func TestExpectedManagedActiveToolsIncludesBuiltinCodingTools(t *testing.T) {
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	active := expectedManagedActiveTools(manifest)
	for _, name := range []string{"read", "bash", "edit", "write", "subagent", "get_subagent_result", "steer_subagent"} {
		require.Contains(t, active, name)
	}
	require.Len(t, active, len(manifest.Tools)+7)
}

func TestManagedStderrTailRetainsAUsefulRedactedExitReason(t *testing.T) {
	tail := newManagedStderrTail()
	go tail.drain(strings.NewReader("first detail\napi_key=do-not-retain\nlast useful detail\n"))
	<-tail.done

	summary := tail.summary()
	require.Contains(t, summary, "last useful detail")
	require.NotContains(t, summary, "do-not-retain")
	require.Contains(t, summary, "[REDACTED]")
	err := &managedProcessExitError{cause: errors.New("exit status 1"), detail: summary}
	require.Contains(t, err.Error(), "exit status 1")
	require.Contains(t, err.Error(), "last useful detail")
}

func TestManagedProcessExitDiagnosticKeepsOnlySafeExitAndStderrDetails(t *testing.T) {
	command := exec.Command("/bin/sh", "-c", "exit 17")
	exitErr := command.Run()
	require.Error(t, exitErr)

	diagnostic := ManagedProcessExitDiagnostic(&managedProcessExitError{cause: exitErr, detail: "fatal token=do-not-retain /private/router/session.json cwd=/tmp/managed"})
	require.Contains(t, diagnostic, "code 17")
	require.Contains(t, diagnostic, "fatal")
	for _, forbidden := range []string{"do-not-retain", "/private/router/session.json", "/tmp/managed"} {
		require.NotContains(t, diagnostic, forbidden)
	}
	require.Contains(t, diagnostic, "[REDACTED]")
	require.LessOrEqual(t, len(diagnostic), 512)
	bounded := ManagedProcessExitDiagnostic(&managedProcessExitError{cause: exitErr, detail: strings.Repeat("permission denied ", 100)})
	require.LessOrEqual(t, len(bounded), 512)
	require.Contains(t, bounded, "permission denied")
	signalCommand := exec.Command("/bin/sh", "-c", "kill -TERM $$")
	signalErr := signalCommand.Run()
	require.Error(t, signalErr)
	require.Contains(t, ManagedProcessExitDiagnostic(&managedProcessExitError{cause: signalErr}), "signal terminated")
}

func TestRedactedStderrRedactsLocalPathsAcrossDiagnosticEncodings(t *testing.T) {
	for _, test := range []struct {
		name, detail, forbidden string
	}{
		{"quoted Unix path", `exit status 17: cwd="/Users/wolfgang/project/output.log" token=secret-value permission denied`, "/Users/wolfgang/project/output.log"},
		{"HTML-escaped quote path", `exit status 17: cwd=&quot;/Users/wolfgang/project/output.log&quot; token=secret-value permission denied`, "/Users/wolfgang/project/output.log"},
		{"quoted path with spaces", `exit status 17: cwd="/Users/wolfgang/project/output log" token=secret-value permission denied`, "/Users/wolfgang/project/output log"},
		{"file URL", `exit status 17: file:///Users/wolfgang/project/output.log token=secret-value permission denied`, "file:///Users/wolfgang/project/output.log"},
		{"Windows-like path", `exit status 17: cwd="C:\Users\wolfgang\project\output log" token=secret-value permission denied`, `C:\Users\wolfgang\project\output log`},
	} {
		t.Run(test.name, func(t *testing.T) {
			diagnostic := RedactedStderr(test.detail)
			require.NotContains(t, diagnostic, test.forbidden)
			require.NotContains(t, diagnostic, "secret-value")
			require.Contains(t, diagnostic, "exit status 17")
			require.Contains(t, diagnostic, "permission denied")
			require.Contains(t, diagnostic, "[REDACTED]")
			require.LessOrEqual(t, len(diagnostic), 512)
		})
	}
}

func TestProcessFactoryRejectsAnUnspecifiedSessionLaunchMode(t *testing.T) {
	_, err := DefaultProcessFactory().Start(context.Background(), CommandSpec{PiExecutable: "/bin/true", SessionRoot: t.TempDir(), SessionID: "session", Cwd: t.TempDir()})
	require.Error(t, err)
}

type processHandshake struct {
	process *execProcess
	status  *os.File
	control *os.File
	reader  *bufio.Reader
}

func startHandshakeProcess(t *testing.T, script string) processHandshake {
	t.Helper()
	status, statusWriter, err := os.Pipe()
	require.NoError(t, err)
	controlReader, control, err := os.Pipe()
	require.NoError(t, err)
	command := exec.Command("/bin/sh", "-c", script)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.ExtraFiles = []*os.File{statusWriter, controlReader}
	stdin, err := command.StdinPipe()
	require.NoError(t, err)
	stdout, err := command.StdoutPipe()
	require.NoError(t, err)
	stderr, err := command.StderrPipe()
	require.NoError(t, err)
	require.NoError(t, command.Start())
	require.NoError(t, statusWriter.Close())
	require.NoError(t, controlReader.Close())
	marker, err := processStartMarker(command.Process.Pid)
	require.NoError(t, err)
	tail := newManagedStderrTail()
	go tail.drain(stderr)
	process := &execProcess{
		cmd: command, stdin: stdin, stdout: stdout, stderr: stderr, stderrTail: tail,
		identity: ProcessIdentity{PID: command.Process.Pid, StartMarker: marker, Incarnation: "test-incarnation"},
		waitDone: make(chan struct{}),
	}
	handshake := processHandshake{process: process, status: status, control: control, reader: bufio.NewReader(status)}
	t.Cleanup(func() {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		_ = process.Wait()
		_ = status.Close()
		_ = control.Close()
	})
	return handshake
}

func requireHandshake(t *testing.T, reader *bufio.Reader, expected string) {
	t.Helper()
	line, err := reader.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, expected, strings.TrimSpace(line))
}

func requireHandshakeSet(t *testing.T, reader *bufio.Reader, expected ...string) {
	t.Helper()
	seen := make(map[string]bool, len(expected))
	for range expected {
		line, err := reader.ReadString('\n')
		require.NoError(t, err)
		seen[strings.TrimSpace(line)] = true
	}
	for _, value := range expected {
		require.True(t, seen[value], "missing handshake %q", value)
	}
}

type passiveProbeOps struct {
	states  []ProcessProbeState
	probes  int
	signals []syscall.Signal
}

func (ops *passiveProbeOps) Probe(ProcessIdentity) ProcessProbe {
	index := ops.probes
	ops.probes++
	if index >= len(ops.states) {
		index = len(ops.states) - 1
	}
	return ProcessProbe{State: ops.states[index]}
}
func (ops *passiveProbeOps) CloseProcessInput(Process) error { return nil }
func (ops *passiveProbeOps) SignalVerifiedProcessGroup(ProcessIdentity, syscall.Signal) error {
	return errors.New("unexpected signal")
}
func (ops *passiveProbeOps) SignalVerifiedProcessGroupAfterLeaderExit(ProcessIdentity) error {
	return errors.New("unexpected leaderless signal")
}
func (ops *passiveProbeOps) WaitForProcessGroupEmpty(context.Context, ProcessIdentity, <-chan error) ProcessProbe {
	return ProcessProbe{State: ProcessPresent}
}

type stopRaceOps struct {
	states             []ProcessProbeState
	probes             int
	signalErr          error
	leaderlessErr      error
	cancelOnLeaderless context.CancelFunc
	cancelOnWait       context.CancelFunc
	signals            []syscall.Signal
	leaderlessCalls    int
	waitState          ProcessProbeState
}

func (ops *stopRaceOps) Probe(ProcessIdentity) ProcessProbe {
	index := ops.probes
	ops.probes++
	if index >= len(ops.states) {
		index = len(ops.states) - 1
	}
	return ProcessProbe{State: ops.states[index]}
}
func (ops *stopRaceOps) CloseProcessInput(Process) error { return nil }
func (ops *stopRaceOps) SignalVerifiedProcessGroup(_ ProcessIdentity, signal syscall.Signal) error {
	if ops.signalErr != nil {
		return ops.signalErr
	}
	ops.signals = append(ops.signals, signal)
	return nil
}
func (ops *stopRaceOps) SignalVerifiedProcessGroupAfterLeaderExit(ProcessIdentity) error {
	ops.leaderlessCalls++
	if ops.leaderlessErr != nil {
		if ops.cancelOnLeaderless != nil {
			ops.cancelOnLeaderless()
		}
		return ops.leaderlessErr
	}
	ops.signals = append(ops.signals, syscall.SIGKILL)
	return nil
}
func (ops *stopRaceOps) WaitForProcessGroupEmpty(context.Context, ProcessIdentity, <-chan error) ProcessProbe {
	if ops.cancelOnWait != nil {
		ops.cancelOnWait()
		ops.cancelOnWait = nil
	}
	state := ops.waitState
	if state == "" {
		state = ProcessAbsent
	}
	return ProcessProbe{State: state}
}

func newExitedProcessForStopTest(t *testing.T, ops ProcessGroupOps) *execProcess {
	t.Helper()
	command := exec.Command("/bin/sh", "-c", "exit 0")
	require.NoError(t, command.Start())
	process := &execProcess{
		cmd: command, stdin: nopWriteCloser{io.Discard}, stdout: io.NopCloser(strings.NewReader("")), stderr: io.NopCloser(strings.NewReader("")),
		identity: ProcessIdentity{PID: command.Process.Pid, StartMarker: "test", Incarnation: "test"}, processOps: ops,
		waitDone: make(chan struct{}),
	}
	t.Cleanup(func() { _ = process.Wait() })
	return process
}

func TestExecProcessStopOwnedProofHandlesLeaderExitRaces(t *testing.T) {
	tests := []struct {
		name           string
		states         []ProcessProbeState
		signalErr      error
		wantState      ProcessProbeState
		wantSignals    []syscall.Signal
		wantLeaderless int
		wantProbeCount int
	}{
		{
			name:           "reprobe present uses only leaderless continuation",
			states:         []ProcessProbeState{ProcessOwned, ProcessPresent},
			wantSignals:    []syscall.Signal{syscall.SIGKILL},
			wantLeaderless: 1,
			wantProbeCount: 2,
		},
		{
			name:           "signal method race reprobes present and continues",
			states:         []ProcessProbeState{ProcessOwned, ProcessOwned, ProcessOwned, ProcessPresent},
			signalErr:      errors.New("process identity is not owned for signal"),
			wantSignals:    []syscall.Signal{syscall.SIGKILL},
			wantLeaderless: 1,
			wantProbeCount: 4,
		},
		{
			name:           "initial present never signals",
			states:         []ProcessProbeState{ProcessPresent, ProcessPresent, ProcessAbsent},
			wantProbeCount: 3,
		},
		{
			name:           "initial mismatch never signals",
			states:         []ProcessProbeState{ProcessMismatch},
			wantState:      ProcessMismatch,
			wantProbeCount: 1,
		},
		{
			name:           "initial absent succeeds",
			states:         []ProcessProbeState{ProcessAbsent},
			wantProbeCount: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ops := &stopRaceOps{states: test.states, signalErr: test.signalErr}
			process := newExitedProcessForStopTest(t, ops)
			err := process.Stop(context.Background())
			if test.wantState == "" {
				require.NoError(t, err)
			} else {
				var cleanupErr *ProcessCleanupError
				require.ErrorAs(t, err, &cleanupErr)
				require.Equal(t, test.wantState, cleanupErr.State)
			}
			require.Equal(t, test.wantSignals, ops.signals)
			require.Equal(t, test.wantLeaderless, ops.leaderlessCalls)
			require.Equal(t, test.wantProbeCount, ops.probes)
		})
	}
}

func TestExecProcessStopLeaderlessEPERMAcceptsNaturalAbsence(t *testing.T) {
	ops := &stopRaceOps{
		states:        []ProcessProbeState{ProcessOwned, ProcessPresent, ProcessAbsent},
		leaderlessErr: syscall.EPERM,
	}
	process := newExitedProcessForStopTest(t, ops)
	require.NoError(t, process.Stop(context.Background()))
	require.Empty(t, ops.signals, "EPERM must not be treated as a successful signal")
	require.Equal(t, 1, ops.leaderlessCalls)
	require.Equal(t, 3, ops.probes)
}

func TestExecProcessStopLeaderlessEPERMRetainsPersistentPresentFailure(t *testing.T) {
	ops := &stopRaceOps{
		states:        []ProcessProbeState{ProcessOwned, ProcessPresent},
		leaderlessErr: syscall.EPERM,
	}
	process := newExitedProcessForStopTest(t, ops)
	started := time.Now()
	err := process.Stop(context.Background())
	var cleanupErr *ProcessCleanupError
	require.ErrorAs(t, err, &cleanupErr)
	require.Equal(t, ProcessPresent, cleanupErr.State)
	require.ErrorIs(t, err, syscall.EPERM)
	require.Empty(t, ops.signals)
	require.Equal(t, 1, ops.leaderlessCalls)
	require.Less(t, time.Since(started), 500*time.Millisecond)
}

func TestExecProcessStopLeaderlessEPERMDrainHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ops := &stopRaceOps{
		states:             []ProcessProbeState{ProcessOwned, ProcessPresent},
		leaderlessErr:      syscall.EPERM,
		cancelOnLeaderless: cancel,
	}
	process := newExitedProcessForStopTest(t, ops)
	err := process.Stop(ctx)
	var cleanupErr *ProcessCleanupError
	require.ErrorAs(t, err, &cleanupErr)
	require.Equal(t, ProcessPresent, cleanupErr.State)
	require.ErrorIs(t, err, syscall.EPERM)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, ops.signals)
	require.Equal(t, 1, ops.leaderlessCalls)
}

func TestExecProcessStopPassivelyDrainsInitiallyPresentGroup(t *testing.T) {
	ops := &passiveProbeOps{states: []ProcessProbeState{ProcessPresent, ProcessPresent, ProcessAbsent}}
	process := newExitedProcessForStopTest(t, ops)
	require.NoError(t, process.Stop(context.Background()))
	require.Empty(t, ops.signals)
	require.GreaterOrEqual(t, ops.probes, 3)
}

func TestExecProcessStopRetainsPersistentPresentWithoutSignals(t *testing.T) {
	ops := &passiveProbeOps{states: []ProcessProbeState{ProcessPresent}}
	process := newExitedProcessForStopTest(t, ops)
	started := time.Now()
	err := process.Stop(context.Background())
	elapsed := time.Since(started)
	var cleanupErr *ProcessCleanupError
	require.ErrorAs(t, err, &cleanupErr)
	require.Equal(t, ProcessPresent, cleanupErr.State)
	require.Empty(t, ops.signals)
	require.Less(t, elapsed, 500*time.Millisecond)
}

func TestExecProcessStopPassiveDrainHonorsCancellation(t *testing.T) {
	ops := &passiveProbeOps{states: []ProcessProbeState{ProcessPresent}}
	process := newExitedProcessForStopTest(t, ops)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	started := time.Now()
	err := process.Stop(ctx)
	var cleanupErr *ProcessCleanupError
	require.ErrorAs(t, err, &cleanupErr)
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(started), 500*time.Millisecond)
	require.Empty(t, ops.signals)
}

func TestExecProcessStopRequiresCompleteGroupAbsence(t *testing.T) {
	handshake := startHandshakeProcess(t, `(printf 'desc-ready\n' >&3; trap 'printf "desc-term\n" >&3; exit 0' TERM; while :; do sleep 30; done) & child=$!; trap 'printf "leader-term\n" >&3; exit 0' TERM; printf 'leader-ready\n' >&3; wait "$child"`)
	requireHandshakeSet(t, handshake.reader, "desc-ready", "leader-ready")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, handshake.process.Stop(ctx))
	requireHandshakeSet(t, handshake.reader, "desc-term", "leader-term")
	require.Equal(t, ProcessAbsent, DefaultProcessGroupOps().Probe(handshake.process.identity).State)
}

func TestExecProcessStopRetryAfterDeadlineDoesNotSignalLeaderlessGroup(t *testing.T) {
	firstContext, cancelFirst := context.WithCancel(context.Background())
	ops := &stopRaceOps{
		states:       []ProcessProbeState{ProcessOwned, ProcessOwned, ProcessOwned, ProcessPresent},
		waitState:    ProcessPresent,
		cancelOnWait: cancelFirst,
	}
	process := newExitedProcessForStopTest(t, ops)

	firstErr := process.Stop(firstContext)
	var firstCleanupErr *ProcessCleanupError
	require.ErrorAs(t, firstErr, &firstCleanupErr)
	require.Equal(t, ProcessPresent, firstCleanupErr.State)
	require.ErrorIs(t, firstErr, context.Canceled)

	// A later Stop call cannot inherit the first call's TERM proof to signal a
	// leaderless group, even though that earlier cleanup did not finish.
	secondErr := process.Stop(context.Background())
	var secondCleanupErr *ProcessCleanupError
	require.ErrorAs(t, secondErr, &secondCleanupErr)
	require.Equal(t, ProcessPresent, secondCleanupErr.State)
	require.ErrorContains(t, secondErr, "process cleanup requires the original stop attempt")
	require.Equal(t, []syscall.Signal{syscall.SIGTERM}, ops.signals)
	require.Zero(t, ops.leaderlessCalls)
	require.Equal(t, 4, ops.probes)
}

func TestExecProcessStopDoesNotSignalUnownedGroup(t *testing.T) {
	handshake := startHandshakeProcess(t, `printf 'ready\n' >&3; IFS= read release <&4; printf 'exit\n' >&3`)
	requireHandshake(t, handshake.reader, "ready")
	identity := handshake.process.identity

	handshake.process.identity.StartMarker = "different-start"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	err := handshake.process.Stop(ctx)
	cancel()
	var cleanupErr *ProcessCleanupError
	require.ErrorAs(t, err, &cleanupErr)
	require.Equal(t, ProcessMismatch, cleanupErr.State)
	require.Equal(t, ProcessOwned, DefaultProcessGroupOps().Probe(identity).State)

	handshake.process.identity = ProcessIdentity{PID: identity.PID, Incarnation: identity.Incarnation}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	err = handshake.process.Stop(ctx)
	cancel()
	cleanupErr = nil
	require.ErrorAs(t, err, &cleanupErr)
	require.Equal(t, ProcessUnknown, cleanupErr.State)
	require.Equal(t, ProcessOwned, DefaultProcessGroupOps().Probe(identity).State)

	_, err = handshake.control.Write([]byte("release\n"))
	require.NoError(t, err)
	requireHandshake(t, handshake.reader, "exit")
}

func TestExecProcessStopEscalatesSIGKILLForBackgroundCaller(t *testing.T) {
	handshake := startHandshakeProcess(t, `trap 'printf "term-ignored\n" >&3' TERM; printf 'ready\n' >&3; while :; do sleep 30; done`)
	requireHandshake(t, handshake.reader, "ready")

	started := time.Now()
	stopDone := make(chan error, 1)
	go func() { stopDone <- handshake.process.Stop(context.Background()) }()
	requireHandshake(t, handshake.reader, "term-ignored")
	select {
	case <-handshake.process.waitDone:
		t.Fatal("process exited after SIGTERM instead of waiting for escalation")
	case <-time.After(100 * time.Millisecond):
	}

	select {
	case err := <-stopDone:
		require.NoError(t, err)
	case <-time.After(processStopInternalGrace + time.Second):
		t.Fatal("background stop exceeded its internal bound")
	}
	require.GreaterOrEqual(t, time.Since(started), processStopInternalGrace/3)
	require.Equal(t, ProcessAbsent, DefaultProcessGroupOps().Probe(handshake.process.identity).State)
	select {
	case <-handshake.process.waitDone:
	default:
		t.Fatal("process waiter did not complete")
	}
	require.ErrorIs(t, syscall.Kill(-handshake.process.identity.PID, 0), syscall.ESRCH)
}

func TestProcessStopContextHonorsEarlierCallerDeadline(t *testing.T) {
	caller, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	callerDeadline, ok := caller.Deadline()
	require.True(t, ok)

	stopCtx, stopCancel := processStopContext(caller)
	defer stopCancel()
	stopDeadline, ok := stopCtx.Deadline()
	require.True(t, ok)
	require.LessOrEqual(t, stopDeadline, callerDeadline)
	require.Less(t, stopDeadline.Sub(time.Now()), processStopInternalGrace)
	<-stopCtx.Done()
	require.ErrorIs(t, stopCtx.Err(), context.DeadlineExceeded)
}

func TestExecProcessStopSerializesConcurrentAttempts(t *testing.T) {
	handshake := startHandshakeProcess(t, `(trap '' TERM HUP; printf 'desc-ready\n' >&3; IFS= read release <&4; printf 'desc-exit\n' >&3; exit 0) & child=$!; trap 'printf "leader-term\n" >&3; exit 0' TERM; printf 'leader-ready\n' >&3; wait "$child"`)
	requireHandshakeSet(t, handshake.reader, "desc-ready", "leader-ready")
	firstDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		firstDone <- handshake.process.Stop(ctx)
		cancel()
	}()
	requireHandshake(t, handshake.reader, "leader-term")

	secondDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		secondDone <- handshake.process.Stop(ctx)
		cancel()
	}()
	firstErr := <-firstDone
	require.NoError(t, firstErr)
	require.Equal(t, ProcessAbsent, DefaultProcessGroupOps().Probe(handshake.process.identity).State)
	secondErr := <-secondDone
	require.NoError(t, secondErr)
}
