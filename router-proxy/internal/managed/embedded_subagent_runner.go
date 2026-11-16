package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
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

const DefaultSubagentInactivityTimeout = 90 * time.Second

type SubagentSpawnRequest struct {
	AgentID      string
	Description  string
	SubagentType string
	MaxTurns     int // 0 = unlimited
	Timeout      time.Duration
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
	timeout      time.Duration
	prompt       string

	mu                sync.Mutex
	status            string
	admissionRejected bool
	bufferedSteering  []string
	cancel            context.CancelFunc
	activeSession     *coding.Session
	activeClaude      *ClaudeCodeProcess
	claude            bool
	resourcesDone     chan struct{}
	retainSlot        bool

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

	lastActivity       time.Time
	lastStage          string
	lastStageTime      time.Time
	inactivityTimedOut bool
}

func (t *subagentTask) touchActivity(now time.Time) {
	t.mu.Lock()
	t.lastActivity = now
	t.mu.Unlock()
}

func (t *subagentTask) updateStage(stage string, now time.Time) {
	t.mu.Lock()
	t.lastActivity = now
	t.lastStage = stage
	t.lastStageTime = now
	t.mu.Unlock()
}

func (t *subagentTask) abortExecution() {
	t.mu.Lock()
	t.stopped = true
	t.hardAborted = true
	if t.cancel != nil {
		t.cancel()
	}
	if t.activeSession != nil {
		t.activeSession.Abort()
	}
	t.mu.Unlock()
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
	return DefaultGlobalSubagentCapacity
}

func (m *Manager) subagentPerWorkerCapacity() int {
	if m.config.Limits.SubagentPerWorkerCapacity > 0 {
		return m.config.Limits.SubagentPerWorkerCapacity
	}
	return DefaultSubagentPerWorkerCapacity
}

func (m *Manager) activeSubagentsForParentLocked(parentID string) int {
	count := 0
	for _, task := range m.subagentsActive {
		if task.parent.AgentID == parentID {
			count++
		}
	}
	return count
}

