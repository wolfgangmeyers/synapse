package managed

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

const managedGroupHelperMode = "MECHA_MANAGED_GROUP_HELPER_MODE"

// TestManagedProcessGroupHelper is a subprocess fixture, not a product test.
// It uses exec.Command with inherited pipes so the leader and descendant share
// the real process group without introducing a shell or shell-parsed input.
func TestManagedProcessGroupHelper(t *testing.T) {
	mode := os.Getenv(managedGroupHelperMode)
	if mode == "" {
		return
	}
	status := os.NewFile(uintptr(3), "managed-group-helper-status")
	if status == nil {
		os.Exit(2)
	}
	defer status.Close()
	write := func(message string) {
		_, _ = io.WriteString(status, message+"\n")
	}

	switch mode {
	case "leader", "leader-stubborn":
		descendant := exec.Command(os.Args[0], "-test.run=TestManagedProcessGroupHelper", "--", "descendant")
		descendant.Env = append(os.Environ(), managedGroupHelperMode+"=descendant")
		descendant.ExtraFiles = []*os.File{status}
		descendant.Stdout = io.Discard
		descendant.Stderr = io.Discard
		if err := descendant.Start(); err != nil {
			write("descendant-start-error:" + err.Error())
			os.Exit(3)
		}
		write(fmt.Sprintf("descendant-pid:%d", descendant.Process.Pid))
		write("leader-ready")
		signals := make(chan os.Signal, 1)
		signalNotify(signals, syscall.SIGTERM)
		defer signalStop(signals)
		<-signals
		write("leader-term")
		if mode == "leader-stubborn" {
			select {}
		}
	case "descendant":
		write("descendant-ready")
		signals := make(chan os.Signal, 1)
		signalNotify(signals, syscall.SIGTERM)
		defer signalStop(signals)
		for signal := range signals {
			if signal == syscall.SIGTERM {
				write("descendant-term")
			}
		}
	}
}

// These wrappers keep the subprocess fixture source portable. The test itself
// skips operating systems where the process.go Setpgid/negative-Kill contract
// is unavailable; the wrappers keep this file buildable on those systems.
func signalNotify(channel chan<- os.Signal, signals ...os.Signal) {
	signal.Notify(channel, signals...)
}

func signalStop(channel chan<- os.Signal) { signal.Stop(channel) }

type realManagedGroup struct {
	command    *exec.Cmd
	identity   ProcessIdentity
	events     <-chan string
	leaderDone <-chan error
}

func startRealManagedGroup(t *testing.T) realManagedGroup {
	return startRealManagedGroupMode(t, "leader")
}

func startRealManagedGroupMode(t *testing.T, mode string) realManagedGroup {
	t.Helper()
	statusReader, statusWriter, err := os.Pipe()
	require.NoError(t, err)
	command := exec.Command(os.Args[0], "-test.run=TestManagedProcessGroupHelper", "--", mode)
	command.Env = append(os.Environ(), managedGroupHelperMode+"="+mode)
	command.ExtraFiles = []*os.File{statusWriter}
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, command.Start())
	require.NoError(t, statusWriter.Close())
	marker, err := processStartMarker(command.Process.Pid)
	require.NoError(t, err)

	events := make(chan string, 16)
	go func() {
		scanner := bufio.NewScanner(statusReader)
		for scanner.Scan() {
			events <- strings.TrimSpace(scanner.Text())
		}
		close(events)
	}()
	leaderDone := make(chan error, 1)
	go func() { leaderDone <- command.Wait() }()

	group := realManagedGroup{command: command, identity: ProcessIdentity{PID: command.Process.Pid, StartMarker: marker, Incarnation: "behavioral-functional"}, events: events, leaderDone: leaderDone}
	// Cleanup is deliberately independent of Manager. A failing assertion must
	// still kill the entire test-owned group and reap the leader.
	t.Cleanup(func() {
		_ = syscall.Kill(-group.identity.PID, syscall.SIGKILL)
		select {
		case <-group.leaderDone:
		case <-time.After(2 * time.Second):
			t.Error("managed group helper leader was not reaped")
		}
		_ = statusReader.Close()
	})
	awaitGroupEventPrefix(t, group.events, "descendant-pid:")
	awaitGroupEventSet(t, group.events, "descendant-ready", "leader-ready")
	return group
}

func awaitGroupEvent(t *testing.T, events <-chan string, expected string) {
	t.Helper()
	select {
	case event := <-events:
		require.Equal(t, expected, event)
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for managed helper event %q", expected)
	}
}

func awaitGroupEventPrefix(t *testing.T, events <-chan string, prefix string) {
	t.Helper()
	select {
	case event := <-events:
		require.True(t, strings.HasPrefix(event, prefix), "unexpected helper event %q", event)
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for managed helper event prefix %q", prefix)
	}
}

func awaitGroupEventSet(t *testing.T, events <-chan string, expected ...string) {
	t.Helper()
	seen := make(map[string]bool, len(expected))
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for len(seen) < len(expected) {
		select {
		case event := <-events:
			for _, want := range expected {
				if event == want {
					seen[want] = true
				}
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for managed helper events %v", expected)
		}
	}
}

func waitForManagedAction(t *testing.T, durable *store.Store, actionID string) *store.ManagedLifecycleAction {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		action, err := durable.ManagedLifecycleAction(context.Background(), actionID)
		if err == nil && (action.State == "succeeded" || action.State == "failed" || action.State == "cancelled" || action.State == "superseded") {
			return action
		}
		select {
		case <-deadline.C:
			require.NoError(t, err)
			t.Fatalf("managed action %s did not reach a terminal state", actionID)
		case <-tick.C:
		}
	}
}

