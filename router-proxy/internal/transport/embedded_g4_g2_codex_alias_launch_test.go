package transport

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

const aliasRoot = "alias-root"
const aliasChild = "alias-child-only"
const aliasCall = "call_alias_root|fc_alias_root"

// ALIAS_TRACE: this fixture is a closed HTTP doer, not a server or a fallback client.
type aliasLaunchDoer struct {
	mu       sync.Mutex
	phase    int
	problems []string
	project  string
}

func (d *aliasLaunchDoer) snapshot() (int, []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.phase, append([]string(nil), d.problems...)
}
func (d *aliasLaunchDoer) Do(r *http.Request) (*http.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	reject := func(category string) (*http.Response, error) {
		d.problems = append(d.problems, category)
		return nil, errors.New("ALIAS_TRACE closed fixture rejected request")
	}
	if r.Method != http.MethodPost || r.URL.String() != "https://chatgpt.com/backend-api/codex/responses" || r.Header.Get("Authorization") != "Bearer DUMMY-alias-access" || r.Header.Get("ChatGPT-Account-ID") != "DUMMY-alias-account" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "text/event-stream" {
		return reject("envelope")
	}
	var request struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
		Store  bool   `json:"store"`
		Tools  []struct {
			Type        string          `json:"type"`
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Strict      *bool           `json:"strict"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"tools"`
		Input []json.RawMessage `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		return reject("json")
	}
	if request.Model != "gpt-6-sol" || !request.Stream || request.Store || len(request.Input) == 0 || d.phase >= 3 {
		return reject("model-or-phase")
	}
	wantNames := []string{"mecha_mailbox_send", "goal", "goal_snooze", "mecha_context", "mecha_reminder", "bash", "read", "edit", "write", "mecha_worker"}
	if len(request.Tools) != len(wantNames) {
		return reject("tool-count")
	}
	for i, tool := range request.Tools {
		if tool.Name != wantNames[i] || tool.Type != "function" || tool.Description == "" || tool.Strict != nil || strings.Contains(tool.Name, ".") {
			return reject("tool-order-or-declaration")
		}
	}
	// Pinned independently of the received tool; ordinary tools use Codex strict:null.
	const pinnedLaunch = `{"type":"object","properties":{"action":{"type":"string","enum":["list","launch","send","cleanup"]},"name":{"type":"string","maxLength":128},"cwd":{"type":"string","maxLength":4096},"instruction":{"type":"string","maxLength":32768},"model":{"type":"object","properties":{"provider":{"type":"string","maxLength":128},"id":{"type":"string","maxLength":128}},"required":["provider","id"],"additionalProperties":false},"body":{"type":"string","maxLength":4096},"subject":{"type":"string","maxLength":256}},"required":["action"],"additionalProperties":false,"oneOf":[{"type":"object","properties":{"action":{"type":"string","enum":["list"]}},"required":["action"],"additionalProperties":false},{"type":"object","properties":{"action":{"type":"string","enum":["launch"]},"name":{"type":"string","maxLength":128},"cwd":{"type":"string","maxLength":4096},"instruction":{"type":"string","maxLength":32768},"model":{"type":"object","properties":{"provider":{"type":"string","maxLength":128},"id":{"type":"string","maxLength":128}},"required":["provider","id"],"additionalProperties":false}},"required":["action","name","cwd","instruction"],"additionalProperties":false},{"type":"object","properties":{"action":{"type":"string","enum":["send"]},"name":{"type":"string","maxLength":128},"body":{"type":"string","maxLength":4096},"subject":{"type":"string","maxLength":256}},"required":["action","name","body"],"additionalProperties":false},{"type":"object","properties":{"action":{"type":"string","enum":["cleanup"]},"name":{"type":"string","maxLength":128}},"required":["action","name"],"additionalProperties":false}]}`
	var gotSchema, wantSchema any
	if json.Unmarshal(request.Tools[9].Parameters, &gotSchema) != nil || json.Unmarshal([]byte(pinnedLaunch), &wantSchema) != nil || !reflect.DeepEqual(gotSchema, wantSchema) || request.Tools[9].Description != "List, launch, send mail to or clean up independent embedded workers by their saved identity. Launch and cleanup return accepted operations, not readiness or completion." {
		return reject("pinned-launch-schema-description")
	}
	inputs := make([]map[string]json.RawMessage, 0, len(request.Input))
	for _, item := range request.Input {
		var v map[string]json.RawMessage
		if json.Unmarshal(item, &v) != nil {
			return reject("input-json")
		}
		inputs = append(inputs, v)
	}
	var outputCount, callCount int
	for _, item := range inputs {
		var kind, call, name, id, args, output string
		_ = json.Unmarshal(item["type"], &kind)
		_ = json.Unmarshal(item["call_id"], &call)
		_ = json.Unmarshal(item["name"], &name)
		_ = json.Unmarshal(item["id"], &id)
		_ = json.Unmarshal(item["arguments"], &args)
		_ = json.Unmarshal(item["output"], &output)
		if kind == "function_call" && call == "call_alias_root" && id == "fc_alias_root" && name == "mecha_worker" {
			var v map[string]any
			if json.Unmarshal([]byte(args), &v) == nil && reflect.DeepEqual(v, map[string]any{"action": "launch", "name": aliasChild, "cwd": d.project, "instruction": "DUMMY child instruction", "model": map[string]any{"provider": "openai-codex", "id": "gpt-6-sol"}}) {
				callCount++
			}
		}
		if kind == "function_call_output" && call == "call_alias_root" {
			var v struct {
				Status      string `json:"status"`
				WorkerID    string `json:"worker_id"`
				OperationID string `json:"operation_id"`
			}
			if json.Unmarshal([]byte(output), &v) == nil && v.Status == "accepted" && v.WorkerID == aliasChild && v.OperationID != "" {
				outputCount++
			}
		}
	}
	if d.phase == 0 && (callCount != 0 || outputCount != 0 || !strings.Contains(string(request.Input[len(request.Input)-1]), "DUMMY root instruction")) {
		return reject("initial-input")
	}
	if d.phase == 1 && (callCount != 1 || outputCount != 1) {
		return reject("continuation-correlation")
	}
	if d.phase == 2 {
		if callCount != 1 || outputCount != 1 {
			return reject("restart-history-correlation")
		}
		var neutralCount int
		for _, item := range inputs {
			var role string
			_ = json.Unmarshal(item["role"], &role)
			if role != "user" {
				continue
			}
			var content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(item["content"], &content) != nil {
				return reject("restart-user-content")
			}
			if len(content) == 1 && content[0].Type == "input_text" && content[0].Text == "Continue working from your saved context." {
				neutralCount++
			}
		}
		var lastRole string
		_ = json.Unmarshal(inputs[len(inputs)-1]["role"], &lastRole)
		if neutralCount != 1 || lastRole != "user" {
			return reject("restart-neutral-prompt")
		}
	}
	phase := d.phase
	d.phase++
	fmt.Printf("ALIAS_TRACE phase=http-request category=%d call=%s item=%s\n", phase, "call_alias_root", "fc_alias_root")
	events := []string{fmt.Sprintf(`{"type":"response.created","response":{"id":"resp_alias_%d"}}`, phase)}
	if phase == 0 {
		args, _ := json.Marshal(map[string]any{"action": "launch", "name": aliasChild, "cwd": d.project, "instruction": "DUMMY child instruction", "model": map[string]any{"provider": "openai-codex", "id": "gpt-6-sol"}})
		events = append(events, `{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_alias_root","call_id":"call_alias_root","name":"mecha_worker"}}`, fmt.Sprintf(`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_alias_root","call_id":"call_alias_root","name":"mecha_worker","arguments":%q}}`, string(args)))
	} else {
		text := "DUMMY accepted final"
		if phase == 2 {
			text = "DUMMY neutral final"
		}
		events = append(events, `{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_alias_final"}}`, fmt.Sprintf(`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_alias_final","content":[{"type":"output_text","text":%q}]}}`, text))
	}
	events = append(events, fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_alias_%d","status":"completed"}}`, phase))
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + strings.Join(events, "\n\ndata: ") + "\n\n")), Request: r}, nil
}

