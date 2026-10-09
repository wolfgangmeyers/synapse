package managed

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/coding"
)

type SubagentSpawnRequest struct {
	AgentID      string
	Description  string
	SubagentType string
	MaxTurns     int // 0 = unlimited
	Prompt       string
	Resume       string // ID of terminal child to resume with Prompt
}

type SubagentSpawnResult struct {
	AgentID string
	Status  string // "running" or "queued"
}

type subagentTask struct {
	parent       store.EmbeddedBinding
	childID      string
	attempt      int
	subagentType string
	description  string
	maxTurns     int
	prompt       string

	mu               sync.Mutex
	status           string
	bufferedSteering []string
	cancel           context.CancelFunc
	activeSession    *coding.Session

	startTime       time.Time
	baseTurns       int
	baseToolUses    int
	baseTokens      int
	baseDuration    float64
	turnCount       int
	toolUseCount    int
	totalTokens     int
	durationSeconds float64
	wrapUpSteered   bool
	hardAborted     bool
	stopped         bool
	leaseErr        error
}

func resolveSubagentSystemPrompt(subagentType string) string {
	switch strings.ToLower(strings.TrimSpace(subagentType)) {
	case "plan":
		return `# CRITICAL: READ-ONLY MODE - NO FILE MODIFICATIONS
You are a software architect and planning specialist.
Your role is EXCLUSIVELY to explore the codebase and design implementation plans.
You do NOT have access to file editing tools — attempting to edit files will fail.

You are STRICTLY PROHIBITED from:
- Creating new files
- Modifying existing files
- Deleting files
- Moving or copying files
- Creating temporary files anywhere, including /tmp
- Using redirect operators (>, >>, |) or heredocs to write to files
- Running ANY commands that change system state

# Planning Process
1. Understand requirements
2. Investigate thoroughly (read files, find patterns, understand architecture)
3. Design solution based on your assigned perspective
4. Detail the plan with step-by-step implementation strategy

# Requirements
- Consider trade-offs and architectural decisions
- Identify dependencies and sequencing
- Anticipate potential challenges
- Follow existing patterns where appropriate

### Responsiveness & Communication
When a user asks a question or steers your work and you need to gather information or run tools to formulate your answer, always acknowledge the user with a brief conversational response first before starting information gathering.

# Tool Usage
- Use the read tool for reading files
- Use Bash ONLY for read-only operations
- When you run a command or tool to investigate, continue executing tools until the task is complete, or write your answer.
- Never simulate, roleplay, or print bracketed tool observations or completion notifications in message text. Always invoke declared tools directly.

# Output Format
- Use absolute file paths
- Do not use emojis
- End your response with:

### Critical Files for Implementation
List 3-5 most important files to modify, with 1-line justification each.`
	default:
		return `You are an expert coding assistant operating inside pi, a coding agent harness. You help users by reading files, executing commands, editing code, and writing new files.

### Responsiveness & Communication
When a user asks a question or steers your work and you need to gather information or run tools to formulate your answer, always acknowledge the user with a brief conversational response first before starting information gathering.`
	}
}

func (m *Manager) subagentCapacity() int {
	if m.config.Limits.SubagentCapacity > 0 {
		return m.config.Limits.SubagentCapacity
	}
	return DefaultSubagentCapacity
}

func (m *Manager) finalizeQueuedTask(task *subagentTask, status, resultText string) {
	task.mu.Lock()
	task.status = status
	task.stopped = true
	task.mu.Unlock()

	err := m.store.UpdateManagedSubagentAttempt(context.Background(), task.parent, task.childID, task.attempt, "queued", store.ManagedSubagentResult{
		Status:          status,
		TurnCount:       task.baseTurns,
		ToolUseCount:    task.baseToolUses,
		TotalTokens:     task.baseTokens,
		DurationSeconds: task.baseDuration,
		ResultText:      resultText,
	})

	m.subagentsMu.Lock()
	if current := m.subagentsRegistry[task.childID]; current == task && current.attempt == task.attempt {
		delete(m.subagentsRegistry, task.childID)
	}
	m.subagentsMu.Unlock()

	if err == nil {
		latestRecord, err := m.store.ManagedSubagent(context.Background(), task.parent, task.childID)
		if err == nil && latestRecord != nil {
			m.AsyncDeliverSubagentParentWake(task.parent, *latestRecord)
		}
	}
	m.resumeParentGoalIfIdle(task.parent)
}

func (m *Manager) closeSubagents() {
	m.subagentsMu.Lock()
	for _, task := range m.subagentsActive {
		task.mu.Lock()
		if task.cancel != nil {
			task.cancel()
		}
		if task.activeSession != nil {
			task.activeSession.Abort()
		}
		task.mu.Unlock()
	}

	queuedList := m.subagentsQueue
	m.subagentsQueue = nil
	m.subagentsMu.Unlock()

	for _, queued := range queuedList {
		m.finalizeQueuedTask(queued, "stopped", "Subagent stopped on router close.")
	}
}

// TimeoutExpiredSubagents stops and hard-aborts any running subagent whose execution has exceeded maxDuration.
func (m *Manager) TimeoutExpiredSubagents(maxDuration time.Duration) {
	if maxDuration <= 0 {
		maxDuration = 1 * time.Hour
	}
	now := time.Now()
	m.subagentsMu.Lock()
	var toAbort []*subagentTask
	for _, task := range m.subagentsActive {
		if task != nil && !task.startTime.IsZero() && now.Sub(task.startTime) > maxDuration {
			toAbort = append(toAbort, task)
		}
	}
	m.subagentsMu.Unlock()

	for _, task := range toAbort {
		task.mu.Lock()
		task.stopped = true
		task.hardAborted = true
		if task.cancel != nil {
			task.cancel()
		}
		if task.activeSession != nil {
			task.activeSession.Abort()
		}
		task.mu.Unlock()
	}
}

