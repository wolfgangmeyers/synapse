package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/stretchr/testify/require"
)

func TestFixedAuthHelperSnapshotUsesOrdinaryPiAgentDirectory(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required for the fixed auth helper")
	}
	root := t.TempDir()
	managedHome := filepath.Join(root, "managed-home")
	piAgentDir := filepath.Join(root, "ordinary", ".pi", "agent")
	require.NoError(t, os.MkdirAll(managedHome, 0o700))
	require.NoError(t, os.MkdirAll(piAgentDir, 0o700))
	helperPath := filepath.Join(root, "helper.mjs")
	script := fmt.Sprintf(`if (process.env.PI_CODING_AGENT_DIR !== %q || process.env.MECHA_PI_AUTH_PATH !== %q) process.exit(1);
console.log(JSON.stringify({providers:[{provider:"openai-codex",label:"ChatGPT",authenticated:true,status:"authenticated"},{provider:"google",label:"Google",authenticated:false,status:"signed_out"},{provider:"cursor",label:"Cursor",authenticated:false,status:"signed_out"}],models:[{provider:"openai-codex",id:"gpt-5.6-terra",name:"GPT-5.6 Terra",available:true}]}));
`, piAgentDir, filepath.Join(piAgentDir, "auth.json"))
	require.NoError(t, os.WriteFile(helperPath, []byte(script), 0o600))

	providers, models, err := (fixedAuthHelper{config: managed.Config{ManagedHome: managedHome, PiAgentDir: piAgentDir, AuthHelperPath: helperPath}, nodeExecutable: node}).Snapshot(context.Background())
	require.NoError(t, err)
	require.Len(t, providers, 3)
	require.Equal(t, "openai-codex", providers[0].Provider)
	require.Len(t, models, 1)
	require.Equal(t, "gpt-5.6-terra", models[0].ID)
}

func TestManagedStartupUsesOnlyCoreLaunchInputs(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	piAgentDir := filepath.Join(root, "ordinary-pi", "agent")
	session := filepath.Join(home, "sessions")
	work := filepath.Join(root, "work")
	require.NoError(t, os.MkdirAll(session, 0o700))
	require.NoError(t, os.MkdirAll(piAgentDir, 0o700))
	require.NoError(t, os.MkdirAll(work, 0o700))
	write := func(name, value string, mode os.FileMode) string {
		path := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(value), mode))
		return path
	}
	config := managed.Config{Enabled: true, PiExecutable: write("bin/pi", "#!/bin/sh\n", 0o700), ManagedHome: home, PiAgentDir: piAgentDir, SessionRoot: session, RouterURL: "http://127.0.0.1:18775", ManifestPath: write("contract/managed.json", `{}`, 0o600), MechaRPCExtension: write("extensions/rpc.ts", "export {}", 0o600), PiSubagentsExtension: write("extensions/subagents.ts", "export {}", 0o600), AllowedWorkingDirectoryRoots: []string{work}, CanonicalQuietRecipientID: "supervisor"}
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
