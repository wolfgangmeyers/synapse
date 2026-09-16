package managedbridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/sys/unix"
)

func TestManagedRuntimeStatusRoundTripTamperAndSecureFiles(t *testing.T) {
	verifier := mustVerifier(t, 'q')
	fence := validFence()
	status := NewManagedRuntimeStatus(fence, ProcessIdentity{PID: int(fence.PiPID), StartMarker: fence.PiStartMarker, Incarnation: fence.PiIncarnation}, ProcessIdentity{PID: 991, StartMarker: "outer", Incarnation: uuid1}, time.Unix(1700000000, 123).UTC())
	status, err := SignManagedRuntimeStatus(verifier, status)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "status.json")
	if err := PublishManagedRuntimeStatus(path, status, verifier); err != nil {
		t.Fatal(err)
	}
	got, err := ReadManagedRuntimeStatus(path, verifier)
	if err != nil || got != status {
		t.Fatalf("round trip status=%+v err=%v", got, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "q") || strings.Contains(string(data), "token") {
		t.Fatalf("status contains secret material: %q", data)
	}
	for name, mutate := range map[string]func(*ManagedRuntimeStatus){
		"agent":             func(s *ManagedRuntimeStatus) { s.AgentID = "other" },
		"bridge":            func(s *ManagedRuntimeStatus) { s.BridgeInstance = uuid2 },
		"session":           func(s *ManagedRuntimeStatus) { s.Fence.PiSessionID = uuid1 },
		"epoch":             func(s *ManagedRuntimeStatus) { s.Fence.PiSessionEpoch++ },
		"generation":        func(s *ManagedRuntimeStatus) { s.Fence.RuntimeGeneration++ },
		"manifest revision": func(s *ManagedRuntimeStatus) { s.Fence.ManifestRevision++ },
		"manifest hash":     func(s *ManagedRuntimeStatus) { s.Fence.ManifestHash = strings.Repeat("b", 64) },
		"inner pid":         func(s *ManagedRuntimeStatus) { s.InnerPi.PID++ },
		"inner start":       func(s *ManagedRuntimeStatus) { s.InnerPi.StartMarker = "other" },
		"inner incarnation": func(s *ManagedRuntimeStatus) { s.InnerPi.Incarnation = uuid1 },
		"outer pid":         func(s *ManagedRuntimeStatus) { s.OuterBridge.PID++ },
		"outer start":       func(s *ManagedRuntimeStatus) { s.OuterBridge.StartMarker = "other" },
		"outer incarnation": func(s *ManagedRuntimeStatus) { s.OuterBridge.Incarnation = uuid2 },
		"proof":             func(s *ManagedRuntimeStatus) { s.Proof = strings.Repeat("a", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			bad := status
			mutate(&bad)
			if err := VerifyManagedRuntimeStatus(verifier, bad); err == nil {
				t.Fatal("tampered status verified")
			}
		})
	}
	if _, err := ReadManagedRuntimeStatus(path, mustVerifier(t, 'r')); err == nil {
		t.Fatal("wrong verifier accepted status")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManagedRuntimeStatus(path, verifier); !errors.Is(err, ErrStatusIO) {
		t.Fatalf("insecure mode error=%v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "target"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManagedRuntimeStatus(path, verifier); !errors.Is(err, ErrStatusIO) {
		t.Fatalf("symlink error=%v", err)
	}
	unknown := append([]byte(nil), data[:len(data)-1]...)
	unknown = append(unknown, []byte(`,"unknown":1}`)...)
	if _, err := DecodeManagedRuntimeStatus(unknown); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("unknown field error=%v", err)
	}
	duplicate := append([]byte(nil), data[:len(data)-1]...)
	duplicate = append(duplicate, []byte(`,"proof":"`+status.Proof+`"}`)...)
	if _, err := DecodeManagedRuntimeStatus(duplicate); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("duplicate field error=%v", err)
	}
}

