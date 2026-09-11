package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
	require.True(t, parsed.managed.Enabled)
	home, homeErr := os.UserHomeDir()
	require.NoError(t, homeErr)
	require.Equal(t, []string{home}, parsed.managed.AllowedWorkingDirectoryRoots)
}

func TestParseConfigAllowsExplicitManagedOptOutAndRejectsInvalidValue(t *testing.T) {
	disabled, err := parseConfig([]string{"--database", "/tmp/router.db", "--managed=false"})
	require.NoError(t, err)
	require.False(t, disabled.managed.Enabled)

	_, err = parseConfig([]string{"--database", "/tmp/router.db", "--managed=invalid"})
	require.Error(t, err)
}

func TestLauncherDefaultsManagedModeOnAndRejectsInvalidEnvironment(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	temporaryDirectory := t.TempDir()
	capture := filepath.Join(temporaryDirectory, "make-args")
	managedHome := filepath.Join(temporaryDirectory, "managed-home")
	fakeBin := filepath.Join(temporaryDirectory, "bin")
	require.NoError(t, os.MkdirAll(fakeBin, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(fakeBin, "make"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$MECHA_TEST_CAPTURE\"\n"), 0o700))

	environment := func(managed string) []string {
		filtered := make([]string, 0, len(os.Environ())+5)
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "MECHA_ROUTER_MANAGED") && !strings.HasPrefix(value, "PATH=") {
				filtered = append(filtered, value)
			}
		}
		filtered = append(filtered, "PATH="+fakeBin+":"+os.Getenv("PATH"), "MECHA_TEST_CAPTURE="+capture, "MECHA_ROUTER_MANAGED_HOME="+managedHome, "MECHA_ROUTER_MANAGED_SESSION_ROOT="+filepath.Join(managedHome, "sessions"))
		if managed != "" {
			filtered = append(filtered, "MECHA_ROUTER_MANAGED="+managed)
		}
		return filtered
	}
	run := func(managed string) *exec.Cmd {
		command := exec.Command("bash", filepath.Join(repositoryRoot, "tools", "run_router.sh"))
		command.Env = environment(managed)
		return command
	}

	require.NoError(t, run("").Run())
	arguments, err := os.ReadFile(capture)
	require.NoError(t, err)
	require.Contains(t, string(arguments), "ROUTER_MANAGED=true")
	home, homeErr := os.UserHomeDir()
	require.NoError(t, homeErr)
	require.Contains(t, string(arguments), "ROUTER_MANAGED_CWD_ROOTS="+home)

	require.NoError(t, run("false").Run())
	arguments, err = os.ReadFile(capture)
	require.NoError(t, err)
	require.Contains(t, string(arguments), "ROUTER_MANAGED=false")

	invalid := run("invalid")
	output, err := invalid.CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(output), "MECHA_ROUTER_MANAGED must be true or false")
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
	home := filepath.Join(root, "home")
	piAgentDir := filepath.Join(root, "ordinary-pi", "agent")
	sessionRoot := filepath.Join(home, "sessions")
	workRoot := filepath.Join(root, "work")
	require.NoError(t, os.MkdirAll(sessionRoot, 0o700))
	require.NoError(t, os.MkdirAll(piAgentDir, 0o700))
	require.NoError(t, os.MkdirAll(workRoot, 0o700))
	config := managed.Config{
		Enabled: true, PiExecutable: pi, ManagedHome: home, PiAgentDir: piAgentDir, SessionRoot: sessionRoot,
		ManifestPath: manifest, MechaRPCExtension: mecha, PiSubagentsExtension: subagents,
		AllowedWorkingDirectoryRoots: []string{workRoot}, CanonicalQuietRecipientID: "supervisor",
	}
	return config, map[string]string{"pi": pi, "manifest": manifest, "mecha": mecha, "subagents": subagents, "home": home, "pi_agent_dir": piAgentDir, "session": sessionRoot, "work": workRoot}
}

func TestManagedStartupAcceptsChangedValidInputsAndRejectsMissingCorePaths(t *testing.T) {
	config, paths := managedStartupTestFixture(t)
	require.NoError(t, validateManagedStartup(config))

	// The active contract permits a usable local Pi and extension to change.
	require.NoError(t, os.WriteFile(paths["pi"], []byte("#!/bin/sh\nprintf 'new local version\\n'\n"), 0o700))
	require.NoError(t, os.WriteFile(paths["mecha"], []byte("updated extension\n"), 0o600))
	require.NoError(t, os.WriteFile(paths["subagents"], []byte("updated extension\n"), 0o600))
	require.NoError(t, validateManagedStartup(config))

	config.ManagedHome = filepath.Join(paths["home"], "missing")
	require.Error(t, validateManagedStartup(config))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "managed validation happens before router.Listen")
	require.NoError(t, listener.Close())
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
	command := exec.Command(binary, "--database", database, "--log", logPath, "--host", "127.0.0.1", "--port", formatPort(port), "--managed=false")
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
			}
		case <-deadline:
			t.Fatal("timed out waiting for isolated Go router readiness")
		}
	}
	require.Contains(t, ready, "database="+database)
	require.Contains(t, ready, "log="+logPath)
	legacyHealth, err := http.Get("http://127.0.0.1:" + formatPort(port) + "/v1/health")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, legacyHealth.StatusCode)
	require.NoError(t, legacyHealth.Body.Close())
	disabledManagedRoute, err := http.Get("http://127.0.0.1:" + formatPort(port) + "/v1/managed-agents")
	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, disabledManagedRoute.StatusCode)
	require.NoError(t, disabledManagedRoute.Body.Close())
	require.FileExists(t, database)
	attachmentInfo, err := os.Stat(database + ".attachments")
	require.NoError(t, err)
	require.True(t, attachmentInfo.IsDir())
	require.FileExists(t, logPath)

	require.NoError(t, command.Process.Signal(syscall.SIGTERM))
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
