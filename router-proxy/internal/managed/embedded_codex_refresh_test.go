package managed

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// The issuer is deliberately hostile: neither its error nor the old bearer
// may cross the checked SDK or router persistence boundary.
func TestEmbeddedCodexIssuerErrorBoundary(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	root := filepath.Join(base, "project")
	require.NoError(t, os.Mkdir(root, 0700))
	path := filepath.Join(base, "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	credentials, err := store.OpenLocalCodexCredentialStore(filepath.Join(base, "private", "credential.json"))
	require.NoError(t, err)
	old := store.CodexCredential{AccessToken: "expired-fake-secret-sentinel", RefreshToken: "refresh-fake-secret-sentinel", ExpiresAt: time.Now().Add(-time.Hour), AccountID: "fixture-account"}
	require.NoError(t, credentials.Save(ctx, old))
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	model := *ai.GetModel("openai-codex", "gpt-6-sol")
	requests := make(chan *http.Request, 1)
	runnerDone := make(chan error, 1)
	var m *Manager
	m, err = NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-6-sol", Available: true}), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return &model }, CodexCredentialStore: credentials, CodexRefresh: func(context.Context, store.CodexCredential) (store.CodexCredential, error) {
		return store.CodexCredential{}, errors.New("issuer failed fake-secret-sentinel Bearer fake-bearer-sentinel")
	}, CodexHTTPTransport: codexTestTransport{codexOfflineDoer{requests: requests}}, EmbeddedRunnerFactory: func(runCtx context.Context, saved store.ManagedAgentRecord) error {
		err := m.runEmbeddedSession(runCtx, saved)
		runnerDone <- err
		return err
	}})
	require.NoError(t, err)
	defer m.Close(ctx)
	launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "issuer-error-root", Cwd: root, Runtime: "embedded", InitialInstruction: "say done", RequestID: "a5555555-5555-4555-8555-555555555594"})
	require.NoError(t, err)
	select {
	case runErr := <-runnerDone:
		require.Error(t, runErr)
		require.Contains(t, runErr.Error(), "Codex auth unavailable")
		require.NotContains(t, runErr.Error(), "fake-secret-sentinel")
		require.NotContains(t, runErr.Error(), "fake-bearer-sentinel")
	case <-time.After(5 * time.Second):
		t.Fatal("SDK turn did not finish")
	}
	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return e == nil && a.LifecycleState == "failed"
	}, 3*time.Second, 10*time.Millisecond)
	failed, err := db.ManagedAgent(ctx, launched.Agent.AgentID)
	require.NoError(t, err)
	require.Contains(t, failed.LastErrorMessage, "Codex auth unavailable")
	require.NotContains(t, failed.LastErrorMessage, "fake-secret-sentinel")
	require.NotContains(t, failed.LastErrorMessage, "fake-bearer-sentinel")
	require.Empty(t, requests, "failed issuer must not send a provider request")
	saved, err := credentials.Load(ctx)
	require.NoError(t, err)
	require.Equal(t, old.AccessToken, saved.AccessToken)
	require.Equal(t, old.RefreshToken, saved.RefreshToken)
	require.Equal(t, old.AccountID, saved.AccountID)
	require.True(t, old.ExpiresAt.Equal(saved.ExpiresAt), "failed issuer must preserve credential expiry")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "fake-secret-sentinel")
	require.NotContains(t, string(raw), "fake-bearer-sentinel")
}

type codexOfflineDoer struct{ requests chan *http.Request }

func (d codexOfflineDoer) Do(r *http.Request) (*http.Response, error) {
	d.requests <- r.Clone(r.Context())
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(bytes.NewBufferString("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_fake\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fake\",\"status\":\"completed\"}}\n\n")), Request: r}, nil
}