// ALIAS_TRACE: source-only cross-component regression; never permit a real provider fallback.
func TestEmbeddedG4G2CodexAliasRootWorkerLaunch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	base, err := os.MkdirTemp("/srv/disk-tmp", "codex-alias-launch-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	require.NoError(t, os.Chmod(base, 0700))
	project := filepath.Join(base, "project")
	require.NoError(t, os.Mkdir(project, 0700))
	dbPath := filepath.Join(base, "router.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer func() {
		if db != nil {
			_ = db.Close()
		}
	}()
	credentialPath := filepath.Join(base, "private", "credential.json")
	credentials, err := store.OpenLocalCodexCredentialStore(credentialPath)
	require.NoError(t, err)
	require.NoError(t, credentials.Save(ctx, store.CodexCredential{AccessToken: "DUMMY-alias-access", RefreshToken: "DUMMY-alias-refresh", ExpiresAt: time.Now().Add(time.Hour), AccountID: "DUMMY-alias-account"}))
	info, err := os.Stat(credentialPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	model := ai.GetModel("openai-codex", "gpt-6-sol")
	require.NotNil(t, model)
	doer := &aliasLaunchDoer{project: project}
	manager, err := managed.NewManager(managed.ManagerOptions{Store: db, Manifest: mustEffectManifest(t), Config: managed.Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{project}}, CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-6-sol", Available: true}), EmbeddedModelResolver: func(row store.ManagedAgentRecord) *ai.Model {
		if row.AgentID == aliasRoot {
			return model
		}
		return nil
	}, CodexCredentialStore: credentials, CodexHTTPTransport: fixtureCodexTransport{doer}})
	require.NoError(t, err)
	defer func() {
		if manager != nil {
			closeCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
			defer stop()
			_ = manager.Close(closeCtx)
		}
	}()
	router, err := New(Options{Store: db, Managed: manager, Host: "127.0.0.1", Port: 0, DefaultEmbeddedRoot: true, StartupManaged: func(c context.Context, m *managed.Manager) error {
		_, e := m.LaunchEmbeddedRoot(c, managed.LaunchRequest{Name: aliasRoot, Cwd: project, Runtime: "embedded", Model: &managed.Model{Provider: "openai-codex", ID: "gpt-6-sol"}, InitialInstruction: "DUMMY root instruction", RequestID: "a7111111-1111-4111-8111-111111111111"})
		return e
	}})
	require.NoError(t, err)
	require.NoError(t, router.Listen(ctx))
	defer func() {
		if router != nil {
			closeCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
			defer stop()
			_ = router.Close(closeCtx)
		}
	}()
	ro, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer ro.Close()
	type savedEntry struct {
		seq                  int64
		parent, kind, digest string
		body                 []byte
	}
	var entries []savedEntry
	var session, generation, role, owner string
	var epoch, token int64
	require.Eventually(t, func() bool {
		phase, problems := doer.snapshot()
		if len(problems) > 0 || phase != 2 {
			return false
		}
		root, e := db.ManagedAgent(ctx, aliasRoot)
		if e != nil || root.LifecycleState != "running" {
			return false
		}
		child, e := db.ManagedAgent(ctx, aliasChild)
		if e != nil || child.Runtime != "embedded" || child.LaunchOperationID == "" {
			return false
		}
		if ro.QueryRowContext(ctx, `SELECT role,session_id,epoch,generation,owner_id,owner_token FROM embedded_sessions WHERE agent_id=? AND current=1`, aliasRoot).Scan(&role, &session, &epoch, &generation, &owner, &token) != nil {
			return false
		}
		rows, e := ro.QueryContext(ctx, `SELECT seq,parent_digest,kind,body,digest FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? ORDER BY seq`, aliasRoot, session, epoch, generation)
		if e != nil {
			return false
		}
		defer rows.Close()
		entries = nil
		for rows.Next() {
			var entry savedEntry
			if rows.Scan(&entry.seq, &entry.parent, &entry.kind, &entry.body, &entry.digest) != nil {
				return false
			}
			entries = append(entries, entry)
		}
		return rows.Err() == nil && len(entries) >= 4
	}, 5*time.Second, 10*time.Millisecond, "ALIAS_TRACE completion category=timeout")
	root, err := db.ManagedAgent(ctx, aliasRoot)
	require.NoError(t, err)
	require.Equal(t, "openai-codex", root.SelectedModel.Provider)
	require.Equal(t, "gpt-6-sol", root.SelectedModel.ID)
	require.Equal(t, "root", role)
	require.Equal(t, root.PiSessionID, session)
	require.Equal(t, root.SessionEpoch, epoch)
	require.Equal(t, root.RuntimeGeneration, generation)
	require.NotEmpty(t, owner)
	require.Positive(t, token)
	fmt.Printf("ALIAS_TRACE phase=selected-model category=root model=%s role=%s owner-token=%d session=%s epoch=%d generation=%s\n", root.SelectedModel.ID, role, token, session, epoch, generation)
	var assistantCall, result *savedEntry
	for i := range entries {
		e := &entries[i]
		require.EqualValues(t, i+1, e.seq)
		require.NotEmpty(t, e.digest)
		if i == 0 {
			require.Equal(t, "genesis", e.parent)
		} else {
			require.Equal(t, entries[i-1].digest, e.parent)
		}
		if e.kind == "assistant" {
			message, decode := ai.UnmarshalMessage(e.body)
			require.NoError(t, decode)
			a, ok := message.(ai.AssistantMessage)
			require.True(t, ok)
			for _, part := range a.Content {
				if call, ok := part.(ai.ToolCall); ok {
					require.Nil(t, assistantCall)
					require.Equal(t, aliasCall, call.ID)
					require.Equal(t, "mecha_worker", call.Name)
					require.Equal(t, map[string]any{"action": "launch", "name": aliasChild, "cwd": project, "instruction": "DUMMY child instruction", "model": map[string]any{"provider": "openai-codex", "id": "gpt-6-sol"}}, call.Arguments)
					assistantCall = e
				}
			}
		}
		if e.kind == "toolResult" {
			require.Nil(t, result)
			message, decode := ai.UnmarshalMessage(e.body)
			require.NoError(t, decode)
			tool, ok := message.(ai.ToolResultMessage)
			require.True(t, ok)
			require.Equal(t, aliasCall, tool.ToolCallID)
			require.Equal(t, "mecha_worker", tool.ToolName)
			require.False(t, tool.IsError)
			require.Len(t, tool.Content, 1)
			text, ok := tool.Content[0].(ai.TextContent)
			require.True(t, ok)
			var accepted struct {
				Status      string `json:"status"`
				WorkerID    string `json:"worker_id"`
				OperationID string `json:"operation_id"`
			}
			require.NoError(t, json.Unmarshal([]byte(text.Text), &accepted))
			require.Equal(t, "accepted", accepted.Status)
			require.Equal(t, aliasChild, accepted.WorkerID)
			child, lookupErr := db.ManagedAgent(ctx, aliasChild)
			require.NoError(t, lookupErr)
			require.Equal(t, child.LaunchOperationID, accepted.OperationID)
			result = e
		}
	}
	require.NotNil(t, assistantCall)
	require.NotNil(t, result)
	require.Equal(t, assistantCall.seq+1, result.seq)
	require.Equal(t, result.seq+1, entries[len(entries)-1].seq)
	require.Equal(t, "assistant", entries[len(entries)-1].kind)
	final, err := ai.UnmarshalMessage(entries[len(entries)-1].body)
	require.NoError(t, err)
	a, ok := final.(ai.AssistantMessage)
	require.True(t, ok)
	require.Equal(t, ai.StopStop, a.StopReason)
	require.Len(t, a.Content, 1)
	finalText, ok := a.Content[0].(ai.TextContent)
	require.True(t, ok)
	require.Equal(t, "DUMMY accepted final", finalText.Text)
	fmt.Printf("ALIAS_TRACE phase=checked-intent category=canonical call=%s seq=%d digest=%s parent=%s\n", aliasCall, assistantCall.seq, assistantCall.digest, assistantCall.parent)
	fmt.Printf("ALIAS_TRACE phase=worker-execute category=accepted worker=%s call=%s\n", aliasChild, aliasCall)
	fmt.Printf("ALIAS_TRACE phase=checked-store-result category=ordered-accepted call=%s seq=%d digest=%s parent=%s final-digest=%s\n", aliasCall, result.seq, result.digest, result.parent, entries[len(entries)-1].digest)
	var state string
	var intent, observed []byte
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT state,intent,result FROM embedded_effects WHERE effect_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, aliasCall, aliasRoot, session, epoch, generation).Scan(&state, &intent, &observed))
	// The addressed producer commits its checked acceptance result, not worker
	// readiness. Its observed effect must match the real ordered SDK result.
	require.Equal(t, "observed", state)
	require.JSONEq(t, string(result.body), string(observed))
	var original ai.ToolCall
	require.NoError(t, json.Unmarshal(intent, &original))
	require.Equal(t, aliasCall, original.ID)
	require.Equal(t, "mecha_worker", original.Name)
	require.Equal(t, map[string]any{"action": "launch", "name": aliasChild, "cwd": project, "instruction": "DUMMY child instruction", "model": map[string]any{"provider": "openai-codex", "id": "gpt-6-sol"}}, original.Arguments)
	var childRole string
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT role FROM embedded_sessions WHERE agent_id=? AND current=1`, aliasChild).Scan(&childRole))
	require.Equal(t, "worker", childRole)
	var checkpoints int
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT count(*) FROM embedded_checkpoints WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, aliasRoot, session, epoch, generation).Scan(&checkpoints))
	if checkpoints > 0 {
		var bad int
		require.NoError(t, ro.QueryRowContext(ctx, `SELECT count(*) FROM embedded_checkpoints c LEFT JOIN embedded_entries e ON e.agent_id=c.agent_id AND e.session_id=c.session_id AND e.epoch=c.epoch AND e.generation=c.generation AND e.seq=c.at_seq AND e.digest=c.ancestor_digest WHERE c.agent_id=? AND c.session_id=? AND c.epoch=? AND c.generation=? AND e.seq IS NULL`, aliasRoot, session, epoch, generation).Scan(&bad))
		require.Zero(t, bad)
	}
	phase, problems := doer.snapshot()
	require.Equal(t, 2, phase)
	require.Empty(t, problems)

	// Close every A component before opening B against the same private SQLite file.
	originalEntries := append([]savedEntry(nil), entries...)
	originalChild, err := db.ManagedAgent(ctx, aliasChild)
	require.NoError(t, err)
	originalGeneration := router.embeddedProcessGeneration
	require.NoError(t, router.Close(ctx))
	router = nil
	require.NoError(t, manager.Close(ctx))
	manager = nil
	require.NoError(t, db.Close())
	db = nil
	// A closed manager has relinquished its lease. Expire it explicitly to make
	// this a deterministic process takeover rather than a wall-clock lease test.
	_, err = ro.ExecContext(ctx, `UPDATE embedded_sessions SET owner_expires_at='2000-01-01T00:00:00.000Z' WHERE agent_id=? AND current=1`, aliasRoot)
	require.NoError(t, err)
	db, err = store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	manager, err = managed.NewManager(managed.ManagerOptions{Store: db, Manifest: mustEffectManifest(t), Config: managed.Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{project}}, CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-6-sol", Available: true}), EmbeddedModelResolver: func(row store.ManagedAgentRecord) *ai.Model {
		if row.AgentID == aliasRoot {
			return model
		}
		return nil
	}, CodexCredentialStore: credentials, CodexHTTPTransport: fixtureCodexTransport{doer}})
	require.NoError(t, err)
	router, err = New(Options{Store: db, Managed: manager, Host: "127.0.0.1", Port: 0, DefaultEmbeddedRoot: true, StartupManaged: func(context.Context, *managed.Manager) error { return nil }})
	require.NoError(t, err)
	require.NoError(t, router.Listen(ctx))
	// Recovery runs asynchronously after Listen. Observe only typed ownership and wake
	// metadata before waiting for the provider's third (neutral) request.
	boundary := "takeover-process-generation"
	checkTakeover := func() bool {
		if router.embeddedProcessGeneration == originalGeneration {
			boundary = "takeover-process-generation"
			return false
		}
		var savedOwner string
		var savedToken int64
		if ro.QueryRowContext(ctx, `SELECT owner_id,owner_token FROM embedded_sessions WHERE agent_id=? AND current=1`, aliasRoot).Scan(&savedOwner, &savedToken) != nil {
			boundary = "takeover-session-missing"
			return false
		}
		if savedOwner == owner || savedToken <= token {
			boundary = "takeover-owner-token"
			return false
		}
		var kind, wakeState string
		var wakeProcess int64
		var claimToken sql.NullInt64
		if ro.QueryRowContext(ctx, `SELECT kind,state,process_generation,claim_owner_token FROM embedded_restart_wake_slots WHERE agent_id=? AND slot='current'`, aliasRoot).Scan(&kind, &wakeState, &wakeProcess, &claimToken) != nil {
			boundary = "wake-not-scheduled"
			return false
		}
		if kind != "root-idle" || wakeProcess != router.embeddedProcessGeneration {
			boundary = "wake-kind-or-generation"
			return false
		}
		if wakeState != "claimed" && wakeState != "checked-complete" {
			boundary = "wake-not-claimed"
			return false
		}
		var claimCount int
		if ro.QueryRowContext(ctx, `SELECT count(*) FROM embedded_restart_wake_slots WHERE agent_id=? AND slot='current' AND kind='root-idle' AND process_generation=? AND state IN ('claimed','checked-complete') AND claim_owner_token=?`, aliasRoot, router.embeddedProcessGeneration, savedToken).Scan(&claimCount) != nil {
			boundary = "claim-count-unavailable"
			return false
		}
		if !claimToken.Valid || claimToken.Int64 != savedToken || claimCount != 1 {
			boundary = "claim-token-or-count"
			return false
		}
		boundary = "claim-observed"
		return true
	}
	deadline := time.Now().Add(5 * time.Second)
	for !checkTakeover() {
		if time.Now().After(deadline) || ctx.Err() != nil {
			p, rejected := doer.snapshot()
			t.Fatalf("ALIAS_TRACE cold-restart boundary=%s provider-accepted=%d provider-rejected=%d", boundary, p, len(rejected))
		}
		time.Sleep(10 * time.Millisecond)
	}
	completionDeadline := time.Now().Add(5 * time.Second)
	for {
		p, bad := doer.snapshot()
		var complete int
		e := ro.QueryRowContext(ctx, `SELECT count(*) FROM embedded_restart_wake_slots WHERE agent_id=? AND process_generation=? AND kind='root-idle' AND state='checked-complete'`, aliasRoot, router.embeddedProcessGeneration).Scan(&complete)
		if p == 3 && len(bad) == 0 && e == nil && complete == 1 {
			break
		}
		if time.Now().Before(completionDeadline) && ctx.Err() == nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		// Count only B's current, newly fenced root history; never inspect stored bodies.
		var userCount, intentCount int
		userErr := ro.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries e JOIN embedded_sessions s ON s.agent_id=e.agent_id AND s.session_id=e.session_id AND s.epoch=e.epoch AND s.generation=e.generation WHERE e.agent_id=? AND e.session_id=? AND e.epoch=? AND e.generation=? AND e.seq>? AND e.kind='user' AND s.current=1 AND s.owner_id<>? AND s.owner_token>?`, aliasRoot, session, epoch, generation, originalEntries[len(originalEntries)-1].seq, owner, token).Scan(&userCount)
		intentErr := ro.QueryRowContext(ctx, `SELECT count(*) FROM embedded_nonmail_provider_entries p JOIN embedded_entries e ON e.agent_id=p.agent_id AND e.session_id=p.session_id AND e.epoch=p.epoch AND e.generation=p.generation AND e.seq=p.user_seq AND e.digest=p.user_digest JOIN embedded_sessions s ON s.agent_id=e.agent_id AND s.session_id=e.session_id AND s.epoch=e.epoch AND s.generation=e.generation WHERE e.agent_id=? AND e.session_id=? AND e.epoch=? AND e.generation=? AND e.seq>? AND e.kind='user' AND s.current=1 AND s.owner_id<>? AND s.owner_token>?`, aliasRoot, session, epoch, generation, originalEntries[len(originalEntries)-1].seq, owner, token).Scan(&intentCount)
		if userErr != nil {
			userCount = -1
		}
		if intentErr != nil {
			intentCount = -1
		}
		accepted, rejected := doer.snapshot()
		counts := make(map[string]int)
		for _, category := range rejected {
			counts[category]++
		}
		t.Fatalf("ALIAS_TRACE cold-restart category=completion-timeout b-checked-user=%d b-provider-intent=%d provider-accepted=%d provider-rejected=%d reject-envelope=%d reject-json=%d reject-model-or-phase=%d reject-tool-count=%d reject-tool-order-or-declaration=%d reject-pinned-launch-schema-description=%d reject-input-json=%d reject-initial-input=%d reject-continuation-correlation=%d reject-restart-history-correlation=%d reject-restart-user-content=%d reject-restart-neutral-prompt=%d", userCount, intentCount, accepted, len(rejected), counts["envelope"], counts["json"], counts["model-or-phase"], counts["tool-count"], counts["tool-order-or-declaration"], counts["pinned-launch-schema-description"], counts["input-json"], counts["initial-input"], counts["continuation-correlation"], counts["restart-history-correlation"], counts["restart-user-content"], counts["restart-neutral-prompt"])
	}
	recovered, err := db.ManagedAgent(ctx, aliasRoot)
	require.NoError(t, err)
	require.Equal(t, root.PiSessionID, recovered.PiSessionID)
	require.Equal(t, root.SessionEpoch, recovered.SessionEpoch)
	require.Equal(t, root.RuntimeGeneration, recovered.RuntimeGeneration)
	child, err := db.ManagedAgent(ctx, aliasChild)
	require.NoError(t, err)
	require.Equal(t, originalChild.PiSessionID, child.PiSessionID)
	require.Equal(t, originalChild.SessionEpoch, child.SessionEpoch)
	require.Equal(t, originalChild.RuntimeGeneration, child.RuntimeGeneration)
	require.Equal(t, originalChild.LaunchOperationID, child.LaunchOperationID)
	var newRole, newSession, newGeneration, newOwner string
	var newEpoch, newToken int64
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT role,session_id,epoch,generation,owner_id,owner_token FROM embedded_sessions WHERE agent_id=? AND current=1`, aliasRoot).Scan(&newRole, &newSession, &newEpoch, &newGeneration, &newOwner, &newToken))
	require.Equal(t, role, newRole)
	require.Equal(t, session, newSession)
	require.Equal(t, epoch, newEpoch)
	require.Equal(t, generation, newGeneration)
	require.NotEqual(t, owner, newOwner)
	require.Greater(t, newToken, token)
	rows, err := ro.QueryContext(ctx, `SELECT seq,parent_digest,kind,body,digest FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? ORDER BY seq`, aliasRoot, session, epoch, generation)
	require.NoError(t, err)
	var all []savedEntry
	for rows.Next() {
		var entry savedEntry
		require.NoError(t, rows.Scan(&entry.seq, &entry.parent, &entry.kind, &entry.body, &entry.digest))
		all = append(all, entry)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Len(t, all, len(originalEntries)+2)
	require.Equal(t, originalEntries, all[:len(originalEntries)], "A history must remain byte-for-byte immutable")
	for i := len(originalEntries); i < len(all); i++ {
		require.EqualValues(t, i+1, all[i].seq)
		require.Equal(t, all[i-1].digest, all[i].parent)
		require.NotEmpty(t, all[i].digest)
		key := fmt.Sprintf("root:%s:%s:%d:%s:%d", aliasRoot, session, epoch, generation, all[i].seq)
		var keyedSeq int64
		require.NoError(t, ro.QueryRowContext(ctx, `SELECT entry_seq FROM embedded_append_keys WHERE append_key=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, key, aliasRoot, session, epoch, generation).Scan(&keyedSeq))
		require.Equal(t, all[i].seq, keyedSeq)
	}
	require.Equal(t, "user", all[len(originalEntries)].kind)
	neutral, err := ai.UnmarshalMessage(all[len(originalEntries)].body)
	require.NoError(t, err)
	u, ok := neutral.(ai.UserMessage)
	require.True(t, ok)
	require.Equal(t, ai.ContentList{ai.TextContent{Text: "Continue working from your saved context."}}, u.Content)
	last := all[len(all)-1]
	require.Equal(t, "assistant", last.kind)
	decoded, err := ai.UnmarshalMessage(last.body)
	require.NoError(t, err)
	answer, ok := decoded.(ai.AssistantMessage)
	require.True(t, ok)
	require.Equal(t, ai.StopStop, answer.StopReason)
	require.Len(t, answer.Content, 1)
	finalText, ok = answer.Content[0].(ai.TextContent)
	require.True(t, ok)
	require.Equal(t, "DUMMY neutral final", finalText.Text)
	var wakeID, wakeDigest, wakeState string
	var wakeProcess, wakeToken int64
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT wake_id,digest,state,process_generation,claim_owner_token FROM embedded_restart_wake_slots WHERE agent_id=? AND slot='current'`, aliasRoot).Scan(&wakeID, &wakeDigest, &wakeState, &wakeProcess, &wakeToken))
	require.NotEmpty(t, wakeID)
	require.Equal(t, originalEntries[len(originalEntries)-1].digest, wakeDigest)
	require.Equal(t, "checked-complete", wakeState)
	require.Equal(t, router.embeddedProcessGeneration, wakeProcess)
	require.Equal(t, newToken, wakeToken)
	var originSeq int64
	var completionDigest string
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT o.seq,e.digest FROM embedded_restart_wake_checked_origins o JOIN embedded_entries e ON e.agent_id=o.agent_id AND e.session_id=o.session_id AND e.epoch=o.epoch AND e.generation=o.runtime_generation AND e.seq=o.seq WHERE o.agent_id=? AND o.wake_id=? AND o.process_generation=? AND o.claim_owner_token=?`, aliasRoot, wakeID, wakeProcess, newToken).Scan(&originSeq, &completionDigest))
	require.Equal(t, last.seq, originSeq)
	require.Equal(t, last.digest, completionDigest)
	for _, table := range []string{"embedded_acks", "embedded_attempts"} {
		var count int
		require.NoError(t, ro.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE agent_id=?", aliasRoot).Scan(&count))
		require.Zero(t, count)
	}
	phase, problems = doer.snapshot()
	require.Equal(t, 3, phase)
	require.Empty(t, problems)
	require.Error(t, router.Listen(ctx))
	p, bad := doer.snapshot()
	require.Equal(t, 3, p)
	require.Empty(t, bad)
	fmt.Printf("ALIAS_TRACE phase=cold-restart category=checked-neutral seq=%d digest=%s parent=%s process-generation=%d\n", last.seq, last.digest, last.parent, wakeProcess)
}
