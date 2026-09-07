package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

// This router-level harness proves the enabled Go-owned child and direct
// worker HTTP boundary against the installed Pi binary without helper, Cursor,
// built-UI, version, or immutable-input dependencies.
func TestManagedRouterWithInstalledPiReachesReadyWithoutMailbox(t *testing.T) {
	pi, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("installed Pi is required for the native managed-router harness")
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	subagents := filepath.Join(os.Getenv("HOME"), "code", "pi-packages", "packages", "pi-subagents", "src", "index.ts")
	if _, err := os.Stat(subagents); err != nil {
		t.Skip("checked pi-subagents workspace is required for the native managed-router harness")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	manifest, _, err := managed.LoadManifest(filepath.Join(root, "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	home := t.TempDir()
	sessionRoot := filepath.Join(home, "sessions")
	require.NoError(t, os.MkdirAll(sessionRoot, 0o700))
	mecha := filepath.Join(root, "pi-extensions", "mecha-rpc", "index.ts")
	manager, err := managed.NewManager(managed.ManagerOptions{
		Store: durable,
		Config: managed.Config{
			Enabled: true, PiExecutable: pi, ManagedHome: home, SessionRoot: sessionRoot,
			ManifestPath: filepath.Join(root, "router", "contract", "managed-tools-v1.json"), MechaRPCExtension: mecha,
			PiSubagentsExtension: subagents, AllowedWorkingDirectoryRoots: []string{root}, CanonicalQuietRecipientID: "supervisor",
		},
		Manifest:      manifest,
		CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
	})
	require.NoError(t, err)
	router, err := New(Options{Store: durable, Host: "127.0.0.1", Port: 0, Managed: manager})
	require.NoError(t, err)
	require.NoError(t, router.Listen(ctx))
	defer func() { _ = router.Close(context.Background()) }()
	httpURL, _ := router.URLs()
	launchBody, err := json.Marshal(map[string]string{"name": "native-alpha", "cwd": root})
	require.NoError(t, err)
	launchRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, httpURL+"/v1/managed-agents", bytes.NewReader(launchBody))
	require.NoError(t, err)
	launchRequest.Header.Set("Content-Type", "application/json")
	launchRequest.Header.Set("X-Request-ID", "123e4567-e89b-42d3-a456-426614174081")
	launchResponse, err := http.DefaultClient.Do(launchRequest)
	require.NoError(t, err)
	defer launchResponse.Body.Close()
	require.Equal(t, http.StatusAccepted, launchResponse.StatusCode)
	deadline := time.Now().Add(20 * time.Second)
	for {
		projectionRequest, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, httpURL+"/v1/managed-agents/native-alpha", nil)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		projectionResponse, requestErr := http.DefaultClient.Do(projectionRequest)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		var projection struct {
			Agent struct {
				Lifecycle string `json:"lifecycle_state"`
			} `json:"agent"`
		}
		body, _ := io.ReadAll(projectionResponse.Body)
		projectionResponse.Body.Close()
		if json.Unmarshal(body, &projection) == nil && projection.Agent.Lifecycle == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("managed Pi did not reach ready: %s", body)
		}
		time.Sleep(100 * time.Millisecond)
	}
	legacyResponse, err := http.Get(httpURL + "/v1/agents")
	require.NoError(t, err)
	defer legacyResponse.Body.Close()
	var legacy struct {
		Agents []map[string]any `json:"agents"`
	}
	require.NoError(t, json.NewDecoder(legacyResponse.Body).Decode(&legacy))
	require.Empty(t, legacy.Agents)

	removedRoute, err := http.Post(httpURL+"/v1/ui-session", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer removedRoute.Body.Close()
	require.Equal(t, http.StatusNotFound, removedRoute.StatusCode)
}