func (m *Manager) removeFromQueueLocked(childID string) {
	filtered := m.subagentsQueue[:0]
	for _, task := range m.subagentsQueue {
		if task.childID != childID {
			filtered = append(filtered, task)
		}
	}
	m.subagentsQueue = filtered
}

func (m *Manager) releaseSubagentSlot(task *subagentTask) {
	if task == nil {
		return
	}
	m.subagentsMu.Lock()
	defer m.subagentsMu.Unlock()
	if current := m.subagentsActive[task.childID]; current == task && current.attempt == task.attempt {
		delete(m.subagentsActive, task.childID)
		m.drainQueueLocked()
	}
}

func (m *Manager) drainQueueLocked() {
	for len(m.subagentsQueue) > 0 && len(m.subagentsActive) < m.subagentCapacity() {
		candidate := m.subagentsQueue[0]
		m.subagentsQueue = m.subagentsQueue[1:]

		candidate.mu.Lock()
		if candidate.stopped || candidate.status != "queued" {
			candidate.mu.Unlock()
			continue
		}
		candidate.status = "running"
		candidate.mu.Unlock()

		m.subagentsActive[candidate.childID] = candidate

		// Attempt queued -> running CAS in store
		ctx := m.runCtx
		err := m.store.UpdateManagedSubagentAttempt(ctx, candidate.parent, candidate.childID, candidate.attempt, "queued", store.ManagedSubagentResult{
			Status:          "running",
			TurnCount:       candidate.baseTurns,
			ToolUseCount:    candidate.baseToolUses,
			TotalTokens:     candidate.baseTokens,
			DurationSeconds: candidate.baseDuration,
		})
		if err != nil {
			delete(m.subagentsActive, candidate.childID)
			candidate.mu.Lock()
			candidate.status = "error"
			candidate.mu.Unlock()
			if current := m.subagentsRegistry[candidate.childID]; current == candidate && current.attempt == candidate.attempt {
				delete(m.subagentsRegistry, candidate.childID)
			}
			continue
		}

		started := m.startManagedTask(func() { m.runSubagent(candidate) })
		if !started {
			delete(m.subagentsActive, candidate.childID)
			m.finalizeQueuedTask(candidate, "stopped", "Subagent stopped on router close.")
			return
		}
	}
}

func (m *Manager) resolveSubagentModel(record store.ManagedAgentRecord) (*ai.Model, agent.StreamFn, error) {
	if record.SelectedModel.Provider == "" || record.SelectedModel.ID == "" {
		record.SelectedModel.Provider = "antigravity"
		record.SelectedModel.ID = "gemini-3.8-flash"
	}
	model := ai.GetModel(record.SelectedModel.Provider, record.SelectedModel.ID)
	if model == nil && (record.SelectedModel.Provider == "antigravity" || record.SelectedModel.Provider == "google") {
		model = ai.GetModel("google", record.SelectedModel.ID)
	}
	if model == nil && record.SelectedModel.Provider == "antigravity" {
		model = &ai.Model{
			ID:            record.SelectedModel.ID,
			Provider:      "antigravity",
			Api:           "google-generative-ai",
			ContextWindow: 1048576,
			MaxTokens:     8192,
		}
	}
	var stream agent.StreamFn
	if m.embeddedModelResolver != nil {
		resolved := m.embeddedModelResolver(record)
		if resolved == nil {
			return nil, nil, errors.New("embedded model is unavailable")
		}
		if resolved.Provider == "openai-codex" {
			model = resolved
		} else {
			if model != nil {
				copyModel := *model
				copyModel.ContextWindow, copyModel.MaxTokens = resolved.ContextWindow, resolved.MaxTokens
				model = &copyModel
			} else {
				model = resolved
			}
			stream = func(requestCtx context.Context, requestModel *ai.Model, transcript ai.TranscriptContext, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
				return ai.StreamSimple(requestCtx, resolved, ai.Context{
					Messages: transcript.Messages,
				}, opts)
			}
		}
	}
	if model == nil {
		return nil, nil, errors.New("embedded model is unavailable")
	}
	if stream == nil {
		if model.Provider == "antigravity" || model.Provider == "google" || record.SelectedModel.Provider == "antigravity" || record.SelectedModel.Provider == "google" {
			stream = func(requestCtx context.Context, requestModel *ai.Model, transcript ai.TranscriptContext, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
				return m.streamEmbeddedGoogle(requestCtx, requestModel, transcript, opts)
			}
		} else if model.Provider == "google-vertex" || record.SelectedModel.Provider == "google-vertex" {
			stream = func(requestCtx context.Context, requestModel *ai.Model, transcript ai.TranscriptContext, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
				return m.streamEmbeddedVertex(requestCtx, requestModel, transcript, opts)
			}
		} else if model.Provider == "openai-codex" || record.SelectedModel.Provider == "openai-codex" {
			var used string
			stream = func(requestCtx context.Context, requestModel *ai.Model, transcript ai.TranscriptContext, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
				if opts == nil {
					opts = &ai.SimpleStreamOptions{}
				} else {
					copy := *opts
					opts = &copy
				}
				existingOnPayload := opts.OnPayload
				opts.OnPayload = func(payload any, model *ai.Model) (any, error) {
					if m, ok := payload.(map[string]any); ok {
						delete(m, "max_output_tokens")
					}
					if existingOnPayload != nil {
						return existingOnPayload(payload, model)
					}
					return payload, nil
				}
				opts.CodexAuth = func(authCtx context.Context) (ai.ModelAuth, error) {
					auth, authErr := m.embeddedCodexAuth(authCtx)
					if authErr == nil {
						used = auth.APIKey
					}
					return auth, authErr
				}
				return m.streamEmbeddedCodexWith401Rotation(requestCtx, requestModel, transcript, opts, &used)
			}
		}
	}
	return model, stream, nil
}

