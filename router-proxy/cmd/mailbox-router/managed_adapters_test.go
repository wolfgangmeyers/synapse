package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/stretchr/testify/require"
)

func TestManagedStartupUsesOnlyCoreLaunchInputs(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	session := filepath.Join(home, "sessions")
	work := filepath.Join(root, "work")
	require.NoError(t, os.MkdirAll(session, 0o700))
	require.NoError(t, os.MkdirAll(work, 0o700))
	write := func(name, value string, mode os.FileMode) string {
		path := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(value), mode))
		return path
	}
	config := managed.Config{Enabled: true, PiExecutable: write("bin/pi", "#!/bin/sh\n", 0o700), ManagedHome: home, SessionRoot: session, ManifestPath: write("contract/managed.json", `{}`, 0o600), MechaRPCExtension: write("extensions/rpc.ts", "export {}", 0o600), PiSubagentsExtension: write("extensions/subagents.ts", "export {}", 0o600), AllowedWorkingDirectoryRoots: []string{work}, CanonicalQuietRecipientID: "supervisor"}
	require.NoError(t, validateManagedStartup(config))
	config.PiSubagentsExtension = ""
	require.Error(t, validateManagedStartup(config))
}

func TestParseManagedRootsDropsEmptyValues(t *testing.T) {
	require.Equal(t, []string{"/one", "/two"}, parseManagedRoots(" /one, ,/two "))
}
