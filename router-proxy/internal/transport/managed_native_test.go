package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

const nativeSeedPolicy = `{"compaction":{"deferred":false},"goal_lock":{"enabled":false},"inline":{"credits":{"bash":2},"enabled":true},"recall":{"content":"readiness seed"},"settled":{"enabled":false}}`
const nativeSeedPolicyHash = "7f88a0a98892509ffdd135e163a5ffe20df486b802d2f176f58af87387a640c8"

type nativeBlockingProcessFactory struct {
	base    managed.ProcessFactory
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (f *nativeBlockingProcessFactory) Start(ctx context.Context, spec managed.CommandSpec) (managed.Process, error) {
	f.once.Do(func() { close(f.entered) })
	select {
	case <-f.release:
		return f.base.Start(ctx, spec)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// This actual-router proof pauses after the launch reservation and before Pi
// starts. The test edits only its temporary database row, so production keeps
// no policy seed surface or route.
func seedNativeReadinessPolicy(ctx context.Context, path, agentID string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	rollback := func(err error) error { _, _ = db.ExecContext(context.Background(), "ROLLBACK"); return err }
	result, err := db.ExecContext(ctx, `UPDATE managed_readiness_sources
		SET desired_policy_canonical = ?, desired_policy_sha256 = ?
		WHERE agent_id = ? AND pi_session_id IS NULL AND applied_policy_canonical IS NULL AND snapshot_id IS NULL`, []byte(nativeSeedPolicy), nativeSeedPolicyHash, agentID)
	if err != nil {
		return rollback(err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		if err != nil {
			return rollback(err)
		}
		return rollback(sql.ErrNoRows)
	}
	_, err = db.ExecContext(ctx, "COMMIT")
	return err
}

func commandOutput(t *testing.T, directory string, command string, args ...string) string {
	t.Helper()
	result := exec.Command(command, args...)
	result.Dir = directory
	output, err := result.Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(output))
}

var readinessCandidatePaths = []string{
	"RPC_IMPLEMENTATION_PLAN.html",
	"router-proxy/internal/managed/readiness_canonical.go",
	"router-proxy/internal/managed/readiness_canonical_test.go",
	"router-proxy/internal/managed/rpc.go",
	"router-proxy/internal/managed/rpc_test.go",
	"router-proxy/internal/managed/supervisor.go",
	"router-proxy/internal/managed/supervisor_launch_test.go",
	"router-proxy/internal/managed/supervisor_test.go",
	"router-proxy/internal/store/managed.go",
	"router-proxy/internal/store/managed_readiness_sources_test.go",
	"router-proxy/internal/store/managed_test.go",
	"router-proxy/internal/store/migrations/024_managed_readiness_sources.sql",
	"router-proxy/internal/store/store_test.go",
	"router-proxy/internal/transport/managed_http_test.go",
	"router-proxy/internal/transport/managed_launch_http_test.go",
	"router-proxy/internal/transport/managed_native_test.go",
	"tests/mecha_rpc_manifest.test.ts",
}

// readinessCandidateContentSHA256 identifies the actual dirty candidate this
// test compiled. Each declared readiness path contributes its exact file hash,
// so the base commit alone cannot be mistaken for the tested worktree.
func readinessCandidateContentSHA256(t *testing.T, root string) string {
	t.Helper()
	hash := sha256.New()
	for _, relativePath := range readinessCandidatePaths {
		content, err := os.ReadFile(filepath.Join(root, relativePath))
		require.NoError(t, err)
		fileHash := sha256.Sum256(content)
		_, err = fmt.Fprintf(hash, "%s\x00%x\n", relativePath, fileHash)
		require.NoError(t, err)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func writeNativeReadinessArtifact(t *testing.T, root, pi, mecha, subagents string, manifest managed.Manifest, snapshotID, ownerID string) {
	t.Helper()
	var random [8]byte
	_, err := rand.Read(random[:])
	require.NoError(t, err)
	head := commandOutput(t, root, "git", "rev-parse", "HEAD")
	tree := commandOutput(t, root, "git", "rev-parse", "HEAD^{tree}")
	candidate := readinessCandidateContentSHA256(t, root)
	piRoot := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(subagents))))
	artifact := map[string]any{
		"protocol": "mecha.phase2.readiness-sources/v1", "mecha_base_head": head, "mecha_base_tree": tree,
		"mecha_candidate_identity_algorithm": "sha256-v1:path-NUL-file-sha256-LF",
		"mecha_candidate_content_sha256":     candidate, "mecha_candidate_paths": readinessCandidatePaths,
		"manifest_revision": manifest.Revision, "manifest_sha256": manifest.CanonicalSHA256,
		"pi_executable": pi, "pi_version": commandOutput(t, root, pi, "--version"),
		"mecha_rpc_source": mecha, "pi_subagents_entry": subagents,
		"pi_packages_remote":      commandOutput(t, piRoot, "git", "remote", "get-url", "origin"),
		"pi_packages_ref":         commandOutput(t, piRoot, "git", "branch", "--show-current"),
		"pi_packages_commit":      commandOutput(t, piRoot, "git", "rev-parse", "HEAD"),
		"synthetic_policy_sha256": nativeSeedPolicyHash, "synthetic_snapshot_id": snapshotID, "synthetic_owner_session_id": ownerID,
	}
	encoded, err := json.MarshalIndent(artifact, "", "  ")
	require.NoError(t, err)
	path := filepath.Join("/tmp", "mecha-rpc-acceptance", head, "phase2-readiness-sources", candidate, hex.EncodeToString(random[:]), "readiness-sources-v1.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, append(encoded, '\n'), 0o600))
	written, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(written), "MECHA_")
	require.Contains(t, string(written), nativeSeedPolicyHash)
	require.Contains(t, string(written), candidate)
}

func TestManagedRouterWithInstalledPiCommitsReadinessSources(t *testing.T) {
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
	databasePath := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, databasePath, store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	manifest, _, err := managed.LoadManifest(filepath.Join(root, "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	home := t.TempDir()
	sessionRoot := filepath.Join(home, "sessions")
	require.NoError(t, os.MkdirAll(sessionRoot, 0o700))
	mecha := filepath.Join(root, "pi-extensions", "mecha-rpc", "index.ts")
	factory := &nativeBlockingProcessFactory{base: managed.DefaultProcessFactory(), entered: make(chan struct{}), release: make(chan struct{})}
	defer func() {
		select {
		case <-factory.release:
		default:
			close(factory.release)
		}
	}()
	manager, err := managed.NewManager(managed.ManagerOptions{
		Store: durable,
		Config: managed.Config{Enabled: true, PiExecutable: pi, ManagedHome: home, SessionRoot: sessionRoot,
			ManifestPath: filepath.Join(root, "router", "contract", "managed-tools-v1.json"), MechaRPCExtension: mecha,
			PiSubagentsExtension: subagents, AllowedWorkingDirectoryRoots: []string{root}, CanonicalQuietRecipientID: "supervisor"},
		Manifest: manifest, ProcessFactory: factory,
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
	select {
	case <-factory.entered:
	case <-ctx.Done():
		t.Fatal("process factory did not reach seed boundary")
	}
	require.NoError(t, seedNativeReadinessPolicy(ctx, databasePath, "native-alpha"))
	close(factory.release)
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
			t.Fatalf("managed Pi did not reach source-confirmed ready: %s", body)
		}
		time.Sleep(100 * time.Millisecond)
	}
	source, err := durable.ManagedReadinessSource(ctx, "native-alpha")
	require.NoError(t, err)
	require.Equal(t, []byte(nativeSeedPolicy), source.DesiredPolicy.Canonical)
	require.Equal(t, nativeSeedPolicyHash, source.DesiredPolicy.SHA256)
	require.NotNil(t, source.AppliedPolicy)
	require.Equal(t, source.DesiredPolicy, *source.AppliedPolicy)
	require.NotNil(t, source.Snapshot)
	require.NotContains(t, string(source.Snapshot.Canonical), "context_ref")
	agent, err := durable.ManagedAgent(ctx, "native-alpha")
	require.NoError(t, err)
	require.True(t, agent.AcceptingPrompts)
	require.False(t, agent.InitialInstructionSent, "no model turn occurs before final readiness")
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
	writeNativeReadinessArtifact(t, root, pi, mecha, subagents, manifest, source.Snapshot.SnapshotID, source.Snapshot.OwnerSessionID)
}
