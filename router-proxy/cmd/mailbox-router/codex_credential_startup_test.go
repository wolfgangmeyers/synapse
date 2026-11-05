package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/stretchr/testify/require"
)

// Root bootstrap must fail during flag parsing, before any database, listener,
// provider, or credential store is constructed.
func TestEmbeddedRootBootstrapRequiresCodexCredentialPathBeforeDatabase(t *testing.T) {
	base := t.TempDir()
	database := filepath.Join(base, "router.db")
	args := []string{"--database", database, "--managed=true", "--embedded-root-bootstrap",
		"--embedded-root-request-id", "a0777777-7777-4777-8777-777777777777",
		"--embedded-root-name", "root", "--embedded-root-cwd", base,
		"--embedded-root-model-provider", "openai-codex", "--embedded-root-model-id", "gpt-5.6-terra",
		"--embedded-root-initial-instruction", "fixed instruction"}
	_, err := os.Stat(database)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = parseConfig(args)
	require.ErrorContains(t, err, "--codex-credential-path")
	_, err = os.Stat(database)
	require.ErrorIs(t, err, os.ErrNotExist, "rejection must not create a database")
	_, err = os.Stat(database + ".jsonl")
	require.ErrorIs(t, err, os.ErrNotExist, "rejection must not create a log")
}

// Use a private test directory without depending on a machine-specific path.
func newCodexCredentialParent(t *testing.T) string {
	t.Helper()
	return testfixture.PrivateDir(t)
}

// Ordinary Pi auth and API-key environment values cannot replace the explicit
// Router-owned credential required by either embedded opt-in.
func TestEmbeddedCodexCredentialFallbackFailClosed(t *testing.T) {
	for _, mode := range []string{"root", "worker"} {
		t.Run(mode, func(t *testing.T) {
			private := newCodexCredentialParent(t)
			base := t.TempDir()
			t.Setenv("HOME", base)
			piAgent := filepath.Join(base, ".pi", "agent")
			require.NoError(t, os.MkdirAll(piAgent, 0o700))
			piAuth := filepath.Join(piAgent, "auth.json")
			require.NoError(t, os.WriteFile(piAuth, []byte(`{"openai-codex":{"type":"api_key","key":"fake-pi-only"}}`), 0o600))
			t.Setenv("PI_CODING_AGENT_DIR", piAgent)
			t.Setenv("MECHA_PI_AUTH_PATH", piAuth)
			t.Setenv("OPENAI_API_KEY", "fake-api-key-only")
			t.Setenv("CODEX_API_KEY", "fake-codex-key-only")

			args := []string{"--database", filepath.Join(base, "router.db"), "--managed-pi-agent-dir", piAgent}
			if mode == "root" {
				args = append(args, "--embedded-root-bootstrap", "--embedded-root-request-id", "a0999999-9999-4999-8999-999999999999",
					"--embedded-root-name", "root", "--embedded-root-cwd", base,
					"--embedded-root-model-provider", "openai-codex", "--embedded-root-model-id", "gpt-5.6-terra",
					"--embedded-root-initial-instruction", "offline fixture")
			} else {
				args = append(args, "--embedded-worker-default")
			}
			_, err := parseConfig(args)
			require.ErrorContains(t, err, "--codex-credential-path", "fallbacks must not satisfy the missing explicit flag")

			path := filepath.Join(private, "router-codex.json")
			options, err := parseConfig(append(args, "--codex-credential-path", path))
			require.NoError(t, err)
			require.Equal(t, mode == "worker", embeddedWorkerDefaultEnabled(options), "root bootstrap must leave the worker default off")
			for _, state := range []string{"missing-file", "invalid-file"} {
				t.Run(state, func(t *testing.T) {
					if state == "invalid-file" {
						require.NoError(t, os.WriteFile(path, []byte(`{"access_token":"","refresh_token":""}`), 0o600))
					}
					managerOptions := managed.ManagerOptions{}
					err := configureCodexCredentials(context.Background(), options, &managerOptions)
					if state == "missing-file" {
						require.NoError(t, err, "an empty protected store permits browser login, not ambient credential fallback")
						require.NotNil(t, managerOptions.CodexCredentialStore)
						_, loadErr := managerOptions.CodexCredentialStore.Load(context.Background())
						require.ErrorIs(t, loadErr, os.ErrNotExist)
						require.NotNil(t, managerOptions.CodexRefresh)
					} else {
						require.ErrorContains(t, err, "invalid credential token length", "Pi auth and API keys must not replace an invalid Router credential")
						require.Nil(t, managerOptions.CodexCredentialStore)
						require.Nil(t, managerOptions.CodexRefresh)
					}
				})
			}
		})
	}
}

