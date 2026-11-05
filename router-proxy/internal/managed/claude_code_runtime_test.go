package managed

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func claudeRuntimeFixture(t *testing.T, script string) (*claudeCodeRuntime, *embeddedPersistence, *claudeCodeProcessPool, ClaudeCodeProcessConfig) {
	t.Helper()
	ctx := context.Background()
	db, b, _, path := wakePersistenceFixtureWithClaim(t, false)
	// The wake fixture inserts the managed row directly; urgency reads require
	// the corresponding canonical identity normally created at launch.
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = raw.ExecContext(ctx, `INSERT INTO agents(agent_id,first_seen_at) VALUES(?,?)`, b.AgentID, time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)
	require.NoError(t, raw.Close())
	p := &embeddedPersistence{db: db, binding: b, digest: "genesis", toolInputs: make(map[string]any), toolCompanionTexts: make(map[string]string)}
	pool := &claudeCodeProcessPool{}
	cfg := claudeTestConfig(t, script)
	cfg.SessionID = claudeCodeSessionID(b.SessionID)
	r, err := newClaudeCodeRuntime(ctx, claudeCodeRuntimeOptions{Pool: pool, Config: cfg, Persistence: p, BeforeSubmit: func(ctx context.Context, _ int) error {
		return db.RenewEmbeddedOwner(ctx, b, "wake-fixture", embeddedOwnerTTL)
	}, AfterEcho: func(ctx context.Context) error {
		return db.EnterEmbeddedNonMailProviderTurn(ctx, b, p.turnSeq, p.turnDigest)
	}})
	require.NoError(t, err)
	t.Cleanup(func() { r.Close(); pool.stop(b.AgentID) })
	return r, p, pool, cfg
}
func TestClaudeCodeRuntimeCheckedTextImageToolsAndFinal(t *testing.T) {
	r, p, _, _ := claudeRuntimeFixture(t, `{"turns":[[{"thinking":"consider"},{"text":"working"},{"tool":{"id":"read-1","name":"Read","input":{"file_path":"file"},"result":"contents"}},{"text":"done"}]]}`)
	original := ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "original input"}, ai.ImageContent{Data: "eA==", MimeType: "image/png"}}, Timestamp: 1234}
	out, err := r.RunMessages(context.Background(), []agent.AgentMessage{original})
	require.NoError(t, err)
	require.Equal(t, ai.StopStop, out.StopReason)
	snap, err := p.db.ReopenEmbedded(context.Background(), p.binding)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(snap.Entries), 5)
	body, err := json.Marshal(original)
	require.NoError(t, err)
	require.Equal(t, body, snap.Entries[0].Body)
	final, err := ai.UnmarshalMessage(snap.Entries[len(snap.Entries)-1].Body)
	require.NoError(t, err)
	assistant := final.(ai.AssistantMessage)
	require.Equal(t, ai.StopStop, assistant.StopReason)
	require.Equal(t, "claude-code", string(assistant.Provider))
	require.Equal(t, "done", r.Snapshot().LastAssistantText)
	require.Positive(t, r.Snapshot().ContextTokens)
	require.Equal(t, 1000000, r.Snapshot().ContextWindow)
}
func TestClaudeCodeRuntimeAdmissionAndEchoFailClosed(t *testing.T) {
	for _, at := range []string{"submit", "echo"} {
		t.Run(at, func(t *testing.T) {
			r, p, _, cfg := claudeRuntimeFixture(t, `{}`)
			if at == "submit" {
				r.beforeSubmit = func(context.Context, int) error { return errors.New("admission denied") }
			} else {
				r.afterEcho = func(context.Context) error { return errors.New("checked entry denied") }
			}
			_, err := r.Run(context.Background(), "input")
			require.ErrorContains(t, err, "denied")
			snap, err := p.db.ReopenEmbedded(context.Background(), p.binding)
			require.NoError(t, err)
			for _, entry := range snap.Entries {
				require.NotEqual(t, "assistant", entry.Kind)
			}
			records, err := os.ReadFile(filepath.Join(cfg.Cwd, "record.jsonl"))
			if at == "submit" && os.IsNotExist(err) {
				err = nil
			}
			require.NoError(t, err)
			if at == "submit" {
				require.NotContains(t, string(records), `"kind":"stdin"`)
				require.Empty(t, snap.Entries)
			} else {
				require.Len(t, snap.Entries, 1)
			}
		})
	}
}
func TestClaudeCodeRuntimeContinueAndFreshScope(t *testing.T) {
	r, p, pool, cfg := claudeRuntimeFixture(t, `{}`)
	ctx := context.Background()
	user := ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "durable user"}}, Timestamp: 9}
	require.NoError(t, p.AppendCheckedMessage(ctx, user))
	r.lastUser = &user
	r.snapshot.HasUser = true
	require.NoError(t, r.Continue(ctx))
	snap, err := p.db.ReopenEmbedded(ctx, p.binding)
	require.NoError(t, err)
	users := 0
	for _, e := range snap.Entries {
		if e.Kind == "user" {
			users++
		}
	}
	require.Equal(t, 1, users)
	first := r.process
	r.Close()
	next, err := newClaudeCodeRuntime(ctx, claudeCodeRuntimeOptions{Pool: pool, Config: cfg, Persistence: p, BeforeSubmit: r.beforeSubmit, AfterEcho: r.afterEcho})
	require.NoError(t, err)
	defer next.Close()
	_, err = next.Run(ctx, "new turn")
	require.NoError(t, err)
	require.NotSame(t, first, next.process)
	records, err := os.ReadFile(filepath.Join(cfg.Cwd, "record.jsonl"))
	require.NoError(t, err)
	require.Equal(t, 4, strings.Count(string(records), `"kind":"start"`), "two local preflights and two scoped CLI processes")
}
func TestClaudeCodeRuntimeFailedOriginalAppendStopsBeforeAssistant(t *testing.T) {
	r, p, _, _ := claudeRuntimeFixture(t, `{}`)
	p.binding.OwnerToken++
	_, err := r.Run(context.Background(), "input")
	require.Error(t, err)
}
func TestClaudeCodeRuntimePoolStopsOldBinding(t *testing.T) {
	r, p, pool, cfg := claudeRuntimeFixture(t, `{}`)
	_, err := r.Run(context.Background(), "first")
	require.NoError(t, err)
	first := r.process
	r.Close()
	changed := p.binding
	changed.Epoch++
	_, err = pool.acquire(context.Background(), changed, cfg, nil)
	require.NoError(t, err)
	select {
	case <-first.Done():
	default:
		t.Fatal("old binding process not reaped")
	}
}

