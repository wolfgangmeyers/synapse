package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// Command-owned embedded Codex admission must not depend on ordinary Pi auth.
// Keep the catalog production-shaped: never inject CatalogFromModels here.
func TestEmbeddedCodexCredentialAdmitsWithoutPiCatalogRED(t *testing.T) {
	ctx := context.Background()
	base := testfixture.PrivateDir(t)
	piAgent := filepath.Join(base, "ordinary-pi-agent")
	credentialDir := filepath.Join(base, "credential")
	work := filepath.Join(base, "work")
	for _, dir := range []string{piAgent, credentialDir, work} {
		require.NoError(t, os.Mkdir(dir, 0o700))
	}
	require.NoError(t, os.WriteFile(filepath.Join(piAgent, "auth.json"), []byte("{}\n"), 0o600))
	credentialPath := filepath.Join(credentialDir, "codex.json")
	credentials, err := store.OpenLocalCodexCredentialStore(credentialPath)
	require.NoError(t, err)
	require.NoError(t, credentials.Save(ctx, store.CodexCredential{
		AccessToken: "fake-access-offline", RefreshToken: "fake-refresh-offline",
		AccountID: "fake-account-offline", ExpiresAt: time.Now().Add(time.Hour),
	}))
	info, err := os.Stat(credentialPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	model := ai.GetModel("openai-codex", "gpt-6-sol")
	require.NotNil(t, model, "SDK registry must contain the requested model")
	require.Equal(t, "openai-codex-responses", model.Api)

	requestID := "a0888888-8888-4888-8888-888888888888"
	options, err := parseConfig([]string{
		"--database", filepath.Join(base, "router.db"),
		"--codex-credential-path", credentialPath,
		"--managed-pi-agent-dir", piAgent, "--managed-cwd-roots", work,
		"--embedded-root-bootstrap=true", "--embedded-root-request-id", requestID,
		"--embedded-root-name", "offline-codex-root", "--embedded-root-cwd", work,
		"--embedded-root-model-provider", "openai-codex", "--embedded-root-model-id", "gpt-6-sol",
		"--embedded-root-initial-instruction", "offline admission only",
	})
	require.NoError(t, err)
	require.Equal(t, piAgent, options.managed.PiAgentDir)
	managerOptions := managed.ManagerOptions{}
	require.NoError(t, configureCodexCredentials(ctx, options, &managerOptions))
	require.NotNil(t, managerOptions.CodexCredentialStore)

	db, err := store.Open(ctx, options.database, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	// Empty ordinary Pi auth yields no available Pi catalog model. This is
	// independent of the complete local Codex credential above; Claude CLI aliases
	// are offered separately without implying provider authentication.
	_, err = db.CommitManagedProviderSnapshot(ctx,
		[]store.ManagedProviderStatus{
			{Provider: "openai-codex", Label: "ChatGPT", Status: "signed_out"},
			{Provider: "google", Label: "Google", Status: "signed_out"},
			{Provider: "cursor", Label: "Cursor", Status: "signed_out"},
			{Provider: "google-vertex", Label: "Google Vertex AI", Status: "signed_out"},
		}, nil)
	require.NoError(t, err)
	catalog := managed.StoreCatalogSource{Store: db}
	snapshot, err := catalog.Catalog(ctx)
	require.NoError(t, err)
	var aliases []string
	for _, model := range snapshot.Models {
		require.Equal(t, "claude-code", model.Provider, "ordinary Pi catalog must remain empty")
		aliases = append(aliases, model.ID)
	}
	require.ElementsMatch(t, []string{"fable", "opus", "sonnet", "haiku"}, aliases)
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	started := false
	managerOptions.Store = db
	managerOptions.Manifest = manifest
	managerOptions.Config = managed.Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{work}}
	managerOptions.CatalogSource = catalog
	managerOptions.EmbeddedRunnerFactory = func(context.Context, store.ManagedAgentRecord) error { started = true; return nil }
	manager, err := managed.NewManager(managerOptions)
	require.NoError(t, err)
	defer manager.Close(ctx)

	err = launchConfiguredEmbeddedRoot(ctx, options, manager)
	_, rowErr := db.ManagedAgent(ctx, "offline-codex-root")
	if managed.IsModelUnavailable(err) {
		require.ErrorIs(t, rowErr, sql.ErrNoRows, "catalog denial must not reserve an agent")
		require.False(t, started, "catalog denial must precede provider/start")
		t.Fatalf("BEHAVIORAL RED: validated command and complete private Codex credential reached admission; got model_unavailable for SDK Codex Responses gpt-6-sol, no reservation")
	}
	require.NoError(t, err, "unexpected parser, DB, fixture, or path failure is not catalog RED")
	require.NoError(t, rowErr, "successful admission must reserve a durable root")
	row, err := db.ManagedAgent(ctx, "offline-codex-root")
	require.NoError(t, err)
	require.Equal(t, requestID, row.LaunchOperationID)
}