func TestManagedRuntimeStatusRejectsBoundsNonregularAndInsecureParents(t *testing.T) {
	verifier := mustVerifier(t, 'q')
	fence := validFence()
	status, err := SignManagedRuntimeStatus(verifier, NewManagedRuntimeStatus(fence,
		ProcessIdentity{PID: int(fence.PiPID), StartMarker: fence.PiStartMarker, Incarnation: fence.PiIncarnation},
		ProcessIdentity{PID: 991, StartMarker: "outer", Incarnation: uuid1}, time.Unix(1700000000, 123).UTC()))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	deep := strings.Repeat("[", MaxJSONDepth+1) + "true" + strings.Repeat("]", MaxJSONDepth+1)
	if _, err := DecodeManagedRuntimeStatus([]byte(deep)); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("deep status error=%v", err)
	}
	if _, err := DecodeManagedRuntimeStatus(append(append([]byte(nil), data...), bytesOf(' ', MaxStatusBytes)...)); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("oversized status error=%v", err)
	}

	directory := t.TempDir()
	path := filepath.Join(directory, "status.json")
	if err := PublishManagedRuntimeStatus(path, status, verifier); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManagedRuntimeStatus(path, verifier); !errors.Is(err, ErrStatusIO) {
		t.Fatalf("directory status error=%v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0777); err != nil {
		t.Fatal(err)
	}
	if err := PublishManagedRuntimeStatus(path, status, verifier); !errors.Is(err, ErrStatusIO) {
		t.Fatalf("insecure parent error=%v", err)
	}
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	// /etc is root-owned and mode 0755. The secure loader must reject it
	// before treating its regular file contents as a runtime descriptor.
	if _, err := ReadManagedRuntimeStatus("/etc/hosts", verifier); !errors.Is(err, ErrStatusIO) {
		t.Fatalf("wrong owner/mode error=%v", err)
	}
}