func TestClaudeCodeRuntimePartialInterrupt(t *testing.T) {
	r, p, _, _ := claudeRuntimeFixture(t, `{"turns":[[{"text":"partial"},{"wait":"interrupt"}],[{"text":"queued answer"}]]}`)
	appended := make(chan struct{})
	p.afterUserAppend = func(context.Context) {
		select {
		case <-appended:
		default:
			close(appended)
		}
	}
	done := make(chan error, 1)
	go func() { _, err := r.Run(context.Background(), "first"); done <- err }()
	<-appended
	r.Interrupt()
	require.ErrorIs(t, <-done, context.Canceled)
	snap, err := p.db.ReopenEmbedded(context.Background(), p.binding)
	require.NoError(t, err)
	last, err := ai.UnmarshalMessage(snap.Entries[len(snap.Entries)-1].Body)
	require.NoError(t, err)
	require.Equal(t, ai.StopAborted, last.(ai.AssistantMessage).StopReason)
}

func TestClaudeCodeTurnBridgeDuplicateConflictAndUnknown(t *testing.T) {
	ctx := context.Background()
	db, b, _, _ := wakePersistenceFixtureWithClaim(t, false)
	m := &Manager{store: db}
	p := &embeddedPersistence{db: db, binding: b, digest: "genesis"}
	a, err := m.newEmbeddedTurnToolAuthority(ctx, embeddedOwner{binding: b, identity: "wake-fixture"}, p, SavedAgentSession{Cwd: t.TempDir()}, false)
	require.NoError(t, err)
	require.NoError(t, a.AppendCheckedMessage(ctx, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "remember"}}}))
	bridge := newClaudeCodeTurnBridge(a)
	bridge.admitted = true
	call := ai.ToolCall{ID: "once", Name: "scratchpad", Arguments: map[string]any{"action": "append", "content": "once only"}}
	require.NoError(t, a.AppendCheckedMessage(ctx, ai.AssistantMessage{Content: ai.ContentList{call}, StopReason: ai.StopToolUse}))
	require.NoError(t, bridge.producer(ctx, call))
	args, _ := json.Marshal(call.Arguments)
	request := ClaudeCodeMCPCall{ToolUseID: call.ID, Name: call.Name, Arguments: args}
	first, err := bridge.execute(ctx, request)
	require.NoError(t, err)
	second, err := bridge.execute(ctx, request)
	require.NoError(t, err)
	require.Equal(t, first, second)
	count, err := db.CountScratchpadEntries(ctx, b.AgentID)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	request.Arguments = json.RawMessage(`{"action":"append","content":"different"}`)
	_, err = bridge.execute(ctx, request)
	require.ErrorContains(t, err, "conflict")
	unknown := ai.ToolCall{ID: "unknown", Name: "scratchpad", Arguments: call.Arguments}
	require.NoError(t, bridge.producer(ctx, unknown))
	bridge.calls[unknown.ID].started = true
	request.ToolUseID = unknown.ID
	request.Arguments = args
	_, err = bridge.execute(ctx, request)
	require.ErrorContains(t, err, "UNKNOWN")
	bridge.close()
	_, err = bridge.execute(ctx, request)
	require.ErrorContains(t, err, "no admitted turn")
}

func TestClaudeCodeRuntimeUsageLimitNoticeBeforeHold(t *testing.T) {
	reset := time.Now().Add(15 * time.Minute).Truncate(time.Second)
	r, p, _, _ := claudeRuntimeFixture(t, fmt.Sprintf(`{"turns":[[{"rate_limit":{"resetsAt":%d}}]]}`, reset.Unix()))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := r.Run(ctx, "limited user"); done <- err }()
	defer func() { cancel(); <-done }()
	require.Eventually(t, func() bool {
		snapshot, err := p.db.ReopenEmbedded(context.Background(), p.binding)
		if err != nil {
			return false
		}
		for _, entry := range snapshot.Entries {
			if strings.Contains(string(entry.Body), "Claude usage limit reached; resuming at "+reset.UTC().Format(time.RFC3339)) {
				return true
			}
		}
		return false
	}, 3*time.Second, 20*time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("hold ended early: %v", err)
	default:
	}
}

func TestClaudeCodeRuntimeUsageLimitContinueWithoutDuplicate(t *testing.T) {
	r, p, _, _ := claudeRuntimeFixture(t, `{"turns":[[{"rate_limit":{"resetsAt":1}}],[{"text":"resumed answer"}]]}`)
	out, err := r.Run(context.Background(), "limited user")
	require.NoError(t, err)
	require.Equal(t, ai.StopStop, out.StopReason)
	snapshot, err := p.db.ReopenEmbedded(context.Background(), p.binding)
	require.NoError(t, err)
	users := 0
	for _, entry := range snapshot.Entries {
		if entry.Kind == "user" {
			users++
		}
	}
	require.Equal(t, 1, users)
	require.Equal(t, "resumed answer", r.Snapshot().LastAssistantText)
}

func TestClaudeCodeTurnBridgeWaitsForCheckedProducer(t *testing.T) {
	ctx := context.Background()
	db, b, _, _ := wakePersistenceFixtureWithClaim(t, false)
	m := &Manager{store: db}
	p := &embeddedPersistence{db: db, binding: b, digest: "genesis"}
	a, err := m.newEmbeddedTurnToolAuthority(ctx, embeddedOwner{binding: b, identity: "wake-fixture"}, p, SavedAgentSession{Cwd: t.TempDir()}, false)
	require.NoError(t, err)
	bridge := newClaudeCodeTurnBridge(a)
	bridge.admitted = true
	require.NoError(t, a.AppendCheckedMessage(ctx, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "test"}}}))
	call := ai.ToolCall{ID: "http-first", Name: "scratchpad", Arguments: map[string]any{"action": "append", "content": "after producer"}}
	args, _ := json.Marshal(call.Arguments)
	done := make(chan error, 1)
	go func() {
		_, err := bridge.execute(ctx, ClaudeCodeMCPCall{ToolUseID: call.ID, Name: call.Name, Arguments: args})
		done <- err
	}()
	bridge.serial.Lock()
	require.NoError(t, a.AppendCheckedMessage(ctx, ai.AssistantMessage{Content: ai.ContentList{call}, StopReason: ai.StopToolUse}))
	require.NoError(t, bridge.producer(ctx, call))
	bridge.serial.Unlock()
	require.NoError(t, <-done)
}

