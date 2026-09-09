package managed

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
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
	require.Equal(t, []string{"--mode", "rpc", "--session-dir", "/managed/sessions", "--session-id", "session", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--no-builtin-tools", "--approve", "--offline", "--provider", "openai-codex", "--model", "gpt-5.6-terra", "-e", "rpc.ts", "-e", "subagents.ts", "-e", "cursor-sdk.js"}, spec.Args())
	env := ChildEnvironmentFor(Config{ManagedHome: "/managed/home", SessionRoot: filepath.Join("/managed/home", "sessions")}, "123e4567-e89b-42d3-a456-426614174000", 2)
	require.Equal(t, []string{
		"HOME=/managed/home", "PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin", "TMPDIR=/managed/home/tmp", "LANG=C", "LC_ALL=C",
		"PI_CODING_AGENT_DIR=/managed/home/.pi/agent", "MECHA_MANAGED_SESSION_ROOT=/managed/home/sessions", "MECHA_MANAGED_ACTIVE_ONLY=1",
		"MECHA_RPC_RUNTIME_GENERATION=123e4567-e89b-42d3-a456-426614174000", "MECHA_RPC_SESSION_EPOCH=2",
	}, env)
	require.NotContains(t, env, "OPENAI_API_KEY=secret")
}

func TestProcessFactoryRejectsAnUnspecifiedSessionLaunchMode(t *testing.T) {
	_, err := DefaultProcessFactory().Start(context.Background(), CommandSpec{PiExecutable: "/bin/true", SessionRoot: t.TempDir(), SessionID: "session", Cwd: t.TempDir()})
	require.Error(t, err)
}