// Startup validates identity without contacting the issuer. An expired record
// remains usable for a serialized refresh on the first provider request.
func TestCodexCredentialStartupIdentity(t *testing.T) {
	ctx := context.Background()
	private := newCodexCredentialParent(t)
	base := t.TempDir()
	path := filepath.Join(private, "codex.json")
	options, err := parseConfig([]string{"--database", filepath.Join(base, "router.db"), "--codex-credential-path", path})
	require.NoError(t, err)
	fixture, err := store.OpenLocalCodexCredentialStore(path)
	require.NoError(t, err)
	credential := store.CodexCredential{AccessToken: "fake-access", RefreshToken: "fake-refresh", ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, fixture.Save(ctx, credential), "store deliberately allows records without an account ID")
	managerOptions := managed.ManagerOptions{}
	require.Error(t, configureCodexCredentials(ctx, options, &managerOptions), "missing account ID must fail before manager or listener construction")
	require.Nil(t, managerOptions.CodexCredentialStore)
	require.Nil(t, managerOptions.CodexRefresh, "startup must not install or call the refresh issuer")

	credential.AccountID = "fake-account"
	credential.ExpiresAt = time.Time{}
	require.NoError(t, fixture.Save(ctx, credential))
	require.Error(t, configureCodexCredentials(ctx, options, &managerOptions), "missing expiry cannot establish a refreshable credential")
	require.Nil(t, managerOptions.CodexCredentialStore)
	require.Nil(t, managerOptions.CodexRefresh)

	credential.ExpiresAt = time.Now().Add(-time.Hour)
	require.NoError(t, fixture.Save(ctx, credential))
	require.NoError(t, configureCodexCredentials(ctx, options, &managerOptions), "expired credential may refresh on the first provider request")
	require.NotNil(t, managerOptions.CodexCredentialStore)
	require.NotNil(t, managerOptions.CodexRefresh)
}

func TestCodexCredentialStartupOptIn(t *testing.T) {
	ctx := context.Background()
	private := newCodexCredentialParent(t)
	base := t.TempDir()
	args := []string{"--database", filepath.Join(base, "router.db")}
	defaultConfig, err := parseConfig(args)
	require.NoError(t, err)
	require.Empty(t, defaultConfig.codexCredentialPath)
	require.True(t, defaultConfig.managed.Enabled)
	unchanged := managed.ManagerOptions{}
	require.NoError(t, configureCodexCredentials(ctx, defaultConfig, &unchanged))
	require.Nil(t, unchanged.CodexCredentialStore)
	require.Nil(t, unchanged.CodexRefresh)

	legacy, err := parseConfig(append(args, "--managed=false"))
	require.NoError(t, err)
	require.NoError(t, configureCodexCredentials(ctx, legacy, &managed.ManagerOptions{}))

	path := filepath.Join(private, "codex.json")
	opted, err := parseConfig(append(args, "--codex-credential-path", path))
	require.NoError(t, err)
	require.Equal(t, path, opted.codexCredentialPath)
	managerOptions := managed.ManagerOptions{}
	err = configureCodexCredentials(ctx, opted, &managerOptions)
	require.NoError(t, err, "protected absence permits browser login without ambient credential fallback")
	require.NotNil(t, managerOptions.CodexCredentialStore)
	require.NotNil(t, managerOptions.CodexRefresh)
	_, err = managerOptions.CodexCredentialStore.Load(ctx)
	require.ErrorIs(t, err, os.ErrNotExist, "startup must leave the protected store signed out")

	credential := store.CodexCredential{AccessToken: "fake-access", RefreshToken: "fake-refresh", ExpiresAt: time.Now().Add(time.Hour), AccountID: "fake-account"}
	fixture, err := store.OpenLocalCodexCredentialStore(path)
	require.NoError(t, err)
	require.NoError(t, fixture.Save(ctx, credential))
	require.NoError(t, configureCodexCredentials(ctx, opted, &managerOptions))
	require.NotNil(t, managerOptions.CodexCredentialStore)
	require.NotNil(t, managerOptions.CodexRefresh, "wiring must not call refresh during startup")
	loaded, err := managerOptions.CodexCredentialStore.Load(ctx)
	require.NoError(t, err)
	require.Equal(t, credential.AccessToken, loaded.AccessToken)
	require.Equal(t, credential.RefreshToken, loaded.RefreshToken)
	require.Equal(t, credential.AccountID, loaded.AccountID)
	require.True(t, credential.ExpiresAt.Equal(loaded.ExpiresAt))

	for name, unsafe := range map[string]func() string{
		"public-file": func() string { require.NoError(t, os.Chmod(path, 0o644)); return path },
		"symlink": func() string {
			link := filepath.Join(private, "linked.json")
			require.NoError(t, os.Symlink(path, link))
			return link
		},
		"public-parent": func() string {
			dir := filepath.Join(private, "public")
			require.NoError(t, os.Mkdir(dir, 0o755))
			require.NoError(t, os.Chmod(dir, 0o755))
			info, err := os.Stat(dir)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o755), info.Mode().Perm())
			return filepath.Join(dir, "cred.json")
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := opted
			bad.codexCredentialPath = unsafe()
			require.Error(t, configureCodexCredentials(ctx, bad, &managed.ManagerOptions{}))
		})
	}
	bad := opted
	bad.managed.Enabled = false
	require.Error(t, configureCodexCredentials(ctx, bad, &managed.ManagerOptions{}))

	require.NoError(t, os.Chmod(path, 0o600))
	require.NoError(t, os.WriteFile(path, []byte(`{"access_token":"","refresh_token":""}`), 0o600))
	require.Error(t, configureCodexCredentials(ctx, opted, &managed.ManagerOptions{}), "invalid credential must fail closed")
}