// Refresh pauses after the stream's first owner renewal but before the SDK's
// HTTP Do. A newer owner claims the same saved binding while A is paused.
func TestEmbeddedCodexStaleOwnerCannotSendAfterRefresh(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	root := filepath.Join(base, "project")
	require.NoError(t, os.Mkdir(root, 0700))
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	path := filepath.Join(base, "router.db")
	aDB, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer aDB.Close()
	bDB, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer bDB.Close()
	credentials, err := store.OpenLocalCodexCredentialStore(filepath.Join(base, "private", "credential.json"))
	require.NoError(t, err)
	require.NoError(t, credentials.Save(ctx, store.CodexCredential{AccessToken: "expired-fake-secret-sentinel", RefreshToken: "refresh-fake-secret-sentinel", ExpiresAt: time.Now().Add(-time.Hour), AccountID: "fixture-account"}))
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	model := *ai.GetModel("openai-codex", "gpt-6-sol")
	entered, release := make(chan struct{}), make(chan struct{})
	requests := make(chan *http.Request, 2)
	ownerCh := make(chan embeddedOwner, 1)
	runnerDone := make(chan error, 1)
	events := make(chan Event, 32)
	var m *Manager
	m, err = NewManager(ManagerOptions{Store: aDB, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-6-sol", Available: true}), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return &model }, CodexCredentialStore: credentials, CodexRefresh: func(_ context.Context, _ store.CodexCredential) (store.CodexCredential, error) {
		close(entered)
		<-release
		return store.CodexCredential{AccessToken: "rotated-fake-secret-sentinel", RefreshToken: "new-refresh-fake-secret-sentinel", ExpiresAt: time.Now().Add(time.Hour), AccountID: "fixture-account"}, nil
	}, CodexHTTPTransport: codexTestTransport{codexOfflineDoer{requests: requests}}, EventSink: func(e Event) { events <- e }, EmbeddedRunnerFactory: func(runCtx context.Context, saved store.ManagedAgentRecord) error {
		ownerCh <- runCtx.Value(embeddedOwnerContextKey{}).(embeddedOwner)
		err := m.runEmbeddedSession(runCtx, saved)
		runnerDone <- err
		return err
	}})
	require.NoError(t, err)
	defer m.Close(ctx)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	_, err = m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "codex-stale-root", Cwd: root, Runtime: "embedded", InitialInstruction: "say done", RequestID: "a5555555-5555-4555-8555-555555555593"})
	require.NoError(t, err)
	var owner embeddedOwner
	select {
	case owner = <-ownerCh:
	case <-time.After(5 * time.Second):
		t.Fatal("owner not claimed")
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("credential refresh not entered")
	}
	require.Empty(t, requests, "SDK must not send before blocked refresh")
	require.NoError(t, clock.Advance(embeddedOwnerTTL+time.Second))
	nextBinding := owner.binding
	nextBinding.SessionID = "new-independent-session"
	nextBinding.Epoch++
	require.NoError(t, bDB.ResetEmbeddedSessionEpoch(ctx, owner.binding, nextBinding, "root"))
	close(release)
	select {
	case <-runnerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("stale runner did not finish")
	}
	require.Empty(t, requests, "stale owner sent a provider request after takeover")
	snap, err := bDB.ReopenEmbedded(ctx, nextBinding)
	require.NoError(t, err)
	for _, entry := range snap.Entries {
		require.NotEqual(t, "assistant", entry.Kind, "stale provider completion must not be persisted")
		for _, secret := range []string{"expired-fake-secret-sentinel", "refresh-fake-secret-sentinel", "rotated-fake-secret-sentinel", "new-refresh-fake-secret-sentinel", "Codex request transport failed"} {
			require.NotContains(t, string(entry.Body), secret)
		}
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	for _, secret := range []string{"expired-fake-secret-sentinel", "refresh-fake-secret-sentinel", "rotated-fake-secret-sentinel", "new-refresh-fake-secret-sentinel", "Codex request transport failed"} {
		require.NotContains(t, string(raw), secret)
	}
	for len(events) > 0 {
		event := <-events
		body := fmt.Sprintf("%+v", event)
		for _, secret := range []string{"expired-fake-secret-sentinel", "refresh-fake-secret-sentinel", "rotated-fake-secret-sentinel", "new-refresh-fake-secret-sentinel", "Codex request transport failed"} {
			require.NotContains(t, body, secret)
		}
	}
}

