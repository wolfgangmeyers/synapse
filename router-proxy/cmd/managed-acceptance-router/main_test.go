package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func acceptanceArgs(port, provider string) []string {
	return []string{
		"--database", "/tmp/router.db", "--log", "/tmp/router.jsonl", "--host", "127.0.0.1", "--port", port,
		"--managed-pi", "/tmp/pi", "--managed-home", "/tmp/home", "--managed-session-root", "/tmp/home/sessions",
		"--managed-manifest", "/tmp/manifest.json", "--managed-mecha-rpc", "/tmp/mecha-rpc.ts", "--managed-pi-subagents", "/tmp/subagents.ts",
		"--managed-probe-extension", "/tmp/probe.ts", "--managed-cwd-root", "/tmp/work", "--test-provider-url", provider, "--static-ui-root", "/tmp/router-ui",
	}
}

func TestParseConfigRejectsLiveAndNonLoopbackAcceptanceResources(t *testing.T) {
	_, err := parseConfig(acceptanceArgs("8765", "http://127.0.0.1:9999/v1"))
	require.EqualError(t, err, "managed acceptance router must bind a non-live explicit loopback port")

	_, err = parseConfig(acceptanceArgs("9999", "https://example.com/v1"))
	require.EqualError(t, err, "managed acceptance router requires a loopback deterministic provider URL")
}

func TestValidateStaticUIRootRequiresBuiltDist(t *testing.T) {
	root := t.TempDir()
	require.EqualError(t, validateStaticUIRoot(root), "managed acceptance router static UI must contain index.html")
	require.NoError(t, os.WriteFile(filepath.Join(root, "index.html"), []byte("<!doctype html>"), 0600))
	require.NoError(t, validateStaticUIRoot(root))
}

func TestParseConfigAcceptsIsolatedLoopbackProvider(t *testing.T) {
	config, err := parseConfig(acceptanceArgs("9999", "http://127.0.0.1:9998/v1"))
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:9998/v1", config.providerURL)
}
