package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestContractManifestScaffoldIsSelfContained(t *testing.T) {
	contractDir := filepath.Join("..", "router", "contract")
	manifest := readContractJSON(t, filepath.Join(contractDir, "manifest.json"))

	require.Equal(t, "v1", manifest["version"])
	require.Equal(t, "scaffold", manifest["status"])
	cors, ok := manifest["cors"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "oracle_pending", cors["status"])
	require.Equal(t, "deferred", cors["go_implementation"])

	httpRoutes, ok := manifest["http"].([]interface{})
	require.True(t, ok)
	require.NotEmpty(t, httpRoutes)
	websocket, ok := manifest["websocket"].(map[string]interface{})
	require.True(t, ok)
	require.NotEmpty(t, websocket["client_frames"])
	require.NotEmpty(t, websocket["server_frames"])

	fixtures, ok := manifest["fixtures"].(map[string]interface{})
	require.True(t, ok)
	for _, name := range []string{"http_outcomes", "frame_cases", "generated_fields"} {
		fixture, ok := fixtures[name].(string)
		require.True(t, ok)
		_, err := os.Stat(filepath.Join(contractDir, fixture))
		require.NoError(t, err)
	}
}

func TestContractFixturesKeepCORSOutcomesUnresolved(t *testing.T) {
	contractDir := filepath.Join("..", "router", "contract")
	outcomes := readContractJSON(t, filepath.Join(contractDir, "http-outcomes.json"))

	require.Equal(t, "scaffold", outcomes["status"])
	cases, ok := outcomes["cases"].([]interface{})
	require.True(t, ok)
	require.Len(t, cases, 2)
	for _, rawCase := range cases {
		fixtureCase, ok := rawCase.(map[string]interface{})
		require.True(t, ok)
		require.Equal(t, "oracle_pending", fixtureCase["status"])
	}
}

func readContractJSON(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	contents, err := os.ReadFile(path)
	require.NoError(t, err)

	var document map[string]interface{}
	require.NoError(t, json.Unmarshal(contents, &document))
	return document
}
