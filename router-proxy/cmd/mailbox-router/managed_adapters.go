package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func managedProcessFactory() managed.ProcessFactory { return managed.DefaultProcessFactory() }
func managedRPCFactory() managed.RPCFactory         { return managed.DefaultRPCFactory() }

func validateManagedStartup(config managed.Config) error {
	return managed.ValidateManagedLaunchConfig(config)
}

func parseManagedRoots(value string) []string {
	var roots []string
	for _, candidate := range strings.Split(value, ",") {
		if strings.TrimSpace(candidate) != "" {
			roots = append(roots, strings.TrimSpace(candidate))
		}
	}
	return roots
}

// fixedAuthHelper starts the project-owned helper with no shell and a closed
// child environment. The auth-store path is ordinary Pi's local credential
// file; it is never logged, persisted, or returned through HTTP.
type fixedAuthHelper struct {
	config         managed.Config
	nodeExecutable string
	ownedCodex     bool // Codex status and rotation belong to the configured Go store.
}

func (h fixedAuthHelper) Start(ctx context.Context, operationID, provider, action string) (managed.ProviderAuthRun, error) {
	if h.config.AuthHelperPath == "" || operationID == "" || provider == "" || action == "" {
		return nil, errors.New("provider helper configuration is incomplete")
	}
	if h.nodeExecutable == "" {
		return nil, errors.New("provider helper runtime is unavailable")
	}
	command := exec.Command(h.nodeExecutable, h.config.AuthHelperPath)
	command.Env = h.environment()
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
	run := &helperRun{stdin: stdin, command: command, events: make(chan managed.ProviderAuthEvent, 4), done: make(chan struct{})}
	// Stderr is deliberately drained and discarded. Provider SDK output cannot
	// become a router diagnostic or a secret-bearing log sink.
	go func() { _, _ = io.Copy(io.Discard, io.LimitReader(stderr, 128*1024)) }()
	go run.read(stdout)
	if err := run.write(map[string]string{"operation": action, "operation_id": operationID, "provider": provider}); err != nil {
		run.Cancel()
		return nil, err
	}
	go func() {
		select {
		case <-ctx.Done():
			run.Cancel()
		case <-run.done:
		}
	}()
	return run, nil
}

// Snapshot invokes the same fixed helper without an interactive provider
// operation. Only its bounded status/catalog result crosses this boundary; a
// parse or runtime failure stays a generic unavailable state.
func (h fixedAuthHelper) Snapshot(ctx context.Context) ([]store.ManagedProviderStatus, []store.ManagedProviderModel, error) {
	if h.config.AuthHelperPath == "" || h.nodeExecutable == "" {
		return nil, nil, errors.New("provider status is unavailable")
	}
	command := exec.CommandContext(ctx, h.nodeExecutable, h.config.AuthHelperPath)
	command.Env = h.environment()
	command.Stdin = strings.NewReader(`{"operation":"models"}` + "\n")
	stdout := &boundedHelperOutput{limit: 128 * 1024}
	command.Stdout = stdout
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return nil, nil, errors.New("provider status is unavailable")
	}
	providers, models, ok := decodeHelperSnapshot(stdout.Bytes())
	if !ok {
		return nil, nil, errors.New("provider status is unavailable")
	}
	return providers, models, nil
}

// consumeCodexCredential feeds ordinary RPC Pi's existing AuthStorage contract.
// Only a private pipe carries tokens. Reused on browser login/logout and every
// Go-owned rotation, not a one-time embedded credential import.
func (h fixedAuthHelper) consumeCodexCredential(ctx context.Context, credential *store.CodexCredential) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var value any
	if credential != nil {
		value = map[string]any{"type": "oauth", "access": credential.AccessToken, "refresh": credential.RefreshToken, "expires": credential.ExpiresAt.UnixMilli(), "accountId": credential.AccountID}
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > 64*1024 || h.nodeExecutable == "" || h.config.AuthHelperPath == "" {
		return errors.New("provider credential synchronization failed")
	}
	command := exec.CommandContext(ctx, h.nodeExecutable, filepath.Join(filepath.Dir(h.config.AuthHelperPath), "codex-consumer.mjs"))
	command.Env = h.environment()
	command.Stdin = bytes.NewReader(append(raw, '\n'))
	output := &boundedHelperOutput{limit: 128}
	command.Stdout = output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil || string(output.Bytes()) != "{\"synchronized\":true}\n" {
		return errors.New("provider credential synchronization failed")
	}
	return nil
}

func (h fixedAuthHelper) environment() []string {
	environment := append(managed.ChildEnvironment(h.config), "MECHA_PI_AUTH_PATH="+filepath.Join(h.config.PiAgentDir, "auth.json"))
	if h.ownedCodex {
		environment = append(environment, "MECHA_ROUTER_OWNS_CODEX=true")
	}
	return environment
}

type boundedHelperOutput struct {
	bytes.Buffer
	limit int
}

func (b *boundedHelperOutput) Write(value []byte) (int, error) {
	if b.Len()+len(value) > b.limit {
		return 0, errors.New("provider helper output is too large")
	}
	return b.Buffer.Write(value)
}

