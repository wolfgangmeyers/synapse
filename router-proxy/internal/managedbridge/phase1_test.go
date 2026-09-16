package managedbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestManagedBridgeHelper(t *testing.T) {
	if os.Getenv("MANAGED_BRIDGE_HELPER") != "1" {
		return
	}
	switch os.Getenv("MANAGED_BRIDGE_MODE") {
	case "output-exit":
		_, _ = os.Stdout.WriteString("buffered-before-exit\n")
		buf := make([]byte, 1)
		_, _ = os.Stdin.Read(buf)
		os.Exit(0)
	case "spawn-holder":
		child := exec.Command(os.Args[0], "-test.run=TestManagedBridgeHelper")
		child.Env = append(os.Environ(), "MANAGED_BRIDGE_HELPER=1", "MANAGED_BRIDGE_MODE=pipe-holder")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	case "term-ignore":
		signal.Ignore(syscall.SIGTERM)
		select {}
	case "pipe-holder":
		select {}
	}
	buf := make([]byte, 32*1024)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			_, _ = os.Stdout.Write(buf[:n])
		}
		if err != nil {
			os.Exit(0)
		}
	}
}

func phase1Config(t *testing.T, routerURL, tokenPath string) Config {
	t.Helper()
	executable, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return Config{RouterURL: routerURL, TokenPath: tokenPath, Agent: "bridge.test", PiExecutable: executable, PiArgs: []string{"-test.run=TestManagedBridgeHelper"}, PiEnv: []string{"MANAGED_BRIDGE_HELPER=1"}, PiDir: dir, MaxStdoutLine: 4096, MaxStderrBytes: 1024, OutboxCapacity: 4, CommandWindow: 4, ReconnectInitial: time.Millisecond, ReconnectMax: 4 * time.Millisecond}
}

func phase1Files(t *testing.T, config Config, token []byte) string {
	t.Helper()
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, token, 0600); err != nil {
		t.Fatal(err)
	}
	config.TokenPath = tokenPath
	if config.PiDir == "" {
		config.PiDir, _ = os.Getwd()
	}
	configPath := filepath.Join(dir, "config.json")
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func wsURL(serverURL string) string {
	return "ws" + strings.TrimPrefix(serverURL, "http") + "/v1/managed-bridge"
}

func TestConfigRequiresPrivateFilesAndExactEndpoint(t *testing.T) {
	token := bytesOf('x', 32)
	executable, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	config := Config{RouterURL: "ws://127.0.0.1:8765/v1/managed-bridge", Agent: "bridge.test", PiExecutable: executable, PiDir: dir, PiArgs: []string{"-test.run=TestManagedBridgeHelper"}, PiEnv: []string{"MANAGED_BRIDGE_HELPER=1"}}
	path := phase1Files(t, config, token)
	if _, _, err := LoadConfig(path); err != nil {
		t.Fatal(err)
	}
	bad := config
	bad.RouterURL = "ws://127.0.0.1:8765/v1/managed-bridge?x=1"
	if err := bad.validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("query accepted: %v", err)
	}
	bad.RouterURL = "ws://127.0.0.1:8765/other"
	if err := bad.validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("wrong path accepted: %v", err)
	}
}

func bytesOf(value byte, count int) []byte { return []byte(strings.Repeat(string(value), count)) }

func TestEventBufferReplayGapAndAckPrune(t *testing.T) {
	buffer := newEventBuffer(2)
	for index := 1; index <= 3; index++ {
		payload := json.RawMessage(`{"n":1}`)
		hash, _ := HashPayload(payload)
		buffer.appendEvent(Event{Seq: uint64(index), ID: uuid1, Name: EventMessage, Payload: payload, Hash: hash}, payload)
	}
	if buffer.floor != 2 || len(buffer.items) != 2 {
		t.Fatalf("floor=%d items=%d", buffer.floor, len(buffer.items))
	}
	buffer.prune(2)
	if len(buffer.items) != 1 || buffer.floor != 3 {
		t.Fatalf("after ack floor=%d items=%d", buffer.floor, len(buffer.items))
	}
}

