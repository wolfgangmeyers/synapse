package managed

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	claudeCodeLineLimit   = 16 << 20
	claudeCodeStderrLimit = 64 << 10
	claudeCodeQueueCount  = 128
	claudeCodeQueueBytes  = 16 << 20
)

const (
	ClaudeMilestonePreflightAuth       = "preflight_auth"
	ClaudeMilestoneMCPGrantSetup       = "mcp_grant_setup"
	ClaudeMilestoneProcessSpawn        = "process_spawn"
	ClaudeMilestoneGateRelease         = "gate_release"
	ClaudeMilestoneControlProbe        = "control_probe"
	ClaudeMilestoneInitialPromptSubmit = "initial_prompt_submit"
)

// ClaudeCodeProcessConfig describes only the CLI boundary. The caller owns
// configuration files, checked persistence, process reuse and turn admission.
// Nil Environment inherits the host environment. Binary overrides
// MECHA_CLAUDE_BIN, then PATH and ~/.local/bin/claude are tried.
type ClaudeCodeProcessConfig struct {
	Adapter                         CLIAdapter
	Binary, Cwd, SessionID, Model   string
	MCPConfigFile, SystemPromptFile string
	OrphanRoot                      string      // managed private recovery root; empty only for standalone callers
	OnUnconfirmedStop               func(error) // closes only the owning manager pool; no filesystem dependency
	admitExternal                   func() (func(), error)
	releaseGate                     func(func() (int, error)) (int, error)
	BeforeRelease                   func() error // durable admission immediately before CLI exec
	waitForCleanup                  func(*exec.Cmd, ProcessIdentity, func()) error
	afterFailedRelease              func() error
	writeRelease                    func(*os.File) (int, error)
	RootAuthority                   bool // root uses only the checked router tool bridge
	ChildAuthority                  bool // child uses only its scoped tool bridge
	Resume                          bool
	Environment                     []string
}

// ClaudeCodeEvent preserves protocol identities and raw fields without SDK
// translation. The single Next consumer must commit echoed original input bytes
// before accepting later events. A stdin write never constitutes consumption.
type ClaudeCodeEvent struct {
	Type            string          `json:"type"`
	Subtype         string          `json:"subtype"`
	SessionID       string          `json:"session_id"`
	UUID            string          `json:"uuid"`
	IsReplay        bool            `json:"isReplay"`
	ParentToolUseID *string         `json:"parent_tool_use_id"`
	Message         json.RawMessage `json:"message"`
	Raw             json.RawMessage `json:"-"`
}
type claudeCodeControlResponse struct {
	Subtype   string          `json:"subtype"`
	RequestID string          `json:"request_id"`
	Response  json.RawMessage `json:"response"`
	Error     string          `json:"error"`
}

type claudeCodePendingInput struct {
	content json.RawMessage
	bytes   int
}

// ClaudeCodeProcess owns one long-lived process group. Next has one consumer;
// SendUser and Control may be concurrent. Interrupt awaits a receipt, not a
// turn result, and is deliberately distinct from Close.
type ClaudeCodeProcess struct {
	adapter             CLIAdapter
	orphanPath          string
	orphanIdentity      ProcessIdentity
	orphanCleanupOnce   sync.Once
	orphanCleanupErr    error
	ownedTasks          map[string]bool
	ownedTaskBytes      int
	cmd                 *exec.Cmd
	stdin               io.WriteCloser
	writeMu             sync.Mutex
	mu                  sync.Mutex
	events              []ClaudeCodeEvent
	eventBytes          int
	notify              chan struct{}
	controls            map[string]chan claudeCodeControlResponse
	inputs              map[string]claudeCodePendingInput
	inputBytes          int
	sequence            uint64
	protocolRecent      []string
	protocolReported    uint8
	protocolDiagnostics *claudeDiagnosticQueue
	err                 error
	stderr              []byte
	done                chan struct{}
	exited              chan struct{}
	onUnconfirmedStop   func(error)
	closeErr            error
	waitErr             error
	closeOnce           sync.Once
	closeDone           chan struct{}
	sessionID           string
}

