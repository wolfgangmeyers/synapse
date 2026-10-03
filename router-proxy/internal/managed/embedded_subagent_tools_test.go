package managed

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestEmbeddedSubagentToolsLifecycle(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()

	tools, cleanup, err := NewEmbeddedSubagentTools(ws)
	require.NoError(t, err)
	defer cleanup()

	require.Len(t, tools, 4)
	names := []string{tools[0].Name, tools[1].Name, tools[2].Name, tools[3].Name}
	require.Equal(t, []string{"read", "edit", "write", "bash"}, names)

	readTool := tools[0]
	editTool := tools[1]
	writeTool := tools[2]
	bashTool := tools[3]

	// 1. Write - create new file inside workspace
	res, err := writeTool.Execute(ctx, "w1", map[string]any{
		"path":    "hello.txt",
		"content": "hello world\nline 2\nline 3\n",
	}, nil)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].(ai.TextContent).Text, "Successfully wrote")

	// 2. Read - inside workspace
	res, err = readTool.Execute(ctx, "r1", map[string]any{
		"path": "hello.txt",
	}, nil)
	require.NoError(t, err)
	require.Equal(t, "hello world\nline 2\nline 3\n", res.Content[0].(ai.TextContent).Text)

	// 2b. Read with offset and limit
	res, err = readTool.Execute(ctx, "r1-paged", map[string]any{
		"path":   "hello.txt",
		"offset": 2,
		"limit":  1,
	}, nil)
	require.NoError(t, err)
	require.Equal(t, "line 2", res.Content[0].(ai.TextContent).Text)

	// 3. Write - overwrite existing file inside workspace (proves NO create-only limitation!)
	res, err = writeTool.Execute(ctx, "w2", map[string]any{
		"path":    "hello.txt",
		"content": "overwritten content\nsecond line\nthird line\n",
	}, nil)
	require.NoError(t, err)

	res, err = readTool.Execute(ctx, "r2", map[string]any{
		"path": "hello.txt",
	}, nil)
	require.NoError(t, err)
	require.Equal(t, "overwritten content\nsecond line\nthird line\n", res.Content[0].(ai.TextContent).Text)

	// 3b. Write nested directory automatically creates parents
	res, err = writeTool.Execute(ctx, "w-nested", map[string]any{
		"path":    "nested/sub/created.txt",
		"content": "deeply nested file",
	}, nil)
	require.NoError(t, err)
	res, err = readTool.Execute(ctx, "r-nested", map[string]any{
		"path": "nested/sub/created.txt",
	}, nil)
	require.NoError(t, err)
	require.Equal(t, "deeply nested file", res.Content[0].(ai.TextContent).Text)

	// 4. Edit - single edit inside workspace
	res, err = editTool.Execute(ctx, "e1", map[string]any{
		"path": "hello.txt",
		"edits": []any{
			map[string]any{"oldText": "overwritten", "newText": "modified"},
		},
	}, nil)
	require.NoError(t, err)

	// 5. Edit - multiple edits inside workspace in single call (proves NO single-edit limitation!)
	res, err = editTool.Execute(ctx, "e2", map[string]any{
		"path": "hello.txt",
		"edits": []any{
			map[string]any{"oldText": "modified", "newText": "updated"},
			map[string]any{"oldText": "third line", "newText": "final line"},
		},
	}, nil)
	require.NoError(t, err)

	res, err = readTool.Execute(ctx, "r3", map[string]any{
		"path": "hello.txt",
	}, nil)
	require.NoError(t, err)
	require.Equal(t, "updated content\nsecond line\nfinal line\n", res.Content[0].(ai.TextContent).Text)

	// 6. Bash - inside workspace execution and writable filesystem
	res, err = bashTool.Execute(ctx, "b1", map[string]any{
		"command": "echo 'from bash' > bash_out.txt && cat bash_out.txt",
	}, nil)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].(ai.TextContent).Text, "from bash")
	content, err := os.ReadFile(filepath.Join(ws, "bash_out.txt"))
	require.NoError(t, err)
	require.Equal(t, "from bash\n", string(content))
}

