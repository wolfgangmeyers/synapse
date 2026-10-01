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
	"golang.org/x/sys/unix"
)

// This is a deliberately small command language, not a shell parser.
type embeddedBashCommand struct {
	op      string
	args    []string
	timeout time.Duration
}

func parseEmbeddedBash(args map[string]any) (embeddedBashCommand, error) {
	var c embeddedBashCommand
	if len(args) < 1 || len(args) > 2 {
		return c, errors.New("invalid bash arguments")
	}
	text, ok := args["command"].(string)
	if !ok || len(text) > 4096 || strings.TrimSpace(text) != text || strings.ContainsAny(text, "\t\n\r\\\"'`$;|&<>(){}*?!#") {
		return c, errors.New("invalid bash command")
	}
	fields := strings.Split(text, " ")
	for _, f := range fields {
		if f == "" {
			return c, errors.New("invalid bash spacing")
		}
	}
	if len(args) == 2 {
		v, ok := args["timeout"].(float64)
		if !ok || v < 1 || v > 10 || v != float64(int(v)) {
			return c, errors.New("invalid bash timeout")
		}
		c.timeout = time.Duration(v) * time.Second
	} else {
		c.timeout = 5 * time.Second
	}
	pathStart := 0
	switch {
	case len(fields) == 6 && fields[0] == "grep" && fields[1] == "-F" && fields[2] == "-n" && fields[3] == "--":
		c.op = "grep"
		pathStart = 5
	case len(fields) == 4 && fields[0] == "cmp" && fields[1] == "--":
		c.op = "cmp"
		pathStart = 2
	case len(fields) == 3 && fields[0] == "test" && fields[1] == "-f":
		c.op = "test"
		pathStart = 2
	case len(fields) == 4 && fields[0] == "wc" && fields[1] == "-c" && fields[2] == "--":
		c.op = "wc"
		pathStart = 3
	case len(fields) == 3 && fields[0] == "sha256sum" && fields[1] == "--":
		c.op = "sha256sum"
		pathStart = 2
	default:
		return c, errors.New("unsupported bash command")
	}
	if c.op == "grep" && (fields[4] == "" || strings.HasPrefix(fields[4], "-")) {
		return c, errors.New("invalid grep pattern")
	}
	for _, p := range fields[pathStart:] {
		if len(p) > 4096 || !validEmbeddedReadPath(p) || strings.ContainsAny(p, " \t\n\r\\\"'`$;|&<>(){}*?!#") || strings.HasPrefix(p, "-") {
			return c, errors.New("invalid bash path")
		}
	}
	c.args = fields[pathStart:]
	if c.op == "grep" {
		c.args = append([]string{fields[4]}, c.args...)
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

// Execute only fixed host-authored Bash source. File operands are inherited
// descriptors, never a pathname resolved by the child process.
func (m *Manager) embeddedBashTool(owner embeddedOwner, rootFD int, fresh func(string) bool, produced func(string, ai.ToolResultMessage, bool), failed func(string, embeddedBashFailureClass)) agent.AgentTool {
	return agent.AgentTool{Name: "bash", Description: "Run a restricted read-only command in the saved private project.", Parameters: embeddedBashSchema(), Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
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
		files := make([]*os.File, 0, 2)
		defer func() {
			for _, f := range files {
				_ = f.Close()
			}
		}()
		paths := spec.args
		if spec.op == "grep" {
			paths = paths[1:]
		}
		for _, p := range paths {
			fd, openErr := unix.Openat2(rootFD, p, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
			if openErr != nil {
				return agent.AgentToolResult{}, fmt.Errorf("bash path denied: %w", openErr)
			}
			f := os.NewFile(uintptr(fd), "pinned-bash-file")
			st, statErr := f.Stat()
			if statErr != nil || !st.Mode().IsRegular() {
				_ = f.Close()
				return agent.AgentToolResult{}, errors.New("bash requires regular files")
			}
			files = append(files, f)
		}
		if err = m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
			return agent.AgentToolResult{}, err
		}
		// No caller-supplied source enters -c. Only the validated pattern is an argv element.
		script := ""
		argv := []string{"embedded-fixed-bash"}
		switch spec.op {
		case "grep":
			script = "exec /usr/bin/grep -F -n -- \"$1\" /proc/self/fd/3"
			argv = append(argv, spec.args[0])
		case "cmp":
			script = "exec /usr/bin/cmp -- /proc/self/fd/3 /proc/self/fd/4"
		case "test":
			script = "exec /usr/bin/test -f /proc/self/fd/3"
		case "wc":
			script = "exec /usr/bin/wc -c -- /proc/self/fd/3"
		case "sha256sum":
			script = "exec /usr/bin/sha256sum -- /proc/self/fd/3"
		}
		// Test-only: a private pipe gates the host-authored script after real Bash
		// starts. Pinned operand descriptors retain their original fd numbers.
		var gateRead, gateWrite *os.File
		if m.embeddedBashAfterStart != nil {
			gateRead, gateWrite, err = os.Pipe()
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			defer gateRead.Close()
			defer gateWrite.Close()
			script = fmt.Sprintf("IFS= read -r embedded_gate <&%d || exit 125\n%s", 3+len(files), script)
		}
		runCtx, cancel := context.WithTimeout(ctx, spec.timeout)
		defer cancel()
		cmd := exec.Command("/usr/bin/bash", append([]string{"--noprofile", "--norc", "-c", script}, argv...)...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
		cmd.ExtraFiles = files
		if gateRead != nil {
			cmd.ExtraFiles = append(append([]*os.File(nil), files...), gateRead)
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		out := &limitedBashWriter{limit: 65536, overflow: make(chan struct{})}
		cmd.Stdout = out
		cmd.Stderr = out
		// Bound Wait even if a child leaves the output pipes open.
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
			failed(id, embeddedBashOutputTruncated)
			return agent.AgentToolResult{}, errors.New("bash output truncated: 65536 byte limit exceeded")
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
			if out.truncated() {
				return truncated()
			}
			return agent.AgentToolResult{}, errors.New("bash outcome UNKNOWN after spawn")
		}
		// Wait may win the select when overflow and completion arrive together.
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
		if spec.op == "wc" {
			result += " " + paths[0]
		}
		if exit != 0 && !(spec.op == "grep" && exit == 1) {
			return agent.AgentToolResult{}, errors.New(result)
		}
		content := ai.ContentList{ai.TextContent{Text: result}}
		produced(id, ai.ToolResultMessage{ToolCallID: id, ToolName: "bash", Content: content}, false)
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
