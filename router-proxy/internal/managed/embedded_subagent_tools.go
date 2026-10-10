package managed

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
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
	embeddedSubagentBashLimit = 512 * 1024
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

// validEmbeddedSubagentPath verifies that a relative path stays within the workspace
// and contains no traversal, absolute prefixes, home expansion, or scheme components.
func validEmbeddedSubagentPath(p string) bool {
	if p == "" || p == "." || strings.HasPrefix(p, "~") || strings.Contains(p, ":") || path.IsAbs(p) || strings.Contains(p, "\\") || path.Clean(p) != p {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// pinEmbeddedChildWorkspace opens and pins an O_PATH descriptor to the workspace root.
// Fails closed if the path is not absolute or not a directory owned by the current EUID.
func pinEmbeddedChildWorkspace(cwd string) (int, string, error) {
	if !filepath.IsAbs(cwd) {
		return -1, "", errors.New("workspace root must be absolute")
	}
	canonical, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return -1, "", fmt.Errorf("failed to evaluate workspace symlinks: %w", err)
	}
	fd, err := openRootDirectory(canonical)
	if err != nil {
		return -1, "", fmt.Errorf("failed to pin child workspace: %w", err)
	}
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return -1, "", err
	}
	if st.Uid != uint32(os.Geteuid()) || st.Mode&unix.S_IFMT != unix.S_IFDIR {
		unix.Close(fd)
		return -1, "", errors.New("workspace root must be a directory owned by the active process UID")
	}
	return fd, canonical, nil
}

// NewEmbeddedSubagentTools constructs the 4 required confined child tools:
// read, edit, write, and bash. Returns the tools, a cleanup function that closes
// the pinned workspace descriptor, and any setup error.
func NewEmbeddedSubagentTools(cwd string) ([]agent.AgentTool, func(), error) {
	rootFD, canonical, err := pinEmbeddedChildWorkspace(cwd)
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() {
		if rootFD >= 0 {
			_ = unix.Close(rootFD)
			rootFD = -1
		}
	}
	tools := []agent.AgentTool{
		NewEmbeddedSubagentReadTool(rootFD),
		NewEmbeddedSubagentEditTool(rootFD),
		NewEmbeddedSubagentWriteTool(rootFD),
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

// NewEmbeddedSubagentReadTool builds a descriptor-rooted read tool.
func NewEmbeddedSubagentReadTool(rootFD int) agent.AgentTool {
	return agent.AgentTool{
		Name:        "read",
		Description: "Read the contents of a bounded file inside the approved child workspace.",
		Parameters:  embeddedSubagentReadSchema(),
		Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if err := ctx.Err(); err != nil {
				return agent.AgentToolResult{}, err
			}
			p, ok := args["path"].(string)
			if !ok || len(p) > 4096 || !validEmbeddedSubagentPath(p) {
				return agent.AgentToolResult{}, errors.New("invalid or denied read path")
			}
			fd, err := openBeneath(rootFD, p)
			if err != nil {
				return agent.AgentToolResult{}, fmt.Errorf("read denied: %w", err)
			}
			f := os.NewFile(uintptr(fd), "subagent-read")
			defer f.Close()

			st, err := f.Stat()
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			if !st.Mode().IsRegular() {
				return agent.AgentToolResult{}, errors.New("read requires regular file of at most 512 KiB")
			}

			mimeType, isImage := imageMimeType(p)
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

// NewEmbeddedSubagentWriteTool builds a descriptor-rooted write tool that allows
// both file creation and overwriting within the child workspace.
func NewEmbeddedSubagentWriteTool(rootFD int) agent.AgentTool {
	return agent.AgentTool{
		Name:        "write",
		Description: "Write content to a file inside the approved child workspace. Overwrites existing files.",
		Parameters:  embeddedSubagentWriteSchema(),
		Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if err := ctx.Err(); err != nil {
				return agent.AgentToolResult{}, err
			}
			p, pathOK := args["path"].(string)
			content, contentOK := args["content"].(string)
			if !pathOK || len(p) > 4096 || !validEmbeddedSubagentPath(p) || !contentOK || len(content) > embeddedSubagentFileLimit {
				return agent.AgentToolResult{}, errors.New("invalid or denied write arguments")
			}
			if err := writeEmbeddedSubagentFile(ctx, rootFD, p, []byte(content)); err != nil {
				return agent.AgentToolResult{}, err
			}
			return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: "Successfully wrote to " + p}}}, nil
		},
	}
}