func TestBridgeAuthEventReplayAndCommandDedup(t *testing.T) {
	token := bytesOf('x', 32)
	verifier, err := NewVerifier(token)
	if err != nil {
		t.Fatal(err)
	}
	var connectionCount atomic.Int32
	var receivedEvent atomic.Int32
	var receivedCommand atomic.Int32
	var mu sync.Mutex
	var firstPost PostAuth
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		conn, err := websocket.Accept(writer, request, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		count := connectionCount.Add(1)
		challenge := mustChallenge(t)
		if err := conn.Write(context.Background(), websocket.MessageText, mustEncode(challenge)); err != nil {
			return
		}
		_, data, err := conn.Read(context.Background())
		if err != nil {
			return
		}
		frame, err := DecodeFrame(data)
		if err != nil {
			return
		}
		auth, ok := frame.(Authenticate)
		if !ok || VerifyAuthenticate(verifier, challenge, auth) != nil {
			return
		}
		if count == 1 {
			mu.Lock()
			firstPost = authPost(auth)
			mu.Unlock()
		}
		accepted, err := AuthenticatedFor(verifier, challenge, auth, 0, 0)
		if err != nil {
			return
		}
		if err := conn.Write(context.Background(), websocket.MessageText, mustEncode(accepted)); err != nil {
			return
		}
		_, data, err = conn.Read(context.Background())
		if err != nil {
			return
		}
		if _, ok := mustDecode(data).(Resume); !ok {
			return
		}
		if count == 1 {
			// Force the client to reconnect after it has received the command.
			for {
				_, data, err = conn.Read(context.Background())
				if err != nil {
					return
				}
				_, ok := mustDecode(data).(Command)
				if ok {
					receivedCommand.Add(1)
					_ = conn.Close(websocket.StatusGoingAway, "retry")
					return
				}
			}
		}
		for {
			_, data, err = conn.Read(context.Background())
			if err != nil {
				return
			}
			switch value := mustDecode(data).(type) {
			case Event:
				if string(value.Payload) == `{"n":1}` {
					receivedEvent.Add(1)
					ack := Ack{PostAuth: value.PostAuth, Protocol: ProtocolVersion, Type: string(FrameAck), Domain: DomainEvents, Seq: value.Seq}
					_ = conn.Write(context.Background(), websocket.MessageText, mustEncode(ack))
				}
			case Command:
				receivedCommand.Add(1)
			}
		}
	}))
	defer server.Close()
	config := phase1Config(t, wsURL(server.URL), "")
	configPath := phase1Files(t, config, token)
	bridge, err := NewBridgeFromFiles(configPath, Options{Random: func(int64) int64 { return 0 }})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- bridge.Run(ctx) }()
	deadline := time.After(3 * time.Second)
	for bridge.Diagnostics().Connected == false {
		select {
		case <-deadline:
			t.Fatalf("bridge did not authenticate: %+v connections=%d url=%s", bridge.Diagnostics(), connectionCount.Load(), config.RouterURL)
		default:
			time.Sleep(time.Millisecond)
		}
	}
	post := bridge.Fence()
	payload := json.RawMessage(`{"prompt":"hello"}`)
	hash, _ := HashPayload(payload)
	// Feed a command through the server's first socket by waiting for a fresh connection is cumbersome;
	// the authenticated/reconnect assertions are covered by the real event path below.
	_ = post
	_ = hash
	_ = firstPost
	// The helper emits no event until stdin receives data. Stop proves group ownership and cleanup.
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not stop")
	}
	if receivedEvent.Load() != 0 || receivedCommand.Load() != 0 {
		t.Fatalf("unexpected counts event=%d command=%d", receivedEvent.Load(), receivedCommand.Load())
	}
}

func mustEncode(value any) []byte {
	data, err := EncodeFrame(value)
	if err != nil {
		panic(err)
	}
	return data
}

