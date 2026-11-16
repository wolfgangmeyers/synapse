package managed

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func buildFakeAgy(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	binPath := filepath.Join(dir, "agy")
	srcPath, err := filepath.Abs("testdata/fake_agy/main.go")
	if err != nil {
		t.Fatalf("failed to get absolute path to testdata/fake_agy/main.go: %v", err)
	}

	cmd := exec.Command("go", "build", "-o", binPath, srcPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to build fake_agy: %v\noutput: %s", err, string(out))
	}
	return binPath
}

func TestCLIAdapter_ClaudeCode(t *testing.T) {
	fakeClaude, err := filepath.Abs("../../../tests/fixtures/fake-claude/claude.mjs")
	if err != nil {
		t.Fatal(err)
	}

	adapter := NewClaudeCodeAdapter(fakeClaude, append(os.Environ(),
		"TMUX=bad",
		"CLAUDECODE=bad",
		"TEST_KEY=hello",
	))

	// 1. Provider
	if got := adapter.Provider(); got != "claude-code" {
		t.Errorf("Provider() = %q, want %q", got, "claude-code")
	}

	// 2. CleanEnvironment
	cleaned := adapter.CleanEnvironment([]string{"TMUX=1", "TMUX_PANE=1", "CLAUDECODE=1", "KEEP=true"})
	if len(cleaned) != 1 || cleaned[0] != "KEEP=true" {
		t.Errorf("CleanEnvironment failed: %v", cleaned)
	}

	// 3. Executable
	ctx := context.Background()
	bin, err := adapter.Executable(ctx)
	if err != nil {
		t.Fatalf("Executable() error: %v", err)
	}
	if bin != fakeClaude {
		t.Errorf("Executable() = %q, want %q", bin, fakeClaude)
	}

	// 4. Preflight
	// fake-claude responds to "auth status --json" with {"loggedIn":true} when script has auth true
	scriptPath := filepath.Join(t.TempDir(), "script.json")
	if err := os.WriteFile(scriptPath, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	adapter.env = append(adapter.env, "FAKE_CLAUDE_SCRIPT="+scriptPath)
	if err := adapter.Preflight(ctx); err != nil {
		t.Errorf("Preflight() error: %v", err)
	}

	// 5. BuildArgs
	args := adapter.BuildArgs(CLIProcessConfig{
		SessionID: "test-sess",
		Model:     "sonnet",
	}, "/tmp/mcp.json")
	argStr := strings.Join(args, " ")
	if !strings.Contains(argStr, "--session-id test-sess") || !strings.Contains(argStr, "--mcp-config /tmp/mcp.json") || !strings.Contains(argStr, "--model sonnet") {
		t.Errorf("BuildArgs produced unexpected args: %v", args)
	}

	// 6. FormatUserTurn
	turnBytes, err := adapter.FormatUserTurn(UserTurnInput{
		ID:        "msg-1",
		SessionID: "sess-1",
		Content:   json.RawMessage(`"hello"`),
	})
	if err != nil {
		t.Fatalf("FormatUserTurn error: %v", err)
	}
	var turnMap map[string]interface{}
	if err := json.Unmarshal(turnBytes, &turnMap); err != nil {
		t.Fatalf("FormatUserTurn unmarshal error: %v", err)
	}
	if turnMap["uuid"] != "msg-1" || turnMap["session_id"] != "sess-1" {
		t.Errorf("FormatUserTurn unexpected content: %v", turnMap)
	}

	// 7. EphemeralSettingsContent
	settingsBytes, err := adapter.EphemeralSettingsContent("http://127.0.0.1:8765/mcp", "secret-token")
	if err != nil {
		t.Fatalf("EphemeralSettingsContent error: %v", err)
	}
	if !strings.Contains(string(settingsBytes), "secret-token") || !strings.Contains(string(settingsBytes), "http://127.0.0.1:8765/mcp") {
		t.Errorf("EphemeralSettingsContent content unexpected: %s", string(settingsBytes))
	}

	// 8. ParseLine
	eventLine := []byte(`{"type":"assistant","session_id":"sess-1","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`)
	event, err := adapter.ParseLine(eventLine, "sess-1")
	if err != nil {
		t.Fatalf("ParseLine error: %v", err)
	}
	if event.Type != "assistant" || event.SessionID != "sess-1" {
		t.Errorf("ParseLine unexpected event: %+v", event)
	}
}

func TestCLIAdapter_Antigravity(t *testing.T) {
	fakeAgy := buildFakeAgy(t)

	adapter := NewAntigravityAdapter(fakeAgy, []string{
		"PATH=/usr/bin",
		"TMUX=bad",
		"CLAUDECODE=bad",
		"TEST_KEY=hello",
	})

	// 1. Provider
	if got := adapter.Provider(); got != "antigravity" {
		t.Errorf("Provider() = %q, want %q", got, "antigravity")
	}

	// 2. CleanEnvironment
	cleaned := adapter.CleanEnvironment([]string{"TMUX=1", "TMUX_PANE=1", "CLAUDECODE=1", "KEEP=true"})
	if len(cleaned) != 1 || cleaned[0] != "KEEP=true" {
		t.Errorf("CleanEnvironment failed: %v", cleaned)
	}

	// 3. Executable
	ctx := context.Background()
	bin, err := adapter.Executable(ctx)
	if err != nil {
		t.Fatalf("Executable() error: %v", err)
	}
	if bin != fakeAgy {
		t.Errorf("Executable() = %q, want %q", bin, fakeAgy)
	}

	// 4. Preflight - Authenticated
	if err := adapter.Preflight(ctx); err != nil {
		t.Errorf("Preflight() unexpected error for authenticated fake_agy: %v", err)
	}

	// 4b. Preflight - Unauthenticated
	unauthAdapter := NewAntigravityAdapter(fakeAgy, []string{
		"TEST_AGY_AUTH=unauthenticated",
	})
	if err := unauthAdapter.Preflight(ctx); err == nil {
		t.Errorf("Preflight() expected error for unauthenticated fake_agy, got nil")
	} else if !strings.Contains(err.Error(), "Please sign in") && !strings.Contains(err.Error(), "unavailable or signed out") {
		t.Errorf("Preflight() unexpected error message: %v", err)
	}

	// 5. BuildArgs
	args := adapter.BuildArgs(CLIProcessConfig{
		SessionID: "test-sess",
		Model:     "gemini-2.5-pro",
	}, "/tmp/agy_settings.json")
	argStr := strings.Join(args, " ")
	if !strings.Contains(argStr, "-p") ||
		!strings.Contains(argStr, "--input-format stream-json") ||
		!strings.Contains(argStr, "--output-format stream-json") ||
		!strings.Contains(argStr, "--dangerously-skip-permissions") ||
		!strings.Contains(argStr, "--conversation test-sess") ||
		!strings.Contains(argStr, "--model gemini-2.5-pro") ||
		!strings.Contains(argStr, "--settings /tmp/agy_settings.json") {
		t.Errorf("BuildArgs produced unexpected args: %v", args)
	}

	// 6. FormatUserTurn
	turnBytes, err := adapter.FormatUserTurn(UserTurnInput{
		ID:        "msg-2",
		SessionID: "sess-2",
		Content:   json.RawMessage(`"hello agy"`),
	})
	if err != nil {
		t.Fatalf("FormatUserTurn error: %v", err)
	}
	var turnMap map[string]interface{}
	if err := json.Unmarshal(turnBytes, &turnMap); err != nil {
		t.Fatalf("FormatUserTurn unmarshal error: %v", err)
	}
	if turnMap["uuid"] != "msg-2" || turnMap["session_id"] != "sess-2" {
		t.Errorf("FormatUserTurn unexpected content: %v", turnMap)
	}

	// 7. EphemeralSettingsContent
	settingsBytes, err := adapter.EphemeralSettingsContent("http://127.0.0.1:8765/mcp", "secret-agy-token")
	if err != nil {
		t.Fatalf("EphemeralSettingsContent error: %v", err)
	}
	if !strings.Contains(string(settingsBytes), "secret-agy-token") || !strings.Contains(string(settingsBytes), "http://127.0.0.1:8765/mcp") {
		t.Errorf("EphemeralSettingsContent content unexpected: %s", string(settingsBytes))
	}

	// 8. ParseLine
	eventLine := []byte(`{"type":"thought","session_id":"sess-2","thought":"thinking..."}`)
	event, err := adapter.ParseLine(eventLine, "sess-2")
	if err != nil {
		t.Fatalf("ParseLine error: %v", err)
	}
	if event.Type != "thought" || event.SessionID != "sess-2" {
		t.Errorf("ParseLine unexpected event: %+v", event)
	}

	// Control response parsing
	ctrlLine := []byte(`{"type":"control_response","response":{"request_id":"req-1","subtype":"success","action":"interrupt"}}`)
	ctrlEvent, err := adapter.ParseLine(ctrlLine, "sess-2")
	if err != nil {
		t.Fatalf("ParseLine error: %v", err)
	}
	if ctrlEvent.Type != "control_response" || ctrlEvent.Control == nil || ctrlEvent.Control.RequestID != "req-1" {
		t.Errorf("ParseLine unexpected control response: %+v", ctrlEvent)
	}
}

func TestCLIAdapter_FakeAgyProcessExecution(t *testing.T) {
	fakeAgy := buildFakeAgy(t)

	// Create ephemeral settings file
	settingsFile := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(settingsFile, []byte(`{"mcpServers":{}}`), 0600); err != nil {
		t.Fatal(err)
	}

	adapter := NewAntigravityAdapter(fakeAgy, os.Environ())
	args := adapter.BuildArgs(CLIProcessConfig{
		SessionID: "proc-sess-1",
		Model:     "gemini-2.5-pro",
	}, settingsFile)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, fakeAgy, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start fake_agy: %v", err)
	}

	// Send user turn
	turnBytes, err := adapter.FormatUserTurn(UserTurnInput{
		ID:        "turn-1",
		SessionID: "proc-sess-1",
		Content:   json.RawMessage(`"hello process"`),
	})
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		defer stdin.Close()
		_, _ = stdin.Write(turnBytes)
		_, _ = stdin.Write([]byte("\n"))
	}()

	decoder := json.NewDecoder(stdout)
	receivedTypes := []string{}
	for decoder.More() {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			break
		}
		event, err := adapter.ParseLine(raw, "proc-sess-1")
		if err != nil {
			t.Fatalf("ParseLine error: %v", err)
		}
		receivedTypes = append(receivedTypes, event.Type)
		if event.Type == "result" {
			break
		}
	}

	_ = cmd.Wait()

	// Verify events: system init, user echo, thought, assistant response, result
	expectedSequence := []string{"system", "user", "thought", "assistant", "result"}
	for _, expected := range expectedSequence {
		found := false
		for _, typ := range receivedTypes {
			if typ == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected event type %q not found in received: %v", expected, receivedTypes)
		}
	}
}