func TestEmbeddedSubagentToolsDenials(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()
	outsideDir := t.TempDir()

	outsideFile := filepath.Join(outsideDir, "secret.txt")
	require.NoError(t, os.WriteFile(outsideFile, []byte("sensitive-secret-token\n"), 0600))

	// Create symlink pointing outside workspace
	symlinkEscape := filepath.Join(ws, "escape_symlink.txt")
	require.NoError(t, os.Symlink(outsideFile, symlinkEscape))

	// Create directory symlink pointing outside
	symlinkDirEscape := filepath.Join(ws, "escape_dir")
	require.NoError(t, os.Symlink(outsideDir, symlinkDirEscape))

	tools, cleanup, err := NewEmbeddedSubagentTools(ws)
	require.NoError(t, err)
	defer cleanup()

	readTool := tools[0]
	editTool := tools[1]
	writeTool := tools[2]
	bashTool := tools[3]

	// 1. Read outside absolute denial
	_, err = readTool.Execute(ctx, "r-abs", map[string]any{"path": outsideFile}, nil)
	require.Error(t, err)
	_, err = readTool.Execute(ctx, "r-etc", map[string]any{"path": "/etc/passwd"}, nil)
	require.Error(t, err)

	// 2. Read outside traversal denial
	for _, bad := range []string{"../secret.txt", "nested/../../secret.txt", "./hello.txt", "file:///etc/passwd", "~/secret.txt", ""} {
		_, err = readTool.Execute(ctx, "r-bad", map[string]any{"path": bad}, nil)
		require.Error(t, err, "expected error for path: %s", bad)
	}

	// 3. Read outside symlink denial
	_, err = readTool.Execute(ctx, "r-sym", map[string]any{"path": "escape_symlink.txt"}, nil)
	require.Error(t, err)
	_, err = readTool.Execute(ctx, "r-sym-dir", map[string]any{"path": "escape_dir/secret.txt"}, nil)
	require.Error(t, err)

	// 4. Write outside absolute denial
	_, err = writeTool.Execute(ctx, "w-abs", map[string]any{"path": outsideFile, "content": "attack"}, nil)
	require.Error(t, err)
	_, err = writeTool.Execute(ctx, "w-etc", map[string]any{"path": "/etc/hacked.txt", "content": "attack"}, nil)
	require.Error(t, err)

	// 5. Write outside traversal denial
	_, err = writeTool.Execute(ctx, "w-dotdot", map[string]any{"path": "../hacked.txt", "content": "attack"}, nil)
	require.Error(t, err)
	_, err = writeTool.Execute(ctx, "w-dotdot2", map[string]any{"path": "sub/../../hacked.txt", "content": "attack"}, nil)
	require.Error(t, err)

	// 6. Write outside symlink denial
	_, err = writeTool.Execute(ctx, "w-sym", map[string]any{"path": "escape_symlink.txt", "content": "attack"}, nil)
	require.Error(t, err)
	_, err = writeTool.Execute(ctx, "w-sym-dir", map[string]any{"path": "escape_dir/hacked.txt", "content": "attack"}, nil)
	require.Error(t, err)

	// 7. Edit outside absolute denial
	_, err = editTool.Execute(ctx, "e-abs", map[string]any{
		"path":  outsideFile,
		"edits": []any{map[string]any{"oldText": "sensitive", "newText": "hacked"}},
	}, nil)
	require.Error(t, err)

	// 8. Edit outside traversal denial
	_, err = editTool.Execute(ctx, "e-dotdot", map[string]any{
		"path":  "../secret.txt",
		"edits": []any{map[string]any{"oldText": "sensitive", "newText": "hacked"}},
	}, nil)
	require.Error(t, err)

	// 9. Edit outside symlink denial
	_, err = editTool.Execute(ctx, "e-sym", map[string]any{
		"path":  "escape_symlink.txt",
		"edits": []any{map[string]any{"oldText": "sensitive", "newText": "hacked"}},
	}, nil)
	require.Error(t, err)

	// Verify outside file remained untouched!
	outsideContent, err := os.ReadFile(outsideFile)
	require.NoError(t, err)
	require.Equal(t, "sensitive-secret-token\n", string(outsideContent))

	// 10. Bash outside access denial
	// In the sandbox: /etc does not exist, host files do not exist, symlinks pointing to host data fail
	res, err := bashTool.Execute(ctx, "b-etc", map[string]any{
		"command": "cat /etc/passwd || echo 'cat /etc/passwd failed'",
	}, nil)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].(ai.TextContent).Text, "cat /etc/passwd failed")

	res, err = bashTool.Execute(ctx, "b-sym", map[string]any{
		"command": "cat escape_symlink.txt || echo 'cat symlink failed'",
	}, nil)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].(ai.TextContent).Text, "cat symlink failed")

	// Readonly runtime libs: cannot modify /usr
	res, err = bashTool.Execute(ctx, "b-ro", map[string]any{
		"command": "touch /usr/hacked || echo 'write to /usr denied'",
	}, nil)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].(ai.TextContent).Text, "write to /usr denied")
}

