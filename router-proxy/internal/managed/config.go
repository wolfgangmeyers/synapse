// Package managed contains the narrow router-managed Pi launch boundary.
package managed

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	MaxManagedAgents                 = 32
	MaxInitialInstructionBytes       = 32 * 1024
	MaxRPCLineBytes                  = 1 << 20
	DefaultGlobalSubagentCapacity    = 100
	DefaultSubagentCapacity          = DefaultGlobalSubagentCapacity
	DefaultSubagentPerWorkerCapacity = 10
	EmbeddedSystemInstruction        = "CRITICAL: To execute any action or launch a subagent, you MUST emit an actual structured tool call (e.g. `subagent`, `bash`). NEVER simulate or type tool observations or background launch notices (such as 'Observation: subagent...' or 'Agent started in background') in message text. Actions only take effect when invoked as real tool calls. Never simulate, roleplay, or print bracketed tool observations or completion notifications in message text. Always invoke declared tools directly. When answering user inquiries or instructions, always provide a clear, direct conversational response to the user after executing any required tools. Never finish silently without addressing the user's inquiry."
)

type Limits struct {
	MaxAgents                  int
	MaxInitialInstructionBytes int
	MaxRPCLineBytes            int
	SubagentCapacity           int
	SubagentPerWorkerCapacity  int
	GracefulStop               time.Duration
	TermStop                   time.Duration
}

type Config struct {
	Enabled                          bool
	AllowManagedLaunchRecipeMismatch bool
	PiExecutable                     string
	ManagedHome                      string
	// PiAgentDir is the existing ordinary Pi agent directory. Managed RPC runs
	// with its own HOME and session root, but reads provider credentials directly
	// from this local Pi directory.
	PiAgentDir  string
	SessionRoot string
	// RouterURL is the exact validated loopback endpoint owned by this router.
	// It is intentionally configured here instead of inherited from the parent
	// environment so managed roots and their native children stay on this router.
	RouterURL            string
	ManifestPath         string
	MechaRPCExtension    string
	PiSubagentsExtension string
	// CursorExtension is the optional fixed pi-cursor-sdk extension loaded only by
	// managed Pi when available. It is never available to ordinary/tmux Pi sessions.
	CursorExtension string
	// AntigravityExtension is the optional fixed pi-antigravity extension loaded
	// by managed Pi when available.
	AntigravityExtension string
	// RuntimeRoot is the checked project root that owns the thin extension,
	// generated manifest, helper, and optional Cursor extension.
	RuntimeRoot string
	// AuthHelperPath is the fixed project helper entry. It is a process path,
	// not a credential path; Pi credentials stay under ManagedHome.
	AuthHelperPath         string
	RequireProviderRuntime bool
	// TestOnlyExtensions are never populated by the production router CLI.
	// Native proofs may add a checked test probe without widening production argv.
	TestOnlyExtensions           []string
	TestOnlyEnvironment          []string
	AllowedWorkingDirectoryRoots []string
	CanonicalQuietRecipientID    string
	Limits                       Limits
}

func DefaultLimits() Limits {
	return Limits{
		MaxAgents:                  MaxManagedAgents,
		MaxInitialInstructionBytes: MaxInitialInstructionBytes,
		MaxRPCLineBytes:            MaxRPCLineBytes,
		SubagentCapacity:           DefaultGlobalSubagentCapacity,
		SubagentPerWorkerCapacity:  DefaultSubagentPerWorkerCapacity,
		GracefulStop:               5 * time.Second,
		TermStop:                   5 * time.Second,
	}
}

func (c *Config) normalize() {
	defaults := DefaultLimits()
	if c.Limits.MaxAgents == 0 {
		c.Limits.MaxAgents = defaults.MaxAgents
	}
	if c.Limits.MaxInitialInstructionBytes == 0 {
		c.Limits.MaxInitialInstructionBytes = defaults.MaxInitialInstructionBytes
	}
	if c.Limits.MaxRPCLineBytes == 0 {
		c.Limits.MaxRPCLineBytes = defaults.MaxRPCLineBytes
	}
	if c.Limits.SubagentCapacity <= 0 {
		c.Limits.SubagentCapacity = defaults.SubagentCapacity
	}
	if c.Limits.SubagentPerWorkerCapacity <= 0 {
		c.Limits.SubagentPerWorkerCapacity = defaults.SubagentPerWorkerCapacity
	}
	if c.Limits.GracefulStop == 0 {
		c.Limits.GracefulStop = defaults.GracefulStop
	}
	if c.Limits.TermStop == 0 {
		c.Limits.TermStop = defaults.TermStop
	}
}

