package managed

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"golang.org/x/sys/unix"
)

const (
	embeddedSubagentFileLimit = 512 * 1024
	embeddedSubagentBashLimit = 64 * 1024
	embeddedImageReadLimit    = 10 * 1024 * 1024 // 10 MiB
)

func imageMimeType(p string) (string, bool) {
	ext := strings.ToLower(filepath.Ext(p))
	switch ext {
	case ".png":
		return "image/png", true
	case ".jpg", ".jpeg":
		return "image/jpeg", true
	case ".webp":
		return "image/webp", true
	case ".gif":
		return "image/gif", true
	case ".bmp":
		return "image/bmp", true
	default:
		return "", false
	}
}

// resolveSubagentPath resolves a path argument.
// Relative paths resolve relative to cwd. Absolute paths and ~ expansions
// are preserved and allowed to access anywhere in the filesystem.
func resolveSubagentPath(cwd, p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("path cannot be empty")
	}
	if p == "~" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			home = os.Getenv("HOME")
		}
		if home == "" {
			return "", errors.New("cannot determine home directory")
		}
		return filepath.Clean(home), nil
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~\\") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			home = os.Getenv("HOME")
		}
		if home == "" {
			return "", errors.New("cannot determine home directory")
		}
		return filepath.Clean(filepath.Join(home, p[2:])), nil
	}
	if strings.HasPrefix(p, "file://") {
		p = strings.TrimPrefix(p, "file://")
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p), nil
	}
	return filepath.Clean(filepath.Join(cwd, p)), nil
}

func validateSubagentCwd(cwd string) (string, error) {
	if !filepath.IsAbs(cwd) {
		return "", errors.New("workspace root must be absolute")
	}
	canonical, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", fmt.Errorf("failed to evaluate workspace symlinks: %w", err)
	}
	st, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("workspace directory inaccessible: %w", err)
	}
	if !st.IsDir() {
		return "", errors.New("workspace root must be a directory")
	}
	return canonical, nil
}

// NewEmbeddedSubagentTools constructs the 4 child tools:
// read, edit, write, and bash. Returns the tools, a cleanup function, and any setup error.
func NewEmbeddedSubagentTools(cwd string) ([]agent.AgentTool, func(), error) {
	canonical, err := validateSubagentCwd(cwd)
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() {}
	tools := []agent.AgentTool{
		NewEmbeddedSubagentReadTool(canonical),
		NewEmbeddedSubagentEditTool(canonical),
		NewEmbeddedSubagentWriteTool(canonical),
		NewEmbeddedSubagentBashTool(canonical),
	}
	return tools, cleanup, nil
}

// EmbeddedSubagentTools provides Manager compatibility for constructing subagent tools.
func (m *Manager) EmbeddedSubagentTools(cwd string) ([]agent.AgentTool, func(), error) {
	return NewEmbeddedSubagentTools(cwd)
}

// ---------------------------------------------------------------------------
// read tool
// ---------------------------------------------------------------------------

func embeddedSubagentReadSchema() *ai.Schema {
	closed := false
	s := ai.Object(
		ai.Prop("path", ai.String()),
		ai.Opt("offset", ai.Integer()),
		ai.Opt("limit", ai.Integer()),
	)
	s.AdditionalAllowed = &closed
	return s
}