func TestEmbeddedSubagentEditEdgeCases(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()

	tools, cleanup, err := NewEmbeddedSubagentTools(ws)
	require.NoError(t, err)
	defer cleanup()

	writeTool := tools[2]
	editTool := tools[1]

	// Seed test file
	_, err = writeTool.Execute(ctx, "w1", map[string]any{
		"path":    "target.txt",
		"content": "alpha beta gamma delta alpha\n",
	}, nil)
	require.NoError(t, err)

	// 1. Non-unique oldText rejected
	_, err = editTool.Execute(ctx, "e-dup", map[string]any{
		"path": "target.txt",
		"edits": []any{
			map[string]any{"oldText": "alpha", "newText": "omega"},
		},
	}, nil)
	require.ErrorContains(t, err, "must be unique")

	// 2. Non-existent oldText rejected
	_, err = editTool.Execute(ctx, "e-missing", map[string]any{
		"path": "target.txt",
		"edits": []any{
			map[string]any{"oldText": "zeta", "newText": "omega"},
		},
	}, nil)
	require.ErrorContains(t, err, "not found")

	// 3. Overlapping edits rejected
	_, err = editTool.Execute(ctx, "e-overlap", map[string]any{
		"path": "target.txt",
		"edits": []any{
			map[string]any{"oldText": "beta gamma", "newText": "one"},
			map[string]any{"oldText": "gamma delta", "newText": "two"},
		},
	}, nil)
	require.ErrorContains(t, err, "overlapping edits")

	// 4. Empty oldText rejected
	_, err = editTool.Execute(ctx, "e-empty", map[string]any{
		"path": "target.txt",
		"edits": []any{
			map[string]any{"oldText": "", "newText": "something"},
		},
	}, nil)
	require.Error(t, err)
}

func TestEmbeddedSubagentBashIsolation(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()

	tools, cleanup, err := NewEmbeddedSubagentTools(ws)
	require.NoError(t, err)
	defer cleanup()

	bashTool := tools[3]

	// 1. Private /proc: ps aux only reveals PID 1 (bwrap) and the command itself
	res, err := bashTool.Execute(ctx, "b-proc", map[string]any{
		"command": "ps aux",
	}, nil)
	require.NoError(t, err)
	output := res.Content[0].(ai.TextContent).Text
	require.NotContains(t, output, "systemd", "/proc must be private, no host systemd visible")
	require.NotContains(t, output, "dockerd", "/proc must be private, no host dockerd visible")

	// 2. Private /tmp: create a secret in host /tmp, ensure sandbox /tmp does not see it
	hostSecret := filepath.Join(os.TempDir(), fmt.Sprintf("host-secret-%d.txt", time.Now().UnixNano()))
	require.NoError(t, os.WriteFile(hostSecret, []byte("host-secret-data"), 0600))
	defer os.Remove(hostSecret)

	res, err = bashTool.Execute(ctx, "b-tmp", map[string]any{
		"command": fmt.Sprintf("cat %s || echo 'host tmp inaccessible'", hostSecret),
	}, nil)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].(ai.TextContent).Text, "host tmp inaccessible")

	// 3. No host /home data: verify that host user home files are not exposed
	res, err = bashTool.Execute(ctx, "b-home", map[string]any{
		"command": "ls /home/wolfgang/.bashrc || echo 'host home inaccessible'",
	}, nil)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].(ai.TextContent).Text, "host home inaccessible")
}

func TestEmbeddedSubagentBashClearenvSentinel(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()

	sentinel := "router-secret-token-leak-probe-998877"
	t.Setenv("ROUTER_TEST_SECRET_SENTINEL", sentinel)

	tools, cleanup, err := NewEmbeddedSubagentTools(ws)
	require.NoError(t, err)
	defer cleanup()

	bashTool := tools[3]
	res, err := bashTool.Execute(ctx, "b-sentinel", map[string]any{
		"command": `echo "LEAKED=$ROUTER_TEST_SECRET_SENTINEL"`,
	}, nil)
	require.NoError(t, err)
	output := res.Content[0].(ai.TextContent).Text
	require.Contains(t, output, "LEAKED=")
	require.NotContains(t, output, sentinel, "host inherited tokens must not leak into subagent bash")
}

func TestEmbeddedSubagentBashTmpRootPreservation(t *testing.T) {
	ctx := context.Background()
	// Test bash execution when cwd is /tmp
	tool := NewEmbeddedSubagentBashTool("/tmp")

	res, err := tool.Execute(ctx, "b-tmp-root", map[string]any{
		"command": "pwd; touch /tmp/subagent_probe_tmp.txt && ls /tmp/subagent_probe_tmp.txt",
	}, nil)
	require.NoError(t, err)
	output := res.Content[0].(ai.TextContent).Text
	require.Contains(t, output, "/workspace")
	require.Contains(t, output, "/tmp/subagent_probe_tmp.txt")

	// Ensure private tmpfs did not touch host /tmp
	_, err = os.Stat("/tmp/subagent_probe_tmp.txt")
	require.ErrorIs(t, err, os.ErrNotExist, "private tmpfs must not pollute host /tmp")
}