func TestManagedRuntimeStatusSubstitutionRaceAndExactCleanup(t *testing.T) {
	verifier := mustVerifier(t, 'q')
	fence := validFence()
	expected, err := SignManagedRuntimeStatus(verifier, NewManagedRuntimeStatus(fence,
		ProcessIdentity{PID: int(fence.PiPID), StartMarker: fence.PiStartMarker, Incarnation: fence.PiIncarnation},
		ProcessIdentity{PID: 991, StartMarker: "outer", Incarnation: uuid1}, time.Unix(1700000000, 123).UTC()))
	if err != nil {
		t.Fatal(err)
	}
	alternate := expected
	alternate.BridgeInstance = uuid2
	alternate.Fence.BridgeInstance = uuid2
	alternate, err = SignManagedRuntimeStatus(verifier, alternate)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "status.json")
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := PublishManagedRuntimeStatus(path, expected, verifier); err != nil {
		t.Fatal(err)
	}
	if err := PublishManagedRuntimeStatus(outside, expected, verifier); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(directory, "replacement.json")
	if err := PublishManagedRuntimeStatus(replacement, alternate, verifier); err != nil {
		t.Fatal(err)
	}
	// A signed replacement with a different bridge identity cannot satisfy an
	// exact removal request, and the replacement remains available.
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if err := RemoveManagedRuntimeStatus(context.Background(), path, expected, verifier); !errors.Is(err, ErrStatusProof) {
		t.Fatalf("replacement cleanup error=%v", err)
	}
	if _, err := ReadManagedRuntimeStatus(path, verifier); err != nil {
		t.Fatalf("replacement was removed or unreadable: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := PublishManagedRuntimeStatus(path, expected, verifier); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	started := make(chan struct{})
	go func() {
		close(started)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = os.Remove(path)
			_ = os.Symlink(outside, path)
			_ = os.Remove(path)
			_ = os.WriteFile(path, mustJSON(expected), 0600)
		}
	}()
	<-started
	for index := 0; index < 128; index++ {
		got, readErr := ReadManagedRuntimeStatus(path, verifier)
		if readErr == nil && got != expected {
			t.Fatalf("substitution returned unexpected status=%+v", got)
		}
		if readErr != nil && !errors.Is(readErr, ErrStatusIO) && !errors.Is(readErr, ErrStatusProof) {
			t.Fatalf("unexpected race read error=%v", readErr)
		}
	}
	close(stop)
	if err := os.Remove(path); err == nil {
		// The attacker may have left a regular file; both outcomes are safe.
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func mustJSON(status ManagedRuntimeStatus) []byte {
	data, err := json.Marshal(status)
	if err != nil {
		panic(err)
	}
	return data
}

func TestManagedCleanupDistinguishesGroupInspectionAndSignalErrors(t *testing.T) {
	inspectionErr := errors.New("injected group inspection failure")
	identity := processIdentity{PID: 42, StartMarker: "start-42", Incarnation: uuid1}
	closed := make(chan struct{})
	close(closed)
	baseOps := processOSOps{
		kill:        func(int, syscall.Signal) error { return nil },
		getpgid:     func(int) (int, error) { return 42, nil },
		startMarker: func(int) (string, error) { return identity.StartMarker, nil },
		listGroup:   func(int) ([]int, error) { return nil, inspectionErr },
	}
	process := &childProcess{identity: identity, processGroupID: 42, managedGroup: true, groupOwned: true, ops: baseOps, waitDone: closed}
	if err := process.cleanupGroup(context.Background(), closedError(), syscall.SIGKILL); !errors.Is(err, inspectionErr) {
		t.Fatalf("inspection error=%v", err)
	}
	signalErr := errors.New("injected member signal failure")
	process.ops.listGroup = func(int) ([]int, error) { return []int{99}, nil }
	process.ops.kill = func(pid int, _ syscall.Signal) error {
		if pid == 99 {
			return signalErr
		}
		return nil
	}
	if err := process.cleanupGroup(context.Background(), closedError(), syscall.SIGKILL); !errors.Is(err, signalErr) {
		t.Fatalf("signal error=%v", err)
	}
}

func closedError() <-chan error {
	result := make(chan error)
	close(result)
	return result
}

func TestManagedStartupJoinsPublishAndStopFailures(t *testing.T) {
	publishErr := errors.New("injected startup publish failure")
	stopErr := errors.New("injected startup stop failure")
	config := processTestConfig(t, "term-ignore")
	config.ManagedProcessGroup = true
	config.StatusPath = filepath.Join(t.TempDir(), "status.json")
	verifier := mustVerifier(t, 'x')
	ops := processOSOps{
		kill: func(_ int, signal syscall.Signal) error {
			if signal == 0 {
				return nil
			}
			return stopErr
		},
		getpgid:     func(pid int) (int, error) { return os.Getpid(), nil },
		startMarker: processStartMarker,
		listGroup:   func(int) ([]int, error) { return nil, nil },
	}
	bridge, err := NewBridge(config, verifier, Options{
		processOps: ops,
		statusOps:  statusFileOps{rename: func(int, string, int, string) error { return publishErr }},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := bridge.Run(context.Background())
	if !errors.Is(got, ErrStatusIO) || !errors.Is(got, stopErr) {
		t.Fatalf("startup error=%v, want status and stop failures", got)
	}
}

func TestManagedStatusPathRejectsReservedBasenames(t *testing.T) {
	config := processTestConfig(t, "term-ignore")
	config.ManagedProcessGroup = true
	for _, name := range []string{statusLockName, statusTempPrefix + "fixed"} {
		config.StatusPath = filepath.Join(t.TempDir(), name)
		if err := config.validate(); !errors.Is(err, ErrReservedStatusPath) {
			t.Fatalf("basename %q error=%v", name, err)
		}
	}
}

func TestManagedPublishAndRemoveLockReplacement(t *testing.T) {
	verifier := mustVerifier(t, 'q')
	fence := validFence()
	expected, err := SignManagedRuntimeStatus(verifier, NewManagedRuntimeStatus(fence,
		ProcessIdentity{PID: int(fence.PiPID), StartMarker: fence.PiStartMarker, Incarnation: fence.PiIncarnation},
		ProcessIdentity{PID: 991, StartMarker: "outer", Incarnation: uuid1}, time.Unix(1700000000, 123).UTC()))
	if err != nil {
		t.Fatal(err)
	}
	replacement := expected
	replacement.BridgeInstance = uuid2
	replacement.Fence.BridgeInstance = uuid2
	replacement, err = SignManagedRuntimeStatus(verifier, replacement)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "status.json")
	if err := PublishManagedRuntimeStatus(path, expected, verifier); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	publishDone := make(chan error, 1)
	ops := statusFileOps{rename: func(fromFD int, from string, toFD int, to string) error {
		close(entered)
		<-release
		return unix.Renameat(fromFD, from, toFD, to)
	}}
	go func() { publishDone <- publishManagedRuntimeStatusWithOps(path, replacement, verifier, ops) }()
	<-entered
	removeDone := make(chan error, 1)
	go func() { removeDone <- RemoveManagedRuntimeStatus(context.Background(), path, expected, verifier) }()
	close(release)
	if err := <-publishDone; err != nil {
		t.Fatal(err)
	}
	if err := <-removeDone; !errors.Is(err, ErrStatusProof) {
		t.Fatalf("remove error=%v", err)
	}
	if got, err := ReadManagedRuntimeStatus(path, verifier); err != nil || got != replacement {
		t.Fatalf("replacement status=%+v err=%v", got, err)
	}
}

func TestPublishPostRenameFsyncCleansExactDescriptor(t *testing.T) {
	verifier := mustVerifier(t, 'q')
	fence := validFence()
	status, err := SignManagedRuntimeStatus(verifier, NewManagedRuntimeStatus(fence,
		ProcessIdentity{PID: int(fence.PiPID), StartMarker: fence.PiStartMarker, Incarnation: fence.PiIncarnation},
		ProcessIdentity{PID: 991, StartMarker: "outer", Incarnation: uuid2}, time.Unix(1700000000, 123).UTC()))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "status.json")
	fsyncErr := errors.New("injected directory fsync failure")
	var calls atomic.Int32
	ops := statusFileOps{fsync: func(fd int) error {
		if calls.Add(1) == 1 {
			return fsyncErr
		}
		return unix.Fsync(fd)
	}}
	if err := publishManagedRuntimeStatusWithOps(path, status, verifier, ops); !errors.Is(err, fsyncErr) {
		t.Fatalf("publish error=%v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("published descriptor remains after cleanup: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("directory fsync calls=%d", calls.Load())
	}

	alwaysFail := errors.New("injected cleanup fsync failure")
	failedPath := filepath.Join(t.TempDir(), "status.json")
	if err := publishManagedRuntimeStatusWithOps(failedPath, status, verifier, statusFileOps{fsync: func(int) error { return alwaysFail }}); !errors.Is(err, alwaysFail) {
		t.Fatalf("failed cleanup error=%v", err)
	}
	if _, err := os.Stat(failedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("descriptor was not removed after failed cleanup: %v", err)
	}
}

func TestManagedStatusIsReadableBeforeFirstDial(t *testing.T) {
	directory := t.TempDir()
	statusPath := filepath.Join(directory, "status.json")
	cmd, input, ready, wait := startManagedHelperWithReady(t, "managed-status-hook", "managed-exit", "ws://127.0.0.1:1/v1/managed-bridge", statusPath)
	defer ready.Close()
	defer stopManagedHelper(t, cmd, input, wait)
	readyCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := waitForReady(readyCtx, ready); err != nil {
		t.Fatalf("status hook handshake: %v", err)
	}
	if err := waitHelper(t, wait); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statusPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status lifecycle stat=%v", err)
	}
}

func startManagedHelperWithReady(t *testing.T, mode, piMode, routerURL, statusPath string) (*exec.Cmd, io.WriteCloser, io.ReadCloser, <-chan error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestManagedBridgeHelper")
	cmd.Env = append(os.Environ(),
		"MANAGED_BRIDGE_HELPER=1",
		"MANAGED_BRIDGE_MODE="+mode,
		"MANAGED_BRIDGE_PI_MODE="+piMode,
		"MANAGED_BRIDGE_ROUTER_URL="+routerURL,
		"MANAGED_BRIDGE_STATUS_PATH="+statusPath,
	)
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	ready, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	wait := make(chan error, 1)
	go func() {
		wait <- cmd.Wait()
		close(wait)
	}()
	return cmd, input, ready, wait
}

func waitForReady(ctx context.Context, ready io.Reader) error {
	result := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(ready, []byte("status-observed\n"))
		result <- err
	}()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestManagedBridgeReconnectKeepsPiIdentityAndStopCleansStatus(t *testing.T) {
	verifier := mustVerifier(t, 'x')
	var connections atomic.Int32
	firstAccepted := make(chan Authenticate, 1)
	secondAccepted := make(chan Authenticate, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		conn, err := websocket.Accept(writer, request, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			return
		}
		connection := connections.Add(1)
		challenge, err := NewChallenge()
		if err != nil || conn.Write(context.Background(), websocket.MessageText, mustEncode(challenge)) != nil {
			_ = conn.Close(websocket.StatusInternalError, "")
			return
		}
		_, data, err := conn.Read(context.Background())
		if err != nil {
			return
		}
		auth, ok := mustDecode(data).(Authenticate)
		if !ok || VerifyAuthenticate(verifier, challenge, auth) != nil {
			return
		}
		accepted, err := AuthenticatedFor(verifier, challenge, auth, 0, 0)
		if err != nil || conn.Write(context.Background(), websocket.MessageText, mustEncode(accepted)) != nil {
			return
		}
		_, data, err = conn.Read(context.Background())
		if err != nil {
			return
		}
		if _, ok := mustDecode(data).(Resume); !ok {
			return
		}
		if connection == 1 {
			firstAccepted <- auth
			_ = conn.Close(websocket.StatusGoingAway, "network loss")
			return
		}
		secondAccepted <- auth
		for {
			if _, _, err := conn.Read(context.Background()); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	statusPath := filepath.Join(t.TempDir(), "status.json")
	cmd, input, lines, wait := startManagedHelper(t, "managed-runtime", "term-ignore", wsURL(server.URL), statusPath)
	defer stopManagedHelper(t, cmd, input, wait)
	first := waitAuth(t, firstAccepted)
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("bridge exited after socket drop: %v", err)
	}
	var second Authenticate
	select {
	case second = <-secondAccepted:
	case <-time.After(3 * time.Second):
		t.Fatalf("managed bridge did not complete reconnect: connections=%d", connections.Load())
	}
	if first.Fence != second.Fence {
		t.Fatalf("fence changed across reconnect: first=%+v second=%+v", first.Fence, second.Fence)
	}
	status, err := ReadManagedRuntimeStatus(statusPath, verifier)
	if err != nil {
		t.Fatal(err)
	}
	if status.InnerPi.PID != int(first.Fence.PiPID) || status.InnerPi.StartMarker != first.Fence.PiStartMarker || status.InnerPi.Incarnation != first.Fence.PiIncarnation || status.Fence.PiSessionID != first.Fence.PiSessionID {
		t.Fatalf("Pi identity changed: status=%+v first=%+v", status.InnerPi, first.Fence)
	}
	if probeManagedMember(status.InnerPi, status.OuterBridge.PID) != processOwned {
		t.Fatalf("Pi process state=%v", probeManagedMember(status.InnerPi, status.OuterBridge.PID))
	}
	if group, err := syscall.Getpgid(status.InnerPi.PID); err != nil || group != status.OuterBridge.PID {
		t.Fatalf("Pi group=%d err=%v outer=%d", group, err, status.OuterBridge.PID)
	}
	if _, err := input.Write([]byte("stop")); err != nil {
		t.Fatal(err)
	}
	if line := waitHelperLine(t, lines, "bridge-stopped"); line != "bridge-stopped" {
		t.Fatalf("stop line=%q", line)
	}
	if err := waitHelper(t, wait); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statusPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status retained after verified stop: %v", err)
	}
}

func TestManagedBridgePiExitIsTerminalAndCleansDescendants(t *testing.T) {
	statusPath := filepath.Join(t.TempDir(), "status.json")
	cmd, input, lines, wait := startManagedHelper(t, "managed-runtime", "managed-exit", "ws://127.0.0.1:1/v1/managed-bridge", statusPath)
	defer stopManagedHelper(t, cmd, input, wait)
	if line := waitHelperLine(t, lines, "pi-exit-observed"); line != "pi-exit-observed" {
		t.Fatalf("Pi exit line=%q", line)
	}
	if err := waitHelper(t, wait); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statusPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status retained after Pi exit: %v", err)
	}
}

func TestManagedGroupStopReapsDescendantAndRejectsWrongGroup(t *testing.T) {
	for _, mode := range []string{"managed-descendant-stop", "managed-wrong-group"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=TestManagedBridgeHelper")
			cmd.Env = append(os.Environ(), "MANAGED_BRIDGE_HELPER=1", "MANAGED_BRIDGE_MODE="+mode)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("helper failed: %v output=%q", err, output)
			}
			if !strings.Contains(string(output), "managed-descendant-clean") && !strings.Contains(string(output), "managed-wrong-group-refused") {
				t.Fatalf("missing proof output=%q", output)
			}
		})
	}
}

