package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

type g4RejectProviderRequests struct{ calls atomic.Int32 }

func (d *g4RejectProviderRequests) Do(*http.Request) (*http.Response, error) {
	d.calls.Add(1)
	return nil, errors.New("unexpected provider request")
}

// An actual embedded SDK turn fails through the fake issuer, then each public
// projection is serialized independently. Nothing here calls a real provider.
func TestEmbeddedG4CodexIssuerErrorRouterSurfaces(t *testing.T) {
	ctx := context.Background()
	base := testfixture.PrivateDir(t)
	root := filepath.Join(base, "project")
	require.NoError(t, os.Mkdir(root, 0700))
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	credentials, err := store.OpenLocalCodexCredentialStore(filepath.Join(testfixture.PrivateDir(t), "credential.json"))
	require.NoError(t, err)
	require.NoError(t, credentials.Save(ctx, store.CodexCredential{
		AccessToken: "expired-fake-secret-sentinel", RefreshToken: "refresh-fake-secret-sentinel",
		ExpiresAt: time.Now().Add(-time.Hour), AccountID: "fixture-account",
	}))
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	model := *ai.GetModel("openai-codex", "gpt-6-sol")
	provider := &g4RejectProviderRequests{}
	var issuerCalls atomic.Int32
	events := make(chan managed.Event, 32)
	manager, err := managed.NewManager(managed.ManagerOptions{
		Store: db, Manifest: manifest,
		Config:                managed.Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:         managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-6-sol", Available: true}),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return &model },
		CodexCredentialStore:  credentials,
		CodexRefresh: func(context.Context, store.CodexCredential) (store.CodexCredential, error) {
			issuerCalls.Add(1)
			return store.CodexCredential{}, errors.New("issuer failed fake-secret-sentinel Bearer fake-bearer-sentinel")
		},
		CodexHTTPTransport: fixtureCodexTransport{provider},
		EventSink:          func(event managed.Event) { events <- event },
	})
	require.NoError(t, err)
	router, err := New(Options{Store: db, Managed: manager, DefaultEmbeddedRoot: true})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	defer func() { server.Close(); _ = router.Close(ctx) }()
	check := func(surface string, raw []byte) {
		t.Helper()
		for _, marker := range []string{"fake-secret-sentinel", "fake-bearer-sentinel"} {
			require.NotContains(t, string(raw), marker, surface)
		}
	}
	fetch := func(method, path, id, body string) (int, []byte) {
		t.Helper()
		req, e := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		require.NoError(t, e)
		if id != "" {
			req.Header.Set("X-Request-ID", id)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, e := http.DefaultClient.Do(req)
		require.NoError(t, e)
		defer resp.Body.Close()
		raw, e := io.ReadAll(resp.Body)
		require.NoError(t, e)
		check(path, raw)
		return resp.StatusCode, raw
	}
	status, launch := fetch(http.MethodPost, "/v1/managed-agents", "a5555555-5555-4555-8555-555555555595", fmt.Sprintf(`{"name":"issuer-error-root","cwd":%q,"initial_instruction":"say done"}`, root))
	require.Equal(t, http.StatusAccepted, status, string(launch))
	require.Eventually(t, func() bool {
		row, e := db.ManagedAgent(ctx, "issuer-error-root")
		return e == nil && row.LifecycleState == "failed"
	}, 5*time.Second, 10*time.Millisecond)
	failedRow, err := db.ManagedAgent(ctx, "issuer-error-root")
	require.NoError(t, err)
	require.EqualValues(t, 1, issuerCalls.Load(), "the fake issuer must actually be reached; last_error_code=%q last_error_message=%q", failedRow.LastErrorCode, failedRow.LastErrorMessage)
	require.Zero(t, provider.calls.Load(), "failed issuer must not send a provider request")
	for _, path := range []string{"/v1/managed-agents/issuer-error-root", "/v1/managed-agents"} {
		status, raw := fetch(http.MethodGet, path, "", "")
		require.Equal(t, http.StatusOK, status, string(raw))
		var projection map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &projection))
		var agents []map[string]any
		if path == "/v1/managed-agents" {
			require.NoError(t, json.Unmarshal(projection["agents"], &agents))
		} else {
			var agent map[string]any
			require.NoError(t, json.Unmarshal(projection["agent"], &agent))
			agents = []map[string]any{agent}
		}
		require.Len(t, agents, 1)
		require.Equal(t, "failed", agents[0]["lifecycle_state"])
		require.Contains(t, string(raw), "Codex auth unavailable", "failure must remain publicly actionable")
	}
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + uiLivePath
	conn, response, err := websocket.Dial(ctx, wsURL, nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "done")
	readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, frame, err := conn.Read(readCtx)
	require.NoError(t, err)
	check("ui-live snapshot", frame)
	var snapshot struct {
		Type   string           `json:"type"`
		Agents []map[string]any `json:"agents"`
	}
	require.NoError(t, json.Unmarshal(frame, &snapshot))
	require.Equal(t, "managed_agents_snapshot", snapshot.Type)
	require.Len(t, snapshot.Agents, 1)
	require.Equal(t, "failed", snapshot.Agents[0]["lifecycle_state"])
	_, children, err := conn.Read(readCtx)
	require.NoError(t, err)
	check("ui-live children", children)
	for len(events) > 0 {
		event := <-events
		raw, e := json.Marshal(event)
		require.NoError(t, e)
		check("manager EventSink", raw)
	}
	row, err := db.ManagedAgent(ctx, "issuer-error-root")
	require.NoError(t, err)
	require.Contains(t, row.LastErrorMessage, "Codex auth unavailable")
	check("last error", []byte(row.LastErrorMessage))
}
