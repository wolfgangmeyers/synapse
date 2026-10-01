package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// Exercise the command flag through a real bound listener. No production
// provider, browser, root bootstrap, or external runtime is involved.
func TestEmbeddedWorkerDefaultConfiguredListenHTTP(t *testing.T) {
	ctx := context.Background()
	parent := "/srv/disk-tmp"
	info, err := os.Lstat(parent)
	require.NoError(t, err)
	stat, ok := info.Sys().(*syscall.Stat_t)
	require.True(t, ok)
	require.True(t, info.IsDir() && info.Mode()&os.ModeSymlink == 0 && info.Mode()&os.ModeSticky != 0 && stat.Uid == 0)
	base, err := os.MkdirTemp(parent, "embedded-worker-listen-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(base)) })
	require.NoError(t, os.Chmod(base, 0o700))
	info, err = os.Lstat(base)
	require.NoError(t, err)
	stat, ok = info.Sys().(*syscall.Stat_t)
	require.True(t, ok)
	require.Equal(t, parent, filepath.Dir(base))
	require.True(t, info.IsDir() && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm() == 0o700 && stat.Uid == uint32(os.Geteuid()))
	project := filepath.Join(base, "project")
	require.NoError(t, os.Mkdir(project, 0700))
	credentialPath := filepath.Join(base, "private", "codex.json")
	credentialStore, err := store.OpenLocalCodexCredentialStore(credentialPath)
	require.NoError(t, err)
	require.NoError(t, credentialStore.Save(ctx, store.CodexCredential{AccessToken: "fake-access", RefreshToken: "fake-refresh", AccountID: "fake-account", ExpiresAt: time.Now().Add(time.Hour)}))
	args := []string{"--database", filepath.Join(base, "router.db"), "--codex-credential-path", credentialPath}
	off, err := parseConfig(args)
	require.NoError(t, err)
	require.False(t, embeddedWorkerDefaultEnabled(off))
	rootOnly, err := parseConfig(append(append([]string{}, args...), "--embedded-root-bootstrap", "--embedded-root-request-id", "a0666666-6666-4666-8666-666666666666", "--embedded-root-name", "root", "--embedded-root-cwd", base, "--embedded-root-model-provider", "openai-codex", "--embedded-root-model-id", "gpt-5.6-terra", "--embedded-root-initial-instruction", "root instruction"))
	require.NoError(t, err)
	require.False(t, embeddedWorkerDefaultEnabled(rootOnly), "bootstrap authority cannot change HTTP worker default")
	on, err := parseConfig(append(append([]string{}, args...), "--embedded-worker-default"))
	require.NoError(t, err)
	require.True(t, embeddedWorkerDefaultEnabled(on))
	require.False(t, on.embeddedRoot.Enabled)
	options := managed.ManagerOptions{}
	require.NoError(t, configureCodexCredentials(ctx, on, &options))
	require.NotNil(t, options.CodexCredentialStore)
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	t.Cleanup(reg.Unregister)
	reg.SetResponses([]providers.FauxResponseStep{
		func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "answered"}}, ai.StopStop)
		},
		func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "answered"}}, ai.StopStop)
		},
	})
	db, err := store.Open(ctx, on.database, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	options.Store = db
	options.Manifest = manifest
	options.Config = managed.Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{base}}
	options.CatalogSource = managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true})
	options.EmbeddedModelResolver = func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() }
	manager, err := managed.NewManager(options)
	require.NoError(t, err)
	defer manager.Close(ctx)
	client := &http.Client{Timeout: 3 * time.Second}
	url := ""
	launch := func(name, id, payload string) {
		t.Helper()
		request, e := http.NewRequest(http.MethodPost, url+"/v1/managed-agents", strings.NewReader(payload))
		require.NoError(t, e)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Request-ID", id)
		response, e := client.Do(request)
		require.NoError(t, e)
		defer response.Body.Close()
		body, e := io.ReadAll(response.Body)
		require.NoError(t, e)
		require.Equal(t, http.StatusAccepted, response.StatusCode, string(body))
		row, e := db.ManagedAgent(ctx, name)
		require.NoError(t, e)
		if name == "explicit-rpc" || name == "default-rpc" {
			require.Equal(t, "rpc", row.Runtime)
		} else {
			require.Equal(t, "embedded", row.Runtime)
		}
	}
	// The default-off listener must keep the unchanged HTTP payload on RPC.
	offRouter, err := transport.New(transport.Options{Store: db, Host: "127.0.0.1", Port: 0, Managed: manager, DefaultEmbeddedRoot: embeddedWorkerDefaultEnabled(off)})
	require.NoError(t, err)
	defer offRouter.Close(ctx)
	require.NoError(t, offRouter.Listen(ctx))
	url, _ = offRouter.URLs()
	launch("default-rpc", "b3888888-1111-4111-8111-111111111111", fmt.Sprintf(`{"name":"default-rpc","cwd":%q}`, base))
	// Capture the durable fence before Close can cancel startup or join Wait.
	// The manager's private binding/process fields are not exposed here; these
	// rows must not be mistaken for an in-memory binding snapshot.
	stopCheck, err := sql.Open("sqlite", on.database)
	require.NoError(t, err)
	defer stopCheck.Close()
	embeddedWorkerStopSnapshot(t, stopCheck, "before offRouter.Close", nil)
	closeErr := offRouter.Close(ctx)
	if closeErr != nil {
		embeddedWorkerStopSnapshot(t, stopCheck, "offRouter.Close returned before assertion/cleanup", closeErr)
	}
	require.NoError(t, closeErr)
	// Each listener has its own process generation, so the second needs a fresh Manager.
	manager, err = managed.NewManager(options)
	require.NoError(t, err)
	defer manager.Close(ctx)
	// This is the same transport option expression used by the command's main.
	router, err := transport.New(transport.Options{Store: db, Host: "127.0.0.1", Port: 0, Managed: manager, DefaultEmbeddedRoot: embeddedWorkerDefaultEnabled(on), StartupManaged: func(c context.Context, m *managed.Manager) error { return launchConfiguredEmbeddedRoot(c, on, m) }})
	require.NoError(t, err)
	defer router.Close(ctx)
	require.NoError(t, router.Listen(ctx))
	url, _ = router.URLs()
	for i, name := range []string{"worker-one", "worker-two"} {
		launch(name, fmt.Sprintf("b3%06d-1111-4111-8111-111111111111", i+1), fmt.Sprintf(`{"name":%q,"cwd":%q,"model":{"provider":"openai-codex","id":"gpt-5.6-terra"},"initial_instruction":%q}`, name, project, "instruction for "+name))
	}
	launch("explicit-rpc", "b3999999-1111-4111-8111-111111111111", fmt.Sprintf(`{"name":"explicit-rpc","cwd":%q,"runtime":"rpc"}`, base))
	check, err := sql.Open("sqlite", on.database)
	require.NoError(t, err)
	defer check.Close()
	sessions := make([]string, 0, 2)
	for _, name := range []string{"worker-one", "worker-two"} {
		require.Eventually(t, func() bool { r, e := db.ManagedAgent(ctx, name); return e == nil && r.LifecycleState == "running" }, 3*time.Second, 10*time.Millisecond)
		row, e := db.ManagedAgent(ctx, name)
		require.NoError(t, e)
		var role, session string
		var token int64
		// No parent agent/window is supplied: the role comes from HTTP worker admission.
		require.NoError(t, check.QueryRowContext(ctx, `SELECT role,session_id,owner_token FROM embedded_sessions WHERE agent_id=? AND current=1`, name).Scan(&role, &session, &token))
		require.Equal(t, "worker", role)
		require.Positive(t, token)
		require.Equal(t, row.PiSessionID, session)
		snapshot, e := db.ReopenEmbedded(ctx, store.EmbeddedBinding{AgentID: row.AgentID, SessionID: session, Epoch: row.SessionEpoch, Generation: row.RuntimeGeneration, OwnerToken: token})
		require.NoError(t, e)
		require.NotEmpty(t, snapshot.Entries)
		sessions = append(sessions, session)
	}
	require.NotEqual(t, sessions[0], sessions[1])
}