func startManagedHelper(t *testing.T, mode, piMode, routerURL, statusPath string) (*exec.Cmd, io.WriteCloser, <-chan string, <-chan error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestManagedBridgeHelper")
	cmd.Env = append(os.Environ(),
		"MANAGED_BRIDGE_HELPER=1",
		"MANAGED_BRIDGE_MODE="+mode,
		"MANAGED_BRIDGE_PI_MODE="+piMode,
		"MANAGED_BRIDGE_ROUTER_URL="+routerURL,
		"MANAGED_BRIDGE_STATUS_PATH="+statusPath,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 16)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	wait := make(chan error, 1)
	go func() {
		wait <- cmd.Wait()
		close(wait)
	}()
	return cmd, input, lines, wait
}

func waitHelperLine(t *testing.T, lines <-chan string, want string) string {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("helper output closed before %q", want)
			}
			if line == want {
				return line
			}
		case <-deadline.C:
			t.Fatalf("helper did not emit %q", want)
		}
	}
}

func waitAuth(t *testing.T, accepted <-chan Authenticate) Authenticate {
	t.Helper()
	select {
	case auth := <-accepted:
		return auth
	case <-time.After(3 * time.Second):
		t.Fatal("managed bridge did not complete handshake")
		return Authenticate{}
	}
}

