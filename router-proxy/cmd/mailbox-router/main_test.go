package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
	"github.com/stretchr/testify/require"
)

func TestParseConfigUsesIsolatedRouterDefaults(t *testing.T) {
	parsed, err := parseConfig([]string{"--database", "/tmp/router.db"})
	require.NoError(t, err)
	require.Equal(t, "/tmp/router.db.jsonl", parsed.logPath)
	require.Equal(t, "127.0.0.1", parsed.host)
	require.Equal(t, 8765, parsed.port)
	require.Equal(t, "supervisor", parsed.auditSupervisorTarget)
	require.Equal(t, "mecha", parsed.auditTmuxSession)
	require.Equal(t, transport.DefaultPolicyTimeout, parsed.policyTimeout)
}

func hashManagedTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hashManagedTestTree(t *testing.T, root string) string {
	t.Helper()
	type entry struct {
		name string
		data []byte
	}
	var entries []entry
	require.NoError(t, filepath.WalkDir(root, func(path string, item fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if item.IsDir() {
			if item.Name() == "node_modules" {
				return fs.SkipDir
			}
			return nil
		}
		if item.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("tree contains symlink: %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries = append(entries, entry{filepath.ToSlash(name), data})
		return nil
	}))
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	digest := sha256.New()
	for _, item := range entries {
		_, _ = digest.Write([]byte(item.name))
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write(item.data)
		_, _ = digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func managedStartupTestFixture(t *testing.T) (managed.Config, map[string]string) {
	t.Helper()
	root := t.TempDir()
	write := func(path, contents string, mode os.FileMode) {
		t.Helper()
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(contents), mode))
	}
	pi := filepath.Join(root, "bin", "pi")
	write(pi, "#!/bin/sh\nprintf '0.85.0\\n'\n", 0o700)
	manifest := filepath.Join(root, "manifest.json")
	write(manifest, `{"revision":1,"tools":[]}`, 0o600)
	mechaRoot := filepath.Join(root, "mecha-rpc")
	mecha := filepath.Join(mechaRoot, "index.ts")
	write(mecha, "import './protocol.js';\n", 0o600)
	write(filepath.Join(mechaRoot, "protocol.ts"), "export const protocol = 1;\n", 0o600)
	subagentsRoot := filepath.Join(root, "pi-subagents")
	subagents := filepath.Join(subagentsRoot, "src", "index.ts")
	write(subagents, "export default function extension() {}\n", 0o600)
	write(filepath.Join(subagentsRoot, "src", "child.ts"), "export const child = true;\n", 0o600)
	cursorRoot := filepath.Join(root, "cursor")
	cursor := filepath.Join(cursorRoot, "dist", "index.js")
	write(cursor, "export default function extension() {}\n", 0o600)
	cursorPackage := filepath.Join(cursorRoot, "package.json")
	write(cursorPackage, `{"name":"@mecha/cursor","version":"1.0.0"}`, 0o600)
	cursorLock := filepath.Join(cursorRoot, "package-lock.json")
	write(cursorLock, `{"lockfileVersion":3}`, 0o600)
	helperRoot := filepath.Join(root, "helper")
	helper := filepath.Join(helperRoot, "index.mjs")
	write(helper, "console.log('{}');\n", 0o600)
	write(filepath.Join(helperRoot, "providers.mjs"), "export const providers = [];\n", 0o600)
	helperLock := filepath.Join(helperRoot, "package-lock.json")
	write(helperLock, `{"lockfileVersion":3}`, 0o600)
	ui := filepath.Join(root, "ui")
	write(filepath.Join(ui, "index.html"), "<!doctype html>", 0o600)
	write(filepath.Join(ui, "app.js"), "console.log('ui');\n", 0o600)
	home := filepath.Join(root, "home")
	sessionRoot := filepath.Join(home, "sessions")
	workRoot := filepath.Join(root, "work")
	require.NoError(t, os.MkdirAll(sessionRoot, 0o700))
	require.NoError(t, os.MkdirAll(workRoot, 0o700))
	config := managed.Config{
		Enabled: true, PiExecutable: pi, ManagedHome: home, SessionRoot: sessionRoot,
		ManifestPath: manifest, MechaRPCExtension: mecha, PiSubagentsExtension: subagents,
		CursorExtension: cursor, CursorPackage: cursorRoot, CursorLockfile: cursorLock,
		HelperPath: helper, HelperLockfile: helperLock, UIDistribution: ui,
		AllowedWorkingDirectoryRoots: []string{workRoot}, CanonicalQuietRecipientID: "supervisor",
		Expected: managed.ExpectedIdentities{
			PiVersion: "0.85.0", PiSHA256: hashManagedTestFile(t, pi), ManifestSHA256: hashManagedTestFile(t, manifest),
			MechaRPCHash: hashManagedTestFile(t, mecha), MechaRPCTreeHash: hashManagedTestTree(t, mechaRoot),
			PiSubagentsEntryHash: hashManagedTestFile(t, subagents), PiSubagentsTreeHash: hashManagedTestTree(t, subagentsRoot),
			CursorExtensionHash: hashManagedTestFile(t, cursor), CursorPackageHash: hashManagedTestFile(t, cursorPackage),
			CursorResolvedIdentity: "@mecha/cursor@1.0.0", CursorLockfileHash: hashManagedTestFile(t, cursorLock), CursorTreeHash: hashManagedTestTree(t, cursorRoot),
			HelperHash: hashManagedTestFile(t, helper), HelperLockfileHash: hashManagedTestFile(t, helperLock), HelperTreeHash: hashManagedTestTree(t, helperRoot),
			UIDistributionTreeHash: hashManagedTestTree(t, ui),
		},
	}
	return config, map[string]string{"pi": pi, "manifest": manifest, "mecha": mecha, "mecha-tree": filepath.Join(mechaRoot, "protocol.ts"), "subagents": subagents, "subagents-tree": filepath.Join(subagentsRoot, "src", "child.ts"), "cursor": cursor, "cursor-package": cursorPackage, "cursor-lock": cursorLock, "cursor-tree": filepath.Join(cursorRoot, "dist", "tampered.js"), "helper": helper, "helper-tree": filepath.Join(helperRoot, "providers.mjs"), "helper-lock": helperLock, "ui-tree": filepath.Join(ui, "tampered.js")}
}

func TestManagedStartupRejectsTamperedFixedInputsBeforeListen(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]string)
	}{
		{"pi", func(paths map[string]string) {
			require.NoError(t, os.WriteFile(paths["pi"], []byte("#!/bin/sh\nprintf '0.85.0\\n'\nprintf tampered\n"), 0o700))
		}},
		{"manifest", func(paths map[string]string) {
			require.NoError(t, os.WriteFile(paths["manifest"], []byte(`{"revision":2}`), 0o600))
		}},
		{"mecha-rpc-entry", func(paths map[string]string) {
			require.NoError(t, os.WriteFile(paths["mecha"], []byte("tampered\n"), 0o600))
		}},
		{"mecha-rpc-local-module", func(paths map[string]string) {
			require.NoError(t, os.WriteFile(paths["mecha-tree"], []byte("tampered\n"), 0o600))
		}},
		{"pi-subagents-entry", func(paths map[string]string) {
			require.NoError(t, os.WriteFile(paths["subagents"], []byte("tampered\n"), 0o600))
		}},
		{"pi-subagents-local-module", func(paths map[string]string) {
			require.NoError(t, os.WriteFile(paths["subagents-tree"], []byte("tampered\n"), 0o600))
		}},
		{"cursor-entry", func(paths map[string]string) {
			require.NoError(t, os.WriteFile(paths["cursor"], []byte("tampered\n"), 0o600))
		}},
		{"cursor-package", func(paths map[string]string) {
			require.NoError(t, os.WriteFile(paths["cursor-package"], []byte(`{"name":"@mecha/cursor","version":"9.9.9"}`), 0o600))
		}},
		{"cursor-lockfile", func(paths map[string]string) {
			require.NoError(t, os.WriteFile(paths["cursor-lock"], []byte(`{"lockfileVersion":2}`), 0o600))
		}},
		{"cursor-tree", func(paths map[string]string) {
			require.NoError(t, os.WriteFile(paths["cursor-tree"], []byte("tampered\n"), 0o600))
		}},
		{"helper-source", func(paths map[string]string) {
			require.NoError(t, os.WriteFile(paths["helper"], []byte("tampered\n"), 0o600))
		}},
		{"helper-local-module", func(paths map[string]string) {
			require.NoError(t, os.WriteFile(paths["helper-tree"], []byte("tampered\n"), 0o600))
		}},
		{"helper-lockfile", func(paths map[string]string) {
			require.NoError(t, os.WriteFile(paths["helper-lock"], []byte(`{"lockfileVersion":2}`), 0o600))
		}},
		{"ui-tree", func(paths map[string]string) {
			require.NoError(t, os.WriteFile(paths["ui-tree"], []byte("tampered\n"), 0o600))
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			config, paths := managedStartupTestFixture(t)
			testCase.mutate(paths)
			require.Error(t, validateManagedStartup(config))
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err, "startup validation must happen before listener creation")
			require.NoError(t, listener.Close())
		})
	}
}