// ValidateManagedLaunchConfig checks the paths and bounds that protect every
// direct managed Pi launch. Version and content pins are intentionally not a
// startup dependency; valid local Pi and extension updates remain usable.
func ValidateManagedLaunchConfig(config Config) error {
	if !config.Enabled {
		return nil
	}
	copy := config
	copy.normalize()
	if copy.PiExecutable == "" || copy.ManagedHome == "" || copy.PiAgentDir == "" || copy.SessionRoot == "" || copy.RouterURL == "" || copy.ManifestPath == "" || copy.MechaRPCExtension == "" || copy.PiSubagentsExtension == "" {
		return errors.New("managed mode requires core launch inputs")
	}
	if err := ValidateManagedRouterURL(copy.RouterURL); err != nil {
		return err
	}
	if copy.RequireProviderRuntime && (copy.AuthHelperPath == "" || copy.RuntimeRoot == "") {
		return errors.New("managed mode requires fixed provider runtime paths")
	}
	if !managedRecipientRE.MatchString(copy.CanonicalQuietRecipientID) {
		return errors.New("canonical quiet recipient is invalid")
	}
	if len(copy.AllowedWorkingDirectoryRoots) == 0 {
		return errors.New("managed mode requires an allowed working-directory root")
	}
	if copy.Limits.MaxAgents < 1 || copy.Limits.MaxAgents > MaxManagedAgents || copy.Limits.MaxInitialInstructionBytes < 1 || copy.Limits.MaxInitialInstructionBytes > MaxInitialInstructionBytes || copy.Limits.MaxRPCLineBytes < 1024 || copy.Limits.MaxRPCLineBytes > MaxRPCLineBytes || copy.Limits.SubagentCapacity < 1 || copy.Limits.GracefulStop <= 0 || copy.Limits.TermStop <= 0 {
		return errors.New("managed limits are outside the first-slice bounds")
	}
	if err := ValidateManagedExecutable(copy.PiExecutable); err != nil {
		return err
	}
	if err := ValidateContainedDirectory(copy.ManagedHome, "managed home"); err != nil {
		return err
	}
	if err := ValidateContainedDirectory(copy.PiAgentDir, "ordinary Pi agent directory"); err != nil {
		return err
	}
	if err := ValidateContainedDirectory(copy.SessionRoot, "managed session root"); err != nil {
		return err
	}
	if copy.RequireProviderRuntime {
		if err := ValidateContainedDirectory(copy.RuntimeRoot, "managed runtime root"); err != nil {
			return err
		}
	}
	if err := containedPath(copy.SessionRoot, copy.ManagedHome); err != nil {
		return fmt.Errorf("managed session root: %w", err)
	}
	for _, root := range copy.AllowedWorkingDirectoryRoots {
		if err := ValidateContainedDirectory(root, "allowed working-directory root"); err != nil {
			return err
		}
	}
	for _, input := range []struct {
		name string
		path string
	}{
		{"manifest", copy.ManifestPath},
		{"thin extension", copy.MechaRPCExtension},
		{"pi-subagents entry", copy.PiSubagentsExtension},
	} {
		if err := ValidateContainedRegularFile(input.path, "", input.name); err != nil {
			return err
		}
	}
	if copy.RequireProviderRuntime {
		requiredFiles := []struct {
			name string
			path string
		}{
			{"managed manifest", copy.ManifestPath},
			{"thin extension", copy.MechaRPCExtension},
			{"auth helper", copy.AuthHelperPath},
		}
		if copy.CursorExtension != "" {
			requiredFiles = append(requiredFiles, struct {
				name string
				path string
			}{"Cursor extension", copy.CursorExtension})
		}
		if copy.AntigravityExtension != "" {
			if err := ValidateContainedRegularFile(copy.AntigravityExtension, "", "Antigravity extension"); err != nil {
				return err
			}
		}
		for _, input := range requiredFiles {
			if err := ValidateContainedRegularFile(input.path, copy.RuntimeRoot, input.name); err != nil {
				return err
			}
		}
	}
	for _, path := range copy.TestOnlyExtensions {
		if err := ValidateContainedRegularFile(path, "", "test-only extension"); err != nil {
			return err
		}
	}
	return nil
}