func (m *Manager) SpawnSubagent(ctx context.Context, parent store.EmbeddedBinding, req SubagentSpawnRequest) (*SubagentSpawnResult, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	if m.isClosed() {
		return nil, errors.New("manager is closed")
	}

	// 1. Resume existing subagent
	if req.Resume != "" {
		childID := req.Resume
		resumedRecord, err := m.store.ResumeManagedSubagent(ctx, parent, childID, req.Prompt, req.MaxTurns)
		if err != nil {
			return nil, err
		}

		task := &subagentTask{
			parent:          parent,
			childID:         childID,
			attempt:         resumedRecord.Attempt,
			subagentType:    resumedRecord.SubagentType,
			description:     resumedRecord.Description,
			maxTurns:        resumedRecord.MaxTurns,
			prompt:          req.Prompt,
			status:          "queued",
			baseTurns:       resumedRecord.TurnCount,
			baseToolUses:    resumedRecord.ToolUseCount,
			baseTokens:      resumedRecord.TotalTokens,
			baseDuration:    resumedRecord.DurationSeconds,
			durationSeconds: resumedRecord.DurationSeconds,
		}

		m.subagentsMu.Lock()
		defer m.subagentsMu.Unlock()
		m.subagentsRegistry[childID] = task

		if len(m.subagentsActive) < m.subagentCapacity() {
			err := m.store.UpdateManagedSubagentAttempt(ctx, parent, childID, task.attempt, "queued", store.ManagedSubagentResult{
				Status:          "running",
				TurnCount:       task.baseTurns,
				ToolUseCount:    task.baseToolUses,
				TotalTokens:     task.baseTokens,
				DurationSeconds: task.baseDuration,
			})
			if err != nil {
				delete(m.subagentsRegistry, childID)
				return nil, err
			}
			task.status = "running"
			m.subagentsActive[childID] = task
			started := m.startManagedTask(func() { m.runSubagent(task) })
			if !started {
				delete(m.subagentsActive, childID)
				m.finalizeQueuedTask(task, "stopped", "Subagent stopped on router close.")
				return nil, errors.New("manager is closing")
			}
			return &SubagentSpawnResult{AgentID: childID, Status: "running"}, nil
		}

		m.subagentsQueue = append(m.subagentsQueue, task)
		return &SubagentSpawnResult{AgentID: childID, Status: "queued"}, nil
	}

	// 2. New subagent spawn
	childID := req.AgentID
	if childID == "" {
		id, err := m.ids.Next()
		if err != nil {
			return nil, err
		}
		childID = fmt.Sprintf("subagent-%s-%s", parent.AgentID, id)
	}

	kind := req.SubagentType
	if kind == "" {
		kind = "general-purpose"
	}

	childRecord := store.ManagedSubagentRecord{
		AgentID:       childID,
		ParentAgentID: parent.AgentID,
		Description:   req.Description,
		SubagentType:  kind,
		Status:        "queued",
		MaxTurns:      req.MaxTurns,
	}

	if err := m.store.CreateManagedSubagent(ctx, parent, childRecord); err != nil {
		return nil, err
	}

	task := &subagentTask{
		parent:       parent,
		childID:      childID,
		attempt:      1,
		subagentType: kind,
		description:  req.Description,
		maxTurns:     req.MaxTurns,
		prompt:       req.Prompt,
		status:       "queued",
	}

	m.subagentsMu.Lock()
	defer m.subagentsMu.Unlock()
	m.subagentsRegistry[childID] = task

	if len(m.subagentsActive) < m.subagentCapacity() {
		err := m.store.UpdateManagedSubagentAttempt(ctx, parent, childID, 1, "queued", store.ManagedSubagentResult{
			Status: "running",
		})
		if err != nil {
			delete(m.subagentsRegistry, childID)
			return nil, err
		}
		task.status = "running"
		m.subagentsActive[childID] = task
		started := m.startManagedTask(func() { m.runSubagent(task) })
		if !started {
			delete(m.subagentsActive, childID)
			m.finalizeQueuedTask(task, "stopped", "Subagent stopped on router close.")
			return nil, errors.New("manager is closing")
		}
		return &SubagentSpawnResult{AgentID: childID, Status: "running"}, nil
	}

	m.subagentsQueue = append(m.subagentsQueue, task)
	return &SubagentSpawnResult{AgentID: childID, Status: "queued"}, nil
}

func (m *Manager) SteerSubagent(ctx context.Context, parent store.EmbeddedBinding, childID, message string) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	record, err := m.store.ManagedSubagent(ctx, parent, childID)
	if err != nil {
		return err
	}
	if IsTerminalSubagentStatus(record.Status) {
		return errors.New("cannot steer terminal subagent")
	}

	m.subagentsMu.Lock()
	task := m.subagentsRegistry[childID]
	m.subagentsMu.Unlock()
	if task == nil {
		return errors.New("subagent task not found")
	}

	task.mu.Lock()
	defer task.mu.Unlock()
	if task.status == "queued" {
		task.bufferedSteering = append(task.bufferedSteering, message)
		return nil
	}
	if task.status == "running" {
		if task.activeSession != nil {
			task.activeSession.Steer(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: message}}})
		} else {
			task.bufferedSteering = append(task.bufferedSteering, message)
		}
		return nil
	}
	return errors.New("cannot steer terminal subagent")
}