// A read transaction keeps the diagnostic rows mutually consistent. It uses
// only this test's disposable database and releases the read lock before Close.
func embeddedWorkerStopSnapshot(t *testing.T, check *sql.DB, phase string, cause error) {
	t.Helper()
	t.Logf("stop snapshot phase=%q at=%s cause=%v in_memory_binding/process/Wait=unavailable (private manager fields)", phase, time.Now().UTC().Format(time.RFC3339Nano), cause)
	tx, err := check.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Logf("stop snapshot begin: %v", err)
		return
	}
	defer tx.Rollback()
	for _, table := range []string{"managed_agents", "managed_http_operations", "managed_lifecycle_actions", "managed_readiness_sources", "managed_orderly_stop_markers", "embedded_sessions", "embedded_process_generations"} {
		rows, queryErr := tx.QueryContext(context.Background(), "SELECT * FROM "+table)
		if queryErr != nil {
			t.Logf("stop snapshot %s query: %v", table, queryErr)
			continue
		}
		columns, columnErr := rows.Columns()
		if columnErr != nil {
			t.Logf("stop snapshot %s columns: %v", table, columnErr)
			_ = rows.Close()
			continue
		}
		var records []map[string]any
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if scanErr := rows.Scan(pointers...); scanErr != nil {
				t.Logf("stop snapshot %s scan: %v", table, scanErr)
				break
			}
			record := make(map[string]any, len(columns))
			for i, column := range columns {
				if value, ok := values[i].([]byte); ok {
					values[i] = string(value)
				}
				record[column] = values[i]
			}
			records = append(records, record)
		}
		body, marshalErr := json.Marshal(records)
		t.Logf("stop snapshot %s=%s rows_error=%v marshal_error=%v close_error=%v", table, body, rows.Err(), marshalErr, rows.Close())
	}
	t.Logf("stop snapshot read commit=%v at=%s", tx.Commit(), time.Now().UTC().Format(time.RFC3339Nano))
}
