package transport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

// TestMain is also the entry point for the deterministic direct child used by
// TestManagedLaunchBehavioralFunctional. The child speaks only the Pi JSON-RPC
// boundary; no model or external service is involved.
func TestMain(m *testing.M) {
	if os.Getenv("MECHA_MANAGED_FUNCTIONAL_FAKE_PI") == "1" {
		runManagedFunctionalFakePi()
		return
	}
	os.Exit(m.Run())
}

type managedFunctionalEvent struct {
	Kind string            `json:"kind"`
	Type string            `json:"type,omitempty"`
	ID   string            `json:"id,omitempty"`
	Text string            `json:"text,omitempty"`
	Cwd  string            `json:"cwd,omitempty"`
	PID  int               `json:"pid,omitempty"`
	Args []string          `json:"args,omitempty"`
	Env  map[string]string `json:"env,omitempty"`
}

func runManagedFunctionalFakePi() {
	fifo := os.Getenv("MECHA_FUNCTIONAL_EVENT_FIFO")
	events, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	if err != nil {
		return
	}
	defer events.Close()
	emit := func(event managedFunctionalEvent) {
		encoded, marshalErr := json.Marshal(event)
		if marshalErr == nil {
			_, _ = fmt.Fprintln(events, string(encoded))
		}
	}
	args := append([]string(nil), os.Args...)
	sessionID := ""
	for index := 0; index+1 < len(args); index++ {
		if args[index] == "--session-id" {
			sessionID = args[index+1]
		}
	}
	environment := map[string]string{}
	for _, value := range os.Environ() {
		key, item, ok := strings.Cut(value, "=")
		if ok {
			environment[key] = item
		}
	}
	emit(managedFunctionalEvent{Kind: "started", Cwd: mustFunctionalCwd(), PID: os.Getpid(), Args: args, Env: environment})
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	activeTools := strings.Split(os.Getenv("MECHA_FUNCTIONAL_ACTIVE_TOOLS"), ",")
	commands := []string{
		"mecha-bootstrap-v1", "mecha-install-tools-v1", "mecha-control-result-v1", "mecha-mailbox-deliver-v1",
		"mecha-policy-apply-v1", "mecha-child-snapshot-v1", "mecha-post-compaction-notification-v1",
		"goal", "goal-lock", "goal-snooze", "goal-snooze-turn-limit", "goal-snooze-budget", "goal-snooze-budget-refill",
		"mecha-parent-message", "inline-tools", "mecha-reply",
	}
	for {
		var request map[string]any
		if err := decoder.Decode(&request); err != nil {
			return
		}
		id, _ := request["id"].(string)
		typ, _ := request["type"].(string)
		emit(managedFunctionalEvent{Kind: "rpc", Type: typ, ID: id})
		switch typ {
		case "get_state":
			_ = encoder.Encode(map[string]any{"id": id, "type": "response", "sessionId": sessionID})
		case "get_commands":
			_ = encoder.Encode(map[string]any{"id": id, "type": "response", "commands": commands})
		case "get_active_tools":
			_ = encoder.Encode(map[string]any{"id": id, "type": "response", "active_tools": activeTools})
		case "prompt":
			message, _ := request["message"].(string)
			emit(managedFunctionalEvent{Kind: "prompt", Text: message})
			if strings.HasPrefix(message, "/") {
				_ = encoder.Encode(map[string]any{"id": id, "type": "response", "command": "prompt", "success": true})
				writeFunctionalReceipt(encoder, message, activeTools, sessionID)
				continue
			}
			emit(managedFunctionalEvent{Kind: "initial_prompt", Text: message})
			// Hold the response until the test has read the event and inspected
			// SQLite. This makes the readiness-before-prompt assertion causal.
			if barrier := os.Getenv("MECHA_FUNCTIONAL_PROMPT_BARRIER"); barrier != "" {
				if gate, gateErr := os.Open(barrier); gateErr == nil {
					_, _ = io.ReadFull(gate, make([]byte, 1))
					_ = gate.Close()
				}
			}
			_ = encoder.Encode(map[string]any{"id": id, "type": "response", "command": "prompt", "success": true})
		default:
			_ = encoder.Encode(map[string]any{"id": id, "type": "response", "success": true})
		}
	}
}

func functionalCanonicalPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)
	return resolved
}

func mustFunctionalCwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "<cwd-error>"
	}
	return cwd
}