// ManagedRouterURLForListener derives the loopback endpoint a managed process
// can use for this router. A wildcard listener is still reached through its
// local IPv4 loopback address.
func ManagedRouterURLForListener(host string, port int) (string, error) {
	endpointHost := host
	if endpointHost == "0.0.0.0" {
		endpointHost = "127.0.0.1"
	}
	raw := "http://" + net.JoinHostPort(endpointHost, strconv.Itoa(port))
	if err := ValidateManagedRouterURL(raw); err != nil {
		return "", err
	}
	return raw, nil
}

// ValidateManagedRouterURL accepts only one local base URL. Managed child
// processes inherit this explicit value, never an ambient canonical-router URL.
func ValidateManagedRouterURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" || parsed.RawPath != "" || parsed.Host == "" {
		return errors.New("managed router URL must be an http loopback base URL")
	}
	if parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1" {
		return errors.New("managed router URL must use a loopback host")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return errors.New("managed router URL must include a valid port")
	}
	return nil
}

func ValidateManagedExecutable(path string) error {
	if err := ValidateContainedRegularFile(path, "", "Pi executable"); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("Pi executable is unavailable: %w", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return errors.New("Pi executable is not executable")
	}
	return nil
}

func ValidateContainedRegularFile(path, root, label string) error {
	if path == "" {
		return fmt.Errorf("%s path is required", label)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("%s path: %w", label, err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return fmt.Errorf("%s is unavailable: %w", label, err)
	}
	resolved := absolute
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, err = filepath.EvalSymlinks(absolute)
		if err != nil {
			return fmt.Errorf("%s symlink target is unavailable: %w", label, err)
		}
		info, err = os.Stat(resolved)
		if err != nil {
			return fmt.Errorf("%s is unavailable: %w", label, err)
		}
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s must be a regular file", label)
	}
	if root != "" {
		if err := containedPath(resolved, root); err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
	}
	return nil
}

func ValidateContainedDirectory(path, label string) error {
	if path == "" {
		return fmt.Errorf("%s path is required", label)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return fmt.Errorf("%s is unavailable: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s must be a directory, not a symlink", label)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s must be a directory", label)
	}
	return nil
}

func ValidateWorkingDirectory(path string, roots []string) (string, error) {
	if path == "" {
		return "", errors.New("invalid working directory")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("working directory home is unavailable: %w", err)
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	} else if home, err := os.UserHomeDir(); err == nil && home != "" {
		username := filepath.Base(home)
		// Transparently map between macOS (/Users/<user>) and Linux (/home/<user>)
		// when switching machines with shared browser history/recent folders.
		if strings.HasPrefix(path, "/home/"+username+"/") || path == "/home/"+username {
			candidate := filepath.Join(home, strings.TrimPrefix(path, "/home/"+username))
			if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
				path = candidate
			}
		} else if strings.HasPrefix(path, "/Users/"+username+"/") || path == "/Users/"+username {
			candidate := filepath.Join(home, strings.TrimPrefix(path, "/Users/"+username))
			if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
				path = candidate
			}
		}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("working directory is unavailable: %w", err)
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return "", errors.New("invalid working directory")
	}
	for _, root := range roots {
		if containedPath(real, root) == nil {
			return real, nil
		}
	}
	return "", errors.New("working directory is outside allowed roots")
}

func containedPath(path, root string) error {
	pathReal, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(rootReal, pathReal)
	if err != nil {
		return err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("path is outside configured root")
	}
	return nil
}

const managedSystemPath = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