// NewEmbeddedSubagentReadTool builds a read tool rooted at cwd.
func NewEmbeddedSubagentReadTool(cwd string) agent.AgentTool {
	return agent.AgentTool{
		Name:        "read",
		Description: "Read the contents of a file. Supports text files and images.",
		Parameters:  embeddedSubagentReadSchema(),
		Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if err := ctx.Err(); err != nil {
				return agent.AgentToolResult{}, err
			}
			p, ok := args["path"].(string)
			if !ok || len(p) > 4096 || strings.TrimSpace(p) == "" {
				return agent.AgentToolResult{}, errors.New("invalid or denied read path")
			}
			target, err := resolveSubagentPath(cwd, p)
			if err != nil {
				return agent.AgentToolResult{}, fmt.Errorf("invalid read path: %w", err)
			}
			// Use O_NONBLOCK to prevent hanging on FIFOs
			f, err := os.OpenFile(target, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
			if err != nil {
				return agent.AgentToolResult{}, fmt.Errorf("read denied: %w", err)
			}
			defer f.Close()

			st, err := f.Stat()
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			if st.IsDir() {
				return agent.AgentToolResult{}, errors.New("reading a directory must fail")
			}
			if !st.Mode().IsRegular() {
				return agent.AgentToolResult{}, errors.New("read requires regular file of at most 512 KiB")
			}

			mimeType, isImage := imageMimeType(target)
			if isImage {
				if st.Size() > embeddedImageReadLimit {
					return agent.AgentToolResult{}, errors.New("read requires regular file of at most 10 MiB")
				}
				data, err := io.ReadAll(io.LimitReader(f, embeddedImageReadLimit+1))
				if err != nil {
					return agent.AgentToolResult{}, err
				}
				if int64(len(data)) > embeddedImageReadLimit {
					return agent.AgentToolResult{}, errors.New("read exceeds 10 MiB limit")
				}
				return agent.AgentToolResult{
					Content: ai.ContentList{
						ai.ImageContent{
							Data:     base64.StdEncoding.EncodeToString(data),
							MimeType: mimeType,
						},
					},
				}, nil
			}

			if st.Size() > embeddedSubagentFileLimit {
				return agent.AgentToolResult{}, errors.New("read requires regular file of at most 512 KiB")
			}
			data, err := io.ReadAll(io.LimitReader(f, embeddedSubagentFileLimit+1))
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			if len(data) > embeddedSubagentFileLimit {
				return agent.AgentToolResult{}, errors.New("read exceeds 512 KiB limit")
			}
			raw := string(data)
			offsetVal, hasOffset := args["offset"]
			limitVal, hasLimit := args["limit"]
			if hasOffset || hasLimit {
				lines := strings.Split(raw, "\n")
				startLine := 0
				if hasOffset {
					switch v := offsetVal.(type) {
					case float64:
						if int(v) > 0 {
							startLine = int(v) - 1
						}
					case int:
						if v > 0 {
							startLine = v - 1
						}
					}
				}
				if startLine >= len(lines) {
					return agent.AgentToolResult{}, fmt.Errorf("offset %d beyond end of file", startLine+1)
				}
				endLine := len(lines)
				if hasLimit {
					var lim int
					switch v := limitVal.(type) {
					case float64:
						lim = int(v)
					case int:
						lim = v
					}
					if lim > 0 && startLine+lim < endLine {
						endLine = startLine + lim
					}
				}
				raw = strings.Join(lines[startLine:endLine], "\n")
			}
			return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: raw}}}, nil
		},
	}
}

// ---------------------------------------------------------------------------
// write tool
// ---------------------------------------------------------------------------

func embeddedSubagentWriteSchema() *ai.Schema {
	closed := false
	s := ai.Object(
		ai.Prop("path", ai.String()),
		ai.Prop("content", ai.String()),
	)
	s.AdditionalAllowed = &closed
	return s
}

// NewEmbeddedSubagentWriteTool builds a write tool rooted at cwd that allows
// file creation and overwriting.
func NewEmbeddedSubagentWriteTool(cwd string) agent.AgentTool {
	return agent.AgentTool{
		Name:        "write",
		Description: "Write content to a file. Overwrites existing files.",
		Parameters:  embeddedSubagentWriteSchema(),
		Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if err := ctx.Err(); err != nil {
				return agent.AgentToolResult{}, err
			}
			p, pathOK := args["path"].(string)
			content, contentOK := args["content"].(string)
			if !pathOK || len(p) > 4096 || strings.TrimSpace(p) == "" || !contentOK || len(content) > embeddedSubagentFileLimit {
				return agent.AgentToolResult{}, errors.New("invalid or denied write arguments")
			}
			target, err := resolveSubagentPath(cwd, p)
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			if err := writeEmbeddedSubagentFile(ctx, target, []byte(content)); err != nil {
				return agent.AgentToolResult{}, err
			}
			return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: "Successfully wrote to " + p}}}, nil
		},
	}
}