func TestClaudeCodeRuntimeRejectsMidTurnSteer(t *testing.T) {
	r, p, _, _ := claudeRuntimeFixture(t, `{"turns":[[{"text":"first answer"}]]}`)
	accepted := make(chan struct{})
	release := make(chan struct{})
	p.afterUserAppend = func(context.Context) { close(accepted); <-release }
	done := make(chan error, 1)
	go func() { _, err := r.Run(context.Background(), "first"); done <- err }()
	<-accepted
	require.False(t, r.TrySteer(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "steered"}}}, nil))
	close(release)
	require.NoError(t, <-done)
	snapshot, err := p.db.ReopenEmbedded(context.Background(), p.binding)
	require.NoError(t, err)
	users := 0
	for _, entry := range snapshot.Entries {
		if entry.Kind == "user" {
			users++
		}
	}
	require.Equal(t, 1, users)
	require.Equal(t, "first answer", r.Snapshot().LastAssistantText)
}

func TestClaudeCodeRuntimeCancelDuringResultAppendPreservesPartial(t *testing.T) {
	r, p, _, _ := claudeRuntimeFixture(t, `{"turns":[[{"text":"partial"},{"tool":{"id":"read","name":"Read","input":{},"result":"data"}},{"text":"last partial"},{"wait":"interrupt"}]]}`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel after the tool-use message is durable, but before its result's
	// checked append. This is the race behind the snapshot/poll reproduction.
	p.goalResult = func(_ context.Context, _ ai.ToolResultMessage, checked bool) error {
		if !checked {
			cancel()
		}
		return nil
	}
	_, err := r.Run(ctx, "interrupt me")
	require.ErrorIs(t, err, context.Canceled)
	snapshot, err := p.db.ReopenEmbedded(context.Background(), p.binding)
	require.NoError(t, err)
	last, err := ai.UnmarshalMessage(snapshot.Entries[len(snapshot.Entries)-1].Body)
	require.NoError(t, err)
	assistant := last.(ai.AssistantMessage)
	require.Equal(t, ai.StopAborted, assistant.StopReason)
	require.Equal(t, "last partial", claudeCodeMessageText(assistant))
}

func TestClaudeCodeRuntimeToolEndingInterruptDiscardsPartial(t *testing.T) {
	r, p, _, _ := claudeRuntimeFixture(t, `{"turns":[[{"text":"partial"},{"tool":{"id":"read","name":"Read","input":{},"result":"data"}},{"text":"discard me"},{"wait":"interrupt"}]]}`)
	// Match the router tool-ending callback: discard the final partial, but
	// keep the checked producer and result. This is not a user cancellation.
	p.goalResult = func(_ context.Context, _ ai.ToolResultMessage, checked bool) error {
		if checked {
			r.mu.Lock()
			r.discardPartial = true
			r.mu.Unlock()
			r.Interrupt()
		}
		return nil
	}
	out, err := r.Run(context.Background(), "end through router tool")
	require.NoError(t, err)
	require.Equal(t, ai.StopStop, out.StopReason)
	snapshot, err := p.db.ReopenEmbedded(context.Background(), p.binding)
	require.NoError(t, err)
	require.Len(t, snapshot.Entries, 4)
	last, err := ai.UnmarshalMessage(snapshot.Entries[len(snapshot.Entries)-1].Body)
	require.NoError(t, err)
	require.IsType(t, ai.ToolResultMessage{}, last)
	require.Equal(t, "data", claudeCodeMessageText(last))
}

func TestClaudeCodeRuntimeCancelPreservesPartialAndReaps(t *testing.T) {
	r, p, pool, _ := claudeRuntimeFixture(t, `{"turns":[[{"text":"partial"},{"tool":{"id":"read","name":"Read","input":{},"result":"data"}},{"text":"last partial"},{"wait":"interrupt"}]]}`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checked := make(chan struct{})
	p.goalResult = func(_ context.Context, _ ai.ToolResultMessage, committed bool) error {
		if committed {
			close(checked)
			cancel()
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { _, err := r.Run(ctx, "interrupt me"); done <- err }()
	select {
	case <-checked:
	case <-time.After(5 * time.Second):
		t.Fatal("tool result was not checked")
	}
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not finish")
	}
	snapshot, err := p.db.ReopenEmbedded(context.Background(), p.binding)
	require.NoError(t, err)
	require.NotEmpty(t, snapshot.Entries)
	last, err := ai.UnmarshalMessage(snapshot.Entries[len(snapshot.Entries)-1].Body)
	require.NoError(t, err)
	assistant, ok := last.(ai.AssistantMessage)
	require.True(t, ok)
	require.Equal(t, ai.StopAborted, assistant.StopReason)
	require.Equal(t, "last partial", claudeCodeMessageText(assistant))
	proc := r.process
	require.NotNil(t, proc)
	pid := proc.cmd.Process.Pid
	r.Close()
	select {
	case <-proc.Done():
	default:
		t.Fatal("process not reaped by foreground close")
	}
	require.ErrorIs(t, syscall.Kill(-pid, 0), syscall.ESRCH, "process group survives foreground close")
	require.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH, "process leader survives foreground close")
	require.NoError(t, pool.stop(p.binding.AgentID))
}

func TestClaudeBridgePreservesOriginalArgumentsAndRequestContext(t *testing.T) {
	ctx := context.Background()
	db, b, _, _ := wakePersistenceFixtureWithClaim(t, false)
	owner := embeddedOwner{binding: b, identity: "wake-fixture"}
	a := &embeddedTurnToolAuthority{persistence: &embeddedPersistence{db: db, binding: b, digest: "genesis"}}
	a.beforeToolExecute = func(ctx context.Context, call agent.BeforeToolCallContext) error {
		call.ToolCall.Arguments["duration_ms"] = float64(60000)
		return nil
	}
	a.tools = []agent.AgentTool{{Name: "goal_snooze", Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
		current, ok := ctx.Value(embeddedOwnerContextKey{}).(embeddedOwner)
		if !ok || current != owner {
			return agent.AgentToolResult{}, errors.New("missing admitted owner")
		}
		if ctx.Value("request-marker") != "retained" {
			return agent.AgentToolResult{}, errors.New("missing HTTP context")
		}
		return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: "ok"}}}, nil
	}}}
	bridge := newClaudeCodeTurnBridge(a)
	bridge.admitted = true
	// The real constructor gets the owner from the admitted runtime context.
	bridgeCtx := context.WithValue(ctx, embeddedOwnerContextKey{}, owner)
	call := ai.ToolCall{ID: "immutable", Name: "goal_snooze", Arguments: map[string]any{"duration_ms": float64(90000)}}
	require.NoError(t, bridge.producer(bridgeCtx, call))
	request := ClaudeCodeMCPCall{ToolUseID: call.ID, Name: call.Name, Arguments: json.RawMessage(`{"duration_ms":90000}`)}
	requestCtx := context.WithValue(ctx, "request-marker", "retained")
	first, err := bridge.execute(requestCtx, request)
	require.NoError(t, err)
	require.NotContains(t, string(first), "missing admitted owner")
	second, err := bridge.execute(requestCtx, request)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, float64(90000), call.Arguments["duration_ms"])
}