func (m *Manager) StopSubagent(ctx context.Context, parentAgentID, childID string) error {
	m.subagentsMu.Lock()
	task := m.subagentsActive[childID]
	if task == nil {
		task = m.subagentsRegistry[childID]
	}
	m.subagentsMu.Unlock()
	if task == nil {
		return errors.New("subagent not found")
	}
	task.mu.Lock()
	task.stopped = true
	if task.cancel != nil {
		task.cancel()
	}
	if task.activeSession != nil {
		task.activeSession.Abort()
	}
	taskStatus := task.status
	task.mu.Unlock()
	m.subagentsMu.Lock()
	m.removeFromQueueLocked(childID)
	m.subagentsMu.Unlock()
	_ = m.store.UpdateManagedSubagentAttempt(ctx, task.parent, childID, task.attempt, taskStatus, store.ManagedSubagentResult{
		Status:          "stopped",
		TurnCount:       task.turnCount,
		ToolUseCount:    task.toolUseCount,
		TotalTokens:     task.totalTokens,
		DurationSeconds: task.durationSeconds,
	})
	m.releaseSubagentSlot(task)

	desc := task.description
	if desc == "" {
		if rec, err := m.store.ManagedSubagent(ctx, task.parent, childID); err == nil && rec != nil {
			desc = rec.Description
		}
	}
	m.recordSubagentCompletionHistory(task.parent.AgentID, childID, task.attempt, desc, "failed", "stopped", "", "Subagent stopped.")
	subject := fmt.Sprintf("[System Notice] Subagent Terminated: %s", childID)
	body := fmt.Sprintf("Subagent %s (\"%s\") was stopped. You can fetch its latest state and findings using get_subagent_result(agent_id=\"%s\").\n\nAutomated system notification. No mailbox reply is needed or expected. Proceed directly with your next step.", childID, desc, childID)
	source := protocol.MessageSource{Kind: "system"}
	msgID := fmt.Sprintf("subagent-terminated-%s-%d", childID, task.attempt)
	now := formatManagedTime(m.clock.Now())
	envelope := protocol.Envelope{
		ID:        msgID,
		From:      "system",
		To:        task.parent.AgentID,
		Subject:   &subject,
		Body:      body,
		CreatedAt: now,
		Source:    &source,
	}
	result, err := m.store.Enqueue(ctx, envelope, store.EnqueueOptions{
		Conversation: &store.ConversationOptions{
			Body:   &body,
			Source: &source,
		},
	})
	if err == nil && (result.Created || result.ConversationCreated) {
		m.mu.Lock()
		publish := m.embeddedMailPublisher
		m.mu.Unlock()
		if publish != nil {
			publish(result)
		} else {
			_, _ = m.store.ResolveMessageTarget(ctx, result.Message.ID)
		}
	}
	m.OfferDirect(task.parent.AgentID)
	return nil
}

func (m *Manager) finalizeSubagent(task *subagentTask, terminalStatus, resultText, errorMsg string) {
	task.mu.Lock()
	if IsTerminalSubagentStatus(task.status) {
		task.mu.Unlock()
		return
	}
	task.status = terminalStatus
	now := m.clock.Now()
	if !task.startTime.IsZero() {
		task.durationSeconds = task.baseDuration + now.Sub(task.startTime).Seconds()
	}
	finalTurns := task.baseTurns + task.turnCount
	finalTools := task.baseToolUses + task.toolUseCount
	finalTokens := task.baseTokens + task.totalTokens
	finalDuration := task.durationSeconds
	task.mu.Unlock()

	err := m.store.UpdateManagedSubagentAttempt(context.Background(), task.parent, task.childID, task.attempt, "running", store.ManagedSubagentResult{
		Status:          terminalStatus,
		TurnCount:       finalTurns,
		ToolUseCount:    finalTools,
		TotalTokens:     finalTokens,
		DurationSeconds: finalDuration,
		ResultText:      resultText,
		Error:           errorMsg,
	})
	if err != nil {
		// CAS failed or stale parent fence; fail closed: do not notify
		return
	}

	// Clean up terminal task from registry guarded by pointer and attempt comparison
	m.subagentsMu.Lock()
	if current := m.subagentsRegistry[task.childID]; current == task && current.attempt == task.attempt {
		delete(m.subagentsRegistry, task.childID)
	}
	m.subagentsMu.Unlock()

	latestRecord, err := m.store.ManagedSubagent(context.Background(), task.parent, task.childID)
	outcome := "completed"
	if terminalStatus != "completed" && terminalStatus != "steered" {
		outcome = "failed"
	}
	desc := strings.TrimSpace(task.description)
	if desc == "" && latestRecord != nil {
		desc = strings.TrimSpace(latestRecord.Description)
	}
	m.recordSubagentCompletionHistory(task.parent.AgentID, task.childID, task.attempt, desc, outcome, terminalStatus, resultText, errorMsg)

	if err == nil && latestRecord != nil {
		rec := *latestRecord
		if latestRecord.Attempt != task.attempt || latestRecord.Status != terminalStatus {
			rec.Attempt = task.attempt
			rec.Status = terminalStatus
			rec.ResultText = resultText
			rec.Error = errorMsg
			rec.TurnCount = finalTurns
			rec.ToolUseCount = finalTools
			rec.TotalTokens = finalTokens
			rec.DurationSeconds = finalDuration
		}
		m.AsyncDeliverSubagentParentWake(task.parent, rec)
	}
}

