// Package managed contains the narrow router-managed Pi launch boundary.
package managed

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
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

type ExpectedIdentities struct {
	PiVersion              string
	PiSHA256               string
	ManifestSHA256         string
	MechaRPCHash           string
	PiSubagentsEntryHash   string
	PiSubagentsTreeHash    string
	CursorExtensionHash    string
	CursorPackageHash      string
	CursorResolvedIdentity string
	CursorLockfileHash     string
	CursorTreeHash         string
	HelperHash             string
	HelperLockfileHash     string
	HelperTreeHash         string
	UIDistributionTreeHash string
}

type FixedInput struct {
	Name        string
	Path        string
	Root        string
	SHA256      string
	TreeSHA256  string
	RegularFile bool
	Required    bool
}

type Config struct {
	Enabled                      bool
	PiExecutable                 string
	ManagedHome                  string
	SessionRoot                  string
	ManifestPath                 string
	MechaRPCExtension            string
	PiSubagentsExtension         string
	CursorExtension              string
	CursorPackage                string
	CursorLockfile               string
	HelperPath                   string
	HelperLockfile               string
	UIDistribution               string
	AllowedWorkingDirectoryRoots []string
	CanonicalQuietRecipientID    string
	Expected                     ExpectedIdentities
	FixedInputs                  []FixedInput
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

func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	copy := c
	copy.normalize()
	if copy.PiExecutable == "" || copy.ManagedHome == "" || copy.SessionRoot == "" || copy.ManifestPath == "" || copy.MechaRPCExtension == "" || copy.PiSubagentsExtension == "" || copy.CursorExtension == "" || copy.CursorLockfile == "" || copy.HelperPath == "" || copy.HelperLockfile == "" || copy.UIDistribution == "" {
		return errors.New("managed mode requires every fixed runtime input")
	}
	if !managedRecipientRE.MatchString(copy.CanonicalQuietRecipientID) {
		return errors.New("canonical quiet recipient is invalid")
	}
	if copy.Expected.PiVersion != "0.85.0" || copy.Expected.PiSHA256 == "" || copy.Expected.ManifestSHA256 == "" || copy.Expected.MechaRPCHash == "" || copy.Expected.PiSubagentsEntryHash == "" || copy.Expected.PiSubagentsTreeHash == "" || copy.Expected.CursorExtensionHash == "" || copy.Expected.CursorPackageHash == "" || copy.Expected.CursorResolvedIdentity == "" || copy.Expected.CursorLockfileHash == "" || copy.Expected.CursorTreeHash == "" || copy.Expected.HelperHash == "" || copy.Expected.HelperLockfileHash == "" || copy.Expected.HelperTreeHash == "" || copy.Expected.UIDistributionTreeHash == "" {
		return errors.New("managed mode requires pinned immutable identities for every fixed runtime input")
	}
	if len(copy.AllowedWorkingDirectoryRoots) == 0 {
		return errors.New("managed mode requires an allowed working-directory root")
	}
	if copy.Limits.MaxAgents < 1 || copy.Limits.MaxAgents > MaxManagedAgents || copy.Limits.MaxInitialInstructionBytes < 1 || copy.Limits.MaxInitialInstructionBytes > MaxInitialInstructionBytes || copy.Limits.MaxRPCLineBytes < 1024 || copy.Limits.MaxRPCLineBytes > MaxRPCLineBytes || copy.Limits.GracefulStop <= 0 || copy.Limits.TermStop <= 0 {
		return errors.New("managed limits are outside the first-slice bounds")
	}
	if err := ValidateContainedDirectory(copy.ManagedHome, "managed home"); err != nil {
		return err
	}
	if err := ValidateContainedDirectory(copy.SessionRoot, "managed session root"); err != nil {
		return err
	}
	if err := containedPath(copy.SessionRoot, copy.ManagedHome); err != nil {
		return fmt.Errorf("managed session root: %w", err)
	}
	if copy.CursorPackage == "" {
		return errors.New("Cursor package root is required")
	}
	if err := ValidateContainedDirectory(copy.CursorPackage, "Cursor package root"); err != nil {
		return err
	}
	if err := ValidateContainedDirectory(copy.UIDistribution, "UI distribution"); err != nil {
		return err
	}
	if err := ValidateContainedRegularFile(filepath.Join(copy.UIDistribution, "index.html"), copy.UIDistribution, "UI index"); err != nil {
		return err
	}
	for _, root := range copy.AllowedWorkingDirectoryRoots {
		if err := ValidateContainedDirectory(root, "allowed working-directory root"); err != nil {
			return err
		}
	}
	fileInputs := copy.requiredFileInputs()
	for _, input := range fileInputs {
		if err := ValidateContainedRegularFile(input.Path, input.Root, input.Name); err != nil {
			return err
		}
		if input.SHA256 != "" {
			if err := verifyFileHash(input.Path, input.SHA256); err != nil {
				return fmt.Errorf("%s: %w", input.Name, err)
			}
		}
		if input.Name == "manifest" && copy.Expected.ManifestSHA256 != "" {
			if err := verifyFileHash(input.Path, copy.Expected.ManifestSHA256); err != nil {
				return fmt.Errorf("manifest: %w", err)
			}
		}
	}
	for _, input := range copy.FixedInputs {
		if !input.Required && input.Path == "" {
			continue
		}
		if input.Path == "" {
			return fmt.Errorf("fixed input %s is required", input.Name)
		}
		if input.RegularFile || input.TreeSHA256 == "" {
			if err := ValidateContainedRegularFile(input.Path, input.Root, input.Name); err != nil {
				return err
			}
		} else if err := ValidateContainedDirectory(input.Path, input.Name); err != nil {
			return err
		}
		if input.SHA256 != "" {
			if err := verifyFileHash(input.Path, input.SHA256); err != nil {
				return fmt.Errorf("%s: %w", input.Name, err)
			}
		}
		if input.TreeSHA256 != "" {
			if err := verifyTreeHash(input.Path, input.TreeSHA256); err != nil {
				return fmt.Errorf("%s: %w", input.Name, err)
			}
		}
	}
	if copy.Expected.PiVersion != "" {
		output, err := exec.Command(copy.PiExecutable, "--version").Output()
		if err != nil || strings.TrimSpace(string(output)) != copy.Expected.PiVersion {
			return fmt.Errorf("installed Pi version does not match pinned identity")
		}
	}
	if copy.Expected.PiSHA256 != "" {
		if err := verifyFileHash(copy.PiExecutable, copy.Expected.PiSHA256); err != nil {
			return fmt.Errorf("Pi executable: %w", err)
		}
	}
	expected := []struct{ name, path, hash string }{
		{"thin extension", copy.MechaRPCExtension, copy.Expected.MechaRPCHash},
		{"pi-subagents entry", copy.PiSubagentsExtension, copy.Expected.PiSubagentsEntryHash},
		{"Cursor extension", copy.CursorExtension, copy.Expected.CursorExtensionHash},
		{"helper", copy.HelperPath, copy.Expected.HelperHash},
	}
	for _, item := range expected {
		if item.hash != "" {
			if err := verifyFileHash(item.path, item.hash); err != nil {
				return fmt.Errorf("%s: %w", item.name, err)
			}
		}
	}
	for _, item := range []struct{ name, path, hash string }{{"Cursor lockfile", copy.CursorLockfile, copy.Expected.CursorLockfileHash}, {"helper lockfile", copy.HelperLockfile, copy.Expected.HelperLockfileHash}} {
		if item.hash != "" {
			if err := verifyFileHash(item.path, item.hash); err != nil {
				return fmt.Errorf("%s: %w", item.name, err)
			}
		}
	}
	if err := verifyFileHash(filepath.Join(copy.CursorPackage, "package.json"), copy.Expected.CursorPackageHash); err != nil {
		return fmt.Errorf("Cursor package identity: %w", err)
	}
	if err := verifyCursorResolvedIdentity(copy.CursorPackage, copy.Expected.CursorResolvedIdentity); err != nil {
		return err
	}
	if copy.Expected.CursorTreeHash != "" {
		if err := verifyTreeHash(copy.CursorPackage, copy.Expected.CursorTreeHash); err != nil {
			return fmt.Errorf("Cursor package: %w", err)
		}
	}
	if copy.Expected.PiSubagentsTreeHash != "" {
		if err := verifyTreeHash(filepath.Dir(filepath.Dir(copy.PiSubagentsExtension)), copy.Expected.PiSubagentsTreeHash); err != nil {
			return fmt.Errorf("pi-subagents tree: %w", err)
		}
	}
	if copy.Expected.HelperTreeHash != "" {
		if err := verifyTreeHash(filepath.Dir(copy.HelperPath), copy.Expected.HelperTreeHash); err != nil {
			return fmt.Errorf("helper source tree: %w", err)
		}
	}
	if copy.Expected.UIDistributionTreeHash != "" {
		if err := verifyTreeHash(copy.UIDistribution, copy.Expected.UIDistributionTreeHash); err != nil {
			return fmt.Errorf("UI distribution: %w", err)
		}
	}
	return nil
}

