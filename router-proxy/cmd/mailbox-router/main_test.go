package main

import (
	"bufio"
	"context"
	"encoding/json"
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
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
	"github.com/stretchr/testify/require"
)

func TestAttachmentReferenceCheckKeepsFilesWhenStoreUnavailable(t *testing.T) {
	ctx := context.Background()
	require.True(t, attachmentReferenced(ctx, nil, "saved.png"))
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	require.False(t, attachmentReferenced(ctx, durable, "unreferenced.png"))
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	require.True(t, attachmentReferenced(cancelled, durable, "saved.png"), "a failed query must preserve uncertain references")
	require.NoError(t, durable.Close())
	require.True(t, attachmentReferenced(ctx, durable, "saved.png"), "a closed store cannot prove a file is unreferenced")
}

func TestParseConfigUsesIsolatedRouterDefaults(t *testing.T) {
	parsed, err := parseConfig([]string{"--database", "/tmp/router.db"})
	require.NoError(t, err)
	require.Equal(t, "/tmp/router.db.jsonl", parsed.logPath)
	require.Equal(t, "127.0.0.1", parsed.host)
	require.Equal(t, 8765, parsed.port)
	require.Empty(t, parsed.auditSupervisorTarget)
	require.Empty(t, parsed.auditTmuxSession)
	require.Equal(t, transport.DefaultPolicyTimeout, parsed.policyTimeout)
	require.True(t, parsed.managed.Enabled)
	home, homeErr := os.UserHomeDir()
	require.NoError(t, homeErr)
	require.Equal(t, []string{home}, parsed.managed.AllowedWorkingDirectoryRoots)
}

func TestParseConfigLaunchRecipeMismatchDefaultsOffAndAcceptsExplicitOptIn(t *testing.T) {
	defaultConfig, err := parseConfig([]string{"--database", "/tmp/router.db"})
	require.NoError(t, err)
	require.False(t, defaultConfig.managed.AllowManagedLaunchRecipeMismatch)

	explicitConfig, err := parseConfig([]string{"--database", "/tmp/router.db", "--managed-allow-launch-recipe-mismatch=true"})
	require.NoError(t, err)
	require.True(t, explicitConfig.managed.AllowManagedLaunchRecipeMismatch)
}

func TestParseConfigRejectsHalfConfiguredTmuxAudit(t *testing.T) {
	for _, args := range [][]string{
		{"--database", "/tmp/router.db", "--audit-supervisor-target", "supervisor"},
		{"--database", "/tmp/router.db", "--audit-tmux-session", "mecha"},
	} {
		_, err := parseConfig(args)
		require.EqualError(t, err, "--audit-supervisor-target and --audit-tmux-session must be provided together")
	}
}

func TestParseConfigPreservesExplicitTmuxAuditPair(t *testing.T) {
	parsed, err := parseConfig([]string{"--database", "/tmp/router.db", "--audit-supervisor-target", "audit-target", "--audit-tmux-session", "audit-session"})
	require.NoError(t, err)
	require.Equal(t, "audit-target", parsed.auditSupervisorTarget)
	require.Equal(t, "audit-session", parsed.auditTmuxSession)
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

	environment := func(managed, allowLaunchRecipeMismatch string) []string {
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
		if allowLaunchRecipeMismatch != "" {
			filtered = append(filtered, "MECHA_ROUTER_MANAGED_ALLOW_LAUNCH_RECIPE_MISMATCH="+allowLaunchRecipeMismatch)
		}
		return filtered
	}
	run := func(managed, allowLaunchRecipeMismatch string) *exec.Cmd {
		command := exec.Command("bash", filepath.Join(repositoryRoot, "tools", "run_router.sh"))
		command.Env = environment(managed, allowLaunchRecipeMismatch)
		return command
	}

	require.NoError(t, run("", "").Run())
	arguments, err := os.ReadFile(capture)
	require.NoError(t, err)
	require.Contains(t, string(arguments), "ROUTER_MANAGED=true")
	require.Contains(t, string(arguments), "ROUTER_MANAGED_ALLOW_LAUNCH_RECIPE_MISMATCH=false")
	home, homeErr := os.UserHomeDir()
	require.NoError(t, homeErr)
	require.Contains(t, string(arguments), "ROUTER_MANAGED_CWD_ROOTS="+home)

	require.NoError(t, run("false", "").Run())
	arguments, err = os.ReadFile(capture)
	require.NoError(t, err)
	require.Contains(t, string(arguments), "ROUTER_MANAGED=false")

	require.NoError(t, run("", "true").Run())
	arguments, err = os.ReadFile(capture)
	require.NoError(t, err)
	require.Contains(t, string(arguments), "ROUTER_MANAGED_ALLOW_LAUNCH_RECIPE_MISMATCH=true")

	invalid := run("invalid", "")
	output, err := invalid.CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(output), "MECHA_ROUTER_MANAGED must be true or false")
}