func TestManagedConfigurationFailureDoesNotBindListener(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "mailbox-router")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", binary, ".")
	build.Dir = filepath.Dir(filepath.Join(".", "main.go"))
	require.NoError(t, build.Run())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	command := exec.Command(binary, "--database", filepath.Join(directory, "router.db"), "--host", "127.0.0.1", "--port", formatPort(port), "--managed=true", "--managed-home", filepath.Join(directory, "missing-managed-home"))
	require.Error(t, command.Run())
	bound, err := net.Listen("tcp", "127.0.0.1:"+formatPort(port))
	require.NoError(t, err, "invalid managed configuration must fail before router.Listen")
	require.NoError(t, bound.Close())
}

func TestCommandStartsWithDistinctDatabaseLogAttachmentsAndPort(t *testing.T) {
	directory := t.TempDir()
	database := filepath.Join(directory, "go-router.db")
	logPath := filepath.Join(directory, "go-router.jsonl")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())

	binary := filepath.Join(directory, "mailbox-router")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", binary, ".")
	build.Dir = filepath.Dir(filepath.Join(".", "main.go"))
	require.NoError(t, build.Run())
	command := exec.Command(binary, "--database", database, "--log", logPath, "--host", "127.0.0.1", "--port", formatPort(port))
	stdout, err := command.StdoutPipe()
	require.NoError(t, err)
	stderr, err := command.StderrPipe()
	require.NoError(t, err)
	require.NoError(t, command.Start())
	defer func() {
		if command.ProcessState == nil || !command.ProcessState.Exited() {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
	}()

	lines := make(chan string, 32)
	go collectLines(stdout, lines)
	go collectLines(stderr, lines)
	var ready string
	deadline := time.After(30 * time.Second)
	for ready == "" {
		select {
		case line := <-lines:
			if strings.HasPrefix(line, "MECHA_V2_ROUTER_READY ") {
				ready = line
				require.NoError(t, command.Process.Signal(syscall.SIGTERM))
			}
		case <-deadline:
			t.Fatal("timed out waiting for isolated Go router readiness")
		}
	}
	require.Contains(t, ready, "database="+database)
	require.Contains(t, ready, "log="+logPath)
	require.FileExists(t, database)
	attachmentInfo, err := os.Stat(database + ".attachments")
	require.NoError(t, err)
	require.True(t, attachmentInfo.IsDir())
	require.FileExists(t, logPath)

	require.NoError(t, command.Wait())
	logBytes, err := os.ReadFile(logPath)
	require.NoError(t, err)
	require.Contains(t, string(logBytes), `"event":"router_stopped"`)
}

