package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/codexdevice"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// Auth terminal includes two real private Node credential-consumer runs
// (10s each) and a real catalog snapshot (5s), not just mocked issuer HTTP.
const browserAuthCommitBudget = 25 * time.Second

type browserOfflineTransport func(*http.Request) (*http.Response, error)

func (f browserOfflineTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func browserResponse(r *http.Request, body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

type browserFixFixture struct {
	base, path, access, rotated string
	db                          *store.Store
	credentials                 *store.LocalCodexCredentialStore
	manager                     *managed.Manager
	router                      *transport.Router
	auth                        *managed.ProviderAuthCoordinator
	ordinary                    fixedAuthHelper
	providerCalls               atomic.Int64
	issuerCalls                 atomic.Int64
	issuerGate                  chan struct{}
}

// All owned components are real. Only OAuth and Responses HTTP transports are
// replaced. Requests use ServeHTTP after a real loopback bind establishes the
// Router-owned process generation and joins managed startup.
func newBrowserFixFixture(t *testing.T) *browserFixFixture {
	t.Helper()
	ctx := context.Background()
	base := newCodexCredentialParent(t)
	project := filepath.Join(base, "project")
	require.NoError(t, os.Mkdir(project, 0700))
	private := filepath.Join(base, "private")
	require.NoError(t, os.Mkdir(private, 0700))
	f := &browserFixFixture{base: base, path: filepath.Join(base, "router.db")}
	var err error
	f.db, err = store.Open(ctx, f.path, store.Options{})
	require.NoError(t, err)
	f.credentials, err = store.OpenLocalCodexCredentialStore(filepath.Join(private, "codex.json"))
	require.NoError(t, err)
	node, err := exec.LookPath("node")
	require.NoError(t, err, "the production auth helper requires Node")
	helperPath, err := filepath.Abs(filepath.Join("..", "..", "..", "auth-helper", "index.mjs"))
	require.NoError(t, err)
	piDir := filepath.Join(base, "pi-agent")
	require.NoError(t, os.Mkdir(piDir, 0700))
	// Ordinary Google remains in the broad catalog. The fake key is resolved
	// locally; the helper disallows model-network refresh and has no Cursor key.
	require.NoError(t, os.WriteFile(filepath.Join(piDir, "auth.json"), []byte(`{"google":{"type":"api_key","key":"fake-google-only"}}`), 0600))
	f.ordinary = fixedAuthHelper{config: managed.Config{ManagedHome: base, PiAgentDir: piDir, AuthHelperPath: helperPath}, nodeExecutable: node, ownedCodex: true}
	f.credentials.SetProjection(f.ordinary.consumeCodexCredential)
	require.NoError(t, f.credentials.SynchronizeProjection(ctx))
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"browser-fixture-account"}}`))
	f.access = "fake-header." + claims + ".fake-browser-secret-signature"
	f.rotated = "fake-header." + claims + ".fake-rotated-browser-secret"
	issuer := browserOfflineTransport(func(r *http.Request) (*http.Response, error) {
		f.issuerCalls.Add(1)
		if r.URL.Host != "auth.openai.com" || r.Method != http.MethodPost {
			return nil, errors.New("offline issuer rejected destination")
		}
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			if f.issuerGate != nil {
				select {
				case <-r.Context().Done():
					return nil, r.Context().Err()
				case <-f.issuerGate:
				}
			}
			return browserResponse(r, `{"device_auth_id":"fake-device-secret","user_code":"TEST-CODE","interval":"1"}`), nil
		case "/api/accounts/deviceauth/token":
			return browserResponse(r, `{"authorization_code":"fake-authorization-secret","code_verifier":"fake-verifier-secret"}`), nil
		case "/oauth/token":
			raw, e := io.ReadAll(r.Body)
			if e != nil {
				return nil, e
			}
			access, refresh := f.access, "fake-browser-refresh-secret"
			if strings.Contains(string(raw), "grant_type=refresh_token") {
				if !strings.Contains(string(raw), "fake-browser-refresh-secret") {
					return nil, errors.New("offline issuer rejected refresh")
				}
				access, refresh = f.rotated, "fake-rotated-refresh-secret"
			} else if !strings.Contains(string(raw), "fake-verifier-secret") {
				return nil, errors.New("offline issuer rejected exchange")
			}
			body, _ := json.Marshal(map[string]any{"access_token": access, "refresh_token": refresh, "expires_in": 3600})
			return browserResponse(r, string(body)), nil
		default:
			return nil, errors.New("offline issuer rejected path")
		}
	})
	model := *ai.GetModel("openai-codex", "gpt-6-sol")
	provider := browserOfflineTransport(func(r *http.Request) (*http.Response, error) {
		f.providerCalls.Add(1)
		credential, e := f.credentials.Load(r.Context())
		if e != nil || r.URL.String() != "https://chatgpt.com/backend-api/codex/responses" || r.Header.Get("Authorization") != "Bearer "+credential.AccessToken || r.Header.Get("ChatGPT-Account-ID") != "browser-fixture-account" {
			return nil, errors.New("offline provider rejected auth")
		}
		body := `data: {"type":"response.created","response":{"id":"resp_browser"}}

data: {"type":"response.output_item.added","output_index":0,"item":{"id":"item_browser","type":"message","role":"assistant","content":[]}}

data: {"type":"response.content_part.added","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}

data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"ready"}

data: {"type":"response.output_text.done","output_index":0,"content_index":0,"text":"ready"}

data: {"type":"response.output_item.done","output_index":0,"item":{"id":"item_browser","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ready"}]}}