func TestEmbeddedSubagentBashCancellationVsTimeout(t *testing.T) {
	ws := t.TempDir()
	tools, cleanup, err := NewEmbeddedSubagentTools(ws)
	require.NoError(t, err)
	defer cleanup()

	bashTool := tools[3]

	// 1. Parent context cancellation returns context.Canceled
	parentCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := bashTool.Execute(parentCtx, "b-cancel", map[string]any{
			"command": "sleep 10",
		}, nil)
		errCh <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	err = <-errCh
	require.ErrorIs(t, err, context.Canceled, "parent cancellation must return context.Canceled")

	// 2. Command timeout returns command timed out error
	start := time.Now()
	_, err = bashTool.Execute(context.Background(), "b-timeout", map[string]any{
		"command": "sleep 10",
		"timeout": 1,
	}, nil)
	require.ErrorContains(t, err, "timed out")
	require.NotErrorIs(t, err, context.Canceled)
	require.WithinDuration(t, start.Add(1*time.Second), time.Now(), 2*time.Second)
}

func TestEmbeddedSubagentToolsWorkspaceValidation(t *testing.T) {
	// Relative path rejected
	_, _, err := NewEmbeddedSubagentTools("relative/path")
	require.Error(t, err)

	// Non-existent path rejected
	_, _, err = NewEmbeddedSubagentTools("/nonexistent/directory/path/for/subagent")
	require.Error(t, err)

	// Regular file (not directory) rejected
	f, err := os.CreateTemp("", "subagent-not-dir")
	require.NoError(t, err)
	defer os.Remove(f.Name())
	f.Close()

	_, _, err = NewEmbeddedSubagentTools(f.Name())
	require.Error(t, err)
}

func TestEmbeddedSubagentWritePreservesExecutableMode(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()

	scriptPath := filepath.Join(ws, "run.sh")
	require.NoError(t, os.WriteFile(scriptPath, []byte("#!/bin/sh\necho hello\n"), 0755))

	tools, cleanup, err := NewEmbeddedSubagentTools(ws)
	require.NoError(t, err)
	defer cleanup()

	writeTool := tools[2]
	_, err = writeTool.Execute(ctx, "w-exec", map[string]any{
		"path":    "run.sh",
		"content": "#!/bin/sh\necho updated\n",
	}, nil)
	require.NoError(t, err)

	info, err := os.Stat(scriptPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0755), info.Mode().Perm(), "overwriting an executable script must preserve 0755 mode")
}

func TestEmbeddedSubagentWriteStripsSetuidSetgid(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()

	scriptPath := filepath.Join(ws, "suid.sh")
	require.NoError(t, os.WriteFile(scriptPath, []byte("#!/bin/sh\necho test\n"), 0755))
	// Attempt to set setuid or setgid bit
	_ = os.Chmod(scriptPath, 0755|os.ModeSetuid|os.ModeSetgid)

	tools, cleanup, err := NewEmbeddedSubagentTools(ws)
	require.NoError(t, err)
	defer cleanup()

	writeTool := tools[2]
	_, err = writeTool.Execute(ctx, "w-suid", map[string]any{
		"path":    "suid.sh",
		"content": "#!/bin/sh\necho updated\n",
	}, nil)
	require.NoError(t, err)

	info, err := os.Stat(scriptPath)
	require.NoError(t, err)
	// Must only have 0755; no setuid/setgid bits preserved
	require.Equal(t, os.FileMode(0755), info.Mode().Perm())
	require.Zero(t, info.Mode()&(os.ModeSetuid|os.ModeSetgid), "setuid/setgid bits must not be propagated")
}