func TestLauncherRestartBehavior(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)

	setupLauncherEnv := func(t *testing.T, makeScript string) (string, []string) {
		t.Helper()
		tempDir := t.TempDir()
		fakeBin := filepath.Join(tempDir, "bin")
		require.NoError(t, os.MkdirAll(fakeBin, 0o700))
		makePath := filepath.Join(fakeBin, "make")
		require.NoError(t, os.WriteFile(makePath, []byte(makeScript), 0o700))

		managedHome := filepath.Join(tempDir, "managed-home")
		env := make([]string, 0, len(os.Environ())+5)
		for _, v := range os.Environ() {
			if !strings.HasPrefix(v, "MECHA_ROUTER_") && !strings.HasPrefix(v, "PATH=") {
				env = append(env, v)
			}
		}
		env = append(env,
			"PATH="+fakeBin+":"+os.Getenv("PATH"),
			"MECHA_ROUTER_MANAGED=false",
			"MECHA_ROUTER_MANAGED_HOME="+managedHome,
			"MECHA_ROUTER_MANAGED_SESSION_ROOT="+filepath.Join(managedHome, "sessions"),
		)
		return tempDir, env
	}

	t.Run("exits cleanly on code 0 without restart", func(t *testing.T) {
		countFile := filepath.Join(t.TempDir(), "runs")
		makeScript := fmt.Sprintf("#!/bin/sh\necho run >> %q\nexit 0\n", countFile)
		_, env := setupLauncherEnv(t, makeScript)

		cmd := exec.Command("bash", filepath.Join(repositoryRoot, "tools", "run_router.sh"))
		cmd.Env = env
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, string(output))

		runs, err := os.ReadFile(countFile)
		require.NoError(t, err)
		require.Equal(t, "run\n", string(runs))
	})

	t.Run("exits cleanly on code 130 without restart", func(t *testing.T) {
		countFile := filepath.Join(t.TempDir(), "runs")
		makeScript := fmt.Sprintf("#!/bin/sh\necho run >> %q\nexit 130\n", countFile)
		_, env := setupLauncherEnv(t, makeScript)

		cmd := exec.Command("bash", filepath.Join(repositoryRoot, "tools", "run_router.sh"))
		cmd.Env = env
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, string(output))

		runs, err := os.ReadFile(countFile)
		require.NoError(t, err)
		require.Equal(t, "run\n", string(runs))
	})

	t.Run("exits cleanly on code 143 without restart", func(t *testing.T) {
		countFile := filepath.Join(t.TempDir(), "runs")
		makeScript := fmt.Sprintf("#!/bin/sh\necho run >> %q\nexit 143\n", countFile)
		_, env := setupLauncherEnv(t, makeScript)

		cmd := exec.Command("bash", filepath.Join(repositoryRoot, "tools", "run_router.sh"))
		cmd.Env = env
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, string(output))

		runs, err := os.ReadFile(countFile)
		require.NoError(t, err)
		require.Equal(t, "run\n", string(runs))
	})

	t.Run("restarts on abnormal exit codes like 1 and 75", func(t *testing.T) {
		countFile := filepath.Join(t.TempDir(), "runs")
		// On run 1, exit 1; on run 2, exit 75; on run 3, exit 0 so the test terminates
		makeScript := fmt.Sprintf(`#!/bin/sh
count=0
if [ -f %[1]q ]; then
  count=$(wc -l < %[1]q)
fi
echo run >> %[1]q
if [ "$count" -eq 0 ]; then
  exit 1
elif [ "$count" -eq 1 ]; then
  exit 75
else
  exit 0
fi
`, countFile)
		_, env := setupLauncherEnv(t, makeScript)

		cmd := exec.Command("bash", filepath.Join(repositoryRoot, "tools", "run_router.sh"))
		cmd.Env = env
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, string(output))

		outStr := string(output)
		require.Contains(t, outStr, "run_router.sh: router exited with code 1; restarting in 1s...")
		require.Contains(t, outStr, "run_router.sh: router exited with code 75; restarting in 1s...")

		runs, err := os.ReadFile(countFile)
		require.NoError(t, err)
		require.Equal(t, "run\nrun\nrun\n", string(runs))
	})
}

