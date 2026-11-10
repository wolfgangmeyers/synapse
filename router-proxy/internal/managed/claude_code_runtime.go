package managed

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

// Grant lifetime is the process incarnation, not the turn. A factory must create
// private configuration files and revoke authority/remove them on Revoke.
type claudeCodeProcessGrant struct {
	MCPConfigFile    string
	SystemPromptFile string
	Binding          *claudeCodeGrantBinding
	Revoke           func()
}
type claudeCodeProcessEntry struct {
	binding      store.EmbeddedBinding
	process      *ClaudeCodeProcess
	revoke       func()
	grantBinding *claudeCodeGrantBinding
	stopping     chan struct{}
	stopErr      error
}
type claudeCodeProcessPool struct {
	mu            sync.Mutex
	entries       map[string]*claudeCodeProcessEntry
	closed        bool
	starts        sync.WaitGroup
	startErr      error
	fencedWorkers map[string]error
	afterStart    func(*ClaudeCodeProcess) // optional synchronization at the start/insert boundary
}

// admitExternal registers non-pooled Claude work before it can create files or
// processes. The returned release must follow all process and file cleanup.
func (pool *claudeCodeProcessPool) admitExternal() (func(), error) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.closed {
		return nil, &ClaudeAdmissionFencedError{Cause: errors.Join(errClaudeDefiniteStart, pool.startErr)}
	}
	pool.starts.Add(1)
	var once sync.Once
	return func() { once.Do(pool.starts.Done) }, nil
}

// Worker-scoped admission shares the same lock as shutdown and gate release.
func (pool *claudeCodeProcessPool) workerErrorLocked(id string) error {
	if pool.closed {
		return &ClaudeAdmissionFencedError{Cause: pool.startErr}
	}
	if err := pool.fencedWorkers[id]; err != nil {
		return &ClaudeAdmissionFencedError{Cause: err}
	}
	return nil
}
func (pool *claudeCodeProcessPool) admitWorker(id string) (func(), error) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if err := pool.workerErrorLocked(id); err != nil {
		return nil, err
	}
	pool.starts.Add(1)
	var once sync.Once
	return func() { once.Do(pool.starts.Done) }, nil
}
func (pool *claudeCodeProcessPool) workerGate(id string) func(func() (int, error)) (int, error) {
	return func(write func() (int, error)) (int, error) {
		pool.mu.Lock()
		defer pool.mu.Unlock()
		if err := pool.workerErrorLocked(id); err != nil {
			return 0, err
		}
		return write()
	}
}

// Retain at most MaxManagedAgents worker fences for the pool lifetime. Overflow
// closes global admission rather than evicting a fence. The first error wins.
var errClaudeWorkerFenceCapacity = errors.New("Claude worker fence capacity exceeded")

func (pool *claudeCodeProcessPool) fenceWorker(id string, err error) {
	if id == "" {
		pool.fenceStartup(err)
		return
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	pool.fenceWorkerLocked(id, err)
}

// fenceWorkerLocked requires pool.mu.
func (pool *claudeCodeProcessPool) fenceWorkerLocked(id string, err error) {
	if _, exists := pool.fencedWorkers[id]; exists {
		return
	}
	if len(pool.fencedWorkers) >= MaxManagedAgents {
		pool.closed = true
		if !errors.Is(pool.startErr, errClaudeWorkerFenceCapacity) {
			pool.startErr = errors.Join(pool.startErr, errClaudeWorkerFenceCapacity)
		}
		return
	}
	if pool.fencedWorkers == nil {
		pool.fencedWorkers = make(map[string]error)
	}
	pool.fencedWorkers[id] = err
}

// releaseGate linearizes the pipe write with fencing. Never run cleanup or
// persistence under this lock: cleanup itself may fence the pool.
func (pool *claudeCodeProcessPool) releaseGate(write func() (int, error)) (int, error) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.closed {
		return 0, &ClaudeAdmissionFencedError{Cause: pool.startErr}
	}
	return write()
}

func (pool *claudeCodeProcessPool) closeAdmission() {
	pool.mu.Lock()
	pool.closed = true
	pool.mu.Unlock()
}

// fenceStartup records the sanitized startup recovery failure without claiming
// that a process group was unconfirmed.
func (pool *claudeCodeProcessPool) fenceStartup(err error) {
	pool.mu.Lock()
	pool.closed = true
	pool.startErr = errors.Join(pool.startErr, err)
	pool.mu.Unlock()
}

func (pool *claudeCodeProcessPool) fence() {
	pool.mu.Lock()
	pool.closed = true
	pool.startErr = errors.Join(pool.startErr, errClaudeCodeGroupUnconfirmed)
	pool.mu.Unlock()
}

type claudeStartCleanupError struct{ err error }

func (e *claudeStartCleanupError) Error() string { return e.err.Error() }
func (e *claudeStartCleanupError) Unwrap() error { return e.err }