func claudeCodeEnvironment(env []string) []string {
	if env == nil {
		env = os.Environ()
	}
	clean := make([]string, 0, len(env))
	for _, s := range env {
		k, _, _ := strings.Cut(s, "=")
		switch k {
		case "TMUX", "TMUX_PANE", "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT":
			continue
		}
		clean = append(clean, s)
	}
	return clean
}
func claudeCodeEnvValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		k, v, _ := strings.Cut(env[i], "=")
		if k == key {
			return v
		}
	}
	return ""
}
func resolveClaudeCodeBinary(cfg ClaudeCodeProcessConfig, env []string) (string, error) {
	candidate := cfg.Binary
	if candidate == "" {
		candidate = claudeCodeEnvValue(env, "MECHA_CLAUDE_BIN")
	}
	executable := func(p string) (string, error) {
		p, err := filepath.Abs(p)
		if err != nil {
			return "", err
		}
		st, err := os.Stat(p)
		if err != nil {
			return "", err
		}
		if !st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0 {
			return "", fmt.Errorf("not executable: %s", p)
		}
		return p, nil
	}
	if candidate != "" {
		if strings.ContainsRune(candidate, os.PathSeparator) {
			p, err := executable(candidate)
			if errors.Is(err, os.ErrNotExist) {
				return "", errors.Join(errClaudeExecutableMissing, err)
			}
			return p, err
		}
		var ambiguous error
		for _, dir := range filepath.SplitList(claudeCodeEnvValue(env, "PATH")) {
			if p, e := executable(filepath.Join(dir, candidate)); e == nil {
				return p, nil
			} else if !errors.Is(e, os.ErrNotExist) && ambiguous == nil {
				ambiguous = e
			}
		}
		if ambiguous != nil {
			return "", ambiguous
		}
		return "", fmt.Errorf("Claude executable %q not found: %w", candidate, errors.Join(errClaudeExecutableMissing, os.ErrNotExist))
	}
	var ambiguous error
	for _, dir := range filepath.SplitList(claudeCodeEnvValue(env, "PATH")) {
		if p, e := executable(filepath.Join(dir, "claude")); e == nil {
			return p, nil
		} else if !errors.Is(e, os.ErrNotExist) && ambiguous == nil {
			ambiguous = e
		}
	}
	home := claudeCodeEnvValue(env, "HOME")
	if home != "" {
		p, err := executable(filepath.Join(home, ".local", "bin", "claude"))
		if err == nil || !errors.Is(err, os.ErrNotExist) {
			return p, err
		}
	}
	if ambiguous != nil {
		return "", ambiguous
	}
	return "", fmt.Errorf("Claude executable not found: %w", errors.Join(errClaudeExecutableMissing, os.ErrNotExist))
}

// PreflightClaudeCode runs local `auth status --json` with a fifteen-second limit,
// bounded capture and scrubbed environment. No identity is retained or model called.
func PreflightClaudeCode(ctx context.Context, cfg ClaudeCodeProcessConfig) (string, error) {
	bin, err := preflightClaudeCodeInternal(ctx, cfg)
	if err != nil {
		return "", fmt.Errorf("startup failed at step '%s': %w", ClaudeMilestonePreflightAuth, err)
	}
	return bin, nil
}