func TestClaudeBridgeCheckedFailureClosesAdmission(t *testing.T) {
	ctx := context.Background()
	db, b, _, _ := wakePersistenceFixtureWithClaim(t, false)
	p := &embeddedPersistence{db: db, binding: b, digest: "genesis"}
	a := &embeddedTurnToolAuthority{persistence: p, beforeToolExecute: func(context.Context, agent.BeforeToolCallContext) error { return nil }}
	a.tools = []agent.AgentTool{{Name: "scratchpad", Execute: func(context.Context, string, map[string]any, agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
		p.binding.OwnerToken++
		return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: "domain committed"}}}, nil
	}}}
	bridge := newClaudeCodeTurnBridge(a)
	bridge.admitted = true
	call := ai.ToolCall{ID: "failed-check", Name: "scratchpad", Arguments: map[string]any{}}
	require.NoError(t, bridge.producer(ctx, call))
	_, err := bridge.execute(ctx, ClaudeCodeMCPCall{ToolUseID: call.ID, Name: call.Name, Arguments: json.RawMessage(`{}`)})
	require.ErrorContains(t, err, "UNKNOWN")
	bridge.mu.Lock()
	closed := bridge.closed
	bridge.mu.Unlock()
	require.True(t, closed, "failed checked result must reject later assistant/final publication")
}

func TestClaudeEvaluatorRequiresConstructibleAuth(t *testing.T) {
	ctx := context.Background()
	db, _, _, _ := wakePersistenceFixtureWithClaim(t, false)
	m := &Manager{store: db}
	_, err := m.claudeCodeEvaluatorModel(ctx)
	require.ErrorContains(t, err, "unavailable")
	require.NoError(t, db.SetDefaultModel(ctx, &store.ManagedModel{Provider: "google", ID: "gemini-2.5-flash"}))
	// A whitespace environment and isolated HOME prevent ambient credentials.
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("HOME", t.TempDir())
	m.config.PiAgentDir = t.TempDir()
	m.config.ManagedHome = t.TempDir()
	_, err = m.claudeCodeEvaluatorModel(ctx)
	require.ErrorContains(t, err, "unavailable")
	t.Setenv("GEMINI_API_KEY", "deterministic-not-sent")
	model, err := m.claudeCodeEvaluatorModel(ctx)
	require.NoError(t, err)
	require.Equal(t, "google", string(model.Provider))
	require.Equal(t, "gemini-2.5-flash", model.ID)
}

func TestClaudeRuntimeMCPFailureCannotPublishLaterFinal(t *testing.T) {
	r, p, _, _ := claudeRuntimeFixture(t, `{"continue_after_mcp_error":true,"turns":[[{"mcp_call":{"id":"fail","name":"scratchpad","input":{}}},{"text":"must not publish"}]]}`)
	a := &embeddedTurnToolAuthority{persistence: p, beforeToolExecute: func(context.Context, agent.BeforeToolCallContext) error { return nil }}
	a.tools = []agent.AgentTool{{Name: "scratchpad", Execute: func(context.Context, string, map[string]any, agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
		p.binding.OwnerToken++
		return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: "domain changed"}}}, nil
	}}}
	bridge := newClaudeCodeTurnBridge(a)
	r.bridge = bridge
	r.toolProducer = bridge.producer
	bridge.fatal = func(err error) { r.process.fail(err); r.pool.stop(p.binding.AgentID) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var rpc struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string            `json:"name"`
				Arguments json.RawMessage   `json:"arguments"`
				Meta      map[string]string `json:"_meta"`
			} `json:"params"`
		}
		require.NoError(t, json.NewDecoder(request.Body).Decode(&rpc))
		w.Header().Set("Content-Type", "application/json")
		switch rpc.Method {
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-03-26"}}`, rpc.ID)
			return
		case "tools/list":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[]}}`, rpc.ID)
			return
		case "ping":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{}}`, rpc.ID)
			return
		case "tools/call":
		default:
			t.Errorf("unexpected MCP method %q", rpc.Method)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, err := bridge.execute(request.Context(), ClaudeCodeMCPCall{ToolUseID: rpc.Params.Meta["claudecode/toolUseId"], Name: rpc.Params.Name, Arguments: rpc.Params.Arguments})
		require.Error(t, err)
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":"UNKNOWN"}}`, rpc.ID)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "mcp.json")
	data, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"mecha": map[string]any{"type": "http", "url": server.URL, "headers": map[string]string{}}}})
	require.NoError(t, os.WriteFile(path, data, 0600))
	r.config.MCPConfigFile = path
	_, err := r.Run(context.Background(), "run tool")
	require.ErrorContains(t, err, "UNKNOWN")
	snap, err := p.db.ReopenEmbedded(context.Background(), func() store.EmbeddedBinding { b := p.binding; b.OwnerToken--; return b }())
	require.NoError(t, err)
	for _, entry := range snap.Entries {
		require.NotContains(t, string(entry.Body), "must not publish")
	}
}

func TestClaudeBridgeRequestCancellationSurvivesOwner(t *testing.T) {
	ctx := context.Background()
	db, b, _, _ := wakePersistenceFixtureWithClaim(t, false)
	p := &embeddedPersistence{db: db, binding: b, digest: "genesis"}
	a := &embeddedTurnToolAuthority{persistence: p, beforeToolExecute: func(context.Context, agent.BeforeToolCallContext) error { return nil }}
	a.tools = []agent.AgentTool{{Name: "scratchpad", Execute: func(ctx context.Context, _ string, _ map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
		return agent.AgentToolResult{}, ctx.Err()
	}}}
	bridge := newClaudeCodeTurnBridge(a)
	bridge.admitted = true
	ownerCtx := context.WithValue(ctx, embeddedOwnerContextKey{}, embeddedOwner{binding: b, identity: "wake-fixture"})
	require.NoError(t, bridge.producer(ownerCtx, ai.ToolCall{ID: "cancelled", Name: "scratchpad", Arguments: map[string]any{}}))
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err := bridge.execute(cancelled, ClaudeCodeMCPCall{ToolUseID: "cancelled", Name: "scratchpad", Arguments: json.RawMessage(`{}`)})
	require.Error(t, err)
}

