package managed

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

// One bridge per turn. Both stdout and HTTP use serial for checked persistence;
// producer rendezvous waits outside serial so HTTP can never block its producer.
// State is bounded by the process event/input limits and explicitly capped here.
type claudeCodeTurnBridge struct {
	serial      sync.Mutex
	mu          sync.Mutex
	changed     chan struct{}
	closed      bool
	admitted    bool
	authority   *embeddedTurnToolAuthority
	inlineCheck func(context.Context, string) error
	calls       map[string]*claudeCodeCheckedCall
	bytes       int
	interrupt   func()
	owner       *embeddedOwner
	fatal       func(error)
	failure     error
	terminal    bool // protected by serial; checked terminating result is durable
}
type claudeCodeCheckedCall struct {
	call     ai.ToolCall
	result   *ai.ToolResultMessage
	started  bool
	original json.RawMessage
}

func newClaudeCodeTurnBridge(a *embeddedTurnToolAuthority) *claudeCodeTurnBridge {
	return &claudeCodeTurnBridge{authority: a, changed: make(chan struct{}), calls: make(map[string]*claudeCodeCheckedCall)}
}
func (b *claudeCodeTurnBridge) signal() { close(b.changed); b.changed = make(chan struct{}) }
func (b *claudeCodeTurnBridge) close()  { b.mu.Lock(); b.closed = true; b.signal(); b.mu.Unlock() }
func (b *claudeCodeTurnBridge) producer(ctx context.Context, call ai.ToolCall) error {
	raw, err := json.Marshal(call)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failure != nil {
		return b.failure
	}
	if b.closed {
		return errors.New("no admitted turn")
	}
	if old := b.calls[call.ID]; old != nil {
		return errors.New("duplicate Claude tool producer")
	}
	if len(b.calls) >= 128 || b.bytes+len(raw) > 16<<20 {
		return errors.New("Claude tool producer buffer overflow")
	}
	var copied ai.ToolCall
	if err := json.Unmarshal(raw, &copied); err != nil {
		return err
	}
	args, err := json.Marshal(copied.Arguments)
	if err != nil {
		return err
	}
	if owner, ok := ctx.Value(embeddedOwnerContextKey{}).(embeddedOwner); ok && b.owner == nil {
		b.owner = &owner
	}
	b.bytes += len(raw)
	b.calls[call.ID] = &claudeCodeCheckedCall{call: copied, original: args}
	b.signal()
	return nil
}
func (b *claudeCodeTurnBridge) execute(ctx context.Context, request ClaudeCodeMCPCall) (json.RawMessage, error) {
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		b.mu.Lock()
		call := b.calls[request.ToolUseID]
		closed := b.closed
		failure := b.failure
		changed := b.changed
		b.mu.Unlock()
		if failure != nil {
			return nil, failure
		}
		if closed {
			return nil, errors.New("no admitted turn")
		}
		if call != nil {
			break
		}
		select {
		case <-waitCtx.Done():
			return nil, errors.New("no checked main-thread tool producer")
		case <-changed:
		}
	}
	b.serial.Lock()
	defer b.serial.Unlock()
	b.mu.Lock()
	item := b.calls[request.ToolUseID]
	admitted := b.admitted && !b.closed
	failure := b.failure
	b.mu.Unlock()
	if failure != nil {
		return nil, failure
	}
	if !admitted {
		return nil, errors.New("no admitted turn")
	}
	if item.call.Name != request.Name || !claudeCodeJSONEqual(item.original, request.Arguments) {
		return nil, errors.New("tool ID argument conflict")
	}
	// A same-turn checked result is the only replayable result. Historical
	// evidence never grants execution or reconstructs a response.
	if item.result != nil {
		return claudeCodeMCPResult(*item.result)
	}
	if b.terminal {
		return nil, errors.New("turn has a checked terminal result")
	}
	if item.started {
		return nil, errors.New("tool outcome UNKNOWN; will not repeat execution")
	}
	if p := b.authority.persistence; p != nil && p.db != nil {
		_, err := p.db.RecoverEmbeddedEffect(ctx, p.binding, request.ToolUseID)
		if err == nil {
			return nil, errors.New("prior tool effect exists; will not repeat execution")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	item.started = true
	if b.owner != nil {
		ctx = context.WithValue(ctx, embeddedOwnerContextKey{}, *b.owner)
	}
	call := item.call
	call.Arguments = nil
	if err := json.Unmarshal(item.original, &call.Arguments); err != nil {
		return nil, err
	}
	if b.inlineCheck != nil {
		if err := b.inlineCheck(ctx, call.Name); err != nil {
			message := ai.ToolResultMessage{ToolCallID: call.ID, ToolName: call.Name, Timestamp: time.Now().UnixMilli(), IsError: true, Content: ai.ContentList{ai.TextContent{Text: err.Error()}}}
			if appendErr := b.authority.AppendCheckedMessage(ctx, message); appendErr != nil {
				b.fail(appendErr)
				return nil, appendErr
			}
			item.result = &message
			return claudeCodeMCPResult(message)
		}
	}
	if err := b.authority.BeforeToolExecute(ctx, agent.BeforeToolCallContext{ToolCall: call}); err != nil {
		var denial *agent.PolicyDenial
		if errors.As(err, &denial) {
			message := ai.ToolResultMessage{ToolCallID: call.ID, ToolName: call.Name, Timestamp: time.Now().UnixMilli(), IsError: true, Content: ai.ContentList{ai.TextContent{Text: err.Error()}}}
			if appendErr := b.authority.AppendCheckedMessage(ctx, message); appendErr != nil {
				b.fail(appendErr)
				return nil, appendErr
			}
			item.result = &message
			return claudeCodeMCPResult(message)
		}
		var observerError *embeddedEffectObserverError
		if errors.As(err, &observerError) {
			b.fail(err)
		}
		return nil, err
	}
	var tool *agent.AgentTool
	candidates := b.authority.baseTools
	if candidates == nil {
		candidates = b.authority.tools
	}
	for _, candidate := range candidates {
		if candidate.Name == request.Name {
			copy := candidate
			tool = &copy
			break
		}
	}
	if tool == nil {
		return nil, errors.New("tool unavailable")
	}
	if b.authority.observerFailure != nil {
		if err := b.authority.observerFailure(); err != nil {
			b.fail(err)
			return nil, err
		}
	}
	result, executeErr := tool.Execute(ctx, call.ID, call.Arguments, nil)
	message := ai.ToolResultMessage{ToolCallID: item.call.ID, ToolName: item.call.Name, Timestamp: time.Now().UnixMilli()}
	message.Content = result.Content
	message.Details = result.Details
	if executeErr != nil {
		message.IsError = true
		message.Content = ai.ContentList{ai.TextContent{Text: executeErr.Error()}}
	}
	if message.Content == nil {
		message.Content = ai.ContentList{}
	}
	if err := b.authority.AppendCheckedMessage(ctx, message); err != nil {
		failure := fmt.Errorf("tool outcome UNKNOWN after execution: %w", err)
		b.fail(failure)
		return nil, failure
	}
	if executeErr == nil && result.Terminate {
		b.terminal = true
		if b.interrupt != nil {
			go b.interrupt()
		}
	}
	rawResult, err := claudeCodeMCPResult(message)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	if b.bytes+len(rawResult) > 16<<20 {
		b.mu.Unlock()
		failure := errors.New("Claude checked result buffer overflow")
		b.fail(failure)
		return nil, failure
	}
	b.bytes += len(rawResult)
	b.mu.Unlock()
	item.result = &message
	return claudeCodeMCPResult(message)
}
func claudeCodeMCPResult(result ai.ToolResultMessage) (json.RawMessage, error) {
	return json.Marshal(struct {
		Content ai.ContentList `json:"content"`
		IsError bool           `json:"isError,omitempty"`
	}{result.Content, result.IsError})
}
func (b *claudeCodeTurnBridge) observeResult(ctx context.Context, result ai.ToolResultMessage) error {
	b.mu.Lock()
	call := b.calls[result.ToolCallID]
	b.mu.Unlock()
	if call == nil || call.result == nil {
		return errors.New("router tool result has no checked HTTP execution")
	}
	// CLI flattens MCP structured content; compare the actual rendered content,
	// error flag and identity, not its fresh timestamp or optional SDK details.
	if call.result.ToolName != result.ToolName || call.result.IsError != result.IsError || claudeCodeMessageText(*call.result) != claudeCodeMessageText(result) {
		return errors.New("Claude stdout differs from checked MCP result")
	}
	return nil
}

// A process grant attaches to at most one turn. Detaching closes all old
// callbacks. No previous turn execution token can be reused while idle.
type claudeCodeGrantBinding struct {
	mu          sync.Mutex
	turn        *claudeCodeTurnBridge
	incarnation string
	changed     chan struct{}
}

func (g *claudeCodeGrantBinding) attach(turn *claudeCodeTurnBridge) {
	g.mu.Lock()
	g.turn = turn
	if g.changed != nil {
		close(g.changed)
	}
	g.changed = make(chan struct{})
	g.mu.Unlock()
}
func (g *claudeCodeGrantBinding) detach(turn *claudeCodeTurnBridge) {
	g.mu.Lock()
	if g.turn == turn {
		g.turn = nil
	}
	g.mu.Unlock()
	turn.close()
}
func claudeCodeForegroundOnly(name string) bool {
	return name == "context_reset" || name == "goal_snooze" || name == "mecha_worker_terminate"
}
func (g *claudeCodeGrantBinding) execute(ctx context.Context, request ClaudeCodeMCPCall) (json.RawMessage, error) {
	// This deadline only bounds waiting for attachment, not tool execution or
	// checked result persistence. Both retain the caller/grant lifetime.
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		g.mu.Lock()
		turn := g.turn
		if g.changed == nil {
			g.changed = make(chan struct{})
		}
		changed := g.changed
		g.mu.Unlock()
		if turn != nil {
			if g.incarnation != "" {
				request.ToolUseID = g.canonicalID(request.ToolUseID)
			}
			return turn.execute(ctx, request)
		}
		if claudeCodeForegroundOnly(request.Name) {
			return nil, errors.New("tool requires foreground turn")
		}
		select {
		case <-waitCtx.Done():
			return nil, errors.New("no admitted turn")
		case <-changed:
		}
	}
}

func (g *claudeCodeGrantBinding) canonicalID(raw string) string {
	if g.incarnation == "" {
		return raw
	}
	return fmt.Sprintf("claude-%x", sha256.Sum256([]byte(g.incarnation+":"+raw)))
}

func (b *claudeCodeTurnBridge) fail(err error) {
	b.mu.Lock()
	if b.failure == nil {
		b.failure = err
	}
	b.closed = true
	b.signal()
	b.mu.Unlock()
	if b.fatal != nil {
		b.fatal(err)
	}
}
func (b *claudeCodeTurnBridge) err() error { b.mu.Lock(); defer b.mu.Unlock(); return b.failure }
