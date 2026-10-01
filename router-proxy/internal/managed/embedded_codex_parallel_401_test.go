package managed

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
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

type parallel401Request struct{ marker, bearer, account string }
type parallel401Doer struct {
	mu        sync.Mutex
	requests  []parallel401Request
	firstBoth chan struct{}
	release   chan struct{}
	old, new  string
}

func (d *parallel401Doer) Do(r *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	marker := ""
	for _, candidate := range []string{"parallel-worker-alpha", "parallel-worker-beta"} {
		if strings.Contains(string(body), candidate) {
			marker = candidate
			break
		}
	}
	record := parallel401Request{marker, r.Header.Get("Authorization"), r.Header.Get("ChatGPT-Account-ID")}
	d.mu.Lock()
	d.requests = append(d.requests, record)
	if record.bearer == "Bearer "+d.old && len(d.requests) == 2 {
		close(d.firstBoth)
	}
	d.mu.Unlock()
	if record.bearer == "Bearer "+d.old {
		select {
		case <-d.release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header), Request: r}, nil
	}
	if record.bearer != "Bearer "+d.new {
		return nil, fmt.Errorf("unexpected fake bearer")
	}
	answer := "answer for " + marker
	payload := fmt.Sprintf("data: {\"type\":\"response.created\",\"response\":{\"id\":\"parallel_fake\"}}\n\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"parallel_item\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\ndata: {\"type\":\"response.content_part.added\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"\"}}\n\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":%q}\n\ndata: {\"type\":\"response.output_text.done\",\"output_index\":0,\"content_index\":0,\"text\":%q}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"parallel_fake\",\"status\":\"completed\"}}\n\n", answer, answer)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(payload)), Request: r}, nil
}
func (d *parallel401Doer) seen() []parallel401Request {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]parallel401Request(nil), d.requests...)
}