func writeEmbeddedSubagentFile(ctx context.Context, rootFD int, p string, content []byte) error {
	if len(content) > embeddedSubagentFileLimit {
		return errors.New("write exceeds 512 KiB limit")
	}
	parts := strings.Split(p, "/")
	parent, err := unix.Openat(rootFD, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() {
		if parent >= 0 {
			_ = unix.Close(parent)
		}
	}()

	for _, component := range parts[:len(parts)-1] {
		if err := ctx.Err(); err != nil {
			return err
		}
		fd, openErr := unix.Openat(parent, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if errors.Is(openErr, unix.ENOENT) {
			if err := unix.Mkdirat(parent, component, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
				return fmt.Errorf("write parent mkdir denied: %w", err)
			}
			fd, openErr = unix.Openat(parent, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			if openErr == nil {
				_ = unix.Fsync(parent)
			}
		}
		if openErr != nil {
			return fmt.Errorf("write parent directory denied: %w", openErr)
		}
		_ = unix.Close(parent)
		parent = fd
	}

	fileName := parts[len(parts)-1]
	// Check if target already exists as a symlink before writing; symlinks are forbidden targets.
	var st unix.Stat_t
	targetMode := uint32(0600)
	if err := unix.Fstatat(parent, fileName, &st, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		if st.Mode&unix.S_IFMT == unix.S_IFLNK {
			return errors.New("write denied: target is a symlink")
		}
		if st.Mode&unix.S_IFMT == unix.S_IFDIR {
			return errors.New("write denied: target is a directory")
		}
		if st.Mode&unix.S_IFMT == unix.S_IFREG {
			// Preserve existing regular destination permission bits safely (ordinary 0777, stripping setuid/setgid)
			targetMode = uint32(st.Mode & 0777)
		}
	}

	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := ".mecha-subagent-write-" + hex.EncodeToString(nonce[:])
	fd, err := unix.Openat(parent, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return fmt.Errorf("write temporary create denied: %w", err)
	}
	defer func() { _ = unix.Unlinkat(parent, tmp, 0) }()

	if targetMode != 0600 {
		if err := unix.Fchmod(fd, targetMode); err != nil {
			_ = unix.Close(fd)
			return err
		}
	}

	f := os.NewFile(uintptr(fd), "subagent-write-tmp")
	for len(content) > 0 {
		if err := ctx.Err(); err != nil {
			_ = f.Close()
			return err
		}
		n, writeErr := f.Write(content)
		if writeErr != nil {
			_ = f.Close()
			return writeErr
		}
		content = content[n:]
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// Rename into place: overwrites existing regular files atomically without RENAME_NOREPLACE limitation.
	if err := unix.Renameat(parent, tmp, parent, fileName); err != nil {
		return fmt.Errorf("write rename denied: %w", err)
	}
	_ = unix.Fsync(parent)
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

// NewEmbeddedSubagentEditTool builds a descriptor-rooted edit tool that supports
// multiple disjoint edits in a single call.
func NewEmbeddedSubagentEditTool(rootFD int) agent.AgentTool {
	return agent.AgentTool{
		Name:        "edit",
		Description: "Edit a file inside the approved child workspace using exact text replacement. Supports multiple non-overlapping edits.",
		Parameters:  embeddedSubagentEditSchema(),
		Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if err := ctx.Err(); err != nil {
				return agent.AgentToolResult{}, err
			}
			p, pathOK := args["path"].(string)
			editsRaw, editsOK := args["edits"].([]any)
			if !pathOK || len(p) > 4096 || !validEmbeddedSubagentPath(p) || !editsOK || len(editsRaw) == 0 {
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

			if err := editEmbeddedSubagentFile(ctx, rootFD, p, pairs); err != nil {
				return agent.AgentToolResult{}, err
			}
			return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: "Successfully edited " + p}}}, nil
		},
	}
}

func editEmbeddedSubagentFile(ctx context.Context, rootFD int, p string, edits []subagentEditPair) error {
	parts := strings.Split(p, "/")
	parent, err := unix.Openat(rootFD, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() {
		if parent >= 0 {
			_ = unix.Close(parent)
		}
	}()

	for _, part := range parts[:len(parts)-1] {
		next, err := unix.Openat(parent, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return fmt.Errorf("edit parent denied: %w", err)
		}
		_ = unix.Close(parent)
		parent = next
	}

	fileName := parts[len(parts)-1]
	fd, err := unix.Openat(parent, fileName, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("edit target denied: %w", err)
	}
	file := os.NewFile(uintptr(fd), "subagent-edit-target")
	defer file.Close()

	var initial unix.Stat_t
	if err := unix.Fstat(fd, &initial); err != nil {
		return err
	}
	if initial.Mode&unix.S_IFMT != unix.S_IFREG || initial.Size > embeddedSubagentFileLimit {
		return errors.New("edit requires bounded regular file")
	}

	data, err := io.ReadAll(io.LimitReader(file, embeddedSubagentFileLimit+1))
	if err != nil {
		return err
	}
	if len(data) > embeddedSubagentFileLimit {
		return errors.New("edit file exceeds 512 KiB limit")
	}

	// Resolve each edit against the original file content
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

	// Check for overlap between edits
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

	// Apply edits from right to left so indices remain intact
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
		return errors.New("edit result exceeds 512 KiB limit")
	}

	// Atomic write via temp file and rename
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := ".mecha-subagent-edit-" + hex.EncodeToString(nonce[:])
	tempFD, err := unix.Openat(parent, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(parent, tmp, 0)

	temp := os.NewFile(uintptr(tempFD), "subagent-edit-temp")
	if err := unix.Fchmod(tempFD, uint32(initial.Mode&0777)); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(result); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := unix.Renameat(parent, tmp, parent, fileName); err != nil {
		return fmt.Errorf("edit rename denied: %w", err)
	}
	_ = unix.Fsync(parent)
	return nil
}

// ---------------------------------------------------------------------------
// bash tool (confined via bubblewrap)
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

// NewEmbeddedSubagentBashTool builds a bwrap-confined bash tool.
// Confines the subprocess to:
//   - workspace directory writable (--bind <cwd> <cwd>)
//   - runtime libraries and binaries readonly (/usr, /bin, /lib, /lib64, /sbin)
//   - private /tmp, /dev, /proc (via --unshare-pid)
//   - no host /home or /etc host data mounted
//   - fails closed if bwrap is missing or fails.
func NewEmbeddedSubagentBashTool(cwd string) agent.AgentTool {
	return agent.AgentTool{
		Name:        "bash",
		Description: "Run a bash command confined to the child workspace.",
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

			bwrapPath, err := findBwrap()
			if err != nil {
				return agent.AgentToolResult{}, fmt.Errorf("sandbox unavailable: %w", err)
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

			return executeConfinedBash(ctx, bwrapPath, canonicalCwd, command, timeout)
		},
	}
}

func findBwrap() (string, error) {
	for _, candidate := range []string{"/usr/bin/bwrap", "/bin/bwrap"} {
		if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() && fi.Mode()&0111 != 0 {
			return candidate, nil
		}
	}
	p, err := exec.LookPath("bwrap")
	if err != nil {
		return "", errors.New("bwrap executable not found")
	}
	return p, nil
}

func executeConfinedBash(ctx context.Context, bwrapPath, cwd, command string, timeout time.Duration) (agent.AgentToolResult, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	bwrapArgs := []string{
		"--clearenv",
		"--die-with-parent",
		"--unshare-pid",
		"--ro-bind", "/usr", "/usr",
	}

	for _, p := range []string{"/bin", "/lib", "/lib64", "/sbin"} {
		fi, err := os.Lstat(p)
		if err != nil {
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			if err == nil {
				bwrapArgs = append(bwrapArgs, "--symlink", target, p)
			}
		} else if fi.IsDir() {
			bwrapArgs = append(bwrapArgs, "--ro-bind", p, p)
		}
	}

	bwrapArgs = append(bwrapArgs,
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
	)

	targetDir := cwd
	if cwd == "/tmp" {
		targetDir = "/workspace"
		bwrapArgs = append(bwrapArgs, "--bind", cwd, "/workspace", "--chdir", "/workspace")
	} else {
		bwrapArgs = append(bwrapArgs, "--bind", cwd, cwd, "--chdir", cwd)
	}

	bwrapArgs = append(bwrapArgs,
		"--setenv", "PATH", "/usr/bin:/bin:/usr/sbin:/sbin",
		"--setenv", "HOME", targetDir,
		"--setenv", "TMPDIR", "/tmp",
		"--setenv", "TERM", "dumb",
		"--setenv", "LANG", "C.UTF-8",
		"/bin/bash", "-c", command,
	)

	cmd := exec.CommandContext(runCtx, bwrapPath, bwrapArgs...)
	cmd.Dir = cwd
	cmd.Env = []string{}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	out := &limitedBashWriter{limit: embeddedSubagentBashLimit, overflow: make(chan struct{})}
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.WaitDelay = time.Second

	if err := cmd.Start(); err != nil {
		return agent.AgentToolResult{}, fmt.Errorf("failed to spawn confined bash: %w", err)
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
