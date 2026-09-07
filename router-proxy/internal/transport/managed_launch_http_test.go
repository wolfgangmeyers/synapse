package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

type managedTestProcess struct {
	identity managed.ProcessIdentity
	stopped  chan struct{}
	once     sync.Once
}

func (p *managedTestProcess) Identity() managed.ProcessIdentity { return p.identity }
func (p *managedTestProcess) Stdin() io.WriteCloser             { return managedTestWriteCloser{} }
func (p *managedTestProcess) Stdout() io.ReadCloser             { return io.NopCloser(strings.NewReader("")) }
func (p *managedTestProcess) Wait() error                       { <-p.stopped; return nil }
func (p *managedTestProcess) Stop(context.Context) error {
	p.once.Do(func() { close(p.stopped) })
	return nil
}

type managedTestWriteCloser struct{}

func (managedTestWriteCloser) Write(value []byte) (int, error) { return len(value), nil }
func (managedTestWriteCloser) Close() error                    { return nil }

type managedTestProcessFactory struct {
	process *managedTestProcess
	session *string
}

func (f managedTestProcessFactory) Start(_ context.Context, spec managed.CommandSpec) (managed.Process, error) {
	*f.session = spec.SessionID
	return f.process, nil
}

type managedTestRPC struct {
	session *string
	active  []string
}

func (r managedTestRPC) GetState(context.Context) (map[string]any, error) {
	return map[string]any{"sessionId": *r.session}, nil
}
func (r managedTestRPC) GetCommands(context.Context) ([]string, error) {
	return []string{"mecha-bootstrap-v1", "mecha-install-tools-v1", "mecha-control-result-v1", "mecha-mailbox-deliver-v1", "mecha-policy-apply-v1", "mecha-child-snapshot-v1", "goal", "goal-lock", "goal-snooze", "goal-snooze-turn-limit", "goal-snooze-budget", "goal-snooze-budget-refill", "mecha-parent-message", "inline-tools", "mecha-reply"}, nil
}
func (r managedTestRPC) Bootstrap(context.Context, map[string]any) error { return nil }
func (r managedTestRPC) WaitForReceipt(context.Context, string, string, int, int64, string) error {
	return nil
}
func (r managedTestRPC) GetActiveTools(context.Context) ([]string, error) { return r.active, nil }
func (r managedTestRPC) Prompt(context.Context, string) error             { return nil }
func (r managedTestRPC) Close() error                                     { return nil }

type managedTestRPCFactory struct{ rpc managedTestRPC }

func (f managedTestRPCFactory) Open(context.Context, managed.Process) (managed.RPC, error) {
	return f.rpc, nil
}

func TestManagedLaunchReplayHasOneReservation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	names := make([]string, 0, len(manifest.Tools)+3)
	names = append(names, "subagent", "get_subagent_result", "steer_subagent")
	for _, tool := range manifest.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	process := &managedTestProcess{identity: managed.ProcessIdentity{PID: 4321, StartMarker: "start", Incarnation: "incarnation"}, stopped: make(chan struct{})}
	sessionID := ""
	manager, err := managed.NewManager(managed.ManagerOptions{Store: durable, Config: managed.Config{AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest, ProcessFactory: managedTestProcessFactory{process: process, session: &sessionID}, RPCFactory: managedTestRPCFactory{rpc: managedTestRPC{session: &sessionID, active: names}}, CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai", ID: "gpt-5.6-terra", Available: true})})
	require.NoError(t, err)
	router, err := New(Options{Store: durable, Managed: manager})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	defer func() { server.Close(); _ = router.Close(ctx); _ = durable.Close() }()
	bootstrapReq, err := http.NewRequest(http.MethodPost, server.URL+"/v1/ui-session", bytes.NewBufferString(`{}`))
	require.NoError(t, err)
	bootstrapReq.Header.Set("Content-Type", "application/json")
	bootstrapResp, err := http.DefaultClient.Do(bootstrapReq)
	require.NoError(t, err)
	defer bootstrapResp.Body.Close()
	cookie := bootstrapResp.Cookies()[0]
	var bootstrap struct {
		CSRF string `json:"csrf_token"`
	}
	require.NoError(t, json.NewDecoder(bootstrapResp.Body).Decode(&bootstrap))
	body := `{"name":"alpha","cwd":"` + root + `","initial_instruction":"hello"}`
	requestID := "123e4567-e89b-42d3-a456-426614174081"
	send := func(payload string) *http.Response {
		request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/managed-agents", strings.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Request-ID", requestID)
		request.Header.Set("X-Mecha-CSRF", bootstrap.CSRF)
		request.AddCookie(cookie)
		response, requestErr := http.DefaultClient.Do(request)
		require.NoError(t, requestErr)
		return response
	}
	first := send(body)
	firstBytes, err := io.ReadAll(first.Body)
	require.NoError(t, err)
	first.Body.Close()
	require.Equal(t, http.StatusAccepted, first.StatusCode)
	replay := send(body)
	replayBytes, err := io.ReadAll(replay.Body)
	require.NoError(t, err)
	replay.Body.Close()
	require.Equal(t, http.StatusAccepted, replay.StatusCode)
	require.Equal(t, firstBytes, replayBytes)
	conflict := send(`{"name":"alpha","cwd":"` + root + `","initial_instruction":"different"}`)
	defer conflict.Body.Close()
	require.Equal(t, http.StatusConflict, conflict.StatusCode)
	agent, err := durable.ManagedAgent(ctx, "alpha")
	require.NoError(t, err)
	require.Equal(t, "running", agent.LifecycleState)
}
