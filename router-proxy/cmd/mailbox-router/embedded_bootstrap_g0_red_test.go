package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/stretchr/testify/require"
)

// Only command flags can authorize the root. An HTTP body, agent name, or
// default managed mode must never silently enable this bootstrap.
func TestEmbeddedCommandBootstrapIsOffAndRejectsIncompleteAuthorityRED(t *testing.T) {
	base := []string{"--database", filepath.Join(t.TempDir(), "router.db")}
	defaultOptions, err := parseConfig(base)
	require.NoError(t, err)
	require.False(t, defaultOptions.embeddedRoot.Enabled)

	root := t.TempDir()
	base = append(base, "--codex-credential-path", filepath.Join(root, "synthetic-codex.json"))
	id := "a0111111-1111-4111-8111-111111111111"
	flags := []string{"--embedded-root-bootstrap=true", "--embedded-root-request-id", id,
		"--embedded-root-name", "command-root", "--embedded-root-cwd", root,
		"--embedded-root-model-provider", "openai-codex", "--embedded-root-model-id", "gpt-5.6-terra",
		"--embedded-root-initial-instruction", "fixed command instruction"}
	for i := 0; i < len(flags); i += 2 {
		partial := append(append([]string{}, base...), flags...)
		partial = append(partial[:i], partial[i+2:]...)
		_, err := parseConfig(partial)
		require.Error(t, err, "omitting command authority field %s must fail", flags[i])
	}
	_, err = parseConfig(append(append([]string{}, base...), flags[:len(flags)-2]...))
	require.Error(t, err, "instruction is required, not inferred from HTTP")
	_, err = parseConfig(append(append(append([]string{}, base...), flags...), "--embedded-root-cwd", "relative"))
	require.Error(t, err, "bootstrap cwd must be absolute")
	_, err = parseConfig(append(append(append([]string{}, base...), flags...), "--embedded-root-request-id", "not-an-id"))
	require.Error(t, err, "bootstrap ID must be stable and valid")
	_, err = parseConfig(append(append(append([]string{}, base...), flags...), "--managed=false"))
	require.Error(t, err, "a disabled manager cannot bootstrap a root")

	opts, err := parseConfig(append(append([]string{}, base...), flags...))
	require.NoError(t, err)
	require.True(t, opts.embeddedRoot.Enabled)
	require.Equal(t, id, opts.embeddedRoot.RequestID)
	require.Equal(t, "command-root", opts.embeddedRoot.Name)
	require.Equal(t, root, opts.embeddedRoot.Cwd)
	require.Equal(t, managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, opts.embeddedRoot.Model)
	require.Equal(t, "fixed command instruction", opts.embeddedRoot.InitialInstruction)
}

// Exercise the command-owned reservation, not Manager.LaunchEmbeddedRoot from
// the test: replay must use the exact same configured request on cold startup.
func TestEmbeddedCommandBootstrapReservesBeforeReadinessAndReplaysRED(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(t.TempDir(), "router.db")
	credentials := testfixture.CodexAdmission(t)
	id := "a0222222-2222-4222-8222-222222222222"
	args := []string{"--database", path, "--codex-credential-path", filepath.Join(root, "synthetic-codex.json"), "--embedded-root-bootstrap=true", "--embedded-root-request-id", id,
		"--embedded-root-name", "command-root", "--embedded-root-cwd", root,
		"--embedded-root-model-provider", "openai-codex", "--embedded-root-model-id", "gpt-5.6-terra",
		"--embedded-root-initial-instruction", "fixed command instruction"}
	opts, err := parseConfig(args)
	require.NoError(t, err)
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	var firstSession, firstGeneration string
	var firstToken int64
	for restart := 0; restart < 2; restart++ {
		db, openErr := store.Open(ctx, path, store.Options{})
		require.NoError(t, openErr)
		manager, managerErr := managed.NewManager(managed.ManagerOptions{Store: db, Manifest: manifest,
			Config:                managed.Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
			CatalogSource:         managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
			EmbeddedRunnerFactory: func(context.Context, store.ManagedAgentRecord) error { return nil },
			CodexCredentialStore:  credentials,
		})
		require.NoError(t, managerErr)
		// Cold recovery is unconditional; the opt-in command reservation is
		// separate and must finish before the router publishes readiness.
		if restart != 0 {
			require.NoError(t, manager.RecoverEmbeddedRoots(ctx))
		}
		require.NoError(t, launchConfiguredEmbeddedRoot(ctx, opts, manager))
		row, rowErr := db.ManagedAgent(ctx, "command-root")
		require.NoError(t, rowErr)
		require.Equal(t, "embedded", row.Runtime)
		require.Equal(t, id, row.LaunchOperationID)
		require.Equal(t, "fixed command instruction", row.PendingInitialInstruction)
		require.Equal(t, "gpt-5.6-terra", row.SelectedModel.ID)
		check, sqlErr := sql.Open("sqlite", path)
		require.NoError(t, sqlErr)
		var role string
		var token int64
		require.Eventually(t, func() bool {
			return check.QueryRowContext(ctx, `SELECT role,owner_token FROM embedded_sessions WHERE agent_id=? AND current=1`, row.AgentID).Scan(&role, &token) == nil && token > firstToken
		}, 3*time.Second, 10*time.Millisecond)
		require.Equal(t, "root", role)
		if restart == 1 {
			require.Equal(t, firstSession, row.PiSessionID)
			require.Equal(t, firstGeneration, row.RuntimeGeneration)
			require.Greater(t, token, firstToken)
		} else {
			firstSession, firstGeneration, firstToken = row.PiSessionID, row.RuntimeGeneration, token
		}
		require.NoError(t, check.Close())
		require.NoError(t, manager.Close(ctx))
		require.NoError(t, db.Close())
		if restart == 0 {
			// Force only the owner lease to expire; never change saved role or identity.
			check, sqlErr = sql.Open("sqlite", path)
			require.NoError(t, sqlErr)
			_, sqlErr = check.ExecContext(ctx, `UPDATE embedded_sessions SET owner_expires_at='2000-01-01T00:00:00.000Z' WHERE agent_id='command-root'`)
			require.NoError(t, sqlErr)
			require.NoError(t, check.Close())
		}
	}
	conflict, err := parseConfig(append(append([]string{}, args...), "--embedded-root-initial-instruction", "changed body"))
	require.NoError(t, err)
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manager, err := managed.NewManager(managed.ManagerOptions{Store: db, Manifest: manifest,
		Config:                managed.Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:         managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedRunnerFactory: func(context.Context, store.ManagedAgentRecord) error { return nil },
		CodexCredentialStore:  credentials,
	})
	require.NoError(t, err)
	defer manager.Close(ctx)
	require.Error(t, launchConfiguredEmbeddedRoot(ctx, conflict, manager), "same ID with changed body must conflict")
}
