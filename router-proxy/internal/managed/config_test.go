package managed

import (
	"os"
	"path/filepath"
	"strings"
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
	return Config{Enabled: true, PiExecutable: pi, ManagedHome: home, PiAgentDir: piAgentDir, SessionRoot: session, RouterURL: "http://127.0.0.1:18775", ManifestPath: manifest, MechaRPCExtension: mecha, PiSubagentsExtension: subagents, CursorExtension: cursor, AuthHelperPath: helper, RuntimeRoot: root, RequireProviderRuntime: true, AllowedWorkingDirectoryRoots: []string{work}, CanonicalQuietRecipientID: "supervisor"}, map[string]string{"pi": pi, "manifest": manifest, "mecha": mecha, "subagents": subagents, "cursor": cursor, "helper": helper, "home": home, "pi_agent_dir": piAgentDir, "session": session, "work": work}
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

	// AntigravityExtension is optional.
	config, paths = managedLaunchConfigFixture(t)
	config.AntigravityExtension = ""
	require.NoError(t, ValidateManagedLaunchConfig(config))

	config.AntigravityExtension = filepath.Join(paths["work"], "nonexistent-ag.ts")
	require.Error(t, ValidateManagedLaunchConfig(config))
}

func TestManagedChildPathUsesNVMRuntimeBehindStableShim(t *testing.T) {
	root := t.TempDir()
	shimDir := filepath.Join(root, "stable-bin")
	nvmDir := filepath.Join(root, "nvm-bin")
	entrypointDir := filepath.Join(root, "pi-package", "dist")
	require.NoError(t, os.MkdirAll(shimDir, 0o700))
	require.NoError(t, os.MkdirAll(nvmDir, 0o700))
	require.NoError(t, os.MkdirAll(entrypointDir, 0o700))
	entrypoint := filepath.Join(entrypointDir, "cli.js")
	nvmLauncher := filepath.Join(nvmDir, "pi")
	shim := filepath.Join(shimDir, "pi")
	require.NoError(t, os.WriteFile(entrypoint, []byte("#!/usr/bin/env node\n"), 0o700))
	require.NoError(t, os.Symlink(entrypoint, nvmLauncher))
	require.NoError(t, os.Symlink(nvmLauncher, shim))

	require.Equal(t, nvmDir+":"+shimDir+":"+managedSystemPath, managedChildPath(shim))
}

