package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func nativeHashFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func nativeHashTree(t *testing.T, root string) string {
	t.Helper()
	type entry struct {
		name string
		data []byte
	}
	var entries []entry
	require.NoError(t, filepath.WalkDir(root, func(path string, item fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if item.IsDir() {
			if item.Name() == "node_modules" {
				return fs.SkipDir
			}
			return nil
		}
		if item.Type()&os.ModeSymlink != 0 {
			return errors.New("tree contains a symlink")
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		entries = append(entries, entry{name: filepath.ToSlash(relative), data: data})
		return nil
	}))
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	hash := sha256.New()
	for _, item := range entries {
		_, _ = hash.Write([]byte(item.name))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(item.data)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// This is intentionally a router-level harness, rather than another extension
// test: it proves the enabled Go-owned child, fixed argv/env, HTTP session, and
// readiness boundary against the installed Pi binary.
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
	cursorPackage := filepath.Join(root, "auth-helper", "node_modules", "pi-cursor-sdk")
	cursor := filepath.Join(cursorPackage, "dist", "index.js")
	helper := filepath.Join(root, "auth-helper", "index.mjs")
	helperLock := filepath.Join(root, "auth-helper", "package-lock.json")
	ui := filepath.Join(root, "router-proxy", "managed-ui")
	for _, required := range []string{cursor, filepath.Join(cursorPackage, "package.json"), helper, helperLock, filepath.Join(ui, "index.html")} {
		if _, statErr := os.Stat(required); statErr != nil {
			t.Skipf("managed runtime dependency is unavailable: %s", required)
		}
	}
	mecha := filepath.Join(root, "pi-extensions", "mecha-rpc", "index.ts")
	cursorPackageJSON := filepath.Join(cursorPackage, "package.json")
	expected := managed.ExpectedIdentities{
		PiVersion:              "0.85.0",
		PiSHA256:               nativeHashFile(t, pi),
		ManifestSHA256:         nativeHashFile(t, filepath.Join(root, "router", "contract", "managed-tools-v1.json")),
		MechaRPCHash:           nativeHashFile(t, mecha),
		PiSubagentsEntryHash:   nativeHashFile(t, subagents),
		PiSubagentsTreeHash:    nativeHashTree(t, filepath.Dir(filepath.Dir(subagents))),
		CursorExtensionHash:    nativeHashFile(t, cursor),
		CursorPackageHash:      nativeHashFile(t, cursorPackageJSON),
		CursorResolvedIdentity: "pi-cursor-sdk@0.3.6",
		CursorLockfileHash:     nativeHashFile(t, filepath.Join(root, "auth-helper", "package-lock.json")),
		CursorTreeHash:         nativeHashTree(t, cursorPackage),
		HelperHash:             nativeHashFile(t, helper),
		HelperLockfileHash:     nativeHashFile(t, helperLock),
		HelperTreeHash:         nativeHashTree(t, filepath.Dir(helper)),
		UIDistributionTreeHash: nativeHashTree(t, ui),
	}
	manager, err := managed.NewManager(managed.ManagerOptions{
		Store: durable,
		Config: managed.Config{
			Enabled: true, PiExecutable: pi, ManagedHome: home, SessionRoot: sessionRoot,
			ManifestPath: filepath.Join(root, "router", "contract", "managed-tools-v1.json"), MechaRPCExtension: mecha,
			PiSubagentsExtension: subagents, CursorExtension: cursor, CursorPackage: cursorPackage,
			CursorLockfile: filepath.Join(root, "auth-helper", "package-lock.json"), HelperPath: helper, HelperLockfile: helperLock,
			UIDistribution: ui, AllowedWorkingDirectoryRoots: []string{root}, CanonicalQuietRecipientID: "supervisor", Expected: expected,
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
	bootstrapRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, httpURL+"/v1/ui-session", strings.NewReader(`{}`))
	require.NoError(t, err)
	bootstrapRequest.Header.Set("Content-Type", "application/json")
	bootstrapResponse, err := http.DefaultClient.Do(bootstrapRequest)
	require.NoError(t, err)
	defer bootstrapResponse.Body.Close()
	require.Equal(t, http.StatusCreated, bootstrapResponse.StatusCode)
	var bootstrap struct {
		CSRF string `json:"csrf_token"`
	}
	require.NoError(t, json.NewDecoder(bootstrapResponse.Body).Decode(&bootstrap))
	cookie := bootstrapResponse.Cookies()[0]
	launchBody, err := json.Marshal(map[string]string{"name": "native-alpha", "cwd": root})
	require.NoError(t, err)
	launchRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, httpURL+"/v1/managed-agents", bytes.NewReader(launchBody))
	require.NoError(t, err)
	launchRequest.Header.Set("Content-Type", "application/json")
	launchRequest.Header.Set("X-Mecha-CSRF", bootstrap.CSRF)
	launchRequest.Header.Set("X-Request-ID", "123e4567-e89b-42d3-a456-426614174081")
	launchRequest.AddCookie(cookie)
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
		projectionRequest.AddCookie(cookie)
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
	authenticatedRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, httpURL+"/v1/agents", nil)
	require.NoError(t, err)
	authenticatedRequest.AddCookie(cookie)
	authenticatedResponse, err := http.DefaultClient.Do(authenticatedRequest)
	require.NoError(t, err)
	defer authenticatedResponse.Body.Close()
	var additive struct {
		Agents []map[string]any `json:"agents"`
	}
	require.NoError(t, json.NewDecoder(authenticatedResponse.Body).Decode(&additive))
	require.Len(t, additive.Agents, 1)
	require.Equal(t, "router-managed", additive.Agents[0]["source"])
}