func decodeHelperSnapshot(raw []byte) ([]store.ManagedProviderStatus, []store.ManagedProviderModel, bool) {
	if len(raw) == 0 || len(raw) > 128*1024 {
		return nil, nil, false
	}
	var wire struct {
		Providers []store.ManagedProviderStatus `json:"providers"`
		Models    []store.ManagedProviderModel  `json:"models"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, nil, false
	}
	return wire.Providers, wire.Models, true
}

type helperRun struct {
	stdin   io.WriteCloser
	command *exec.Cmd
	events  chan managed.ProviderAuthEvent
	done    chan struct{}
	mu      sync.Mutex
	closed  bool
	waitErr error
}

func (r *helperRun) Events() <-chan managed.ProviderAuthEvent { return r.events }

func (r *helperRun) write(value any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("provider helper is closed")
	}
	line, err := json.Marshal(value)
	if err != nil || len(line) > 8*1024 {
		return errors.New("provider helper input is invalid")
	}
	_, err = r.stdin.Write(append(line, '\n'))
	return err
}

func (r *helperRun) Respond(ctx context.Context, promptID, value string) error {
	if len([]byte(value)) > 8*1024 {
		return errors.New("provider response is too large")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.write(map[string]string{"type": "response", "prompt_id": promptID, "value": value})
}

func (r *helperRun) Cancel() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	_ = r.stdin.Close()
	if r.command.Process != nil {
		_ = r.command.Process.Kill()
	}
	r.mu.Unlock()
}

func (r *helperRun) Wait() error {
	<-r.done
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.waitErr
}

func (r *helperRun) read(stdout io.Reader) {
	defer close(r.events)
	defer close(r.done)
	scanner := bufio.NewScanner(io.LimitReader(stdout, 128*1024))
	scanner.Buffer(make([]byte, 4*1024), 64*1024)
	for scanner.Scan() {
		event, ok := decodeHelperEvent(scanner.Bytes())
		if !ok {
			r.Cancel()
			break
		}
		r.events <- event
	}
	r.mu.Lock()
	r.closed = true
	if r.command != nil {
		r.waitErr = r.command.Wait()
	}
	r.mu.Unlock()
}

type helperWireEvent struct {
	Type        string                        `json:"type"`
	OperationID string                        `json:"operation_id"`
	Provider    string                        `json:"provider"`
	Status      string                        `json:"status"`
	Error       string                        `json:"error"`
	Prompt      *helperWirePrompt             `json:"prompt"`
	Providers   []store.ManagedProviderStatus `json:"providers"`
	Models      []store.ManagedProviderModel  `json:"models"`
}

type helperWirePrompt struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Message     string `json:"message"`
	Placeholder string `json:"placeholder"`
	URL         string `json:"url"`
}

// safeProviderPromptURL admits only the ChatGPT OAuth endpoint needed to
// complete sign-in. Every other helper-owned string remains untrusted display
// data, so an injected child cannot turn the router UI into an open redirect.
func safeProviderPromptURL(provider, raw string) string {
	if provider != "openai-codex" || raw == "" || len(raw) > 2048 {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "auth.openai.com") || parsed.Port() != "" || parsed.User != nil || parsed.Fragment != "" {
		return ""
	}
	return parsed.String()
}

func decodeHelperEvent(raw []byte) (managed.ProviderAuthEvent, bool) {
	if len(raw) == 0 || len(raw) > 64*1024 {
		return managed.ProviderAuthEvent{}, false
	}
	var wire helperWireEvent
	if json.Unmarshal(raw, &wire) != nil || wire.OperationID == "" || wire.Provider == "" || (wire.Type != "prompt" && wire.Type != "state" && wire.Type != "terminal") {
		return managed.ProviderAuthEvent{}, false
	}
	if (wire.Type == "state" && wire.Status != "pending") || (wire.Type == "terminal" && wire.Status != "succeeded" && wire.Status != "failed") {
		return managed.ProviderAuthEvent{}, false
	}
	// Treat every helper-owned string as untrusted. The fixed helper should
	// already sanitize it, but a malformed or compromised child must not turn
	// provider text, stderr, or a secret into a browser frame or DB diagnostic.
	event := managed.ProviderAuthEvent{Kind: wire.Type, OperationID: wire.OperationID, Provider: wire.Provider, Status: wire.Status, Providers: wire.Providers, Models: wire.Models}
	if wire.Error != "" {
		event.Error = "Provider sign-in did not complete"
	}
	if wire.Prompt != nil {
		if wire.Prompt.ID == "" || len(wire.Prompt.ID) > 128 || (wire.Prompt.Kind != "text" && wire.Prompt.Kind != "secret" && wire.Prompt.Kind != "select" && wire.Prompt.Kind != "manual_code") {
			return managed.ProviderAuthEvent{}, false
		}
		event.Prompt = &managed.ProviderAuthPrompt{ID: wire.Prompt.ID, Kind: wire.Prompt.Kind, Message: "Enter the requested sign-in value.", URL: safeProviderPromptURL(wire.Provider, wire.Prompt.URL)}
	}
	if event.Kind == "prompt" && event.Prompt == nil {
		return managed.ProviderAuthEvent{}, false
	}
	return event, true
}