func preflightClaudeCodeInternal(ctx context.Context, cfg ClaudeCodeProcessConfig) (string, error) {
	if cfg.admitExternal != nil {
		release, err := cfg.admitExternal()
		if err != nil {
			return "", err
		}
		defer release()
	}
	if cfg.Adapter != nil && cfg.Adapter.Provider() != claudeCodeProvider {
		bin, err := cfg.Adapter.Executable(ctx)
		if err != nil {
			return "", err
		}
		if err := cfg.Adapter.Preflight(ctx); err != nil {
			return "", err
		}
		return bin, nil
	}
	env := claudeCodeEnvironment(cfg.Environment)
	bin, err := resolveClaudeCodeBinary(cfg, env)
	if err != nil {
		category := claudeStartUnknown
		if errors.Is(err, os.ErrNotExist) {
			category = claudeStartSpawn
		}
		return "", claudeStartCategorize(category, fmt.Errorf("install Claude CLI and sign in on the host: %w", err))
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd, gate, err := claudeCodeGatedCommand(bin, []string{"auth", "status", "--json"})
	if err != nil {
		return "", err
	}
	defer gate.Close()
	defer cmd.ExtraFiles[0].Close()
	cmd.Dir = cfg.Cwd
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 250 * time.Millisecond
	out := &claudeCodeBoundedCapture{limit: claudeCodeStderrLimit}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	var orphanPath string
	if err = ctx.Err(); err == nil {
		orphanPath, err = startClaudeShortCommand(cmd, gate, cfg)
	}
	if err != nil {
		return "", fmt.Errorf("check Claude CLI sign-in on the host: %w", err)
	}
	err = waitClaudeShortCommand(ctx, cmd, cfg, orphanPath)
	var cleanupErr *claudeStartCleanupError
	if errors.As(err, &cleanupErr) {
		return "", err
	}
	var status struct {
		LoggedIn *bool `json:"loggedIn"`
	}
	if err != nil {
		return "", fmt.Errorf("Claude CLI auth status failed; check installation and sign in on the host: %w", err)
	}
	if out.overflow {
		return "", errors.New("Claude CLI auth status output exceeded the limit; check installation and sign in on the host")
	}
	if err := json.Unmarshal(out.data, &status); err != nil {
		return "", fmt.Errorf("Claude CLI auth status returned invalid JSON; check installation and sign in on the host: %w", err)
	}
	if status.LoggedIn == nil {
		return "", errors.New("Claude CLI auth status omitted loggedIn; check installation and sign in on the host")
	}
	if !*status.LoggedIn {
		return "", claudeStartCategorize(claudeStartAuth, errors.New("Claude CLI unavailable or signed out; run claude auth status and sign in on the host"))
	}
	return bin, nil
}

type claudeCodeBoundedCapture struct {
	data     []byte
	limit    int
	overflow bool
}

func (b *claudeCodeBoundedCapture) Write(v []byte) (int, error) {
	n := len(v)
	room := b.limit - len(b.data)
	if len(v) > room {
		v = v[:room]
		b.overflow = true
	}
	b.data = append(b.data, v...)
	return n, nil
}

// claudeCodeProcessArgs limits built-in tools while preserving role-specific MCP configuration.
func claudeCodeProcessArgs(cfg ClaudeCodeProcessConfig) []string {
	if cfg.Adapter != nil && cfg.Adapter.Provider() != claudeCodeProvider {
		return cfg.Adapter.BuildArgs(CLIProcessConfig{
			Binary:           cfg.Binary,
			Cwd:              cfg.Cwd,
			SessionID:        cfg.SessionID,
			Model:            cfg.Model,
			MCPConfigFile:    cfg.MCPConfigFile,
			SystemPromptFile: cfg.SystemPromptFile,
			RootAuthority:    cfg.RootAuthority,
			ChildAuthority:   cfg.ChildAuthority,
			Resume:           cfg.Resume,
			Environment:      cfg.Environment,
		}, cfg.MCPConfigFile)
	}
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--replay-user-messages", "--model", cfg.Model, "--permission-mode", "bypassPermissions", "--tools", "Bash,Read,Write,Edit,Glob,Grep,ToolSearch"}
	if cfg.RootAuthority || cfg.ChildAuthority {
		args = append(args, "--strict-mcp-config")
	}
	if cfg.MCPConfigFile != "" {
		args = append(args, "--mcp-config", cfg.MCPConfigFile)
	}
	if cfg.SystemPromptFile != "" {
		args = append(args, "--append-system-prompt-file", cfg.SystemPromptFile)
	}
	flag := "--session-id"
	if cfg.Resume {
		flag = "--resume"
	}
	args = append(args, flag, cfg.SessionID)
	return args
}

func StartClaudeCodeProcess(ctx context.Context, cfg ClaudeCodeProcessConfig) (*ClaudeCodeProcess, error) {
	if cfg.Cwd == "" || cfg.SessionID == "" || cfg.Model == "" {
		return nil, errors.New("Claude process requires cwd, session ID and model")
	}
	if cfg.Adapter == nil {
		cfg.Adapter = NewClaudeCodeAdapter(cfg.Binary, cfg.Environment)
	}
	bin, err := PreflightClaudeCode(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	args := claudeCodeProcessArgs(cfg)
	cmd, gate, err := claudeCodeGatedCommand(bin, args)
	if err != nil {
		return nil, err
	}
	defer gate.Close()
	defer cmd.ExtraFiles[0].Close()
	cmd.Dir = cfg.Cwd
	cmd.Env = cfg.Adapter.CleanEnvironment(cfg.Environment)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	startedOK := false
	var stdout, outw, stderr, errw *os.File
	defer func() {
		if !startedOK {
			_ = stdin.Close()
			for _, f := range []*os.File{stdout, outw, stderr, errw} {
				if f != nil {
					_ = f.Close()
				}
			}
		}
	}()
	// Explicit pipes ensure Wait does not close stdout before its final record.
	stdout, outw, err = os.Pipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	stderr, errw, err = os.Pipe()
	if err != nil {
		stdin.Close()
		stdout.Close()
		outw.Close()
		return nil, err
	}
	cmd.Stdout = outw
	cmd.Stderr = errw
	if err = cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		stderr.Close()
		outw.Close()
		errw.Close()
		return nil, fmt.Errorf("startup failed at step '%s': %w", ClaudeMilestoneProcessSpawn, claudeStartCategorize(claudeStartSpawn, err))
	}
	_ = cmd.ExtraFiles[0].Close()
	var orphanPath string
	marker, markerErr := processStartMarker(cmd.Process.Pid)
	orphanIdentity := ProcessIdentity{PID: cmd.Process.Pid, StartMarker: marker, Incarnation: claudeCodeNewInputID()}
	if markerErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, markerErr
	}
	if cfg.OrphanRoot != "" {
		orphanPath, err = persistClaudeCodeOrphan(cfg.OrphanRoot, cmd.Process.Pid, orphanIdentity.Incarnation)
		if err != nil {
			// This is our unreaped child, not a persisted/recycled PID.
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
			stdin.Close()
			stdout.Close()
			stderr.Close()
			outw.Close()
			errw.Close()
			cfg.blockUnconfirmedClaudeStop(err)
			return nil, fmt.Errorf("persist Claude orphan identity: %w", err)
		}
		record, readErr := readClaudeCodeOrphan(orphanPath)
		if readErr == nil {
			orphanIdentity = record.Identity
		}
	}
	if cfg.BeforeRelease != nil {
		if err = cfg.BeforeRelease(); err != nil {
			_ = cmd.Process.Kill()
			wait := cfg.waitForCleanup
			if wait == nil {
				wait = waitClaudeCodeCommandConfirmed
			}
			confirmed := false
			var removalErr error
			cleanupErr := wait(cmd, orphanIdentity, func() {
				confirmed = true
				if orphanPath != "" {
					removalErr = removeClaudeCodeOrphan(orphanPath, orphanIdentity)
				}
			})
			if !confirmed {
				cfg.blockUnconfirmedClaudeStop(errClaudeCodeGroupUnconfirmed)
				cleanupErr = errors.Join(errClaudeCodeGroupUnconfirmed, cleanupErr)
			} else if removalErr != nil {
				cfg.blockUnconfirmedClaudeStop(removalErr)
				cleanupErr = removalErr
			} else {
				cleanupErr = nil
			}
			return nil, errors.Join(err, cleanupErr)
		}
	}
	writeRelease := cfg.writeRelease
	if writeRelease == nil {
		writeRelease = func(f *os.File) (int, error) { return f.Write([]byte("ready\n")) }
	}
	release := func() (int, error) { return writeRelease(gate) }
	var n int
	var writeErr error
	if cfg.releaseGate != nil {
		n, writeErr = cfg.releaseGate(release)
	} else {
		n, writeErr = release()
	}
	if writeErr != nil || n != len("ready\n") {
		if writeErr == nil {
			writeErr = io.ErrShortWrite
		}
		_ = cmd.Process.Kill()
		confirmed := false
		var removalErr error
		cleanupErr := waitClaudeCodeCommandConfirmed(cmd, orphanIdentity, func() {
			confirmed = true
			if orphanPath != "" {
				removalErr = removeClaudeCodeOrphan(orphanPath, orphanIdentity)
			}
		})
		stdin.Close()
		stdout.Close()
		stderr.Close()
		outw.Close()
		errw.Close()
		if confirmed && removalErr != nil {
			cfg.blockUnconfirmedClaudeStop(removalErr)
			cleanupErr = removalErr
		} else if confirmed {
			cleanupErr = nil
		} else {
			cfg.blockUnconfirmedClaudeStop(errClaudeCodeGroupUnconfirmed)
		}
		if n == 0 && confirmed && cfg.afterFailedRelease != nil {
			if restoreErr := cfg.afterFailedRelease(); restoreErr != nil {
				return nil, fmt.Errorf("startup failed at step '%s': %w", ClaudeMilestoneGateRelease, errors.Join(writeErr, cleanupErr, restoreErr))
			}
		}
		return nil, fmt.Errorf("startup failed at step '%s': %w", ClaudeMilestoneGateRelease, errors.Join(writeErr, cleanupErr))
	}
	gate.Close()
	outw.Close()
	errw.Close()
	p := &ClaudeCodeProcess{adapter: cfg.Adapter, cmd: cmd, stdin: stdin, notify: make(chan struct{}, 1), controls: make(map[string]chan claudeCodeControlResponse), inputs: make(map[string]claudeCodePendingInput), done: make(chan struct{}), exited: make(chan struct{}), closeDone: make(chan struct{}), sessionID: cfg.SessionID}
	p.orphanPath, p.orphanIdentity = orphanPath, orphanIdentity
	p.onUnconfirmedStop = cfg.OnUnconfirmedStop
	reads := sync.WaitGroup{}
	reads.Add(2)
	go func() { defer reads.Done(); defer stdout.Close(); p.read(stdout) }()
	go func() {
		defer reads.Done()
		defer stderr.Close()
		buf := make([]byte, 8192)
		for {
			n, e := stderr.Read(buf)
			if n > 0 {
				p.mu.Lock()
				p.stderr = append(p.stderr, buf[:n]...)
				if len(p.stderr) > claudeCodeStderrLimit {
					p.stderr = append([]byte(nil), p.stderr[len(p.stderr)-claudeCodeStderrLimit:]...)
				}
				p.mu.Unlock()
			}
			if e != nil {
				return
			}
		}
	}()
	go func() {
		err := waitClaudeCodeCommandConfirmed(cmd, orphanIdentity, p.forgetClaudeOrphanConfirmed)
		err = errors.Join(err, p.orphanCleanupErr)
		if errors.Is(err, errClaudeCodeGroupUnconfirmed) {
			// The regular identity record remains durable recovery authority.
			// Stop same-manager admission even if every filesystem write fails.
			cfg.blockUnconfirmedClaudeStop(err)
			stdout.Close()
			stderr.Close()
		}
		close(p.exited)
		reads.Wait()
		if !errors.Is(err, errClaudeCodeGroupUnconfirmed) {
			p.forgetClaudeOrphanIfAbsent()
		}
		p.recordWaitError(err)
		p.mu.Lock()
		if p.err == nil {
			if err != nil {
				p.err = fmt.Errorf("Claude process exited: %w", err)
			} else {
				p.err = io.EOF
			}
		}
		p.inputs = make(map[string]claudeCodePendingInput)
		p.inputBytes = 0
		p.mu.Unlock()
		close(p.done)
		p.signal()
	}()
	startedOK = true
	return p, nil
}
func (p *ClaudeCodeProcess) recordWaitError(err error) {
	p.mu.Lock()
	p.waitErr = err
	p.mu.Unlock()
}
func (p *ClaudeCodeProcess) signal() {
	select {
	case p.notify <- struct{}{}:
	default:
	}
}
func (p *ClaudeCodeProcess) fail(err error) {
	p.mu.Lock()
	if p.err == nil {
		p.err = err
	}
	// Failed protocol state must never leak buffered output to persistence.
	p.events = nil
	p.eventBytes = 0
	p.mu.Unlock()
	p.signal()
	_ = (defaultProcessGroupOps{}).SignalVerifiedProcessGroup(p.orphanIdentity, syscall.SIGKILL)
}
func (p *ClaudeCodeProcess) read(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), claudeCodeLineLimit+1)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) > claudeCodeLineLimit {
			p.fail(errors.New("Claude stdout record exceeds 16 MiB"))
			return
		}
		var event ClaudeCodeEvent
		var control *claudeCodeControlResponse
		var err error
		if p.adapter != nil && p.adapter.Provider() != claudeCodeProvider {
			var norm NormalizedEvent
			norm, err = p.adapter.ParseLine(line, p.sessionID)
			if err == nil {
				event = ClaudeCodeEvent{
					Type:            norm.Type,
					Subtype:         norm.Subtype,
					SessionID:       norm.SessionID,
					UUID:            norm.UUID,
					IsReplay:        norm.IsReplay,
					ParentToolUseID: norm.ParentToolUseID,
					Message:         norm.Message,
					Raw:             norm.Raw,
				}
				if norm.Control != nil {
					control = &claudeCodeControlResponse{
						Subtype:   norm.Control.Subtype,
						RequestID: norm.Control.RequestID,
						Response:  norm.Control.Response,
						Error:     norm.Control.Error,
					}
				}
			}
		} else {
			event, control, err = parseClaudeCodeEvent(line, p.sessionID)
		}
		if err != nil {
			p.fail(err)
			return
		}
		if control != nil {
			p.mu.Lock()
			ch, ok := p.controls[control.RequestID]
			if ok {
				delete(p.controls, control.RequestID)
			}
			p.mu.Unlock()
			if !ok {
				p.fail(errors.New("Claude control response has unknown request_id"))
				return
			}
			ch <- *control
			continue
		}
		if err := p.observeNotificationOwnership(event); err != nil {
			p.fail(err)
			return
		}
		p.mu.Lock()
		if p.err != nil {
			p.mu.Unlock()
			return
		}
		p.recordProtocolEvent(event)
		if event.Type == "result" && len(p.inputs) > 0 {
			p.logProtocolDiagnostic(1, "result_with_pending_input")
		}
		if event.Type == "user" && event.IsReplay && event.ParentToolUseID == nil {
			var origin struct {
				Origin json.RawMessage `json:"origin"`
			}
			_ = json.Unmarshal(line, &origin)
			if len(origin.Origin) == 0 || string(origin.Origin) == "null" {
				pending, ok := p.inputs[event.UUID]
				var message struct {
					Content json.RawMessage `json:"content"`
				}
				_ = json.Unmarshal(event.Message, &message)
				if !ok || !claudeCodeJSONEqual(pending.content, message.Content) {
					if !ok {
						p.logProtocolDiagnostic(2, "replay_unknown_identity")
					} else {
						p.logProtocolDiagnostic(4, "replay_content_mismatch")
					}
					p.mu.Unlock()
					p.fail(errors.New("Claude replay input identity/content mismatch"))
					return
				}
				delete(p.inputs, event.UUID)
				p.inputBytes -= pending.bytes
			}
		}
		if len(p.events) >= claudeCodeQueueCount || p.eventBytes+len(event.Raw) > claudeCodeQueueBytes {
			p.mu.Unlock()
			p.fail(errors.New("Claude event buffer overflow"))
			return
		}
		p.events = append(p.events, event)
		p.eventBytes += len(event.Raw)
		p.mu.Unlock()
		p.signal()
	}
	if err := scanner.Err(); err != nil {
		p.fail(fmt.Errorf("Claude stdout: %w", err))
	}
}
func claudeCodeJSONEqual(a, b []byte) bool {
	var x, y interface{}
	decode := func(raw []byte, target *interface{}) error {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		return d.Decode(target)
	}
	if decode(a, &x) != nil || decode(b, &y) != nil {
		return false
	}
	xa, _ := json.Marshal(x)
	ya, _ := json.Marshal(y)
	return bytes.Equal(xa, ya)
}
func (p *ClaudeCodeProcess) Next(ctx context.Context) (ClaudeCodeEvent, error) {
	for {
		p.mu.Lock()
		err := p.err
		if len(p.events) > 0 {
			e := p.events[0]
			p.events[0] = ClaudeCodeEvent{}
			p.events = p.events[1:]
			p.eventBytes -= len(e.Raw)
			p.mu.Unlock()
			return e, nil
		}
		p.mu.Unlock()
		if err != nil {
			return ClaudeCodeEvent{}, err
		}
		select {
		case <-ctx.Done():
			return ClaudeCodeEvent{}, ctx.Err()
		case <-p.notify:
		case <-p.done:
		}
	}
}
func (p *ClaudeCodeProcess) write(ctx context.Context, value interface{}) error {
	return p.writeWithStart(ctx, value, nil)
}
func (p *ClaudeCodeProcess) writeWithStart(ctx context.Context, value interface{}, start func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > 6<<20 {
		return errors.New("Claude stdin record exceeds 6 MiB")
	}
	data = append(data, '\n')
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = p.Err(); err != nil {
		return err
	}
	if start != nil {
		if err := start(ctx); err != nil {
			return err
		}
	}
	// A timed-out partial write is uncertain, never safe to replay or reuse.
	callbackDone := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(callbackDone); p.fail(ctx.Err()); p.stdin.Close() })
	defer func() {
		if !stop() {
			<-callbackDone
		}
	}()
	n, err := p.stdin.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		p.fail(fmt.Errorf("Claude stdin: %w", err))
	}
	return err
}

