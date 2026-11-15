package managed

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/coding"
)

// EmbeddedEffectObservation is immutable evidence, never execution authority.
// Observers run after store methods return (outside their transactions).
type EmbeddedEffectObservation struct {
	Phase             string
	Binding           store.EmbeddedBinding
	CanonicalEffectID string
}
type embeddedEffectObserverError struct{ cause error }

func (e *embeddedEffectObserverError) Error() string { return "effect observer: " + e.cause.Error() }
func (e *embeddedEffectObserverError) Unwrap() error { return e.cause }
func (m *Manager) observeEmbeddedEffect(ctx context.Context, phase string, binding store.EmbeddedBinding, id string) error {
	if m.embeddedEffectObserver == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := m.embeddedEffectObserver(ctx, EmbeddedEffectObservation{Phase: phase, Binding: binding, CanonicalEffectID: id}); err != nil {
		return &embeddedEffectObserverError{cause: err}
	}
	return nil
}

// embeddedTurnToolAuthority owns one admitted turn's Execute witnesses and
// checked result callbacks. It must not outlive the turn. The lane owner must
// serialize producer append -> admission -> Execute -> checked result append;
// its witness mutex is NOT permission for concurrent HTTP/stdout persistence.
// No map or recovered receipt is independent execution authority.
type embeddedTurnToolAuthority struct {
	tools             []agent.AgentTool
	baseTools         []agent.AgentTool
	role              string
	beforeToolExecute func(context.Context, agent.BeforeToolCallContext) error
	persistence       *embeddedPersistence
	workflowState     *store.WorkflowState
	observeChecked    func(context.Context, agent.AgentMessage) error
	observerFailure   func() error
}

// Tools retains the existing Execute closures (including completion witnesses).
func (a *embeddedTurnToolAuthority) Tools() []agent.AgentTool {
	return append([]agent.AgentTool{}, a.tools...)
}
func (a *embeddedTurnToolAuthority) BeforeToolExecute(ctx context.Context, call agent.BeforeToolCallContext) error {
	if a.observerFailure != nil {
		if err := a.observerFailure(); err != nil {
			return err
		}
	}
	return a.beforeToolExecute(ctx, call)
}

// AppendCheckedMessage uses the internal SDK encoding, with no early success
// projection. Callers must stop the turn on any error; this is not a replay API.
func (a *embeddedTurnToolAuthority) AppendCheckedMessage(ctx context.Context, message agent.AgentMessage) error {
	if a.observerFailure != nil {
		if err := a.observerFailure(); err != nil {
			return err
		}
	}
	if err := a.persistence.AppendCheckedMessage(ctx, message); err != nil {
		return err
	}
	if a.observeChecked != nil {
		return a.observeChecked(ctx, message)
	}
	return nil
}