func TestEventLogRotatesAfterOneDay(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "router.jsonl")
	current := time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	require.NoError(t, os.Chtimes(path, current, current))
	writer, err := newLogWriterAt(path, func() time.Time { return current })
	require.NoError(t, err)
	defer writer.Close()
	require.NoError(t, writer.Log("first", nil))
	current = current.Add(logRotationAge)
	require.NoError(t, writer.Log("second", nil))

	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	var rotated []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "router.jsonl.") {
			rotated = append(rotated, entry.Name())
		}
	}
	require.Len(t, rotated, 1)
	rotatedBytes, err := os.ReadFile(filepath.Join(directory, rotated[0]))
	require.NoError(t, err)
	require.Contains(t, string(rotatedBytes), `"event":"first"`)
	activeBytes, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(activeBytes), `"event":"second"`)
}

func TestEventLogRefreshesGenerationTimestampAfterRotation(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "router.jsonl")
	current := time.Now().UTC().Truncate(time.Millisecond)
	writer, err := newLogWriterAt(path, func() time.Time { return current })
	require.NoError(t, err)
	defer writer.Close()

	require.NoError(t, writer.Log("first", nil))
	current = current.Add(logRotationAge)
	require.NoError(t, writer.Log("second", nil))
	current = current.Add(time.Hour)
	require.NoError(t, writer.Log("third", nil))

	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	var rotated []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "router.jsonl.") {
			rotated = append(rotated, entry.Name())
		}
	}
	require.Len(t, rotated, 1)

	current = current.Add(logRotationAge - time.Hour)
	require.NoError(t, writer.Log("fourth", nil))
	entries, err = os.ReadDir(directory)
	require.NoError(t, err)
	rotated = rotated[:0]
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "router.jsonl.") {
			rotated = append(rotated, entry.Name())
		}
	}
	require.Len(t, rotated, 2)

	var retained strings.Builder
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "router.jsonl") {
			continue
		}
		contents, readErr := os.ReadFile(filepath.Join(directory, entry.Name()))
		require.NoError(t, readErr)
		retained.Write(contents)
	}
	require.Contains(t, retained.String(), `"event":"second"`)
	require.Contains(t, retained.String(), `"event":"third"`)
	require.Contains(t, retained.String(), `"event":"fourth"`)
}