func writeFunctionalReceipt(encoder *json.Encoder, message string, activeTools []string, sessionID string) {
	parts := strings.SplitN(message, " ", 2)
	if len(parts) != 2 {
		return
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return
	}
	var envelope map[string]any
	if json.Unmarshal(decoded, &envelope) != nil {
		return
	}
	protocol, _ := envelope["protocol"].(string)
	runtimeGeneration, _ := envelope["runtime_generation"].(string)
	epoch, _ := envelope["session_epoch"].(float64)
	manifestHash, _ := envelope["manifest_sha256"].(string)
	details := map[string]any{"runtime_generation": runtimeGeneration, "session_epoch": epoch}
	customType := ""
	switch protocol {
	case "mecha.bootstrap/v1":
		customType = "mecha.tools.accepted.v1"
		details["manifest_revision"] = 2
		details["manifest_sha256"] = manifestHash
		values := make([]any, len(activeTools))
		for index, value := range activeTools {
			values[index] = value
		}
		details["active_tools"] = values
	case "mecha.policy/v1":
		customType = "mecha.policy.applied.v1"
		details["protocol"] = "mecha.policy/v1"
		details["applied"] = map[string]any{"inline_enabled": false, "inline_credits": map[string]any{}, "recall_content": "", "settled_enabled": false, "goal_lock_enabled": false, "compaction_deferred": false}
	case "mecha.child-snapshot/v1":
		customType = "mecha.child.snapshot.v1"
		details = map[string]any{"protocol": "mecha.children/v1", "snapshot_id": "functional-snapshot", "owner_session_id": sessionID, "sequence": 0, "runs": []any{}}
	}
	if customType == "" {
		return
	}
	_ = encoder.Encode(map[string]any{"type": "message_end", "customType": customType, "details": details})
}

type managedFunctionalFixture struct {
	t         *testing.T
	ctx       context.Context
	root      string
	home      string
	session   string
	fifo      string
	barrier   string
	events    chan managedFunctionalEvent
	durable   *store.Store
	router    *Router
	serverURL string
	closed    bool
}

func newManagedFunctionalFixture(t *testing.T) *managedFunctionalFixture {
	t.Helper()
	ctx := context.Background()
	root := functionalCanonicalPath(t, t.TempDir())
	home := functionalCanonicalPath(t, t.TempDir())
	sessionRoot := filepath.Join(home, "sessions")
	piAgentDir := filepath.Join(home, ".pi", "agent")
	require.NoError(t, os.MkdirAll(sessionRoot, 0o700))
	require.NoError(t, os.MkdirAll(piAgentDir, 0o700))
	manifestPath := filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json")
	manifest, _, err := managed.LoadManifest(manifestPath)
	require.NoError(t, err)
	active := []string{"read", "bash", "edit", "write", "subagent", "get_subagent_result", "steer_subagent"}
	for _, tool := range manifest.Tools {
		active = append(active, tool.Name)
	}
	sort.Strings(active)
	fifo := filepath.Join(t.TempDir(), "events.fifo")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	barrier := filepath.Join(t.TempDir(), "prompt-barrier.fifo")
	require.NoError(t, syscall.Mkfifo(barrier, 0o600))
	events := make(chan managedFunctionalEvent, 128)
	go readManagedFunctionalEvents(fifo, events)
	piExecutable, err := filepath.Abs(os.Args[0])
	require.NoError(t, err)
	config := managed.Config{
		Enabled: true, PiExecutable: piExecutable, ManagedHome: home, PiAgentDir: piAgentDir, SessionRoot: sessionRoot,
		RouterURL: "http://127.0.0.1:1", ManifestPath: manifestPath, MechaRPCExtension: manifestPath, PiSubagentsExtension: manifestPath,
		AllowedWorkingDirectoryRoots: []string{root}, CanonicalQuietRecipientID: "supervisor",
		TestOnlyEnvironment: []string{"MECHA_MANAGED_FUNCTIONAL_FAKE_PI=1", "MECHA_FUNCTIONAL_EVENT_FIFO=" + fifo, "MECHA_FUNCTIONAL_PROMPT_BARRIER=" + barrier, "MECHA_FUNCTIONAL_ACTIVE_TOOLS=" + strings.Join(active, ",")},
	}
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	manager, err := managed.NewManager(managed.ManagerOptions{
		Store: durable, Config: config, Manifest: manifest,
		CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Name: "GPT-5.6 Terra", Available: true}),
	})
	require.NoError(t, err)
	router, err := New(Options{Store: durable, Host: "127.0.0.1", Port: 0, Managed: manager})
	require.NoError(t, err)
	require.NoError(t, router.Listen(ctx))
	serverURL, _ := router.URLs()
	fixture := &managedFunctionalFixture{t: t, ctx: ctx, root: root, home: home, session: sessionRoot, fifo: fifo, barrier: barrier, events: events, durable: durable, router: router, serverURL: serverURL}
	t.Cleanup(func() { fixture.close() })
	return fixture
}