func newRealCleanupManager(t *testing.T, group realManagedGroup, storedMarker string) (*Manager, *store.Store, string) {
	t.Helper()
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, databasePath, store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, durable.Close()) })
	config, paths := managedLaunchConfigFixture(t)
	config.Enabled = false
	config.Limits = Limits{GracefulStop: 40 * time.Millisecond, TermStop: behavioralCleanupTermStop}
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	manager, err := NewManager(ManagerOptions{Store: durable, Config: config, Manifest: manifest})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close(context.Background())) })

	agent := store.ManagedAgentRecord{
		AgentID: "real-process-cleanup", Name: "real-process-cleanup", SlotHeld: true, Cwd: paths["work"],
		SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		DesiredState:  "running", LifecycleState: "running", ActivityState: "idle", AcceptingPrompts: true,
		PiSessionID: "real-cleanup-session", SessionEpoch: 1, ProcessPID: group.identity.PID,
		ProcessStartMarker: storedMarker, ProcessIncarnation: group.identity.Incarnation, RuntimeGeneration: "real-cleanup-runtime",
		ManifestRevision: manifest.Revision, ManifestHash: manifest.CanonicalSHA256,
	}
	seedColdRecoveryAgent(t, durable, agent, store.ManagedOperation{
		OperationID: "123e4567-e89b-42d3-a456-426614174901", Method: "POST", CanonicalPath: "/v1/managed-agents",
		RequestID: "123e4567-e89b-42d3-a456-426614174901", CanonicalBodyHash: strings.Repeat("a", 64),
		State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`),
	})
	return manager, durable, paths["work"]
}

func requireGroupAbsent(t *testing.T, identity ProcessIdentity) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		err := syscall.Kill(-identity.PID, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("managed process group %d remains present: %v", identity.PID, err)
		case <-tick.C:
		}
	}
}

func TestManagedProcessGroupCleanupBehavioralFunctional(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("managed process-group contract requires Unix Setpgid and negative Kill")
	}

	t.Run("cold recovery reaps a stubborn owned leader before replacement", func(t *testing.T) {
		group := startRealManagedGroupMode(t, "leader-stubborn")
		manager := &Manager{processOps: DefaultProcessGroupOps(), config: Config{Limits: Limits{GracefulStop: 40 * time.Millisecond, TermStop: behavioralCleanupTermStop}}}

		reaped := manager.reapRestartGroup(context.Background(), group.identity, group.leaderDone)
		require.True(t, reaped, "an owned leader that survives TERM must receive verified group KILL")
		awaitGroupEvent(t, group.events, "leader-term")
		requireGroupAbsent(t, group.identity)
	})

	t.Run("termination reaps a stubborn descendant after leader exit", func(t *testing.T) {
		group := startRealManagedGroup(t)
		manager, durable, cwd := newRealCleanupManager(t, group, group.identity.StartMarker)
		request := TerminateRequest{
			AgentID: "real-process-cleanup", Method: "DELETE", CanonicalPath: "/v1/managed-agents/real-process-cleanup",
			RequestID: "123e4567-e89b-42d3-a456-426614174902", CanonicalBodyHash: strings.Repeat("b", 64),
		}
		_, err := manager.Terminate(context.Background(), request)
		require.NoError(t, err)
		awaitGroupEventSet(t, group.events, "descendant-term", "leader-term")
		action := waitForManagedAction(t, durable, request.RequestID)
		t.Logf("cleanup action: state=%s stage=%s code=%s message=%s", action.State, action.Stage, action.TerminalCode, action.TerminalMessage)
		require.Equal(t, "succeeded", action.State, "termination must complete after TERM->KILL group cleanup")
		require.Equal(t, "TERMINATED", action.TerminalCode)
		requireGroupAbsent(t, group.identity)
		stored, err := durable.FindManagedOperation(context.Background(), request.Method, request.RequestID)
		require.NoError(t, err)
		require.Contains(t, string(stored.ResponseBody), cwd)
	})

	t.Run("mismatched start marker sends no signal and keeps unsafe state", func(t *testing.T) {
		group := startRealManagedGroup(t)
		manager, durable, cwd := newRealCleanupManager(t, group, group.identity.StartMarker+"-mismatch")
		request := TerminateRequest{
			AgentID: "real-process-cleanup", Method: "DELETE", CanonicalPath: "/v1/managed-agents/real-process-cleanup",
			RequestID: "123e4567-e89b-42d3-a456-426614174903", CanonicalBodyHash: strings.Repeat("c", 64),
		}
		_, err := manager.Terminate(context.Background(), request)
		require.NoError(t, err)
		action := waitForManagedAction(t, durable, request.RequestID)
		require.Equal(t, "failed", action.State)
		require.Equal(t, "TERMINATION_PROCESS_IDENTITY_UNSAFE", action.TerminalCode)
		require.False(t, action.Retryable)
		agent, err := durable.ManagedAgent(context.Background(), request.AgentID)
		require.NoError(t, err)
		require.Equal(t, cwd, agent.Cwd)
		require.Equal(t, "TERMINATION_PROCESS_IDENTITY_UNSAFE", agent.LastErrorCode)
		select {
		case event := <-group.events:
			require.NotEqual(t, "leader-term", event, "mismatched identity must receive zero signals")
		case <-time.After(100 * time.Millisecond):
		}
	})
}