func (m *Manager) recordSubagentCompletionHistory(parentAgentID, childID string, attempt int, description, outcome, status, resultText, errorMsg string) {
	if parentAgentID == "" || childID == "" {
		return
	}
	now := m.clock.Now()
	nowStr := formatManagedTime(now)

	level := "info"
	message := "Subagent completed"
	if outcome == "failed" {
		level = "error"
		message = "Subagent failed"
	}

	summary := "Subagent " + outcome
	if description != "" {
		summary = "Subagent " + outcome + ": " + description
	} else if childID != "" {
		summary = "Subagent " + outcome + ": " + childID
	}
	if !store.IsSafeManagedHistoryText(summary, 512, true) {
		summary = "Subagent " + outcome + ": " + childID
		if !store.IsSafeManagedHistoryText(summary, 512, true) {
			summary = "Subagent " + outcome
		}
	}

	presentation := &store.ManagedHistoryPresentation{
		Kind:       "subagent_complete",
		ToolName:   "subagent",
		Outcome:    outcome,
		Summary:    summary,
		ChildLabel: childID,
		OccurredAt: nowStr,
	}

	toolInput := map[string]any{
		"agent_id": childID,
		"status":   status,
	}
	if description != "" {
		toolInput["description"] = description
	}

	var messageContent *string
	if strings.TrimSpace(resultText) != "" {
		trimmed := strings.TrimSpace(resultText)
		messageContent = &trimmed
	} else if strings.TrimSpace(errorMsg) != "" {
		trimmed := strings.TrimSpace(errorMsg)
		messageContent = &trimmed
	}

	eventKey := fmt.Sprintf("subagent-complete:%s:%d", childID, attempt)
	entry, err := m.store.RecordManagedCompleteHistoryWithDetails(
		context.Background(),
		parentAgentID,
		"managed_subagent",
		level,
		message,
		eventKey,
		presentation,
		toolInput,
		messageContent,
	)
	if err == nil {
		m.publishCompleteHistory(entry)
	}
}

