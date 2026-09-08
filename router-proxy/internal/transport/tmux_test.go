package transport_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
	"github.com/stretchr/testify/require"
)

func TestTmuxSourceRejectsManagedNameCollision(t *testing.T) {
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	owner, err := durable.ReserveTmuxRecipientSource(context.Background(), "collision", "tmux-source-v1:collision")
	require.NoError(t, err)
	require.NoError(t, durable.ActivateTmuxRecipientSource(context.Background(), "collision", owner))
	err = durable.ReserveManagedLaunch(context.Background(), store.ManagedAgentRecord{AgentID: "collision-agent", Name: "collision", SlotHeld: true, Cwd: "/tmp", SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: strings.Repeat("a", 64)}, store.ManagedOperation{OperationID: "collision-op", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "collision-op", CanonicalBodyHash: "body", State: "pending", ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)})
	var conflict *store.RecipientSourceConflictError
	require.ErrorAs(t, err, &conflict)
}

type recordedTmux struct {
	mu                  sync.Mutex
	sessions            []string
	windows             []transport.TmuxWindow
	calls               []string
	block               chan struct{}
	started             chan struct{}
	provisions          int
	secondProvision     chan struct{}
	windowsErr          error
	provisionErr        error
	keysErr             error
	provisionSessionErr error
}

func (tmux *recordedTmux) Sessions(context.Context) ([]string, error) {
	return tmux.sessions, nil
}

func (tmux *recordedTmux) ProvisionSession(_ context.Context, session, name string) error {
	tmux.record("provision-session:" + session + ":" + name)
	return tmux.provisionSessionErr
}

func (tmux *recordedTmux) Windows(_ context.Context, session string) ([]transport.TmuxWindow, error) {
	tmux.record("windows:" + session)
	return tmux.windows, tmux.windowsErr
}

func (tmux *recordedTmux) Provision(_ context.Context, session, name string) error {
	tmux.mu.Lock()
	tmux.provisions++
	if tmux.provisions > 1 && tmux.secondProvision != nil {
		tmux.secondProvision <- struct{}{}
	}
	tmux.mu.Unlock()
	if tmux.block != nil {
		if tmux.started != nil {
			tmux.started <- struct{}{}
		}
		<-tmux.block
	}
	tmux.record("provision:" + session + ":" + name)
	return tmux.provisionErr
}

func (tmux *recordedTmux) Remove(_ context.Context, session, name string) error {
	tmux.record("remove:" + session + ":" + name)
	return nil
}

func (tmux *recordedTmux) Rename(_ context.Context, session, name, nextName string) error {
	tmux.record("rename:" + session + ":" + name + ":" + nextName)
	return nil
}

func (tmux *recordedTmux) SendKeys(_ context.Context, session, name, keys, _ string) error {
	tmux.record("keys:" + session + ":" + name + ":" + keys)
	return tmux.keysErr
}

func (tmux *recordedTmux) Start(_ context.Context, session, name, sessionID string) (string, string, error) {
	tmux.record("start:" + session + ":" + name + ":" + sessionID)
	return name, "started", nil
}

func (tmux *recordedTmux) Stop(_ context.Context, session, name, sessionID string) (string, string, error) {
	tmux.record("stop:" + session + ":" + name + ":" + sessionID)
	return name, "stopped", nil
}

func (tmux *recordedTmux) Restart(_ context.Context, session, name, sessionID string) (string, string, error) {
	tmux.record("restart:" + session + ":" + name + ":" + sessionID)
	return name, "started", nil
}

func (tmux *recordedTmux) Resume(_ context.Context, session, name, sessionID string) (string, string, error) {
	tmux.record("resume:" + session + ":" + name + ":" + sessionID)
	return name, "resumed", nil
}

func (tmux *recordedTmux) record(call string) {
	tmux.mu.Lock()
	defer tmux.mu.Unlock()
	tmux.calls = append(tmux.calls, call)
}