func writeEmbeddedSubagentFile(ctx context.Context, target string, content []byte) error {
	if len(content) > embeddedSubagentFileLimit {
		return errors.New("write exceeds 512 KiB limit")
	}
	resolvedTarget := target
	if real, err := filepath.EvalSymlinks(target); err == nil {
		resolvedTarget = real
	}
	parent := filepath.Dir(resolvedTarget)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return fmt.Errorf("write parent mkdir denied: %w", err)
	}

	targetMode := os.FileMode(0600)
	if st, err := os.Stat(resolvedTarget); err == nil {
		if st.IsDir() {
			return errors.New("cannot overwrite directory")
		}
		if st.Mode().IsRegular() {
			// Strip setuid/setgid bits safely
			targetMode = st.Mode().Perm() & 0777
		}
	}

	tmpFile, err := os.CreateTemp(parent, ".mecha-subagent-write-*")
	if err != nil {
		return fmt.Errorf("write temporary create denied: %w", err)
	}
	tmpName := tmpFile.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if targetMode != 0600 {
		if err := tmpFile.Chmod(targetMode); err != nil {
			_ = tmpFile.Close()
			return err
		}
	}

	for len(content) > 0 {
		if err := ctx.Err(); err != nil {
			_ = tmpFile.Close()
			return err
		}
		n, writeErr := tmpFile.Write(content)
		if writeErr != nil {
			_ = tmpFile.Close()
			return writeErr
		}
		content = content[n:]
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, resolvedTarget); err != nil {
		return fmt.Errorf("write rename denied: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// edit tool
// ---------------------------------------------------------------------------

func embeddedSubagentEditSchema() *ai.Schema {
	closed := false
	change := ai.Object(ai.Prop("oldText", ai.String()), ai.Prop("newText", ai.String()))
	change.AdditionalAllowed = &closed
	schema := ai.Object(ai.Prop("path", ai.String()), ai.Prop("edits", ai.ArrayOf(change)))
	schema.AdditionalAllowed = &closed
	return schema
}

type subagentEditPair struct {
	oldText string
	newText string
}

type subagentParsedEdit struct {
	oldText    string
	newText    string
	startIndex int
	endIndex   int
}

// NewEmbeddedSubagentEditTool builds an edit tool rooted at cwd that supports
// multiple disjoint edits in a single call.
func NewEmbeddedSubagentEditTool(cwd string) agent.AgentTool {
	return agent.AgentTool{
		Name:        "edit",
		Description: "Edit a file using exact text replacement. Supports multiple non-overlapping edits.",
		Parameters:  embeddedSubagentEditSchema(),
		Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if err := ctx.Err(); err != nil {
				return agent.AgentToolResult{}, err
			}
			p, pathOK := args["path"].(string)
			editsRaw, editsOK := args["edits"].([]any)
			if !pathOK || len(p) > 4096 || strings.TrimSpace(p) == "" || !editsOK || len(editsRaw) == 0 {
				return agent.AgentToolResult{}, errors.New("invalid or denied edit arguments")
			}

			pairs := make([]subagentEditPair, len(editsRaw))
			for i, e := range editsRaw {
				m, ok := e.(map[string]any)
				if !ok {
					return agent.AgentToolResult{}, errors.New("invalid edit item")
				}
				oldText, oldOK := m["oldText"].(string)
				newText, newOK := m["newText"].(string)
				if !oldOK || oldText == "" || len(oldText) > embeddedSubagentFileLimit || !newOK || len(newText) > embeddedSubagentFileLimit {
					return agent.AgentToolResult{}, errors.New("invalid edit replacement texts")
				}
				pairs[i] = subagentEditPair{oldText: oldText, newText: newText}
			}

			target, err := resolveSubagentPath(cwd, p)
			if err != nil {
				return agent.AgentToolResult{}, err
			}

			if err := editEmbeddedSubagentFile(ctx, target, pairs); err != nil {
				return agent.AgentToolResult{}, err
			}
			return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: "Successfully edited " + p}}}, nil
		},
	}
}