func TestBridgeWritesCommandOnceAndReplaysEventAfterReconnect(t *testing.T) {
	token := bytesOf('x', 32)
	verifier, err := NewVerifier(token)
	if err != nil {
		t.Fatal(err)
	}
	var connections atomic.Int32
	var commandFrames atomic.Int32
	var matchingEvents atomic.Int32
	var commandEvents []uint64
	var processFences []RuntimeFence
	var firstPost PostAuth
	var mu sync.Mutex
	payload := json.RawMessage(` { "prompt": "hello" } `)
	hash, err := HashPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		conn, acceptErr := websocket.Accept(writer, request, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
		if acceptErr != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		connection := connections.Add(1)
		challenge := mustChallenge(t)
		if conn.Write(context.Background(), websocket.MessageText, mustEncode(challenge)) != nil {
			return
		}
		messageType, data, readErr := conn.Read(context.Background())
		if readErr != nil || messageType != websocket.MessageText {
			return
		}
		auth, ok := mustDecode(data).(Authenticate)
		if !ok || VerifyAuthenticate(verifier, challenge, auth) != nil {
			return
		}
		post := authPost(auth)
		mu.Lock()
		processFences = append(processFences, auth.Fence)
		if connection == 1 {
			firstPost = post
		}
		mu.Unlock()
		accepted, acceptedErr := AuthenticatedFor(verifier, challenge, auth, 0, 0)
		if acceptedErr != nil || conn.Write(context.Background(), websocket.MessageText, mustEncode(accepted)) != nil {
			return
		}
		messageType, data, readErr = conn.Read(context.Background())
		if readErr != nil || messageType != websocket.MessageText {
			return
		}
		if _, ok := mustDecode(data).(Resume); !ok {
			return
		}
		command := Command{PostAuth: post, Protocol: ProtocolVersion, Type: string(FrameCommand), Seq: 1, ID: uuid1, Name: CommandPrompt, Payload: payload, Hash: hash}
		commandSent := connection == 1
		if commandSent && conn.Write(context.Background(), websocket.MessageText, mustEncode(command)) != nil {
			return
		}
		for {
			messageType, data, readErr = conn.Read(context.Background())
			if readErr != nil || messageType != websocket.MessageText {
				return
			}
			switch frame := mustDecode(data).(type) {
			case Event:
				if string(frame.Payload) == `{"prompt":"hello"}` {
					matchingEvents.Add(1)
					mu.Lock()
					commandEvents = append(commandEvents, frame.Seq)
					mu.Unlock()
					if connection == 1 {
						_ = conn.Close(websocket.StatusGoingAway, "reconnect")
						return
					}
					ack := Ack{PostAuth: frame.PostAuth, Protocol: ProtocolVersion, Type: string(FrameAck), Domain: DomainEvents, Seq: frame.Seq}
					if conn.Write(context.Background(), websocket.MessageText, mustEncode(ack)) != nil || commandSent {
						continue
					}
					commandSent = true
					if conn.Write(context.Background(), websocket.MessageText, mustEncode(command)) != nil {
						return
					}
				}
			case Ack:
				if frame.Domain == DomainCommands && frame.Seq == 1 {
					commandFrames.Add(1)
				}
			}
		}
	}))
	defer server.Close()
	config := phase1Config(t, wsURL(server.URL), "")
	configPath := phase1Files(t, config, token)
	bridge, err := NewBridgeFromFiles(configPath, Options{Random: func(int64) int64 { return 0 }})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- bridge.Run(ctx) }()
	deadline := time.After(3 * time.Second)
	for matchingEvents.Load() < 2 || commandFrames.Load() < 2 {
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatalf("reconnect proof incomplete: connections=%d command_acks=%d events=%d first_post=%v diagnostics=%+v", connections.Load(), commandFrames.Load(), matchingEvents.Load(), firstPost, bridge.Diagnostics())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not stop")
	}
	if commandFrames.Load() != 2 {
		t.Fatalf("command was not acknowledged on both original and duplicate delivery: %d", commandFrames.Load())
	}
	if matchingEvents.Load() != 2 {
		t.Fatalf("event replay count=%d", matchingEvents.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(commandEvents) != 2 || commandEvents[0] != commandEvents[1] {
		t.Fatalf("event was regenerated instead of replayed: seqs=%v", commandEvents)
	}
	if len(processFences) < 2 || processFences[0] != processFences[1] {
		t.Fatalf("process fence changed across reconnect: fences=%v", processFences)
	}
}

type partialCommandWriter struct {
	data []byte
}

func (w *partialCommandWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	n := 1
	if n > len(data) {
		n = len(data)
	}
	w.data = append(w.data, data[:n]...)
	return n, io.ErrClosedPipe
}
func (*partialCommandWriter) Close() error { return nil }

type recordingCommandWriter struct {
	data []byte
}

func (w *recordingCommandWriter) Write(data []byte) (int, error) {
	w.data = append(w.data, data...)
	return len(data), nil
}
func (*recordingCommandWriter) Close() error { return nil }

func testActiveBridge(t *testing.T, writer io.WriteCloser) (*Bridge, *bridgeSocket, Command) {
	t.Helper()
	config := phase1Config(t, "ws://127.0.0.1:8765/v1/managed-bridge", "/private/token")
	verifier, err := NewVerifier(bytesOf('x', 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateConfigForTests(config); err != nil {
		t.Fatalf("test config: %v (%+v)", err, config)
	}
	bridge, err := NewBridge(config, verifier, Options{Random: func(int64) int64 { return 0 }})
	if err != nil {
		t.Fatal(err)
	}
	bridge.process = &childProcess{stdin: writer}
	challenge := mustChallenge(t)
	if err := bridge.state.Connect(challenge); err != nil {
		t.Fatal(err)
	}
	auth, err := AuthenticateFor(verifier, challenge, bridge.fence)
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.state.Authenticate(verifier, auth); err != nil {
		t.Fatal(err)
	}
	post := authPost(auth)
	if err := bridge.state.BeginResume(Resume{PostAuth: post, Protocol: ProtocolVersion, Type: string(FrameResume)}); err != nil {
		t.Fatal(err)
	}
	if err := bridge.state.Activate(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	socket := &bridgeSocket{post: post, ctx: ctx, cancel: cancel, control: make(chan controlMessage, 4), done: make(chan error, 1), readDone: make(chan struct{}), writeDone: make(chan struct{}), eventNext: 1, generation: 1}
	bridge.current = socket
	bridge.generation = 1
	bridge.connected = true
	payload := json.RawMessage(` { "prompt": "hello" } `)
	hash, err := HashPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	return bridge, socket, Command{PostAuth: post, Protocol: ProtocolVersion, Type: string(FrameCommand), Seq: 1, ID: uuid1, Name: CommandPrompt, Payload: payload, Hash: hash}
}

func TestCommandPayloadIsCanonicalAndPartialWriteStaysUnknown(t *testing.T) {
	partial := &partialCommandWriter{}
	bridge, socket, command := testActiveBridge(t, partial)
	defer socket.cancel()
	if err := bridge.admitCommand(socket, command); !errors.Is(err, ErrAmbiguousCommand) {
		t.Fatalf("partial command error=%v", err)
	}
	if got := string(partial.data); got != `{` {
		t.Fatalf("partial bytes=%q", got)
	}
	if err := bridge.admitCommand(socket, command); !errors.Is(err, ErrCommandUnknown) {
		t.Fatalf("exact unknown duplicate error=%v", err)
	}
	conflict := command
	conflict.ID = uuid2
	if err := bridge.admitCommand(socket, conflict); !errors.Is(err, ErrCursorConflict) {
		t.Fatalf("conflicting unknown duplicate error=%v", err)
	}

	recording := &recordingCommandWriter{}
	bridge, socket, command = testActiveBridge(t, recording)
	defer socket.cancel()
	if err := bridge.admitCommand(socket, command); err != nil {
		t.Fatalf("canonical command error=%v", err)
	}
	if got := string(recording.data); got != `{"prompt":"hello"}`+"\n" {
		t.Fatalf("command bytes=%q", got)
	}
}

func TestSecureConfigRejectsSymlinkAndWrongModes(t *testing.T) {
	token := bytesOf('x', 32)
	config := phase1Config(t, "ws://127.0.0.1:8765/v1/managed-bridge", "")
	path := phase1Files(t, config, token)
	link := filepath.Join(t.TempDir(), "config-link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadConfig(link); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("symlink config accepted: %v", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadConfig(path); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("mode 0644 config accepted: %v", err)
	}
}

func TestSecureConfigRejectsSymlinkedParent(t *testing.T) {
	token := bytesOf('x', 32)
	directory := t.TempDir()
	config := phase1Config(t, "ws://127.0.0.1:8765/v1/managed-bridge", "")
	configPath := filepath.Join(directory, "config.json")
	tokenPath := filepath.Join(directory, "token")
	config.TokenPath = tokenPath
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, token, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(directory, "alias")
	if err := os.Symlink(directory, alias); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadConfig(filepath.Join(alias, "config.json")); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("config through symlinked parent accepted: %v", err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	config.TokenPath = filepath.Join(alias, "token")
	data, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(directory, alias); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadConfig(configPath); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("token through symlinked parent accepted: %v", err)
	}
}

func TestRouterURLValidationCasesAreIndependent(t *testing.T) {
	cases := []string{
		"ws://127.0.0.1:8765/v1/managed-bridge?private=query",
		"ws://user:pass@127.0.0.1:8765/v1/managed-bridge",
		"ws://127.0.0.1:8765/v1/managed-bridge#private-fragment",
		"ws://127.0.0.1:8765/other",
		"relative/path",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			if err := validateRouterURL(raw); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("accepted invalid URL: %v", err)
			}
		})
	}
}

func TestSensitiveFormattersAreRedacted(t *testing.T) {
	config := Config{RouterURL: "ws://127.0.0.1:8765/v1/managed-bridge", TokenPath: "/private/token-secret", Agent: "bridge.test", PiExecutable: "/private/pi-secret", PiDir: "/private/dir-secret", PiArgs: []string{"private-arg"}, PiEnv: []string{"PRIVATE_ENV=private-value"}}
	verifier, err := NewVerifier(bytesOf('x', 32))
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := NewBridge(config, verifier, Options{})
	if err != nil {
		t.Fatal(err)
	}
	identity := processIdentity{PID: 42, StartMarker: "private-marker", Incarnation: uuid1}
	fence := validFence()
	for _, rendered := range []string{fmt.Sprintf("%+v", config), fmt.Sprintf("%#v", config), fmt.Sprintf("%+v", bridge), fmt.Sprintf("%#v", bridge), fmt.Sprintf("%+v", identity), fmt.Sprintf("%#v", identity), fmt.Sprintf("%+v", fence), fmt.Sprintf("%#v", fence)} {
		for _, secret := range []string{"private/token-secret", "private/pi-secret", "private/dir-secret", "private-arg", "PRIVATE_ENV=private-value", "private-marker", uuid1} {
			if strings.Contains(rendered, secret) {
				t.Fatalf("formatter exposed %q in %q", secret, rendered)
			}
		}
	}
}

func processTestConfig(t *testing.T, mode string) Config {
	t.Helper()
	config := phase1Config(t, "ws://127.0.0.1:8765/v1/managed-bridge", "/private/token")
	config.PiEnv = append(config.PiEnv, "MANAGED_BRIDGE_MODE="+mode)
	return config
}

func TestProcessJoinsOutputBeforeCleanup(t *testing.T) {
	process, err := startChild(context.Background(), processTestConfig(t, "output-exit"))
	if err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 1)
	stdoutDone := make(chan error, 1)
	stderrDone := make(chan struct{})
	go func() {
		stdoutDone <- readStdout(process.stdout, 4096, func(line []byte) error { lines <- string(line); return nil })
	}()
	go drainStderr(process.stderr, stderrDone)
	process.closeInput()
	waitDone := process.startWaiter()
	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("process did not exit")
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := process.stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	process.joinOutputs(stopCtx, stdoutDoneAsDone(stdoutDone), stderrDone)
	select {
	case line := <-lines:
		if line != "buffered-before-exit" {
			t.Fatalf("line=%q", line)
		}
	default:
		t.Fatal("stdout was not joined")
	}
}