func TestClaudeRuntimeRejectedSteerReservationReleasedOnInterrupt(t *testing.T) {
	r, p, _, _ := claudeRuntimeFixture(t, `{"turns":[[{"text":"partial"},{"wait":"interrupt"}]]}`)
	ctx := context.Background()
	require.NoError(t, p.db.ReconcileManagedRecipientSources(ctx))
	_, err := p.db.Enqueue(ctx, protocol.Envelope{ID: "pending-steer", From: "sender", To: p.binding.AgentID, Body: "queued mail", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")}, store.EnqueueOptions{})
	require.NoError(t, err)
	_, err = p.db.ResolveMessageTarget(ctx, "pending-steer")
	require.NoError(t, err)
	attempt, err := p.db.ReserveEmbeddedMail(ctx, p.binding, "pending-steer")
	require.NoError(t, err)
	appended := make(chan struct{})
	release := make(chan struct{})
	p.afterUserAppend = func(context.Context) { close(appended); <-release }
	done := make(chan error, 1)
	go func() { _, err := r.Run(ctx, "first"); done <- err }()
	<-appended
	require.False(t, r.TrySteer(buildSteerUserMessage(attempt, 2), attempt))
	// Rejection leaves reservation ownership with the caller.
	require.NoError(t, p.db.ReleaseEmbeddedUnconsumedAttempt(ctx, p.binding, attempt.AttemptID))
	r.Interrupt()
	close(release)
	require.ErrorIs(t, <-done, context.Canceled)
	fresh, err := p.db.ReserveEmbeddedMail(ctx, p.binding, "pending-steer")
	require.NoError(t, err)
	require.NotEqual(t, attempt.AttemptID, fresh.AttemptID, "unconsumed exact reservation released after run callbacks join")
}

func TestClaudeCodeMailContentPreservesCheckedEnvelopeAndImageBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "uploaded.jpg")
	data := []byte("exact uploaded image bytes")
	require.NoError(t, os.WriteFile(path, data, 0600))
	attempt := &store.EmbeddedMailAttempt{Body: "inspect", Attachments: []store.MailAttachment{{Path: path, MIMEType: "image/jpeg"}}}
	user, err := store.EmbeddedMailUserMessage(attempt, 123)
	require.NoError(t, err)
	before, err := json.Marshal(user)
	require.NoError(t, err)
	content := claudeCodeMailContent(user.Content, attempt)
	require.Len(t, content, 2)
	require.Equal(t, user.Content[0], content[0])
	raw, err := claudeCodeRender(content)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"type":"image"`)
	require.Contains(t, string(raw), `"media_type":"image/jpeg"`)
	require.Contains(t, string(raw), base64.StdEncoding.EncodeToString(data))
	after, err := json.Marshal(user)
	require.NoError(t, err)
	require.Equal(t, before, after)
	// Already translated steering input must not acquire duplicate blocks.
	require.Equal(t, content, claudeCodeMailContent(content, attempt))
	require.Equal(t, user.Content, claudeCodeMailContent(user.Content, nil))
	inline := &store.EmbeddedMailAttempt{Attachments: []store.MailAttachment{{Data: "eA==", MIMEType: "image/png"}}}
	require.Equal(t, ai.ImageContent{Data: "eA==", MimeType: "image/png"}, claudeCodeMailContent(user.Content, inline)[1])
}

// Interrupts before activation and before the first write must not submit the input.
func TestClaudeInterruptInitialAdmissionWindows(t *testing.T) {
	for _, window := range []string{"published", "before-write"} {
		t.Run(window, func(t *testing.T) {
			r, _, _, cfg := claudeRuntimeFixture(t, `{"turns":[[{"text":"later"}]]}`)
			entered, release := make(chan struct{}), make(chan struct{})
			if window == "before-write" {
				before := r.beforeSubmit
				calls := 0
				r.beforeSubmit = func(ctx context.Context, n int) error {
					calls++
					if calls == 2 {
						close(entered)
						<-release
					}
					return before(ctx, n)
				}
			} else {
				r.Interrupt()
			}
			done := make(chan error, 1)
			go func() { _, err := r.Run(context.Background(), "revoked"); done <- err }()
			if window == "before-write" {
				<-entered
				r.Interrupt()
				close(release)
			}
			select {
			case err := <-done:
				require.ErrorIs(t, err, context.Canceled)
			case <-time.After(5 * time.Second):
				t.Fatal("interrupted run did not finish")
			}
			data, _ := os.ReadFile(filepath.Join(cfg.Cwd, "record.jsonl"))
			require.NotContains(t, string(data), "revoked")
			_, err := r.Run(context.Background(), "later")
			require.NoError(t, err)
			require.Equal(t, "later", r.Snapshot().LastAssistantText)
		})
	}
}

// A reset revokes admission; interrupt must leave queued input unsubmitted and unconsumed.
func TestClaudeCodeRuntimeInterruptDoesNotSubmitRevokedExecution(t *testing.T) {
	r, p, _, cfg := claudeRuntimeFixture(t, `{"turns":[[{"text":"partial"},{"wait":"interrupt"}],[{"text":"must not be checked"}]]}`)
	originalBefore := r.beforeSubmit
	var revoked atomic.Bool
	r.beforeSubmit = func(ctx context.Context, size int) error {
		if revoked.Load() {
			return errors.New("reset pending")
		}
		return originalBefore(ctx, size)
	}
	appended := make(chan struct{}, 2)
	p.afterUserAppend = func(context.Context) { appended <- struct{}{} }
	done := make(chan error, 1)
	go func() { _, err := r.Run(context.Background(), "first"); done <- err }()
	<-appended
	require.False(t, r.TrySteer(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "queued"}}}, nil))
	require.Eventually(t, func() bool {
		data, _ := os.ReadFile(filepath.Join(cfg.Cwd, "record.jsonl"))
		return strings.Count(string(data), `\"type\":\"user\"`) == 1
	}, time.Second, 10*time.Millisecond)
	revoked.Store(true)
	r.Interrupt()
	require.ErrorIs(t, <-done, context.Canceled)
	data, err := os.ReadFile(filepath.Join(cfg.Cwd, "record.jsonl"))
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(data), `\"type\":\"user\"`))
	snap, err := p.db.ReopenEmbedded(context.Background(), p.binding)
	require.NoError(t, err)
	require.Len(t, snap.Entries, 2, "only the first user and interrupted answer are consumed")
}