// Both parentless SDK turns send the same still-valid bearer before either can
// receive a 401. Only one Store.Update exchange may rotate that shared bearer.
func TestEmbeddedIndependentWorkersConcurrentPreOutput401SharedRotation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	base, err := os.MkdirTemp("/srv/disk-tmp", "embedded-parallel-401-*")
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
	old := store.CodexCredential{AccessToken: "parallel-old-fake-bearer", RefreshToken: "parallel-old-fake-refresh", ExpiresAt: time.Now().Add(time.Hour), AccountID: "parallel-old-account"}
	rotated := store.CodexCredential{AccessToken: "parallel-new-fake-bearer", RefreshToken: "parallel-new-fake-refresh", ExpiresAt: time.Now().Add(2 * time.Hour), AccountID: "parallel-new-account"}
	require.NoError(t, credentials.Save(ctx, old))
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	model := *ai.GetModel("openai-codex", "gpt-6-sol")
	doer := &parallel401Doer{firstBoth: make(chan struct{}), release: make(chan struct{}), old: old.AccessToken, new: rotated.AccessToken}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(doer.release) }) }
	defer release()
	var refreshes atomic.Int32
	var eventMu sync.Mutex
	var events []string
	runs := make(chan error, 2)
	var m *Manager
	m, err = NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-6-sol", Available: true}), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return &model }, CodexCredentialStore: credentials, CodexRefresh: func(_ context.Context, locked store.CodexCredential) (store.CodexCredential, error) {
		refreshes.Add(1)
		if locked.AccessToken != old.AccessToken {
			return store.CodexCredential{}, fmt.Errorf("exchange received unexpected bearer")
		}
		return rotated, nil
	}, CodexHTTPTransport: codexTestTransport{doer}, EventSink: func(e Event) { eventMu.Lock(); events = append(events, fmt.Sprintf("%+v", e)); eventMu.Unlock() }, EmbeddedRunnerFactory: func(runCtx context.Context, saved store.ManagedAgentRecord) error {
		e := m.runEmbeddedSession(runCtx, saved)
		runs <- e
		return e
	}})
	require.NoError(t, err)
	defer func() {
		release()
		cancel()
		closeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = m.Close(closeCtx)
	}()
	type launchResult struct {
		index int
		agent store.ManagedAgentRecord
		err   error
	}
	launches := make(chan launchResult, 2)
	markers := []string{"parallel-worker-alpha", "parallel-worker-beta"}
	for i := range markers {
		go func(i int) {
			launched, e := m.Launch(ctx, LaunchRequest{Name: markers[i], Cwd: root, Runtime: "embedded", InitialInstruction: "say " + markers[i], RequestID: []string{"a5555555-5555-4555-8555-555555555597", "a5555555-5555-4555-8555-555555555598"}[i]})
			if e != nil {
				launches <- launchResult{index: i, err: e}
				return
			}
			launches <- launchResult{index: i, agent: *launched.Agent}
		}(i)
	}
	select {
	case <-doer.firstBoth:
	case <-ctx.Done():
		t.Fatal("both old-bearer HTTP requests did not reach barrier", ctx.Err())
	}
	first := doer.seen()
	require.Len(t, first, 2)
	seenMarkers := map[string]bool{}
	for _, r := range first {
		require.Equal(t, "Bearer "+old.AccessToken, r.bearer)
		require.Equal(t, old.AccountID, r.account)
		require.Contains(t, markers, r.marker)
		require.False(t, seenMarkers[r.marker])
		seenMarkers[r.marker] = true
	}
	require.Zero(t, refreshes.Load(), "no 401 was returned before barrier release")
	release()
	agents := make([]store.ManagedAgentRecord, 2)
	for range markers {
		select {
		case result := <-launches:
			require.NoError(t, result.err)
			agents[result.index] = result.agent
		case <-ctx.Done():
			t.Fatal("launch timed out", ctx.Err())
		}
	}
	var runErrors []string
	for range markers {
		select {
		case e := <-runs:
			require.NoError(t, e)
			if e != nil {
				runErrors = append(runErrors, e.Error())
			}
		case <-ctx.Done():
			t.Fatal("runner timed out", ctx.Err())
		}
	}
	requests := doer.seen()
	require.Len(t, requests, 4, "two sends per worker, never a third")
	byMarker := map[string][]parallel401Request{}
	for _, r := range requests {
		byMarker[r.marker] = append(byMarker[r.marker], r)
	}
	for _, marker := range markers {
		require.Equal(t, []parallel401Request{{marker, "Bearer " + old.AccessToken, old.AccountID}, {marker, "Bearer " + rotated.AccessToken, rotated.AccountID}}, byMarker[marker])
	}
	require.EqualValues(t, 1, refreshes.Load(), "Store.Update must compare the bearer sent by each worker")
	reopened, err := store.OpenLocalCodexCredentialStore(credentialPath)
	require.NoError(t, err)
	saved, err := reopened.Load(ctx)
	require.NoError(t, err)
	require.Equal(t, rotated.AccessToken, saved.AccessToken)
	require.Equal(t, rotated.RefreshToken, saved.RefreshToken)
	require.Equal(t, rotated.AccountID, saved.AccountID)
	require.True(t, rotated.ExpiresAt.Equal(saved.ExpiresAt))
	read, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer read.Close()
	var count int
	require.NoError(t, read.QueryRowContext(ctx, "SELECT count(*) FROM managed_agents").Scan(&count))
	require.Equal(t, 2, count, "only the two independent workers exist")
	require.NotEqual(t, agents[0].AgentID, agents[1].AgentID)
	for i, agent := range agents {
		binding := store.EmbeddedBinding{AgentID: agent.AgentID, SessionID: agent.PiSessionID, Epoch: agent.SessionEpoch, Generation: agent.RuntimeGeneration}
		role, e := db.SavedEmbeddedSessionRole(ctx, binding)
		require.NoError(t, e)
		require.Equal(t, "worker", role)
		rows, e := read.QueryContext(ctx, "SELECT body FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND kind='assistant' ORDER BY seq", binding.AgentID, binding.SessionID, binding.Epoch)
		require.NoError(t, e)
		var assistants []ai.AssistantMessage
		for rows.Next() {
			var body []byte
			require.NoError(t, rows.Scan(&body))
			message, e := ai.UnmarshalMessage(body)
			require.NoError(t, e)
			assistants = append(assistants, message.(ai.AssistantMessage))
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
		require.Len(t, assistants, 1, "401 must not save an assistant")
		require.Equal(t, ai.StopStop, assistants[0].StopReason)
		require.Equal(t, ai.ContentList{ai.TextContent{Text: "answer for " + markers[i]}}, assistants[0].Content)
		require.NoError(t, read.QueryRowContext(ctx, "SELECT count(*) FROM embedded_reply_outbox o JOIN embedded_attempts a ON a.attempt_id=o.attempt_id WHERE a.agent_id=? AND a.session_id=? AND a.epoch=? AND a.generation=?", binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation).Scan(&count))
		require.Zero(t, count, "no sender-bound reply")
	}
	var readable strings.Builder
	rows, err := read.QueryContext(ctx, "SELECT body FROM embedded_entries")
	require.NoError(t, err)
	for rows.Next() {
		var body []byte
		require.NoError(t, rows.Scan(&body))
		readable.Write(body)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	for _, agent := range agents {
		current, e := db.ManagedAgent(ctx, agent.AgentID)
		require.NoError(t, e)
		readable.WriteString(current.LastErrorMessage)
	}
	raw, err := os.ReadFile(dbPath)
	require.NoError(t, err)
	eventMu.Lock()
	observedEvents := strings.Join(events, "\n")
	eventMu.Unlock()
	for _, secret := range []string{old.AccessToken, old.RefreshToken, rotated.AccessToken, rotated.RefreshToken} {
		for _, surface := range []string{readable.String(), string(raw), strings.Join(runErrors, "\n"), observedEvents} {
			require.NotContains(t, surface, secret)
		}
	}
}
