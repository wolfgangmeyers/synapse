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
	MaxManagedAgents           = 32
	MaxInitialInstructionBytes = 32 * 1024
	MaxRPCLineBytes            = 1 << 20
)

type Limits struct {
	MaxAgents                  int
	MaxInitialInstructionBytes int
	MaxRPCLineBytes            int
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
	return Limits{MaxAgents: MaxManagedAgents, MaxInitialInstructionBytes: MaxInitialInstructionBytes, MaxRPCLineBytes: MaxRPCLineBytes, GracefulStop: 5 * time.Second, TermStop: 5 * time.Second}
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
	if copy.Limits.MaxAgents < 1 || copy.Limits.MaxAgents > MaxManagedAgents || copy.Limits.MaxInitialInstructionBytes < 1 || copy.Limits.MaxInitialInstructionBytes > MaxInitialInstructionBytes || copy.Limits.MaxRPCLineBytes < 1024 || copy.Limits.MaxRPCLineBytes > MaxRPCLineBytes || copy.Limits.GracefulStop <= 0 || copy.Limits.TermStop <= 0 {
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

func ChildEnvironment(c Config) []string {
	return ChildEnvironmentFor(c, "", 1)
}
func ChildEnvironmentFor(c Config, runtimeGeneration string, sessionEpoch int64) []string {
	home, _ := filepath.Abs(c.ManagedHome)
	session, _ := filepath.Abs(c.SessionRoot)
	temp := filepath.Join(home, "tmp")
	piAgentDir, _ := filepath.Abs(c.PiAgentDir)
	environment := []string{"HOME=" + home, "PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin", "TMPDIR=" + temp, "LANG=C", "LC_ALL=C", "PI_CODING_AGENT_DIR=" + piAgentDir, "MECHA_MANAGED_SESSION_ROOT=" + session, "MECHA_MANAGED_ACTIVE_ONLY=1", "MECHA_ROUTER_URL=" + c.RouterURL}
	if runtimeGeneration != "" {
		environment = append(environment, "MECHA_RPC_RUNTIME_GENERATION="+runtimeGeneration, fmt.Sprintf("MECHA_RPC_SESSION_EPOCH=%d", sessionEpoch))
	}
	return append(environment, c.TestOnlyEnvironment...)
}

var managedRecipientRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
