package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedCodexPrivateCredentialAdmissionFence(t *testing.T) {
	ctx := context.Background()
	good := store.CodexCredential{AccessToken: "offline-secret-access", RefreshToken: "offline-secret-refresh", AccountID: "offline-account", ExpiresAt: time.Now().Add(-time.Hour)}
	cases := []struct {
		name, runtime, provider, model string
		credential                     bool
		mutate                         func(*testing.T, string, *store.LocalCodexCredentialStore)
		admit, root                    bool
	}{
		{name: "root-expired-refreshable", runtime: "embedded", provider: "openai-codex", model: "gpt-6-sol", credential: true, admit: true, root: true},
		{name: "fresh-sol61-worker", runtime: "embedded", provider: "openai-codex", model: "gpt-6.1-sol", credential: true, admit: true},
		{name: "parentless-worker-opt-in", runtime: "embedded", provider: "openai-codex", model: "gpt-6-sol", credential: true, admit: true},
		{name: "no-opt-in", runtime: "embedded", provider: "openai-codex", model: "gpt-6-sol", root: true},
		{name: "ordinary-rpc", runtime: "rpc", provider: "openai-codex", model: "gpt-6-sol", credential: true},
		{name: "missing-at-selection", runtime: "embedded", provider: "openai-codex", model: "gpt-6-sol", credential: true, mutate: func(t *testing.T, path string, _ *store.LocalCodexCredentialStore) {
			require.NoError(t, os.Remove(path))
		}},
		{name: "incomplete-at-selection", runtime: "embedded", provider: "openai-codex", model: "gpt-6-sol", credential: true, mutate: func(t *testing.T, _ string, s *store.LocalCodexCredentialStore) {
			require.NoError(t, s.Save(ctx, store.CodexCredential{AccessToken: "offline-secret-access", RefreshToken: "offline-secret-refresh"}))
		}},
		{name: "unsafe-at-selection", runtime: "embedded", provider: "openai-codex", model: "gpt-6-sol", credential: true, mutate: func(t *testing.T, path string, _ *store.LocalCodexCredentialStore) {
			require.NoError(t, os.Chmod(path, 0o644))
		}},
		{name: "unknown-sdk-model", runtime: "embedded", provider: "openai-codex", model: "does-not-exist", credential: true},
		{name: "wrong-provider", runtime: "embedded", provider: "openai", model: "gpt-6-sol", credential: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			project := filepath.Join(base, "project")
			require.NoError(t, os.Mkdir(project, 0700))
			db, err := store.Open(ctx, filepath.Join(base, "router.db"), store.Options{})
			require.NoError(t, err)
			defer db.Close()
			_, err = db.CommitManagedProviderSnapshot(ctx, []store.ManagedProviderStatus{
				{Provider: "openai-codex", Label: "ChatGPT", Status: "signed_out"},
				{Provider: "google", Label: "Google", Status: "signed_out"},
				{Provider: "cursor", Label: "Cursor", Status: "signed_out"},
				{Provider: "google-vertex", Label: "Google Vertex AI", Status: "signed_out"},
				{Provider: "antigravity", Label: "Google Antigravity", Status: "signed_out"},
			}, nil)
			require.NoError(t, err)
			catalog := StoreCatalogSource{Store: db}
			before, err := catalog.Catalog(ctx)
			require.NoError(t, err)
			require.Empty(t, before.Models)
			manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
			require.NoError(t, err)
			var credentials *store.LocalCodexCredentialStore
			if tc.credential {
				path := filepath.Join(base, "private", "credential.json")
				credentials, err = store.OpenLocalCodexCredentialStore(path)
				require.NoError(t, err)
				require.NoError(t, credentials.Save(ctx, good))
				if tc.mutate != nil {
					tc.mutate(t, path, credentials)
				}
			}
			started := make(chan struct{}, 1)
			m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{base}}, CatalogSource: catalog, CodexCredentialStore: credentials, EmbeddedRunnerFactory: func(context.Context, store.ManagedAgentRecord) error { started <- struct{}{}; return nil }})
			require.NoError(t, err)
			defer m.Close(ctx)
			req := LaunchRequest{Name: "test-agent", Cwd: project, Runtime: tc.runtime, Model: &Model{Provider: tc.provider, ID: tc.model}, RequestID: "a0888888-8888-4888-8888-888888888889"}
			var launched LaunchResult
			if tc.root {
				launched, err = m.LaunchEmbeddedRoot(ctx, req)
			} else {
				launched, err = m.Launch(ctx, req)
			}
			if !tc.admit {
				require.True(t, IsModelUnavailable(err), "unexpected admission result: %v", err)
				_, rowErr := db.ManagedAgent(ctx, req.Name)
				require.ErrorIs(t, rowErr, sql.ErrNoRows)
				require.Empty(t, started)
			} else {
				require.NoError(t, err)
				row, rowErr := db.ManagedAgent(ctx, req.Name)
				require.NoError(t, rowErr)
				require.Equal(t, req.RequestID, row.LaunchOperationID)
				require.Equal(t, tc.provider, launched.Agent.SelectedModel.Provider)
				require.Equal(t, tc.model, launched.Agent.SelectedModel.ID)
				binding := store.EmbeddedBinding{AgentID: row.AgentID, SessionID: row.PiSessionID, Epoch: row.SessionEpoch, Generation: row.RuntimeGeneration}
				role, roleErr := db.EmbeddedSessionRole(ctx, binding)
				require.NoError(t, roleErr)
				if tc.root {
					require.Equal(t, "root", role)
				} else {
					require.Equal(t, "worker", role)
				}
				select {
				case <-started:
				case <-time.After(3 * time.Second):
					t.Fatal("runner not started")
				}
				encoded, encodeErr := json.Marshal(row)
				require.NoError(t, encodeErr)
				for _, secret := range []string{good.AccessToken, good.RefreshToken, good.AccountID} {
					require.NotContains(t, string(encoded), secret)
				}
			}
			after, catErr := catalog.Catalog(ctx)
			require.NoError(t, catErr)
			require.Equal(t, before.Models, after.Models)
			serialized, encodeErr := json.Marshal(after)
			require.NoError(t, encodeErr)
			for _, secret := range []string{good.AccessToken, good.RefreshToken, good.AccountID} {
				require.False(t, strings.Contains(string(serialized), secret))
				if err != nil {
					require.NotContains(t, err.Error(), secret)
				}
			}
		})
	}
	require.Equal(t, "openai-codex-responses", ai.GetModel("openai-codex", "gpt-6-sol").Api)
}
