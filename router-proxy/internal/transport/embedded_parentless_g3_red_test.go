package transport

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func newParentlessHTTPG3Fixture(t *testing.T) *embeddedHTTPG2Fixture {
	t.Helper()
	f := &embeddedHTTPG2Fixture{root: t.TempDir()}
	info, statErr := os.Stat(f.root)
	require.NoError(t, statErr)
	t.Logf("Cwd fixture source=embedded_parentless_g3_red_test.go:t.TempDir path=%s before_mode=%#o", f.root, info.Mode().Perm())
	// Only this test-owned Cwd, never shared TMPDIR or a production root.
	require.NoError(t, os.Chmod(f.root, 0700))
	info, statErr = os.Stat(f.root)
	require.NoError(t, statErr)
	require.Equal(t, os.FileMode(0700), info.Mode().Perm())
	t.Logf("Cwd fixture path=%s after_mode=%#o", f.root, info.Mode().Perm())
	ctx := context.Background()
	f.dbPath = filepath.Join(t.TempDir(), "router.db")
	var err error
	f.db, err = store.Open(ctx, f.dbPath, store.Options{})
	require.NoError(t, err)
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	t.Cleanup(reg.Unregister)
	answer := func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
		f.calls.Add(1)
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "answered"}}, ai.StopStop)
	}
	steps := make([]providers.FauxResponseStep, 16)
	for i := range steps {
		steps[i] = answer
	}
	reg.SetResponses(steps)
	f.model = reg.GetModel()
	f.manager, err = managed.NewManager(managed.ManagerOptions{Store: f.db, Manifest: manifest, Config: managed.Config{SessionRoot: filepath.Join(f.root, "sessions"), AllowedWorkingDirectoryRoots: []string{f.root}}, CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return f.model }, CodexCredentialStore: testfixture.CodexAdmission(t)})
	require.NoError(t, err)
	f.router, err = New(Options{Store: f.db, Managed: f.manager, DefaultEmbeddedRoot: true})
	require.NoError(t, err)
	f.server = httptest.NewServer(f.router)
	t.Cleanup(func() { f.server.Close(); _ = f.router.Close(ctx); _ = f.db.Close() })
	return f
}

// Read-only failure evidence for this scoped fixture batch; never claims leases.
func cwdPrivacyTransportSnapshot(t *testing.T, path string) {
	t.Helper()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Logf("failure snapshot open_error=%v", err)
		return
	}
	defer raw.Close()
	for _, query := range []string{
		`SELECT json_group_array(json_object('agent',agent_id,'lifecycle',lifecycle_state,'error_code',last_error_code,'error_message',last_error_message)) FROM managed_agents`,
		`SELECT json_group_array(json_object('operation',o.operation_id,'state',o.state,'status',o.response_status)) FROM managed_http_operations o JOIN managed_agents a ON a.launch_operation_id=o.operation_id`,
		`SELECT json_group_array(json_object('agent',s.agent_id,'session',s.session_id,'head',(SELECT max(e.seq) FROM embedded_entries e WHERE e.agent_id=s.agent_id AND e.session_id=s.session_id AND e.epoch=s.epoch),'owner',s.owner_id,'token',s.owner_token,'lease',s.owner_expires_at)) FROM embedded_sessions s WHERE s.current=1`,
		`SELECT json_group_array(json_object('process',generation)) FROM embedded_process_generation_counter`,
		`SELECT json_group_array(json_object('agent',agent_id,'slot',slot,'kind',kind,'state',state,'process',process_generation,'token',claim_owner_token)) FROM embedded_restart_wake_slots`,
	} {
		var snapshot string
		err := raw.QueryRow(query).Scan(&snapshot)
		t.Logf("failure snapshot query=%s result=%s read_error=%v", query, snapshot, err)
	}
}