func claudeCodeSessionID(id string) string {
	if len(id) == 36 && id[8] == '-' && id[13] == '-' && id[18] == '-' && id[23] == '-' {
		return id
	}
	sum := sha1.Sum(append([]byte("mecha-claude-session:"), []byte(id)...))
	sum[6] = (sum[6] & 15) | 80
	sum[8] = (sum[8] & 63) | 128
	h := hex.EncodeToString(sum[:16])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
func claudeCodeNewInputID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// acquire is lane-serialized. Each foreground execution owns a fresh process.
// A pool lock protects lifetime only; no persistence callback runs under it.
func (pool *claudeCodeProcessPool) acquire(ctx context.Context, b store.EmbeddedBinding, cfg ClaudeCodeProcessConfig, newGrant func() (claudeCodeProcessGrant, error)) (*ClaudeCodeProcess, error) {
	pool.mu.Lock()
	entry := pool.entries[b.AgentID]
	pool.mu.Unlock()
	if err := pool.stopEntry(b.AgentID, entry); err != nil {
		return nil, err
	}
	pool.mu.Lock()
	if err := pool.workerErrorLocked(b.AgentID); err != nil {
		pool.mu.Unlock()
		return nil, errors.Join(errClaudeDefiniteStart, err)
	}
	if pool.entries == nil {
		pool.entries = make(map[string]*claudeCodeProcessEntry)
	}
	pool.starts.Add(1)
	pool.mu.Unlock()
	defer pool.starts.Done()
	grant := claudeCodeProcessGrant{}
	var err error
	if newGrant != nil {
		grant, err = newGrant()
		if err != nil {
			if ctx.Err() == nil {
				return nil, errors.Join(errClaudeDefiniteStart, err)
			}
			return nil, err
		}
		cfg.MCPConfigFile = grant.MCPConfigFile
		cfg.SystemPromptFile = grant.SystemPromptFile
	}
	cfg.releaseGate = pool.workerGate(b.AgentID)
	previousFence := cfg.OnUnconfirmedStop
	cfg.OnUnconfirmedStop = func(cause error) {
		pool.fenceWorker(b.AgentID, cause)
		if previousFence != nil {
			previousFence(cause)
		}
	}
	proc, err := StartClaudeCodeProcess(ctx, cfg)
	if err != nil {
		if grant.Revoke != nil {
			grant.Revoke()
		}
		if ctx.Err() == nil {
			return nil, errors.Join(errClaudeDefiniteStart, err)
		}
		return nil, err
	}
	if grant.Revoke != nil {
		revoke := grant.Revoke
		var once sync.Once
		grant.Revoke = func() { once.Do(revoke) }
	}
	entry = &claudeCodeProcessEntry{binding: b, process: proc, revoke: grant.Revoke, grantBinding: grant.Binding}
	if pool.afterStart != nil {
		pool.afterStart(proc)
	}
	pool.mu.Lock()
	if fenceErr := pool.workerErrorLocked(b.AgentID); fenceErr != nil {
		pool.mu.Unlock()
		if entry.revoke != nil {
			entry.revoke()
		}
		closeErr := proc.Close()
		if closeErr != nil {
			pool.mu.Lock()
			pool.fenceWorkerLocked(b.AgentID, closeErr)
			pool.mu.Unlock()
		}
		if closeErr != nil {
			// Keep the cleanup marker and cause chain, but never format raw paths.
			return nil, errors.Join(fenceErr, &claudeStartCleanupError{err: &ClaudeAdmissionFencedError{Cause: closeErr}})
		}
		return nil, errors.Join(errClaudeDefiniteStart, fenceErr)
	}
	pool.entries[b.AgentID] = entry
	pool.mu.Unlock()
	go func() {
		<-proc.Done()
		if entry.revoke != nil {
			entry.revoke()
		}
	}()
	return proc, nil
}
func (pool *claudeCodeProcessPool) stop(agentID string) error {
	pool.mu.Lock()
	entry := pool.entries[agentID]
	pool.mu.Unlock()
	return pool.stopEntry(agentID, entry)
}

// Mark stopping while holding the pool lock; every caller joins the same close.
func (pool *claudeCodeProcessPool) stopEntry(agentID string, entry *claudeCodeProcessEntry) error {
	pool.mu.Lock()
	if entry == nil || pool.entries[agentID] != entry {
		pool.mu.Unlock()
		return nil
	}
	if entry.stopping != nil {
		done := entry.stopping
		pool.mu.Unlock()
		<-done
		return entry.stopErr
	}
	entry.stopping = make(chan struct{})
	pool.mu.Unlock()
	return pool.finishStop(agentID, entry)
}
func (pool *claudeCodeProcessPool) finishStop(agentID string, entry *claudeCodeProcessEntry) error {
	if entry.revoke != nil {
		entry.revoke()
	}
	err := entry.process.Close()
	pool.mu.Lock()
	entry.stopErr = err
	if err != nil {
		pool.fenceWorkerLocked(agentID, err)
	}
	if pool.entries[agentID] == entry && err == nil {
		delete(pool.entries, agentID)
	}
	close(entry.stopping)
	pool.mu.Unlock()
	return err
}
func (pool *claudeCodeProcessPool) stopAll() error {
	pool.mu.Lock()
	pool.closed = true
	ids := make([]string, 0, len(pool.entries))
	for id := range pool.entries {
		ids = append(ids, id)
	}
	pool.mu.Unlock()
	var err error
	for _, id := range ids {
		err = errors.Join(err, pool.stop(id))
	}
	pool.starts.Wait()
	pool.mu.Lock()
	err = errors.Join(err, pool.startErr)
	for _, workerErr := range pool.fencedWorkers {
		err = errors.Join(err, workerErr)
	}
	pool.mu.Unlock()
	return err
}

type claudeCodeRuntimeOptions struct {
	Pool         *claudeCodeProcessPool
	Config       ClaudeCodeProcessConfig
	Persistence  *embeddedPersistence
	BeforeSubmit func(context.Context, int) error
	Submit       func(context.Context, int, func() error) error
	AfterEcho    func(context.Context) error
	NewGrant     func() (claudeCodeProcessGrant, error)
	// ToolProducer is called only after the actual producer was checked. HTTP
	// authority may wait for this witness; never invent it from an HTTP request.
	ToolProducer func(context.Context, ai.ToolCall) error
	Bridge       *claudeCodeTurnBridge
}
type claudeCodeRuntime struct {
	pool             *claudeCodeProcessPool
	config           ClaudeCodeProcessConfig
	p                *embeddedPersistence
	beforeSubmit     func(context.Context, int) error
	submit           func(context.Context, int, func() error) error
	afterEcho        func(context.Context) error
	newGrant         func() (claudeCodeProcessGrant, error)
	toolProducer     func(context.Context, ai.ToolCall) error
	bridge           *claudeCodeTurnBridge
	grantBinding     *claudeCodeGrantBinding
	mu               sync.Mutex
	runMu            sync.Mutex
	process          *ClaudeCodeProcess
	probeCleanupErr  error
	active           bool
	pendingInterrupt bool
	preparing        bool
	hasRun           bool
	interrupted      bool
	discardPartial   bool
	runDone          chan struct{}
	interruptNotify  chan struct{}
	interruptDone    chan struct{}
	inputCancels     map[string]context.CancelFunc
	snapshot         embeddedSnapshot
	lastUser         *ai.UserMessage
	seedHistory      []agent.AgentMessage
	instruction      string
	notes            string
	admissionBytes   int
	tools            map[string]string
	rawTools         map[string]string
	steers           map[string]*claudeSteer
	steerOpen        bool
}

var _ embeddedRuntime = (*claudeCodeRuntime)(nil)

func newClaudeCodeRuntime(ctx context.Context, opts claudeCodeRuntimeOptions) (*claudeCodeRuntime, error) {
	if opts.Pool == nil || opts.Persistence == nil || opts.BeforeSubmit == nil || opts.AfterEcho == nil {
		return nil, errors.New("Claude runtime requires pool, checked persistence, and submission/echo admission")
	}
	r := &claudeCodeRuntime{pool: opts.Pool, config: opts.Config, p: opts.Persistence, beforeSubmit: opts.BeforeSubmit, submit: opts.Submit, afterEcho: opts.AfterEcho, newGrant: opts.NewGrant, toolProducer: opts.ToolProducer, bridge: opts.Bridge, tools: make(map[string]string), rawTools: make(map[string]string), snapshot: embeddedSnapshot{ContextWindow: 1000000}}
	snap, err := r.p.db.ReopenEmbedded(ctx, r.p.binding)
	if err != nil {
		return nil, err
	}
	var history []agent.AgentMessage
	for _, entry := range snap.Entries {
		message, err := ai.UnmarshalMessage(entry.Body)
		if err != nil {
			return nil, err
		}
		r.p.seq, r.p.digest = entry.Sequence, entry.Digest
		if user, ok := message.(ai.UserMessage); ok {
			copy := user
			r.lastUser = &copy
			r.snapshot.HasUser = true
			r.p.turnSeq, r.p.turnDigest = entry.Sequence, entry.Digest
		}
		if assistant, ok := message.(ai.AssistantMessage); ok {
			if tokens := assistant.Usage.Input + assistant.Usage.CacheRead + assistant.Usage.CacheWrite; tokens > 0 {
				r.snapshot.ContextTokens = tokens
			}
			r.snapshot.LastAssistantText = claudeCodeMessageText(assistant)
		}
		history = append(history, message)
	}
	r.seedHistory = history
	saved, err := r.p.db.ManagedAgent(ctx, r.p.binding.AgentID)
	if err != nil {
		return nil, err
	}
	r.instruction = saved.PendingInitialInstruction
	notes, _, err := r.p.db.FetchScratchpadRecent(ctx, r.p.binding.AgentID, 20)
	if err != nil {
		return nil, err
	}
	for _, note := range notes {
		status := "Recent"
		if note.Pinned {
			status = "Pinned"
		}
		r.notes += fmt.Sprintf("- [%s] %s\n", status, note.Content)
	}
	return r, nil
}
func claudeCodeMessageText(message agent.AgentMessage) string {
	var content ai.ContentList
	switch m := message.(type) {
	case ai.UserMessage:
		content = m.Content
	case ai.AssistantMessage:
		content = m.Content
	case ai.ToolResultMessage:
		content = m.Content
	}
	var s strings.Builder
	for _, block := range content {
		switch b := block.(type) {
		case ai.TextContent:
			s.WriteString(b.Text)
		case ai.ToolCall:
			s.WriteString("[tool " + b.Name + "]")
		}
	}
	return s.String()
}
func (r *claudeCodeRuntime) Snapshot() embeddedSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshot
}
func (r *claudeCodeRuntime) Run(ctx context.Context, text string, images ...ai.ImageContent) (*embeddedOutcome, error) {
	content := ai.ContentList{ai.TextContent{Text: text}}
	for _, image := range images {
		content = append(content, image)
	}
	return r.RunMessages(ctx, []agent.AgentMessage{ai.UserMessage{Content: content, Timestamp: time.Now().UnixMilli()}})
}
func (r *claudeCodeRuntime) RunMessages(ctx context.Context, messages []agent.AgentMessage) (*embeddedOutcome, error) {
	return r.run(ctx, messages, false)
}
func (r *claudeCodeRuntime) Continue(ctx context.Context) error {
	r.mu.Lock()
	user := r.lastUser
	r.mu.Unlock()
	if user == nil {
		return errors.New("Claude continue has no durable user")
	}
	_, err := r.run(ctx, []agent.AgentMessage{*user}, true)
	return err
}

