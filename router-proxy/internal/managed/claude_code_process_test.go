package managed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func claudeTestConfig(t *testing.T, script string) ClaudeCodeProcessConfig {
	t.Helper()
	binary, err := filepath.Abs("../../../tests/fixtures/fake-claude/claude.mjs")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "script.json")
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	return ClaudeCodeProcessConfig{Binary: binary, Cwd: root, SessionID: "11111111-1111-4111-8111-111111111111", Model: "sonnet", Environment: append(os.Environ(), "FAKE_CLAUDE_SCRIPT="+path, "FAKE_CLAUDE_RECORD="+filepath.Join(root, "record.jsonl"), "TMUX=bad", "TMUX_PANE=bad", "CLAUDECODE=bad", "CLAUDE_CODE_ENTRYPOINT=bad")}
}
func claudeNext(t *testing.T, p *ClaudeCodeProcess) ClaudeCodeEvent {
	t.Helper()
	ctx, c := context.WithTimeout(context.Background(), 3*time.Second)
	defer c()
	e, err := p.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func claudeWait(t *testing.T, p *ClaudeCodeProcess) {
	t.Helper()
	select {
	case <-p.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("process not reaped")
	}
}
func claudeStart(t *testing.T, cfg ClaudeCodeProcessConfig) *ClaudeCodeProcess {
	t.Helper()
	p, err := StartClaudeCodeProcess(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}
func claudeSend(t *testing.T, p *ClaudeCodeProcess, id, content string) {
	t.Helper()
	ctx, c := context.WithTimeout(context.Background(), 3*time.Second)
	defer c()
	if err := p.SendUser(ctx, id, json.RawMessage(content)); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeCodeProcessSharedFixtureRoundTrip(t *testing.T) {
	cfg := claudeTestConfig(t, `{"turns":[[{"text":"hello"},{"tool":{"id":"call-1","name":"Read","input":{"file_path":"x"},"result":"contents"}}],[{"text":"second"}]]}`)
	cfg.MCPConfigFile = "test-mcp.json"
	cfg.SystemPromptFile = "test-prompt.txt"
	if err := os.WriteFile(filepath.Join(cfg.Cwd, cfg.SystemPromptFile), []byte("test prompt"), 0600); err != nil {
		t.Fatal(err)
	}
	p := claudeStart(t, cfg)
	content := `[{"type":"text","text":"same text"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"eA=="}}]`
	for i := 0; i < 2; i++ {
		id := fmt.Sprintf("input-%d", i)
		claudeSend(t, p, id, content)
		if e := claudeNext(t, p); e.Subtype != "init" {
			t.Fatal(e)
		}
		e := claudeNext(t, p)
		if e.Type != "user" || !e.IsReplay || e.UUID != id || !strings.Contains(string(e.Message), "image/png") {
			t.Fatal(e)
		}
		if e = claudeNext(t, p); e.Type != "assistant" {
			t.Fatal(e)
		}
		if i == 0 {
			if e = claudeNext(t, p); !strings.Contains(string(e.Message), "tool_use") {
				t.Fatal(e)
			}
			if e = claudeNext(t, p); !strings.Contains(string(e.Message), "tool_result") {
				t.Fatal(e)
			}
		}
		if e = claudeNext(t, p); e.Type != "result" || e.Subtype != "success" {
			t.Fatal(e)
		}
	}
	p.Close()
	data, err := os.ReadFile(filepath.Join(cfg.Cwd, "record.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	starts := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var r struct {
			Kind string            `json:"kind"`
			Argv []string          `json:"argv"`
			Cwd  string            `json:"cwd"`
			Env  map[string]string `json:"env"`
		}
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal(err)
		}
		if r.Kind == "start" {
			starts++
			if r.Cwd != cfg.Cwd {
				t.Fatal(r)
			}
			for _, key := range []string{"TMUX", "TMUX_PANE", "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT"} {
				if _, ok := r.Env[key]; ok {
					t.Fatalf("leaked %s", key)
				}
			}
			if len(r.Argv) > 3 {
				want := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--replay-user-messages", "--model", "sonnet", "--permission-mode", "bypassPermissions", "--tools", "Bash,Read,Write,Edit,Glob,Grep,ToolSearch", "--mcp-config", "test-mcp.json", "--append-system-prompt-file", "test-prompt.txt", "--session-id", cfg.SessionID}
				a, _ := json.Marshal(r.Argv)
				b, _ := json.Marshal(want)
				if string(a) != string(b) {
					t.Fatalf("argv %s want %s", a, b)
				}
			}
		}
	}
	if starts != 2 {
		t.Fatalf("preflight plus one reused process, got %d", starts)
	}
}
func TestClaudeCodeProcessInterruptIsNotClose(t *testing.T) {
	p := claudeStart(t, claudeTestConfig(t, `{"turns":[[{"text":"partial"},{"wait":"interrupt"}],[{"text":"after"}]]}`))
	claudeSend(t, p, "first", `"same"`)
	claudeNext(t, p)
	claudeNext(t, p)
	if e := claudeNext(t, p); !strings.Contains(string(e.Message), "partial") {
		t.Fatal(e)
	}
	ctx, c := context.WithTimeout(context.Background(), 3*time.Second)
	defer c()
	if err := p.Interrupt(ctx); err != nil {
		t.Fatal(err)
	}
	if e := claudeNext(t, p); e.Subtype != "error_during_execution" {
		t.Fatal(e)
	}
	claudeSend(t, p, "second", `"same"`)
	claudeNext(t, p)
	if e := claudeNext(t, p); e.UUID != "second" {
		t.Fatal(e)
	}
	claudeNext(t, p)
	if e := claudeNext(t, p); e.Subtype != "success" {
		t.Fatal(e)
	}
	if _, err := p.Control(ctx, map[string]interface{}{"subtype": "set_model", "model": "haiku"}); err != nil {
		t.Fatal(err)
	}
	if e := claudeNext(t, p); e.IsReplay || e.Type != "user" {
		t.Fatal(e)
	}
}
func TestClaudeCodeProcessPreflightFailures(t *testing.T) {
	for name, script := range map[string]string{"signed-out": `{"auth":{"loggedIn":false}}`, "malformed": `{"auth":{"raw":"not json"}}`, "missing-field": `{"auth":{"raw":"{}"}}`, "timeout": `{"auth":{"hang":true}}`} {
		t.Run(name, func(t *testing.T) {
			cfg := claudeTestConfig(t, script)
			ctx, c := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer c()
			p, err := StartClaudeCodeProcess(ctx, cfg)
			if err == nil {
				p.Close()
				t.Fatal("expected preflight failure")
			}
			if !strings.Contains(err.Error(), "host") {
				t.Fatal(err)
			}
		})
	}
	cfg := claudeTestConfig(t, `{}`)
	cfg.Binary = "/missing/claude"
	if _, err := PreflightClaudeCode(context.Background(), cfg); err == nil {
		t.Fatal("missing binary accepted")
	}
}
func TestClaudeCodeProcessProtocolFailures(t *testing.T) {
	tests := map[string]string{
		"malformed":          `{"startup":[{"raw":"{broken"}]}`,
		"old":                `{"capabilities":[]}`,
		"oversize":           `{"startup":[{"raw":{"text":"x","repeat":16777217}}]}`,
		"wrong-echo-id":      `{"turns":[[{"emit":{"type":"user","isReplay":true,"uuid":"wrong","message":{"role":"user","content":"same"}}}]]}`,
		"control":            `{"startup":[{"emit":{"type":"control_response","response":{"subtype":"success","request_id":"unknown","response":{}}}}]}`,
		"malformed-required": `{"startup":[{"emit":{"type":"assistant","message":{}}}]}`,
		"session":            `{"startup":[{"emit":{"type":"system","subtype":"init","session_id":"wrong","capabilities":["interrupt_receipt_v1"]}}]}`,
		"crash":              `{"turns":[[{"crash":7}]]}`,
	}
	for name, script := range tests {
		t.Run(name, func(t *testing.T) {
			p := claudeStart(t, claudeTestConfig(t, script))
			_ = p.SendUser(context.Background(), "input", json.RawMessage(`"same"`))
			claudeWait(t, p)
			if p.Err() == nil {
				t.Fatal("expected visible error")
			}
		})
	}
}
func TestClaudeCodeProcessBufferBounds(t *testing.T) {
	t.Run("count", func(t *testing.T) {
		steps := make([]interface{}, 129)
		for i := range steps {
			steps[i] = map[string]interface{}{"emit": map[string]interface{}{"type": "benign"}}
		}
		data, _ := json.Marshal(map[string]interface{}{"startup": steps})
		p := claudeStart(t, claudeTestConfig(t, string(data)))
		claudeWait(t, p)
		if !strings.Contains(p.Err().Error(), "overflow") {
			t.Fatal(p.Err())
		}
	})
	t.Run("bytes", func(t *testing.T) {
		p := claudeStart(t, claudeTestConfig(t, `{}`))
		line := `{"type":"benign","padding":"` + strings.Repeat("x", 9<<20) + `"}` + "\n"
		p.read(strings.NewReader(line + line))
		claudeWait(t, p)
		if !strings.Contains(p.Err().Error(), "overflow") {
			t.Fatal(p.Err())
		}
	})
}

func TestClaudeCodeProcessSerializedWritesAndExactIDs(t *testing.T) {
	p := claudeStart(t, claudeTestConfig(t, `{}`))
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := p.SendUser(context.Background(), fmt.Sprintf("id-%d", i), json.RawMessage(`"equal text"`)); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for i := 0; i < 48; i++ {
		e := claudeNext(t, p)
		if e.IsReplay {
			if seen[e.UUID] {
				t.Fatal("duplicate echo")
			}
			seen[e.UUID] = true
		}
	}
	if len(seen) != 12 {
		t.Fatal(seen)
	}
}

// A small supplementary shell fixture covers descendants: shared fake has none.
func TestClaudeCodeProcessGroupCleanupAndStderr(t *testing.T) {
	cfg := claudeTestConfig(t, `{}`)
	cfg.Binary = filepath.Join(cfg.Cwd, "claude")
	script := `#!/usr/bin/env python3
import json,sys,subprocess,os
if sys.argv[1:] == ['auth','status','--json']:
 print('{"loggedIn":true}');sys.exit(0)
child=subprocess.Popen(['sleep','60'])
sys.stderr.write('x'*70000);sys.stderr.flush()
print(json.dumps({'type':'child','pid':child.pid}),flush=True)
for line in sys.stdin: pass
child.terminate();child.wait()
`
	if err := os.WriteFile(cfg.Binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	p := claudeStart(t, cfg)
	e := claudeNext(t, p)
	var child struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(e.Raw, &child); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(child.PID, 0); err != syscall.ESRCH {
		t.Fatalf("child remains: %v", err)
	}
	if len(p.Stderr()) != 64<<10 {
		t.Fatalf("stderr bytes %d", len(p.Stderr()))
	}
}

func TestClaudeCodeProcessControlRefusalAndResumeFailure(t *testing.T) {
	p := claudeStart(t, claudeTestConfig(t, `{"refuse_model":true}`))
	ctx, c := context.WithTimeout(context.Background(), 3*time.Second)
	defer c()
	if _, err := p.Control(ctx, map[string]interface{}{"subtype": "set_model", "model": "haiku"}); err == nil {
		t.Fatal("model refusal accepted")
	}
	claudeSend(t, p, "still-alive", `"hello"`)
	for i := 0; i < 4; i++ {
		claudeNext(t, p)
	}
	cfg := claudeTestConfig(t, `{"resume_missing":true}`)
	cfg.Resume = true
	r := claudeStart(t, cfg)
	e := claudeNext(t, r)
	if e.Type != "result" || e.Subtype != "error_during_execution" {
		t.Fatal(e)
	}
	claudeWait(t, r)
	if !strings.Contains(r.Stderr(), "No conversation found with session ID") {
		t.Fatal(r.Stderr())
	}
}

func TestClaudeCodeProcessInputBounds(t *testing.T) {
	for _, kind := range []string{"count", "bytes"} {
		t.Run(kind, func(t *testing.T) {
			// A startup hang prevents echoes. The stdin reader still runs deterministically.
			p := claudeStart(t, claudeTestConfig(t, `{"startup":[{"hang":true}]}`))
			if kind == "count" {
				for i := 0; i < 64; i++ {
					claudeSend(t, p, fmt.Sprintf("pending-%d", i), `"same"`)
				}
				if err := p.SendUser(context.Background(), "overflow", json.RawMessage(`"same"`)); err == nil {
					t.Fatal("count overflow accepted")
				}
			} else {
				content := json.RawMessage(`"` + strings.Repeat("x", 2<<20) + `"`)
				if err := p.SendUser(context.Background(), "bytes-1", content); err != nil {
					t.Fatal(err)
				}
				if err := p.SendUser(context.Background(), "bytes-2", content); err != nil {
					t.Fatal(err)
				}
				if err := p.SendUser(context.Background(), "overflow", content); err == nil {
					t.Fatal("byte overflow accepted")
				}
			}
			p.mu.Lock()
			n := len(p.inputs)
			p.mu.Unlock()
			if n > 64 {
				t.Fatal(n)
			}
			p.fail(errors.New("test cleanup"))
			claudeWait(t, p)
		})
	}
}

func TestClaudeCodeProcessBinaryResolution(t *testing.T) {
	cfg := claudeTestConfig(t, `{}`)
	binary := cfg.Binary
	cfg.Binary = ""
	cfg.Environment = append(cfg.Environment, "MECHA_CLAUDE_BIN="+binary)
	if got, err := PreflightClaudeCode(context.Background(), cfg); err != nil || got != binary {
		t.Fatalf("override %q: %v", got, err)
	}
	dir := filepath.Join(cfg.Cwd, ".local", "bin")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(binary, filepath.Join(dir, "claude")); err != nil {
		t.Fatal(err)
	}
	cfg.Environment = append(cfg.Environment, "MECHA_CLAUDE_BIN=", "HOME="+cfg.Cwd)
	if got, err := resolveClaudeCodeBinary(cfg, []string{"PATH=/missing", "HOME=" + cfg.Cwd}); err != nil || got != filepath.Join(dir, "claude") {
		t.Fatalf("local %q: %v", got, err)
	}
	if got, err := resolveClaudeCodeBinary(cfg, []string{"PATH=" + dir}); err != nil || got != filepath.Join(dir, "claude") {
		t.Fatalf("PATH %q: %v", got, err)
	}
}

func TestClaudeCodeProcessCloseEscalatesAndReaps(t *testing.T) {
	cfg := claudeTestConfig(t, `{}`)
	cfg.Binary = filepath.Join(cfg.Cwd, "claude")
	script := `#!/usr/bin/env python3
import sys,signal,time
if sys.argv[1:] == ['auth','status','--json']:
 print('{"loggedIn":true}');sys.exit(0)
signal.signal(signal.SIGTERM,signal.SIG_IGN)
print('{"type":"ready"}',flush=True)
while True: time.sleep(60)
`
	if err := os.WriteFile(cfg.Binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	p := claudeStart(t, cfg)
	claudeNext(t, p)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(p.cmd.Process.Pid, 0); err != syscall.ESRCH {
		t.Fatalf("leader not reaped: %v", err)
	}
	if err := p.SendUser(context.Background(), "late", json.RawMessage(`"late"`)); err == nil {
		t.Fatal("write after close accepted")
	}
}

func TestClaudeCodeProcessParserRequiredAndBenignRecords(t *testing.T) {
	for _, line := range []string{
		`{"type":"assistant","message":{"role":"assistant","content":42}}`,
		`{"type":"user","message":{"role":"user","content":[{}]}}`,
		`{"type":"control_request","request":{"subtype":"can_use_tool"}}`,
		`{"type":"control_response","response":{"subtype":"success"}}`,
		`{"type":"result","subtype":"mystery"}`,
	} {
		if _, _, err := parseClaudeCodeEvent([]byte(line), "session"); err == nil {
			t.Fatalf("accepted %s", line)
		}
	}
	for _, line := range []string{
		`{"type":"future_benign","extra":true}`,
		`{"type":"assistant","parent_tool_use_id":"subagent","message":{"role":"assistant","content":[{"type":"text","text":"child"}]}}`,
		`{"type":"user","isReplay":true,"origin":{"kind":"task-notification"},"message":{"role":"user","content":"notice"}}`,
	} {
		e, _, err := parseClaudeCodeEvent([]byte(line), "session")
		if err != nil || string(e.Raw) != line {
			t.Fatalf("lost benign/raw record: %v", err)
		}
	}
	if claudeCodeJSONEqual([]byte(`{"id":9007199254740992}`), []byte(`{"id":9007199254740993}`)) {
		t.Fatal("large integer input identity changed")
	}
}

func TestClaudeCodeProcessRejectsEchoWrongRole(t *testing.T) {
	p := claudeStart(t, claudeTestConfig(t, `{"replay_role":"assistant"}`))
	claudeSend(t, p, "input", `"same"`)
	claudeWait(t, p)
	if p.Err() == nil {
		t.Fatal("echo with assistant role accepted")
	}
}

func TestClaudeCodeProcessArgsRolesAndResume(t *testing.T) {
	for _, role := range []struct {
		name        string
		root, child bool
	}{
		{"root", true, false},
		{"worker", false, false},
		{"child", false, true},
	} {
		for _, resume := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/resume=%t", role.name, resume), func(t *testing.T) {
				cfg := ClaudeCodeProcessConfig{Model: "sonnet", SessionID: "session", MCPConfigFile: "role-mcp.json", RootAuthority: role.root, ChildAuthority: role.child, Resume: resume}
				args := claudeCodeProcessArgs(cfg)
				tools, strict, mcp := 0, 0, 0
				for i, arg := range args {
					switch arg {
					case "--tools":
						tools++
						if i+1 >= len(args) || args[i+1] != "Bash,Read,Write,Edit,Glob,Grep,ToolSearch" {
							t.Fatalf("unexpected tools: %q", args)
						}
					case "--strict-mcp-config":
						strict++
					case "--mcp-config":
						mcp++
						if i+1 >= len(args) || args[i+1] != cfg.MCPConfigFile {
							t.Fatalf("unexpected MCP config: %q", args)
						}
					}
				}
				if tools != 1 || mcp != 1 || (strict == 1) != (role.root || role.child) || strict > 1 {
					t.Fatalf("unexpected role flags: %q", args)
				}
				flag, other := "--session-id", "--resume"
				if resume {
					flag, other = other, flag
				}
				if !containsArg(args, flag) || containsArg(args, other) || args[len(args)-1] != cfg.SessionID {
					t.Fatalf("unexpected session flags: %q", args)
				}
			})
		}
	}
}

func TestClaudeCodeChildProcessIsolationArguments(t *testing.T) {
	cfg := claudeTestConfig(t, `{"turns":[[{"text":"done"}]]}`)
	cfg.ChildAuthority = true
	cfg.MCPConfigFile = "child-mcp.json"
	p := claudeStart(t, cfg)
	claudeSend(t, p, "child-input", `"task"`)
	for i := 0; i < 4; i++ {
		claudeNext(t, p)
	}
	p.Close()
	data, err := os.ReadFile(filepath.Join(cfg.Cwd, "record.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var r struct {
			Kind string   `json:"kind"`
			Argv []string `json:"argv"`
		}
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal(err)
		}
		for i, arg := range r.Argv {
			if arg == "--tools" {
				if i+1 >= len(r.Argv) || r.Argv[i+1] != "Bash,Read,Write,Edit,Glob,Grep,ToolSearch" {
					t.Fatalf("unexpected native tools: %q", r.Argv)
				}
				found = true
			}
		}
		if found {
			if !containsArg(r.Argv, "--strict-mcp-config") || !containsArg(r.Argv, "child-mcp.json") {
				t.Fatalf("missing strict child MCP: %q", r.Argv)
			}
			break
		}
	}
	if !found {
		t.Fatal("missing child isolation flags")
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func TestClaudeGateWriteFailureConfirmsNeverReleased(t *testing.T) {
	cfg := claudeTestConfig(t, `{"turns":[]}`)
	admitted, restored := false, false
	cfg.BeforeRelease = func() error { admitted = true; return nil }
	cfg.afterFailedRelease = func() error { restored = true; return nil }
	cfg.writeRelease = func(*os.File) (int, error) { return 0, errors.New("injected gate failure") }
	_, err := StartClaudeCodeProcess(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "injected gate failure") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !admitted {
		t.Fatal("not admitted")
	}
	if !restored {
		t.Fatal("not restored")
	}
}

func TestClaudeGatePartialWriteRemainsUncertain(t *testing.T) {
	cfg := claudeTestConfig(t, `{"turns":[]}`)
	restored := false
	cfg.BeforeRelease = func() error { return nil }
	cfg.afterFailedRelease = func() error { restored = true; return nil }
	cfg.writeRelease = func(f *os.File) (int, error) {
		n, _ := f.Write([]byte("ready\n"))
		return n, errors.New("injected partial failure")
	}
	_, err := StartClaudeCodeProcess(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected gate failure")
	}
	if restored {
		t.Fatal("possibly released process marked unstarted")
	}
}

func TestClaudeBeforeReleaseUnconfirmedCleanupFencesAdmission(t *testing.T) {
	cfg := claudeTestConfig(t, `{"turns":[]}`)
	cfg.OrphanRoot = t.TempDir()
	admission := errors.New("admission failed")
	fenced := false
	cfg.BeforeRelease = func() error { return admission }
	cfg.OnUnconfirmedStop = func(error) { fenced = true }
	calls := 0
	cfg.waitForCleanup = func(cmd *exec.Cmd, id ProcessIdentity, confirmed func()) error {
		calls++
		if calls == 1 {
			return waitClaudeCodeCommandConfirmed(cmd, id, confirmed)
		}
		// Reap the test child without confirming it to the production callback.
		_ = waitClaudeCodeCommandConfirmed(cmd, id, nil)
		return errClaudeCodeGroupUnconfirmed
	}
	_, err := StartClaudeCodeProcess(context.Background(), cfg)
	if !errors.Is(err, admission) || !errors.Is(err, errClaudeCodeGroupUnconfirmed) || !fenced {
		t.Fatalf("error=%v fenced=%v", err, fenced)
	}
	entries, e := os.ReadDir(filepath.Join(cfg.OrphanRoot, "claude-orphans"))
	if e != nil || len(entries) != 1 {
		t.Fatalf("recovery records=%v err=%v", entries, e)
	}
}