func TestClaudeToolResetOptionalArguments(t *testing.T) {
	for _, raw := range []string{`{}`, `{"reason":"fresh"}`, `{"window":20}`, `{"reason":"fresh","window":20}`} {
		t.Run(raw, func(t *testing.T) {
			ctx := context.Background()
			db, b, _, _ := wakePersistenceFixtureWithClaim(t, false)
			m := &Manager{store: db}
			p := &embeddedPersistence{db: db, binding: b, digest: "genesis"}
			a, err := m.newEmbeddedTurnToolAuthority(ctx, embeddedOwner{binding: b, identity: "wake-fixture"}, p, SavedAgentSession{Cwd: t.TempDir()}, false)
			require.NoError(t, err)
			require.NoError(t, a.AppendCheckedMessage(ctx, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "reset"}}}))
			var args map[string]any
			require.NoError(t, json.Unmarshal([]byte(raw), &args))
			call := ai.ToolCall{ID: "reset-now", Name: "context_reset", Arguments: args}
			require.NoError(t, a.AppendCheckedMessage(ctx, ai.AssistantMessage{Content: ai.ContentList{call}, StopReason: ai.StopToolUse}))
			bridge := newClaudeCodeTurnBridge(a)
			bridge.admitted = true
			require.NoError(t, bridge.producer(ctx, call))
			result, err := bridge.execute(ctx, ClaudeCodeMCPCall{ToolUseID: call.ID, Name: call.Name, Arguments: json.RawMessage(raw)})
			require.NoError(t, err)
			require.Contains(t, string(result), "Context reset initiated")
			require.True(t, m.laneFor(b.AgentID).embeddedResetPending())
			require.Equal(t, 20, m.contextResetRequested[b.AgentID].window)
			snap, err := db.ReopenEmbedded(ctx, b)
			require.NoError(t, err)
			require.Equal(t, "toolResult", snap.Entries[len(snap.Entries)-1].Kind)
		})
	}
	for _, raw := range []string{`{"reason":20}`, `{"window":"20"}`, `{"window":1.5}`, `{"other":true}`} {
		t.Run(raw, func(t *testing.T) {
			var args map[string]any
			require.NoError(t, json.Unmarshal([]byte(raw), &args))
			require.False(t, embeddedContextResetSchema().Check(args))
		})
	}
}

func TestClaudeUsageHoldUserInterrupt(t *testing.T) {
	reset := time.Now().Add(15 * time.Minute).Truncate(time.Second)
	r, p, pool, _ := claudeRuntimeFixture(t, fmt.Sprintf(`{"turns":[[{"rate_limit":{"resetsAt":%d}}],[{"text":"next answer"}]]}`, reset.Unix()))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := r.Run(ctx, "limited user"); done <- err }()
	require.Eventually(t, func() bool {
		snap, err := p.db.ReopenEmbedded(context.Background(), p.binding)
		if err != nil {
			return false
		}
		for _, e := range snap.Entries {
			if strings.Contains(string(e.Body), "Claude usage limit reached") {
				return true
			}
		}
		return false
	}, 3*time.Second, 10*time.Millisecond)
	r.Interrupt()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("interrupt did not end hold")
	}
	require.True(t, pool.reusable(p.binding))
	_, err := r.Run(context.Background(), "next user")
	require.NoError(t, err)
	require.Equal(t, "next answer", r.Snapshot().LastAssistantText)
}

func TestClaudeUsageRetryInterruptBeforeWrite(t *testing.T) {
	r, p, pool, cfg := claudeRuntimeFixture(t, `{"turns":[[{"rate_limit":{"resetsAt":1}}],[{"text":"must not retry"}]]}`)
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	original := r.beforeSubmit
	var calls atomic.Int32
	r.beforeSubmit = func(ctx context.Context, n int) error {
		if calls.Add(1) == 3 {
			close(entered)
			<-release
		}
		return original(ctx, n)
	}
	done := make(chan error, 1)
	go func() { _, err := r.Run(context.Background(), "limited"); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("retry did not reach admission")
	}
	snapBefore, err := p.db.ReopenEmbedded(context.Background(), p.binding)
	require.NoError(t, err)
	var notice bool
	for _, e := range snapBefore.Entries {
		notice = notice || strings.Contains(string(e.Body), "usage limit reached; resuming")
	}
	require.True(t, notice, "usage notice must be durable before retry admission")
	records, err := os.ReadFile(filepath.Join(cfg.Cwd, "record.jsonl"))
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(records), `\"type\":\"user\"`), "initial input submitted once before retry")
	require.Contains(t, string(records), "limited")
	r.Interrupt()
	close(release)
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("retry did not stop")
	}
	require.True(t, pool.reusable(p.binding))
	// The fake records stdin before acknowledging controls. A control sent after
	// Run returns therefore drains any retry write before inspecting the record.
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelDrain()
	require.NoError(t, r.process.Interrupt(drainCtx))
	records, err = os.ReadFile(filepath.Join(cfg.Cwd, "record.jsonl"))
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(records), `\"type\":\"user\"`), "retry must not reach stdin")
	snap, err := p.db.ReopenEmbedded(context.Background(), p.binding)
	require.NoError(t, err)
	for _, e := range snap.Entries {
		require.NotContains(t, string(e.Body), "must not retry")
	}
}

func TestClaudeUsageHoldInterruptControlJoined(t *testing.T) {
	r, p, _, _ := claudeRuntimeFixture(t, fmt.Sprintf(`{"turns":[[{"rate_limit":{"resetsAt":%d}}],[{"text":"next"}]]}`, time.Now().Add(time.Hour).Unix()))
	done := make(chan error, 1)
	go func() { _, e := r.Run(context.Background(), "first"); done <- e }()
	require.Eventually(t, func() bool {
		s, e := p.db.ReopenEmbedded(context.Background(), p.binding)
		if e != nil {
			return false
		}
		for _, v := range s.Entries {
			if strings.Contains(string(v.Body), "usage limit reached; resuming") {
				return true
			}
		}
		return false
	}, 3*time.Second, 10*time.Millisecond)
	r.process.writeMu.Lock()
	interrupted := make(chan struct{})
	go func() { r.Interrupt(); close(interrupted) }()
	require.Eventually(t, func() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.interrupted }, time.Second, time.Millisecond)
	var early error
	returned := false
	select {
	case early = <-done:
		returned = true
		t.Error("turn returned before interrupt control completed")
	case <-time.After(100 * time.Millisecond):
	}
	r.process.writeMu.Unlock()
	<-interrupted
	if returned {
		require.ErrorIs(t, early, context.Canceled)
	} else {
		require.ErrorIs(t, <-done, context.Canceled)
	}
	_, err := r.Run(context.Background(), "next")
	require.NoError(t, err)
}