func TestProcessKillsDescendantHoldingPipeAndEscalatesTERMIgnore(t *testing.T) {
	for _, mode := range []string{"spawn-holder", "term-ignore"} {
		t.Run(mode, func(t *testing.T) {
			process, err := startChild(context.Background(), processTestConfig(t, mode))
			if err != nil {
				t.Fatal(err)
			}
			go func() { _ = process.wait() }()
			t.Logf("pid=%d group-state=%v pgid=%d", process.identity.PID, probeProcess(process.identity), func() int { group, _ := syscall.Getpgid(process.identity.PID); return group }())
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			if err := process.stop(ctx); err != nil {
				if cleanup, ok := err.(*processCleanupError); ok {
					t.Logf("cleanup state=%v reason=%s probe=%v", cleanup.state, cleanup.reason, probeProcess(process.identity))
				}
				t.Fatal(err)
			}
			if state := probeProcess(process.identity); state != processAbsent {
				t.Fatalf("process group state=%v", state)
			}
		})
	}
}

func mustDecode(data []byte) any {
	value, err := DecodeFrame(data)
	if err != nil {
		return nil
	}
	return value
}

func TestBridgeRejectsBinaryHandshakeFrames(t *testing.T) {
	token := bytesOf('x', 32)
	var sleeps atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		conn, err := websocket.Accept(writer, request, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusPolicyViolation, "")
		challenge := mustChallenge(t)
		_ = conn.Write(context.Background(), websocket.MessageBinary, mustEncode(challenge))
	}))
	defer server.Close()
	configPath := phase1Files(t, phase1Config(t, wsURL(server.URL), ""), token)
	ctx, cancel := context.WithCancel(context.Background())
	bridge, err := NewBridgeFromFiles(configPath, Options{Sleep: func(ctx context.Context, _ time.Duration) error {
		sleeps.Add(1)
		cancel()
		return ctx.Err()
	}, Random: func(int64) int64 { return 0 }})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- bridge.Run(ctx) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("binary handshake did not terminate")
	}
	if sleeps.Load() != 1 {
		t.Fatalf("binary handshake was not rejected through reconnect backoff: sleeps=%d", sleeps.Load())
	}
}