func (tmux *recordedTmux) Calls() []string {
	tmux.mu.Lock()
	defer tmux.mu.Unlock()
	return append([]string(nil), tmux.calls...)
}

func TestTmuxRoutesRestoreWorkerLifecycleContract(t *testing.T) {
	tmux := &recordedTmux{sessions: []string{"other", "mecha"}, windows: []transport.TmuxWindow{{Name: "supervisor", Target: "mecha:supervisor"}}}
	server := newTmuxServer(t, tmux)

	response := tmuxRequest(t, server.URL, http.MethodGet, "", "")
	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{"session":"mecha","windows":[{"name":"supervisor","target":"mecha:supervisor"}]}`, response.Body.String())

	response = tmuxRequest(t, server.URL, http.MethodPost, "", `{"name":"worker","session_id":"pi-session"}`)
	require.Equal(t, http.StatusCreated, response.Code)
	require.JSONEq(t, `{"session":{"name":"worker","selected":true},"session_id":"pi-session","selected_session":"mecha"}`, response.Body.String())

	response = tmuxRequest(t, server.URL, http.MethodPost, "/worker/keys", `{"keys":"pi --session-id pi-session"}`)
	require.Equal(t, http.StatusOK, response.Code)
	for _, action := range []string{"start", "stop", "restart", "resume"} {
		response = tmuxRequest(t, server.URL, http.MethodPost, "/worker/"+action, `{}`)
		require.Equal(t, http.StatusOK, response.Code, action)
	}
	response = tmuxRequest(t, server.URL, http.MethodPatch, "/worker", `{"name":"renamed"}`)
	require.Equal(t, http.StatusOK, response.Code)
	response = tmuxRequest(t, server.URL, http.MethodPost, "/renamed/rename", `{"name":"final"}`)
	require.Equal(t, http.StatusOK, response.Code)
	response = tmuxRequest(t, server.URL, http.MethodDelete, "/final", "")
	require.Equal(t, http.StatusNoContent, response.Code)

	require.Equal(t, []string{
		"windows:mecha",
		"provision:mecha:worker",
		"keys:mecha:worker:pi --session-id pi-session",
		"start:mecha:worker:pi-session",
		"stop:mecha:worker:pi-session",
		"restart:mecha:worker:pi-session",
		"resume:mecha:worker:pi-session",
		"rename:mecha:worker:renamed",
		"keys:mecha:renamed:{\"type\":\"tmux_window_renamed\",\"old_name\":\"worker\",\"new_name\":\"renamed\"}",
		"rename:mecha:renamed:final",
		"keys:mecha:final:{\"type\":\"tmux_window_renamed\",\"old_name\":\"renamed\",\"new_name\":\"final\"}",
		"remove:mecha:final",
	}, tmux.Calls())
}

func TestTmuxRoutesCoalesceConcurrentProvision(t *testing.T) {
	tmux := &recordedTmux{sessions: []string{"mecha"}, block: make(chan struct{}), started: make(chan struct{}, 1), secondProvision: make(chan struct{}, 1)}
	server := newTmuxServer(t, tmux)
	responses := make(chan *http.Response, 2)
	errors := make(chan error, 2)
	launch := func(sessionID string) {
		body := `{"name":"worker","session_id":"` + sessionID + `"}`
		response, err := http.Post(server.URL+"/v1/tmux/windows", "application/json", strings.NewReader(body))
		if err != nil {
			errors <- err
			return
		}
		responses <- response
	}
	go launch("pi-session-first")
	<-tmux.started
	go launch("pi-session-second")
	select {
	case <-tmux.secondProvision:
		t.Fatal("duplicate provision reached the tmux adapter")
	case <-time.After(100 * time.Millisecond):
	}
	close(tmux.block)
	for range 2 {
		select {
		case err := <-errors:
			require.NoError(t, err)
		case response := <-responses:
			require.Equal(t, http.StatusCreated, response.StatusCode)
			var payload map[string]any
			require.NoError(t, json.NewDecoder(response.Body).Decode(&payload))
			response.Body.Close()
			require.Equal(t, "pi-session-first", payload["session_id"])
		}
	}
	require.Equal(t, []string{"provision:mecha:worker"}, tmux.Calls())
}

func TestTmuxRoutesMapAdapterFailuresAndRecoverProvision(t *testing.T) {
	tmux := &recordedTmux{sessions: []string{"mecha"}, windowsErr: transport.ErrTmuxUnavailable}
	server := newTmuxServer(t, tmux)

	response := tmuxRequest(t, server.URL, http.MethodGet, "", "")
	require.Equal(t, http.StatusNotFound, response.Code)
	require.Contains(t, response.Body.String(), `"tmux_unavailable"`)
	tmux.windowsErr = errors.New("tmux command failed")
	response = tmuxRequest(t, server.URL, http.MethodGet, "", "")
	require.Equal(t, http.StatusInternalServerError, response.Code)
	require.Contains(t, response.Body.String(), `"tmux_operation_failed"`)

	tmux.provisionErr = errors.New("create failed")
	response = tmuxRequest(t, server.URL, http.MethodPost, "", `{"name":"worker","session_id":"pi-session"}`)
	require.Equal(t, http.StatusInternalServerError, response.Code)
	tmux.provisionErr = nil
	response = tmuxRequest(t, server.URL, http.MethodPost, "", `{"name":"worker","session_id":"pi-session"}`)
	require.Equal(t, http.StatusCreated, response.Code)

	tmux.keysErr = errors.New("send failed")
	response = tmuxRequest(t, server.URL, http.MethodPost, "/worker/keys", `{"keys":"start"}`)
	require.Equal(t, http.StatusInternalServerError, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), `"tmux_operation_failed"`)
	require.Equal(t, []string{"windows:mecha", "windows:mecha", "provision:mecha:worker", "provision:mecha:worker", "keys:mecha:worker:start"}, tmux.Calls())
}

func TestTmuxRoutesProvisionTheInitialManagedSession(t *testing.T) {
	tmux := &recordedTmux{}
	server := newTmuxServer(t, tmux)
	response := tmuxRequest(t, server.URL, http.MethodGet, "", "")
	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{"session":"mecha","windows":null}`, response.Body.String())
	require.Equal(t, []string{"provision-session:mecha:supervisor", "windows:mecha"}, tmux.Calls())
}

