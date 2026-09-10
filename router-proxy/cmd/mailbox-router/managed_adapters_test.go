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

func TestHelperEventDecoderNeverRelaysChildOwnedStrings(t *testing.T) {
	event, ok := decodeHelperEvent([]byte(`{"type":"prompt","operation_id":"operation-1","provider":"google","prompt":{"id":"prompt-1","kind":"secret","message":"sentinel-secret","placeholder":"sentinel-secret"}}`))
	require.True(t, ok)
	require.NotNil(t, event.Prompt)
	require.NotContains(t, event.Prompt.Message, "sentinel-secret")
	require.Empty(t, event.Prompt.Placeholder)
	trusted, ok := decodeHelperEvent([]byte(`{"type":"prompt","operation_id":"operation-1","provider":"openai-codex","prompt":{"id":"prompt-2","kind":"manual_code","message":"untrusted","url":"https://auth.openai.com/authorize?state=browser-only"}}`))
	require.True(t, ok)
	require.NotNil(t, trusted.Prompt)
	require.Equal(t, "https://auth.openai.com/authorize?state=browser-only", trusted.Prompt.URL)
	unsafe, ok := decodeHelperEvent([]byte(`{"type":"prompt","operation_id":"operation-1","provider":"openai-codex","prompt":{"id":"prompt-3","kind":"manual_code","message":"untrusted","url":"https://example.invalid/authorize"}}`))
	require.True(t, ok)
	require.NotNil(t, unsafe.Prompt)
	require.Empty(t, unsafe.Prompt.URL)
	failed, ok := decodeHelperEvent([]byte(`{"type":"terminal","operation_id":"operation-1","provider":"google","status":"failed","error":"sentinel-secret"}`))
	require.True(t, ok)
	require.Equal(t, "Provider sign-in did not complete", failed.Error)
	_, ok = decodeHelperEvent([]byte(`{"type":"state","operation_id":"operation-1","provider":"google","status":"sentinel-secret"}`))
	require.False(t, ok)
}

func TestParseManagedRootsDropsEmptyValues(t *testing.T) {
	require.Equal(t, []string{"/one", "/two"}, parseManagedRoots(" /one, ,/two "))
}