// The generic Steer method cannot express the accepted router attempt identity.
// Managed Claude callers must use TrySteerAttempt; never use Pi's head fallback.
func (r *claudeCodeRuntime) Steer(message agent.AgentMessage) {
	if !r.TrySteer(message, nil) {
		r.mu.Lock()
		proc := r.process
		r.mu.Unlock()
		if proc != nil {
			proc.fail(errors.New("Claude steering rejected; input not consumed"))
		}
	}
}

// Generic steering has no reservation, so it is refused. Managed Claude input
// is steered through steerInput with its mail attempt or prompt request.
func (r *claudeCodeRuntime) TrySteer(message agent.AgentMessage, attempt *store.EmbeddedMailAttempt) bool {
	return false
}

func (r *claudeCodeRuntime) Interrupt() {
	r.mu.Lock()
	if !r.active {
		if !r.hasRun || r.preparing {
			r.pendingInterrupt = true
		}
		r.mu.Unlock()
		return
	}
	if r.interrupted {
		r.mu.Unlock()
		return
	}
	r.interrupted = true
	for _, cancel := range r.inputCancels {
		cancel()
	}
	close(r.interruptNotify)
	done := make(chan struct{})
	r.interruptDone = done
	proc := r.process
	r.mu.Unlock()
	defer close(done)
	if proc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := proc.Interrupt(ctx); err != nil {
			r.pool.stop(r.p.binding.AgentID)
		}
	}
}
func (r *claudeCodeRuntime) Close() { _ = r.closeScoped() }

func (r *claudeCodeRuntime) closeScoped() error {
	r.mu.Lock()
	done := r.runDone
	active := r.active
	proc := r.process
	r.mu.Unlock()
	if active {
		r.Interrupt()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			r.pool.stop(r.p.binding.AgentID)
			<-done
		}
	}
	if r.grantBinding != nil && r.bridge != nil {
		r.grantBinding.detach(r.bridge)
		r.bridge.serial.Lock()
		r.bridge.serial.Unlock()
	}
	r.pool.mu.Lock()
	entry := r.pool.entries[r.p.binding.AgentID]
	// Before assignment, only join a stop already initiated by this lane. Do
	// not stop a later incarnation that this runtime never owned.
	if entry != nil && ((proc != nil && entry.process != proc) || (proc == nil && entry.stopping == nil)) {
		entry = nil
	}
	r.pool.mu.Unlock()
	r.mu.Lock()
	probeCleanupErr := r.probeCleanupErr
	r.mu.Unlock()
	return errors.Join(probeCleanupErr, r.pool.stopEntry(r.p.binding.AgentID, entry))
}
func (r *claudeCodeRuntime) stop(err error) (*embeddedOutcome, error) {
	if r.bridge != nil {
		r.bridge.close()
	}
	return nil, errors.Join(err, r.pool.stop(r.p.binding.AgentID))
}