func TestTmuxRoutesRejectUnavailableAndInvalidNames(t *testing.T) {
	unavailable := newTmuxServer(t, &recordedTmux{provisionSessionErr: transport.ErrTmuxUnavailable})
	response := tmuxRequest(t, unavailable.URL, http.MethodGet, "", "")
	require.Equal(t, http.StatusNotFound, response.Code)
	require.Contains(t, response.Body.String(), `"tmux_unavailable"`)

	tmux := &recordedTmux{sessions: []string{"mecha"}}
	server := newTmuxServer(t, tmux)
	response = tmuxRequest(t, server.URL, http.MethodDelete, "/%2F", "")
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Contains(t, response.Body.String(), `"tmux_invalid_name"`)
	require.Empty(t, tmux.Calls())
}

func newTmuxServer(t *testing.T, tmux transport.TmuxAdapter) *httptest.Server {
	t.Helper()
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.sqlite"), store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, durable.Close()) })
	router, err := transport.New(transport.Options{Store: durable, Tmux: tmux})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server
}

func tmuxRequest(t *testing.T, base, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request, err := http.NewRequest(method, base+"/v1/tmux/windows"+path, strings.NewReader(body))
	require.NoError(t, err)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	recorder := httptest.NewRecorder()
	recorder.Code = response.StatusCode
	_, err = recorder.Body.ReadFrom(response.Body)
	require.NoError(t, err)
	return recorder
}