func TestEventLogDropsActiveRecordsOlderThanRetention(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "router.jsonl")
	current := time.Now().UTC().Truncate(time.Millisecond)
	old := current.Add(-logRetentionAge - time.Hour)
	oldRecord := fmt.Sprintf(`{"event":"old","timestamp":%q}`+"\n", old.Format("2006-01-02T15:04:05.000Z"))
	require.NoError(t, os.WriteFile(path, []byte(oldRecord), 0o600))
	require.NoError(t, os.Chtimes(path, old, old))
	writer, err := newLogWriterAt(path, func() time.Time { return current })
	require.NoError(t, err)
	defer writer.Close()
	require.NoError(t, writer.Log("current", nil))
	activeBytes, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(activeBytes), `"event":"old"`)
	require.Contains(t, string(activeBytes), `"event":"current"`)
}

func TestEventLogRemovesRotationsOlderThanRetention(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "router.jsonl")
	current := time.Now().UTC().Truncate(time.Millisecond)
	oldPath := path + "." + current.Add(-logRetentionAge-time.Hour).Format(rotationLayout)
	newPath := path + "." + current.Add(-logRetentionAge+time.Hour).Format(rotationLayout)
	require.NoError(t, os.WriteFile(oldPath, []byte("old\n"), 0o600))
	require.NoError(t, os.WriteFile(newPath, []byte("new\n"), 0o600))

	writer, err := newLogWriterAt(path, func() time.Time { return current })
	require.NoError(t, err)
	defer writer.Close()
	require.NoError(t, writer.Log("current", nil))
	_, err = os.Stat(oldPath)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(newPath)
	require.NoError(t, err)
}

func TestEventLogBoundsEachRetainedFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "router.jsonl")
	current := time.Now().UTC().Truncate(time.Millisecond)
	writer, err := newLogWriterAt(path, func() time.Time { return current })
	require.NoError(t, err)
	defer writer.Close()
	payload := strings.Repeat("x", maxLogBytes/2-128)
	require.NoError(t, writer.Log("first", map[string]any{"payload": payload}))
	require.NoError(t, writer.Log("second", map[string]any{"payload": payload}))

	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "router.jsonl") {
			continue
		}
		info, statErr := entry.Info()
		require.NoError(t, statErr)
		require.LessOrEqual(t, info.Size(), int64(maxLogBytes), entry.Name())
	}
}

func TestEventLoggerReportsWriteFailureWithoutRecursing(t *testing.T) {
	writer, err := newLogWriter(filepath.Join(t.TempDir(), "router.jsonl"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	var output strings.Builder
	eventLogger(writer, &output)("router_stopped", nil)
	require.Contains(t, output.String(), "event log write failed")
}

func collectLines(reader io.Reader, lines chan<- string) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		lines <- scanner.Text()
	}
}
func formatPort(port int) string { return strconv.Itoa(port) }