func (r *claudeCodeRuntime) acquire(ctx context.Context) (*ClaudeCodeProcess, error) {
	// Only this runtime's process can be reused: a pool entry from another
	// foreground scope must still be stopped before a new process is admitted.
	r.mu.Lock()
	owned := r.process
	r.mu.Unlock()
	r.pool.mu.Lock()
	entry := r.pool.entries[r.p.binding.AgentID]
	if owned != nil && !r.pool.closed && entry != nil && entry.process == owned && entry.binding == r.p.binding && entry.stopping == nil && owned.Err() == nil {
		r.pool.mu.Unlock()
		return owned, nil
	}
	r.pool.mu.Unlock()
	proc, err := r.pool.acquire(ctx, r.p.binding, r.config, r.newGrant)
	var cleanup *claudeStartCleanupError
	if errors.As(err, &cleanup) {
		r.mu.Lock()
		r.probeCleanupErr = errors.Join(r.probeCleanupErr, cleanup.err)
		r.mu.Unlock()
	}
	return proc, err
}

var errClaudeDefiniteStart = errors.New("definite Claude acquisition or start failure")

func (r *claudeCodeRuntime) failedInitialProbe(proc *ClaudeCodeProcess, started bool, probeErr error) (bool, error) {
	cleanupErr := r.pool.stop(r.p.binding.AgentID)
	if cleanupErr != nil {
		r.mu.Lock()
		r.probeCleanupErr = cleanupErr
		r.mu.Unlock()
		return false, errors.Join(probeErr, cleanupErr)
	}
	missing := started && strings.Contains(proc.Stderr(), "No conversation found with session ID:")
	return missing, probeErr
}

func claudeCodeRender(content ai.ContentList) (json.RawMessage, error) {
	blocks := make([]interface{}, 0, len(content))
	for _, block := range content {
		switch b := block.(type) {
		case ai.TextContent:
			blocks = append(blocks, map[string]interface{}{"type": "text", "text": unwrapEmbeddedMailText(b.Text)})
		case ai.ImageContent:
			blocks = append(blocks, map[string]interface{}{"type": "image", "source": map[string]interface{}{"type": "base64", "data": b.Data, "media_type": b.MimeType}})
		default:
			return nil, fmt.Errorf("unsupported Claude user content %T", block)
		}
	}
	return json.Marshal(blocks)
}

// Idle mail's checked user message is the canonical attribution envelope, not
// the provider payload. Add reserved attachment bytes only to the latter;
// changing the checked message would invalidate its exact consumption witness.
func claudeCodeMailContent(content ai.ContentList, attempt *store.EmbeddedMailAttempt) ai.ContentList {
	if attempt == nil {
		return content
	}
	for _, part := range content {
		if _, ok := part.(ai.ImageContent); ok {
			return content
		}
	}
	result := append(ai.ContentList(nil), content...)
	for _, part := range buildSteerUserMessage(attempt, 0).Content {
		if image, ok := part.(ai.ImageContent); ok {
			result = append(result, image)
		}
	}
	return result
}

func (r *claudeCodeRuntime) run(ctx context.Context, messages []agent.AgentMessage, continuing bool) (outcome *embeddedOutcome, runErr error) {
	r.runMu.Lock()
	defer r.runMu.Unlock()
	r.mu.Lock()
	r.preparing = true
	r.hasRun = true
	defer func() {
		r.mu.Lock()
		if r.preparing {
			r.pendingInterrupt = false
		}
		r.preparing = false
		r.mu.Unlock()
	}()
	if r.pendingInterrupt {
		r.pendingInterrupt = false
		r.mu.Unlock()
		return &embeddedOutcome{StopReason: ai.StopAborted}, context.Canceled
	}
	r.admissionBytes = 0
	r.mu.Unlock()
	if len(messages) != 1 {
		return nil, errors.New("Claude run requires exactly one input per execution")
	}
	// Validate all input bounds before acquisition or stdin submission.
	users := make([]ai.UserMessage, len(messages))
	rendered := make([]json.RawMessage, len(messages))
	total := 0
	for i, message := range messages {
		user, ok := message.(ai.UserMessage)
		if !ok {
			return nil, errors.New("Claude run accepts user messages only")
		}
		users[i] = user
		content := user.Content
		if i == 0 {
			content = claudeCodeMailContent(content, r.p.mail)
		}
		raw, err := claudeCodeRender(content)
		if err != nil {
			return nil, err
		}
		rendered[i] = raw
		total += len(raw)
	}
	if total > 6<<20 {
		return nil, fmt.Errorf("%w: Claude rendered inputs exceed 6 MiB", errClaudeInputBounds)
	}
	if err := r.beforeSubmit(ctx, total); err != nil {
		return r.stop(err)
	}
	started, err := r.p.db.ClaudeBindingStarted(ctx, r.p.binding)
	if err != nil {
		return nil, err
	}
	r.config.Resume = started
	proc, err := r.acquire(ctx)
	if err != nil {
		return nil, err
	}
	// Probe the control channel before any user submission. Resume loss is
	// recoverable only here: no echoed input or effect may have been consumed.
	fresh := !started
	{
		if _, probeErr := proc.Control(ctx, map[string]interface{}{"subtype": "interrupt"}); probeErr != nil {
			fallback, cleanupErr := r.failedInitialProbe(proc, started, probeErr)
			if !fallback {
				if proc.Err() != nil && ctx.Err() == nil {
					return nil, errors.Join(errClaudeDefiniteStart, cleanupErr)
				}
				return nil, cleanupErr
			}
			r.config.Resume = false
			proc, err = r.acquire(ctx)
			if err != nil {
				return nil, err
			}
			if _, err = proc.Control(ctx, map[string]interface{}{"subtype": "interrupt"}); err != nil {
				_, cleanupErr := r.failedInitialProbe(proc, false, err)
				if proc.Err() != nil && ctx.Err() == nil {
					return nil, errors.Join(errClaudeDefiniteStart, cleanupErr)
				}
				return nil, cleanupErr
			}
			fresh = true
		}
	}
	if fresh {
		// Checked history describes the old provider session, not this fresh one.
		r.mu.Lock()
		r.snapshot.ContextTokens = 0
		r.mu.Unlock()
		wakeContent := claudeCodeMailContent(users[0].Content, r.p.mail)
		var wakeParts strings.Builder
		for _, part := range wakeContent {
			if text, ok := part.(ai.TextContent); ok {
				wakeParts.WriteString(unwrapEmbeddedMailText(text.Text))
			}
		}
		seeded, err := claudeCodeSeed(r.instruction, r.notes, r.seedHistory, wakeParts.String(), 200000)
		if err != nil {
			return r.stop(err)
		}
		content := append(ai.ContentList{ai.TextContent{Text: seeded}}, wakeContent...)
		rendered[0], err = claudeCodeRender(content)
		if err != nil {
			return r.stop(err)
		}
		total = len(rendered[0])
		if total > 6<<20 {
			return r.stop(fmt.Errorf("%w: Claude rendered inputs exceed 6 MiB", errClaudeInputBounds))
		}
	}
	if err := r.checkInputHeadroom(total); err != nil {
		return r.stop(err)
	}
	r.pool.mu.Lock()
	entry := r.pool.entries[r.p.binding.AgentID]
	if entry != nil {
		r.grantBinding = entry.grantBinding
	}
	r.pool.mu.Unlock()
	if r.grantBinding != nil && r.bridge != nil {
		r.grantBinding.attach(r.bridge)
	}
	r.mu.Lock()
	r.process = proc
	if r.pendingInterrupt {
		r.pendingInterrupt = false
		r.mu.Unlock()
		return &embeddedOutcome{StopReason: ai.StopAborted}, context.Canceled
	}
	r.preparing = false
	r.active = true
	r.interrupted = false
	r.discardPartial = false
	r.interruptNotify = make(chan struct{})
	r.interruptDone = nil
	r.runDone = make(chan struct{})
	done := r.runDone
	r.mu.Unlock()
	defer func() {
		if r.bridge != nil {
			r.bridge.serial.Lock()
			defer r.bridge.serial.Unlock()
		}
		r.mu.Lock()
		r.active = false
		r.steerOpen = false
		r.steers = nil
		interruptDone := r.interruptDone
		r.mu.Unlock()
		if interruptDone != nil {
			<-interruptDone
		}
		close(done)
	}()
	pending := make(map[string]ai.UserMessage, len(users))
	pendingRaw := make(map[string]json.RawMessage, len(users))
	for i, user := range users[:1] {
		if err := r.checkInputHeadroom(len(rendered[i])); err != nil {
			return r.stop(err)
		}
		if err := r.beforeSubmit(ctx, len(rendered[i])); err != nil {
			return r.stop(err)
		}
		id := claudeCodeNewInputID()
		pending[id] = user
		pendingRaw[id] = rendered[i]
		if err := r.submitInput(ctx, id, rendered[i], r.p.mail); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil, err
			}
			return r.stop(err)
		}
		r.mu.Lock()
		r.admissionBytes += len(rendered[i])
		r.mu.Unlock()
	}
	return r.readTurn(ctx, pending, pendingRaw, continuing)
}