// SendUser reserves exact protocol UUID correlation before the serialized write.
// IDs must be unique for the caller's process incarnation. Content is rendered
// CLI content (text or blocks); original accepted bytes remain with the caller.
func (p *ClaudeCodeProcess) SendUser(ctx context.Context, id string, content json.RawMessage) error {
	return p.sendUserWithStart(ctx, id, content, nil)
}
func (p *ClaudeCodeProcess) sendUserWithStart(ctx context.Context, id string, content json.RawMessage, start func(context.Context) error) error {
	if id == "" || !claudeCodeValidContent(content) {
		return errors.New("Claude input requires ID and valid content")
	}
	p.mu.Lock()
	_, exists := p.inputs[id]
	if exists || len(p.inputs) >= 64 || p.inputBytes+len(content) > 6<<20 {
		p.mu.Unlock()
		return errors.New("Claude input correlation duplicate or capacity exceeded")
	}
	p.inputs[id] = claudeCodePendingInput{content: append(json.RawMessage(nil), content...), bytes: len(content)}
	p.inputBytes += len(content)
	p.mu.Unlock()
	var turnInput any
	if p.adapter != nil && p.adapter.Provider() != claudeCodeProvider {
		rawTurn, err := p.adapter.FormatUserTurn(UserTurnInput{
			ID:        id,
			SessionID: p.sessionID,
			Content:   content,
		})
		if err != nil {
			return err
		}
		turnInput = json.RawMessage(rawTurn)
	} else {
		turnInput = map[string]interface{}{"type": "user", "uuid": id, "session_id": p.sessionID, "message": map[string]interface{}{"role": "user", "content": content}}
	}
	err := p.writeWithStart(ctx, turnInput, start)
	if err != nil {
		p.mu.Lock()
		if pending, ok := p.inputs[id]; ok {
			delete(p.inputs, id)
			p.inputBytes -= pending.bytes
		}
		p.mu.Unlock()
	}
	return err
}