func (c Config) requiredFileInputs() []FixedInput {
	return []FixedInput{
		{Name: "Pi executable", Path: c.PiExecutable},
		{Name: "manifest", Path: c.ManifestPath},
		{Name: "thin extension", Path: c.MechaRPCExtension},
		{Name: "pi-subagents entry", Path: c.PiSubagentsExtension},
		{Name: "Cursor extension", Path: c.CursorExtension},
		{Name: "Cursor lockfile", Path: c.CursorLockfile},
		{Name: "helper", Path: c.HelperPath},
		{Name: "helper lockfile", Path: c.HelperLockfile},
	}
}

func ValidateFixedInputs(c Config) error { return c.Validate() }

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

func verifyCursorResolvedIdentity(path, expected string) error {
	data, err := os.ReadFile(filepath.Join(path, "package.json"))
	if err != nil {
		return fmt.Errorf("Cursor resolved identity: %w", err)
	}
	var packageJSON struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &packageJSON); err != nil || packageJSON.Name == "" || packageJSON.Version == "" || packageJSON.Name+"@"+packageJSON.Version != expected {
		return errors.New("Cursor resolved package identity does not match pinned identity")
	}
	return nil
}

func verifyFileHash(path, expected string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	actual := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(actual[:]), expected) {
		return errors.New("pinned SHA-256 does not match")
	}
	return nil
}