// The unchanged WebUI payload has no parent field. Two opt-in launches must
// bind independent worker sessions, not two root sessions.
func TestEmbeddedParentlessHTTPDualWorkerRED(t *testing.T) {
	f := newParentlessHTTPG3Fixture(t)
	defer func() {
		if t.Failed() {
			t.Logf("failure snapshot providers=%d owner_published=unmeasured(private Manager map)", f.calls.Load())
			cwdPrivacyTransportSnapshot(t, f.dbPath)
		}
	}()
	ctx := context.Background()
	names := []string{"parentless-one", "parentless-two"}
	ids := []string{"b3111111-1111-4111-8111-111111111111", "b3222222-2222-4222-8222-222222222222"}
	for i, name := range names {
		status, body := f.call(http.MethodPost, "/v1/managed-agents", ids[i], fmt.Sprintf(`{"name":%q,"cwd":%q,"model":{"provider":"openai-codex","id":"gpt-5.6-terra"},"initial_instruction":%q}`, name, f.root, "independent instruction for "+name))
		require.Equal(t, http.StatusAccepted, status, string(body))
	}
	require.Eventually(t, func() bool {
		for _, name := range names {
			row, err := f.db.ManagedAgent(ctx, name)
			if err != nil || row.LifecycleState != "running" {
				return false
			}
		}
		return true
	}, 3*time.Second, 10*time.Millisecond, "both HTTP launches must become ready before testing roles")
	check, err := sql.Open("sqlite", f.dbPath)
	require.NoError(t, err)
	defer check.Close()
	for _, name := range names {
		var role string
		require.NoError(t, check.QueryRowContext(ctx, `SELECT role FROM embedded_sessions WHERE agent_id=? AND current=1`, name).Scan(&role), "missing durable role is a setup failure")
		require.Equal(t, "worker", role, "parentless HTTP launch must be a durable worker, not a root: %s", name)
	}
	first, err := f.db.ManagedAgent(ctx, names[0])
	require.NoError(t, err)
	second, err := f.db.ManagedAgent(ctx, names[1])
	require.NoError(t, err)
	require.Equal(t, "embedded", first.Runtime)
	require.Equal(t, "embedded", second.Runtime)
	require.NotEqual(t, first.AgentID, second.AgentID)
	require.NotEqual(t, first.PiSessionID, second.PiSessionID)
	require.NotEqual(t, first.RuntimeGeneration, second.RuntimeGeneration)
	require.Equal(t, "independent instruction for "+names[0], first.PendingInitialInstruction)
	require.Equal(t, "independent instruction for "+names[1], second.PendingInitialInstruction)
	require.Equal(t, "gpt-5.6-terra", first.SelectedModel.ID)
	require.Equal(t, first.SelectedModel.ID, second.SelectedModel.ID)
	// Addressed mail must be consumed and replied to by only its own worker.
	for i, name := range names {
		sender := fmt.Sprintf("g3-sender-%d", i)
		id := fmt.Sprintf("b4%06d-2222-4222-8222-222222222222", i+1)
		payload := fmt.Sprintf(`{"id":%q,"from":%q,"to":%q,"body":%q,"created_at":"2026-09-27T00:00:00.000Z"}`, id, sender, name, "addressed mail for "+name)
		status, raw := f.call(http.MethodPost, "/v1/messages", "", payload)
		require.Equal(t, http.StatusCreated, status, string(raw))
		require.Eventually(t, func() bool {
			message, e := f.db.MessageFor(ctx, id)
			if e != nil || message.State != domain.MessageAcknowledged {
				return false
			}
			history, e := f.db.ConversationHistory(ctx, sender, name)
			return e == nil && len(history) == 2 && history[1].From == name && history[1].To == sender && history[1].Body == "answered"
		}, 3*time.Second, 10*time.Millisecond)
		status, raw = f.call(http.MethodPost, "/v1/messages", "", payload)
		require.Equal(t, http.StatusOK, status, string(raw))
		status, _ = f.call(http.MethodPost, "/v1/messages", "", fmt.Sprintf(`{"id":%q,"from":%q,"to":%q,"body":"changed","created_at":"2026-09-27T00:00:00.000Z"}`, id, sender, name))
		require.Equal(t, http.StatusConflict, status)
		other := names[1-i]
		history, e := f.db.ConversationHistory(ctx, sender, other)
		require.NoError(t, e)
		require.Empty(t, history)
		status, raw = f.call(http.MethodGet, "/v1/conversations?owner="+sender+"&peer="+name, "", "")
		require.Equal(t, http.StatusOK, status)
		var view struct {
			Messages []json.RawMessage `json:"messages"`
		}
		require.NoError(t, json.Unmarshal(raw, &view))
		require.Len(t, view.Messages, 2)
	}
	require.EqualValues(t, 4, f.calls.Load(), "two initial prompts plus one mail turn per worker")
}