func (m *Manager) runSubagent(task *subagentTask) {
	defer m.resumeParentGoalIfIdle(task.parent)
	defer m.releaseSubagentSlot(task)

	taskCtx, cancel := context.WithCancel(m.runCtx)
	task.mu.Lock()
	task.cancel = cancel
	task.startTime = m.clock.Now()
	task.mu.Unlock()
	defer cancel()

	var terminalStatus, resultText, errorMsg string
	var childBinding store.EmbeddedBinding
	var ownerID string
	var cancelRenew context.CancelFunc
	var renewDone chan struct{}

	defer func() {
		if cancelRenew != nil {
			cancelRenew()
		}
		if renewDone != nil {
			<-renewDone
		}
		if childBinding.OwnerToken > 0 && ownerID != "" {
			_ = m.store.ReleaseEmbeddedOwner(context.Background(), childBinding, ownerID)
		}
		if m.isClosed() && !task.stopped {
			return
		}
		if terminalStatus != "" {
			m.finalizeSubagent(task, terminalStatus, resultText, errorMsg)
		}
	}()

	childAgent, err := m.store.ManagedAgent(m.runCtx, task.childID)
	if err != nil {
		terminalStatus, errorMsg = "error", err.Error()
		return
	}

	childBinding = store.EmbeddedBinding{
		AgentID:    task.childID,
		SessionID:  task.childID + "-session",
		Epoch:      1,
		Generation: task.parent.Generation,
	}

	// 1. Explicitly bind child subagent role if not yet bound - check error failclosed
	savedRole, err := m.store.SavedEmbeddedSessionRole(m.runCtx, childBinding)
	if err != nil || savedRole == "" {
		if err := m.store.BindEmbeddedSession(m.runCtx, childBinding, "subagent"); err != nil {
			terminalStatus, errorMsg = "error", fmt.Sprintf("bind child session: %v", err)
			return
		}
	}

	// 2. Acquire owner lease for child using embeddedOwnerTTL
	ownerID, err = newEmbeddedOwnerIdentity()
	if err != nil {
		terminalStatus, errorMsg = "error", err.Error()
		return
	}
	var token int64
	for attempt := 0; attempt < 50; attempt++ {
		token, err = m.store.AcquireEmbeddedOwner(taskCtx, childBinding, ownerID, embeddedOwnerTTL)
		if err == nil {
			break
		}
		if taskCtx.Err() != nil {
			task.mu.Lock()
			isStopped := task.stopped
			task.mu.Unlock()
			if isStopped || taskCtx.Err() == context.Canceled {
				terminalStatus, resultText = "stopped", "Subagent stopped."
			} else {
				terminalStatus, errorMsg = "error", taskCtx.Err().Error()
			}
			return
		}
		var held *store.EmbeddedOwnerLeaseHeld
		if errors.As(err, &held) {
			wait := held.RetryAfter + 10*time.Millisecond
			if wait < 10*time.Millisecond {
				wait = 10 * time.Millisecond
			}
			if wait > time.Second {
				wait = time.Second
			}
			select {
			case <-taskCtx.Done():
				task.mu.Lock()
				isStopped := task.stopped
				task.mu.Unlock()
				if isStopped || taskCtx.Err() == context.Canceled {
					terminalStatus, resultText = "stopped", "Subagent stopped."
				} else {
					terminalStatus, errorMsg = "error", taskCtx.Err().Error()
				}
				return
			case <-time.After(wait):
				continue
			}
		}
		terminalStatus, errorMsg = "error", fmt.Sprintf("acquire child owner: %v", err)
		return
	}
	if token == 0 {
		terminalStatus, errorMsg = "error", "acquire child owner lease timeout"
		return
	}
	childBinding.OwnerToken = token

	var renewCtx context.Context
	renewCtx, cancelRenew = context.WithCancel(taskCtx)
	defer cancelRenew()
	renewDone = make(chan struct{})
	if !m.startManagedTask(func() {
		defer close(renewDone)
		ticker := time.NewTicker(embeddedOwnerTTL / 3)
		defer ticker.Stop()
		for {
			select {
			case <-renewCtx.Done():
				return
			case <-ticker.C:
				if err := m.store.RenewEmbeddedOwner(renewCtx, childBinding, ownerID, embeddedOwnerTTL); err != nil {
					if renewCtx.Err() != nil || errors.Is(err, context.Canceled) {
						return
					}
					token, acqErr := m.store.AcquireEmbeddedOwner(renewCtx, childBinding, ownerID, embeddedOwnerTTL)
					if acqErr == nil {
						childBinding.OwnerToken = token
					} else {
						task.mu.Lock()
						if task.stopped {
							task.mu.Unlock()
							return
						}
						task.leaseErr = fmt.Errorf("owner lease renewal failed: %w", err)
						sess := task.activeSession
						task.mu.Unlock()
						cancel()
						if sess != nil {
							sess.Abort()
						}
						return
					}
				}
			}
		}
	}) {
		close(renewDone)
	}

	// 3. Checked SDK persistence
	p := &embeddedPersistence{
		db:              m.store,
		binding:         childBinding,
		digest:          "genesis",
		afterUserAppend: m.embeddedAfterUserAppend,
		publishComplete: m.publishCompleteHistory,
		toolInputs:      make(map[string]any),
	}

	// 4. Fresh confined tools on every run (never reuse closed tools)
	tools, toolsCleanup, err := m.EmbeddedSubagentTools(childAgent.Cwd)
	if err != nil {
		terminalStatus, errorMsg = "error", err.Error()
		return
	}
	defer toolsCleanup()

	model, stream, err := m.resolveSubagentModel(*childAgent)
	if err != nil {
		terminalStatus, errorMsg = "error", err.Error()
		return
	}

	targetWorkflowAgentID := childAgent.AgentID
	activeWorkflowState, activeStateErr := m.store.GetManagedAgentActiveWorkflowState(taskCtx, targetWorkflowAgentID)
	if (activeStateErr != nil || activeWorkflowState == nil) && childAgent.ParentAgentID != "" {
		targetWorkflowAgentID = childAgent.ParentAgentID
		activeWorkflowState, activeStateErr = m.store.GetManagedAgentActiveWorkflowState(taskCtx, targetWorkflowAgentID)
	}
	if activeStateErr == nil && activeWorkflowState != nil && len(activeWorkflowState.AllowedTools) > 0 {
		var filteredTools []agent.AgentTool
		for _, tool := range tools {
			if store.IsToolAllowedInWorkflowState(tool.Name, activeWorkflowState.AllowedTools) {
				filteredTools = append(filteredTools, tool)
			}
		}
		tools = filteredTools
	}

	// 5. Fresh session with current attempt callbacks
	session := coding.NewSession(coding.SessionOptions{
		SessionID:          childBinding.SessionID,
		Model:              model,
		Cwd:                childAgent.Cwd,
		Tools:              tools,
		SystemPrompt:       resolveSubagentSystemPrompt(task.subagentType),
		OrderedPersistence: p,
		StreamFn:           stream,
		BeforeToolCall: func(callCtx context.Context, c agent.BeforeToolCallContext) *agent.BeforeToolCallResult {
			if err := admitWorkflowStateTool(callCtx, m.store, childAgent.AgentID, c.ToolCall.Name); err != nil {
				return &agent.BeforeToolCallResult{Block: true, Reason: err.Error()}
			}
			task.mu.Lock()
			defer task.mu.Unlock()
			if task.maxTurns > 0 && task.turnCount >= task.maxTurns+5 {
				task.hardAborted = true
				if task.activeSession != nil {
					task.activeSession.Abort()
				}
				if task.cancel != nil {
					task.cancel()
				}
				return &agent.BeforeToolCallResult{Block: true, Reason: "Hard turn limit reached"}
			}
			return nil
		},
		BeforeToolExecute: func(callCtx context.Context, call agent.BeforeToolCallContext) error {
			if err := admitWorkflowStateTool(callCtx, m.store, childAgent.AgentID, call.ToolCall.Name); err != nil {
				return &agent.PolicyDenial{Reason: err.Error()}
			}
			return nil
		},
	})

	// 6. Restore prior transcript from store if resuming or recovering
	if err := p.restore(taskCtx, session); err != nil && !errors.Is(err, sql.ErrNoRows) {
		task.mu.Lock()
		isStopped := task.stopped
		task.mu.Unlock()
		status := "error"
		errStr := fmt.Sprintf("restore history: %v", err)
		resText := ""
		if isStopped || taskCtx.Err() != nil {
			status = "stopped"
			resText = "Subagent stopped."
			errStr = ""
		}
		terminalStatus, resultText, errorMsg = status, resText, errStr
		return
	}

	task.mu.Lock()
	task.activeSession = session
	for _, steerMsg := range task.bufferedSteering {
		session.Steer(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: steerMsg}}})
	}
	task.bufferedSteering = nil
	task.mu.Unlock()

	defer func() {
		task.mu.Lock()
		task.activeSession = nil
		task.mu.Unlock()
	}()

	unsubscribe := session.Subscribe(func(eventCtx context.Context, event agent.AgentEvent) error {
		if event.Type == agent.EvTurnEnd {
			task.mu.Lock()
			task.turnCount++
			task.toolUseCount += len(event.ToolResults)
			if asst, ok := event.Message.(ai.AssistantMessage); ok {
				tokens := asst.Usage.TotalTokens
				if tokens == 0 {
					tokens = asst.Usage.Input + asst.Usage.Output
				}
				task.totalTokens += tokens
			} else if asstPtr, ok := event.Message.(*ai.AssistantMessage); ok && asstPtr != nil {
				tokens := asstPtr.Usage.TotalTokens
				if tokens == 0 {
					tokens = asstPtr.Usage.Input + asstPtr.Usage.Output
				}
				task.totalTokens += tokens
			}

			now := m.clock.Now()
			task.durationSeconds = task.baseDuration + now.Sub(task.startTime).Seconds()
			currentProgress := store.ManagedSubagentProgress{
				TurnCount:       task.baseTurns + task.turnCount,
				ToolUseCount:    task.baseToolUses + task.toolUseCount,
				TotalTokens:     task.baseTokens + task.totalTokens,
				DurationSeconds: task.durationSeconds,
			}
			currentTurns := task.turnCount
			maxTurns := task.maxTurns
			task.mu.Unlock()

			// Failclosed progress update: abort if CAS / parent fence fails
			if err := m.store.UpdateManagedSubagentProgress(taskCtx, task.parent, task.childID, task.attempt, currentProgress); err != nil {
				task.mu.Lock()
				task.stopped = true
				task.mu.Unlock()
				session.Abort()
				if task.cancel != nil {
					task.cancel()
				}
				return err
			}

			// Soft limit check: at maxTurns, inject wrap-up steer prompt
			if maxTurns > 0 && currentTurns == maxTurns {
				session.Steer(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "You have reached your turn limit. Wrap up your work immediately. Do not make any new tool calls; provide your final answer to the parent now."}}})
				task.mu.Lock()
				task.wrapUpSteered = true
				task.mu.Unlock()
			}

			// Hard limit check: maxTurns + 5 grace turns. If tools were called, abort before next turn
			if maxTurns > 0 && currentTurns >= maxTurns+5 && len(event.ToolResults) > 0 {
				task.mu.Lock()
				task.hardAborted = true
				task.mu.Unlock()
				session.Abort()
				if task.cancel != nil {
					task.cancel()
				}
				return errors.New("hard turn limit reached")
			}
		}
		return nil
	})
	defer unsubscribe()

	var runErr error
	hasUserOrTool := false
	var lastRole ai.Role
	for _, msg := range session.History() {
		role := msg.MessageRole()
		if role != ai.RoleSystem {
			hasUserOrTool = true
			lastRole = role
		}
	}

	if !hasUserOrTool {
		_, runErr = session.Run(taskCtx, task.prompt)
	} else if lastRole == ai.RoleUser || lastRole == ai.RoleToolResult {
		runErr = session.Continue(taskCtx)
		if runErr != nil && strings.Contains(runErr.Error(), "Cannot continue") {
			_, runErr = session.Run(taskCtx, "Continue working from your saved context.")
		}
	} else {
		prompt := "Continue working from your saved context."
		if task.prompt != "" && task.attempt > 1 && task.turnCount == 0 {
			prompt = task.prompt
		}
		_, runErr = session.Run(taskCtx, prompt)
	}

	task.mu.Lock()
	resultText = session.LastAssistantText()
	if task.leaseErr != nil {
		terminalStatus = "error"
		errorMsg = task.leaseErr.Error()
		resultText = ""
	} else if task.hardAborted {
		terminalStatus = "aborted"
		resultText = "Subagent aborted: exceeded maximum turn limit and grace period without settling."
	} else if task.stopped {
		terminalStatus = "stopped"
		resultText = "Subagent stopped."
		errorMsg = ""
	} else if m.isClosed() || (m.runCtx != nil && m.runCtx.Err() != nil) || errors.Is(runErr, context.Canceled) || (runErr != nil && strings.Contains(runErr.Error(), "context canceled")) {
		// Router is shutting down/restarting. Preserve running state for restart recovery.
		terminalStatus = ""
	} else if taskCtx.Err() != nil {
		terminalStatus = "stopped"
		resultText = "Subagent stopped."
		errorMsg = ""
	} else if runErr != nil {
		terminalStatus = "error"
		errorMsg = runErr.Error()
	} else {
		if task.wrapUpSteered {
			terminalStatus = "steered"
		} else {
			terminalStatus = "completed"
		}
	}
	task.mu.Unlock()
}

