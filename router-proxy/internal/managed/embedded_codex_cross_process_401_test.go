package managed

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

const crossProcessOld = "cross-process-fake-old-bearer"
const crossProcessNew = "cross-process-fake-new-bearer"
const crossProcessTest = "TestEmbeddedCodexCrossProcessSameOldBearer401"

func crossProcessCredential(newToken bool) store.CodexCredential {
	if newToken {
		return store.CodexCredential{AccessToken: crossProcessNew, RefreshToken: "cross-process-fake-new-refresh", AccountID: "cross-process-new-account", ExpiresAt: time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)}
	}
	return store.CodexCredential{AccessToken: crossProcessOld, RefreshToken: "cross-process-fake-old-refresh", AccountID: "cross-process-old-account", ExpiresAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}
}

type crossProcessRequest struct{ marker, bearer, account string }

// Each subprocess has its own Manager and Store handle. Only the credential
// path and the parent's loopback fake are shared; no in-process Manager mutex.
func TestEmbeddedCodexCrossProcessSameOldBearer401(t *testing.T) {
	if marker := os.Getenv("G4_CROSS_PROCESS_CHILD"); marker != "" {
		crossProcessChild(t, marker)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	base, err := os.MkdirTemp("/srv/disk-tmp", "g4-cross-process-401-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	credentialPath := filepath.Join(base, "private", "credential.json")
	credentials, err := store.OpenLocalCodexCredentialStore(credentialPath)
	require.NoError(t, err)
	require.NoError(t, credentials.Save(ctx, crossProcessCredential(false)))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var mu sync.Mutex
	var requests []crossProcessRequest
	var refreshes atomic.Int32
	both := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/refresh" && r.Method == http.MethodPost {
			refreshes.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(crossProcessCredential(true))
			return
		}
		if r.URL.Path != "/codex/responses" || r.Method != http.MethodPost {
			http.Error(w, "unexpected endpoint", http.StatusBadRequest)
			return
		}
		body, e := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if e != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		marker := ""
		for _, candidate := range []string{"cross-alpha", "cross-beta"} {
			if strings.Contains(string(body), candidate) {
				marker = candidate
				break
			}
		}
		record := crossProcessRequest{marker, r.Header.Get("Authorization"), r.Header.Get("ChatGPT-Account-ID")}
		mu.Lock()
		requests = append(requests, record)
		if len(requests) == 2 && requests[0].bearer == "Bearer "+crossProcessOld && requests[1].bearer == "Bearer "+crossProcessOld {
			close(both)
		}
		mu.Unlock()
		if record.bearer == "Bearer "+crossProcessOld {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if record.bearer != "Bearer "+crossProcessNew || marker == "" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		answer := "answer for " + marker
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"cross_fake\"}}\n\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"cross_item\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\ndata: {\"type\":\"response.content_part.added\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"\"}}\n\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":%q}\n\ndata: {\"type\":\"response.output_text.done\",\"output_index\":0,\"content_index\":0,\"text\":%q}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"cross_fake\",\"status\":\"completed\"}}\n\n", answer, answer)
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 3 * time.Second}
	serveDone := make(chan struct{})
	go func() { defer close(serveDone); _ = server.Serve(listener) }()
	defer func() { unblock(); _ = server.Close(); <-serveDone }()
	endpoint := "http://" + listener.Addr().String()
	type childResult struct {
		marker string
		err    error
		output string
	}
	results := make(chan childResult, 2)
	started, reaped := 0, 0
	defer func() {
		cancel()
		unblock()
		for reaped < started {
			<-results // CommandContext kills, Wait reaps, even on assertion failure.
			reaped++
		}
	}()
	for _, marker := range []string{"cross-alpha", "cross-beta"} {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+crossProcessTest+"$", "-test.v")
		cmd.Env = append(os.Environ(), "G4_CROSS_PROCESS_CHILD="+marker, "G4_CROSS_PROCESS_BASE="+base, "G4_CROSS_PROCESS_ENDPOINT="+endpoint)
		var output bytes.Buffer
		cmd.Stdout = &output
		cmd.Stderr = &output
		require.NoError(t, cmd.Start())
		started++
		go func(marker string) { e := cmd.Wait(); results <- childResult{marker, e, output.String()} }(marker)
	}
	select {
	case <-both:
	case <-ctx.Done():
		t.Fatal("two old-bearer requests never reached parent barrier")
	}
	mu.Lock()
	initial := append([]crossProcessRequest(nil), requests...)
	mu.Unlock()
	require.Len(t, initial, 2)
	require.NotEqual(t, initial[0].marker, initial[1].marker)
	for _, r := range initial {
		require.Contains(t, []string{"cross-alpha", "cross-beta"}, r.marker)
		require.Equal(t, "Bearer "+crossProcessOld, r.bearer)
		require.Equal(t, "cross-process-old-account", r.account)
	}
	require.Zero(t, refreshes.Load(), "barrier must precede any 401 or refresh")
	unblock()
	for i := 0; i < 2; i++ {
		select {
		case result := <-results:
			reaped++
			if result.err != nil {
				output := result.output
				for _, secret := range []string{crossProcessOld, crossProcessNew, crossProcessCredential(false).RefreshToken, crossProcessCredential(true).RefreshToken} {
					output = strings.ReplaceAll(output, secret, "[redacted]")
				}
				t.Errorf("child %s failed: %v\n%s", result.marker, result.err, output)
			}
		case <-ctx.Done():
			t.Fatal("child timeout; CommandContext kills processes")
		}
	}
	if t.Failed() {
		return
	}
	mu.Lock()
	observed := append([]crossProcessRequest(nil), requests...)
	mu.Unlock()
	require.Len(t, observed, 4, "exactly old then new per process")
	for _, marker := range []string{"cross-alpha", "cross-beta"} {
		var seen []crossProcessRequest
		for _, r := range observed {
			if r.marker == marker {
				seen = append(seen, r)
			}
		}
		require.Equal(t, []crossProcessRequest{{marker, "Bearer " + crossProcessOld, "cross-process-old-account"}, {marker, "Bearer " + crossProcessNew, "cross-process-new-account"}}, seen)
	}
	require.EqualValues(t, 1, refreshes.Load(), "one actual refresh under the file lock")
	reopened, err := store.OpenLocalCodexCredentialStore(credentialPath)
	require.NoError(t, err)
	saved, err := reopened.Load(ctx)
	require.NoError(t, err)
	require.Equal(t, crossProcessCredential(true), saved)
	for _, path := range []string{credentialPath, credentialPath + ".lock"} {
		info, e := os.Lstat(path)
		require.NoError(t, e)
		require.True(t, info.Mode().IsRegular())
		require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	}
}