// Usage-limit errors are not ordinary StopError answers: they must not trigger
// synthesis nudges, goal evaluation, or input replay. Queue hold policy owns retry.
type claudeCodeUsageLimitError struct{ ResetsAt time.Time }

func (e *claudeCodeUsageLimitError) Error() string {
	return "Claude usage limit reached; retry after " + e.ResetsAt.UTC().Format(time.RFC3339)
}

func claudeUsageLimitDelay(now, reset time.Time) time.Duration {
	delay := reset.Sub(now)
	if delay < 0 {
		return 0
	}
	return delay
}

func (r *claudeCodeRuntime) readTurn(ctx context.Context, pending map[string]ai.UserMessage, pendingRaw map[string]json.RawMessage, continuing bool) (*embeddedOutcome, error) {
	retryInputs := make(map[string]bool)
	var lastRaw json.RawMessage
	if continuing {
		for id := range pending {
			retryInputs[id] = true
		}
	}
	var held *ai.AssistantMessage
	heldID := ""
	resolvedModel := ""
	ambiguousModel := false
	admitted := false
	var limit *claudeCodeUsageLimitError
	var cancelDrain context.CancelFunc
	var lateDeadline time.Time
	defer func() {
		if cancelDrain != nil {
			cancelDrain()
		}
	}()
	flush := func(stop ai.StopReason, errorText string) error {
		if held == nil {
			return nil
		}
		r.mu.Lock()
		discard := r.discardPartial
		r.mu.Unlock()
		if discard {
			held = nil
			heldID = ""
			return nil
		}
		held.StopReason = stop
		held.ErrorMessage = errorText
		if err := r.appendChecked(ctx, *held); err != nil {
			if errors.Is(err, errClaudeTerminalOutput) {
				held = nil
				heldID = ""
				return nil
			}
			return err
		}
		for _, block := range held.Content {
			if call, ok := block.(ai.ToolCall); ok {
				if r.toolProducer != nil && strings.HasPrefix(r.rawTools[call.ID], "mcp__mecha__") {
					if err := r.toolProducer(ctx, call); err != nil {
						return err
					}
				}
			}
		}
		r.mu.Lock()
		if tokens := held.Usage.Input + held.Usage.CacheRead + held.Usage.CacheWrite; tokens > 0 {
			r.snapshot.ContextTokens = tokens
		}
		if stop != ai.StopToolUse {
			r.snapshot.LastAssistantText = claudeCodeMessageText(*held)
		}
		r.mu.Unlock()
		held = nil
		heldID = ""
		return nil
	}
	for {
		if r.bridge != nil {
			if err := r.bridge.err(); err != nil {
				return r.stop(err)
			}
		}
		if ctx.Err() != nil && cancelDrain == nil {
			r.Interrupt()
			ctx, cancelDrain = context.WithTimeout(context.Background(), 5*time.Second)
		}
		nextCtx := ctx
		var cancelNext context.CancelFunc
		if !lateDeadline.IsZero() {
			nextCtx, cancelNext = context.WithDeadline(ctx, lateDeadline)
		}
		event, err := claudeNextBeforeDeadline(r.process, nextCtx, lateDeadline)
		if cancelNext != nil {
			cancelNext()
		}
		if err != nil {
			if ctx.Err() != nil && cancelDrain == nil {
				r.Interrupt()
				ctx, cancelDrain = context.WithTimeout(context.Background(), 5*time.Second)
				continue
			}
			if errors.Is(err, io.EOF) {
				err = errors.New("Claude process exited before result")
			}
			return r.stop(err)
		}
		if event.ParentToolUseID != nil {
			continue
		}
		switch event.Type {
		case "system":
			if event.Subtype == "init" {
				var init struct {
					Model string `json:"model"`
				}
				_ = json.Unmarshal(event.Raw, &init)
				if init.Model != "" {
					r.config.Model = init.Model
				}
			}
		case "user":
			var record struct {
				Origin *struct {
					Kind  string `json:"kind"`
					RunID string `json:"runId"`
				} `json:"origin"`
			}
			if err := json.Unmarshal(event.Raw, &record); err != nil {
				return r.stop(err)
			}
			if event.IsReplay && record.Origin != nil && record.Origin.Kind == "task-notification" {
				r.process.mu.Lock()
				owned := r.process.ownedTasks[record.Origin.RunID]
				r.process.mu.Unlock()
				if owned {
					continue
				}
				if !admitted {
					return r.stop(errors.New("Claude notification without admitted execution"))
				}
				var msg struct {
					Content json.RawMessage `json:"content"`
				}
				if err := json.Unmarshal(event.Message, &msg); err != nil {
					return r.stop(err)
				}
				var text string
				if json.Unmarshal(msg.Content, &text) != nil {
					var blocks []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					}
					if json.Unmarshal(msg.Content, &blocks) != nil {
						return r.stop(errors.New("malformed Claude notification content"))
					}
					for _, block := range blocks {
						if block.Type != "text" {
							return r.stop(errors.New("unsupported Claude notification block"))
						}
						text += block.Text
					}
				}
				if err := r.p.appendInExecutionNotice(ctx, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: text}}, Timestamp: time.Now().UnixMilli()}); err != nil {
					return r.stop(err)
				}
				continue
			}
			if event.IsReplay && record.Origin == nil {
				if steer := r.takeEchoedSteer(event.UUID); steer != nil {
					if err := r.commitSteerEcho(ctx, steer); err != nil {
						return r.stop(err)
					}
					// The echoed steer now has an ordinary, unbounded response.
					lateDeadline = time.Time{}
					continue
				}
				original, ok := pending[event.UUID]
				if !ok {
					return r.stop(errors.New("Claude echo has no exact accepted input"))
				}
				delete(pending, event.UUID)
				lastRaw = pendingRaw[event.UUID]
				delete(pendingRaw, event.UUID)
				if !retryInputs[event.UUID] {
					if err := r.appendChecked(ctx, original); err != nil {
						return r.stop(err)
					}
				} else if err := r.p.bindExecutionUser(ctx); err != nil {
					return r.stop(err)
				}
				if err := r.checkedEcho(ctx); err != nil {
					return r.stop(err)
				}
				admitted = true
				if r.bridge != nil {
					r.bridge.mu.Lock()
					r.bridge.admitted = true
					r.bridge.mu.Unlock()
				}
				r.mu.Lock()
				r.snapshot.HasUser = true
				r.steerOpen = true
				copy := original
				r.lastUser = &copy
				r.mu.Unlock()
				continue
			}
			if !admitted && !event.IsReplay {
				continue
			}
			if !admitted {
				return r.stop(errors.New("Claude user output before checked consumption"))
			}
			results, err := r.toolResults(event)
			if err != nil {
				return r.stop(err)
			}
			if len(results) > 0 {
				if err := flush(ai.StopToolUse, ""); err != nil {
					return r.stop(err)
				}
				for _, result := range results {
					if err := r.appendChecked(ctx, result); err != nil {
						return r.stop(err)
					}
					delete(r.tools, result.ToolCallID)
					delete(r.rawTools, result.ToolCallID)
				}
			}
		case "assistant":
			if !admitted {
				return r.stop(errors.New("Claude assistant before checked consumption"))
			}
			var api struct {
				IsAPIError bool   `json:"is_api_error_message"`
				APIError   string `json:"api_error"`
				Params     struct {
					Info json.RawMessage `json:"rate_limit_info"`
				} `json:"api_error_params"`
			}
			_ = json.Unmarshal(event.Raw, &api)
			if api.IsAPIError && api.APIError == "usage_limit_reached" {
				limit = claudeCodeLimit(api.Params.Info)
				continue
			}
			assistant, id, err := r.assistant(event)
			if err != nil {
				return r.stop(err)
			}
			if resolvedModel == "" {
				resolvedModel = assistant.Model
			} else if resolvedModel != assistant.Model {
				ambiguousModel = true
			}
			if held != nil && heldID != id {
				if err := flush(ai.StopToolUse, ""); err != nil {
					return r.stop(err)
				}
			}
			if held == nil {
				held = assistant
				heldID = id
			} else {
				held.Content = append(held.Content, assistant.Content...)
				held.Usage = assistant.Usage
			}
			hasTool := false
			for _, block := range assistant.Content {
				if _, ok := block.(ai.ToolCall); ok {
					hasTool = true
				}
			}
			if hasTool {
				if err := flush(ai.StopToolUse, ""); err != nil {
					return r.stop(err)
				}
			}
		case "rate_limit_event":
			var e struct {
				Info json.RawMessage `json:"rate_limit_info"`
			}
			_ = json.Unmarshal(event.Raw, &e)
			var info struct {
				Status string `json:"status"`
			}
			if json.Unmarshal(e.Info, &info) != nil {
				return r.stop(errors.New("malformed Claude rate limit"))
			}
			if info.Status == "rejected" {
				limit = claudeCodeLimit(e.Info)
			}
		case "result":
			if !admitted {
				return r.stop(errors.New("Claude result before checked consumption"))
			}
			// Unacknowledged steers stay reserved: a result without their echo is not
			// an error here, so the held answer and original turn still settle.
			answered, uncertain := r.settleSteersAtResult()
			if limit != nil && (len(answered) > 0 || uncertain > 0) {
				return r.stop(errors.New("Claude usage limit with steered input in flight"))
			}
			if limit != nil {
				notice := r.emptyAssistant()
				notice.Content = ai.ContentList{ai.TextContent{Text: "Claude usage limit reached; resuming at " + limit.ResetsAt.UTC().Format(time.RFC3339)}}
				// A hold is not a terminal error: the active mail must remain open
				// until the retried input produces its actual final result.
				notice.StopReason = ai.StopToolUse
				if err := r.appendChecked(ctx, *notice); err != nil {
					return r.stop(err)
				}
				delay := claudeUsageLimitDelay(time.Now(), limit.ResetsAt)
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return r.stop(ctx.Err())
				case <-r.interruptNotify:
					timer.Stop()
					return &embeddedOutcome{StopReason: ai.StopAborted}, context.Canceled
				case <-timer.C:
				}
				r.mu.Lock()
				interrupted := r.interrupted
				r.mu.Unlock()
				if interrupted {
					return &embeddedOutcome{StopReason: ai.StopAborted}, context.Canceled
				}
				r.mu.Lock()
				last := r.lastUser
				r.mu.Unlock()
				if last == nil {
					return r.stop(limit)
				}
				raw := lastRaw
				if len(raw) == 0 {
					return r.stop(errors.New("Claude limit without echoed input payload"))
				}
				if err := r.checkInputHeadroom(0); err != nil {
					return r.stop(fmt.Errorf("Claude retry after consumption: %v", err))
				}
				if err := r.beforeSubmit(ctx, len(raw)); err != nil {
					return r.stop(err)
				}
				id := claudeCodeNewInputID()
				if err := r.submitRetryInput(ctx, id, raw, nil); err != nil {
					if errors.Is(err, context.Canceled) {
						return &embeddedOutcome{StopReason: ai.StopAborted}, err
					}
					return r.stop(err)
				}
				pending[id] = *last
				pendingRaw[id] = raw
				retryInputs[id] = true
				admitted = false
				limit = nil
				held = nil
				continue
			}
			r.mu.Lock()
			interrupted := r.interrupted
			discard := r.discardPartial
			r.mu.Unlock()
			stop := ai.StopStop
			errorText := ""
			var result struct {
				Result     string   `json:"result"`
				Errors     []string `json:"errors"`
				ModelUsage map[string]struct {
					ContextWindow json.RawMessage `json:"contextWindow"`
				} `json:"modelUsage"`
			}
			if err := json.Unmarshal(event.Raw, &result); err != nil {
				return r.stop(err)
			}
			windows := make(map[string]json.RawMessage, len(result.ModelUsage))
			for key, usage := range result.ModelUsage {
				windows[key] = usage.ContextWindow
			}
			resolved := resolvedModel
			if ambiguousModel {
				resolved = ""
			}
			if window := claudeReportedWindow(r.config.Model, resolved, windows); window > 0 {
				r.mu.Lock()
				r.snapshot.ContextWindow = window
				r.mu.Unlock()
			}
			resolvedModel, ambiguousModel = "", false
			if interrupted {
				stop = ai.StopAborted
			} else if event.Subtype != "success" {
				stop = ai.StopError
				errorText = result.Result
				if errorText == "" {
					errorText = strings.Join(result.Errors, "; ")
				}
				if errorText == "" {
					errorText = "Claude " + event.Subtype
				}
			}
			if discard {
				held = nil
			} else {
				if held == nil {
					held = r.emptyAssistant()
				}
				if err := flush(stop, errorText); err != nil {
					return r.stop(err)
				}
			}
			if discard || interrupted || stop == ai.StopError {
				var resultErr error
				if interrupted {
					resultErr = context.Canceled
				} else if stop == ai.StopError {
					resultErr = errors.New(errorText)
				}
				if uncertain > 0 {
					_, resultErr = r.stop(resultErr)
				}
				if discard {
					return &embeddedOutcome{StopReason: ai.StopStop}, resultErr
				}
				return &embeddedOutcome{StopReason: stop}, resultErr
			}
			if err := r.completeSteeredPrompts(ctx, answered); err != nil {
				return r.stop(err)
			}
			if len(pending) != 0 {
				return r.stop(errors.New("Claude result without all input echoes"))
			}
			if uncertain > 0 {
				if lateDeadline.IsZero() {
					lateDeadline = time.Now().Add(30 * time.Second)
				}
				continue
			}
			return &embeddedOutcome{StopReason: stop}, nil
		}
	}
	if len(pending) != 0 {
		return r.stop(errors.New("Claude result without all input echoes"))
	}
	return &embeddedOutcome{StopReason: ai.StopStop}, nil
}