// Control correlates the nested response. Timeouts stop uncertain protocol state;
// no abandoned response map accumulates. A rejection does not stop the process.
func (p *ClaudeCodeProcess) Control(ctx context.Context, request map[string]interface{}) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	p.mu.Lock()
	if len(p.controls) >= 64 {
		p.mu.Unlock()
		return nil, errors.New("Claude pending control limit reached")
	}
	p.sequence++
	id := fmt.Sprintf("mecha-%d", p.sequence)
	ch := make(chan claudeCodeControlResponse, 1)
	p.controls[id] = ch
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.controls, id); p.mu.Unlock() }()
	if err := p.write(ctx, map[string]interface{}{"type": "control_request", "request_id": id, "request": request}); err != nil {
		return nil, err
	}
	select {
	case response := <-ch:
		if response.Subtype == "error" {
			return nil, fmt.Errorf("Claude control rejected: %s", response.Error)
		}
		return response.Response, nil
	case <-ctx.Done():
		p.fail(ctx.Err())
		return nil, ctx.Err()
	case <-p.done:
		return nil, p.Err()
	}
}
func (p *ClaudeCodeProcess) Interrupt(ctx context.Context) error {
	_, err := p.Control(ctx, map[string]interface{}{"subtype": "interrupt"})
	return err
}
func (p *ClaudeCodeProcess) Done() <-chan struct{} { return p.done }
func (p *ClaudeCodeProcess) Err() error            { p.mu.Lock(); defer p.mu.Unlock(); return p.err }