// Refuse SDK fallback to a real issuer even if its model BaseURL is ignored.
type crossProcessDoer struct {
	endpoint string
	client   *http.Client
}

func (d crossProcessDoer) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.String() != embeddedCodexResponsesURL {
		return nil, fmt.Errorf("unexpected provider destination")
	}
	copy := r.Clone(r.Context())
	copy.URL, _ = url.Parse(d.endpoint + "/codex/responses")
	copy.Host = copy.URL.Host
	return http.DefaultTransport.RoundTrip(copy)
}

func crossProcessChild(t *testing.T, marker string) {
	require.Contains(t, []string{"cross-alpha", "cross-beta"}, marker)
	base := os.Getenv("G4_CROSS_PROCESS_BASE")
	endpoint := os.Getenv("G4_CROSS_PROCESS_ENDPOINT")
	require.True(t, strings.HasPrefix(base, "/srv/disk-tmp/g4-cross-process-401-"))
	require.True(t, strings.HasPrefix(endpoint, "http://127.0.0.1:"))
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	root := filepath.Join(base, marker, "project")
	require.NoError(t, os.MkdirAll(root, 0700))
	db, err := store.Open(ctx, filepath.Join(base, marker, "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	credentials, err := store.OpenLocalCodexCredentialStore(filepath.Join(base, "private", "credential.json"))
	require.NoError(t, err)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	model := *ai.GetModel("openai-codex", "gpt-6-sol")
	transport := &http.Client{Timeout: 15 * time.Second}
	done := make(chan error, 1)
	var m *Manager
	m, err = NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(base, marker, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-6-sol", Available: true}), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return &model }, CodexCredentialStore: credentials, CodexHTTPTransport: crossProcessDoer{endpoint, transport}, CodexRefresh: func(ctx context.Context, locked store.CodexCredential) (store.CodexCredential, error) {
		if locked != crossProcessCredential(false) {
			return store.CodexCredential{}, fmt.Errorf("unexpected locked credential")
		}
		req, e := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/refresh", nil)
		if e != nil {
			return store.CodexCredential{}, e
		}
		response, e := transport.Do(req)
		if e != nil {
			return store.CodexCredential{}, e
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			return store.CodexCredential{}, fmt.Errorf("refresh status %d", response.StatusCode)
		}
		var next store.CodexCredential
		e = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&next)
		return next, e
	}, EmbeddedRunnerFactory: func(runCtx context.Context, saved store.ManagedAgentRecord) error {
		e := m.runEmbeddedSession(runCtx, saved)
		done <- e
		return e
	}})
	require.NoError(t, err)
	defer func() {
		closeCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		_ = m.Close(closeCtx)
	}()
	launched, err := m.Launch(ctx, LaunchRequest{Name: marker, Cwd: root, Runtime: "embedded", InitialInstruction: "say " + marker, RequestID: map[string]string{"cross-alpha": "a5555555-5555-4555-8555-555555555591", "cross-beta": "a5555555-5555-4555-8555-555555555592"}[marker]})
	require.NoError(t, err)
	select {
	case e := <-done:
		require.NoError(t, e)
	case <-ctx.Done():
		t.Fatal("worker timed out")
	}
	binding := store.EmbeddedBinding{AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
	role, err := db.SavedEmbeddedSessionRole(ctx, binding)
	require.NoError(t, err)
	require.Equal(t, "worker", role)
	read, err := sql.Open("sqlite", filepath.Join(base, marker, "router.db"))
	require.NoError(t, err)
	defer read.Close()
	var count int
	require.NoError(t, read.QueryRowContext(ctx, "SELECT count(*) FROM managed_agents").Scan(&count))
	require.Equal(t, 1, count)
	rows, err := read.QueryContext(ctx, "SELECT body FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND kind='assistant' ORDER BY seq", binding.AgentID, binding.SessionID, binding.Epoch)
	require.NoError(t, err)
	var assistants []ai.AssistantMessage
	for rows.Next() {
		var body []byte
		require.NoError(t, rows.Scan(&body))
		msg, e := ai.UnmarshalMessage(body)
		require.NoError(t, e)
		assistants = append(assistants, msg.(ai.AssistantMessage))
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Len(t, assistants, 1)
	require.Equal(t, ai.StopStop, assistants[0].StopReason)
	require.Equal(t, ai.ContentList{ai.TextContent{Text: "answer for " + marker}}, assistants[0].Content)
}