func (m *Manager) canRunSubagentLocked(parentID string) bool {
	if len(m.subagentsActive) >= m.subagentCapacity() {
		return false
	}
	if m.activeSubagentsForParentLocked(parentID) >= m.subagentPerWorkerCapacity() {
		return false
	}
	return true
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

	if err != nil {
		log.Printf("subagent %s settle error: %v", task.childID, err)
	} else {
		latestRecord, err := m.store.ManagedSubagent(context.Background(), task.parent, task.childID)
		if err == nil && latestRecord != nil {
			m.AsyncDeliverSubagentParentWake(task.parent, *latestRecord)
		}
	}
	m.publishProjection()
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

// TimeoutExpiredSubagents stops and hard-aborts any running subagent whose execution has exceeded its timeout.
func (m *Manager) TimeoutExpiredSubagents(maxDuration time.Duration) {
	if maxDuration <= 0 {
		maxDuration = 1 * time.Hour
	}
	now := m.clock.Now()
	m.subagentsMu.Lock()
	var toAbort []*subagentTask
	for _, task := range m.subagentsActive {
		if task != nil {
			task.mu.Lock()
			started := !task.startTime.IsZero()
			limit := task.timeout
			if limit <= 0 {
				limit = maxDuration
			}
			expired := started && now.Sub(task.startTime) > limit
			inactive := !task.lastActivity.IsZero() && now.Sub(task.lastActivity) >= DefaultSubagentInactivityTimeout
			if !expired && inactive {
				task.inactivityTimedOut = true
			}
			if expired || inactive {
				toAbort = append(toAbort, task)
			}
			task.mu.Unlock()
		}
	}
	m.subagentsMu.Unlock()

	for _, task := range toAbort {
		task.abortExecution()
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
	task.mu.Lock()
	done, retain := task.resourcesDone, task.retainSlot
	task.mu.Unlock()
	if retain {
		return
	}
	if done != nil {
		select {
		case <-done:
		default:
			go func() { <-done; m.releaseSubagentSlot(task) }()
			return
		}
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
		candidateIndex := -1
		for i, cand := range m.subagentsQueue {
			if m.subagentsActive[cand.childID] != nil || !m.canRunSubagentLocked(cand.parent.AgentID) {
				continue
			}
			candidateIndex = i
			break
		}
		if candidateIndex < 0 {
			break
		}

		candidate := m.subagentsQueue[candidateIndex]
		m.subagentsQueue = append(m.subagentsQueue[:candidateIndex], m.subagentsQueue[candidateIndex+1:]...)

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
			candidate.status = "failed"
			candidate.mu.Unlock()
			if current := m.subagentsRegistry[candidate.childID]; current == candidate && current.attempt == candidate.attempt {
				delete(m.subagentsRegistry, candidate.childID)
			}
			go m.finalizeQueuedTask(candidate, "failed", "Subagent admission failed: "+err.Error())
			continue
		}

		if m.subagentAdmissionTestHook != nil {
			m.subagentAdmissionTestHook()
		}
		started := m.startManagedTask(func() { m.runSubagent(candidate) })
		if !started {
			candidate.mu.Lock()
			candidate.admissionRejected = true
			candidate.mu.Unlock()
			delete(m.subagentsActive, candidate.childID)
			go m.finalizeSubagent(candidate, "stopped", "Subagent stopped on router close.", "")
			return
		}
		m.publishProjection()
	}
}

func (m *Manager) resolveSubagentModel(record store.ManagedAgentRecord) (*ai.Model, agent.StreamFn, error) {
	// CLI children use the scoped process path, never the Pi resolver.
	if isCLIProvider(record.SelectedModel.Provider) {
		return nil, nil, errors.New("CLI managed child execution requires the scoped child process bridge")
	}
	if record.SelectedModel.Provider == "" || record.SelectedModel.ID == "" {
		record.SelectedModel.Provider = "google-vertex"
		record.SelectedModel.ID = "gemini-3.8-flash"
	}
	model, err := ResolveModel(record.SelectedModel)
	if err != nil && m.embeddedModelResolver == nil {
		return nil, nil, err
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
				if opts == nil {
					opts = &ai.SimpleStreamOptions{}
				} else {
					copyOpts := *opts
					opts = &copyOpts
				}
				if opts.APIKey == "" {
					opts.APIKey = "none"
				}
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
		if model.Provider == "google" || record.SelectedModel.Provider == "google" {
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
				// Preserve the SDK default for ordinary children; honor an explicitly configured transport.
				if m.codexHTTPTransport != nil {
					opts.HTTPClient = embeddedCodexTerminalDoer{newEmbeddedCodexClient(m.codexHTTPTransport)}
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

		timeout := req.Timeout
		if timeout <= 0 {
			timeout = 10 * time.Minute
		}

		task := &subagentTask{
			parent:          parent,
			childID:         childID,
			attempt:         resumedRecord.Attempt,
			subagentType:    resumedRecord.SubagentType,
			description:     resumedRecord.Description,
			maxTurns:        resumedRecord.MaxTurns,
			timeout:         timeout,
			prompt:          req.Prompt,
			status:          "queued",
			baseTurns:       resumedRecord.TurnCount,
			baseToolUses:    resumedRecord.ToolUseCount,
			baseTokens:      resumedRecord.TotalTokens,
			baseDuration:    resumedRecord.DurationSeconds,
			durationSeconds: resumedRecord.DurationSeconds,
		}

		return m.admitSubagent(ctx, task)

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

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}

	task := &subagentTask{
		parent:       parent,
		childID:      childID,
		attempt:      1,
		subagentType: kind,
		description:  req.Description,
		maxTurns:     req.MaxTurns,
		timeout:      timeout,
		prompt:       req.Prompt,
		status:       "queued",
	}

	return m.admitSubagent(ctx, task)

}

// admitSubagent records the model chosen transactionally by the store before
// attempting admission, including on resume where the model is unchanged.
func (m *Manager) admitSubagent(ctx context.Context, task *subagentTask) (*SubagentSpawnResult, error) {
	child, err := m.store.ManagedAgent(ctx, task.childID)
	if err != nil {
		m.finalizeQueuedTask(task, "failed", err.Error())
		return nil, err
	}
	task.claude = isCLIProvider(child.SelectedModel.Provider)
	m.subagentsMu.Lock()
	if m.runCtx.Err() != nil {
		m.subagentsMu.Unlock()
		m.finalizeQueuedTask(task, "stopped", "Subagent stopped on router close.")
		return nil, errors.New("manager is closing")
	}
	m.subagentsRegistry[task.childID] = task
	m.subagentsQueue = append(m.subagentsQueue, task)
	m.drainQueueLocked()
	task.mu.Lock()
	status := task.status
	rejected := task.admissionRejected
	task.mu.Unlock()
	m.subagentsMu.Unlock()
	m.publishProjection()
	if status == "failed" {
		return nil, errors.New("subagent running transition failed")
	}
	if rejected {
		return nil, errors.New("manager is closing")
	}
	return &SubagentSpawnResult{AgentID: task.childID, Status: status}, nil
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
	if task.activeClaude != nil {
		return errors.New("steering a running Claude child is not supported")
	}

	summary := fmt.Sprintf("Steering from %s", parent.AgentID)
	presentation := &store.ManagedHistoryPresentation{
		Kind:    "message",
		Outcome: "success",
		From:    parent.AgentID,
		To:      childID,
		Summary: summary,
	}
	eventKey := fmt.Sprintf("subagent-steer:%s:%s:%d", childID, parent.AgentID, m.clock.Now().UnixNano())
	msgCopy := message
	parentIdentity := parent.AgentID
	entry, recErr := m.store.RecordManagedCompleteHistoryWithFullDetails(
		ctx,
		childID,
		"managed_message",
		"info",
		summary,
		eventKey,
		presentation,
		nil,
		&msgCopy,
		&parentIdentity,
	)
	if recErr == nil && entry.ID != "" {
		m.publishCompleteHistory(entry)
	} else if recErr != nil {
		log.Printf("failed to record subagent steer message: error=%v child_id=%s", recErr, childID)
	}

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
	// The registry holds the current attempt; an older attempt can still be
	// active while its resources are cleaning up after stop and resume.
	task := m.subagentsRegistry[childID]
	if task == nil {
		task = m.subagentsActive[childID]
	}
	m.subagentsMu.Unlock()

	var desc string
	var attempt int = 1
	targetParentID := parentAgentID

	if task != nil {
		task.mu.Lock()
		task.stopped = true
		task.status = "stopped"
		if task.cancel != nil {
			task.cancel()
		}
		if task.activeSession != nil {
			task.activeSession.Abort()
		}
		desc = task.description
		attempt = task.attempt
		if task.parent.AgentID != "" {
			targetParentID = task.parent.AgentID
		}
		task.mu.Unlock()

		m.subagentsMu.Lock()
		m.removeFromQueueLocked(childID)
		if current := m.subagentsRegistry[childID]; current == task {
			delete(m.subagentsRegistry, childID)
		}
		m.subagentsMu.Unlock()
		if !task.claude {
			m.releaseSubagentSlot(task)
		}
	}

	rec, err := m.store.StopManagedSubagent(ctx, targetParentID, childID)
	if err != nil {
		return err
	}
	if rec != nil {
		if rec.ParentAgentID != "" {
			targetParentID = rec.ParentAgentID
		}
		if desc == "" {
			desc = rec.Description
		}
		if rec.Attempt > 0 {
			attempt = rec.Attempt
		}
	}

	m.recordSubagentCompletionHistory(targetParentID, childID, attempt, desc, "failed", "stopped", "", "Subagent stopped.")
	m.publishProjection()
	subject := fmt.Sprintf("[System Notice] Subagent Terminated: %s", childID)
	body := fmt.Sprintf("Subagent %s (\"%s\") was stopped. You can fetch its latest state and findings using get_subagent_result(agent_id=\"%s\").\n\n---\n*Automated notification from system. Do not reply to this message — messages to 'system' are not accepted.*", childID, desc, childID)
	source := protocol.MessageSource{Kind: "system"}
	msgID := fmt.Sprintf("subagent-terminated-%s-%d", childID, attempt)
	now := formatManagedTime(m.clock.Now())
	envelope := protocol.Envelope{
		ID:        msgID,
		From:      "system",
		To:        targetParentID,
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
	m.OfferDirect(targetParentID)
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

	if (terminalStatus == "failed" || terminalStatus == "timed_out") && task.lastStage != "" {
		var stageDur time.Duration
		if !task.lastStageTime.IsZero() {
			stageDur = now.Sub(task.lastStageTime)
		}
		stageDurSec := int(stageDur.Round(time.Second).Seconds())
		stageDiag := fmt.Sprintf("[%ds in stage: %s]", stageDurSec, task.lastStage)
		if !strings.Contains(errorMsg, task.lastStage) {
			if errorMsg != "" {
				errorMsg = fmt.Sprintf("%s %s", errorMsg, stageDiag)
			} else {
				errorMsg = stageDiag
			}
		}
		if resultText != "" && !strings.Contains(resultText, task.lastStage) {
			resultText = fmt.Sprintf("%s %s", resultText, stageDiag)
		}
	}
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
		log.Printf("subagent %s settle error: %v", task.childID, err)
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
	if terminalStatus != "completed" {
		outcome = "failed"
	}
	desc := strings.TrimSpace(task.description)
	if desc == "" && latestRecord != nil {
		desc = strings.TrimSpace(latestRecord.Description)
	}
	m.recordSubagentCompletionHistory(task.parent.AgentID, task.childID, task.attempt, desc, outcome, terminalStatus, resultText, errorMsg)
	m.publishProjection()

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

func settleClaudeChildOutcome(status, result, diagnostic string, stopped, inactive, timedOut bool) (string, string, string) {
	// Cleanup and uncertainty failures take precedence over cancellation signals.
	if strings.HasPrefix(diagnostic, "Claude process cleanup:") || strings.HasPrefix(diagnostic, "Claude child outcome uncertain:") {
		return status, result, diagnostic
	}
	if inactive {
		return "failed", "Subagent execution timed out due to inactivity.", "subagent execution timed out due to inactivity"
	}
	if timedOut {
		return "failed", "Subagent execution timed out.", "subagent execution timed out"
	}
	if stopped {
		return "stopped", "Subagent stopped.", ""
	}
	return status, result, diagnostic
}

func (m *Manager) runSubagent(task *subagentTask) {
	defer m.resumeParentGoalIfIdle(task.parent)
	defer m.releaseSubagentSlot(task)

	timeout := task.timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	taskCtx, cancel := context.WithTimeout(m.runCtx, timeout)
	task.mu.Lock()
	task.cancel = cancel
	task.startTime = m.clock.Now()
	task.lastActivity = m.clock.Now()
	stoppedBeforeStart := task.stopped
	task.mu.Unlock()
	defer cancel()
	if stoppedBeforeStart {
		return
	}

	var terminalStatus, resultText, errorMsg string
	var childBinding store.EmbeddedBinding

	defer func() {
		if m.isClosed() && !task.stopped {
			return
		}
		if terminalStatus != "" {
			m.finalizeSubagent(task, terminalStatus, resultText, errorMsg)
		}
	}()

	childAgent, err := m.store.ManagedAgent(m.runCtx, task.childID)
	if err != nil {
		terminalStatus, errorMsg = "failed", err.Error()
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
			terminalStatus, errorMsg = "failed", fmt.Sprintf("bind child session: %v", err)
			return
		}
	}

	if isCLIProvider(childAgent.SelectedModel.Provider) {
		terminalStatus, resultText, errorMsg = m.runClaudeSubagent(taskCtx, task, childBinding, *childAgent)
		task.mu.Lock()
		terminalStatus, resultText, errorMsg = settleClaudeChildOutcome(terminalStatus, resultText, errorMsg, task.stopped, task.inactivityTimedOut, taskCtx.Err() == context.DeadlineExceeded)
		task.mu.Unlock()
		return
	}

	// Each attempt gets a fresh, fenced child identity and its own job capability.
	childIdentity, err := newEmbeddedOwnerIdentity()
	if err != nil {
		terminalStatus, errorMsg = "failed", err.Error()
		return
	}
	token, err := m.store.AcquireEmbeddedOwner(taskCtx, childBinding, childIdentity, embeddedOwnerTTL)
	if err != nil {
		terminalStatus, errorMsg = "failed", err.Error()
		return
	}
	childBinding.OwnerToken = token
	childOwner := embeddedOwner{binding: childBinding, identity: childIdentity, backgroundBash: &backgroundBashOwnerCapability{}}
	m.mu.Lock()
	if m.embeddedOwners == nil {
		m.embeddedOwners = make(map[string]embeddedOwner)
	}
	m.embeddedOwners[task.childID] = childOwner
	m.mu.Unlock()
	defer func() {
		if err := m.retireBackgroundBashOwner(childOwner); err != nil {
			log.Printf("subagent %s background bash cleanup: %v", task.childID, err)
		}
		m.mu.Lock()
		if current, ok := m.embeddedOwners[task.childID]; ok && current.identity == childOwner.identity {
			delete(m.embeddedOwners, task.childID)
		}
		m.mu.Unlock()
	}()
	checkChild := func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := taskCtx.Err(); err != nil {
			return err
		}
		if err := m.store.RenewEmbeddedOwner(ctx, childBinding, childIdentity, embeddedOwnerTTL); err != nil {
			return err
		}
		rec, err := m.store.ManagedSubagent(ctx, task.parent, task.childID)
		if err != nil {
			return err
		}
		if rec.Attempt != task.attempt || rec.Status != "running" {
			return errors.New("stale child attempt")
		}
		task.mu.Lock()
		stopped := task.stopped
		task.mu.Unlock()
		if stopped {
			return errors.New("child stopped")
		}
		return nil
	}

	// 2. Checked SDK persistence
	p := &embeddedPersistence{
		db:                 m.store,
		binding:            childBinding,
		digest:             "genesis",
		afterUserAppend:    m.embeddedAfterUserAppend,
		publishComplete:    m.publishCompleteHistory,
		toolInputs:         make(map[string]any),
		toolCompanionTexts: make(map[string]string),
	}

	// 3. Fresh confined tools on every run (never reuse closed tools)
	tools, toolsCleanup, err := m.EmbeddedSubagentTools(childAgent.Cwd)
	if err != nil {
		terminalStatus, errorMsg = "failed", err.Error()
		return
	}
	defer toolsCleanup()
	m.ensureBackgroundBash()
	for i := range tools {
		if tools[i].Name == "bash" {
			tools[i] = embeddedSubagentBashTool(childAgent.Cwd, func(ctx context.Context, cwd, command string, timeout time.Duration) (agent.AgentToolResult, error) {
				if err := checkChild(ctx); err != nil {
					return agent.AgentToolResult{}, err
				}
				if backgroundBashPlatformSupport() != nil {
					return executeSubagentBash(ctx, cwd, command, timeout)
				}
				return m.executePromotableBash(ctx, childOwner, cwd, embeddedBashCommand{command: command, timeout: timeout})
			})
		}
	}
	tools = append(tools, m.backgroundBashToolsForOwner(childOwner, childAgent.Cwd, task.parent.AgentID, checkChild)...)

	model, stream, err := m.resolveSubagentModel(*childAgent)
	if err != nil {
		terminalStatus, errorMsg = "failed", err.Error()
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
			task.touchActivity(m.clock.Now())
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
			now := m.clock.Now()
			task.updateStage("executing_tool:"+call.ToolCall.Name, now)
			if err := admitWorkflowStateTool(callCtx, m.store, childAgent.AgentID, call.ToolCall.Name); err != nil {
				return &agent.PolicyDenial{Reason: err.Error()}
			}
			toolName := call.ToolCall.Name
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
				if p != nil {
					p.toolExecuted = true
					if p.toolInputs == nil {
						p.toolInputs = make(map[string]any)
					}
					p.toolInputs[toolCallID] = call.ToolCall.Arguments
				}
				invocationKey := fmt.Sprintf("tool:%s:%s", childBinding.AgentID, toolCallID)
				entry, err := m.store.RecordManagedCompleteHistoryWithPresentationAndInput(callCtx, childBinding.AgentID, "managed_tool", "info", "Running "+toolName, invocationKey, &store.ManagedHistoryPresentation{
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
			return nil
		},
	})

	// 5. Restore prior transcript from store if resuming or recovering
	if err := p.restore(taskCtx, session); err != nil && !errors.Is(err, sql.ErrNoRows) {
		task.mu.Lock()
		isStopped := task.stopped
		timedOut := task.inactivityTimedOut
		task.mu.Unlock()
		status := "failed"
		errStr := fmt.Sprintf("restore history: %v", err)
		resText := ""
		if timedOut {
			status = "failed"
			stage := task.lastStage
			if stage == "" {
				stage = "initialized"
			}
			var stageDur time.Duration
			if !task.lastStageTime.IsZero() {
				stageDur = m.clock.Now().Sub(task.lastStageTime)
			}
			stageDurSec := int(stageDur.Round(time.Second).Seconds())
			errStr = fmt.Sprintf("subagent execution timed out due to inactivity (no activity for %s, %ds in stage: %s)", DefaultSubagentInactivityTimeout, stageDurSec, stage)
			resText = fmt.Sprintf("Subagent execution timed out due to inactivity (no activity for %s, %ds in stage: %s).", DefaultSubagentInactivityTimeout, stageDurSec, stage)
		} else if isStopped || taskCtx.Err() != nil {
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
		now := m.clock.Now()
		switch event.Type {
		case agent.EvTurnStart, agent.EvMessageStart:
			task.updateStage("waiting_for_model", now)
		case agent.EvMessageUpdate:
			task.updateStage("streaming_model_response", now)
		case agent.EvTurnEnd:
			task.updateStage("turn_completed", now)
		default:
			task.touchActivity(now)
		}
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

			// Progress telemetry: log and continue on error rather than aborting active task
			if err := m.store.UpdateManagedSubagentProgress(taskCtx, task.parent, task.childID, task.attempt, currentProgress); err != nil {
				log.Printf("subagent %s progress telemetry update failed: %v", task.childID, err)
			}

			// Soft limit check: at maxTurns, inject wrap-up steer prompt
			if maxTurns > 0 && currentTurns == maxTurns {
				session.Steer(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "You have reached your turn limit. Wrap up your work immediately. Do not make any new tool calls; provide your final answer to the parent now."}}})
				task.mu.Lock()
				task.wrapUpSteered = true
				task.mu.Unlock()
			}

			// Hard limit check: maxTurns + 5 grace turns. Abort regardless of whether tools were called.
			if maxTurns > 0 && currentTurns >= maxTurns+5 {
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

	hasGoalTool := false
	for _, t := range tools {
		if t.Name == "goal" {
			hasGoalTool = true
			break
		}
	}

	initialPrompt := task.prompt
	if hasGoalTool && initialPrompt != "" && !strings.Contains(strings.ToLower(initialPrompt), "goal tool") && !strings.Contains(initialPrompt, "action='set'") {
		initialPrompt = initialPrompt + "\n\nCRITICAL: You have the `goal` tool available. As your very first action, use the `goal` tool (with action='set') to set a clear, actionable goal for completing this task."
	}

	watchCtx, cancelWatch := context.WithCancel(taskCtx)
	defer cancelWatch()
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-ticker.C:
				task.mu.Lock()
				last := task.lastActivity
				timedOut := !last.IsZero() && m.clock.Now().Sub(last) >= DefaultSubagentInactivityTimeout
				if timedOut && !task.stopped && !task.inactivityTimedOut {
					task.inactivityTimedOut = true
					task.mu.Unlock()
					task.abortExecution()
					return
				}
				task.mu.Unlock()
			}
		}
	}()

	if !hasUserOrTool {
		_, runErr = session.Run(taskCtx, initialPrompt)
	} else if lastRole == ai.RoleUser || lastRole == ai.RoleToolResult {
		runErr = session.Continue(taskCtx)
		if runErr != nil && strings.Contains(runErr.Error(), "Cannot continue") {
			_, runErr = session.Run(taskCtx, "Continue working from your saved context.")
		}
	} else {
		prompt := "Continue working from your saved context."
		if task.prompt != "" && task.attempt > 1 && task.turnCount == 0 {
			prompt = initialPrompt
		}
		_, runErr = session.Run(taskCtx, prompt)
	}

	task.mu.Lock()
	resultText = session.LastAssistantText()
	inactivityTimedOut := task.inactivityTimedOut
	if inactivityTimedOut {
		terminalStatus = "failed"
		stage := task.lastStage
		if stage == "" {
			stage = "initialized"
		}
		var stageDur time.Duration
		if !task.lastStageTime.IsZero() {
			stageDur = m.clock.Now().Sub(task.lastStageTime)
		}
		stageDurSec := int(stageDur.Round(time.Second).Seconds())
		errorMsg = fmt.Sprintf("subagent execution timed out due to inactivity (no activity for %s, %ds in stage: %s)", DefaultSubagentInactivityTimeout, stageDurSec, stage)
		resultText = fmt.Sprintf("Subagent execution timed out due to inactivity (no activity for %s, %ds in stage: %s).", DefaultSubagentInactivityTimeout, stageDurSec, stage)
	} else if task.hardAborted {
		terminalStatus = "failed"
		errorMsg = "hard turn limit reached: exceeded maximum turn limit and grace period without settling"
		resultText = "Subagent aborted: exceeded maximum turn limit and grace period without settling."
	} else if taskCtx.Err() == context.DeadlineExceeded || errors.Is(taskCtx.Err(), context.DeadlineExceeded) || errors.Is(runErr, context.DeadlineExceeded) {
		terminalStatus = "failed"
		errorMsg = fmt.Sprintf("subagent execution timed out (exceeded %v)", timeout)
		resultText = fmt.Sprintf("Subagent execution timed out (exceeded %v). If necessary, you can specify a higher timeout, but it is strongly preferred to break the task into smaller batches of work.", timeout)
	} else if task.stopped {
		terminalStatus = "stopped"
		resultText = "Subagent stopped."
		errorMsg = ""
	} else if m.isClosed() || (m.runCtx != nil && m.runCtx.Err() != nil) || errors.Is(runErr, context.Canceled) || (runErr != nil && strings.Contains(runErr.Error(), "context canceled")) {
		// Router is shutting down/restarting.
		terminalStatus = "failed"
		errorMsg = "interrupted: router shutdown"
	} else if taskCtx.Err() != nil {
		terminalStatus = "stopped"
		resultText = "Subagent stopped."
		errorMsg = ""
	} else if runErr != nil {
		terminalStatus = "failed"
		errorMsg = runErr.Error()
	} else {
		terminalStatus = "completed"
	}
	task.mu.Unlock()
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
	if err := m.store.MarkInFlightSubagentsFailed(ctx); err != nil {
		log.Printf("STARTUP: mark in-flight subagents failed: %v", err)
	}
	records, err := m.store.InterruptedSubagentsPendingNotification(ctx)
	if err != nil {
		return err
	}
	for _, rec := range records {
		childID := rec.AgentID
		m.recordSubagentCompletionHistory(rec.ParentAgentID, rec.AgentID, rec.Attempt, rec.Description, "failed", "failed", "", "Interrupted on restart")
		subject := fmt.Sprintf("[System Notice] Subagent Interrupted: %s", childID)
		body := fmt.Sprintf("Subagent %s (\"%s\") was interrupted due to a router process restart. Partial progress and logs have been saved. You can inspect its results using get_subagent_result(agent_id=\"%s\").\n\n---\n*Automated notification from system. Do not reply to this message — messages to 'system' are not accepted.*", childID, rec.Description, childID)
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

	m.startManagedTask(func() {
		m.kickoffIdleGoalContinuation(parent)
	})
}

type claudeChildAnswer struct{ current bool }

func (a *claudeChildAnswer) assistant(final bool) { a.current = final }
func (a *claudeChildAnswer) complete() error {
	if !a.current {
		return errors.New("Claude child success without current final assistant response")
	}
	return nil
}
func quarantineClaudeChildOutcome(status, result, diagnostic string, err error) (string, string, string) {
	if err != nil {
		return "failed", "", fmt.Sprintf("Claude process cleanup: %v", err)
	}
	return status, result, diagnostic
}

func claudeChildAllowedTool(raw string) bool {
	if strings.HasPrefix(raw, "mcp__mecha__") {
		return true
	}
	switch raw {
	case "Bash", "Read", "Write", "Edit", "Glob", "Grep", "ToolSearch":
		return true
	default:
		return false
	}
}

// claudeChildTurn joins stdout producers to MCP requests. All transcript writes
// and tool execution are serialized; a request alone never grants authority.
type claudeChildTurn struct {
	mu        sync.Mutex
	serial    sync.Mutex
	changed   chan struct{}
	calls     map[string]*claudeChildCall
	closed    bool
	uncertain bool
}
type claudeChildCall struct {
	bridge   bool
	call     ai.ToolCall
	original json.RawMessage
	result   *ai.ToolResultMessage
	started  bool
}

func (t *claudeChildTurn) signal() { close(t.changed); t.changed = make(chan struct{}) }
func (t *claudeChildTurn) close() {
	t.mu.Lock()
	if !t.closed {
		t.closed = true
		t.signal()
	}
	t.mu.Unlock()
}

// lock polls a contended mutex without spawning a goroutine that could later
// acquire it after cancellation. Every caller rechecks its own fences inside.
func (t *claudeChildTurn) lock(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if t.serial.TryLock() {
			if err := ctx.Err(); err != nil {
				t.serial.Unlock()
				return err
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Stop the process before waiting on any tool; an uncooperative tool may never return.
func cleanupClaudeChildTurn(turn *claudeChildTurn, closeProcess func() error) error {
	turn.close()
	return closeProcess()
}

func (m *Manager) fenceClaudeChildConfig(cfg *ClaudeCodeProcessConfig) {
	cfg.OnUnconfirmedStop = m.claudeCodeProcesses.fenceStartup
	cfg.releaseGate = m.claudeCodeProcesses.releaseGate
}

func (m *Manager) fenceClaudeChildTaskConfig(cfg *ClaudeCodeProcessConfig, task *subagentTask) {
	cfg.releaseGate = m.claudeCodeProcesses.workerGate(task.childID)
	cfg.OnUnconfirmedStop = func(cause error) {
		task.mu.Lock()
		task.retainSlot = true
		task.mu.Unlock()
		m.claudeCodeProcesses.fenceWorker(task.childID, cause)
	}
}

func (m *Manager) fenceClaudeChildToolError(task *subagentTask, err error) bool {
	if !errors.Is(err, errClaudeCodeGroupUnconfirmed) {
		return false
	}
	task.mu.Lock()
	task.retainSlot = true
	task.mu.Unlock()
	m.claudeCodeProcesses.fenceWorker(task.childID, err)
	return true
}

func (m *Manager) runClaudeSubagent(ctx context.Context, task *subagentTask, binding store.EmbeddedBinding, child store.ManagedAgentRecord) (status, result, diagnostic string) {
	release, err := m.claudeCodeProcesses.admitWorker(task.childID)
	if err != nil {
		return "failed", "", err.Error()
	}
	defer release()
	owner, err := newEmbeddedOwnerIdentity()
	if err != nil {
		return "failed", "", err.Error()
	}
	token, err := m.store.AcquireEmbeddedOwner(ctx, binding, owner, embeddedOwnerTTL)
	if err != nil {
		return "failed", "", err.Error()
	}
	binding.OwnerToken = token
	childOwner := embeddedOwner{binding: binding, identity: owner, backgroundBash: &backgroundBashOwnerCapability{}, claudeOrphanRoot: m.config.SessionRoot}
	m.mu.Lock()
	if m.embeddedOwners == nil {
		m.embeddedOwners = make(map[string]embeddedOwner)
	}
	m.embeddedOwners[task.childID] = childOwner
	m.mu.Unlock()
	defer func() {
		if err := m.retireBackgroundBashOwner(childOwner); err != nil {
			log.Printf("subagent %s background bash cleanup: %v", task.childID, err)
		}
		m.mu.Lock()
		if current, ok := m.embeddedOwners[task.childID]; ok && current.identity == childOwner.identity {
			delete(m.embeddedOwners, task.childID)
		}
		m.mu.Unlock()
	}()
	p := &embeddedPersistence{db: m.store, binding: binding, digest: "genesis", publishComplete: m.publishCompleteHistory, toolInputs: map[string]any{}, toolCompanionTexts: map[string]string{}}
	snap, err := m.store.ReopenEmbedded(ctx, binding)
	if err != nil {
		return "failed", "", err.Error()
	}
	for _, entry := range snap.Entries {
		p.seq, p.digest = entry.Sequence, entry.Digest
	}
	turn := &claudeChildTurn{changed: make(chan struct{}), calls: make(map[string]*claudeChildCall)}
	defer turn.close()
	incarnation := claudeCodeNewInputID()
	check := func(ctx context.Context) error {
		if err := m.store.RenewEmbeddedOwner(ctx, binding, owner, embeddedOwnerTTL); err != nil {
			return err
		}
		rec, err := m.store.ManagedSubagent(ctx, task.parent, task.childID)
		if err != nil {
			return err
		}
		if rec.Attempt != task.attempt || rec.Status != "running" {
			return errors.New("stale child attempt")
		}
		task.mu.Lock()
		stopped := task.stopped
		task.mu.Unlock()
		if stopped {
			return errors.New("child stopped")
		}
		return ctx.Err()
	}
	checked := func(callCtx context.Context, req ClaudeCodeMCPCall, tool agent.AgentTool, args map[string]any, recheck func(context.Context) error) (json.RawMessage, error) {
		wait, cancel := context.WithTimeout(callCtx, 5*time.Second)
		defer cancel()
		for {
			turn.mu.Lock()
			item := turn.calls[req.ToolUseID]
			closed := turn.closed
			changed := turn.changed
			turn.mu.Unlock()
			if closed {
				return nil, errors.New("child turn closed")
			}
			if item != nil {
				break
			}
			select {
			case <-wait.Done():
				return nil, errors.New("no checked child tool producer")
			case <-changed:
			}
		}
		if err := turn.lock(callCtx); err != nil {
			return nil, err
		}
		defer turn.serial.Unlock()
		turn.mu.Lock()
		item := turn.calls[req.ToolUseID]
		closed := turn.closed
		turn.mu.Unlock()
		if closed || item == nil {
			return nil, errors.New("child turn closed")
		}
		if !item.bridge || item.call.Name != req.Name || !claudeCodeJSONEqual(item.original, req.Arguments) {
			return nil, errors.New("child tool ID argument conflict")
		}
		if item.result != nil {
			return claudeCodeMCPResult(*item.result)
		}
		if item.started {
			return nil, errors.New("child tool outcome UNKNOWN")
		}
		if err := check(callCtx); err != nil {
			return nil, err
		}
		if err := recheck(callCtx); err != nil {
			return nil, err
		}
		task.mu.Lock()
		over := task.maxTurns > 0 && task.turnCount >= task.maxTurns+5
		task.mu.Unlock()
		if over {
			return nil, errors.New("hard turn limit reached")
		}
		item.started = true
		task.updateStage("executing_tool:"+tool.Name, m.clock.Now())
		tool = capacityFixtureWrap(tool, req.ToolUseID)
		toolResult, execErr := tool.Execute(callCtx, req.ToolUseID, args, nil)
		if m.fenceClaudeChildToolError(task, execErr) {
			turn.uncertain = true
			return nil, fmt.Errorf("child tool outcome UNKNOWN: %w", execErr)
		}
		msg := ai.ToolResultMessage{ToolCallID: req.ToolUseID, ToolName: tool.Name, Timestamp: time.Now().UnixMilli(), Content: toolResult.Content, Details: toolResult.Details}
		if execErr != nil {
			msg.IsError = true
			msg.Content = ai.ContentList{ai.TextContent{Text: execErr.Error()}}
		}
		if msg.Content == nil {
			msg.Content = ai.ContentList{}
		}
		if err := check(callCtx); err != nil {
			return nil, fmt.Errorf("child tool outcome UNKNOWN: %w", err)
		}
		if err := p.AppendCheckedMessage(callCtx, msg); err != nil {
			turn.uncertain = true
			return nil, fmt.Errorf("child tool outcome UNKNOWN: %w", err)
		}
		item.result = &msg
		task.mu.Lock()
		task.toolUseCount++
		task.mu.Unlock()
		return claudeCodeMCPResult(msg)
	}
	bridge, cleanup, err := newClaudeChildBridge(m.store, task.parent, binding, task.attempt, owner, incarnation, child.Cwd, m.config.SessionRoot, checked)
	if err != nil {
		return "failed", "", err.Error()
	}
	// This is the managed MCP bash, not Claude's native Bash tool.
	managedBash := embeddedSubagentBashTool(child.Cwd, func(callCtx context.Context, cwd, command string, timeout time.Duration) (agent.AgentToolResult, error) {
		if err := check(callCtx); err != nil {
			return agent.AgentToolResult{}, err
		}
		if backgroundBashPlatformSupport() != nil {
			return executeSubagentBashOwned(callCtx, cwd, command, timeout, m.config.SessionRoot)
		}
		return m.executePromotableBash(callCtx, childOwner, cwd, embeddedBashCommand{command: command, timeout: timeout})
	})
	if err := bridge.replaceTool(managedBash); err != nil {
		cleanup()
		return "failed", "", err.Error()
	}
	childTools := m.backgroundBashToolsForOwner(childOwner, child.Cwd, task.parent.AgentID, check)
	if err := bridge.addTools(childTools); err != nil {
		cleanup()
		return "failed", "", err.Error()
	}
	task.mu.Lock()
	task.resourcesDone = make(chan struct{})
	resourcesDone := task.resourcesDone
	task.mu.Unlock()
	// A tool can ignore cancellation. Cleanup of its resources is deferred until
	// it releases serialization, without holding up process reaping or settlement.
	defer func() {
		go func() { turn.serial.Lock(); defer turn.serial.Unlock(); defer close(resourcesDone); cleanup() }()
	}()
	url, err := m.ClaudeCodeMCPURL()
	if err != nil {
		return "failed", "", err.Error()
	}
	secret, revoke, err := m.ClaudeCodeMCP().Issue(ctx, bridge.grant())
	if err != nil {
		return "failed", "", err.Error()
	}
	defer revoke()
	dir := filepath.Join(m.config.SessionRoot, "claude-processes")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "failed", "", err.Error()
	}
	f, err := os.CreateTemp(dir, "child-mcp-*.json")
	if err != nil {
		return "failed", "", err.Error()
	}
	path := f.Name()
	defer os.Remove(path)
	err = f.Chmod(0600)
	if err == nil {
		err = json.NewEncoder(f).Encode(map[string]any{"mcpServers": map[string]any{"mecha": map[string]any{"type": "http", "url": url, "headers": map[string]string{"Authorization": "Bearer " + secret}}}})
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "failed", "", err.Error()
	}
	promptPath, err := writeClaudeCodePrompt(m.config.SessionRoot, child.AgentID, resolveSubagentSystemPrompt(task.subagentType))
	if err != nil {
		return "failed", "", err.Error()
	}
	defer os.Remove(promptPath)
	started, err := m.store.ClaudeBindingStarted(ctx, binding)
	if err != nil {
		return "failed", "", err.Error()
	}
	if started {
		return "failed", "", "Claude child already started; automatic resume is unavailable and prior effects may have occurred"
	}
	cfg := ClaudeCodeProcessConfig{Cwd: child.Cwd, SessionID: claudeCodeSessionID(binding.SessionID), Model: child.SelectedModel.ID, ChildAuthority: true, MCPConfigFile: path, SystemPromptFile: promptPath, OrphanRoot: m.config.SessionRoot}
	if child.SelectedModel.Provider == antigravityProvider {
		cfg.Adapter = NewAntigravityAdapter("", nil)
	}
	m.fenceClaudeChildTaskConfig(&cfg, task)
	if err := check(ctx); err != nil {
		return "failed", "", err.Error()
	}
	proc, err := StartClaudeCodeProcess(ctx, cfg)
	if err != nil {
		return "failed", "", err.Error()
	}
	defer func() {
		revoke()
		if err := cleanupClaudeChildTurn(turn, proc.Close); err != nil {
			task.mu.Lock()
			task.retainSlot = true
			task.mu.Unlock()
			status, result, diagnostic = quarantineClaudeChildOutcome(status, result, diagnostic, err)
		} else if !turn.serial.TryLock() {
			status = "failed"
			diagnostic = "Claude child outcome uncertain: tool still running"
			result = ""
		} else {
			var err error
			if turn.uncertain {
				err = errors.New("checked result handoff uncertain")
			}
			turn.serial.Unlock()
			if err != nil {
				status = "failed"
				diagnostic = fmt.Sprintf("Claude child outcome uncertain: %v", err)
				result = ""
			}
		}
	}()
	task.mu.Lock()
	task.activeClaude = proc
	stopped := task.stopped
	steering := append([]string(nil), task.bufferedSteering...)
	task.bufferedSteering = nil
	task.mu.Unlock()
	defer func() { task.mu.Lock(); task.activeClaude = nil; task.mu.Unlock() }()
	if stopped {
		return "stopped", "Subagent stopped.", ""
	}
	text := task.prompt
	if len(steering) > 0 {
		text += "\n\nSteering from parent:\n" + strings.Join(steering, "\n")
	}
	raw, _ := json.Marshal(text)
	id := claudeCodeNewInputID()
	if err := check(ctx); err != nil {
		return "failed", "", err.Error()
	}
	if _, err := proc.Control(ctx, map[string]interface{}{"subtype": "interrupt"}); err != nil {
		return "failed", "", fmt.Sprintf("Claude child pre-submit probe: %v", err)
	}
	if err := m.store.MarkClaudeBindingStarted(ctx, binding); err != nil {
		return "failed", "", err.Error()
	}
	if err := proc.SendUser(ctx, id, raw); err != nil {
		return "failed", "", err.Error()
	}
	user := ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: text}}, Timestamp: time.Now().UnixMilli()}
	parser := &claudeCodeRuntime{config: cfg, tools: make(map[string]string), rawTools: make(map[string]string)}
	admitted := false
	var steeringID string
	var steeringUser ai.UserMessage
	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-ticker.C:
				task.mu.Lock()
				expired := !task.lastActivity.IsZero() && m.clock.Now().Sub(task.lastActivity) >= DefaultSubagentInactivityTimeout
				if expired {
					task.inactivityTimedOut = true
				}
				task.mu.Unlock()
				if expired {
					task.abortExecution()
					return
				}
			}
		}
	}()
	var runErr error
	var answer claudeChildAnswer
	var answerID string
	for {
		event, e := proc.Next(ctx)
		if e != nil {
			runErr = e
			break
		}
		task.touchActivity(m.clock.Now())
		if event.ParentToolUseID != nil {
			continue
		}
		if err := check(ctx); err != nil {
			runErr = err
			break
		}
		switch event.Type {
		case "user":
			answer.assistant(false)
			answerID = ""
			result = ""
			if event.IsReplay && steeringID != "" && event.UUID == steeringID {
				if e = turn.lock(ctx); e != nil {
					runErr = e
					break
				}
				e = p.AppendCheckedMessage(ctx, steeringUser)
				turn.serial.Unlock()
				if e != nil {
					runErr = e
					break
				}
				steeringID = ""
				continue
			}
			if event.IsReplay && event.UUID == id && !admitted {
				if e = turn.lock(ctx); e != nil {
					runErr = e
					break
				}
				e = p.AppendCheckedMessage(ctx, user)
				turn.serial.Unlock()
				if e != nil {
					runErr = e
					break
				}
				admitted = true
				continue
			}
			if !admitted {
				runErr = errors.New("Claude child output before input echo")
				break
			}
			results, e := parser.toolResults(event)
			if e != nil {
				runErr = e
				break
			}
			if e = turn.lock(ctx); e != nil {
				runErr = e
				break
			}
			for _, got := range results {
				item := turn.calls[got.ToolCallID]
				if item == nil {
					runErr = errors.New("Claude child tool result has no producer")
					break
				}
				if item.bridge {
					if item.result == nil || item.result.ToolName != got.ToolName || item.result.IsError != got.IsError || claudeCodeMessageText(*item.result) != claudeCodeMessageText(got) {
						runErr = errors.New("Claude child tool result differs from checked result")
						break
					}
				} else {
					if e = p.AppendCheckedMessage(ctx, got); e != nil {
						runErr = e
						break
					}
					task.mu.Lock()
					task.toolUseCount++
					task.mu.Unlock()
				}
				turn.mu.Lock()
				delete(turn.calls, got.ToolCallID)
				turn.mu.Unlock()
				delete(parser.rawTools, got.ToolCallID)
				delete(parser.tools, got.ToolCallID)
			}
			turn.serial.Unlock()
		case "assistant":
			if !admitted {
				runErr = errors.New("Claude child assistant before input echo")
				break
			}
			msg, messageID, e := parser.assistant(event)
			if e != nil {
				runErr = e
				break
			}
			for _, block := range msg.Content {
				if call, ok := block.(ai.ToolCall); ok && !claudeChildAllowedTool(parser.rawTools[call.ID]) {
					runErr = errors.New("Claude child used disallowed tool")
					break
				}
			}
			if runErr != nil {
				break
			}
			msg.StopReason = ai.StopStop
			for _, block := range msg.Content {
				if _, ok := block.(ai.ToolCall); ok {
					msg.StopReason = ai.StopToolUse
				}
			}
			if e = turn.lock(ctx); e != nil {
				runErr = e
				break
			}
			e = p.AppendCheckedMessage(ctx, *msg)
			if e == nil {
				turn.mu.Lock()
				for _, block := range msg.Content {
					if call, ok := block.(ai.ToolCall); ok {
						if turn.calls[call.ID] != nil || len(turn.calls) >= 128 {
							e = errors.New("duplicate or excessive Claude child tool calls")
							break
						}
						original, _ := json.Marshal(call.Arguments)
						turn.calls[call.ID] = &claudeChildCall{bridge: strings.HasPrefix(parser.rawTools[call.ID], "mcp__mecha__"), call: call, original: original}
					}
				}
				turn.signal()
				turn.mu.Unlock()
			}
			turn.serial.Unlock()
			if e != nil {
				runErr = e
				break
			}
			task.mu.Lock()
			task.turnCount++
			task.totalTokens += msg.Usage.TotalTokens
			progress := store.ManagedSubagentProgress{TurnCount: task.baseTurns + task.turnCount, ToolUseCount: task.baseToolUses + task.toolUseCount, TotalTokens: task.baseTokens + task.totalTokens, DurationSeconds: task.baseDuration + m.clock.Now().Sub(task.startTime).Seconds()}
			hard := task.maxTurns > 0 && task.turnCount >= task.maxTurns+5
			soft := task.maxTurns > 0 && task.turnCount == task.maxTurns && !task.wrapUpSteered
			task.mu.Unlock()
			_ = m.store.UpdateManagedSubagentProgress(ctx, task.parent, task.childID, task.attempt, progress)
			if hard {
				task.mu.Lock()
				task.hardAborted = true
				task.mu.Unlock()
				runErr = errors.New("hard turn limit reached")
				break
			}
			if soft && msg.StopReason == ai.StopToolUse {
				text := "You have reached your turn limit. Wrap up your work immediately. Do not make any new tool calls; provide your final answer to the parent now."
				steeringID = claudeCodeNewInputID()
				steeringUser = ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: text}}, Timestamp: time.Now().UnixMilli()}
				raw, _ := json.Marshal(text)
				if e := proc.SendUser(ctx, steeringID, raw); e != nil {
					runErr = e
					break
				}
				task.mu.Lock()
				task.wrapUpSteered = true
				task.mu.Unlock()
			}
			answer.assistant(msg.StopReason == ai.StopStop)
			if msg.StopReason == ai.StopStop {
				if answerID != messageID {
					result = ""
				}
				result += claudeCodeMessageText(*msg)
				answerID = messageID
			} else {
				result = ""
				answerID = ""
			}
		case "result":
			if !admitted {
				runErr = errors.New("Claude child result before input echo")
				break
			}
			if event.Subtype != "success" {
				var outcome struct {
					Result string `json:"result"`
				}
				_ = json.Unmarshal(event.Raw, &outcome)
				runErr = fmt.Errorf("Claude child failed: %s", outcome.Result)
				break
			}
			if e = answer.complete(); e != nil {
				runErr = e
				break
			}
			if e = turn.lock(ctx); e != nil {
				runErr = e
				break
			}
			if len(turn.calls) != 0 {
				runErr = errors.New("Claude child completed with unacknowledged tool result")
			}
			turn.serial.Unlock()
			if runErr == nil {
				return "completed", result, ""
			}
		}
		if runErr != nil {
			break
		}
	}
	task.mu.Lock()
	stopped = task.stopped
	inactivity := task.inactivityTimedOut
	hard := task.hardAborted
	task.mu.Unlock()
	if inactivity {
		return "failed", "Subagent execution timed out due to inactivity.", "subagent execution timed out due to inactivity"
	}
	if hard {
		return "failed", "Subagent aborted: exceeded maximum turn limit and grace period without settling.", "hard turn limit reached"
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "failed", "Subagent execution timed out.", "subagent execution timed out"
	}
	if stopped {
		return "stopped", "Subagent stopped.", ""
	}
	if runErr == nil {
		runErr = errors.New("Claude child ended without result")
	}
	return "failed", "", runErr.Error()
}
