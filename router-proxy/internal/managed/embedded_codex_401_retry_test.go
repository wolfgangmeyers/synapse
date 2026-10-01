package managed

import (
	"context"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedTransportCategoriesFixedOnly(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&net.DNSError{Err: "secret.invalid", Name: "secret.invalid"}, "dns"},
		{x509.UnknownAuthorityError{}, "tls_cert"},
		{&net.OpError{Op: "read", Err: context.DeadlineExceeded}, "timeout"},
		{&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, "connection"},
		{errors.New("secret bearer"), "other"},
	} {
		require.Equal(t, tc.want, embeddedTransportCategory(tc.err))
	}
}

type codex401Doer struct {
	mu           sync.Mutex
	requests     []string
	accounts     []string
	secondStatus int
	firstFailure string
}

func (d *codex401Doer) Do(r *http.Request) (*http.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	bearer := r.Header.Get("Authorization")
	d.requests = append(d.requests, bearer)
	d.accounts = append(d.accounts, r.Header.Get("ChatGPT-Account-ID"))
	if len(d.requests) == 1 && d.firstFailure == "redirect" {
		return &http.Response{StatusCode: http.StatusTemporaryRedirect, Header: http.Header{"Location": []string{"https://other.invalid/deny"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	}
	if len(d.requests) == 1 && d.firstFailure == "transport" {
		return nil, errors.New("fake transport error")
	}
	if len(d.requests) == 1 && d.firstFailure == "connection" {
		return nil, &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}
	}
	status := 401
	body := ""
	if len(d.requests) == 1 && d.firstFailure == "partial" {
		status = 200
		body = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"partial_fake\"}}\n\ndata: {\"type\":\"error\",\"error\":{\"message\":\"401 unauthorized\"}}\n\n"
	}
	if len(d.requests) == 1 && d.firstFailure == "partial-output" {
		status = 200
		body = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"partial_worker\"}}\n\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"item_worker\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\ndata: {\"type\":\"response.content_part.added\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"\"}}\n\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"partial worker answer\"}\n\ndata: {\"type\":\"error\",\"error\":{\"message\":\"401 unauthorized worker-old-fake-bearer worker-old-fake-refresh\"}}\n\n"
	}
	if len(d.requests) == 2 {
		status = d.secondStatus
		if status == 200 {
			body = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_fake\"}}\n\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"item_fake\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\ndata: {\"type\":\"response.content_part.added\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"\"}}\n\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"rotated answer\"}\n\ndata: {\"type\":\"response.output_text.done\",\"output_index\":0,\"content_index\":0,\"text\":\"rotated answer\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fake\",\"status\":\"completed\"}}\n\n"
		}
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}
func (d *codex401Doer) seen() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.requests...)
}
func (d *codex401Doer) seenAccounts() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.accounts...)
}

// A direct independent worker has no parent root; its checked SDK turn must
// use the same single typed pre-output 401 boundary as a root session.
func TestEmbeddedIndependentWorkerCodexPreOutput401(t *testing.T) {
	for _, tc := range []struct {
		name          string
		second        int
		firstFailure  string
		wantRequests  int
		wantRefreshes int32
	}{
		{"pre-output 401 rotates once", 200, "", 2, 1},
		{"second 401 stops", 401, "", 2, 1},
		{"output then SSE error stays unknown", 0, "partial-output", 1, 0},
		{"redirect is terminal without refresh", 0, "redirect", 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			base, err := os.MkdirTemp("/srv/disk-tmp", "embedded-worker-codex-401-*")
			require.NoError(t, err)
			t.Cleanup(func() { _ = os.RemoveAll(base) })
			root := filepath.Join(base, "project")
			require.NoError(t, os.Mkdir(root, 0700))
			dbPath := filepath.Join(base, "router.db")
			db, err := store.Open(ctx, dbPath, store.Options{})
			require.NoError(t, err)
			defer db.Close()
			credentialPath := filepath.Join(base, "private", "credential.json")
			credentials, err := store.OpenLocalCodexCredentialStore(credentialPath)
			require.NoError(t, err)
			old := store.CodexCredential{AccessToken: "worker-old-fake-bearer", RefreshToken: "worker-old-fake-refresh", ExpiresAt: time.Now().Add(time.Hour), AccountID: "worker-old-account"}
			rotated := store.CodexCredential{AccessToken: "worker-new-fake-bearer", RefreshToken: "worker-new-fake-refresh", ExpiresAt: time.Now().Add(2 * time.Hour), AccountID: "worker-new-account"}
			require.NoError(t, credentials.Save(ctx, old))
			manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
			require.NoError(t, err)
			model := *ai.GetModel("openai-codex", "gpt-6-sol")
			doer := &codex401Doer{secondStatus: tc.second, firstFailure: tc.firstFailure}
			var refreshes atomic.Int32
			done := make(chan error, 1)
			var m *Manager
			m, err = NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-6-sol", Available: true}), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return &model }, CodexCredentialStore: credentials, CodexRefresh: func(_ context.Context, locked store.CodexCredential) (store.CodexCredential, error) {
				refreshes.Add(1)
				require.Equal(t, old.AccessToken, locked.AccessToken, "Store.Update must compare the bearer actually sent")
				require.Equal(t, old.AccountID, locked.AccountID)
				return rotated, nil
			}, CodexHTTPTransport: codexTestTransport{doer}, EmbeddedRunnerFactory: func(runCtx context.Context, saved store.ManagedAgentRecord) error {
				err := m.runEmbeddedSession(runCtx, saved)
				done <- err
				return err
			}})
			require.NoError(t, err)
			defer m.Close(ctx)
			launched, err := m.Launch(ctx, LaunchRequest{Name: "retry-401-independent-worker", Cwd: root, Runtime: "embedded", InitialInstruction: "say done", RequestID: "a5555555-5555-4555-8555-555555555596"})
			require.NoError(t, err)
			select {
			case <-done:
			case <-time.After(8 * time.Second):
				t.Fatal("worker SDK turn timed out")
			}
			binding := store.EmbeddedBinding{AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
			role, err := db.SavedEmbeddedSessionRole(ctx, binding)
			require.NoError(t, err)
			require.Equal(t, "worker", role)
			// The only managed identity in this fixture is this worker: no parent
			// session, launch tool, or root issued its initial provider request.
			read, err := sql.Open("sqlite", dbPath)
			require.NoError(t, err)
			defer read.Close()
			var agents int
			require.NoError(t, read.QueryRowContext(ctx, `SELECT count(*) FROM managed_agents`).Scan(&agents))
			require.Equal(t, 1, agents)
			wantBearers := []string{"Bearer " + old.AccessToken}
			wantAccounts := []string{old.AccountID}
			if tc.wantRequests == 2 {
				wantBearers = append(wantBearers, "Bearer "+rotated.AccessToken)
				wantAccounts = append(wantAccounts, rotated.AccountID)
			}
			require.Equal(t, wantBearers, doer.seen(), "no third request or post-output replay")
			require.Equal(t, wantAccounts, doer.seenAccounts())
			require.Equal(t, tc.wantRefreshes, refreshes.Load(), "one shared credential refresh maximum")
			reopened, err := store.OpenLocalCodexCredentialStore(credentialPath)
			require.NoError(t, err)
			saved, err := reopened.Load(ctx)
			require.NoError(t, err)
			wantCredential := rotated
			if tc.firstFailure != "" {
				wantCredential = old
			}
			require.Equal(t, wantCredential.AccessToken, saved.AccessToken)
			require.Equal(t, wantCredential.RefreshToken, saved.RefreshToken)
			require.Equal(t, wantCredential.AccountID, saved.AccountID)
			require.True(t, wantCredential.ExpiresAt.Equal(saved.ExpiresAt))
			rows, err := read.QueryContext(ctx, `SELECT body FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND kind='assistant' ORDER BY seq`, binding.AgentID, binding.SessionID, binding.Epoch)
			require.NoError(t, err)
			var assistants []ai.AssistantMessage
			for rows.Next() {
				var body []byte
				require.NoError(t, rows.Scan(&body))
				message, err := ai.UnmarshalMessage(body)
				require.NoError(t, err)
				assistants = append(assistants, message.(ai.AssistantMessage))
			}
			require.NoError(t, rows.Err())
			require.NoError(t, rows.Close())
			require.Len(t, assistants, 1, "discarded 401 must not persist an assistant or replay the SDK turn")
			if tc.second == 200 {
				require.Equal(t, ai.StopStop, assistants[0].StopReason)
				require.Equal(t, ai.ContentList{ai.TextContent{Text: "rotated answer"}}, assistants[0].Content)
			} else {
				require.NotEqual(t, ai.StopStop, assistants[0].StopReason, "provider error is not a successful turn")
			}
			var replies int
			require.NoError(t, read.QueryRowContext(ctx, `SELECT count(*) FROM embedded_reply_outbox o JOIN embedded_attempts a ON a.attempt_id=o.attempt_id WHERE a.agent_id=? AND a.session_id=? AND a.epoch=? AND a.generation=?`, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation).Scan(&replies))
			require.Zero(t, replies, "provider retry or partial response must not create a sender-bound mail reply")
			current, err := db.ManagedAgent(ctx, binding.AgentID)
			require.NoError(t, err)
			if tc.second != 200 {
				require.Equal(t, "failed", current.LifecycleState, "uncertain or rejected worker turn must terminate durably")
			}
			raw, err := os.ReadFile(dbPath)
			require.NoError(t, err)
			for _, secret := range []string{old.AccessToken, old.RefreshToken, rotated.AccessToken, rotated.RefreshToken} {
				require.NotContains(t, string(raw), secret)
				require.NotContains(t, current.LastErrorMessage, secret)
				for _, assistant := range assistants {
					require.NotContains(t, assistant.ErrorMessage, secret)
				}
			}
		})
	}
}

