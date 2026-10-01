package managed

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func g1Tool(execute func(context.Context) (agent.AgentToolResult, error)) *agent.AgentTool {
	return &agent.AgentTool{Name: "g1_test_external_effect", Description: "fixed external test effect", Parameters: &ai.Schema{Type: "object"}, Execute: func(ctx context.Context, _ string, _ map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
		return execute(ctx)
	}}
}
func g1Manager(t *testing.T, db *store.Store, root string, tool *agent.AgentTool, calls *atomic.Int32) *Manager {
	t.Helper()
	credentials := testfixture.CodexAdmission(t)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	t.Cleanup(reg.Unregister)
	reg.SetResponses([]providers.FauxResponseStep{func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		calls.Add(1)
		return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("g1_test_external_effect", map[string]any{}, "g1-effect-1")}, ai.StopToolUse)
	}})
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() }, EmbeddedExternalTool: tool, CodexCredentialStore: credentials})
	require.NoError(t, err)
	return m
}
func g1Launch(t *testing.T, m *Manager, root string) store.EmbeddedBinding {
	t.Helper()
	launched, err := m.LaunchEmbeddedRoot(context.Background(), LaunchRequest{Name: "g1-root", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "call g1_test_external_effect", RequestID: "a1111111-1111-4111-8111-111111111111"})
	require.NoError(t, err)
	return store.EmbeddedBinding{AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
}
func g1Binding(t *testing.T, db *store.Store) store.EmbeddedBinding {
	t.Helper()
	agents, err := db.ManagedAgents(context.Background())
	require.NoError(t, err)
	require.Len(t, agents, 1)
	a := agents[0]
	return store.EmbeddedBinding{AgentID: a.AgentID, SessionID: a.PiSessionID, Epoch: a.SessionEpoch, Generation: a.RuntimeGeneration}
}
func TestEmbeddedG1ProductionRunnerExternalResultAppendFailure(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700)) // fixture-owned Cwd, before launch
	path := filepath.Join(t.TempDir(), "g1.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = raw.ExecContext(ctx, `CREATE TRIGGER g1_result_fault BEFORE INSERT ON embedded_entries WHEN NEW.kind='toolResult' BEGIN SELECT RAISE(ABORT,'g1 result fault'); END`)
	require.NoError(t, err)
	require.NoError(t, raw.Close())
	var calls, executions atomic.Int32
	tool := g1Tool(func(context.Context) (agent.AgentToolResult, error) {
		effect, err := db.RecoverEmbeddedEffect(ctx, inspectManagedOwnerBinding(t, path, g1Binding(t, db)), "g1-effect-1")
		if err != nil || effect.State != "unknown" {
			return agent.AgentToolResult{}, errors.New("Execute before committed intent")
		}
		executions.Add(1)
		return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: "executed"}}}, nil
	})
	m := g1Manager(t, db, root, tool, &calls)
	initialGeneration := setEmbeddedRecoveryGeneration(t, ctx, db, m)
	t.Logf("external-result initial process_generation=%d", initialGeneration)
	binding := g1Launch(t, m, root)
	defer func() {
		if !t.Failed() {
			return
		}
		m.mu.Lock()
		processGeneration := m.embeddedStartupGeneration
		m.mu.Unlock()
		t.Logf("external-result clock=%s manager_process_generation=%d provider_calls=%d executions=%d binding=%+v", time.Now().UTC(), processGeneration, calls.Load(), executions.Load(), binding)
		cwdPrivacyManagedSnapshot(t, path)
		row, rowErr := db.ManagedAgent(ctx, binding.AgentID)
		t.Logf("external-result managed=%+v query_error=%v", row, rowErr)
		read, readErr := sql.Open("sqlite", "file:"+path+"?mode=ro")
		if readErr != nil {
			t.Logf("external-result snapshot open_error=%v", readErr)
			return
		}
		defer read.Close()
		for _, query := range []string{
			`SELECT json_group_array(json_object('id',effect_id,'state',state,'intent',CAST(intent AS TEXT),'result',CAST(result AS TEXT))) FROM embedded_effects`,
			`SELECT json_group_array(json_object('seq',seq,'kind',kind,'body',CAST(body AS TEXT))) FROM embedded_entries`,
		} {
			var snapshot string
			queryErr := read.QueryRowContext(ctx, query).Scan(&snapshot)
			t.Logf("external-result query=%s result=%s query_error=%v", query, snapshot, queryErr)
		}
	}()
	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, binding.AgentID)
		return e == nil && a.LifecycleState == "failed"
	}, 3*time.Second, 10*time.Millisecond)
	require.EqualValues(t, 1, executions.Load())
	require.EqualValues(t, 1, calls.Load())
	// The failed managed lifecycle deliberately fences Store recovery APIs;
	// inspect persisted evidence read-only across the independent connection.
	check, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	var state string
	var result []byte
	var results int
	require.NoError(t, check.QueryRowContext(ctx, `SELECT state,result FROM embedded_effects WHERE agent_id=? AND effect_id=?`, binding.AgentID, "g1-effect-1").Scan(&state, &result))
	require.Equal(t, "unknown", state)
	require.Empty(t, result)
	require.NoError(t, check.QueryRowContext(ctx, `SELECT COUNT(*) FROM embedded_entries WHERE agent_id=? AND kind='toolResult'`, binding.AgentID).Scan(&results))
	require.Zero(t, results)
	require.NoError(t, check.QueryRowContext(ctx, `SELECT COUNT(*) FROM embedded_acks WHERE agent_id=?`, binding.AgentID).Scan(&results))
	require.Zero(t, results)
	require.NoError(t, check.Close())
	require.NoError(t, m.Close(ctx))
	require.NoError(t, db.Close())
	reopened, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer reopened.Close()
	next := g1Manager(t, reopened, root, tool, &calls)
	defer next.Close(ctx)
	require.NoError(t, next.RecoverEmbeddedRoots(ctx))
	time.Sleep(100 * time.Millisecond)
	require.EqualValues(t, 1, executions.Load())
	require.EqualValues(t, 1, calls.Load())
	saved, err := reopened.ManagedAgent(ctx, binding.AgentID)
	require.NoError(t, err)
	require.Equal(t, "failed", saved.LifecycleState)
}