// RecoverActiveSubagents queries running/queued subagents from the store and resumes them across router restart.
func (m *Manager) RecoverActiveSubagents(ctx context.Context) error {
	if ctx == nil {
		ctx = m.runCtx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	records, err := m.store.ActiveManagedSubagents(ctx)
	if err != nil {
		return err
	}
	for _, rec := range records {
		parentAgent, err := m.store.ManagedAgent(ctx, rec.ParentAgentID)
		if err != nil || parentAgent == nil || parentAgent.DesiredState != "running" {
			continue
		}
		if parentAgent.SessionEpoch != rec.ParentEpoch {
			continue
		}
		parentBinding := store.EmbeddedBinding{
			AgentID:    rec.ParentAgentID,
			SessionID:  rec.ParentSessionID,
			Epoch:      rec.ParentEpoch,
			Generation: rec.ParentGeneration,
		}
		m.mu.Lock()
		if owner, ok := m.embeddedOwners[rec.ParentAgentID]; ok && owner.binding.Epoch == rec.ParentEpoch {
			parentBinding.OwnerToken = owner.binding.OwnerToken
		}
		m.mu.Unlock()

		task := &subagentTask{
			parent:          parentBinding,
			childID:         rec.AgentID,
			attempt:         rec.Attempt,
			subagentType:    rec.SubagentType,
			description:     rec.Description,
			maxTurns:        rec.MaxTurns,
			prompt:          rec.ResumePrompt,
			status:          "queued",
			baseTurns:       rec.TurnCount,
			baseToolUses:    rec.ToolUseCount,
			baseTokens:      rec.TotalTokens,
			baseDuration:    rec.DurationSeconds,
			durationSeconds: rec.DurationSeconds,
		}

		m.subagentsMu.Lock()
		m.subagentsRegistry[rec.AgentID] = task
		if len(m.subagentsActive) < m.subagentCapacity() {
			task.status = "running"
			m.subagentsActive[rec.AgentID] = task
			m.subagentsMu.Unlock()
			started := m.startManagedTask(func() { m.runSubagent(task) })
			if !started {
				m.subagentsMu.Lock()
				delete(m.subagentsActive, rec.AgentID)
				m.subagentsMu.Unlock()
			}
		} else {
			m.subagentsQueue = append(m.subagentsQueue, task)
			m.subagentsMu.Unlock()
		}
	}

	// Also deliver any completed notifications that were missed during restart
	completed, err := m.store.CompletedSubagentsPendingNotification(ctx)
	if err == nil {
		for _, rec := range completed {
			subject := fmt.Sprintf("[System Notice] Subagent Completed: %s", rec.AgentID)
			if rec.Status != "completed" && rec.Status != "steered" {
				subject = fmt.Sprintf("[System Notice] Subagent %s: %s", strings.Title(rec.Status), rec.AgentID)
			}
			body := FormatSubagentWakePrompt(*rec)
			source := protocol.MessageSource{Kind: "system"}
			msgID := fmt.Sprintf("subagent-completed-%s-%d", rec.AgentID, rec.Attempt)
			now := formatManagedTime(m.clock.Now())
			envelope := protocol.Envelope{
				ID:        msgID,
				From:      "system",
				To:        rec.ParentAgentID,
				Subject:   &subject,
				Body:      body,
				CreatedAt: now,
				Source:    &source,
			}
			result, err := m.store.Enqueue(ctx, envelope, store.EnqueueOptions{
				Conversation: &store.ConversationOptions{
					Body:   &body,
					Source: &source,
				},
			})
			if err == nil && (result.Created || result.ConversationCreated) {
				m.mu.Lock()
				publish := m.embeddedMailPublisher
				m.mu.Unlock()
				if publish != nil {
					publish(result)
				} else {
					_, _ = m.store.ResolveMessageTarget(ctx, result.Message.ID)
				}
				m.OfferDirect(rec.ParentAgentID)
			}
			parentBinding := store.EmbeddedBinding{
				AgentID:    rec.ParentAgentID,
				SessionID:  rec.ParentSessionID,
				Epoch:      rec.ParentEpoch,
				Generation: rec.ParentGeneration,
			}
			m.AsyncDeliverSubagentParentWake(parentBinding, *rec)
		}
	}

	return nil
}

// ReconcileInterruptedSubagents notifies parent workers of previously running subagents
// that were interrupted by a router process restart.
func (m *Manager) ReconcileInterruptedSubagents(ctx context.Context) error {
	if ctx == nil {
		ctx = m.runCtx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	records, err := m.store.InterruptedSubagentsPendingNotification(ctx)
	if err != nil {
		return err
	}
	for _, rec := range records {
		childID := rec.AgentID
		m.recordSubagentCompletionHistory(rec.ParentAgentID, rec.AgentID, rec.Attempt, rec.Description, "failed", "interrupted", "", "Interrupted on restart")
		subject := fmt.Sprintf("[System Notice] Subagent Interrupted: %s", childID)
		body := fmt.Sprintf("Subagent %s (\"%s\") was interrupted due to a router process restart. Partial progress and logs have been saved. You can inspect its results using get_subagent_result(agent_id=\"%s\").\n\nAutomated system notification. No mailbox reply is needed or expected. Proceed directly with your next step.", childID, rec.Description, childID)
		source := protocol.MessageSource{Kind: "system"}
		msgID := fmt.Sprintf("subagent-interrupted-%s-%s", childID, uuid.New().String()[:8])
		now := formatManagedTime(m.clock.Now())
		envelope := protocol.Envelope{
			ID:        msgID,
			From:      "system",
			To:        rec.ParentAgentID,
			Subject:   &subject,
			Body:      body,
			CreatedAt: now,
			Source:    &source,
		}
		result, err := m.store.Enqueue(ctx, envelope, store.EnqueueOptions{
			Conversation: &store.ConversationOptions{
				Body:   &body,
				Source: &source,
			},
		})
		if err != nil {
			log.Printf("STARTUP: subagent interrupt notification notice for %s skipped: %v", childID, err)
			continue
		}
		if result.Created || result.ConversationCreated {
			m.mu.Lock()
			publish := m.embeddedMailPublisher
			m.mu.Unlock()
			if publish != nil {
				publish(result)
			} else {
				_, _ = m.store.ResolveMessageTarget(ctx, result.Message.ID)
			}
			m.OfferDirect(rec.ParentAgentID)
		}
	}
	return nil
}

func (m *Manager) resumeParentGoalIfIdle(parent store.EmbeddedBinding) {
	if parent.AgentID == "" {
		return
	}
	// Check if any subagents remain active for this parent
	m.subagentsMu.Lock()
	hasActive := false
	for _, t := range m.subagentsActive {
		if t.parent.AgentID == parent.AgentID {
			hasActive = true
			break
		}
	}
	if !hasActive {
		for _, t := range m.subagentsQueue {
			if t.parent.AgentID == parent.AgentID {
				hasActive = true
				break
			}
		}
	}
	m.subagentsMu.Unlock()
	if hasActive {
		return
	}

	ctx := m.runCtx
	if ctx == nil {
		ctx = context.Background()
	}

	subagents, err := m.store.ManagedSubagents(ctx, parent)
	if err == nil {
		for _, s := range subagents {
			if s.Status == "queued" || s.Status == "running" {
				return
			}
		}
	}

	parentAgent, err := m.store.ManagedAgent(ctx, parent.AgentID)
	if err != nil || parentAgent == nil || parentAgent.ActivityState != "idle" || parentAgent.LifecycleState != "running" || parentAgent.DesiredState != "running" {
		return
	}

	policy, err := m.store.ManagedGoalPolicyFor(ctx, parent.AgentID)
	if err != nil || policy == nil || !policy.Config.Enabled {
		return
	}

	if key, found, err := m.store.LatestReservedEmbeddedGoalContinuation(ctx, parent); err == nil && found {
		m.startManagedTask(func() {
			m.startEmbeddedGoalContinuation(parent, key)
		})
		return
	}

	m.mu.Lock()
	owner, ok := m.embeddedOwners[parent.AgentID]
	m.mu.Unlock()
	if ok && owner.binding == parent {
		m.startManagedTask(func() {
			runCtx := context.WithValue(m.runCtx, embeddedOwnerContextKey{}, owner)
			prompt := m.formatGoalContinuationPrompt(runCtx, owner.binding, nil)
			_ = m.runEmbeddedSessionTurn(runCtx, *parentAgent, prompt, nil)
		})
	}
}