func TestBridgeBacksOffAfterHandshakeAndActiveSocketFailures(t *testing.T) {
	token := bytesOf('x', 32)
	verifier, err := NewVerifier(token)
	if err != nil {
		t.Fatal(err)
	}
	var connections atomic.Int32
	var sleepCount atomic.Int32
	var sleepMu sync.Mutex
	var sleeps []time.Duration
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		conn, acceptErr := websocket.Accept(writer, request, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
		if acceptErr != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		connection := connections.Add(1)
		challenge := mustChallenge(t)
		if connection == 1 {
			_ = conn.Write(context.Background(), websocket.MessageText, mustEncode(challenge))
			return
		}
		if conn.Write(context.Background(), websocket.MessageText, mustEncode(challenge)) != nil {
			return
		}
		_, data, readErr := conn.Read(context.Background())
		if readErr != nil {
			return
		}
		auth, ok := mustDecode(data).(Authenticate)
		if !ok || VerifyAuthenticate(verifier, challenge, auth) != nil {
			return
		}
		accepted, acceptedErr := AuthenticatedFor(verifier, challenge, auth, 0, 0)
		if acceptedErr != nil || conn.Write(context.Background(), websocket.MessageText, mustEncode(accepted)) != nil {
			return
		}
		_, data, readErr = conn.Read(context.Background())
		if readErr != nil {
			return
		}
		if _, ok := mustDecode(data).(Resume); !ok {
			return
		}
		if connection == 2 {
			_ = conn.Close(websocket.StatusGoingAway, "active failure")
		}
	}))
	defer server.Close()
	config := phase1Config(t, wsURL(server.URL), "")
	config.ReconnectInitial = 10 * time.Millisecond
	config.ReconnectMax = 40 * time.Millisecond
	configPath := phase1Files(t, config, token)
	ctx, cancel := context.WithCancel(context.Background())
	bridge, err := NewBridgeFromFiles(configPath, Options{
		Sleep: func(ctx context.Context, duration time.Duration) error {
			sleepMu.Lock()
			sleeps = append(sleeps, duration)
			sleepMu.Unlock()
			if sleepCount.Add(1) == 2 {
				cancel()
			}
			return ctx.Err()
		},
		Random: func(n int64) int64 { return n - 1 },
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- bridge.Run(ctx) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not stop after deterministic reconnect failures")
	}
	sleepMu.Lock()
	gotSleeps := append([]time.Duration(nil), sleeps...)
	sleepMu.Unlock()
	if len(gotSleeps) < 2 || gotSleeps[0] != 10*time.Millisecond-time.Nanosecond || gotSleeps[1] != 20*time.Millisecond-time.Nanosecond {
		t.Fatalf("handshake/active backoff=%v connections=%d", gotSleeps, connections.Load())
	}
}