// Count at Execute entry, before any validation or error return. The synced file
// survives SIGKILL and is shared by both independent Manager processes.
func g1CountExternalInvocation(root string) error {
	f, err := os.OpenFile(filepath.Join(root, "executed"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write([]byte("execute\n"))
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// Child runs the same production Manager and SDK executor, stopping only inside
// Execute after the durable intent is externally visible. Parent sends SIGKILL.
func TestEmbeddedG1KillChild(t *testing.T) {
	if os.Getenv("G1_CHILD") != "1" {
		return
	}
	ctx := context.Background()
	root := os.Getenv("G1_ROOT")
	path := os.Getenv("G1_DB")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	var calls atomic.Int32
	tool := g1Tool(func(context.Context) (agent.AgentToolResult, error) {
		if err := g1CountExternalInvocation(root); err != nil {
			return agent.AgentToolResult{}, err
		}
		binding := inspectManagedOwnerBinding(t, path, g1Binding(t, db))
		effect, err := db.RecoverEmbeddedEffect(ctx, binding, "g1-effect-1")
		if err != nil || effect.State != "unknown" {
			return agent.AgentToolResult{}, errors.New("Execute before committed intent")
		}
		for {
			time.Sleep(time.Second)
		}
	})
	if os.Getenv("G1_EXPLICIT") == "1" {
		manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
		require.NoError(t, err)
		reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
		reg.SetResponses([]providers.FauxResponseStep{
			func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
				return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "initial complete"}}, ai.StopStop)
			},
			func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
				return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("g1_test_external_effect", map[string]any{}, "g1-effect-1")}, ai.StopToolUse)
			},
		})
		var m *Manager
		m, err = NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() }, EmbeddedExternalTool: tool, CodexCredentialStore: testfixture.CodexAdmission(t), EmbeddedRunnerFactory: func(runCtx context.Context, saved store.ManagedAgentRecord) error {
			if err := m.runEmbeddedSession(runCtx, saved); err != nil {
				return err
			}
			return m.runEmbeddedSessionPrompt(runCtx, saved, "explicit provider work")
		}})
		require.NoError(t, err)
		initialGeneration := setEmbeddedRecoveryGeneration(t, ctx, db, m)
		t.Logf("explicit initial process_generation=%d", initialGeneration)
		_ = g1Launch(t, m, root)
	} else {
		m := g1Manager(t, db, root, tool, &calls)
		_ = g1Launch(t, m, root)
	}
	select {}
}
func TestEmbeddedG1SIGKILLAfterIntentNoReplay(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "g1.db")
	cmd := exec.Command(os.Args[0], "-test.run=^TestEmbeddedG1KillChild$", "-test.v")
	cmd.Env = append(os.Environ(), "G1_CHILD=1", "G1_ROOT="+root, "G1_DB="+path)
	var childOutput bytes.Buffer
	cmd.Stdout, cmd.Stderr = &childOutput, &childOutput
	require.NoError(t, cmd.Start())
	var binding store.EmbeddedBinding
	var pollStage, effectState, executed string
	var pollErr, fileErr error
	var pollMu sync.Mutex
	defer func() {
		pollMu.Lock()
		defer pollMu.Unlock()
		if t.Failed() {
			t.Logf("intent-child before cleanup clock=%s pid=%d binding=%+v poll_stage=%s effect_state=%s query_error=%v executed=%q file_error=%v waited=%t", time.Now().UTC(), cmd.Process.Pid, binding, pollStage, effectState, pollErr, executed, fileErr, cmd.ProcessState != nil)
			cwdPrivacyManagedSnapshot(t, path)
			published, publishedErr := os.ReadFile(filepath.Join(root, "executed"))
			t.Logf("intent-child synced_file=%q file_error=%v", published, publishedErr)
			read, readErr := sql.Open("sqlite", "file:"+path+"?mode=ro")
			if readErr != nil {
				t.Logf("intent-child snapshot open_error=%v", readErr)
			} else {
				for _, query := range []string{
					`SELECT json_group_array(json_object('agent',agent_id,'session',pi_session_id,'epoch',session_epoch,'runtime',runtime_generation,'pid',process_pid,'marker',process_start_marker,'incarnation',process_incarnation,'lifecycle',lifecycle_state,'error',last_error_code,'message',last_error_message)) FROM managed_agents`,
					`SELECT json_group_array(json_object('id',effect_id,'state',state,'intent',CAST(intent AS TEXT),'result',CAST(result AS TEXT))) FROM embedded_effects`,
					`SELECT json_group_array(json_object('process',generation,'incarnation',incarnation_key)) FROM embedded_process_generations`,
				} {
					var snapshot string
					queryErr := read.QueryRowContext(ctx, query).Scan(&snapshot)
					t.Logf("intent-child query=%s result=%s query_error=%v", query, snapshot, queryErr)
				}
				_ = read.Close()
			}
		}
		if cmd.ProcessState == nil {
			killErr := cmd.Process.Kill()
			waitErr := cmd.Wait()
			if t.Failed() {
				t.Logf("intent-child cleanup kill_error=%v wait_error=%v exit=%v stdout_stderr=%s", killErr, waitErr, cmd.ProcessState, childOutput.String())
			}
		} else if t.Failed() {
			t.Logf("intent-child exit=%v stdout_stderr=%s", cmd.ProcessState, childOutput.String())
		}
	}()
	require.Eventually(t, func() bool {
		pollMu.Lock()
		defer pollMu.Unlock()
		// Execute writes and syncs this existing milestone after child Store.Open.
		// Do not contend with the child's initial journal-mode setup before it.
		pollStage = "execution-file"
		milestone, milestoneErr := os.ReadFile(filepath.Join(root, "executed"))
		executed, fileErr = string(milestone), milestoneErr
		if milestoneErr != nil || string(milestone) != "execute\n" {
			return false
		}
		pollStage = "open"
		db, e := store.Open(ctx, path, store.Options{})
		pollErr = e
		if e != nil {
			return false
		}
		defer db.Close()
		pollStage = "agents"
		agents, e := db.ManagedAgents(ctx)
		pollErr = e
		if e != nil || len(agents) != 1 {
			return false
		}
		a := agents[0]
		binding = store.EmbeddedBinding{AgentID: a.AgentID, SessionID: a.PiSessionID, Epoch: a.SessionEpoch, Generation: a.RuntimeGeneration}
		pollStage = "owner"
		read, e := sql.Open("sqlite", "file:"+path+"?mode=ro")
		pollErr = e
		if e != nil {
			return false
		}
		defer read.Close()
		pollErr = read.QueryRowContext(ctx, `SELECT owner_token FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1 AND owner_id IS NOT NULL`, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation).Scan(&binding.OwnerToken)
		if pollErr != nil || binding.OwnerToken <= 0 {
			return false
		}
		pollStage = "effect"
		effect, effectErr := db.RecoverEmbeddedEffect(ctx, binding, "g1-effect-1")
		pollErr, effectState = effectErr, effect.State
		b, readErr := os.ReadFile(filepath.Join(root, "executed"))
		executed, fileErr = string(b), readErr
		return effectErr == nil && effect.State == "unknown" && readErr == nil && string(b) == "execute\n"
	}, 8*time.Second, 20*time.Millisecond, "child must reach committed intent and real Execute")
	require.NoError(t, cmd.Process.Kill())
	err := cmd.Wait()
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "killed") || strings.Contains(err.Error(), "signal: killed"), "not an actual SIGKILL: %v", err)
	// The killed process cannot release its lease. Move only this recovery
	// Store's deterministic clock beyond the persisted expiry before retrying.
	clock := deterministic.NewFixedClock(time.Now().Add(embeddedOwnerTTL + time.Minute))
	db, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer db.Close()
	var calls atomic.Int32
	tool := g1Tool(func(context.Context) (agent.AgentToolResult, error) {
		if err := g1CountExternalInvocation(root); err != nil {
			return agent.AgentToolResult{}, err
		}
		return agent.AgentToolResult{}, errors.New("replayed external effect")
	})
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	var unstarted atomic.Int32
	reg.SetResponses([]providers.FauxResponseStep{func(transcript ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		calls.Add(1)
		check, e := sql.Open("sqlite", path)
		if e != nil {
			unstarted.Add(1)
		} else {
			var started int
			e = check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_interrupted_notices WHERE agent_id=? AND state='started' AND entry_seq IS NOT NULL`, binding.AgentID).Scan(&started)
			if e != nil || started != 1 {
				unstarted.Add(1)
			}
			_ = check.Close()
		}
		if len(transcript.Messages) < 2 {
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "missing recovery notice"}}, ai.StopError)
		}
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "recovered"}}, ai.StopStop)
	}})
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() }, EmbeddedExternalTool: tool, CodexCredentialStore: testfixture.CodexAdmission(t)})
	require.NoError(t, err)
	defer m.Close(ctx)
	setEmbeddedRecoveryGeneration(t, ctx, db, m)
	require.NoError(t, m.RecoverEmbeddedRoots(ctx))
	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, binding.AgentID)
		return e == nil && a.LifecycleState == "running"
	}, 3*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		fresh := inspectManagedOwnerBinding(t, path, binding)
		if fresh.OwnerToken == binding.OwnerToken {
			return false
		}
		binding.OwnerToken = fresh.OwnerToken
		return true
	}, 3*time.Second, 10*time.Millisecond)
	effect, err := db.RecoverEmbeddedEffect(ctx, binding, "g1-effect-1")
	require.NoError(t, err)
	require.Equal(t, "unknown", effect.State)
	require.False(t, effect.ShouldReplay)
	require.Empty(t, effect.Result)
	b, err := os.ReadFile(filepath.Join(root, "executed"))
	require.NoError(t, err)
	require.Equal(t, "execute\n", string(b), "exactly one external Execute invocation across both processes")
	require.Eventually(t, func() bool { return calls.Load() == 1 }, 3*time.Second, 10*time.Millisecond, "one non-mail recovery provider request")
	snap, err := db.ReopenEmbedded(ctx, binding)
	require.Len(t, snap.Entries, 5, "system, original user/assistant, notice/final")
	require.Equal(t, "user", snap.Entries[3].Kind)
	var noticeRows, startedRows int
	checkNotice, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	require.NoError(t, checkNotice.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER (WHERE state='started') FROM embedded_interrupted_notices WHERE agent_id=?`, binding.AgentID).Scan(&noticeRows, &startedRows))
	require.NoError(t, checkNotice.Close())
	require.Equal(t, 1, noticeRows)
	require.Equal(t, 1, startedRows)
	require.Zero(t, unstarted.Load(), "one-shot notice start must commit before provider entry")
	require.EqualValues(t, 1, calls.Load())

	require.NoError(t, err)
	for _, entry := range snap.Entries {
		require.NotEqual(t, "toolResult", entry.Kind)
	}
	check, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	var results, acks int
	require.NoError(t, check.QueryRowContext(ctx, `SELECT COUNT(*) FROM embedded_entries WHERE agent_id=? AND kind='toolResult'`, binding.AgentID).Scan(&results))
	require.NoError(t, check.QueryRowContext(ctx, `SELECT COUNT(*) FROM embedded_acks WHERE agent_id=?`, binding.AgentID).Scan(&acks))
	require.Zero(t, results)
	require.Zero(t, acks)
	var replies int
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_reply_outbox WHERE recipient=?`, binding.AgentID).Scan(&replies))
	require.Zero(t, replies)
	require.NoError(t, check.Close())
	// Check again after recovery settles: even an error-returning replacement
	// Execute must leave an additional durable marker.
	time.Sleep(100 * time.Millisecond)
	b, err = os.ReadFile(filepath.Join(root, "executed"))
	require.NoError(t, err)
	require.Equal(t, "execute\n", string(b))
}

func TestEmbeddedG1FirstUserAppendFailureNeverRequestsProvider(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "g1-user.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	fault, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = fault.ExecContext(ctx, `CREATE TRIGGER g1_user_append_fault BEFORE INSERT ON embedded_entries WHEN NEW.kind='user' BEGIN SELECT RAISE(ABORT,'g1 injected user append failure'); END`)
	require.NoError(t, err)
	require.NoError(t, fault.Close())
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	var calls, processes, rpcs atomic.Int32
	reg.SetResponses([]providers.FauxResponseStep{func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		calls.Add(1)
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "unexpected"}}, ai.StopStop)
	}})
	makeManager := func(s *store.Store) *Manager {
		m, e := NewManager(ManagerOptions{Store: s, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), ProcessFactory: &rootRedProcessFactory{calls: &processes}, RPCFactory: rootRedRPCFactory{calls: &rpcs}, EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() }, CodexCredentialStore: testfixture.CodexAdmission(t)})
		require.NoError(t, e)
		return m
	}
	m := makeManager(db)
	launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "g1-user-fault", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "first user", RequestID: "b1111111-1111-4111-8111-111111111111"})
	require.NoError(t, err)
	binding := store.EmbeddedBinding{AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, binding.AgentID)
		return e == nil && a.LifecycleState == "failed"
	}, 3*time.Second, 10*time.Millisecond)
	require.Zero(t, calls.Load())
	check, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	var userRows, ackRows int
	require.NoError(t, check.QueryRowContext(ctx, `SELECT COUNT(*) FROM embedded_entries WHERE agent_id=? AND kind IN ('user','assistant','toolResult')`, binding.AgentID).Scan(&userRows))
	require.NoError(t, check.QueryRowContext(ctx, `SELECT COUNT(*) FROM embedded_acks WHERE agent_id=?`, binding.AgentID).Scan(&ackRows))
	require.Zero(t, userRows)
	require.Zero(t, ackRows)
	require.NoError(t, check.Close())
	require.NoError(t, m.Close(ctx))
	require.NoError(t, db.Close())
	reopened, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer reopened.Close()
	next := makeManager(reopened)
	defer next.Close(ctx)
	require.NoError(t, next.RecoverEmbeddedRoots(ctx))
	saved, err := reopened.ManagedAgent(ctx, binding.AgentID)
	require.NoError(t, err)
	require.Equal(t, "failed", saved.LifecycleState)
	require.Zero(t, calls.Load())
	require.Zero(t, processes.Load())
	require.Zero(t, rpcs.Load())
}

func TestEmbeddedG1ExplicitPromptSIGKILLNonMailNotice(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700)) // parent owns the child's saved Cwd
	path := filepath.Join(root, "explicit.db")
	cmd := exec.Command(os.Args[0], "-test.run=^TestEmbeddedG1KillChild$", "-test.v")
	cmd.Env = append(os.Environ(), "G1_CHILD=1", "G1_EXPLICIT=1", "G1_ROOT="+root, "G1_DB="+path)
	childLogPath := filepath.Join(root, "child.log")
	childLog, err := os.Create(childLogPath)
	require.NoError(t, err)
	defer childLog.Close()
	cmd.Stdout, cmd.Stderr = childLog, childLog
	require.NoError(t, cmd.Start())
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		if t.Failed() {
			output, readErr := os.ReadFile(childLogPath)
			t.Logf("explicit child output=%s read_error=%v", output, readErr)
		}
	}()
	var binding store.EmbeddedBinding
	require.Eventually(t, func() bool {
		// Do not create/migrate the child's SQLite database while it is
		// starting. Execute's durable marker proves startup has completed;
		// the independent effect-state proof below is still required.
		data, err := os.ReadFile(filepath.Join(root, "executed"))
		if err != nil || string(data) != "execute\n" {
			return false
		}
		db, err := store.Open(ctx, path, store.Options{})
		if err != nil {
			return false
		}
		defer db.Close()
		agents, err := db.ManagedAgents(ctx)
		if err != nil || len(agents) != 1 {
			return false
		}
		a := agents[0]
		binding = store.EmbeddedBinding{AgentID: a.AgentID, SessionID: a.PiSessionID, Epoch: a.SessionEpoch, Generation: a.RuntimeGeneration}
		binding = inspectManagedOwnerBinding(t, path, binding)
		effect, err := db.RecoverEmbeddedEffect(ctx, binding, "g1-effect-1")
		if err != nil || effect.State != "unknown" {
			return false
		}
		return true
	}, 8*time.Second, 20*time.Millisecond)
	require.NoError(t, cmd.Process.Kill())
	require.Error(t, cmd.Wait())
	clock := deterministic.NewFixedClock(time.Now().Add(embeddedOwnerTTL + time.Minute))
	db, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	var calls, unstarted atomic.Int32
	reg.SetResponses([]providers.FauxResponseStep{func(transcript ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		calls.Add(1)
		var originals int
		var lastUser string
		for _, message := range transcript.Messages {
			if user, ok := message.(ai.UserMessage); ok {
				lastUser = strings.ToLower(string(mustMessageJSON(user)))
				if strings.Contains(lastUser, "explicit provider work") {
					originals++
				}
			}
		}
		if originals != 1 || !strings.Contains(lastUser, "interrupted") {
			unstarted.Add(1) // entered marker: notice only, never replay original prompt
		}
		check, e := sql.Open("sqlite", path)
		if e != nil {
			unstarted.Add(1)
		} else {
			var count int
			e = check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_interrupted_notices WHERE agent_id=? AND state='started'`, binding.AgentID).Scan(&count)
			if e != nil || count != 1 {
				unstarted.Add(1)
			}
			_ = check.Close()
		}
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "recovered"}}, ai.StopStop)
	}})
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() }, CodexCredentialStore: testfixture.CodexAdmission(t)})
	require.NoError(t, err)
	defer m.Close(ctx)
	setEmbeddedRecoveryGeneration(t, ctx, db, m)
	require.NoError(t, m.RecoverEmbeddedRoots(ctx))
	require.Eventually(t, func() bool { return calls.Load() == 1 }, 3*time.Second, 10*time.Millisecond)
	require.Zero(t, unstarted.Load())
	binding = inspectManagedOwnerBinding(t, path, binding)
	require.Eventually(t, func() bool {
		snap, e := db.ReopenEmbedded(ctx, binding)
		if e != nil {
			return false
		}
		return len(snap.Entries) == 8
	}, 3*time.Second, 200*time.Millisecond)
	snap, err := db.ReopenEmbedded(ctx, binding)
	require.NoError(t, err)
	var users, assistants int
	for _, entry := range snap.Entries {
		if entry.Kind == "user" {
			users++
		}
		if entry.Kind == "assistant" {
			assistants++
		}
	}
	require.Equal(t, 3, users)
	require.GreaterOrEqual(t, assistants, 3)
	effect, err := db.RecoverEmbeddedEffect(ctx, binding, "g1-effect-1")
	require.NoError(t, err)
	require.Equal(t, "unknown", effect.State)
	require.False(t, effect.ShouldReplay)
	data, err := os.ReadFile(filepath.Join(root, "executed"))
	require.NoError(t, err)
	require.Equal(t, "execute\n", string(data))
	check, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer check.Close()
	for _, table := range []string{"embedded_interrupted_notices", "embedded_nonmail_provider_entries"} {
		var count int
		require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE agent_id=?`, binding.AgentID).Scan(&count))
		if table == "embedded_interrupted_notices" {
			require.Equal(t, 1, count)
		} else {
			require.Equal(t, 2, count)
		}
	}
	var replies, acks int
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_reply_outbox WHERE recipient=?`, binding.AgentID).Scan(&replies))
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_acks WHERE agent_id=?`, binding.AgentID).Scan(&acks))
	require.Zero(t, replies)
	require.Zero(t, acks)
}