func TestClaudeUsageLimitAfterInterruptedTurnCanBeInterrupted(t *testing.T) {
	reset := time.Now().Add(time.Hour).Unix()
	r, p, _, cfg := claudeRuntimeFixture(t, fmt.Sprintf(`{"turns":[[{"wait":"interrupt"}],[{"rate_limit":{"resetsAt":%d}}]]}`, reset))
	appended := make(chan struct{}, 2)
	p.afterUserAppend = func(context.Context) { appended <- struct{}{} }
	done := make(chan error, 1)
	go func() { _, e := r.Run(context.Background(), "first"); done <- e }()
	<-appended
	require.False(t, r.TrySteer(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "steer"}}}, nil))
	require.Eventually(t, func() bool {
		b, _ := os.ReadFile(filepath.Join(cfg.Cwd, "record.jsonl"))
		return strings.Count(string(b), `\"type\":\"user\"`) == 1
	}, 3*time.Second, 10*time.Millisecond)
	r.Interrupt()
	select {
	case e := <-done:
		require.ErrorIs(t, e, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("first turn did not interrupt")
	}
	b, err := os.ReadFile(filepath.Join(cfg.Cwd, "record.jsonl"))
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(b), `\"type\":\"user\"`), "unsubmitted steer must not be sent after interrupt")
	go func() { _, e := r.Run(context.Background(), "next"); done <- e }()
	require.Eventually(t, func() bool {
		s, e := p.db.ReopenEmbedded(context.Background(), p.binding)
		if e != nil {
			return false
		}
		for _, v := range s.Entries {
			if strings.Contains(string(v.Body), "usage limit reached; resuming") {
				return true
			}
		}
		return false
	}, 3*time.Second, 10*time.Millisecond)
	select {
	case e := <-done:
		t.Fatalf("hold ended without second interrupt: %v", e)
	default:
	}
	r.Interrupt()
	select {
	case e := <-done:
		require.ErrorIs(t, e, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("hold did not interrupt")
	}
}

func TestClaudeUsageLimitMissingResetFallback(t *testing.T) {
	before := time.Now().Add(15 * time.Minute)
	limit := claudeCodeLimit(json.RawMessage(`{"status":"rejected"}`))
	require.False(t, limit.ResetsAt.Before(before))
	require.WithinDuration(t, before, limit.ResetsAt, time.Second)
}