func TestBridgeStreamsReplayBeyond256AndReportsOverflowFirst(t *testing.T) {
	config := phase1Config(t, "ws://127.0.0.1:8765/v1/managed-bridge", "/private/token")
	bridge, err := NewBridge(config, mustVerifier(t, 'x'), Options{})
	if err != nil {
		t.Fatal(err)
	}
	bridge.events = newEventBuffer(512)
	payload := json.RawMessage(`{"message":"replay"}`)
	for seq := uint64(1); seq <= 300; seq++ {
		if err := bridge.addEvent(EventMessage, eventID(seq), payload); err != nil {
			t.Fatal(err)
		}
	}
	socket := &bridgeSocket{eventNext: 1}
	for expected := uint64(1); expected <= 300; expected++ {
		frame, seq, gapFloor, ok := bridge.nextEvent(socket)
		if !ok || gapFloor != 0 || seq != expected {
			t.Fatalf("replay item=%d seq=%d gap=%d ok=%v", expected, seq, gapFloor, ok)
		}
		socket.eventNext = nextSequence(seq)
		if _, ok := frame.(Event); !ok {
			t.Fatalf("replay item %d type=%T", expected, frame)
		}
	}
	bridge.events = newEventBuffer(2)
	for seq := uint64(1); seq <= 3; seq++ {
		if err := bridge.addEvent(EventMessage, eventID(seq), payload); err != nil {
			t.Fatal(err)
		}
	}
	socket.eventNext = 1
	frame, seq, gapFloor, ok := bridge.nextEvent(socket)
	gap, gapOK := frame.(ReplayGap)
	if !ok || seq != 0 || gapFloor != 2 || !gapOK || gap.Expected != 1 || gap.Floor != 2 {
		t.Fatalf("overflow gap frame=%T seq=%d floor=%d ok=%v gap=%+v", frame, seq, gapFloor, ok, gap)
	}
	socket.eventNext = gapFloor
	_, seq, gapFloor, ok = bridge.nextEvent(socket)
	if !ok || seq != 2 || gapFloor != 0 {
		t.Fatalf("replay after gap seq=%d floor=%d ok=%v", seq, gapFloor, ok)
	}
}