func (m *Manager) newEmbeddedTurnToolAuthority(ctx context.Context, owner embeddedOwner, p *embeddedPersistence, saved SavedAgentSession, initialTurn bool) (*embeddedTurnToolAuthority, error) {
	binding := owner.binding
	var observerMu sync.Mutex
	var observerFailure error
	latchObserverFailure := func(err error) {
		var failure *embeddedEffectObserverError
		if errors.As(err, &failure) {
			observerMu.Lock()
			observerFailure = err
			observerMu.Unlock()
		}
	}
	getObserverFailure := func() error { observerMu.Lock(); defer observerMu.Unlock(); return observerFailure }
	// Only an intent admitted by this SDK turn may cross the first reservation
	// window. A recovered intent has no such in-memory admission.
	freshToolIntents := make(map[string]*store.EmbeddedToolInvocation)
	toolExecutions := make(map[string]*store.EmbeddedToolInvocation)
	freshGoalIntents := make(map[string]*store.EmbeddedGoalInvocation)
	m.mu.Lock()
	goalProcess := m.embeddedStartupGeneration
	m.mu.Unlock()
	type goalExecution struct {
		invocation *store.EmbeddedGoalInvocation
		expected   *ai.ToolResultMessage
	}
	goalExecutions := make(map[string]goalExecution)
	var freshWorkerMu sync.Mutex
	p.toolInvocation = func(id string) *store.EmbeddedToolInvocation {
		freshWorkerMu.Lock()
		defer freshWorkerMu.Unlock()
		return toolExecutions[id]
	}
	p.toolChecked = func(id string) {
		freshWorkerMu.Lock()
		defer freshWorkerMu.Unlock()
		delete(toolExecutions, id)
	}
	toolAdmitted := func(id string) *store.EmbeddedToolInvocation {
		freshWorkerMu.Lock()
		defer freshWorkerMu.Unlock()
		w := freshToolIntents[id]
		delete(freshToolIntents, id)
		// Retain only this actual Execute's witness for its safe error disposition.
		// Store still requires post-domain completion before any checked result.
		toolExecutions[id] = w
		return w
	}
	toolCompleted := func(id string, w *store.EmbeddedToolInvocation) {
		freshWorkerMu.Lock()
		defer freshWorkerMu.Unlock()
		toolExecutions[id] = w
	}
	freshReminderIntents := make(map[string]bool)

	p.goalInvocation = func(id string) *store.EmbeddedGoalInvocation {
		freshWorkerMu.Lock()
		defer freshWorkerMu.Unlock()
		return goalExecutions[id].invocation
	}
	p.goalResult = func(resultCtx context.Context, result ai.ToolResultMessage, commit bool) error {
		freshWorkerMu.Lock()
		execution := goalExecutions[result.ToolCallID]
		if commit {
			delete(goalExecutions, result.ToolCallID)
		}
		freshWorkerMu.Unlock()
		if execution.expected == nil && result.ToolName != "goal" && result.ToolName != "goal_snooze" && result.ToolName != "workflow_snooze" {
			return nil
		}
		if !commit {
			if execution.expected == nil {
				// Pure pre-intent denials may be checked; saved DOMAIN/SDK
				// evidence is never a substitute for this Execute's completion.
				if result.IsError {
					_, err := m.store.RecoverEmbeddedEffect(resultCtx, binding, result.ToolCallID)
					if errors.Is(err, sql.ErrNoRows) {
						return nil
					}
					if err != nil {
						return err
					}
				}
				return errors.New("goal outcome unknown: no current Host Execute completion")
			}
			actual, err := json.Marshal(result.Content)
			if err != nil {
				return err
			}
			expected, err := json.Marshal(execution.expected.Content)
			if err != nil {
				return err
			}
			if result.ToolCallID != execution.expected.ToolCallID || result.ToolName != execution.expected.ToolName || result.IsError || !bytes.Equal(actual, expected) {
				return errors.New("goal SDK result differs from current Host Execute")
			}
			return m.store.RenewEmbeddedOwner(resultCtx, binding, owner.identity, embeddedOwnerTTL)
		}
		if execution.invocation != nil {
			return m.store.ObserveEmbeddedGoalInvocation(resultCtx, execution.invocation, p.goalEntry)
		}
		return nil
	}
	goalAdmitted := func(id string) *store.EmbeddedGoalInvocation {
		freshWorkerMu.Lock()
		defer freshWorkerMu.Unlock()
		w := freshGoalIntents[id]
		delete(freshGoalIntents, id)
		return w
	}
	goalCompleted := func(id string, w *store.EmbeddedGoalInvocation, result ai.ToolResultMessage) {
		freshWorkerMu.Lock()
		defer freshWorkerMu.Unlock()
		goalExecutions[id] = goalExecution{invocation: w, expected: &result}
	}
	compactionResults := make(map[string]*ai.ToolResultMessage)
	p.compactionResult = func(result ai.ToolResultMessage) (bool, error) {
		freshWorkerMu.Lock()
		expected := compactionResults[result.ToolCallID]
		delete(compactionResults, result.ToolCallID)
		freshWorkerMu.Unlock()
		if expected == nil {
			return false, nil
		} // status never starts a compaction
		if result.IsError || result.ToolName != expected.ToolName || result.ToolCallID != expected.ToolCallID {
			return false, errors.New("compaction SDK result has no fresh checked request")
		}
		actual, err := json.Marshal(result.Content)
		if err != nil {
			return false, err
		}
		saved, err := json.Marshal(expected.Content)
		if err != nil {
			return false, err
		}
		if !bytes.Equal(actual, saved) {
			return false, errors.New("compaction SDK result differs from saved request")
		}
		return true, nil
	}
	// Each SDK execution has its own expected result. A reused call ID must not
	// inherit the fresh execution's permission to observe an effect again.
	type reminderExecution struct {
		expected  *ai.ToolResultMessage
		replay    bool
		uncertain bool
	}
	reminderExecutions := make(map[string]reminderExecution)
	p.reminderResult = func(resultCtx context.Context, result ai.ToolResultMessage, commit bool) error {
		freshWorkerMu.Lock()
		execution := reminderExecutions[result.ToolCallID]
		freshWorkerMu.Unlock()
		if result.IsError {
			if commit {
				if !execution.replay {
					body, err := json.Marshal(result)
					if err != nil {
						return err
					}
					_ = m.store.ObserveEmbeddedEffect(resultCtx, binding, result.ToolCallID, body)
				}
				freshWorkerMu.Lock()
				delete(reminderExecutions, result.ToolCallID)
				delete(freshReminderIntents, result.ToolCallID)
				freshWorkerMu.Unlock()
			}
			return nil
		}
		if execution.uncertain {
			return errors.New("reminder outcome unknown: no checked SDK result")
		}
		if !commit && execution.expected != nil {
			actualContent, err := json.Marshal(result.Content)
			if err != nil {
				return err
			}
			expectedContent, err := json.Marshal(execution.expected.Content)
			if err != nil {
				return err
			}
			if result.ToolCallID != execution.expected.ToolCallID || result.ToolName != execution.expected.ToolName || result.IsError != execution.expected.IsError || !bytes.Equal(actualContent, expectedContent) {
				return errors.New("reminder SDK result differs from saved effect")
			}
		}
		if commit {
			if execution.expected != nil && !execution.replay {
				body, err := json.Marshal(result)
				if err != nil {
					return err
				}
				if err := m.store.ObserveEmbeddedEffect(resultCtx, binding, result.ToolCallID, body); err != nil {
					return err
				}
			}
			freshWorkerMu.Lock()
			delete(reminderExecutions, result.ToolCallID)
			delete(freshReminderIntents, result.ToolCallID)
			freshWorkerMu.Unlock()
		}
		return nil
	}
	// A non-nil list, even when empty, excludes every SDK built-in.
	tools := []agent.AgentTool{}
	trustedDiagnosticTools := map[string]*ai.Schema{}
	trustDiagnostics := func(tools []agent.AgentTool) {
		for _, tool := range tools {
			if tool.Parameters != nil {
				trustedDiagnosticTools[tool.Name] = tool.Parameters
			}
		}
	}
	role, err := m.store.EmbeddedSessionRole(ctx, binding)
	if err != nil {
		return nil, err
	}
	if role == "root" || role == "worker" {
		tools = append(tools, m.embeddedAddressedTool(owner, "mecha_mailbox_send", toolAdmitted, toolCompleted))
		tools = append(tools, m.embeddedSendImageTool(owner, toolAdmitted, toolCompleted))
		tools = append(tools, m.embeddedGoalTool(owner, goalAdmitted, goalCompleted))
		tools = append(tools, m.embeddedGoalSnoozeTool(owner, func() store.EmbeddedGoalSnoozeTurn {
			return store.EmbeddedGoalSnoozeTurn{UserSeq: p.turnSeq, UserDigest: p.turnDigest}
		}, goalAdmitted, goalCompleted))
		tools = append(tools, m.embeddedWorkflowSnoozeTool(owner, func() store.EmbeddedGoalSnoozeTurn {
			return store.EmbeddedGoalSnoozeTurn{UserSeq: p.turnSeq, UserDigest: p.turnDigest}
		}, goalAdmitted, goalCompleted))
		tools = append(tools, m.embeddedCompactionTool(owner))
		tools = append(tools, m.embeddedReminderTool(owner, func(id string) bool {
			freshWorkerMu.Lock()
			defer freshWorkerMu.Unlock()
			fresh := freshReminderIntents[id]
			delete(freshReminderIntents, id)
			return fresh
		}, func(id string) {
			freshWorkerMu.Lock()
			state := reminderExecutions[id]
			state.uncertain = true
			reminderExecutions[id] = state
			freshWorkerMu.Unlock()
		}, func(id string, expected ai.ToolResultMessage, replay bool) {
			freshWorkerMu.Lock()
			reminderExecutions[id] = reminderExecution{expected: &expected, replay: replay}
			freshWorkerMu.Unlock()
		}))
		p.resetChecked = func(result ai.ToolResultMessage) {
			m.mu.Lock()
			defer m.mu.Unlock()
			if req := m.contextResetRequested[owner.binding.AgentID]; req != nil && req.id == result.ToolCallID && result.ToolName == "context_reset" && !result.IsError {
				req.committed = true
			}
		}
		resetCB := func(req *contextResetRequest) {
			m.mu.Lock()
			if m.contextResetRequested == nil {
				m.contextResetRequested = make(map[string]*contextResetRequest)
			}
			m.contextResetRequested[owner.binding.AgentID] = req
			m.mu.Unlock()
		}
		tools = append(tools, m.embeddedContextResetTool(owner, resetCB, "context_reset"))
		if role == "worker" {
			terminateCB := func(req workerSelfTerminateRequest) {
				m.mu.Lock()
				if m.workerSelfTerminateRequested == nil {
					m.workerSelfTerminateRequested = make(map[string]workerSelfTerminateRequest)
				}
				m.workerSelfTerminateRequested[owner.binding.AgentID] = req
				m.mu.Unlock()
			}
			tools = append(tools, m.embeddedWorkerTerminateTool(owner, terminateCB))
		}
		tools = append(tools, m.embeddedScratchpadTool(owner))
		tools = append(tools, m.embeddedKBSearchTool(owner))
		tools = append(tools, m.embeddedKBReadTool(owner))
		tools = append(tools, m.embeddedKBWriteTool(owner))
		tools = append(tools, m.embeddedKBDeleteTool(owner))
		tools = append(tools, m.embeddedKBListTool(owner))
		tools = append(tools, m.embeddedWorkflowTransitionTool(owner, "mecha_workflow_transition"))
		tools = append(tools, m.embeddedWorkflowJournalNoteTool(owner))
		tools = append(tools, m.embeddedToolTimeoutTool(owner, "mecha_tool_timeout"))
		tools = append(tools, m.embeddedRestartRouterTool(owner))
		tools = append(tools, m.embeddedRequestInlineToolAllowanceTool(owner))
		tools = append(tools, m.embeddedBashTool(owner, saved.Cwd, role))
		m.ensureBackgroundBash()
		tools = append(tools, m.backgroundBashTools(owner, saved.Cwd)...)
		readTool, err := coding.CreateTool("read", saved.Cwd)
		if err != nil {
			return nil, fmt.Errorf("embedded read tool: %w", err)
		}
		configureEmbeddedReadSchema(&readTool)
		readTool.PromptGuidelines = append(readTool.PromptGuidelines, "When you run a command or tool to investigate, continue until the task is complete, or write your answer to the user.", "When a user asks a question or steers your work and you need to gather information or run tools to formulate your answer, always acknowledge the user with a brief conversational response first before starting information gathering.")
		editTool, err := coding.CreateTool("edit", saved.Cwd)
		if err != nil {
			return nil, fmt.Errorf("embedded edit tool: %w", err)
		}
		writeTool, err := coding.CreateTool("write", saved.Cwd)
		if err != nil {
			return nil, fmt.Errorf("embedded write tool: %w", err)
		}
		wrapTool := func(tool agent.AgentTool) agent.AgentTool {
			original := tool.Execute
			tool.Execute = func(callCtx context.Context, id string, args map[string]any, update agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
				if err := m.store.RenewEmbeddedOwner(callCtx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
					return agent.AgentToolResult{}, err
				}
				role, err := m.store.EmbeddedSessionRole(callCtx, owner.binding)
				if err != nil {
					return agent.AgentToolResult{}, err
				}
				if role != "root" && role != "worker" {
					return agent.AgentToolResult{}, fmt.Errorf("%s requires embedded root or worker", tool.Name)
				}
				return original(callCtx, id, args, update)
			}
			return tool
		}
		tools = append(tools, wrapTool(readTool), wrapTool(editTool), wrapTool(writeTool))
		if m.hasGoogleSearchAuth(ctx) {
			tools = append(tools, m.embeddedGoogleSearchTool(owner))
		}
		if m.hasCodexSearchAuth(ctx) {
			tools = append(tools, m.embeddedCodexSearchTool(owner))
		}
		tools = append(tools, m.EmbeddedSubagentPublicTools(binding)...)
		tools = append(tools, m.embeddedAgentUrgencyTool(owner, toolAdmitted, toolCompleted))
		tools = append(tools, m.embeddedProgressTool(owner))
		tools = append(tools, m.embeddedBlockedTool(owner))
		trustDiagnostics(tools)
		tools = append(tools, m.buildSessionTools(&saved)...)
		mcpEnd := len(tools)
		tools = append(tools, m.embeddedSessionTools(owner)...)
		tools = append(tools, m.embeddedWorkflowManagementTools(owner)...)
		trustDiagnostics(tools[mcpEnd:])
	}
	if m.embeddedExternalTool != nil {
		tool := *m.embeddedExternalTool
		if tool.Name == "" || tool.Parameters == nil || tool.Execute == nil {
			return nil, errors.New("invalid registered embedded external tool")
		}
		original := tool.Execute
		tool.Execute = func(callCtx context.Context, id string, args map[string]any, update agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if m.embeddedAfterIntent != nil {
				m.embeddedAfterIntent(callCtx)
			}
			// The checked SDK admission commits intent before this point. Renew
			// again at the final boundary: failure cannot authorize Execute.
			if err := m.store.RenewEmbeddedOwner(callCtx, binding, owner.identity, embeddedOwnerTTL); err != nil {
				return agent.AgentToolResult{}, err
			}
			return original(callCtx, id, args, update)
		}
		tools = append(tools, tool)
	}
	for i := range tools {
		// Install diagnostics only for the host-owned schema captured above.
		// External tool hooks and schema text are not trusted for disclosure.
		tool := tools[i]
		trusted := trustedDiagnosticTools[tool.Name] != nil && trustedDiagnosticTools[tool.Name] == tool.Parameters
		tools[i].ValidationError = func(args map[string]any) string {
			return embeddedInvalidToolArguments(&tool, args, trusted)
		}
		original := tools[i].Execute
		tools[i].Execute = func(ctx context.Context, id string, args map[string]any, update agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if err := getObserverFailure(); err != nil {
				return agent.AgentToolResult{}, err
			}
			result, err := original(ctx, id, args, update)
			latchObserverFailure(err)
			return result, err
		}
		tools[i] = m.wrapEmbeddedToolTimeout(binding.AgentID, tools[i])
	}
	baseTools := append([]agent.AgentTool(nil), tools...)
	lane := m.laneFor(binding.AgentID)
	activeWorkflowState, activeStateErr := m.store.GetManagedAgentActiveWorkflowState(ctx, binding.AgentID)
	if activeStateErr == nil && activeWorkflowState != nil && len(activeWorkflowState.AllowedTools) > 0 {
		var filteredTools []agent.AgentTool
		for _, tool := range baseTools {
			if store.IsToolAllowedInWorkflowState(tool.Name, activeWorkflowState.AllowedTools) {
				filteredTools = append(filteredTools, tool)
			}
		}
		tools = filteredTools
	} else if filteredTools, err := m.filterToolsForAgent(ctx, binding.AgentID, baseTools); err == nil {
		tools = filteredTools
	}
	if lane != nil {
		lane.setTools(baseTools, tools)
	}
	beforeToolExecute := func(callCtx context.Context, call agent.BeforeToolCallContext) error {
		if err := getObserverFailure(); err != nil {
			return err
		}
		if lane.embeddedResetPending() {
			return &store.ManagedAgentNotReadyError{AgentID: binding.AgentID}
		}
		if err := p.bindExecutionUser(callCtx); err != nil {
			return err
		}
		toolName := call.ToolCall.Name
		if isBackgroundBashTool(toolName) && role != "root" && role != "worker" {
			return &agent.PolicyDenial{Reason: "background bash requires embedded root or worker"}
		}
		if err := admitWorkflowStateTool(callCtx, m.store, binding.AgentID, toolName); err != nil {
			return &agent.PolicyDenial{Reason: err.Error()}
		}
		toolCallID := call.ToolCall.ID
		if toolCallID != "" && toolName != "" {
			var companionText string
			if p != nil && p.currentTurnAssistantText != "" {
				companionText = p.currentTurnAssistantText
				p.currentTurnAssistantText = ""
				if p.toolCompanionTexts == nil {
					p.toolCompanionTexts = make(map[string]string)
				}
				p.toolCompanionTexts[toolCallID] = companionText
			}
			if p != nil && toolName != "goal" && toolName != "goal_snooze" && toolName != "workflow_snooze" && toolName != "context_reset" && toolName != "reset" {
				p.toolExecuted = true
				if p.toolInputs == nil {
					p.toolInputs = make(map[string]any)
				}
				p.toolInputs[toolCallID] = call.ToolCall.Arguments
			}
			invocationKey := fmt.Sprintf("tool:%s:%s", binding.AgentID, toolCallID)
			entry, err := m.store.RecordManagedCompleteHistoryWithPresentationAndInput(callCtx, binding.AgentID, "managed_tool", "info", "Running "+toolName, invocationKey, &store.ManagedHistoryPresentation{
				Kind:          "tool",
				ToolName:      toolName,
				Outcome:       "active",
				Summary:       "Running",
				CompanionText: companionText,
			}, call.ToolCall.Arguments)
			if err == nil {
				m.publishCompleteHistory(entry)
			}
		}
		// Policy has already been checked against the current store state. Use
		// the unfiltered catalog for schema validation so a newly enabled tool
		// is not rejected by a stale per-turn projection.
		lookupTools := baseTools
		if currentLane := m.laneFor(binding.AgentID); currentLane != nil {
			if dynBase := currentLane.getBaseTools(); len(dynBase) > 0 {
				lookupTools = dynBase
			}
		}
		var registered *agent.AgentTool
		for i := range lookupTools {
			if call.ToolCall.Name == lookupTools[i].Name {
				registered = &lookupTools[i]
				break
			}
		}
		if registered == nil {
			return &agent.PolicyDenial{Reason: "embedded tool " + embeddedDiagnosticToolName(call.ToolCall.Name, trustedDiagnosticTools[call.ToolCall.Name] != nil) + " is not registered in the current catalog"}
		}
		// The SDK validates and normalizes Args before this callback; the raw
		// ToolCall retains provider values for history and stricter policy checks.
		// Direct callers without SDK arguments must still pass the current schema.
		validatedArgs := call.Args
		if validatedArgs == nil {
			validatedArgs = call.ToolCall.Arguments
		}
		if registered.Parameters == nil || !registered.Parameters.Check(validatedArgs) {
			return &agent.PolicyDenial{Reason: embeddedInvalidToolArguments(registered, validatedArgs, trustedDiagnosticTools[registered.Name] != nil && trustedDiagnosticTools[registered.Name] == registered.Parameters)}
		}
		// Check the original, uncoerced arguments before durable intent.
		if call.ToolCall.Name == "goal" {
			if role != "root" && role != "worker" {
				return &agent.PolicyDenial{Reason: "goal requires embedded root or worker"}
			}
			if _, err := embeddedGoalCommand(call.ToolCall.Arguments); err != nil {
				return &agent.PolicyDenial{Reason: err.Error()}
			}
		}
		if call.ToolCall.Name == "mecha_context" {
			if role != "root" && role != "worker" {
				return &agent.PolicyDenial{Reason: "context requires own root or worker"}
			}
			if len(call.ToolCall.Arguments) != 1 || call.ToolCall.Arguments["action"] != "status" {
				return &agent.PolicyDenial{Reason: "mecha_context only supports action=status; context is managed via context_reset and scratchpad"}
			}
		}
		if call.ToolCall.Name == "mecha_reminder" {
			if role != "root" && role != "worker" {
				return &agent.PolicyDenial{Reason: "reminder requires embedded root or worker"}
			}
			if _, err := embeddedReminderArguments(call.ToolCall.Arguments); err != nil {
				return &agent.PolicyDenial{Reason: err.Error()}
			}
		}
		if call.ToolCall.Name == "workflow_snooze" {
			if role != "root" && role != "worker" {
				return &agent.PolicyDenial{Reason: "workflow snooze requires embedded root or worker"}
			}
			if _, _, err := embeddedWorkflowSnoozeArguments(call.ToolCall.Arguments, 600000); err != nil {
				return &agent.PolicyDenial{Reason: err.Error()}
			}
		}

		if call.ToolCall.Name == "goal_snooze" {
			if role != "root" && role != "worker" {
				return &agent.PolicyDenial{Reason: "goal snooze requires embedded root or worker"}
			}
			if call.ToolCall.Arguments == nil {
				call.ToolCall.Arguments = make(map[string]any)
			}
			limitMs := m.goalSnoozeLimitMS(callCtx, binding.AgentID)
			if _, err := embeddedGoalSnoozeArguments(call.ToolCall.Arguments, limitMs); err != nil {
				return &agent.PolicyDenial{Reason: err.Error()}
			}
		}
		if call.ToolCall.Name == "context_reset" || call.ToolCall.Name == "reset" {
			if role != "root" && role != "worker" {
				return &agent.PolicyDenial{Reason: "context reset requires embedded root or worker"}
			}
			return nil
		}
		if call.ToolCall.Name == "scratchpad" {
			if role != "root" && role != "worker" {
				return &agent.PolicyDenial{Reason: "scratchpad requires embedded root or worker"}
			}
			return nil
		}
		if call.ToolCall.Name == "kb_search" || call.ToolCall.Name == "kb_read" || call.ToolCall.Name == "kb_write" || call.ToolCall.Name == "kb_list" {
			if role != "root" && role != "worker" {
				return &agent.PolicyDenial{Reason: call.ToolCall.Name + " requires embedded root or worker"}
			}
			return nil
		}
		if call.ToolCall.Name == "mecha_tool_timeout" || call.ToolCall.Name == "extend_tool_timeout" {
			if role != "root" && role != "worker" {
				return &agent.PolicyDenial{Reason: call.ToolCall.Name + " requires embedded root or worker"}
			}
			return nil
		}
		if isBackgroundBashTool(call.ToolCall.Name) {
			if role != "root" && role != "worker" {
				return &agent.PolicyDenial{Reason: "background bash requires embedded root or worker"}
			}
			if call.ToolCall.Name == "list_background_bash_jobs" || call.ToolCall.Name == "get_background_bash_job" {
				return nil
			}
		}
		if call.ToolCall.Name == "read" || call.ToolCall.Name == "edit" || call.ToolCall.Name == "write" || call.ToolCall.Name == "bash" {
			if role != "root" && role != "worker" {
				return &agent.PolicyDenial{Reason: call.ToolCall.Name + " requires embedded root or worker"}
			}
			if call.ToolCall.Name == "bash" {
				if _, parseErr := parseEmbeddedBash(call.ToolCall.Arguments); parseErr != nil {
					return &agent.PolicyDenial{Reason: parseErr.Error()}
				}
			}
			return nil
		}
		if call.ToolCall.Name == "mecha_mailbox_send" || call.ToolCall.Name == "mecha_send_image" || call.ToolCall.Name == "mecha_agent_urgency" {
			if role != "root" && role != "worker" {
				return &agent.PolicyDenial{Reason: "addressed managed tools require a saved root or worker"}
			}
			if call.ToolCall.Name == "mecha_agent_urgency" {
				if _, err := store.AgentUrgencyArguments(call.ToolCall.Arguments, binding.AgentID); err != nil {
					return &agent.PolicyDenial{Reason: err.Error()}
				}
			} else if call.ToolCall.Name != "mecha_send_image" {
				if err := embeddedAddressedArguments(call.ToolCall.Name, call.ToolCall.Arguments); err != nil {
					return &agent.PolicyDenial{Reason: err.Error()}
				}
			}
			if p.notice != nil || p.wake != nil {
				return &agent.PolicyDenial{Reason: "recovery control sources do not authorize new addressed effects"}
			}
			if err := m.authorizeEmbeddedWorkerLaunch(callCtx, owner); err != nil {
				return err
			}
		}
		intent, err := json.Marshal(call.ToolCall)
		if err != nil {
			return err
		}
		if err := m.store.RenewEmbeddedOwner(callCtx, binding, owner.identity, embeddedOwnerTTL); err != nil {
			return err
		}
		if call.ToolCall.Name == "goal" && call.ToolCall.Arguments["action"] == "status" {
			return nil // Read-only status has no unresolved effect intent.
		}
		if call.ToolCall.Name == "goal" || call.ToolCall.Name == "goal_snooze" || call.ToolCall.Name == "workflow_snooze" {
			freshWorkerMu.Lock()
			delete(freshGoalIntents, call.ToolCall.ID)
			delete(goalExecutions, call.ToolCall.ID)
			freshWorkerMu.Unlock()
			prior, err := m.store.RecoverEmbeddedEffect(callCtx, binding, call.ToolCall.ID)
			if err == nil {
				if !bytes.Equal(prior.Intent, intent) {
					return errors.New("goal call ID conflicts with prior arguments")
				}
				return errors.New("goal call outcome unknown or already committed: no new admission")
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}

		if call.ToolCall.Name == "mecha_reminder" {
			freshWorkerMu.Lock()
			reminderExecutions[call.ToolCall.ID] = reminderExecution{}
			delete(freshReminderIntents, call.ToolCall.ID)
			freshWorkerMu.Unlock()
			prior, err := m.store.RecoverEmbeddedEffect(callCtx, binding, call.ToolCall.ID)
			if err == nil {
				// A conflicting call is converted to a checked SDK error by Execute.
				// An identical unknown intent cannot execute again.
				if bytes.Equal(prior.Intent, intent) && prior.State == "unknown" {
					return errors.New("reminder outcome unknown: no checked SDK result")
				}
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if call.ToolCall.Name == "mecha_mailbox_send" || call.ToolCall.Name == "mecha_send_image" || call.ToolCall.Name == "mecha_agent_urgency" {
			freshWorkerMu.Lock()
			delete(freshToolIntents, call.ToolCall.ID)
			delete(toolExecutions, call.ToolCall.ID)
			freshWorkerMu.Unlock()
			prior, err := m.store.RecoverEmbeddedEffect(callCtx, binding, call.ToolCall.ID)
			if err == nil {
				if !bytes.Equal(prior.Intent, intent) {
					return errors.New("addressed tool call ID conflicts with original arguments")
				}
				return errors.New("addressed tool outcome UNKNOWN or already checked: no new execution")
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if call.ToolCall.Name == "mecha_context" && call.ToolCall.Arguments["action"] == "status" {
			return nil // read-only; no durable effect intent or compaction request
		}
		var goalAdmission *store.EmbeddedGoalInvocation
		var toolAdmission *store.EmbeddedToolInvocation
		if call.ToolCall.Name == "mecha_mailbox_send" || call.ToolCall.Name == "mecha_send_image" || call.ToolCall.Name == "mecha_agent_urgency" {
			toolAdmission, err = m.store.RecordEmbeddedToolInvocation(callCtx, binding, goalProcess, p.turnSeq, p.turnDigest, p.mailAttempt, call.ToolCall)
		} else if call.ToolCall.Name == "goal" || call.ToolCall.Name == "goal_snooze" || call.ToolCall.Name == "workflow_snooze" {
			goalAdmission, err = m.store.RecordEmbeddedGoalInvocation(callCtx, binding, goalProcess, p.turnSeq, p.turnDigest, p.mailAttempt, call.ToolCall.ID, intent)
		} else {
			err = m.store.RecordEmbeddedEffectIntent(callCtx, binding, call.ToolCall.ID, intent)
		}
		if err != nil {
			return err
		}
		if err := m.observeEmbeddedEffect(callCtx, "intent", binding, call.ToolCall.ID); err != nil {
			latchObserverFailure(err)
			return err
		}
		aliasTrace("checked-intent", binding.AgentID, binding.SessionID, binding.Generation, call.ToolCall.ID)
		if m.embeddedAfterCheckedIntent != nil {
			m.embeddedAfterCheckedIntent(callCtx)
		}
		// Intent is durable, but ownership may change before this callback
		// returns. Fail admission here, not inside Execute's checked result path.
		if err := m.store.RenewEmbeddedOwner(callCtx, binding, owner.identity, embeddedOwnerTTL); err != nil {
			return err
		}

		if call.ToolCall.Name == "mecha_reminder" {
			freshWorkerMu.Lock()
			freshReminderIntents[call.ToolCall.ID] = true
			freshWorkerMu.Unlock()
		}
		if call.ToolCall.Name == "goal" || call.ToolCall.Name == "goal_snooze" || call.ToolCall.Name == "workflow_snooze" {
			freshWorkerMu.Lock()
			freshGoalIntents[call.ToolCall.ID] = goalAdmission
			freshWorkerMu.Unlock()
		}

		if toolAdmission != nil {
			freshWorkerMu.Lock()
			freshToolIntents[call.ToolCall.ID] = toolAdmission
			freshWorkerMu.Unlock()
		}
		return nil
	}
	return &embeddedTurnToolAuthority{tools: tools, baseTools: baseTools, role: role, beforeToolExecute: beforeToolExecute, persistence: p, workflowState: activeWorkflowState, observerFailure: getObserverFailure,
		observeChecked: func(ctx context.Context, message agent.AgentMessage) error {
			result, ok := message.(ai.ToolResultMessage)
			if !ok || result.IsError {
				return nil
			}
			switch result.ToolName {
			case "mecha_mailbox_send", "mecha_send_image", "mecha_agent_urgency":
				err := m.observeEmbeddedEffect(ctx, "checked", binding, result.ToolCallID)
				latchObserverFailure(err)
				return err
			}
			return nil
		}}, nil
}
