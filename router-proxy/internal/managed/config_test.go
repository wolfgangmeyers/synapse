package managed

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func managedLaunchConfigFixture(t *testing.T) (Config, map[string]string) {
	t.Helper()
	root := t.TempDir()
	write := func(name, body string, mode os.FileMode) string {
		path := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(body), mode))
		return path
	}
	pi := write("bin/pi", "#!/bin/sh\nprintf 'any local version\\n'\n", 0o700)
	manifest := write("contract/managed-tools.json", `{"revision":1,"tools":[]}`, 0o600)
	mecha := write("extensions/mecha-rpc.ts", "export default function extension() {}\n", 0o600)
	subagents := write("extensions/pi-subagents.ts", "export default function extension() {}\n", 0o600)
	cursor := write("auth-helper/node_modules/pi-cursor-sdk/dist/index.js", "export default function extension() {}\n", 0o600)
	helper := write("auth-helper/index.mjs", "#!/usr/bin/env node\n", 0o700)
	home := filepath.Join(root, "home")
	piAgentDir := filepath.Join(root, "ordinary-pi", "agent")
	session := filepath.Join(home, "sessions")
	work := filepath.Join(root, "work")
	require.NoError(t, os.MkdirAll(session, 0o700))
	require.NoError(t, os.MkdirAll(piAgentDir, 0o700))
	require.NoError(t, os.MkdirAll(work, 0o700))
	return Config{Enabled: true, PiExecutable: pi, ManagedHome: home, PiAgentDir: piAgentDir, SessionRoot: session, ManifestPath: manifest, MechaRPCExtension: mecha, PiSubagentsExtension: subagents, CursorExtension: cursor, AuthHelperPath: helper, RuntimeRoot: root, RequireProviderRuntime: true, AllowedWorkingDirectoryRoots: []string{work}, CanonicalQuietRecipientID: "supervisor"}, map[string]string{"pi": pi, "manifest": manifest, "mecha": mecha, "subagents": subagents, "cursor": cursor, "helper": helper, "home": home, "pi_agent_dir": piAgentDir, "session": session, "work": work}
}

func TestManagedLaunchConfigRequiresOnlyCorePathsAndLimits(t *testing.T) {
	config, paths := managedLaunchConfigFixture(t)
	require.NoError(t, ValidateManagedLaunchConfig(config))

	// Valid local updates remain launchable. Phase 2 no longer pins versions or
	// file content for Pi or either required extension.
	require.NoError(t, os.WriteFile(paths["pi"], []byte("#!/bin/sh\nprintf 'changed version\\n'\n"), 0o700))
	require.NoError(t, os.WriteFile(paths["mecha"], []byte("export default function changed() {}\n"), 0o600))
	require.NoError(t, os.WriteFile(paths["subagents"], []byte("export default function changed() {}\n"), 0o600))
	require.NoError(t, ValidateManagedLaunchConfig(config))

	config.PiExecutable = ""
	require.Error(t, ValidateManagedLaunchConfig(config))

	// CursorExtension is optional.
	config, paths = managedLaunchConfigFixture(t)
	config.CursorExtension = ""
	require.NoError(t, ValidateManagedLaunchConfig(config))

	config.CursorExtension = filepath.Join(paths["work"], "nonexistent.js")
	require.Error(t, ValidateManagedLaunchConfig(config))
}

func TestValidateWorkingDirectoryExpandsHomeRelativePaths(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "code", "project")
	require.NoError(t, os.MkdirAll(project, 0o700))
	t.Setenv("HOME", home)

	resolved, err := ValidateWorkingDirectory("~/code/project", []string{filepath.Join(home, "code")})
	expected, expectedErr := filepath.EvalSymlinks(project)

	require.NoError(t, err)
	require.NoError(t, expectedErr)
	require.Equal(t, expected, resolved)
}

func TestManagedLaunchConfigRejectsUnsafeCorePaths(t *testing.T) {
	config, paths := managedLaunchConfigFixture(t)
	outside := t.TempDir()
	require.NoError(t, os.Remove(paths["session"]))
	require.NoError(t, os.Symlink(outside, paths["session"]))
	require.Error(t, ValidateManagedLaunchConfig(config))

	link := filepath.Join(paths["work"], "escaped-extension.ts")
	escaped := filepath.Join(outside, "escaped-extension.ts")
	require.NoError(t, os.WriteFile(escaped, []byte("export {}"), 0o600))
	require.NoError(t, os.Symlink(escaped, link))
	require.Error(t, ValidateContainedRegularFile(link, paths["work"], "extension"))
}