func TestEmbeddedCodexPreOutput401RotatesBeforeSDKPersistence(t *testing.T) {
	for _, tc := range []struct {
		name         string
		second       int
		firstFailure string
	}{
		{"rotated success", 200, ""}, {"second 403", 403, ""}, {"second 401", 401, ""}, {"transport unknown", 0, "transport"}, {"connection unknown", 0, "connection"}, {"partial SSE unknown", 0, "partial"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			base, err := os.MkdirTemp("/srv/disk-tmp", "embedded-codex-401-*")
			require.NoError(t, err)
			t.Cleanup(func() { _ = os.RemoveAll(base) })
			root := filepath.Join(base, "project")
			require.NoError(t, os.Mkdir(root, 0700))
			dbPath := filepath.Join(base, "router.db")
			db, err := store.Open(ctx, dbPath, store.Options{})
			require.NoError(t, err)
			defer db.Close()
			credentialPath := filepath.Join(base, "private", "credential.json")
			credentials, err := store.OpenLocalCodexCredentialStore(credentialPath)
			require.NoError(t, err)
			old := store.CodexCredential{AccessToken: "old-fake-bearer-401", RefreshToken: "old-fake-refresh-401", ExpiresAt: time.Now().Add(time.Hour), AccountID: "fixture-account"}
			rotated := store.CodexCredential{AccessToken: "new-fake-bearer-401", RefreshToken: "new-fake-refresh-401", ExpiresAt: time.Now().Add(2 * time.Hour), AccountID: "fixture-account"}
			require.NoError(t, credentials.Save(ctx, old))
			manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
			require.NoError(t, err)
			model := *ai.GetModel("openai-codex", "gpt-6-sol")
			doer := &codex401Doer{secondStatus: tc.second, firstFailure: tc.firstFailure}
			var traceMu sync.Mutex
			var traces []embeddedTransportTrace
			var refreshes atomic.Int32
			done := make(chan error, 1)
			var m *Manager
			m, err = NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-6-sol", Available: true}), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return &model }, CodexCredentialStore: credentials, CodexRefresh: func(_ context.Context, locked store.CodexCredential) (store.CodexCredential, error) {
				refreshes.Add(1)
				require.Equal(t, old.AccessToken, locked.AccessToken, "refresh must compare the actual sent credential under Store.Update")
				return rotated, nil
			}, CodexHTTPTransport: codexTestTransport{doer}, EmbeddedRunnerFactory: func(runCtx context.Context, saved store.ManagedAgentRecord) error {
				err := m.runEmbeddedSession(runCtx, saved)
				done <- err
				return err
			}})
			require.NoError(t, err)
			m.embeddedStatusObserver = func(trace embeddedTransportTrace) {
				traceMu.Lock()
				defer traceMu.Unlock()
				traces = append(traces, trace)
			}
			probePath := setupPrivateStatusProbeRetryTest(t, m, base, tc.second, tc.firstFailure)
			defer m.Close(ctx)
			launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "retry-401-root", Cwd: root, Runtime: "embedded", InitialInstruction: "say done", RequestID: "a5555555-5555-4555-8555-555555555595"})
			require.NoError(t, err)
			select {
			case <-done:
			case <-time.After(8 * time.Second):
				t.Fatal("SDK turn timed out")
			}
			traceMu.Lock()
			gotTraces := append([]embeddedTransportTrace(nil), traces...)
			traceMu.Unlock()
			verifyPrivateStatusProbeRetryTest(t, probePath)
			if tc.firstFailure == "" {
				require.Equal(t, []string{"Bearer " + old.AccessToken, "Bearer " + rotated.AccessToken}, doer.seen(), "exactly two sends, each with its own credential")
				require.EqualValues(t, 1, refreshes.Load())
				require.Equal(t, []embeddedTransportTrace{{Ordinal: 1, Status: 401}, {Ordinal: 2, Status: tc.second}}, gotTraces)
			} else {
				require.Equal(t, []string{"Bearer " + old.AccessToken}, doer.seen(), "unknown outcome must not retry")
				require.Zero(t, refreshes.Load())
				want := embeddedTransportTrace{Ordinal: 1, Status: 200}
				if tc.firstFailure == "transport" {
					want = embeddedTransportTrace{Ordinal: 1, Category: "other"}
				}
				if tc.firstFailure == "connection" {
					want = embeddedTransportTrace{Ordinal: 1, Category: "connection"}
				}
				require.Equal(t, []embeddedTransportTrace{want}, gotTraces)
			}
			encoded, err := json.Marshal(gotTraces)
			require.NoError(t, err)
			for _, secret := range []string{old.AccessToken, old.RefreshToken, rotated.AccessToken, rotated.RefreshToken, old.AccountID, "offline.invalid", "Authorization", "request", "error"} {
				require.NotContains(t, string(encoded), secret)
			}
			reopened, err := store.OpenLocalCodexCredentialStore(credentialPath)
			require.NoError(t, err)
			saved, err := reopened.Load(ctx)
			require.NoError(t, err)
			want := rotated
			if tc.firstFailure != "" {
				want = old
			}
			require.Equal(t, want.AccessToken, saved.AccessToken)
			require.Equal(t, want.RefreshToken, saved.RefreshToken)
			require.Equal(t, want.AccountID, saved.AccountID)
			require.True(t, want.ExpiresAt.Equal(saved.ExpiresAt))
			binding := store.EmbeddedBinding{AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
			read, err := sql.Open("sqlite", dbPath)
			require.NoError(t, err)
			defer read.Close()
			rows, err := read.QueryContext(ctx, `SELECT body FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND kind='assistant' ORDER BY seq`, binding.AgentID, binding.SessionID, binding.Epoch)
			require.NoError(t, err)
			var assistants []ai.AssistantMessage
			for rows.Next() {
				var body []byte
				require.NoError(t, rows.Scan(&body))
				message, err := ai.UnmarshalMessage(body)
				require.NoError(t, err)
				assistants = append(assistants, message.(ai.AssistantMessage))
			}
			require.NoError(t, rows.Err())
			require.NoError(t, rows.Close())
			require.Len(t, assistants, 1, "discarded 401 must never reach SDK persistence")
			if tc.second == 200 {
				require.Equal(t, ai.StopStop, assistants[0].StopReason)
				require.Equal(t, ai.ContentList{ai.TextContent{Text: "rotated answer"}}, assistants[0].Content)
			} else {
				require.NotEqual(t, ai.StopStop, assistants[0].StopReason)
			}
			raw, err := os.ReadFile(dbPath)
			require.NoError(t, err)
			for _, secret := range []string{old.AccessToken, old.RefreshToken, rotated.AccessToken, rotated.RefreshToken} {
				require.NotContains(t, string(raw), secret)
			}
		})
	}
}