func verifyTreeHash(path, expected string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("tree identity requires a directory")
	}
	type entry struct {
		name string
		data []byte
	}
	var entries []entry
	err = filepath.WalkDir(path, func(candidate string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if item.IsDir() {
			// Dependencies are pinned by their lockfile; the source tree hash
			// must not include the machine-specific node_modules installation.
			if item.Name() == "node_modules" {
				return fs.SkipDir
			}
			return nil
		}
		if item.Type()&os.ModeSymlink != 0 {
			return errors.New("tree contains a symlink")
		}
		data, readErr := os.ReadFile(candidate)
		if readErr != nil {
			return readErr
		}
		relative, relErr := filepath.Rel(path, candidate)
		if relErr != nil {
			return relErr
		}
		entries = append(entries, entry{filepath.ToSlash(relative), data})
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	h := sha256.New()
	for _, item := range entries {
		_, _ = h.Write([]byte(item.name))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(item.data)
		_, _ = h.Write([]byte{0})
	}
	if hex.EncodeToString(h.Sum(nil)) != strings.ToLower(expected) {
		return errors.New("pinned tree SHA-256 does not match")
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
	environment := []string{"HOME=" + home, "PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin", "TMPDIR=" + temp, "LANG=C", "LC_ALL=C", "PI_CODING_AGENT_DIR=" + filepath.Join(home, ".pi", "agent"), "MECHA_MANAGED_SESSION_ROOT=" + session, "MECHA_MANAGED_ACTIVE_ONLY=1"}
	if runtimeGeneration != "" {
		environment = append(environment, "MECHA_RPC_RUNTIME_GENERATION="+runtimeGeneration, fmt.Sprintf("MECHA_RPC_SESSION_EPOCH=%d", sessionEpoch))
	}
	return environment
}

var managedRecipientRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