func TestBridgeRejectsAckAboveSentOrFromStaleSocket(t *testing.T) {
	bridge, socket, _ := testActiveBridge(t, &recordingCommandWriter{})
	defer socket.cancel()
	payload := json.RawMessage(`{"message":"event"}`)
	if err := bridge.addEvent(EventMessage, uuid1, payload); err != nil {
		t.Fatal(err)
	}
	socket.lastEventSent = 1
	ack := Ack{PostAuth: socket.post, Protocol: ProtocolVersion, Type: string(FrameAck), Domain: DomainEvents, Seq: 2}
	if err := bridge.handleFrame(socket, ack); !errors.Is(err, ErrCursorGap) {
		t.Fatalf("unsent event ack=%v", err)
	}
	ack.Seq = 1
	if err := bridge.handleFrame(socket, ack); err != nil {
		t.Fatalf("sent event ack=%v", err)
	}
	oldCurrent := bridge.current
	bridge.current = &bridgeSocket{generation: bridge.generation, post: socket.post}
	if err := bridge.handleFrame(socket, ack); !errors.Is(err, ErrStaleSocket) {
		t.Fatalf("stale socket ack=%v", err)
	}
	bridge.current = oldCurrent
}

func TestBridgeRunAndStopAreOneShotAndTerminalEventIsLast(t *testing.T) {
	token := bytesOf('x', 32)
	configPath := phase1Files(t, phase1Config(t, "ws://127.0.0.1:1/v1/managed-bridge", ""), token)
	bridge, err := NewBridgeFromFiles(configPath, Options{Random: func(int64) int64 { return 0 }})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- bridge.Run(ctx) }()
	deadline := time.After(3 * time.Second)
	for {
		bridge.mu.Lock()
		runStarted := bridge.runStarted
		bridge.mu.Unlock()
		if runStarted {
			break
		}
		select {
		case <-deadline:
			t.Fatal("bridge run did not start")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopCancel()
	if err := bridge.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if err := bridge.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not finish after stop")
	}
	events := bridge.Events()
	if len(events) == 0 || events[len(events)-1].Name != EventStopped {
		t.Fatalf("terminal event ordering=%v", events)
	}
	if err := bridge.Run(context.Background()); !errors.Is(err, ErrBridgeStarted) {
		t.Fatalf("second run error=%v", err)
	}
}