func TestMakefileForwardsLaunchRecipeMismatchToRouterCommand(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)

	runMake := func(allowLaunchRecipeMismatch string) string {
		args := []string{"-C", filepath.Join(repositoryRoot, "router-proxy"), "-n", "run-router", "ROUTER_SOURCE_TREE=clean"}
		if allowLaunchRecipeMismatch != "" {
			args = append(args, "ROUTER_MANAGED_ALLOW_LAUNCH_RECIPE_MISMATCH="+allowLaunchRecipeMismatch)
		}
		command := exec.Command("make", args...)
		filtered := make([]string, 0, len(os.Environ()))
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "ROUTER_MANAGED_ALLOW_LAUNCH_RECIPE_MISMATCH=") {
				filtered = append(filtered, value)
			}
		}
		command.Env = filtered
		output, err := command.CombinedOutput()
		require.NoError(t, err, string(output))
		return string(output)
	}

	defaultOutput := runMake("")
	require.Contains(t, defaultOutput, `--managed-allow-launch-recipe-mismatch="false"`)

	explicitOutput := runMake("true")
	require.Contains(t, explicitOutput, `--managed-allow-launch-recipe-mismatch="true"`)
}

func TestMakefileKeepsDirtyDeployBuildBlockedButLocalBuildEmbedsDirtyIdentity(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	makeDirectory := filepath.Join(repositoryRoot, "router-proxy")
	binary := filepath.Join(t.TempDir(), "mailbox-router")
	commit := "dirty-test-commit"

	deploy := exec.Command("make", "-C", makeDirectory, "build-router", "ROUTER_BINARY="+binary, "ROUTER_SOURCE_COMMIT="+commit, "ROUTER_SOURCE_TREE=dirty")
	deployOutput, deployErr := deploy.CombinedOutput()
	require.Error(t, deployErr, string(deployOutput))
	require.Contains(t, string(deployOutput), "Refusing to deploy a router binary from a dirty source tree.")

	local := exec.Command("make", "-C", makeDirectory, "build-router-local", "ROUTER_BINARY="+binary, "ROUTER_SOURCE_COMMIT="+commit, "ROUTER_SOURCE_TREE=dirty")
	localOutput, localErr := local.CombinedOutput()
	require.NoError(t, localErr, string(localOutput))
	require.FileExists(t, binary)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	directory := t.TempDir()
	command := exec.Command(binary, "--database", filepath.Join(directory, "router.db"), "--log", filepath.Join(directory, "router.jsonl"), "--host", "127.0.0.1", "--port", formatPort(port), "--managed=false")
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
	deadline := time.After(30 * time.Second)
	for {
		select {
		case line := <-lines:
			if !strings.HasPrefix(line, "MECHA_V2_ROUTER_READY ") {
				continue
			}
			health, healthErr := http.Get("http://127.0.0.1:" + formatPort(port) + "/v1/health")
			require.NoError(t, healthErr)
			defer health.Body.Close()
			var body struct {
				Runtime struct {
					SourceCommit string `json:"source_commit"`
					SourceTree   string `json:"source_tree"`
				} `json:"runtime"`
			}
			require.NoError(t, json.NewDecoder(health.Body).Decode(&body))
			require.Equal(t, commit, body.Runtime.SourceCommit)
			require.Equal(t, "dirty", body.Runtime.SourceTree)
			return
		case <-deadline:
			t.Fatal("timed out waiting for locally built router readiness")
		}
	}
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
		Enabled: true, PiExecutable: pi, ManagedHome: home, PiAgentDir: piAgentDir, SessionRoot: sessionRoot, RouterURL: "http://127.0.0.1:18775",
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

func TestSIGTERMRouterCloseReopenOrdering(t *testing.T) {
	directory := t.TempDir()
	database := filepath.Join(directory, "router.db")
	logPath := filepath.Join(directory, "router.jsonl")
	binary := filepath.Join(directory, "mailbox-router")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", binary, ".")
	build.Dir = filepath.Dir(filepath.Join(".", "main.go"))
	require.NoError(t, build.Run())

	run := func() {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		port := listener.Addr().(*net.TCPAddr).Port
		require.NoError(t, listener.Close())
		command := exec.Command(binary, "--database", database, "--log", logPath, "--host", "127.0.0.1", "--port", formatPort(port), "--managed=false")
		stdout, err := command.StdoutPipe()
		require.NoError(t, err)
		stderr, err := command.StderrPipe()
		require.NoError(t, err)
		require.NoError(t, command.Start())
		lines := make(chan string, 32)
		go collectLines(stdout, lines)
		go collectLines(stderr, lines)
		deadline := time.After(30 * time.Second)
		for {
			select {
			case line := <-lines:
				if !strings.HasPrefix(line, "MECHA_V2_ROUTER_READY ") {
					continue
				}
				response, healthErr := http.Get("http://127.0.0.1:" + formatPort(port) + "/v1/health")
				require.NoError(t, healthErr)
				require.Equal(t, http.StatusOK, response.StatusCode)
				require.NoError(t, response.Body.Close())
				require.NoError(t, command.Process.Signal(syscall.SIGTERM))
				require.NoError(t, command.Wait())
				return
			case <-deadline:
				_ = command.Process.Kill()
				_ = command.Wait()
				t.Fatal("timed out waiting for production router readiness")
			}
		}
	}

	run()
	run()
	logBytes, err := os.ReadFile(logPath)
	require.NoError(t, err)
	require.Equal(t, 2, strings.Count(string(logBytes), `"event":"router_started"`))
	require.Equal(t, 2, strings.Count(string(logBytes), `"event":"router_stopped"`))
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
	requireSafeEventFixture(t, "router_started", nil)
	require.NoError(t, writer.Log("router_started", nil))
	current = current.Add(logRotationAge)
	requireSafeEventFixture(t, "router_stopped", nil)
	require.NoError(t, writer.Log("router_stopped", nil))

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
	require.Contains(t, string(rotatedBytes), `"event":"router_started"`)
	activeBytes, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(activeBytes), `"event":"router_stopped"`)
}