func TestEmbeddedCodexRefreshSerializedAcrossRootAndIndependentWorker(t *testing.T) {
	ctx := context.Background()
	base := testfixture.PrivateDir(t)
	root := filepath.Join(base, "project")
	require.NoError(t, os.Mkdir(root, 0700))
	credentials, err := store.OpenLocalCodexCredentialStore(filepath.Join(base, "private", "credential.json"))
	require.NoError(t, err)
	expired := store.CodexCredential{AccessToken: "expired-fake-secret-sentinel", RefreshToken: "refresh-fake-secret-sentinel", ExpiresAt: time.Now().Add(-time.Hour), AccountID: "fixture-account"}
	require.NoError(t, credentials.Save(ctx, expired))
	dbPath := filepath.Join(base, "router.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	model := *ai.GetModel("openai-codex", "gpt-6-sol")
	requests := make(chan *http.Request, 2)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	rotated := store.CodexCredential{AccessToken: "rotated-fake-secret-sentinel", RefreshToken: "new-refresh-fake-secret-sentinel", ExpiresAt: time.Now().Add(time.Hour), AccountID: "fixture-account"}
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-6-sol", Available: true}), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return &model }, CodexCredentialStore: credentials, CodexRefresh: func(ctx context.Context, c store.CodexCredential) (store.CodexCredential, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
		case <-ctx.Done():
			return store.CodexCredential{}, ctx.Err()
		}
		return rotated, nil
	}, CodexHTTPTransport: codexTestTransport{codexOfflineDoer{requests: requests}}})
	require.NoError(t, err)
	defer m.Close(ctx)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := LaunchRequest{Name: []string{"codex-root", "codex-worker"}[i], Cwd: root, Runtime: "embedded", InitialInstruction: "say done", RequestID: []string{"a5555555-5555-4555-8555-555555555591", "a5555555-5555-4555-8555-555555555592"}[i]}
			if i == 0 {
				_, e := m.LaunchEmbeddedRoot(ctx, req)
				errs <- e
			} else {
				_, e := m.Launch(ctx, req)
				errs <- e
			}
		}(i)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh not entered")
	}
	require.Equal(t, int32(1), calls.Load())
	close(release)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("launch timed out")
	}
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	for i := 0; i < 2; i++ {
		select {
		case r := <-requests:
			require.Equal(t, "Bearer "+rotated.AccessToken, r.Header.Get("Authorization"))
			require.Equal(t, "fixture-account", r.Header.Get("ChatGPT-Account-ID"))
			require.Equal(t, "/backend-api/codex/responses", r.URL.Path)
		case <-time.After(5 * time.Second):
			t.Fatal("Codex request missing")
		}
	}
	require.Equal(t, int32(1), calls.Load())
	reopened, err := store.OpenLocalCodexCredentialStore(filepath.Join(base, "private", "credential.json"))
	require.NoError(t, err)
	saved, err := reopened.Load(ctx)
	require.NoError(t, err)
	require.Equal(t, rotated.AccessToken, saved.AccessToken)
	require.Equal(t, rotated.RefreshToken, saved.RefreshToken)
	require.Equal(t, rotated.AccountID, saved.AccountID)
	require.True(t, rotated.ExpiresAt.Equal(saved.ExpiresAt))
	raw, err := os.ReadFile(dbPath)
	require.NoError(t, err)
	for _, secret := range []string{expired.AccessToken, expired.RefreshToken, rotated.AccessToken, rotated.RefreshToken} {
		require.NotContains(t, string(raw), secret)
	}
}