func readManagedFunctionalEvents(fifo string, events chan<- managedFunctionalEvent) {
	file, err := os.Open(fifo)
	if err != nil {
		close(events)
		return
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event managedFunctionalEvent
		if json.Unmarshal(scanner.Bytes(), &event) == nil {
			events <- event
		}
	}
	close(events)
}

func (f *managedFunctionalFixture) close() {
	if f == nil || f.closed {
		return
	}
	f.closed = true
	_ = f.router.Close(context.Background())
	_ = f.durable.Close()
	_ = os.Remove(f.fifo)
	_ = os.Remove(f.barrier)
}

func (f *managedFunctionalFixture) waitEvent(kind string) managedFunctionalEvent {
	f.t.Helper()
	select {
	case event := <-f.events:
		require.Equal(f.t, kind, event.Kind)
		return event
	case <-time.After(5 * time.Second):
		f.t.Fatalf("timed out waiting for fake Pi event %q", kind)
		return managedFunctionalEvent{}
	}
}

func managedFunctionalRequest(t *testing.T, method, url, requestID string, body []byte) (int, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, url, bytes.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	if requestID != "" {
		request.Header.Set("X-Request-ID", requestID)
	}
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	value, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, value
}

func TestManagedLaunchBehavioralFunctional(t *testing.T) {
	fixture := newManagedFunctionalFixture(t)
	body := []byte(fmt.Sprintf(`{"name":"alpha","cwd":%q,"initial_instruction":"hello from functional test"}`, fixture.root))
	requestID := "123e4567-e89b-42d3-a456-426614174081"
	status, firstBody := managedFunctionalRequest(t, http.MethodPost, fixture.serverURL+"/v1/managed-agents", requestID, body)
	require.Equal(t, http.StatusAccepted, status)
	started := fixture.waitEvent("started")
	require.Equal(t, fixture.root, started.Cwd)
	require.Equal(t, "openai-codex", argumentAfter(started.Args, "--provider"))
	require.Equal(t, "gpt-5.6-terra", argumentAfter(started.Args, "--model"))
	require.Equal(t, "--mode", started.Args[1])
	require.NotContains(t, strings.Join(started.Args, " "), "tmux")
	require.NotEqual(t, os.Getpid(), started.PID)
	groupID, err := syscall.Getpgid(started.PID)
	require.NoError(t, err)
	require.Equal(t, started.PID, groupID, "managed child must own its process group")
	for key, value := range map[string]string{"HOME": fixture.home, "MECHA_MANAGED_SESSION_ROOT": fixture.session, "MECHA_ROUTER_URL": fixture.serverURL} {
		require.Equal(t, value, started.Env[key], key)
	}
	require.Equal(t, "1", started.Env["MECHA_MANAGED_ACTIVE_ONLY"])
	require.Equal(t, "C", started.Env["LANG"])
	require.Equal(t, "C", started.Env["LC_ALL"])
	for _, key := range []string{"HOME", "PATH", "TMPDIR", "LANG", "LC_ALL", "PI_CODING_AGENT_DIR", "MECHA_MANAGED_SESSION_ROOT", "MECHA_MANAGED_ACTIVE_ONLY", "MECHA_ROUTER_URL", "MECHA_RPC_RUNTIME_GENERATION", "MECHA_RPC_SESSION_EPOCH", "MECHA_MANAGED_FUNCTIONAL_FAKE_PI", "MECHA_FUNCTIONAL_EVENT_FIFO", "MECHA_FUNCTIONAL_PROMPT_BARRIER", "MECHA_FUNCTIONAL_ACTIVE_TOOLS"} {
		require.Contains(t, started.Env, key)
	}
	for key := range started.Env {
		require.Contains(t, []string{"HOME", "PATH", "TMPDIR", "LANG", "LC_ALL", "PI_CODING_AGENT_DIR", "MECHA_MANAGED_SESSION_ROOT", "MECHA_MANAGED_ACTIVE_ONLY", "MECHA_ROUTER_URL", "MECHA_RPC_RUNTIME_GENERATION", "MECHA_RPC_SESSION_EPOCH", "MECHA_MANAGED_FUNCTIONAL_FAKE_PI", "MECHA_FUNCTIONAL_EVENT_FIFO", "MECHA_FUNCTIONAL_PROMPT_BARRIER", "MECHA_FUNCTIONAL_ACTIVE_TOOLS"}, key)
	}
	var rpcKinds []string
	var promptMessages []string
	initialPromptCount := 0
	for {
		select {
		case event := <-fixture.events:
			switch event.Kind {
			case "rpc":
				rpcKinds = append(rpcKinds, event.Type)
			case "prompt":
				promptMessages = append(promptMessages, event.Text)
			case "initial_prompt":
				initialPromptCount++
				require.Equal(t, "hello from functional test", event.Text)
				// The child emits this before answering the prompt. The durable row
				// must already be ready, while the private sent marker is still false.
				readyBeforePrompt, readErr := fixture.durable.ManagedAgent(fixture.ctx, "alpha")
				require.NoError(t, readErr)
				require.Equal(t, "running", readyBeforePrompt.LifecycleState)
				require.Equal(t, "idle", readyBeforePrompt.ActivityState)
				require.True(t, readyBeforePrompt.AcceptingPrompts)
				require.False(t, readyBeforePrompt.InitialInstructionSent)
				release, releaseErr := os.OpenFile(fixture.barrier, os.O_WRONLY, 0)
				require.NoError(t, releaseErr)
				_, releaseErr = release.Write([]byte{1})
				require.NoError(t, releaseErr)
				require.NoError(t, release.Close())
				goto initialObserved
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for the initial instruction")
		}
	}
initialObserved:
	require.Equal(t, []string{"get_state", "get_commands", "prompt", "prompt", "prompt", "prompt"}, rpcKinds)
	require.Equal(t, 1, initialPromptCount, "the fake Pi must receive the initial instruction exactly once")
	require.Len(t, promptMessages, 4)
	for _, message := range promptMessages[:3] {
		require.True(t, strings.HasPrefix(message, "/"), "readiness prompts must complete before the initial instruction")
	}
	require.Equal(t, "hello from functional test", promptMessages[3])
	status, replayBody := managedFunctionalRequest(t, http.MethodPost, fixture.serverURL+"/v1/managed-agents", requestID, body)
	require.Equal(t, http.StatusAccepted, status)
	require.Equal(t, firstBody, replayBody)
	status, projectionBody := managedFunctionalRequest(t, http.MethodGet, fixture.serverURL+"/v1/managed-agents/alpha", "", nil)
	require.Equal(t, http.StatusOK, status)
	var projection struct {
		Agent map[string]json.RawMessage `json:"agent"`
	}
	require.NoError(t, json.Unmarshal(projectionBody, &projection))
	t.Logf("projection: %s", projectionBody)
	var lifecycle, activity string
	require.NoError(t, json.Unmarshal(projection.Agent["lifecycle_state"], &lifecycle))
	require.NoError(t, json.Unmarshal(projection.Agent["activity_state"], &activity))
	require.Equal(t, "running", lifecycle)
	require.Equal(t, "idle", activity)
	require.Equal(t, "true", string(projection.Agent["accepting_prompts"]))
	_, hasPrivateInitialField := projection.Agent["initial_instruction_sent"]
	require.False(t, hasPrivateInitialField, "HTTP projection must omit the private initial-instruction record")
	stored, err := fixture.durable.ManagedAgent(fixture.ctx, "alpha")
	require.NoError(t, err)
	require.True(t, stored.InitialInstructionSent, "authoritative SQLite record must prove the initial instruction was sent")
	status, listBody := managedFunctionalRequest(t, http.MethodGet, fixture.serverURL+"/v1/managed-agents", "", nil)
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, string(listBody), `"lifecycle_state":"running"`)
	require.Contains(t, string(listBody), `"accepting_prompts":true`)
	childPID := started.PID
	require.Greater(t, childPID, 0)
	fixture.close()
	require.Eventually(t, func() bool { return processGroupAbsent(childPID) }, time.Second, time.Millisecond)
}

func argumentAfter(args []string, name string) string {
	for index := range args {
		if index+1 < len(args) && args[index] == name {
			return args[index+1]
		}
	}
	return ""
}

func processGroupAbsent(pid int) bool {
	if pid <= 0 {
		return true
	}
	err := syscall.Kill(-pid, 0)
	return errors.Is(err, syscall.ESRCH)
}