func TestEmbeddedSubagentReadDenialsEdgeCases(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()

	tools, cleanup, err := NewEmbeddedSubagentTools(ws)
	require.NoError(t, err)
	defer cleanup()

	readTool := tools[0]

	// 1. Read directory rejected
	subDir := filepath.Join(ws, "subdir")
	require.NoError(t, os.Mkdir(subDir, 0755))
	_, err = readTool.Execute(ctx, "r-dir", map[string]any{"path": "subdir"}, nil)
	require.Error(t, err, "reading a directory must fail")

	// 2. Read FIFO/nonregular rejected without hanging
	fifoPath := filepath.Join(ws, "test_fifo")
	require.NoError(t, unix.Mkfifo(fifoPath, 0600))
	_, err = readTool.Execute(ctx, "r-fifo", map[string]any{"path": "test_fifo"}, nil)
	require.Error(t, err, "reading a FIFO must fail without hanging")

	// 3. Read file > 64 KiB rejected
	largeFile := filepath.Join(ws, "large.txt")
	require.NoError(t, os.WriteFile(largeFile, []byte(strings.Repeat("a", 65536+1)), 0600))
	_, err = readTool.Execute(ctx, "r-large", map[string]any{"path": "large.txt"}, nil)
	require.Error(t, err, "reading file > 64 KiB must fail")

	// 4. Read offset past EOF rejected
	smallFile := filepath.Join(ws, "small.txt")
	require.NoError(t, os.WriteFile(smallFile, []byte("line1\nline2\n"), 0600))
	_, err = readTool.Execute(ctx, "r-past-eof", map[string]any{
		"path":   "small.txt",
		"offset": 50,
	}, nil)
	require.ErrorContains(t, err, "beyond end of file")
}

func TestEmbeddedSubagentWriteDenialsEdgeCases(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()

	tools, cleanup, err := NewEmbeddedSubagentTools(ws)
	require.NoError(t, err)
	defer cleanup()

	writeTool := tools[2]

	// 1. Write payload > 64 KiB rejected
	_, err = writeTool.Execute(ctx, "w-oversize", map[string]any{
		"path":    "oversize.txt",
		"content": strings.Repeat("x", 65536+1),
	}, nil)
	require.Error(t, err, "write > 64 KiB must be rejected")

	// 2. Write to existing directory path rejected
	subDir := filepath.Join(ws, "dir_target")
	require.NoError(t, os.Mkdir(subDir, 0755))
	_, err = writeTool.Execute(ctx, "w-dir", map[string]any{
		"path":    "dir_target",
		"content": "cannot overwrite directory",
	}, nil)
	require.Error(t, err, "write targeting a directory must be rejected")
}

func TestEmbeddedSubagentEditDenialsEdgeCases(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()

	tools, cleanup, err := NewEmbeddedSubagentTools(ws)
	require.NoError(t, err)
	defer cleanup()

	editTool := tools[1]

	// 1. Edit non-regular file (FIFO) rejected
	fifoPath := filepath.Join(ws, "edit_fifo")
	require.NoError(t, unix.Mkfifo(fifoPath, 0600))
	_, err = editTool.Execute(ctx, "e-fifo", map[string]any{
		"path":  "edit_fifo",
		"edits": []any{map[string]any{"oldText": "a", "newText": "b"}},
	}, nil)
	require.Error(t, err, "editing non-regular file must be rejected")

	// 2. Edit large file (> 64 KiB initial size) rejected
	largePath := filepath.Join(ws, "large_edit.txt")
	require.NoError(t, os.WriteFile(largePath, []byte(strings.Repeat("x", 65536+1)), 0600))
	_, err = editTool.Execute(ctx, "e-large", map[string]any{
		"path":  "large_edit.txt",
		"edits": []any{map[string]any{"oldText": "x", "newText": "y"}},
	}, nil)
	require.Error(t, err, "editing file > 64 KiB must be rejected")

	// 3. Edit expansion limit rejected (> 64 KiB after replacement)
	baseContent := strings.Repeat("z", 60*1024) + " REPLACEME"
	basePath := filepath.Join(ws, "expand.txt")
	require.NoError(t, os.WriteFile(basePath, []byte(baseContent), 0600))

	expansion := strings.Repeat("e", 10*1024) // 60K + 10K = 70K > 64 KiB
	_, err = editTool.Execute(ctx, "e-expand", map[string]any{
		"path":  "expand.txt",
		"edits": []any{map[string]any{"oldText": "REPLACEME", "newText": expansion}},
	}, nil)
	require.ErrorContains(t, err, "limit", "edit expansion exceeding 64 KiB must be rejected")
}

func TestEmbeddedSubagentBashStdoutOverflowTerminatesDescendants(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()

	tools, cleanup, err := NewEmbeddedSubagentTools(ws)
	require.NoError(t, err)
	defer cleanup()

	bashTool := tools[3]

	// Command generates > 64 KiB to trigger overflow
	_, err = bashTool.Execute(ctx, "b-overflow", map[string]any{
		"command": "head -c 70000 /dev/zero | tr '\\0' 'x'",
	}, nil)
	require.ErrorContains(t, err, "output truncated", "stdout overflow must terminate and return truncation error")
}