// Buffered events must not extend the late-echo drain deadline.
func claudeNextBeforeDeadline(p *ClaudeCodeProcess, ctx context.Context, deadline time.Time) (ClaudeCodeEvent, error) {
	if !deadline.IsZero() && !time.Now().Before(deadline) {
		return ClaudeCodeEvent{}, context.DeadlineExceeded
	}
	event, err := p.Next(ctx)
	if !deadline.IsZero() && !time.Now().Before(deadline) {
		return ClaudeCodeEvent{}, context.DeadlineExceeded
	}
	return event, err
}

var errClaudeTerminalOutput = errors.New("assistant output after checked terminating result")

func (r *claudeCodeRuntime) appendChecked(ctx context.Context, message agent.AgentMessage) error {
	if message.MessageRole() != ai.RoleUser {
		// Output already received belongs to the admitted turn. A caller cancel
		// must not abort its checked append before readTurn can interrupt and
		// drain the partial answer. Keep owner values/fences and bound the write;
		// user consumption and tool execution retain their cancellation policy.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
	}
	if r.bridge != nil {
		r.bridge.serial.Lock()
		defer r.bridge.serial.Unlock()
		if _, ok := message.(ai.AssistantMessage); ok && r.bridge.terminal {
			return errClaudeTerminalOutput
		}
		if err := r.bridge.err(); err != nil {
			return err
		}
		if result, ok := message.(ai.ToolResultMessage); ok && strings.HasPrefix(r.rawToolName(result.ToolCallID), "mcp__mecha__") {
			return r.bridge.observeResult(ctx, result)
		}
	}
	return r.p.AppendCheckedMessage(ctx, message)
}
func (r *claudeCodeRuntime) rawToolName(id string) string { return r.rawTools[id] }
func (r *claudeCodeRuntime) checkedEcho(ctx context.Context) error {
	if r.bridge != nil {
		r.bridge.serial.Lock()
		defer r.bridge.serial.Unlock()
	}
	return r.afterEcho(ctx)
}

