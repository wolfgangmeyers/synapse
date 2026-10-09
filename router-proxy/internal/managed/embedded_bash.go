package managed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

type embeddedBashCommand struct {
	command string
	timeout time.Duration
}

func parseEmbeddedBash(args map[string]any) (embeddedBashCommand, error) {
	var c embeddedBashCommand
	if len(args) < 1 || len(args) > 2 {
		return c, errors.New("invalid bash arguments")
	}
	text, ok := args["command"].(string)
	if !ok || len(text) > 65536 || strings.TrimSpace(text) == "" {
		return c, errors.New("invalid bash command")
	}
	c.command = text
	if val, exists := args["timeout"]; exists {
		var secs float64
		switch t := val.(type) {
		case float64:
			secs = t
		case int:
			secs = float64(t)
		case int64:
			secs = float64(t)
		default:
			return c, errors.New("invalid bash timeout")
		}
		if secs < 1 || secs > 600 {
			return c, errors.New("invalid bash timeout")
		}
		c.timeout = time.Duration(secs * float64(time.Second))
	} else {
		c.timeout = 120 * time.Second
	}
	return c, nil
}

func embeddedBashSchema() *ai.Schema {
	closed := false
	s := ai.Object(ai.Prop("command", ai.String()), ai.Opt("timeout", ai.Number()))
	s.AdditionalAllowed = &closed
	return s
}

// Only fixed runner branches may set a trusted Bash failure class.
type embeddedBashFailureClass uint8

const embeddedBashOutputTruncated embeddedBashFailureClass = 1

func (m *Manager) embeddedBashTool(owner embeddedOwner, cwd string, fresh func(string) bool, produced func(string, ai.ToolResultMessage, bool), failed func(string, embeddedBashFailureClass)) agent.AgentTool {
	return agent.AgentTool{
		Name:             "bash",
		Description:      "Run a bash command in the current working directory.",
		PromptGuidelines: []string{
			"When you run a command to investigate, continue until the task is complete, or write your answer to the user.",
			"When a user asks a question or steers your work and you need to gather information or run tools to formulate your answer, always acknowledge the user with a brief conversational response first before starting information gathering.",
		},
		Parameters:       embeddedBashSchema(),
		Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
		effect, err := m.store.RecoverEmbeddedEffect(ctx, owner.binding, id)
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		var original struct {
			ID        string         `json:"id"`
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err = json.Unmarshal(effect.Intent, &original); err != nil {
			return agent.AgentToolResult{}, err
		}
		a, _ := json.Marshal(args)
		b, _ := json.Marshal(original.Arguments)
		if original.ID != id || original.Name != "bash" || !bytes.Equal(a, b) {
			return agent.AgentToolResult{}, errors.New("bash intent mismatch")
		}
		if effect.State == "observed" {
			message, decodeErr := ai.UnmarshalMessage(effect.Result)
			if decodeErr != nil {
				return agent.AgentToolResult{}, decodeErr
			}
			saved, ok := message.(ai.ToolResultMessage)
			if !ok || saved.ToolCallID != id || saved.ToolName != "bash" || saved.IsError {
				return agent.AgentToolResult{}, errors.New("invalid saved bash result")
			}
			produced(id, saved, true)
			return agent.AgentToolResult{Content: saved.Content}, nil
		}
		if !fresh(id) {
			return agent.AgentToolResult{}, errors.New("bash outcome unknown: no fresh admission")
		}
		spec, err := parseEmbeddedBash(original.Arguments)
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		if err = m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
			return agent.AgentToolResult{}, err
		}

		var gateRead, gateWrite *os.File
		if m.embeddedBashAfterStart != nil {
			gateRead, gateWrite, err = os.Pipe()
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			defer gateRead.Close()
			defer gateWrite.Close()
		}

		runCtx, cancel := context.WithTimeout(ctx, spec.timeout)
		defer cancel()

		script := spec.command
		if gateRead != nil {
			script = fmt.Sprintf("IFS= read -r embedded_gate <&3 || exit 125\n%s", spec.command)
		}
		cmd := exec.CommandContext(runCtx, "bash", "-c", script)
		cmd.Dir = cwd
		cmd.Env = os.Environ()
		if gateRead != nil {
			cmd.ExtraFiles = []*os.File{gateRead}
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		out := &limitedBashWriter{limit: 524288, overflow: make(chan struct{})}
		cmd.Stdout = out
		cmd.Stderr = out
		cmd.WaitDelay = time.Second
		if err := cmd.Start(); err != nil {
			return agent.AgentToolResult{}, err
		}
		if gateRead != nil {
			_ = gateRead.Close()
			gateRead = nil
			m.embeddedBashAfterStart(cmd.Process.Pid, gateWrite)
		}
		truncated := func() (agent.AgentToolResult, error) {
			result := fmt.Sprintf("exit=0\n%s\n[Output truncated at %d bytes. Narrow your command if full output is needed.]", out.text(), out.limit)
			content := ai.ContentList{ai.TextContent{Text: result}}
			produced(id, ai.ToolResultMessage{ToolCallID: id, ToolName: "bash", Content: content, IsError: false}, false)
			return agent.AgentToolResult{Content: content}, nil
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err = <-done:
		case <-out.overflow:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
			}
			return truncated()
		case <-runCtx.Done():
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
			}
			if ctx.Err() != nil {
				return agent.AgentToolResult{}, ctx.Err()
			}
			if out.truncated() {
				return truncated()
			}
			result := fmt.Sprintf("exit=124\n[Command timed out after %s]\n%s", spec.timeout, out.text())
			content := ai.ContentList{ai.TextContent{Text: result}}
			produced(id, ai.ToolResultMessage{ToolCallID: id, ToolName: "bash", Content: content, IsError: false}, false)
			return agent.AgentToolResult{Content: content}, nil
		}
		if out.truncated() {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			return truncated()
		}
		exit := 0
		if err != nil {
			var e *exec.ExitError
			if !errors.As(err, &e) {
				return agent.AgentToolResult{}, errors.New("bash outcome UNKNOWN after spawn")
			}
			exit = e.ExitCode()
		}
		result := fmt.Sprintf("exit=%d\n%s", exit, out.text())
		content := ai.ContentList{ai.TextContent{Text: result}}
		produced(id, ai.ToolResultMessage{ToolCallID: id, ToolName: "bash", Content: content, IsError: false}, false)
		return agent.AgentToolResult{Content: content}, nil
	}}
}

type limitedBashWriter struct {
	mu       sync.Mutex
	dst      bytes.Buffer
	limit    int
	overflow chan struct{}
	cut      bool
}

func (w *limitedBashWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	if n > w.limit-w.dst.Len() {
		if !w.cut {
			w.cut = true
			close(w.overflow)
		}
	}
	if remaining := w.limit - w.dst.Len(); remaining > 0 {
		_, _ = w.dst.Write(p[:min(n, remaining)])
	}
	return n, nil
}

func (w *limitedBashWriter) truncated() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cut
}

func (w *limitedBashWriter) text() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dst.String()
}

var _ io.Writer = (*limitedBashWriter)(nil)