// Stderr returns a raw bounded tail; redact before any user-facing publication.
func (p *ClaudeCodeProcess) Stderr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return string(p.stderr)
}
func (p *ClaudeCodeProcess) Close() error {
	p.closeOnce.Do(func() {
		defer close(p.closeDone)
		// The whole stop, including a blocked stdin close and reaping, has one budget.
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		stdinClosed := make(chan struct{})
		go func() { defer close(stdinClosed); _ = p.stdin.Close() }()
		_ = (defaultProcessGroupOps{}).SignalVerifiedProcessGroup(p.orphanIdentity, syscall.SIGTERM)
		escalate := time.NewTimer(2 * time.Second)
		defer escalate.Stop()
		select {
		case <-p.done:
		case <-escalate.C:
			_ = (defaultProcessGroupOps{}).SignalVerifiedProcessGroup(p.orphanIdentity, syscall.SIGKILL)
			select {
			case <-p.done:
			case <-deadline.C:
				if p.onUnconfirmedStop != nil {
					p.onUnconfirmedStop(errClaudeCodeGroupUnconfirmed)
				}
				p.closeErr = errors.New("Claude process stop unconfirmed")
			}
		case <-deadline.C:
			if p.onUnconfirmedStop != nil {
				p.onUnconfirmedStop(errClaudeCodeGroupUnconfirmed)
			}
			p.closeErr = errors.New("Claude process stop unconfirmed")
		}
		if p.closeErr == nil {
			select {
			case <-stdinClosed:
			case <-deadline.C:
				if p.onUnconfirmedStop != nil {
					p.onUnconfirmedStop(errClaudeCodeGroupUnconfirmed)
				}
				p.closeErr = errors.New("Claude process stop unconfirmed")
			}
		}
		if p.closeErr == nil {
			p.mu.Lock()
			if p.orphanCleanupErr != nil {
				p.closeErr = p.orphanCleanupErr
			} else if errors.Is(p.waitErr, errClaudeCodeGroupUnconfirmed) {
				p.closeErr = p.waitErr
			}
			p.mu.Unlock()
		}
		if p.closeErr == nil {
			p.forgetClaudeOrphanIfAbsent()
		}
		p.mu.Lock()
		p.events = nil
		p.eventBytes = 0
		p.ownedTasks = nil
		p.ownedTaskBytes = 0
		p.controls = make(map[string]chan claudeCodeControlResponse)
		p.inputs = make(map[string]claudeCodePendingInput)
		p.inputBytes = 0
		p.mu.Unlock()
	})
	<-p.closeDone
	return p.closeErr
}