func claudeCodeLimit(raw json.RawMessage) *claudeCodeUsageLimitError {
	var info struct {
		ResetsAt float64 `json:"resetsAt"`
	}
	_ = json.Unmarshal(raw, &info)
	reset := time.Now().Add(15 * time.Minute)
	if info.ResetsAt > 0 {
		reset = time.Unix(int64(info.ResetsAt), 0)
	}
	return &claudeCodeUsageLimitError{ResetsAt: reset}
}
func (r *claudeCodeRuntime) emptyAssistant() *ai.AssistantMessage {
	return &ai.AssistantMessage{Content: ai.ContentList{}, Api: "claude-code", Provider: "claude-code", Model: r.config.Model, Timestamp: time.Now().UnixMilli()}
}
func (r *claudeCodeRuntime) assistant(event ClaudeCodeEvent) (*ai.AssistantMessage, string, error) {
	var message struct {
		ID      string            `json:"id"`
		Model   string            `json:"model"`
		Content []json.RawMessage `json:"content"`
		Usage   struct {
			Input      int `json:"input_tokens"`
			Output     int `json:"output_tokens"`
			CacheRead  int `json:"cache_read_input_tokens"`
			CacheWrite int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(event.Message, &message) != nil || message.ID == "" || message.Model == "" {
		return nil, "", errors.New("malformed required Claude assistant")
	}
	result := r.emptyAssistant()
	result.Model = message.Model
	result.ResponseID = message.ID
	result.Usage = ai.Usage{Input: message.Usage.Input, Output: message.Usage.Output, CacheRead: message.Usage.CacheRead, CacheWrite: message.Usage.CacheWrite}
	result.Usage.TotalTokens = result.Usage.Input + result.Usage.Output + result.Usage.CacheRead + result.Usage.CacheWrite
	for _, raw := range message.Content {
		var b struct {
			Type     string                 `json:"type"`
			Text     string                 `json:"text"`
			Thinking string                 `json:"thinking"`
			ID       string                 `json:"id"`
			Name     string                 `json:"name"`
			Input    map[string]interface{} `json:"input"`
		}
		if json.Unmarshal(raw, &b) != nil {
			return nil, "", errors.New("malformed Claude assistant block")
		}
		switch b.Type {
		case "text":
			result.Content = append(result.Content, ai.TextContent{Text: b.Text})
		case "thinking":
			result.Content = append(result.Content, ai.ThinkingContent{Thinking: b.Thinking})
		case "tool_use":
			if b.ID == "" || b.Name == "" || b.Input == nil {
				return nil, "", errors.New("malformed Claude tool use")
			}
			if strings.HasPrefix(b.Name, "mcp__mecha__") && r.grantBinding != nil {
				b.ID = r.grantBinding.canonicalID(b.ID)
			}
			if len(r.tools) >= 128 {
				return nil, "", errors.New("Claude active tool buffer overflow")
			}
			name := strings.TrimPrefix(b.Name, "mcp__mecha__")
			r.tools[b.ID] = name
			r.rawTools[b.ID] = b.Name
			result.Content = append(result.Content, ai.ToolCall{ID: b.ID, Name: name, Arguments: b.Input})
		default:
			return nil, "", fmt.Errorf("unsupported Claude assistant block %q", b.Type)
		}
	}
	return result, message.ID, nil
}
func (r *claudeCodeRuntime) toolResults(event ClaudeCodeEvent) ([]ai.ToolResultMessage, error) {
	var msg struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(event.Message, &msg) != nil {
		return nil, errors.New("malformed Claude tool result message")
	}
	var blocks []json.RawMessage
	if json.Unmarshal(msg.Content, &blocks) != nil {
		return nil, nil
	}
	var results []ai.ToolResultMessage
	for _, raw := range blocks {
		var block struct {
			Type    string          `json:"type"`
			ID      string          `json:"tool_use_id"`
			Content json.RawMessage `json:"content"`
			IsError bool            `json:"is_error"`
		}
		if json.Unmarshal(raw, &block) != nil {
			return nil, errors.New("malformed Claude result block")
		}
		if block.Type != "tool_result" {
			continue
		}
		if r.grantBinding != nil {
			canonical := r.grantBinding.canonicalID(block.ID)
			if _, ok := r.tools[canonical]; ok {
				block.ID = canonical
			}
		}
		name, ok := r.tools[block.ID]
		if !ok {
			return nil, errors.New("Claude tool result has no checked producer")
		}
		var text string
		if json.Unmarshal(block.Content, &text) != nil {
			var content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(block.Content, &content) != nil {
				return nil, errors.New("malformed Claude tool result content")
			}
			for _, b := range content {
				if b.Type == "image" {
					text += "[image]"
				} else {
					text += b.Text
				}
			}
		}
		results = append(results, ai.ToolResultMessage{ToolCallID: block.ID, ToolName: name, Content: ai.ContentList{ai.TextContent{Text: text}}, IsError: block.IsError, Timestamp: time.Now().UnixMilli()})
	}
	return results, nil
}

// writeClaudeCodePrompt creates the private leaf only; normal CLI project/user
// configuration remains enabled. Its caller owns deletion at process shutdown.
func writeClaudeCodePrompt(root, agentID, prompt string) (string, error) {
	dir := filepath.Join(root, "claude-prompts")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(dir, agentID+"-*.prompt")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if err = file.Chmod(0600); err == nil {
		_, err = file.WriteString(prompt)
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

func (pool *claudeCodeProcessPool) reusable(b store.EmbeddedBinding) bool {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	e := pool.entries[b.AgentID]
	return !pool.closed && e != nil && e.stopping == nil && e.binding == b && e.process.Err() == nil
}

func (r *claudeCodeRuntime) submitInput(ctx context.Context, id string, raw json.RawMessage, attempts ...*store.EmbeddedMailAttempt) error {
	var attempt *store.EmbeddedMailAttempt
	if len(attempts) != 0 {
		attempt = attempts[0]
	}
	return r.submitRetryInput(ctx, id, raw, attempt)
}

// Serialize retry submission with Interrupt at the stdin write boundary.
func (r *claudeCodeRuntime) submitRetryInput(ctx context.Context, id string, raw json.RawMessage, attempt *store.EmbeddedMailAttempt) error {
	write := func() error {
		r.mu.Lock()
		if r.interrupted {
			r.mu.Unlock()
			return context.Canceled
		}
		writeCtx, cancel := context.WithCancel(ctx)
		if r.inputCancels == nil {
			r.inputCancels = make(map[string]context.CancelFunc)
		}
		r.inputCancels[id] = cancel
		r.mu.Unlock()
		defer func() {
			r.mu.Lock()
			delete(r.inputCancels, id)
			r.mu.Unlock()
			cancel()
		}()
		return func() error {
			// Persist before the potentially uncertain write, but only after interrupt
			// and notification admission have allowed this submission.
			if err := r.p.db.MarkClaudeBindingStarted(writeCtx, r.p.binding); err != nil {
				return err
			}
			var mark func(context.Context) error
			if attempt != nil {
				mark = func(ctx context.Context) error {
					return r.p.db.MarkClaudeMailWriteStarted(ctx, r.p.binding, attempt.AttemptID)
				}
			}
			return r.process.sendUserWithStart(writeCtx, id, raw, mark)
		}()
	}
	if r.submit != nil {
		return r.submit(ctx, len(raw), write)
	}
	return write()
}

func (r *claudeCodeRuntime) checkInputHeadroom(size int) error {
	snapshot := r.Snapshot()
	if !claudeHeadroomFits(snapshot.ContextWindow, snapshot.ContextTokens, r.admissionBytes+size) {
		return fmt.Errorf("%w: Claude submission cannot fit fixed headroom", errClaudeInputBounds)
	}
	return nil
}