func waitHelper(t *testing.T, wait <-chan error) error {
	t.Helper()
	select {
	case err := <-wait:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("managed helper did not exit")
		return errors.New("helper timeout")
	}
}

func stopManagedHelper(t *testing.T, cmd *exec.Cmd, input io.WriteCloser, wait <-chan error) {
	t.Helper()
	_ = input.Close()
	select {
	case <-wait:
		return
	default:
	}
	pid := cmd.Process.Pid
	group, err := syscall.Getpgid(pid)
	if err == nil && group == pid {
		// The child was started in this exact group. Never signal a group
		// unless the kernel still proves that ownership; the test process is
		// never a member of it.
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	select {
	case <-wait:
	case <-time.After(3 * time.Second):
		t.Fatalf("managed helper cleanup timed out")
	}
}

func TestManagedChildGroupIsProvenInSubprocessAndStandaloneRemainsSeparate(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=TestManagedBridgeHelper")
	command.Env = append(os.Environ(), "MANAGED_BRIDGE_HELPER=1", "MANAGED_BRIDGE_MODE=managed-child")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("managed subprocess failed: %v output=%q", err, output)
	}
	standalone, err := startChild(context.Background(), processTestConfig(t, "term-ignore"))
	if err != nil {
		t.Fatal(err)
	}
	if group, err := syscall.Getpgid(standalone.identity.PID); err != nil || group != standalone.identity.PID {
		t.Fatalf("standalone child group=%d err=%v child=%d", group, err, standalone.identity.PID)
	}
	if err := standalone.stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