func TestClaudeUsageLimitRetryPreservesSeedAndImage(t *testing.T) {
	r, _, _, cfg := claudeRuntimeFixture(t, `{"turns":[[{"rate_limit":{"resetsAt":1}}],[{"text":"done"}]]}`)
	r.seedHistory = []agent.AgentMessage{ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "historical seed marker"}}, Timestamp: 1}}
	user := ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "wake"}, ai.ImageContent{Data: "eA==", MimeType: "image/png"}}, Timestamp: 2}
	_, err := r.RunMessages(context.Background(), []agent.AgentMessage{user})
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(cfg.Cwd, "record.jsonl"))
	require.NoError(t, err)
	var inputs []json.RawMessage
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var record struct {
			Kind string `json:"kind"`
			Line string `json:"line"`
		}
		if json.Unmarshal([]byte(line), &record) == nil && record.Kind == "stdin" {
			var m struct {
				Type    string `json:"type"`
				Message struct {
					Content json.RawMessage `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal([]byte(record.Line), &m) == nil && m.Type == "user" {
				inputs = append(inputs, m.Message.Content)
			}
		}
	}
	require.Len(t, inputs, 2)
	require.Contains(t, string(inputs[0]), "historical seed marker")
	require.Contains(t, string(inputs[0]), "image/png")
	require.JSONEq(t, string(inputs[0]), string(inputs[1]))
}

func TestClaudeUsageLimitDelayBeyondDay(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.Equal(t, 49*time.Hour, claudeUsageLimitDelay(now, now.Add(49*time.Hour)))
	require.Zero(t, claudeUsageLimitDelay(now, now.Add(-time.Second)))
}

func TestClaudeStartupInterruptPreservesSeed(t *testing.T) {
	for _, barrierCall := range []int{1, 2} {
		t.Run(fmt.Sprintf("barrier-%d", barrierCall), func(t *testing.T) {
			r, _, _, cfg := claudeRuntimeFixture(t, `{"turns":[[{"text":"done"}]]}`)
			r.seedHistory = []agent.AgentMessage{ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "retained seed marker"}}, Timestamp: 1}}
			// Interrupt during preparation or after activation before the first write.
			entered, release := make(chan struct{}), make(chan struct{})
			before := r.beforeSubmit
			calls := 0
			r.beforeSubmit = func(ctx context.Context, n int) error {
				calls++
				if calls == barrierCall {
					close(entered)
					<-release
				}
				return before(ctx, n)
			}
			done := make(chan error, 1)
			go func() { _, err := r.Run(context.Background(), "revoked"); done <- err }()
			<-entered
			r.Interrupt()
			close(release)
			require.ErrorIs(t, <-done, context.Canceled)
			_, err := r.Run(context.Background(), "next wake")
			require.NoError(t, err)
			data, err := os.ReadFile(filepath.Join(cfg.Cwd, "record.jsonl"))
			require.NoError(t, err)
			require.NotContains(t, string(data), "revoked")
			require.Contains(t, string(data), "retained seed marker")
		})
	}
}

func TestClaudePreparationFailureClearsInterrupt(t *testing.T) {
	r, _, _, _ := claudeRuntimeFixture(t, `{"turns":[[{"text":"done"}]]}`)
	entered, release := make(chan struct{}), make(chan struct{})
	before := r.beforeSubmit
	calls := 0
	r.beforeSubmit = func(ctx context.Context, n int) error {
		calls++
		if calls == 1 {
			close(entered)
			<-release
			return errors.New("preparation failed")
		}
		return before(ctx, n)
	}
	done := make(chan error, 1)
	go func() { _, err := r.Run(context.Background(), "failed"); done <- err }()
	<-entered
	r.Interrupt()
	close(release)
	require.ErrorContains(t, <-done, "preparation failed")
	_, err := r.Run(context.Background(), "next wake")
	require.NoError(t, err)
	require.Equal(t, "done", r.Snapshot().LastAssistantText)
}

// A full OS pipe must not keep the runtime lock away from Interrupt.
func TestClaudeInterruptBlockedRealStdin(t *testing.T) {
	r, _, _, _ := claudeRuntimeFixture(t, `{}`)
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	defer reader.Close()
	proc := &ClaudeCodeProcess{stdin: writer, inputs: make(map[string]claudeCodePendingInput), controls: make(map[string]chan claudeCodeControlResponse), notify: make(chan struct{}, 1), done: make(chan struct{})}
	r.mu.Lock()
	r.process = proc
	r.active = true
	r.interruptNotify = make(chan struct{})
	r.mu.Unlock()
	raw, err := json.Marshal(strings.Repeat("x", 1024*1024))
	require.NoError(t, err)
	submitted := make(chan error, 1)
	go func() { submitted <- r.submitInput(context.Background(), "blocked", raw) }()
	require.Eventually(t, func() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.inputCancel != nil }, time.Second, time.Millisecond)
	// Reading one byte proves the OS pipe write began; the remaining megabyte cannot fit.
	var first [1]byte
	_, err = reader.Read(first[:])
	require.NoError(t, err)
	interrupted := make(chan struct{})
	go func() { r.Interrupt(); close(interrupted) }()
	select {
	case <-interrupted:
	case <-time.After(3 * time.Second):
		t.Fatal("interrupt blocked on stdin")
	}
	select {
	case err := <-submitted:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("write did not unblock")
	}
	require.Error(t, proc.Err(), "partial write must poison the process")
	r.mu.Lock()
	r.active = false
	r.process = nil
	r.mu.Unlock()
}

func TestClaudeDeadEntryUsesRequestedResume(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(fmt.Sprint(resume), func(t *testing.T) {
			cfg := claudeTestConfig(t, `{}`)
			pool := &claudeCodeProcessPool{}
			b := store.EmbeddedBinding{AgentID: "dead", SessionID: cfg.SessionID}
			old, err := pool.acquire(context.Background(), b, cfg, nil)
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				data, _ := os.ReadFile(filepath.Join(cfg.Cwd, "record.jsonl"))
				return strings.Count(string(data), `"-p"`) >= 1
			}, 3*time.Second, 10*time.Millisecond)
			require.NoError(t, old.Close())
			cfg.Resume = resume
			replacement, err := pool.acquire(context.Background(), b, cfg, nil)
			require.NoError(t, err)
			defer pool.stop(b.AgentID)
			require.NotSame(t, old, replacement)
			require.Eventually(t, func() bool {
				data, _ := os.ReadFile(filepath.Join(cfg.Cwd, "record.jsonl"))
				return strings.Count(string(data), `"-p"`) >= 2
			}, 3*time.Second, 10*time.Millisecond)
			data, err := os.ReadFile(filepath.Join(cfg.Cwd, "record.jsonl"))
			require.NoError(t, err)
			var starts []struct {
				Kind string   `json:"kind"`
				Argv []string `json:"argv"`
			}
			for _, line := range strings.Split(string(data), "\n") {
				var row struct {
					Kind string   `json:"kind"`
					Argv []string `json:"argv"`
				}
				if json.Unmarshal([]byte(line), &row) == nil && row.Kind == "start" && len(row.Argv) > 0 && row.Argv[0] == "-p" {
					starts = append(starts, row)
				}
			}
			require.Len(t, starts, 2)
			if resume {
				require.Contains(t, starts[1].Argv, "--resume")
			} else {
				require.NotContains(t, starts[1].Argv, "--resume")
			}
		})
	}
}

// Both producers are registered before the terminating call completes. Fake only
// the terminal tool; the second call uses the real scratchpad and checked store.
func TestClaudeBridgeTerminalFencesPreregisteredCall(t *testing.T) {
	for _, name := range []string{"context_reset", "mecha_worker_terminate"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db, binding, _, _ := wakePersistenceFixtureWithClaim(t, false)
			m := &Manager{store: db}
			p := &embeddedPersistence{db: db, binding: binding, digest: "genesis"}
			a, err := m.newEmbeddedTurnToolAuthority(ctx, embeddedOwner{binding: binding, identity: "wake-fixture"}, p, SavedAgentSession{Cwd: t.TempDir()}, false)
			require.NoError(t, err)
			// The terminal result is deterministic; the bridge, persistence, and second tool are real.
			for i := len(a.tools) - 1; i >= 0; i-- {
				if a.tools[i].Name == name {
					a.tools = append(a.tools[:i], a.tools[i+1:]...)
				}
			}
			a.tools = append(a.tools, agent.AgentTool{Name: name, Execute: func(context.Context, string, map[string]any, agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
				return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: "terminating"}}, Terminate: true}, nil
			}})
			originalBefore := a.beforeToolExecute
			a.beforeToolExecute = func(ctx context.Context, c agent.BeforeToolCallContext) error {
				if c.ToolCall.Name == name {
					return nil
				}
				return originalBefore(ctx, c)
			}
			require.NoError(t, a.AppendCheckedMessage(ctx, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "test"}}}))
			terminal := ai.ToolCall{ID: "terminal", Name: name, Arguments: map[string]any{}}
			second := ai.ToolCall{ID: "second", Name: "scratchpad", Arguments: map[string]any{"action": "append", "content": "must not exist"}}
			require.NoError(t, a.AppendCheckedMessage(ctx, ai.AssistantMessage{Content: ai.ContentList{terminal, second}, StopReason: ai.StopToolUse}))
			bridge := newClaudeCodeTurnBridge(a)
			bridge.admitted = true
			require.NoError(t, bridge.producer(ctx, terminal))
			require.NoError(t, bridge.producer(ctx, second))
			published := 0
			p.publishComplete = func(store.ManagedHistoryEntry) { published++ }
			req := ClaudeCodeMCPCall{ToolUseID: terminal.ID, Name: name, Arguments: json.RawMessage(`{}`)}
			first, err := bridge.execute(ctx, req)
			require.NoError(t, err)
			snap, err := db.ReopenEmbedded(ctx, binding)
			require.NoError(t, err)
			beforeEntries, beforePublished := len(snap.Entries), published
			args, err := json.Marshal(second.Arguments)
			require.NoError(t, err)
			other := ClaudeCodeMCPCall{ToolUseID: second.ID, Name: second.Name, Arguments: args}
			_, err = bridge.execute(ctx, other)
			require.ErrorContains(t, err, "terminal")
			_, err = bridge.execute(ctx, other)
			require.ErrorContains(t, err, "terminal")
			replay, err := bridge.execute(ctx, req)
			require.NoError(t, err)
			require.Equal(t, first, replay)
			req.Arguments = json.RawMessage(`{"different":true}`)
			_, err = bridge.execute(ctx, req)
			require.ErrorContains(t, err, "conflict")
			count, err := db.CountScratchpadEntries(ctx, binding.AgentID)
			require.NoError(t, err)
			require.Zero(t, count)
			snap, err = db.ReopenEmbedded(ctx, binding)
			require.NoError(t, err)
			require.Len(t, snap.Entries, beforeEntries)
			require.Equal(t, beforePublished, published)
			_, err = db.RecoverEmbeddedEffect(ctx, binding, second.ID)
			require.ErrorIs(t, err, sql.ErrNoRows)
		})
	}
}