data: {"type":"response.completed","response":{"id":"resp_browser","status":"completed"}}

`
		response := browserResponse(r, body)
		response.Header.Set("Content-Type", "text/event-stream")
		return response, nil
	})
	f.manager, err = managed.NewManager(managed.ManagerOptions{Store: f.db, Manifest: manifest, Config: managed.Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{project}}, CatalogSource: managed.StoreCatalogSource{Store: f.db}, CodexCredentialStore: f.credentials, CodexRefresh: func(ctx context.Context, c store.CodexCredential) (store.CodexCredential, error) {
		return codexdevice.Refresh(ctx, &http.Client{Transport: issuer}, c)
	}, CodexHTTPTransport: provider, EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return &model },
		// Keep goal evaluation enabled, but mock its external verdict separately
		// from ordinary Responses turns and their exact provider-entry counts.
		EmbeddedGoalEvaluator: func(context.Context, store.EmbeddedGoalEvaluationKey) (string, string, error) {
			return store.EmbeddedGoalEvaluationDone, "", nil
		}})
	require.NoError(t, err)
	helper := managed.CodexBrowserAuth{Ordinary: f.ordinary, Credentials: f.credentials, Client: &http.Client{Transport: issuer}, Manager: f.manager}
	f.auth, err = managed.NewProviderAuthCoordinator(f.db, f.manager, helper)
	require.NoError(t, err)
	f.router, err = transport.New(transport.Options{Store: f.db, Managed: f.manager, ProviderAuth: f.auth, Host: "127.0.0.1", Port: 0})
	require.NoError(t, err)
	t.Cleanup(func() {
		f.auth.Close()
		require.NoError(t, f.router.Close(ctx))
		require.NoError(t, f.manager.Close(ctx))
		require.NoError(t, f.db.Close())
	})
	require.NoError(t, f.router.Listen(ctx))
	return f
}

func (f *browserFixFixture) call(t *testing.T, method, path, body, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if id != "" {
		req.Header.Set("X-Request-ID", id)
	}
	out := httptest.NewRecorder()
	f.router.ServeHTTP(out, req)
	for _, secret := range []string{f.access, f.rotated, "fake-browser-refresh-secret", "fake-device-secret", "fake-authorization-secret", "fake-verifier-secret"} {
		require.NotContains(t, out.Body.String(), secret)
	}
	return out
}
func (f *browserFixFixture) login(t *testing.T) {
	t.Helper()
	id := "e5111111-1111-4111-8111-111111111111"
	out := f.call(t, "POST", "/v1/provider-auth/openai-codex/login", "{}", id)
	require.Equal(t, 202, out.Code, out.Body.String())
	require.Eventually(t, func() bool {
		operation, e := f.db.ManagedOperation(context.Background(), id)
		return e == nil && operation.State == "terminal"
	}, browserAuthCommitBudget, 10*time.Millisecond)
	credential, err := f.credentials.Load(context.Background())
	require.NoError(t, err)
	require.Equal(t, f.access, credential.AccessToken)
	before := f.issuerCalls.Load()
	replay := f.call(t, "POST", "/v1/provider-auth/openai-codex/login", "{}", id)
	require.Equal(t, out.Body.String(), replay.Body.String())
	require.Equal(t, before, f.issuerCalls.Load())
	require.Equal(t, int64(3), before)
}
func (f *browserFixFixture) launch(t *testing.T, name, role, id string) {
	t.Helper()
	request := managed.LaunchRequest{Name: name, Cwd: filepath.Join(f.base, "project"), Runtime: "embedded", Model: &managed.Model{Provider: "openai-codex", ID: "gpt-6-sol"}, InitialInstruction: "say ready", RequestID: id}
	var err error
	if role == "root" {
		_, err = f.manager.LaunchEmbeddedRoot(context.Background(), request)
	} else {
		_, err = f.manager.Launch(context.Background(), request)
	}
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		result, e := f.manager.DirectRootControl(context.Background(), name, "", "goal.control", map[string]any{"action": "status"})
		return e == nil && result.Status == "ok"
	}, 5*time.Second, 10*time.Millisecond)
}

func TestEmbeddedBrowserGoalInlineRoundTripReplayAndReopen(t *testing.T) {
	f := newBrowserFixFixture(t)
	f.login(t)
	f.launch(t, "browser-root", "root", "e5222222-2222-4222-8222-222222222222")
	f.launch(t, "browser-worker", "worker", "e5333333-3333-4333-8333-333333333333")
	for i, name := range []string{"browser-root", "browser-worker"} {
		goalID := []string{"e5444444-4444-4444-8444-444444444444", "e5555555-5555-4555-8555-555555555555"}[i]
		inlineID := []string{"e5666666-6666-4666-8666-666666666666", "e5777777-7777-4777-8777-777777777777"}[i]
		goalPath := "/v1/managed-agents/" + name + "/goal"
		body := `{"action":"set","goal":"browser goal","settings":{"goal_lock_enabled":true,"snooze_enabled":true,"snooze_duration_ms":120000,"snooze_turn_limit":3,"snooze_budget_cap_ms":240000,"snooze_refill_yield_ms":50000,"snooze_refill_elapsed_ms":80000}}`
		set := f.call(t, "PUT", goalPath, body, goalID)
		require.Equal(t, 200, set.Code, set.Body.String())
		get := f.call(t, "GET", goalPath, "", "")
		require.Equal(t, 200, get.Code)
		require.Contains(t, get.Body.String(), `"goal":"browser goal"`)
		require.Contains(t, get.Body.String(), `"snooze_duration_ms":120000`)
		policy, err := f.db.ManagedGoalPolicyFor(context.Background(), name)
		require.NoError(t, err)
		require.Equal(t, int64(1), policy.DesiredRevision)
		replay := f.call(t, "PUT", goalPath, body, goalID)
		require.Equal(t, set.Body.String(), replay.Body.String())
		conflict := f.call(t, "PUT", goalPath, `{"action":"off"}`, goalID)
		require.Equal(t, 409, conflict.Code)
		inlinePath := "/v1/managed-agents/" + name + "/inline-tools"
		inline := f.call(t, "PUT", inlinePath, `{"enabled":true}`, inlineID)
		require.Equal(t, 200, inline.Code, inline.Body.String())
		read := f.call(t, "GET", inlinePath, "", "")
		require.Equal(t, `{"enabled":true}`, read.Body.String())
		require.Equal(t, inline.Body.String(), f.call(t, "PUT", inlinePath, `{"enabled":true}`, inlineID).Body.String())
		// The saved allowance is a browser preference, not an embedded tool gate.
		offID := []string{"e5888888-8888-4888-8888-888888888888", "e5999999-9999-4999-8999-999999999999"}[i]
		off := f.call(t, "PUT", goalPath, `{"action":"off"}`, offID)
		require.Equal(t, 200, off.Code, off.Body.String())
		policy, err = f.db.ManagedGoalPolicyFor(context.Background(), name)
		require.NoError(t, err)
		require.False(t, policy.Config.Enabled)
		require.Equal(t, int64(2), policy.DesiredRevision)
		require.Equal(t, int64(120000), policy.Config.Settings.SnoozeDurationMS, "off retains the existing browser settings")
		require.Equal(t, set.Body.String(), f.call(t, "PUT", goalPath, body, goalID).Body.String(), "replay returns its immutable response, not current policy")
	}
	// Join the listener and manager before reopening persistence without a
	// recovered owner or an RPC process.
	require.NoError(t, f.router.Close(context.Background()))
	require.NoError(t, f.manager.Close(context.Background()))
	require.NoError(t, f.db.Close())
	var err error
	f.db, err = store.Open(context.Background(), f.path, store.Options{})
	require.NoError(t, err)
	for _, name := range []string{"browser-root", "browser-worker"} {
		policy, e := f.db.ManagedGoalPolicyFor(context.Background(), name)
		require.NoError(t, e)
		require.False(t, policy.Config.Enabled)
		desired, e := f.db.LoadManagedDesiredPolicy(context.Background(), name)
		require.NoError(t, e)
		require.Contains(t, string(desired.Canonical), `"enabled":true`)
	}
	operation, err := f.db.ManagedOperation(context.Background(), "e5444444-4444-4444-8444-444444444444")
	require.NoError(t, err)
	require.Contains(t, string(operation.ResponseBody), "browser goal")
	require.NotEmpty(t, operation.ExpiresAt)
}

func TestEmbeddedBrowserCodexCancellationKeepsStoreSignedOut(t *testing.T) {
	f := newBrowserFixFixture(t)
	f.issuerGate = make(chan struct{})
	id := "e6444444-4444-4444-8444-444444444444"
	out := f.call(t, "POST", "/v1/provider-auth/openai-codex/login", "{}", id)
	require.Equal(t, 202, out.Code)
	require.Eventually(t, func() bool { return f.issuerCalls.Load() == 1 }, time.Second, 10*time.Millisecond)
	f.auth.Close()
	_, err := f.credentials.Load(context.Background())
	require.ErrorIs(t, err, os.ErrNotExist)
	operation, err := f.db.ManagedOperation(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, "terminal", operation.State)
	require.Equal(t, out.Body.String(), f.call(t, "POST", "/v1/provider-auth/openai-codex/login", "{}", id).Body.String())
	require.Equal(t, int64(1), f.issuerCalls.Load())
	require.Zero(t, f.providerCalls.Load())
}

func TestEmbeddedBrowserCodexLoginRootWorkerRotationLogoutComposition(t *testing.T) {
	f := newBrowserFixFixture(t)
	status := f.call(t, "GET", "/v1/provider-auth", "", "")
	require.Equal(t, 200, status.Code)
	require.Contains(t, status.Body.String(), `"signed_out"`)
	f.login(t)
	status = f.call(t, "GET", "/v1/provider-auth", "", "")
	require.Equal(t, 200, status.Code)
	require.Contains(t, status.Body.String(), `"authenticated":true`)
	// A separate unconfigured CLI/store handle cannot invoke this instance's
	// consumer. The authoritative snapshot must reconcile both B and absence
	// through the real private pipe/ModelRuntime, before status is committed.
	ctx := context.Background()
	readOrdinary := func() map[string]map[string]any {
		raw, e := os.ReadFile(filepath.Join(f.ordinary.config.PiAgentDir, "auth.json"))
		require.NoError(t, e)
		var value map[string]map[string]any
		require.NoError(t, json.Unmarshal(raw, &value))
		return value
	}
	original, err := f.credentials.Load(ctx)
	require.NoError(t, err)
	peer, err := store.OpenLocalCodexCredentialStore(filepath.Join(f.base, "private", "codex.json"))
	require.NoError(t, err)
	issuerBefore := f.issuerCalls.Load()
	credentialB := original
	credentialB.AccessToken = original.AccessToken + "-peer"
	require.NoError(t, peer.Save(ctx, credentialB))
	require.Equal(t, original.AccessToken, readOrdinary()["openai-codex"]["access"])
	status = f.call(t, "GET", "/v1/provider-auth", "", "")
	require.Equal(t, 200, status.Code)
	projected := readOrdinary()
	require.Equal(t, credentialB.AccessToken, projected["openai-codex"]["access"])
	require.Contains(t, projected, "google")
	require.NoError(t, peer.Delete(ctx))
	require.Equal(t, credentialB.AccessToken, readOrdinary()["openai-codex"]["access"])
	status = f.call(t, "GET", "/v1/provider-auth", "", "")
	require.Equal(t, 200, status.Code)
	projected = readOrdinary()
	require.NotContains(t, projected, "openai-codex")
	require.Contains(t, projected, "google")
	var peerSnapshot struct {
		Providers []store.ManagedProviderStatus `json:"providers"`
	}
	require.NoError(t, json.Unmarshal(status.Body.Bytes(), &peerSnapshot))
	for _, p := range peerSnapshot.Providers {
		if p.Provider == "openai-codex" {
			require.False(t, p.Authenticated)
			require.Equal(t, "signed_out", p.Status)
		}
	}
	require.NoError(t, peer.Save(ctx, original))
	status = f.call(t, "GET", "/v1/provider-auth", "", "")
	require.Equal(t, 200, status.Code)
	require.Equal(t, original.AccessToken, readOrdinary()["openai-codex"]["access"])
	require.Equal(t, issuerBefore, f.issuerCalls.Load(), "peer reconciliation must not invoke the issuer")
	require.Zero(t, f.providerCalls.Load(), "peer reconciliation must not invoke the provider")
	models := f.call(t, "GET", "/v1/models", "", "")
	require.Equal(t, 200, models.Code)
	require.Contains(t, models.Body.String(), "gpt-6-sol")
	require.Contains(t, models.Body.String(), "google")
	f.launch(t, "auth-root", "root", "e6111111-1111-4111-8111-111111111111")
	f.launch(t, "auth-worker", "worker", "e6222222-2222-4222-8222-222222222222")
	require.Equal(t, int64(2), f.providerCalls.Load(), "real root and worker SDK requests use browser-completed credentials")
	// Rotation uses the same authoritative file and continuously updates the
	// ordinary RPC consumer's expected OAuth record through the private helper.
	require.NoError(t, f.credentials.Update(context.Background(), func(c store.CodexCredential) (store.CodexCredential, error) {
		c.ExpiresAt = time.Now().Add(-time.Hour)
		return c, nil
	}))
	require.NoError(t, f.manager.AdmitPrompt(context.Background(), "auth-root", "use the rotated credential"))
	require.Equal(t, int64(3), f.providerCalls.Load())
	require.Equal(t, int64(4), f.issuerCalls.Load(), "one shared refresh after device acquisition")
	require.NoError(t, f.manager.AdmitPrompt(context.Background(), "auth-worker", "reuse the rotated credential"))
	require.Equal(t, int64(4), f.providerCalls.Load())
	require.Equal(t, int64(4), f.issuerCalls.Load(), "the worker reuses the committed root refresh")
	raw, err := os.ReadFile(filepath.Join(f.ordinary.config.PiAgentDir, "auth.json"))
	require.NoError(t, err)
	var ordinary map[string]map[string]any
	require.NoError(t, json.Unmarshal(raw, &ordinary))
	require.Equal(t, f.rotated, ordinary["openai-codex"]["access"])
	require.Contains(t, ordinary, "google")
	// Require a checked final for the rotated turn, not merely provider entry.
	settled, err := sql.Open("sqlite", f.path)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		var count int
		e := settled.QueryRow(`SELECT count(*) FROM embedded_entries WHERE agent_id='auth-root' AND kind='assistant' AND json_extract(body,'$.stopReason')='stop'`).Scan(&count)
		return e == nil && count == 2
	}, 3*time.Second, 10*time.Millisecond)
	require.NoError(t, settled.Close())
	logoutID := "e6333333-3333-4333-8333-333333333333"
	out := f.call(t, "POST", "/v1/provider-auth/openai-codex/logout", "{}", logoutID)
	require.Equal(t, 202, out.Code)
	require.Eventually(t, func() bool {
		op, e := f.db.ManagedOperation(context.Background(), logoutID)
		return e == nil && op.State == "terminal"
	}, browserAuthCommitBudget, 10*time.Millisecond)
	_, err = f.credentials.Load(context.Background())
	require.ErrorIs(t, err, os.ErrNotExist)
	raw, err = os.ReadFile(filepath.Join(f.ordinary.config.PiAgentDir, "auth.json"))
	require.NoError(t, err)
	ordinary = nil
	require.NoError(t, json.Unmarshal(raw, &ordinary))
	require.NotContains(t, ordinary, "openai-codex")
	require.Contains(t, ordinary, "google")
	status = f.call(t, "GET", "/v1/provider-auth", "", "")
	require.Equal(t, 200, status.Code)
	var snapshot struct {
		Providers []store.ManagedProviderStatus `json:"providers"`
	}
	require.NoError(t, json.Unmarshal(status.Body.Bytes(), &snapshot))
	for _, p := range snapshot.Providers {
		if p.Provider == "openai-codex" {
			require.False(t, p.Authenticated)
			require.Equal(t, "signed_out", p.Status)
		}
	}
	before := f.providerCalls.Load()
	require.Error(t, f.manager.AdmitPrompt(context.Background(), "auth-root", "must reject after logout"))
	require.Error(t, f.manager.AdmitPrompt(context.Background(), "auth-worker", "must reject after logout"))
	require.Equal(t, before, f.providerCalls.Load(), "logout rejection occurs before provider entry")
	check, err := sql.Open("sqlite", f.path)
	require.NoError(t, err)
	defer check.Close()
	for _, query := range []string{`SELECT response_body FROM managed_http_operations`, `SELECT body FROM embedded_entries`} {
		rows, e := check.Query(query)
		require.NoError(t, e)
		for rows.Next() {
			var value []byte
			require.NoError(t, rows.Scan(&value))
			require.NotContains(t, string(value), f.access)
			require.NotContains(t, string(value), "fake-browser-refresh-secret")
			require.NotContains(t, string(value), "fake-rotated-browser-secret")
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
	}
	info, err := os.Stat(filepath.Join(f.base, "private", "codex.json.lock"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
}
