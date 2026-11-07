package managed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

// claudeChildBridge adapts the existing policy-scoped child catalog to the Claude MCP
// grant. It does not make an HTTP request into an admitted turn: checked must
// match the request to a persisted main-thread tool call, enforce the child's
// limits, and append the checked result. It must call recheck immediately
// before any effect, after its own serialization/admission waits.
type claudeChildBridge struct {
	db                 *store.Store
	parent, child      store.EmbeddedBinding
	attempt            int
	owner, incarnation string
	tools              map[string]agent.AgentTool
	catalog            []ClaudeCodeMCPTool
	checked            func(context.Context, ClaudeCodeMCPCall, agent.AgentTool, map[string]any, func(context.Context) error) (json.RawMessage, error)
}

func newClaudeChildBridge(db *store.Store, parent, child store.EmbeddedBinding, attempt int, owner, incarnation, cwd, orphanRoot string, checked func(context.Context, ClaudeCodeMCPCall, agent.AgentTool, map[string]any, func(context.Context) error) (json.RawMessage, error)) (*claudeChildBridge, func(), error) {
	if db == nil || parent.AgentID == "" || parent.SessionID == "" || parent.Generation == "" || parent.Epoch <= 0 || child.AgentID == "" || child.SessionID == "" || child.Generation != parent.Generation || child.Epoch <= 0 || attempt <= 0 || owner == "" || incarnation == "" || checked == nil {
		return nil, nil, errors.New("invalid Claude child bridge identity")
	}
	tools, cleanup, err := NewEmbeddedSubagentTools(cwd)
	if err != nil {
		return nil, nil, err
	}
	for i := range tools {
		if tools[i].Name == "bash" {
			tools[i] = newEmbeddedSubagentBashTool(cwd, orphanRoot)
		}
	}
	b := &claudeChildBridge{db: db, parent: parent, child: child, attempt: attempt, owner: owner, incarnation: incarnation, checked: checked, tools: make(map[string]agent.AgentTool)}
	for _, tool := range tools {
		schema, err := json.Marshal(tool.Parameters)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		b.tools[tool.Name] = tool
		b.catalog = append(b.catalog, ClaudeCodeMCPTool{Name: tool.Name, Description: tool.Description, InputSchema: schema})
	}
	return b, cleanup, nil
}

func (b *claudeChildBridge) check(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	role, err := b.db.SavedEmbeddedSessionRole(ctx, b.parent)
	if err != nil || role == "" {
		return errors.New("stale child parent binding")
	}
	rec, err := b.db.ManagedSubagent(ctx, b.parent, b.child.AgentID)
	if err != nil {
		return fmt.Errorf("child attempt lookup: %w", err)
	}
	if rec.ParentAgentID != b.parent.AgentID || rec.ParentSessionID != b.parent.SessionID || rec.ParentEpoch != b.parent.Epoch || rec.ParentGeneration != b.parent.Generation || rec.Attempt != b.attempt || rec.Status != "running" {
		return errors.New("stale child attempt")
	}
	return admitWorkflowStateTool(ctx, b.db, b.child.AgentID, name)
}

func (b *claudeChildBridge) grant() ClaudeCodeMCPGrant {
	return ClaudeCodeMCPGrant{Binding: b.child, Owner: b.owner, Incarnation: b.incarnation, Tools: b.catalog, ExecuteChecked: b.execute}
}

func (b *claudeChildBridge) execute(ctx context.Context, call ClaudeCodeMCPCall) (json.RawMessage, error) {
	if call.Binding != b.child || call.Owner != b.owner || call.Incarnation != b.incarnation || call.ToolUseID == "" {
		return nil, errors.New("stale child process")
	}
	tool, ok := b.tools[call.Name]
	if !ok {
		return nil, errors.New("unknown child tool")
	}
	var args map[string]any
	if json.Unmarshal(call.Arguments, &args) != nil || args == nil {
		return nil, errors.New("invalid child tool arguments")
	}
	validated, err := ai.ValidateToolArguments(ai.Tool{Name: tool.Name, Parameters: tool.Parameters}, ai.ToolCall{Name: tool.Name, Arguments: args})
	if err != nil {
		return nil, err
	}
	if err := b.check(ctx, tool.Name); err != nil {
		return nil, err
	}
	return b.checked(ctx, call, tool, validated, func(ctx context.Context) error { return b.check(ctx, tool.Name) })
}
