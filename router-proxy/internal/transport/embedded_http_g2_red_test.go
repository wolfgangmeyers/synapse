package transport

import (
	"context"
	"database/sql"
	"encoding/json"
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

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedHTTPDefaultOffPreservesRPCLaunch(t *testing.T) {
	f := newEmbeddedHTTPG2Fixture(t)
	status, body := f.call(http.MethodPost, "/v1/managed-agents", "d1111111-1111-4111-8111-111111111111", fmt.Sprintf(`{"name":"stock-root","cwd":%q}`, f.root))
	require.Equal(t, http.StatusAccepted, status, string(body))
	saved, err := f.db.ManagedAgent(context.Background(), "stock-root")
	require.NoError(t, err)
	require.Equal(t, "rpc", saved.Runtime)
	f.assertRefresh("stock-root", "rpc")
	promptStatus, _ := f.call(http.MethodPost, "/v1/managed-agents/stock-root/prompt", "d2222222-2222-4222-8222-222222222222", `{"message":"not an embedded root"}`)
	require.Equal(t, http.StatusConflict, promptStatus)
	require.Zero(t, f.calls.Load())
}

// The existing WebUI sends the bare launch payload. The isolated router's
// opt-in must select embedded without requiring a browser payload change.
func TestEmbeddedHTTPUnchangedGlobalLaunchOptInRED(t *testing.T) {
	f := newEmbeddedHTTPG2FixtureWithDefault(t, true)
	id := "a1111111-1111-4111-8111-111111111111"
	status, body := f.call(http.MethodPost, "/v1/managed-agents", id, fmt.Sprintf(`{"name":"web-root","cwd":%q}`, f.root))
	require.Equal(t, http.StatusAccepted, status, string(body))
	var launch struct {
		OperationID string         `json:"operation_id"`
		Agent       map[string]any `json:"agent"`
	}
	require.NoError(t, json.Unmarshal(body, &launch))
	require.Equal(t, id, launch.OperationID)
	require.Equal(t, "router-managed", launch.Agent["source"])
	saved, err := f.db.ManagedAgent(context.Background(), "web-root")
	require.NoError(t, err)
	require.Equal(t, "embedded", saved.Runtime, "isolated router opt-in must work with unchanged WebUI payload")
	f.assertRefresh("web-root", "embedded")
	status, body = f.call(http.MethodPost, "/v1/managed-agents", "a5555555-5555-4555-8555-555555555555", fmt.Sprintf(`{"name":"api-rpc","cwd":%q,"runtime":"rpc"}`, f.root))
	require.Equal(t, http.StatusAccepted, status, string(body))
	explicit, err := f.db.ManagedAgent(context.Background(), "api-rpc")
	require.NoError(t, err)
	require.Equal(t, "rpc", explicit.Runtime, "explicit API choice overrides the router default")
}

func TestEmbeddedHTTPExplicitRuntimeAndPromptRequestIDRED(t *testing.T) {
	f := newEmbeddedHTTPG2Fixture(t)
	ctx := context.Background()
	id := "a2222222-2222-4222-8222-222222222222"
	body := fmt.Sprintf(`{"name":"api-root","cwd":%q,"runtime":"embedded"}`, f.root)
	status, response := f.call(http.MethodPost, "/v1/managed-agents", id, body)
	require.Equal(t, http.StatusAccepted, status, string(response))
	saved, err := f.db.ManagedAgent(ctx, "api-root")
	require.NoError(t, err)
	require.Equal(t, "embedded", saved.Runtime)
	require.Eventually(t, func() bool {
		row, e := f.db.ManagedAgent(ctx, "api-root")
		return e == nil && row.LifecycleState == "running"
	}, 3*time.Second, 10*time.Millisecond)
	f.assertRefresh("api-root", "embedded")
	sameStatus, same := f.call(http.MethodPost, "/v1/managed-agents", id, body)
	require.Equal(t, status, sameStatus)
	require.JSONEq(t, string(response), string(same))
	conflictStatus, conflict := f.call(http.MethodPost, "/v1/managed-agents", id, fmt.Sprintf(`{"name":"api-root","cwd":%q,"runtime":"rpc"}`, f.root))
	require.Equal(t, http.StatusConflict, conflictStatus, string(conflict))
	invalidStatus, invalid := f.call(http.MethodPost, "/v1/managed-agents", "a3333333-3333-4333-8333-333333333333", fmt.Sprintf(`{"name":"invalid","cwd":%q,"runtime":"anything"}`, f.root))
	require.Equal(t, http.StatusUnprocessableEntity, invalidStatus, string(invalid))
	_, err = f.db.ManagedAgent(ctx, "invalid")
	require.ErrorIs(t, err, sql.ErrNoRows)

	path := "/v1/managed-agents/api-root/prompt"
	turnID := "a4444444-4444-4444-8444-444444444444"
	prompt := `{"message":"first HTTP turn"}`
	promptStatus, promptResponse := f.call(http.MethodPost, path, turnID, prompt)
	require.Equal(t, http.StatusOK, promptStatus, string(promptResponse))
	require.EqualValues(t, 1, f.calls.Load())
	replayStatus, replay := f.call(http.MethodPost, path, turnID, prompt)
	require.Equal(t, promptStatus, replayStatus)
	require.Equal(t, promptResponse, replay, "same ID returns identical status and body")
	require.EqualValues(t, 1, f.calls.Load(), "replay cannot call provider twice")
	conflictStatus, conflict = f.call(http.MethodPost, path, turnID, `{"message":"different"}`)
	require.Equal(t, http.StatusConflict, conflictStatus, string(conflict))
	require.EqualValues(t, 1, f.calls.Load())
	f.assertRefresh("api-root", "embedded")
}

func TestEmbeddedHTTPPromptRouteAdmissionRED(t *testing.T) {
	f := newEmbeddedHTTPG2Fixture(t)
	ctx := context.Background()
	launched, err := f.manager.LaunchEmbeddedRoot(ctx, managed.LaunchRequest{Name: "api-root", Cwd: f.root, Runtime: "embedded", RequestID: "c1111111-1111-4111-8111-111111111111"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		row, e := f.db.ManagedAgent(ctx, launched.Agent.AgentID)
		return e == nil && row.LifecycleState == "running"
	}, 3*time.Second, 10*time.Millisecond)
	path := "/v1/managed-agents/api-root/prompt"
	id := "c2222222-2222-4222-8222-222222222222"
	status, result := f.call(http.MethodPost, path, id, `{"message":"first HTTP turn"}`)
	require.Equal(t, http.StatusOK, status, string(result))
	require.EqualValues(t, 1, f.calls.Load(), "checked user append precedes the faux provider")
	replayStatus, replay := f.call(http.MethodPost, path, id, `{"message":"first HTTP turn"}`)
	require.Equal(t, status, replayStatus)
	require.Equal(t, result, replay)
	require.EqualValues(t, 1, f.calls.Load())
	conflictStatus, conflict := f.call(http.MethodPost, path, id, `{"message":"different"}`)
	require.Equal(t, http.StatusConflict, conflictStatus, string(conflict))
	require.EqualValues(t, 1, f.calls.Load())
	wrongStatus, _ := f.call(http.MethodPost, "/v1/managed-agents/not-api-root/prompt", "c3333333-3333-4333-8333-333333333333", `{"message":"first HTTP turn"}`)
	require.Equal(t, http.StatusNotFound, wrongStatus)
	invalidStatus, _ := f.call(http.MethodPost, path, "c4444444-4444-4444-8444-444444444444", `{"message":"first HTTP turn","agent_id":"api-root"}`)
	require.Equal(t, http.StatusBadRequest, invalidStatus)
	require.EqualValues(t, 1, f.calls.Load())
	f.assertRefresh("api-root", "embedded")
}

// Distinct Manager/Store reopening is already covered at the Manager seam.
// HTTP must forward the same durable ID and reject ambiguous pending turns.
func TestEmbeddedHTTPPromptPendingFailsClosedRED(t *testing.T) {
	f := newEmbeddedHTTPG2Fixture(t)
	ctx := context.Background()
	launched, err := f.manager.LaunchEmbeddedRoot(ctx, managed.LaunchRequest{Name: "pending-root", Cwd: f.root, Runtime: "embedded", RequestID: "b1111111-1111-4111-8111-111111111111"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		row, e := f.db.ManagedAgent(ctx, launched.Agent.AgentID)
		return e == nil && row.LifecycleState == "running"
	}, 3*time.Second, 10*time.Millisecond)
	path := "/v1/managed-agents/pending-root/prompt"
	id := "b2222222-2222-4222-8222-222222222222"
	// Reserve a request whose user append failed. HTTP retries cannot guess that
	// the provider did or didn't run, even once the injected fault is removed.
	fault, err := sql.Open("sqlite", f.dbPath)
	require.NoError(t, err)
	_, err = fault.Exec(`CREATE TRIGGER http_g2_user_fault BEFORE INSERT ON embedded_entries WHEN NEW.kind='user' BEGIN SELECT RAISE(ABORT,'http g2 user append fault'); END`)
	require.NoError(t, err)
	require.Error(t, f.manager.AdmitPromptWithRequestID(ctx, launched.Agent.AgentID, "ambiguous", id))
	_, err = fault.Exec(`DROP TRIGGER http_g2_user_fault`)
	require.NoError(t, err)
	require.NoError(t, fault.Close())
	status, body := f.call(http.MethodPost, path, id, `{"message":"ambiguous"}`)
	require.Equal(t, http.StatusConflict, status, string(body))
	require.Zero(t, f.calls.Load())
}

type embeddedHTTPG2Fixture struct {
	root    string
	dbPath  string
	db      *store.Store
	manager *managed.Manager
	server  *httptest.Server
	calls   atomic.Int32
	model   *ai.Model
	router  *Router
	// Only the cold-owner fixture opts into one durably fenced idle wake.
	coldMailRouter   *Router
	coldMailOldOwner int64
	neutralCalls     atomic.Int32
}

func newEmbeddedHTTPG2Fixture(t *testing.T) *embeddedHTTPG2Fixture {
	return newEmbeddedHTTPG2FixtureWithDefault(t, false)
}

func newEmbeddedHTTPG2FixtureWithDefault(t *testing.T, embeddedDefault bool) *embeddedHTTPG2Fixture {
	t.Helper()
	f := &embeddedHTTPG2Fixture{root: t.TempDir()}
	require.NoError(t, os.Chmod(f.root, 0700))
	ctx := context.Background()
	var err error
	f.dbPath = filepath.Join(t.TempDir(), "router.db")
	opts := store.Options{}
	if opts.Migrations == nil {
		if err := store.CloneTemplateDB(t, f.dbPath); err == nil {
			opts.Migrations = []store.Migration{}
		}
	}
	f.db, err = store.Open(ctx, f.dbPath, opts)
	require.NoError(t, err)
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	t.Cleanup(reg.Unregister)
	answer := func(history ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		row, e := f.db.ManagedAgent(ctx, "api-root")
		if e != nil {
			row, e = f.db.ManagedAgent(ctx, "mail-root")
			if e != nil {
				panic(e)
			}
		}
		check, e := sql.Open("sqlite", f.dbPath)
		if e != nil {
			panic(e)
		}
		defer check.Close()
		var kind string
		var body []byte
		e = check.QueryRowContext(ctx, `SELECT kind,body FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? ORDER BY seq DESC LIMIT 1`, row.AgentID, row.PiSessionID, row.SessionEpoch, row.RuntimeGeneration).Scan(&kind, &body)
		neutral, marshalErr := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "The mailbox router was restarted. Your session was restored from saved context. Continue working from your saved context."}}})
		if e == nil && marshalErr == nil && kind == "user" && string(body) == string(neutral) && row.AgentID == "mail-root" && f.coldMailRouter != nil {
			var scoped int
			e = check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_sessions s JOIN embedded_restart_wake_slots w ON w.agent_id=s.agent_id AND w.session_id=s.session_id AND w.epoch=s.epoch AND w.runtime_generation=s.generation AND w.claim_owner_token=s.owner_token JOIN embedded_entries u ON u.agent_id=s.agent_id AND u.session_id=s.session_id AND u.epoch=s.epoch AND u.generation=s.generation JOIN embedded_nonmail_provider_entries p ON p.agent_id=u.agent_id AND p.session_id=u.session_id AND p.epoch=u.epoch AND p.generation=u.generation AND p.user_seq=u.seq AND p.user_digest=u.digest JOIN embedded_entries h ON h.agent_id=u.agent_id AND h.session_id=u.session_id AND h.epoch=u.epoch AND h.generation=u.generation AND h.seq=u.seq-1 JOIN embedded_process_generations g ON g.generation=w.process_generation WHERE s.current=1 AND s.agent_id='mail-root' AND s.owner_token>=? AND w.slot='current' AND w.process_generation=? AND g.incarnation_key=? AND w.wake_id='idle:mail-root:' || s.session_id || ':' || s.epoch || ':' || w.process_generation AND w.kind='root-idle' AND w.state='claimed' AND w.notice_id IS NULL AND w.digest=h.digest AND u.kind='user' AND u.body=? AND u.seq=(SELECT max(seq) FROM embedded_entries WHERE agent_id=s.agent_id) AND NOT EXISTS (SELECT 1 FROM embedded_acks k WHERE k.agent_id=u.agent_id AND k.session_id=u.session_id AND k.epoch=u.epoch AND k.generation=u.generation AND k.entry_seq=u.seq)`, f.coldMailOldOwner, f.coldMailRouter.embeddedProcessGeneration, func() string {
				var key string
				if err := check.QueryRowContext(ctx, "SELECT incarnation_key FROM embedded_process_generations WHERE generation=?", f.coldMailRouter.embeddedProcessGeneration).Scan(&key); err != nil {
					panic("missing Store-owned process generation")
				}
				return key
			}(), neutral).Scan(&scoped)
			if e != nil || scoped != 1 || f.neutralCalls.Add(1) != 1 {
				panic("cold mail neutral requires exactly one distinct fenced idle wake, not a mail ACK")
			}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "neutral recovery complete"}}, ai.StopStop)
		}
		var state string
		if row.AgentID == "mail-root" && e == nil {
			e = check.QueryRowContext(ctx, `SELECT state FROM messages WHERE recipient='mail-root' ORDER BY enqueue_sequence DESC LIMIT 1`).Scan(&state)
		}
		_ = check.Close()
		expected := "first HTTP turn"
		if row.AgentID == "mail-root" {
			expected = "ordered mail turn"
		}
		if e != nil || kind != "user" || !strings.Contains(string(body), expected) || (row.AgentID == "mail-root" && state != "acknowledged") {
			panic(fmt.Sprintf("provider before checked user append: %v kind=%s body=%s", e, kind, body))
		}
		f.calls.Add(1) // Mail/HTTP attribution excludes the separately proved neutral wake.
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "answered"}}, ai.StopStop)
	}
	reg.SetResponses([]providers.FauxResponseStep{answer, answer})
	f.model = reg.GetModel()
	f.manager, err = managed.NewManager(managed.ManagerOptions{Store: f.db, Manifest: manifest, Config: managed.Config{SessionRoot: filepath.Join(f.root, "sessions"), AllowedWorkingDirectoryRoots: []string{f.root}}, CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-sol", Available: true}), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return f.model }, CodexCredentialStore: testfixture.CodexAdmission(t)})
	require.NoError(t, err)
	router, err := New(Options{Store: f.db, Managed: f.manager, DefaultEmbeddedRoot: embeddedDefault})
	require.NoError(t, err)
	f.router = router
	f.server = httptest.NewServer(router)
	t.Cleanup(func() { f.server.Close(); _ = router.Close(ctx); _ = f.db.Close() })
	return f
}

func (f *embeddedHTTPG2Fixture) call(method, path, id, body string) (int, []byte) {
	req, err := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		panic(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if id != "" {
		req.Header.Set("X-Request-ID", id)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		panic(err)
	}
	return response.StatusCode, payload
}

func (f *embeddedHTTPG2Fixture) assertRefresh(name, runtime string) {
	for _, path := range []string{"/v1/managed-agents", "/v1/managed-agents/" + name} {
		status, raw := f.call(http.MethodGet, path, "", "")
		if status != http.StatusOK {
			panic(fmt.Sprintf("refresh %s: %d %s", path, status, raw))
		}
		var projection map[string]any
		if err := json.Unmarshal(raw, &projection); err != nil {
			panic(err)
		}
		if projection["projection_version"] == nil {
			panic("refresh lacks projection version")
		}
		var agent map[string]any
		if path == "/v1/managed-agents" {
			agents, ok := projection["agents"].([]any)
			if !ok || len(agents) != 1 {
				panic(fmt.Sprintf("list projection: %s", raw))
			}
			agent, _ = agents[0].(map[string]any)
		} else {
			agent, _ = projection["agent"].(map[string]any)
		}
		if agent == nil || agent["source"] != "router-managed" || agent["model"] == nil || agent["runtime"] != runtime || agent["lifecycle_state"] == nil {
			panic(fmt.Sprintf("refresh projection: %s", raw))
		}
	}
	row, err := f.db.ManagedAgent(context.Background(), name)
	if err != nil || row.Runtime != runtime {
		panic("refresh runtime differs from saved choice")
	}
}
