package managed

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func (m *Manager) buildClaudeCodeRuntime(ctx context.Context, saved store.ManagedAgentRecord, owner embeddedOwner, p *embeddedPersistence, authority *embeddedTurnToolAuthority, prompt string, setReceipt func(*store.EmbeddedGoalAppliedReceipt)) (*claudeCodeRuntime, error) {
	bridge := newClaudeCodeTurnBridge(authority)
	bridge.owner = &owner
	before := func(ctx context.Context, size int) error {
		lane := m.laneFor(p.binding.AgentID)
		if lane.embeddedResetPending() {
			return errors.New("Claude submission denied: reset pending")
		}
		if size > 6<<20 {
			return errors.New("Claude input exceeds 6 MiB")
		}
		if err := m.store.RenewEmbeddedOwner(ctx, p.binding, owner.identity, embeddedOwnerTTL); err != nil {
			return err
		}
		current, err := m.store.ManagedAgent(ctx, p.binding.AgentID)
		if err != nil {
			return err
		}
		if current.PiSessionID != p.binding.SessionID || current.SessionEpoch != p.binding.Epoch || current.RuntimeGeneration != p.binding.Generation || current.DesiredState != "running" || (current.LifecycleState != "running" && current.LifecycleState != "starting") || (current.LifecycleState == "running" && !current.AcceptingPrompts) {
			return errors.New("Claude submission denied: stale lifecycle/binding")
		}
		if p.executionToken <= 0 {
			return errors.New("Claude submission denied: no admitted execution")
		}
		return nil
	}
	after := func(ctx context.Context) error {
		if err := m.store.RenewEmbeddedOwner(ctx, p.binding, owner.identity, embeddedOwnerTTL); err != nil {
			return err
		}
		if p.noticeStart {
			if err := m.store.StartEmbeddedInterruptedNotice(ctx, p.binding, *p.notice); err != nil {
				return err
			}
			p.noticeStart = false
		}
		if err := p.bindExecutionUser(ctx); err != nil {
			return err
		}
		if p.notice == nil && p.mailAttempt == "" && p.turnSeq != 0 {
			if err := m.store.EnterEmbeddedNonMailProviderTurn(ctx, p.binding, p.turnSeq, p.turnDigest); err != nil {
				return err
			}
		}
		setReceipt(nil)
		if p.wake != nil || p.notice != nil {
			return nil
		}
		policy, err := m.store.ManagedGoalPolicyFor(ctx, p.binding.AgentID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if policy.DesiredRevision <= 0 {
			return nil
		}
		key := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d:%s:%d:%d", p.binding.AgentID, p.binding.SessionID, p.binding.Epoch, p.binding.Generation, p.turnSeq, policy.DesiredRevision))))
		receipt, _, err := m.store.CommitEmbeddedGoalAppliedReceipt(ctx, p.binding, store.EmbeddedGoalAppliedReceiptRequest{TurnKey: key, TurnEntrySeq: p.turnSeq, TurnEntryDigest: p.turnDigest, GoalRevision: policy.DesiredRevision})
		if errors.Is(err, store.ErrEmbeddedGoalSDKResultUnchecked) {
			return nil
		}
		if err != nil {
			return err
		}
		setReceipt(&receipt)
		return nil
	}
	config := ClaudeCodeProcessConfig{OrphanRoot: m.config.SessionRoot, Cwd: saved.Cwd, SessionID: claudeCodeSessionID(p.binding.SessionID), Model: saved.SelectedModel.ID}
	config.OnUnconfirmedStop = func() {
		m.claudeCodeProcesses.mu.Lock()
		m.claudeCodeProcesses.closed = true
		m.claudeCodeProcesses.mu.Unlock()
	}
	grantFactory := func() (claudeCodeProcessGrant, error) {
		mcpURL, err := m.ClaudeCodeMCPURL()
		if err != nil {
			return claudeCodeProcessGrant{}, fmt.Errorf("start private Claude MCP listener: %w", err)
		}
		binding := &claudeCodeGrantBinding{incarnation: claudeCodeNewInputID()}

		tools := make([]ClaudeCodeMCPTool, 0)
		for _, tool := range authority.Tools() {
			if !claudeCodeV1Tool(tool.Name) {
				continue
			}
			schema, err := json.Marshal(tool.Parameters)
			if err != nil {
				return claudeCodeProcessGrant{}, err
			}
			tools = append(tools, ClaudeCodeMCPTool{Name: tool.Name, Description: tool.Description, InputSchema: schema})
		}
		life := m.runCtx
		if life == nil {
			life = context.Background()
		}
		token, revoke, err := m.ClaudeCodeMCP().Issue(life, ClaudeCodeMCPGrant{Binding: p.binding, Owner: owner.identity, Incarnation: binding.incarnation, Tools: tools, ExecuteChecked: binding.execute})
		if err != nil {
			return claudeCodeProcessGrant{}, err
		}
		dir := filepath.Join(m.config.SessionRoot, "claude-processes")
		if err := os.MkdirAll(dir, 0700); err != nil {
			revoke()
			return claudeCodeProcessGrant{}, err
		}
		file, err := os.CreateTemp(dir, "mcp-*.json")
		if err != nil {
			revoke()
			return claudeCodeProcessGrant{}, err
		}
		path := file.Name()
		err = file.Chmod(0600)
		if err == nil {
			err = json.NewEncoder(file).Encode(map[string]interface{}{"mcpServers": map[string]interface{}{"mecha": map[string]interface{}{"type": "http", "url": mcpURL, "headers": map[string]string{"Authorization": "Bearer " + token}}}})
		}
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			revoke()
			os.Remove(path)
			return claudeCodeProcessGrant{}, err
		}
		var once sync.Once
		return claudeCodeProcessGrant{MCPConfigFile: path, Binding: binding, Revoke: func() { once.Do(func() { revoke(); os.Remove(path) }) }}, nil
	}
	// Prompt is created on actual process start and removed with its grant.
	factory := func() (claudeCodeProcessGrant, error) {
		grant, err := grantFactory()
		if err != nil {
			return grant, err
		}
		path, err := writeClaudeCodePrompt(m.config.SessionRoot, saved.AgentID, prompt+"\nYour final answer is shown to the user. Nothing is sent automatically; use mecha_mailbox_send to message agents.")
		if err != nil {
			grant.Revoke()
			return claudeCodeProcessGrant{}, err
		}
		previous := grant.Revoke
		grant.Revoke = func() { previous(); os.Remove(path) }
		grant.SystemPromptFile = path
		return grant, nil
	}
	r, err := newClaudeCodeRuntime(ctx, claudeCodeRuntimeOptions{Pool: &m.claudeCodeProcesses, Config: config, Persistence: p, BeforeSubmit: before, Submit: func(ctx context.Context, size int, write func() error) error {
		lane := m.laneFor(p.binding.AgentID)
		lane.admission.Lock()
		defer lane.admission.Unlock()
		if (lane.resetPending || len(lane.resetToolGates) > 0) || lane.terminationPending > 0 {
			return errors.New("Claude reset pending before stdin")
		}
		if err := m.store.RenewEmbeddedOwner(ctx, p.binding, owner.identity, embeddedOwnerTTL); err != nil {
			return err
		}
		return write()
	}, AfterEcho: after, NewGrant: factory, ToolProducer: bridge.producer, Bridge: bridge})
	if err != nil {
		return nil, err
	}
	r.snapshot.ContextWindow = m.laneFor(p.binding.AgentID).claudeWindow(p.binding, saved.SelectedModel.ID)
	bridge.fatal = func(err error) {
		r.mu.Lock()
		proc := r.process
		r.mu.Unlock()
		if proc != nil {
			proc.fail(err)
		}
		m.claudeCodeProcesses.stop(p.binding.AgentID)
	}
	bridge.interrupt = func() { r.mu.Lock(); r.discardPartial = true; r.mu.Unlock(); r.Interrupt() }
	return r, nil
}
func claudeCodeV1Tool(name string) bool {
	switch name {
	case "mecha_mailbox_send", "mecha_send_image", "mecha_agent_urgency", "scratchpad", "mecha_progress", "mecha_reminder", "goal", "goal_snooze", "context_reset", "mecha_context", "mecha_worker_terminate":
		return true
	}
	return false
}

// Record against the exact embedded owner so a stale cleanup cannot fail a successor.
func (m *Manager) recordClaudeRestartStopFailure(owner embeddedOwner, hasOwner bool, cause error) error {
	if !hasOwner {
		return cause
	}
	return m.recordClaudeStopFailure(owner.binding, cause)
}

func (m *Manager) recordClaudeStopFailure(binding store.EmbeddedBinding, cause error) error {
	saved, err := m.store.ManagedAgent(context.Background(), binding.AgentID)
	if err != nil {
		return errors.Join(cause, err)
	}
	err = m.store.MarkEmbeddedOwnerFailed(context.Background(), binding, saved.LaunchOperationID, "PROCESS_STOP_FAILED", "Claude process stop unconfirmed")
	return errors.Join(cause, err)
}