func ChildEnvironment(c Config) []string {
	return ChildEnvironmentFor(c, "", 1)
}
func ChildEnvironmentFor(c Config, runtimeGeneration string, sessionEpoch int64) []string {
	home, _ := filepath.Abs(c.ManagedHome)
	session, _ := filepath.Abs(c.SessionRoot)
	temp := filepath.Join(home, "tmp")
	piAgentDir, _ := filepath.Abs(c.PiAgentDir)
	environment := []string{"HOME=" + home, "PATH=" + managedChildPath(c.PiExecutable), "TMPDIR=" + temp, "LANG=C", "LC_ALL=C", "PI_CODING_AGENT_DIR=" + piAgentDir, "MECHA_MANAGED_SESSION_ROOT=" + session, "MECHA_MANAGED_ACTIVE_ONLY=1", "MECHA_ROUTER_URL=" + c.RouterURL}
	if hostHome, err := os.UserHomeDir(); err == nil && hostHome != "" {
		environment = append(environment, "MECHA_HOST_HOME="+hostHome)
	} else if hostHome := os.Getenv("HOME"); hostHome != "" {
		environment = append(environment, "MECHA_HOST_HOME="+hostHome)
	}
	if gCreds := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); gCreds != "" {
		if absCreds, err := filepath.Abs(gCreds); err == nil && absCreds != "" {
			environment = append(environment, "GOOGLE_APPLICATION_CREDENTIALS="+absCreds)
		} else {
			environment = append(environment, "GOOGLE_APPLICATION_CREDENTIALS="+gCreds)
		}
	} else if os.Getenv("CLOUDSDK_CONFIG") == "" {
		if hostHome := os.Getenv("MECHA_HOST_HOME"); hostHome != "" {
			candidate := filepath.Join(hostHome, ".config", "gcloud", "application_default_credentials.json")
			if _, err := os.Stat(candidate); err == nil {
				environment = append(environment, "GOOGLE_APPLICATION_CREDENTIALS="+candidate)
			}
		} else if userHome, err := os.UserHomeDir(); err == nil && userHome != "" {
			candidate := filepath.Join(userHome, ".config", "gcloud", "application_default_credentials.json")
			if _, err := os.Stat(candidate); err == nil {
				environment = append(environment, "GOOGLE_APPLICATION_CREDENTIALS="+candidate)
			}
		}
	}
	if cloudSDK := os.Getenv("CLOUDSDK_CONFIG"); cloudSDK != "" {
		environment = append(environment, "CLOUDSDK_CONFIG="+cloudSDK)
	}
	if runtimeGeneration != "" {
		environment = append(environment, "MECHA_RPC_RUNTIME_GENERATION="+runtimeGeneration, fmt.Sprintf("MECHA_RPC_SESSION_EPOCH=%d", sessionEpoch))
	}
	return append(environment, c.TestOnlyEnvironment...)
}

// managedChildPath adds only directories traversed while resolving the
// configured launcher. Pi's launcher uses `#!/usr/bin/env node`; a stable shim
// can point at an NVM launcher which in turn points at the JS entrypoint.
func managedChildPath(piExecutable string) string {
	directories, ok := managedLauncherDirectories(piExecutable)
	if !ok || len(directories) == 0 {
		return managedSystemPath
	}
	return strings.Join(append(directories, strings.Split(managedSystemPath, ":")...), ":")
}

const maxManagedLauncherSymlinks = 32

func managedLauncherDirectories(piExecutable string) ([]string, bool) {
	if piExecutable == "" {
		return nil, false
	}
	current, err := filepath.Abs(piExecutable)
	if err != nil {
		return nil, false
	}
	seenPaths := make(map[string]struct{}, maxManagedLauncherSymlinks)
	directories := make([]string, 0, 2)
	seenDirectories := make(map[string]struct{}, 2)
	addDirectory := func(path string) bool {
		directory := filepath.Dir(path)
		if !filepath.IsAbs(directory) || strings.ContainsRune(directory, filepath.ListSeparator) {
			return false
		}
		for _, systemDirectory := range strings.Split(managedSystemPath, ":") {
			if directory == systemDirectory {
				return true
			}
		}
		if _, duplicate := seenDirectories[directory]; !duplicate {
			directories = append(directories, directory)
			seenDirectories[directory] = struct{}{}
		}
		return true
	}
	for links := 0; links <= maxManagedLauncherSymlinks; links++ {
		if _, duplicate := seenPaths[current]; duplicate {
			return nil, false
		}
		seenPaths[current] = struct{}{}
		info, err := os.Lstat(current)
		if err != nil {
			return nil, false
		}
		if info.Mode()&os.ModeSymlink == 0 {
			if !info.Mode().IsRegular() {
				return nil, false
			}
			if len(directories) == 0 && !addDirectory(current) {
				return nil, false
			}
			for left, right := 0, len(directories)-1; left < right; left, right = left+1, right-1 {
				directories[left], directories[right] = directories[right], directories[left]
			}
			return directories, true
		}
		if !addDirectory(current) {
			return nil, false
		}
		target, err := os.Readlink(current)
		if err != nil {
			return nil, false
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(current), target)
		}
		current, err = filepath.Abs(target)
		if err != nil {
			return nil, false
		}
	}
	return nil, false
}

var managedRecipientRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