func TestManagedRouterURLForListenerUsesAnOwningLoopbackURL(t *testing.T) {
	url, err := ManagedRouterURLForListener("0.0.0.0", 18775)
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:18775", url)

	url, err = ManagedRouterURLForListener("::1", 18776)
	require.NoError(t, err)
	require.Equal(t, "http://[::1]:18776", url)
	_, err = ManagedRouterURLForListener("0.0.0.0", 0)
	require.Error(t, err)
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

func TestValidateWorkingDirectoryTranslatesCrossPlatformHome(t *testing.T) {
	home := t.TempDir()
	username := filepath.Base(home)
	project := filepath.Join(home, "code", "project")
	require.NoError(t, os.MkdirAll(project, 0o700))
	t.Setenv("HOME", home)

	// If on macOS/other, simulated Linux path /home/<user>/code/project
	linuxPath := filepath.Join("/home", username, "code", "project")
	resolved, err := ValidateWorkingDirectory(linuxPath, []string{home})
	expected, expectedErr := filepath.EvalSymlinks(project)

	require.NoError(t, err)
	require.NoError(t, expectedErr)
	require.Equal(t, expected, resolved)

	// Simulated macOS path /Users/<user>/code/project
	macPath := filepath.Join("/Users", username, "code", "project")
	resolvedMac, errMac := ValidateWorkingDirectory(macPath, []string{home})
	require.NoError(t, errMac)
	require.Equal(t, expected, resolvedMac)
}

func TestValidateWorkingDirectoryRejectsDirectoriesOutsideConfiguredRoots(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "code", "project")
	outside := t.TempDir()
	require.NoError(t, os.MkdirAll(workspace, 0o700))

	resolved, err := ValidateWorkingDirectory(workspace, []string{home})
	require.NoError(t, err)
	require.NotEmpty(t, resolved)
	_, err = ValidateWorkingDirectory(outside, []string{home})
	require.EqualError(t, err, "working directory is outside allowed roots")
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

func TestChildEnvironmentForDoesNotInjectDefaultGACOverExplicitCloudSDK(t *testing.T) {
	config, _ := managedLaunchConfigFixture(t)

	fakeHostHome := t.TempDir()
	defaultGcloudDir := filepath.Join(fakeHostHome, ".config", "gcloud")
	require.NoError(t, os.MkdirAll(defaultGcloudDir, 0o700))
	defaultADC := filepath.Join(defaultGcloudDir, "application_default_credentials.json")
	require.NoError(t, os.WriteFile(defaultADC, []byte(`{"type":"authorized_user","client_id":"host-default"}`), 0o600))

	customCloudSDK := t.TempDir()
	customADC := filepath.Join(customCloudSDK, "application_default_credentials.json")
	require.NoError(t, os.WriteFile(customADC, []byte(`{"type":"authorized_user","client_id":"custom-sdk"}`), 0o600))

	t.Setenv("HOME", fakeHostHome)
	t.Setenv("MECHA_HOST_HOME", fakeHostHome)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("CLOUDSDK_CONFIG", customCloudSDK)

	env := ChildEnvironmentFor(config, "", 0)

	var childGAC string
	var childCloudSDK string
	for _, entry := range env {
		if val, found := strings.CutPrefix(entry, "GOOGLE_APPLICATION_CREDENTIALS="); found {
			childGAC = val
		}
		if val, found := strings.CutPrefix(entry, "CLOUDSDK_CONFIG="); found {
			childCloudSDK = val
		}
	}

	require.Equal(t, customCloudSDK, childCloudSDK)
	require.NotEqual(t, defaultADC, childGAC, "host default GAC must not defeat explicit CLOUDSDK_CONFIG")
	require.Empty(t, childGAC, "no default GAC should be injected when explicit CLOUDSDK_CONFIG is set without GAC")
}

func TestChildEnvironmentForExplicitGACWinsOverCloudSDKAndDefault(t *testing.T) {
	config, _ := managedLaunchConfigFixture(t)

	fakeHostHome := t.TempDir()
	defaultGcloudDir := filepath.Join(fakeHostHome, ".config", "gcloud")
	require.NoError(t, os.MkdirAll(defaultGcloudDir, 0o700))
	defaultADC := filepath.Join(defaultGcloudDir, "application_default_credentials.json")
	require.NoError(t, os.WriteFile(defaultADC, []byte(`{"type":"authorized_user","client_id":"host-default"}`), 0o600))

	customCloudSDK := t.TempDir()
	customADC := filepath.Join(customCloudSDK, "application_default_credentials.json")
	require.NoError(t, os.WriteFile(customADC, []byte(`{"type":"authorized_user","client_id":"custom-sdk"}`), 0o600))

	gacDir := t.TempDir()
	explicitGAC := filepath.Join(gacDir, "custom-gac.json")
	require.NoError(t, os.WriteFile(explicitGAC, []byte(`{"type":"service_account","client_email":"explicit@test"}`), 0o600))

	t.Setenv("HOME", fakeHostHome)
	t.Setenv("MECHA_HOST_HOME", fakeHostHome)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", explicitGAC)
	t.Setenv("CLOUDSDK_CONFIG", customCloudSDK)

	env := ChildEnvironmentFor(config, "", 0)

	var childGAC string
	var childCloudSDK string
	for _, entry := range env {
		if val, found := strings.CutPrefix(entry, "GOOGLE_APPLICATION_CREDENTIALS="); found {
			childGAC = val
		}
		if val, found := strings.CutPrefix(entry, "CLOUDSDK_CONFIG="); found {
			childCloudSDK = val
		}
	}

	absExplicitGAC, err := filepath.Abs(explicitGAC)
	require.NoError(t, err)
	require.Equal(t, absExplicitGAC, childGAC, "explicit GAC must take precedence over CloudSDK and default ADC")
	require.Equal(t, customCloudSDK, childCloudSDK)
}

func TestChildEnvironmentForFallbackToDefaultWhenCloudSDKUnset(t *testing.T) {
	config, _ := managedLaunchConfigFixture(t)

	fakeHostHome := t.TempDir()
	defaultGcloudDir := filepath.Join(fakeHostHome, ".config", "gcloud")
	require.NoError(t, os.MkdirAll(defaultGcloudDir, 0o700))
	defaultADC := filepath.Join(defaultGcloudDir, "application_default_credentials.json")
	require.NoError(t, os.WriteFile(defaultADC, []byte(`{"type":"authorized_user","client_id":"host-default"}`), 0o600))

	t.Setenv("HOME", fakeHostHome)
	t.Setenv("MECHA_HOST_HOME", fakeHostHome)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("CLOUDSDK_CONFIG", "")

	env := ChildEnvironmentFor(config, "", 0)

	var childGAC string
	var childCloudSDK string
	for _, entry := range env {
		if val, found := strings.CutPrefix(entry, "GOOGLE_APPLICATION_CREDENTIALS="); found {
			childGAC = val
		}
		if val, found := strings.CutPrefix(entry, "CLOUDSDK_CONFIG="); found {
			childCloudSDK = val
		}
	}

	require.Equal(t, defaultADC, childGAC, "when GAC and CLOUDSDK_CONFIG unset, fallback to host default ADC")
	require.Empty(t, childCloudSDK)
}

func TestChildEnvironmentUsesChildEnvironmentFor(t *testing.T) {
	config, paths := managedLaunchConfigFixture(t)
	env := ChildEnvironment(config)

	var hasHome, hasSessionRoot bool
	for _, entry := range env {
		if strings.HasPrefix(entry, "HOME=") {
			hasHome = true
		}
		if entry == "MECHA_MANAGED_SESSION_ROOT="+paths["session"] {
			hasSessionRoot = true
		}
	}
	require.True(t, hasHome)
	require.True(t, hasSessionRoot)
}

func TestChildEnvironmentForMissingCloudSDKDirDoesNotFallbackToDefaultADC(t *testing.T) {
	config, _ := managedLaunchConfigFixture(t)

	fakeHostHome := t.TempDir()
	defaultGcloudDir := filepath.Join(fakeHostHome, ".config", "gcloud")
	require.NoError(t, os.MkdirAll(defaultGcloudDir, 0o700))
	defaultADC := filepath.Join(defaultGcloudDir, "application_default_credentials.json")
	require.NoError(t, os.WriteFile(defaultADC, []byte(`{"type":"authorized_user","client_id":"host-default"}`), 0o600))

	missingCloudSDK := filepath.Join(t.TempDir(), "nonexistent-cloudsdk-dir")

	t.Setenv("HOME", fakeHostHome)
	t.Setenv("MECHA_HOST_HOME", fakeHostHome)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("CLOUDSDK_CONFIG", missingCloudSDK)

	env := ChildEnvironmentFor(config, "", 0)

	var childGAC string
	var childCloudSDK string
	for _, entry := range env {
		if val, found := strings.CutPrefix(entry, "GOOGLE_APPLICATION_CREDENTIALS="); found {
			childGAC = val
		}
		if val, found := strings.CutPrefix(entry, "CLOUDSDK_CONFIG="); found {
			childCloudSDK = val
		}
	}

	require.Equal(t, missingCloudSDK, childCloudSDK, "exact CLOUDSDK_CONFIG must be forwarded even when directory is missing")
	require.NotEqual(t, defaultADC, childGAC, "broken/missing CLOUDSDK_CONFIG must not quietly fall back to host default ADC")
	require.Empty(t, childGAC, "no GAC should be injected when explicit CLOUDSDK_CONFIG is set")
}




