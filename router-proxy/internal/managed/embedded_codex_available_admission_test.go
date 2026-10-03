package managed

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

// A persisted Pi catalog row does not confer access to the router-owned Codex credential.
// Mutate the credential after manager construction to prove admission reopens it per launch.
func TestEmbeddedCodexAvailableCatalogRechecksPrivateCredential(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name, role, model string
		mutate            func(*testing.T, string, *store.LocalCodexCredentialStore)
		noCredentialStore bool
		omitted           bool
		admit             bool
	}{
		{name: "root-valid", role: "root", admit: true},
		{name: "worker-valid", role: "worker", admit: true},
		{name: "root-omitted-valid", role: "root", omitted: true, admit: true},
		{name: "worker-omitted-valid", role: "worker", omitted: true, admit: true},
		{name: "root-omitted-no-router-store", role: "root", omitted: true, noCredentialStore: true},
		{name: "worker-omitted-no-router-store", role: "worker", omitted: true, noCredentialStore: true},
		{name: "root-omitted-removed", role: "root", omitted: true, mutate: func(t *testing.T, path string, _ *store.LocalCodexCredentialStore) {
			require.NoError(t, os.Remove(path))
		}},
		{name: "worker-omitted-unsafe", role: "worker", omitted: true, mutate: func(t *testing.T, path string, _ *store.LocalCodexCredentialStore) {
			require.NoError(t, os.Chmod(path, 0o644))
		}},
		{name: "root-omitted-incomplete", role: "root", omitted: true, mutate: func(t *testing.T, _ string, s *store.LocalCodexCredentialStore) {
			require.NoError(t, s.Save(ctx, store.CodexCredential{AccessToken: "offline-access", RefreshToken: "offline-refresh"}))
		}},
		{name: "root-removed", role: "root", mutate: func(t *testing.T, path string, _ *store.LocalCodexCredentialStore) {
			require.NoError(t, os.Remove(path))
		}},
		{name: "worker-removed", role: "worker", mutate: func(t *testing.T, path string, _ *store.LocalCodexCredentialStore) {
			require.NoError(t, os.Remove(path))
		}},
		{name: "root-incomplete", role: "root", mutate: func(t *testing.T, _ string, s *store.LocalCodexCredentialStore) {
			require.NoError(t, s.Save(ctx, store.CodexCredential{AccessToken: "offline-access", RefreshToken: "offline-refresh"}))
		}},
		{name: "worker-unsafe", role: "worker", mutate: func(t *testing.T, path string, _ *store.LocalCodexCredentialStore) {
			require.NoError(t, os.Chmod(path, 0o644))
		}},
		{name: "root-no-router-store", role: "root", noCredentialStore: true},
		{name: "worker-unknown-sdk-model", role: "worker", model: "unregistered-codex-model"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Credential ancestry must begin under the verified root-owned sticky tmp.
			base, tempErr := os.MkdirTemp("/srv/disk-tmp", "codex-admission-")
			require.NoError(t, tempErr)
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(base)) })
			db, err := store.Open(ctx, filepath.Join(base, "router.db"), store.Options{})
			require.NoError(t, err)
			defer db.Close()
			modelID := "gpt-6-sol"
			if tc.omitted {
				modelID = "gpt-5.6-sol"
			}
			if tc.model != "" {
				modelID = tc.model
			}
			_, err = db.CommitManagedProviderSnapshot(ctx, []store.ManagedProviderStatus{
				{Provider: "openai-codex", Label: "ChatGPT", Status: "authenticated", Authenticated: true},
				{Provider: "google", Label: "Google", Status: "signed_out"},
				{Provider: "cursor", Label: "Cursor", Status: "signed_out"},
				{Provider: "google-vertex", Label: "Google Vertex AI", Status: "signed_out"},
				{Provider: "antigravity", Label: "Google Antigravity", Status: "signed_out"},
			}, []store.ManagedProviderModel{{Provider: "openai-codex", ID: modelID, Name: "catalog model", Available: true}})
			require.NoError(t, err)
			catalog := StoreCatalogSource{Store: db}
			before, err := catalog.Catalog(ctx)
			require.NoError(t, err)
			require.Len(t, before.Models, 1)
			project := filepath.Join(base, "project")
			require.NoError(t, os.Mkdir(project, 0700))
			path := filepath.Join(base, "private", "credential.json")
			credentials, err := store.OpenLocalCodexCredentialStore(path)
			require.NoError(t, err)
			require.NoError(t, credentials.Save(ctx, store.CodexCredential{AccessToken: "offline-access", RefreshToken: "offline-refresh", AccountID: "offline-account", ExpiresAt: time.Now().Add(-time.Hour)}))
			manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
			require.NoError(t, err)
			started := make(chan struct{}, 1)
			if tc.noCredentialStore {
				credentials = nil
			}
			m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{base}}, CatalogSource: catalog, CodexCredentialStore: credentials, EmbeddedRunnerFactory: func(context.Context, store.ManagedAgentRecord) error { started <- struct{}{}; return nil }})
			require.NoError(t, err)
			defer m.Close(ctx)
			if tc.mutate != nil {
				tc.mutate(t, path, credentials)
			}
			var requested *Model
			if !tc.omitted {
				requested = &Model{Provider: "openai-codex", ID: modelID}
			}
			req := LaunchRequest{Name: "catalog-codex-" + tc.role, Cwd: project, Runtime: "embedded", Model: requested, RequestID: "a0888888-8888-4888-8888-888888888891"}
			var launched LaunchResult
			if tc.role == "root" {
				launched, err = m.LaunchEmbeddedRoot(ctx, req)
			} else {
				launched, err = m.Launch(ctx, req)
			}
			if tc.admit {
				require.NoError(t, err)
				require.NotNil(t, launched.Agent)
				require.Equal(t, modelID, launched.Agent.SelectedModel.ID)
				row, rowErr := db.ManagedAgent(ctx, req.Name)
				require.NoError(t, rowErr)
				binding := store.EmbeddedBinding{AgentID: row.AgentID, SessionID: row.PiSessionID, Epoch: row.SessionEpoch, Generation: row.RuntimeGeneration}
				role, roleErr := db.EmbeddedSessionRole(ctx, binding)
				require.NoError(t, roleErr)
				require.Equal(t, tc.role, role)
				select {
				case <-started:
				case <-time.After(3 * time.Second):
					t.Fatal("embedded runner did not start")
				}
			} else {
				require.True(t, IsModelUnavailable(err), "expected admission refusal, got %v", err)
				_, rowErr := db.ManagedAgent(ctx, req.Name)
				require.ErrorIs(t, rowErr, sql.ErrNoRows)
				_, opErr := db.ManagedOperation(ctx, req.RequestID)
				require.ErrorIs(t, opErr, sql.ErrNoRows)
				require.Empty(t, started)
			}
			after, catErr := catalog.Catalog(ctx)
			require.NoError(t, catErr)
			require.Equal(t, before, after)
		})
	}
}