func editEmbeddedSubagentFile(ctx context.Context, target string, edits []subagentEditPair) error {
	resolvedTarget := target
	if real, err := filepath.EvalSymlinks(target); err == nil {
		resolvedTarget = real
	}
	file, err := os.OpenFile(resolvedTarget, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("edit target denied: %w", err)
	}
	defer file.Close()

	initial, err := file.Stat()
	if err != nil {
		return err
	}
	if initial.IsDir() {
		return errors.New("editing a directory must be rejected")
	}
	if !initial.Mode().IsRegular() {
		return errors.New("editing non-regular file must be rejected")
	}
	if initial.Size() > embeddedSubagentFileLimit {
		return errors.New("editing file > 512 KiB must be rejected")
	}

	data, err := io.ReadAll(io.LimitReader(file, embeddedSubagentFileLimit+1))
	if err != nil {
		return err
	}
	if len(data) > embeddedSubagentFileLimit {
		return errors.New("edit file exceeds 512 KiB limit")
	}

	// Resolve each edit against original content
	parsed := make([]subagentParsedEdit, len(edits))
	for i, e := range edits {
		count := bytes.Count(data, []byte(e.oldText))
		if count == 0 {
			return fmt.Errorf("edit oldText not found: %q", e.oldText)
		}
		if count > 1 {
			return fmt.Errorf("edit oldText must be unique, found %d occurrences: %q", count, e.oldText)
		}
		idx := bytes.Index(data, []byte(e.oldText))
		parsed[i] = subagentParsedEdit{
			oldText:    e.oldText,
			newText:    e.newText,
			startIndex: idx,
			endIndex:   idx + len(e.oldText),
		}
	}

	// Check for overlap
	sortedAsc := make([]subagentParsedEdit, len(parsed))
	copy(sortedAsc, parsed)
	sort.Slice(sortedAsc, func(i, j int) bool {
		return sortedAsc[i].startIndex < sortedAsc[j].startIndex
	})
	for i := 1; i < len(sortedAsc); i++ {
		if sortedAsc[i].startIndex < sortedAsc[i-1].endIndex {
			return fmt.Errorf("overlapping edits detected: %q and %q", sortedAsc[i-1].oldText, sortedAsc[i].oldText)
		}
	}

	// Apply edits from right to left
	sort.Slice(parsed, func(i, j int) bool {
		return parsed[i].startIndex > parsed[j].startIndex
	})
	result := data
	for _, pe := range parsed {
		head := result[:pe.startIndex]
		tail := result[pe.endIndex:]
		result = append(head, append([]byte(pe.newText), tail...)...)
	}
	if len(result) > embeddedSubagentFileLimit {
		return errors.New("edit expansion exceeds 512 KiB limit")
	}

	parent := filepath.Dir(resolvedTarget)
	tmpFile, err := os.CreateTemp(parent, ".mecha-subagent-edit-*")
	if err != nil {
		return err
	}
	tmpName := tmpFile.Name()
	defer os.Remove(tmpName)

	if err := tmpFile.Chmod(initial.Mode().Perm() & 0777); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if _, err := tmpFile.Write(result); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, resolvedTarget); err != nil {
		return fmt.Errorf("edit rename denied: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// bash tool
// ---------------------------------------------------------------------------

func embeddedSubagentBashSchema() *ai.Schema {
	closed := false
	s := ai.Object(
		ai.Prop("command", ai.String()),
		ai.Opt("timeout", ai.Number()),
	)
	s.AdditionalAllowed = &closed
	return s
}

// NewEmbeddedSubagentBashTool builds an unconfined bash tool rooted at cwd.
func NewEmbeddedSubagentBashTool(cwd string) agent.AgentTool {
	return agent.AgentTool{
		Name:        "bash",
		Description: "Run a bash command in the current working directory.",
		Parameters:  embeddedSubagentBashSchema(),
		Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			command, ok := args["command"].(string)
			if !ok || len(command) > 65536 || strings.TrimSpace(command) == "" {
				return agent.AgentToolResult{}, errors.New("invalid bash command")
			}
			timeout := 30 * time.Second
			if v, exists := args["timeout"]; exists {
				var secs float64
				switch t := v.(type) {
				case float64:
					secs = t
				case int:
					secs = float64(t)
				case int64:
					secs = float64(t)
				}
				if secs >= 1 && secs <= 600 {
					timeout = time.Duration(secs * float64(time.Second))
				}
			}

			if !filepath.IsAbs(cwd) {
				return agent.AgentToolResult{}, errors.New("workspace directory must be absolute")
			}
			canonicalCwd, err := filepath.EvalSymlinks(cwd)
			if err != nil {
				return agent.AgentToolResult{}, fmt.Errorf("workspace directory inaccessible: %w", err)
			}
			info, err := os.Stat(canonicalCwd)
			if err != nil || !info.IsDir() {
				return agent.AgentToolResult{}, fmt.Errorf("workspace directory inaccessible: %w", err)
			}

			return executeSubagentBash(ctx, canonicalCwd, command, timeout)
		},
	}
}

func executeSubagentBash(ctx context.Context, cwd, command string, timeout time.Duration) (agent.AgentToolResult, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "bash", "-c", command)
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	out := &limitedBashWriter{limit: embeddedSubagentBashLimit, overflow: make(chan struct{})}
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.WaitDelay = time.Second

	if err := cmd.Start(); err != nil {
		return agent.AgentToolResult{}, fmt.Errorf("failed to spawn bash: %w", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		exit := 0
		if err != nil {
			var e *exec.ExitError
			if errors.As(err, &e) {
				exit = e.ExitCode()
			} else {
				return agent.AgentToolResult{}, fmt.Errorf("bash outcome unknown: %w", err)
			}
		}
		resultText := out.text()
		if out.truncated() {
			resultText = fmt.Sprintf("%s\n[Output truncated at %d bytes. Narrow your command if full output is needed.]", resultText, out.limit)
		}
		result := fmt.Sprintf("exit=%d\n%s", exit, resultText)
		return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: result}}}, nil

	case <-out.overflow:
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		resultText := out.text()
		result := fmt.Sprintf("exit=0\n%s\n[Output truncated at %d bytes. Narrow your command if full output is needed.]", resultText, out.limit)
		return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: result}}}, nil

	case <-runCtx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		if ctx.Err() != nil {
			return agent.AgentToolResult{}, ctx.Err()
		}
		return agent.AgentToolResult{}, errors.New("bash command timed out")
	}
}