func claudeCodeValidContent(content json.RawMessage) bool {
	var text string
	if json.Unmarshal(content, &text) == nil && string(content) != "null" {
		return true
	}
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(content, &blocks) != nil || blocks == nil {
		return false
	}
	for _, block := range blocks {
		if block.Type == "" {
			return false
		}
	}
	return true
}

func parseClaudeCodeEvent(line []byte, sessionID string) (ClaudeCodeEvent, *claudeCodeControlResponse, error) {
	var e ClaudeCodeEvent
	bad := func() (ClaudeCodeEvent, *claudeCodeControlResponse, error) {
		return e, nil, errors.New("malformed required Claude protocol record")
	}
	if json.Unmarshal(line, &e) != nil || e.Type == "" {
		return bad()
	}
	e.Raw = append(json.RawMessage(nil), line...)
	switch e.Type {
	case "control_response":
		var envelope struct {
			Response claudeCodeControlResponse `json:"response"`
		}
		if json.Unmarshal(line, &envelope) != nil {
			return bad()
		}
		r := envelope.Response
		if r.RequestID == "" || (r.Subtype != "success" && r.Subtype != "error") {
			return bad()
		}
		return e, &r, nil
	case "control_request":
		return e, nil, errors.New("unsupported Claude inbound control request")
	case "system":
		if e.Subtype == "" {
			return bad()
		}
		if e.Subtype == "init" {
			var init struct {
				Capabilities []string `json:"capabilities"`
			}
			if json.Unmarshal(line, &init) != nil {
				return bad()
			}
			if e.SessionID != sessionID {
				return e, nil, errors.New("Claude session_id mismatch")
			}
			supported := false
			for _, c := range init.Capabilities {
				if c == "interrupt_receipt_v1" {
					supported = true
				}
			}
			if !supported {
				return e, nil, errors.New("Claude Code CLI too old: missing interrupt_receipt_v1")
			}
		}
	case "assistant", "user":
		var message struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(e.Message, &message) != nil || message.Role != e.Type || !claudeCodeValidContent(message.Content) {
			return bad()
		}
	case "result":
		if e.Subtype == "" {
			return bad()
		}
		if e.Subtype != "success" && !strings.HasPrefix(e.Subtype, "error_") {
			return e, nil, errors.New("unsupported Claude result subtype")
		}
	}
	return e, nil, nil
}

// Task snapshots are inspected by the sole stdout reader, including while idle.
// Retain only the bounded count; task IDs/descriptions are not a history cache.
func (p *ClaudeCodeProcess) observeNotificationOwnership(event ClaudeCodeEvent) error {
	if event.Type != "system" || event.Subtype != "task_started" {
		return nil
	}
	var task struct {
		ID    string `json:"task_id"`
		Owned bool   `json:"owned_by_subagent"`
	}
	if json.Unmarshal(event.Raw, &task) != nil || task.ID == "" {
		return errors.New("malformed Claude task_started")
	}
	if !task.Owned {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ownedTasks[task.ID] {
		return nil
	}
	if len(p.ownedTasks) >= 1024 || p.ownedTaskBytes+len(task.ID) > 1<<20 {
		return errors.New("Claude owned task set overflow")
	}
	if p.ownedTasks == nil {
		p.ownedTasks = make(map[string]bool)
	}
	p.ownedTasks[task.ID] = true
	p.ownedTaskBytes += len(task.ID)
	return nil
}