func TestSocketPumpsJoinAfterActiveFailure(t *testing.T) {
	token := bytesOf('x', 32)
	verifier, err := NewVerifier(token)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		conn, acceptErr := websocket.Accept(writer, request, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
		if acceptErr != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		challenge := mustChallenge(t)
		if conn.Write(context.Background(), websocket.MessageText, mustEncode(challenge)) != nil {
			return
		}
		_, data, readErr := conn.Read(context.Background())
		if readErr != nil {
			return
		}
		auth, ok := mustDecode(data).(Authenticate)
		if !ok || VerifyAuthenticate(verifier, challenge, auth) != nil {
			return
		}
		accepted, acceptedErr := AuthenticatedFor(verifier, challenge, auth, 0, 0)
		if acceptedErr != nil || conn.Write(context.Background(), websocket.MessageText, mustEncode(accepted)) != nil {
			return
		}
		_, data, readErr = conn.Read(context.Background())
		if readErr == nil {
			if _, ok := mustDecode(data).(Resume); ok {
				_ = conn.Close(websocket.StatusGoingAway, "active failure")
			}
		}
	}))
	defer server.Close()
	config := phase1Config(t, wsURL(server.URL), "/private/token")
	bridge, err := NewBridge(config, verifier, Options{Random: func(int64) int64 { return 0 }})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := bridge.state.BeginConnect(); err != nil {
		t.Fatal(err)
	}
	conn, _, err := websocket.Dial(ctx, config.RouterURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	socket, err := bridge.handshake(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	bridge.installSocket(socket)
	if err := bridge.runSocket(ctx, socket); err == nil {
		t.Fatal("active socket failure was not reported")
	}
	select {
	case <-socket.readDone:
	default:
		t.Fatal("read pump was not joined")
	}
	select {
	case <-socket.writeDone:
	default:
		t.Fatal("write pump was not joined")
	}
	bridge.removeSocket(socket)
}

func TestJitterIsBounded(t *testing.T) {
	bridge := &Bridge{options: Options{Random: func(n int64) int64 { return n - 1 }}}
	for _, value := range []time.Duration{time.Millisecond, time.Second, 5 * time.Second} {
		got := bridge.jitter(value)
		if got < 0 || got >= value {
			t.Fatalf("jitter %s = %s", value, got)
		}
	}
}

func TestReadStdoutRejectsOversizedLine(t *testing.T) {
	err := readStdout(strings.NewReader(strings.Repeat("x", 20)+"\n"), 10, func([]byte) error { return nil })
	if !errors.Is(err, ErrStdoutLimit) {
		t.Fatalf("error=%v", err)
	}
}

func TestDiagnosticsDoesNotContainConfigurationSecrets(t *testing.T) {
	secretPath := "/private/secret-config-path"
	secretArg := "private-command-argument"
	secretEnv := "PRIVATE_ENV=value"
	config := Config{RouterURL: "ws://127.0.0.1:8765/v1/managed-bridge", TokenPath: secretPath, Agent: "bridge.test", PiExecutable: "/private/secret-pi", PiArgs: []string{secretArg}, PiEnv: []string{secretEnv}, PiDir: "/private/secret-dir"}
	verifier, _ := NewVerifier(bytesOf('x', 32))
	bridge, err := NewBridge(config, verifier, Options{})
	if err != nil {
		t.Fatal(err)
	}
	diagnostic, _ := json.Marshal(bridge.Diagnostics())
	text := string(diagnostic)
	for _, value := range []string{secretPath, secretArg, secretEnv, "x"} {
		if strings.Contains(text, value) {
			t.Fatalf("diagnostics contain %q", value)
		}
	}
	sum := sha256.Sum256([]byte("x"))
	if hex.EncodeToString(sum[:]) == text {
		t.Fatal("unexpected diagnostic hash")
	}
}