func TestEventLogRefreshesGenerationTimestampAfterRotation(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "router.jsonl")
	current := time.Now().UTC().Truncate(time.Millisecond)
	writer, err := newLogWriterAt(path, func() time.Time { return current })
	require.NoError(t, err)
	defer writer.Close()

	requireSafeEventFixture(t, "router_started", nil)
	require.NoError(t, writer.Log("router_started", nil))
	current = current.Add(logRotationAge)
	requireSafeEventFixture(t, "router_stopped", nil)
	require.NoError(t, writer.Log("router_stopped", nil))
	current = current.Add(time.Hour)
	requireSafeEventFixture(t, "router_error", nil)
	require.NoError(t, writer.Log("router_error", nil))

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
	requireSafeEventFixture(t, "lifecycle_ingested", nil)
	require.NoError(t, writer.Log("lifecycle_ingested", nil))
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
	require.Contains(t, retained.String(), `"event":"router_stopped"`)
	require.Contains(t, retained.String(), `"event":"router_error"`)
	require.Contains(t, retained.String(), `"event":"lifecycle_ingested"`)
}

func TestEventLogDropsActiveRecordsOlderThanRetention(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "router.jsonl")
	current := time.Now().UTC().Truncate(time.Millisecond)
	old := current.Add(-logRetentionAge - time.Hour)
	oldRecord := fmt.Sprintf(`{"event":"router_started","timestamp":%q}`+"\n", old.Format("2006-01-02T15:04:05.000Z"))
	require.NoError(t, os.WriteFile(path, []byte(oldRecord), 0o600))
	require.NoError(t, os.Chtimes(path, old, old))
	writer, err := newLogWriterAt(path, func() time.Time { return current })
	require.NoError(t, err)
	defer writer.Close()
	requireSafeEventFixture(t, "router_stopped", nil)
	require.NoError(t, writer.Log("router_stopped", nil))
	activeBytes, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(activeBytes), `"event":"router_started"`)
	require.Contains(t, string(activeBytes), `"event":"router_stopped"`)
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
	requireSafeEventFixture(t, "router_stopped", nil)
	require.NoError(t, writer.Log("router_stopped", nil))
	_, err = os.Stat(oldPath)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(newPath)
	require.NoError(t, err)
}

func TestEventLogBoundsEachRetainedFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "router.jsonl")
	current := time.Now().UTC().Truncate(time.Millisecond)
	// Exercise the pre-existing-file size guard with a bounded event fixture.
	require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("x", maxLogBytes+1)), 0o600))
	writer, err := newLogWriterAt(path, func() time.Time { return current })
	require.NoError(t, err)
	defer writer.Close()
	requireSafeEventFixture(t, "router_stopped", nil)
	require.NoError(t, writer.Log("router_stopped", nil))

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
	eventLogger(writer, &output)(transport.RouterStoppedEvent())
	require.Contains(t, output.String(), "event log write failed")
}

func requireSafeEventFixture(t *testing.T, event string, fields map[string]any) {
	t.Helper()
	require.NoError(t, validateEvent(event, fields))
}

func collectLines(reader io.Reader, lines chan<- string) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		lines <- scanner.Text()
	}
}
func formatPort(port int) string { return strconv.Itoa(port) }
