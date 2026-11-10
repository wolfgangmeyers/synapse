package managed

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/gitsync"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/mcp"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/coding"
)

type steerPending struct {
	attempt *store.EmbeddedMailAttempt
	userMsg []byte
}

// EmbeddedManager is an alias for Manager providing embedded session control and tool bridging.
type EmbeddedManager = Manager

// SavedAgentSession is an alias for store.ManagedAgentRecord representing a saved agent session.
type SavedAgentSession = store.ManagedAgentRecord

// SetMCPManager installs the MCP manager used for remote MCP tool distribution.
func (m *EmbeddedManager) SetMCPManager(mgr *mcp.Manager) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mcpManager = mgr
}

// SetGitSyncer installs the git syncer used for git-synced tools (e.g. knowledge base).
func (m *EmbeddedManager) SetGitSyncer(syncer gitsync.Syncer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gitSyncer = syncer
}

// GitSyncer returns the active git syncer, if configured.
func (m *EmbeddedManager) GitSyncer() gitsync.Syncer {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.gitSyncer
}

// MCPManager returns the active MCP manager, if configured.
func (m *EmbeddedManager) MCPManager() *mcp.Manager {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mcpManager
}

// buildSessionTools queries active MCP tools if m.mcpManager is set and returns them as agent tools.
func (m *EmbeddedManager) buildSessionTools(saved *SavedAgentSession) []agent.AgentTool {
	var tools []agent.AgentTool
	m.mu.Lock()
	mcpMgr := m.mcpManager
	m.mu.Unlock()

	if mcpMgr == nil {
		return tools
	}

	activeTools, err := mcpMgr.ListActiveTools(context.Background())
	if err != nil {
		return tools
	}

	for _, t := range activeTools {
		toolName := t.PrefixedName
		if toolName == "" {
			toolName = t.Name
		}
		desc := ""
		if t.Description != nil {
			desc = *t.Description
		}
		tools = append(tools, m.embeddedMCPTool(t.ServerID, toolName, desc, t.InputSchemaJSON))
	}
	return tools
}

// embeddedMCPTool constructs an agent.AgentTool wrapper around a remote MCP tool.
func (m *EmbeddedManager) embeddedMCPTool(serverID, toolName, desc, schema string) agent.AgentTool {
	var parsedSchema ai.Schema
	trimmed := strings.TrimSpace(schema)
	if trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &parsedSchema); err != nil {
			parsedSchema = ai.Schema{Type: "object"}
		}
	}
	if parsedSchema.Type == "" {
		parsedSchema.Type = "object"
	}

	return agent.AgentTool{
		Name:        toolName,
		Description: desc,
		Parameters:  &parsedSchema,
		Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			m.mu.Lock()
			mcpMgr := m.mcpManager
			m.mu.Unlock()

			if mcpMgr == nil {
				return agent.AgentToolResult{
					Content: ai.ContentList{ai.TextContent{Text: "Error: MCP manager not available"}},
				}, errors.New("mcp manager not available")
			}

			res, err := mcpMgr.CallTool(ctx, serverID, toolName, args)
			if err != nil {
				return agent.AgentToolResult{
					Content: ai.ContentList{ai.TextContent{Text: fmt.Sprintf("Error calling remote tool %s: %v", toolName, err)}},
				}, fmt.Errorf("call remote tool %s: %w", toolName, err)
			}

			var content ai.ContentList
			if res != nil {
				for _, c := range res.Content {
					if c.Type == "image" && c.Data != "" {
						content = append(content, ai.ImageContent{Data: c.Data, MimeType: c.MimeType})
					} else {
						content = append(content, ai.TextContent{Text: c.Text})
					}
				}
			}
			if len(content) == 0 {
				content = ai.ContentList{ai.TextContent{Text: ""}}
			}

			return agent.AgentToolResult{Content: content}, nil
		},
	}
}

// embeddedPersistence serializes one root's checked SDK callbacks in its
// Manager lane. SQLite owns the binding, append head, and checkpoint fence.
type embeddedPersistence struct {
	db                       *store.Store
	binding                  store.EmbeddedBinding
	executionToken           int64
	claudeRestartGeneration  uint64
	seq                      int64
	digest                   string
	turnSeq                  int64
	turnDigest               string
	mail                     *store.EmbeddedMailAttempt
	mailMessage              []byte
	mailAttempt              string
	mailAppendErr            error
	steerMu                  sync.Mutex
	steerQueue               []*steerPending
	notice                   *store.EmbeddedInterruptedNotice
	noticeMessage            []byte
	noticeStart              bool
	wake                     *embeddedClaimedWakeScope
	wakeUsed                 bool
	wakeFinal                *store.EmbeddedEntry
	wakeLaterFinalTurn       int64
	reminderResult           func(context.Context, ai.ToolResultMessage, bool) error
	goalResult               func(context.Context, ai.ToolResultMessage, bool) error
	goalInvocation           func(string) *store.EmbeddedGoalInvocation
	toolInvocation           func(string) *store.EmbeddedToolInvocation
	toolChecked              func(string)
	resetChecked             func(ai.ToolResultMessage)
	goalEntry                store.EmbeddedEntry
	compactionResult         func(ai.ToolResultMessage) (bool, error)
	pendingCompaction        string
	afterUserAppend          func(context.Context)
	publishComplete          func(store.ManagedHistoryEntry)
	flushReplies             func(string)
	toolInputs               map[string]any
	currentThinking          string
	lastThinkingText         string
	toolExecuted             bool
	thinkingGenerated        bool
	toolCallReceived         bool
	lastStopReason           ai.StopReason
	lastErrorMessage         string
	currentTurnAssistantText string
	toolCompanionTexts       map[string]string
	source                   string
	msgID                    string
}

func (p *embeddedPersistence) hasSteerAttempt(messageID string) bool {
	p.steerMu.Lock()
	defer p.steerMu.Unlock()
	for _, item := range p.steerQueue {
		if item.attempt.MessageID == messageID {
			return true
		}
	}
	return false
}

func (p *embeddedPersistence) queueSteerAttempt(attempt *store.EmbeddedMailAttempt, userMsg []byte) bool {
	p.steerMu.Lock()
	defer p.steerMu.Unlock()
	for _, item := range p.steerQueue {
		if item.attempt.MessageID == attempt.MessageID {
			return false
		}
	}
	p.steerQueue = append(p.steerQueue, &steerPending{
		attempt: attempt,
		userMsg: userMsg,
	})
	return true
}

func (p *embeddedPersistence) popSteerAttempt(body []byte) *store.EmbeddedMailAttempt {
	p.steerMu.Lock()
	defer p.steerMu.Unlock()
	if len(p.steerQueue) == 0 {
		return nil
	}
	for i, item := range p.steerQueue {
		if bytes.Equal(body, item.userMsg) {
			p.steerQueue = append(p.steerQueue[:i], p.steerQueue[i+1:]...)
			return item.attempt
		}
	}
	item := p.steerQueue[0]
	p.steerQueue = p.steerQueue[1:]
	return item.attempt
}

func (p *embeddedPersistence) traceChecked(message agent.AgentMessage) {
	switch message.MessageRole() {
	case ai.RoleUser:
		aliasTrace("checked-user", p.binding.AgentID, p.binding.SessionID, p.binding.Generation, "")
	case ai.RoleAssistant:
		switch assistant := message.(type) {
		case ai.AssistantMessage:
			if assistant.StopReason == ai.StopStop {
				aliasTrace("checked-assistant", p.binding.AgentID, p.binding.SessionID, p.binding.Generation, "")
			}
		case *ai.AssistantMessage:
			if assistant.StopReason == ai.StopStop {
				aliasTrace("checked-assistant", p.binding.AgentID, p.binding.SessionID, p.binding.Generation, "")
			}
		}
	}
}

func (p *embeddedPersistence) clearExecution() error {
	if p.executionToken == 0 {
		return nil
	}
	if err := p.db.ClearEmbeddedExecution(context.Background(), p.binding, p.executionToken); err != nil {
		log.Printf("[embedded:%s] clear SDK execution: %v", p.binding.AgentID, err)
		return err
	}
	p.executionToken = 0
	return nil
}

// Repeated provider/tool admission validates the already bound user without
// refreshing its revision when a goal tool changes policy during the turn.
func (p *embeddedPersistence) bindExecutionUser(ctx context.Context) error {
	if p.executionToken == 0 {
		return nil
	} // Supporting persistence fixtures own no runtime execution.
	// Revalidate cached users at every provider/tool admission. The store's
	// exact-user path checks owner/token without rebinding the frozen revision.
	if p.turnSeq <= 0 || p.turnDigest == "" {
		return errors.New("SDK execution has no checked user")
	}
	if err := p.db.SetEmbeddedSDKExecutionUser(ctx, p.binding, p.executionToken, p.turnSeq, p.turnDigest); err != nil {
		return err
	}
	return nil
}

func (p *embeddedPersistence) projectMessage(ctx context.Context, message agent.AgentMessage) {
	if message.MessageRole() == ai.RoleUser {
		p.currentThinking = ""
		p.currentTurnAssistantText = ""
	}
	var assistantContent ai.ContentList
	var assistantTextContent strings.Builder
	switch assistant := message.(type) {
	case ai.AssistantMessage:
		assistantContent = assistant.Content
		for _, part := range assistant.Content {
			if t, ok := part.(ai.TextContent); ok {
				assistantTextContent.WriteString(t.Text)
			} else if t, ok := part.(*ai.TextContent); ok && t != nil {
				assistantTextContent.WriteString(t.Text)
			}
		}
	case *ai.AssistantMessage:
		if assistant != nil {
			assistantContent = assistant.Content
			for _, part := range assistant.Content {
				if t, ok := part.(ai.TextContent); ok {
					assistantTextContent.WriteString(t.Text)
				} else if t, ok := part.(*ai.TextContent); ok && t != nil {
					assistantTextContent.WriteString(t.Text)
				}
			}
		}
	}
	if text := strings.TrimSpace(assistantTextContent.String()); text != "" {
		p.currentTurnAssistantText = text
	}
	var stepThinking string
	for _, part := range assistantContent {
		if th, ok := part.(ai.ThinkingContent); ok && th.Thinking != "" {
			stepThinking += th.Thinking
		} else if th, ok := part.(*ai.ThinkingContent); ok && th != nil && th.Thinking != "" {
			stepThinking += th.Thinking
		}
		if call, ok := part.(ai.ToolCall); ok && call.ID != "" {
			p.toolCallReceived = true
			if p.toolInputs == nil {
				p.toolInputs = make(map[string]any)
			}
			p.toolInputs[call.ID] = call.Arguments
		} else if call, ok := part.(*ai.ToolCall); ok && call != nil && call.ID != "" {
			p.toolCallReceived = true
			if p.toolInputs == nil {
				p.toolInputs = make(map[string]any)
			}
			p.toolInputs[call.ID] = call.Arguments
		}
	}
	if stepThinking != "" {
		p.thinkingGenerated = true
		if p.currentThinking == "" || stepThinking == p.currentThinking {
			p.currentThinking = stepThinking
		} else if strings.HasPrefix(stepThinking, p.currentThinking) {
			p.currentThinking = stepThinking
		} else {
			p.currentThinking += stepThinking
		}
	}
	if assistantContent != nil {
		hasToolCalls := false
		for _, part := range assistantContent {
			if _, ok := part.(ai.ToolCall); ok {
				hasToolCalls = true
				break
			}
			if _, ok := part.(*ai.ToolCall); ok {
				hasToolCalls = true
				break
			}
		}
		if !hasToolCalls && p.currentThinking != "" {
			newThinking := strings.TrimSpace(p.currentThinking)
			if newThinking != "" && newThinking != p.lastThinkingText {
				thinkingKey := fmt.Sprintf("thinking:%s:%x", p.binding.AgentID, sha256.Sum256([]byte(p.currentThinking)))
				pres := &store.ManagedHistoryPresentation{
					Kind:    "thinking",
					Outcome: "success",
					Summary: "Thinking process",
				}
				thinkingCopy := p.currentThinking
				entry, err := p.db.RecordManagedCompleteHistoryWithDetails(ctx, p.binding.AgentID, "managed_thinking", "info", "Thinking Process", thinkingKey, pres, nil, &thinkingCopy)
				if err == nil && p.publishComplete != nil {
					p.publishComplete(entry)
				}
				p.lastThinkingText = newThinking
			}
			p.currentThinking = ""
		}
	}
	if result, ok := message.(ai.ToolResultMessage); ok {
		if result.ToolName != "goal" && result.ToolName != "goal_snooze" && result.ToolName != "workflow_snooze" && result.ToolName != "context_reset" && result.ToolName != "reset" {
			p.toolExecuted = true
		}
		toolName := result.ToolName
		toolCallID := result.ToolCallID
		if toolCallID != "" && toolName != "" {
			var toolInput any
			if p.toolInputs != nil {
				toolInput = p.toolInputs[toolCallID]
				delete(p.toolInputs, toolCallID)
			}
			isFailed := result.IsError
			if !isFailed && toolName == "bash" && len(result.Content) > 0 {
				if textContent, ok := result.Content[0].(ai.TextContent); ok {
					if strings.HasPrefix(textContent.Text, "exit=") && !strings.HasPrefix(textContent.Text, "exit=0") {
						isFailed = true
					}
				}
			}
			var outputBuilder strings.Builder
			for _, item := range result.Content {
				switch c := item.(type) {
				case ai.TextContent:
					outputBuilder.WriteString(c.Text)
				case *ai.TextContent:
					if c != nil {
						outputBuilder.WriteString(c.Text)
					}
				}
			}
			toolOutput := outputBuilder.String()
			if toolOutput == "" && result.Details != nil {
				if detailsBytes, err := json.Marshal(result.Details); err == nil && len(detailsBytes) > 0 && string(detailsBytes) != "{}" && string(detailsBytes) != "null" {
					toolOutput = string(detailsBytes)
				}
			}
			var contentPtr *string
			if toolOutput != "" {
				contentPtr = &toolOutput
			}
			var companionText string
			if p.toolCompanionTexts != nil {
				companionText = p.toolCompanionTexts[toolCallID]
				delete(p.toolCompanionTexts, toolCallID)
			}
			level := "info"
			messageText := "Ran " + toolName
			pres := &store.ManagedHistoryPresentation{
				Kind:          "tool",
				ToolName:      toolName,
				Outcome:       "success",
				Summary:       "Completed successfully",
				Thinking:      p.currentThinking,
				CompanionText: companionText,
			}
			if isFailed {
				level = "error"
				displayName := toolName
				if len(displayName) > 0 {
					displayName = strings.ToUpper(displayName[:1]) + displayName[1:]
				}
				messageText = displayName + " failed"
				pres = &store.ManagedHistoryPresentation{
					Kind:          "tool",
					ToolName:      toolName,
					Outcome:       "failed",
					Summary:       "The operation could not be completed.",
					Error:         &store.ManagedHistoryError{Code: "TOOL_FAILED", Retryable: false, DetailAvailable: false},
					Thinking:      p.currentThinking,
					CompanionText: companionText,
				}
			}
			if toolName == "mecha_mailbox_send" || toolName == "mecha_send_image" {
				if args, ok := toolInput.(map[string]any); ok {
					if recipient, ok := args["recipient"].(string); ok {
						pres.To = recipient
					}
					pres.From = p.binding.AgentID
				}
			}
			completionKey := fmt.Sprintf("tool:%s:%s", p.binding.AgentID, toolCallID)
			entry, err := p.db.RecordManagedCompleteHistoryWithDetails(ctx, p.binding.AgentID, "managed_tool", level, messageText, completionKey, pres, toolInput, contentPtr)
			if err == nil && p.publishComplete != nil {
				p.publishComplete(entry)
			}
		}
	}
}

func (p *embeddedPersistence) AppendMessage(ctx context.Context, message agent.AgentMessage) error {
	return p.appendMessage(ctx, message, false)
}

// AppendCheckedMessage retains checked validators and callbacks, but projects
// thinking/tool success only after the checked append and result observations.
// Unlike Pi's SDK callback this is suitable for serialized Claude translation.
func (p *embeddedPersistence) AppendCheckedMessage(ctx context.Context, message agent.AgentMessage) error {
	if err := p.appendMessage(ctx, message, true); err != nil {
		return err
	}
	p.projectMessage(ctx, message)
	return nil
}

// appendClaudeSteerMail commits a mail input steered into a running Claude turn.
// The attempt is bound explicitly, so equal-text messages never swap identities
// the way byte matching in the Pi steer queue could.
func (p *embeddedPersistence) appendClaudeSteerMail(ctx context.Context, attempt *store.EmbeddedMailAttempt, message ai.UserMessage) error {
	body, err := json.Marshal(message)
	if err != nil {
		return err
	}
	entry, historyEntry, err := p.db.AppendEmbeddedSteerMailAndACK(ctx, p.binding, attempt.AttemptID, body)
	if err != nil {
		return err
	}
	p.seq, p.digest = entry.Sequence, entry.Digest
	p.turnSeq, p.turnDigest = entry.Sequence, entry.Digest
	p.mailAttempt = attempt.AttemptID
	p.traceChecked(message)
	if p.publishComplete != nil && historyEntry.ID != "" {
		p.publishComplete(historyEntry)
	}
	p.projectMessage(ctx, message)
	return p.bindExecutionUser(ctx)
}

func (p *embeddedPersistence) appendMessage(ctx context.Context, message agent.AgentMessage, checkedFirst bool) (retErr error) {
	// Every user writer (ordinary, mail, steer and recovery notice) converges
	// here. Bind only after checked persistence, before SDK provider/tool entry.
	defer func() {
		if retErr == nil && message.MessageRole() == ai.RoleUser && p.executionToken > 0 {
			retErr = p.bindExecutionUser(ctx)
		}
	}()
	defer func() {
		if retErr != nil {
			log.Printf("[embedded:%s] ERROR turn failed (source: %s, message: %s): %v", p.binding.AgentID, p.source, p.msgID, retErr)
		}
	}()
	if !checkedFirst {
		p.projectMessage(ctx, message)
	}
	if result, ok := message.(ai.ToolResultMessage); ok && result.ToolName == "mecha_context" && p.compactionResult != nil {
		compact, err := p.compactionResult(result)
		if err != nil {
			return err
		}
		if compact {
			p.pendingCompaction = result.ToolCallID
		}
	}
	if result, ok := message.(ai.ToolResultMessage); ok {
		if result.ToolName == "mecha_reminder" && p.reminderResult != nil {
			if err := p.reminderResult(ctx, result, false); err != nil {
				return err
			}
		}
		if p.goalResult != nil {
			if err := p.goalResult(ctx, result, false); err != nil {
				return err
			}
		}
	}
	if result, ok := message.(ai.ToolResultMessage); ok && result.ToolName == "goal" && !result.IsError {
		if len(result.Content) != 1 {
			return errors.New("goal result must have one text payload")
		}
		text, ok := result.Content[0].(ai.TextContent)
		if !ok {
			return errors.New("goal result must be text")
		}
		if err := p.db.CheckEmbeddedGoalResult(ctx, p.binding, result.ToolCallID, []byte(text.Text)); err != nil {
			return err
		}
	}
	if result, ok := message.(ai.ToolResultMessage); ok && (result.ToolName == "mecha_mailbox_send" || result.ToolName == "mecha_send_image" || result.ToolName == "mecha_agent_urgency") && p.toolInvocation != nil && p.toolInvocation(result.ToolCallID) == nil {
		// A denial before intent is an ordinary checked SDK error. An intent
		// without this Execute's completion stays UNKNOWN, never an SDK receipt.
		_, err := p.db.RecoverEmbeddedEffect(ctx, p.binding, result.ToolCallID)
		if !result.IsError || !errors.Is(err, sql.ErrNoRows) {
			return errors.New("managed tool outcome UNKNOWN: no current SDK producer completion")
		}
	}
	body, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if p.notice != nil && message.MessageRole() == ai.RoleUser {
		if !bytes.Equal(body, p.noticeMessage) {
			return errors.New("embedded SDK recovery notice differs from saved work")
		}
		entry, err := p.db.AppendEmbeddedInterruptedNotice(ctx, p.binding, *p.notice, p.seq, p.digest, body)
		if err == nil {
			p.seq, p.digest = entry.Sequence, entry.Digest
			p.turnSeq, p.turnDigest = entry.Sequence, entry.Digest
			p.noticeStart = true
			p.traceChecked(message)
		}
		return err
	}
	if p.mail != nil && message.MessageRole() == ai.RoleUser {
		if !bytes.Equal(body, p.mailMessage) {
			return errors.New("embedded SDK mail differs from reserved attempt")
		}
		historyEntry, err := p.db.AppendEmbeddedMailSDKAndACKWithHistory(ctx, p.binding, p.mail.AttemptID, []byte(p.mail.Body), body)
		if err != nil {
			p.mailAppendErr = err
			return err
		}
		snap, err := p.db.ReopenEmbedded(ctx, p.binding)
		if err != nil || len(snap.Entries) == 0 {
			return errors.New("embedded mail append head unavailable")
		}
		last := snap.Entries[len(snap.Entries)-1]
		if last.Sequence != p.seq+1 || !bytes.Equal(last.Body, body) {
			return errors.New("embedded mail append head mismatch")
		}
		p.seq, p.digest = last.Sequence, last.Digest
		p.turnSeq, p.turnDigest = last.Sequence, last.Digest
		p.mailAttempt = p.mail.AttemptID
		p.mail = nil
		p.traceChecked(message)
		if p.publishComplete != nil && historyEntry.ID != "" {
			p.publishComplete(historyEntry)
		}
		return nil
	}
	if message.MessageRole() == ai.RoleUser {
		if steerAttempt := p.popSteerAttempt(body); steerAttempt != nil {
			entry, historyEntry, err := p.db.AppendEmbeddedSteerMailAndACK(ctx, p.binding, steerAttempt.AttemptID, body)
			if err != nil {
				return err
			}
			p.seq, p.digest = entry.Sequence, entry.Digest
			p.turnSeq, p.turnDigest = entry.Sequence, entry.Digest
			p.mailAttempt = steerAttempt.AttemptID
			p.traceChecked(message)
			if p.publishComplete != nil && historyEntry.ID != "" {
				p.publishComplete(historyEntry)
			}
			return nil
		}
	}
	var checkedWakeFinal *store.EmbeddedEntry
	if p.wake != nil {
		final := false
		switch assistant := message.(type) {
		case ai.AssistantMessage:
			final = assistant.StopReason == ai.StopStop
		case *ai.AssistantMessage:
			final = assistant.StopReason == ai.StopStop
		}
		if final {
			if p.wakeFinal != nil {
				if p.turnSeq <= p.wakeFinal.Sequence || p.turnSeq <= p.wakeLaterFinalTurn {
					return errors.New("claimed wake final already appended")
				}
				defer func() {
					if retErr == nil {
						p.wakeLaterFinalTurn = p.turnSeq
					}
				}()
			} else {
				key := fmt.Sprintf("root:%s:%s:%d:%s:%d", p.binding.AgentID, p.binding.SessionID, p.binding.Epoch, p.binding.Generation, p.seq+1)
				entry, err := p.db.AppendEmbeddedRestartWakeFinalAt(ctx, p.binding, p.wake.processGeneration, p.wake.id, p.seq, p.digest, key, body)
				if err != nil {
					return err
				}
				p.wakeFinal = &entry
				checkedWakeFinal = &entry
			}
		}
	}
	if p.mailAttempt != "" {
		final := false
		switch assistant := message.(type) {
		case ai.AssistantMessage:
			final = assistant.StopReason == ai.StopStop || assistant.StopReason == ai.StopError
		case *ai.AssistantMessage:
			if assistant != nil {
				final = assistant.StopReason == ai.StopStop || assistant.StopReason == ai.StopError
			}
		}
		if final {
			entry, replyHistoryEntry, err := p.db.AppendEmbeddedFinalAndReplyWithHistory(ctx, p.binding, p.mailAttempt, p.seq, p.digest, body)
			if err == nil {
				p.seq, p.digest = entry.Sequence, entry.Digest
				p.mailAttempt = ""
				p.traceChecked(message)
				if p.publishComplete != nil && replyHistoryEntry.ID != "" {
					p.publishComplete(replyHistoryEntry)
				}
				if p.flushReplies != nil {
					p.flushReplies(p.binding.AgentID)
				}
			}
			return err
		}
	}
	key := fmt.Sprintf("root:%s:%s:%d:%s:%d", p.binding.AgentID, p.binding.SessionID, p.binding.Epoch, p.binding.Generation, p.seq+1)
	var entry store.EmbeddedEntry
	if checkedWakeFinal != nil {
		// The fenced wake append already saved this final. Reuse its checked
		// entry for normal history projection, never append the SDK reply twice.
		entry = *checkedWakeFinal
	} else if result, ok := message.(ai.ToolResultMessage); ok && p.toolInvocation != nil && p.toolInvocation(result.ToolCallID) != nil {
		entry, err = p.db.AppendEmbeddedToolInvocationResult(ctx, p.toolInvocation(result.ToolCallID), p.seq, p.digest, key, result, body)
	} else if result, ok := message.(ai.ToolResultMessage); ok && p.goalInvocation != nil && p.goalInvocation(result.ToolCallID) != nil {
		entry, err = p.db.AppendEmbeddedGoalInvocationResult(ctx, p.goalInvocation(result.ToolCallID), p.seq, p.digest, key, result, body)
	} else {
		entry, err = p.db.AppendEmbeddedEntryAt(ctx, p.binding, p.seq, p.digest, key, string(message.MessageRole()), body)
	}
	if err == nil {
		p.seq, p.digest = entry.Sequence, entry.Digest
		p.traceChecked(message)
		if result, ok := message.(ai.ToolResultMessage); ok && p.resetChecked != nil {
			p.resetChecked(result)
		}
		if result, ok := message.(ai.ToolResultMessage); ok && p.toolChecked != nil {
			p.toolChecked(result.ToolCallID)
		}

		if result, ok := message.(ai.ToolResultMessage); ok && result.ToolName == "mecha_reminder" && p.reminderResult != nil {
			if observeErr := p.reminderResult(ctx, result, true); observeErr != nil {
				return observeErr
			}
		}
		if result, ok := message.(ai.ToolResultMessage); ok && p.goalResult != nil {
			p.goalEntry = entry
			if observeErr := p.goalResult(ctx, result, true); observeErr != nil {
				return observeErr
			}
		}
		if result, ok := message.(ai.ToolResultMessage); ok && result.ToolName != "mecha_mailbox_send" && result.ToolName != "mecha_send_image" && result.ToolName != "mecha_agent_urgency" {
			if result.ToolName != "read" && result.ToolName != "write" && result.ToolName != "edit" && result.ToolName != "mecha_reminder" && result.ToolName != "mecha_context" && result.ToolName != "bash" && result.ToolName != "goal" && result.ToolName != "goal_snooze" && result.ToolName != "workflow_snooze" && result.ToolName != "mecha_tool_timeout" && result.ToolName != "extend_tool_timeout" && result.ToolName != InlineToolAllowanceToolName {
				body, err := json.Marshal(result)
				if err == nil {
					_ = p.db.ObserveEmbeddedEffect(ctx, p.binding, result.ToolCallID, body)
				}
			}
		}
		if message.MessageRole() == ai.RoleUser {
			p.turnSeq, p.turnDigest = entry.Sequence, entry.Digest
			p.mailAttempt = ""
			if p.afterUserAppend != nil {
				p.afterUserAppend(ctx)
			}

			// Record user / steering message to managed_complete_history
			var userText strings.Builder
			switch user := message.(type) {
			case ai.UserMessage:
				for _, c := range user.Content {
					if t, ok := c.(ai.TextContent); ok {
						userText.WriteString(t.Text)
					} else if t, ok := c.(*ai.TextContent); ok && t != nil {
						userText.WriteString(t.Text)
					}
				}
			case *ai.UserMessage:
				if user != nil {
					for _, c := range user.Content {
						if t, ok := c.(ai.TextContent); ok {
							userText.WriteString(t.Text)
						} else if t, ok := c.(*ai.TextContent); ok && t != nil {
							userText.WriteString(t.Text)
						}
					}
				}
			}
			userBodyText := userText.String()
			if len(strings.TrimSpace(userBodyText)) > 0 && !strings.HasPrefix(strings.TrimSpace(userBodyText), "[Workflow Activated:") {
				eventKey := fmt.Sprintf("embedded-user:%s:%d:%s", p.binding.AgentID, entry.Sequence, entry.Digest)
				presentation := &store.ManagedHistoryPresentation{
					Kind:    "message",
					Outcome: "success",
					From:    "user",
					To:      p.binding.AgentID,
					Summary: "User message",
				}
				historyEntry, err := p.db.RecordManagedCompleteHistoryWithDetails(
					ctx,
					p.binding.AgentID,
					"managed_message",
					"info",
					"User message",
					eventKey,
					presentation,
					nil,
					&userBodyText,
				)
				if err == nil && p.publishComplete != nil && historyEntry.ID != "" {
					p.publishComplete(historyEntry)
				}
			}
		} else if message.MessageRole() == ai.RoleAssistant {
			final := false
			_ = final
			var text strings.Builder
			var thinking strings.Builder
			var errMsg string
			var stopReason ai.StopReason
			hasToolCalls := false
			switch assistant := message.(type) {
			case ai.AssistantMessage:
				stopReason = assistant.StopReason
				p.lastStopReason = assistant.StopReason
				p.lastErrorMessage = assistant.ErrorMessage
				final = stopReason == ai.StopStop || stopReason == ai.StopError
				errMsg = assistant.ErrorMessage
				for _, c := range assistant.Content {
					if _, ok := c.(ai.ToolCall); ok {
						hasToolCalls = true
					} else if call, ok := c.(*ai.ToolCall); ok && call != nil {
						hasToolCalls = true
					} else if t, ok := c.(ai.TextContent); ok {
						text.WriteString(t.Text)
					} else if t, ok := c.(*ai.TextContent); ok && t != nil {
						text.WriteString(t.Text)
					} else if th, ok := c.(ai.ThinkingContent); ok {
						thinking.WriteString(th.Thinking)
					} else if th, ok := c.(*ai.ThinkingContent); ok && th != nil {
						thinking.WriteString(th.Thinking)
					}
				}
			case *ai.AssistantMessage:
				if assistant != nil {
					stopReason = assistant.StopReason
					p.lastStopReason = assistant.StopReason
					p.lastErrorMessage = assistant.ErrorMessage
					final = stopReason == ai.StopStop || stopReason == ai.StopError
					errMsg = assistant.ErrorMessage
					for _, c := range assistant.Content {
						if _, ok := c.(ai.ToolCall); ok {
							hasToolCalls = true
						} else if call, ok := c.(*ai.ToolCall); ok && call != nil {
							hasToolCalls = true
						} else if t, ok := c.(ai.TextContent); ok {
							text.WriteString(t.Text)
						} else if t, ok := c.(*ai.TextContent); ok && t != nil {
							text.WriteString(t.Text)
						} else if th, ok := c.(ai.ThinkingContent); ok {
							thinking.WriteString(th.Thinking)
						} else if th, ok := c.(*ai.ThinkingContent); ok && th != nil {
							thinking.WriteString(th.Thinking)
						}
					}
				}
			}
			replyBodyText := text.String()
			outcome := "success"
			msgText := "Message sent"
			if stopReason == ai.StopError {
				outcome = "error"
				if errMsg == "" {
					errMsg = "turn failed"
				}
				msgText = fmt.Sprintf("Turn failed: %s", errMsg)
				if len(strings.TrimSpace(replyBodyText)) == 0 {
					replyBodyText = fmt.Sprintf("Turn failed: %s", errMsg)
				} else {
					replyBodyText = fmt.Sprintf("%s\n\nTurn failed: %s", replyBodyText, errMsg)
				}
			}
			if !hasToolCalls && len(strings.TrimSpace(replyBodyText)) > 0 {
				eventKey := fmt.Sprintf("embedded-assistant:%s:%d:%s", p.binding.AgentID, entry.Sequence, entry.Digest)
				presentation := &store.ManagedHistoryPresentation{
					Kind:     "message",
					Outcome:  outcome,
					From:     p.binding.AgentID,
					To:       "user",
					Thinking: thinking.String(),
				}
				historyEntry, err := p.db.RecordManagedCompleteHistoryWithDetails(
					ctx,
					p.binding.AgentID,
					"managed_message",
					"info",
					msgText,
					eventKey,
					presentation,
					nil,
					&replyBodyText,
				)
				if err == nil && p.publishComplete != nil && historyEntry.ID != "" {
					p.publishComplete(historyEntry)
				}
			}
		}
	}
	return err
}
func (p *embeddedPersistence) CommitCheckpoint(ctx context.Context, checkpoint coding.OrderedCheckpoint) error {
	snap, err := p.db.ReopenEmbedded(ctx, p.binding)
	if err != nil {
		return err
	}
	if checkpoint.ThroughSequence < 1 || checkpoint.ThroughSequence > int64(len(snap.Entries)) {
		return errors.New("checkpoint outside durable history")
	}
	body, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	return p.db.CommitEmbeddedCheckpoint(ctx, p.binding, checkpoint.ThroughSequence, snap.Entries[checkpoint.ThroughSequence-1].Digest, body)
}
func (p *embeddedPersistence) restore(ctx context.Context, session *coding.Session) error {
	snap, err := p.db.ReopenEmbedded(ctx, p.binding)
	if err == nil && len(snap.Entries) == 0 {
		snap, err = p.db.ReopenLatestAgentEmbedded(ctx, p.binding.AgentID)
	}
	if err != nil {
		return err
	}
	projection := coding.OrderedSnapshot{SessionID: p.binding.SessionID}
	for _, entry := range snap.Entries {
		message, err := ai.UnmarshalMessage(entry.Body)
		if err != nil {
			return err
		}
		if string(message.MessageRole()) != entry.Kind {
			return errors.New("embedded saved role mismatch")
		}
		projection.Messages = append(projection.Messages, coding.OrderedMessage{Sequence: entry.Sequence, ParentSequence: entry.Sequence - 1, Message: message})
		p.seq, p.digest = entry.Sequence, entry.Digest
	}
	if snap.Checkpoint != nil {
		var checkpoint coding.OrderedCheckpoint
		if err := json.Unmarshal(snap.Checkpoint.Body, &checkpoint); err != nil {
			return err
		}
		if checkpoint.ThroughSequence != snap.Checkpoint.Sequence {
			return errors.New("embedded checkpoint sequence mismatch")
		}
		projection.Checkpoint = &checkpoint
	}
	return session.RestoreOrdered(ctx, projection)
}

// embeddedOwnerSendDoer checks the exact saved claim at the last router-owned
// boundary before handing a request to the SDK's chosen HTTP doer. A takeover
// after this check (including an in-flight request) remains uncertain.
// An unexported, process-local observation surface. Ordinary builds have no
// runtime configuration or persistence. The explicit embedded_private_status_probe
// build tag enables environment configuration and fixed projected JSON rows.
// Never pass the request, response, or original error to the observer.
type embeddedTransportTrace struct {
	Ordinal  int
	Status   int
	Category string
}

func embeddedTransportCategory(err error) string {
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "dns"
	}
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	if errors.As(err, &unknownAuthority) || errors.As(err, &hostname) || errors.As(err, &invalid) {
		return "tls_cert"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ENETUNREACH) {
		return "connection"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return "connection"
	}
	return "other"
}

type embeddedOwnerSendDoer struct {
	store    *store.Store
	binding  store.EmbeddedBinding
	identity string
	inner    ai.HTTPDoer
	attempts *atomic.Int32
	observe  func(embeddedTransportTrace)
	terminal func()
}

func (d embeddedOwnerSendDoer) Do(req *http.Request) (*http.Response, error) {
	if err := d.store.RenewEmbeddedOwner(req.Context(), d.binding, d.identity, embeddedOwnerTTL); err != nil {
		return nil, errors.New("embedded owner unavailable before provider send")
	}
	// Count only sends that crossed the owner fence, including the one possible
	// SDK 401 rotation. Nil observers incur no telemetry or stored trace.
	if d.observe == nil {
		resp, err := d.inner.Do(req)
		if d.terminal != nil {
			d.terminal()
		}
		return resp, err
	}
	ordinal := int(d.attempts.Add(1))
	resp, err := d.inner.Do(req)
	trace := embeddedTransportTrace{Ordinal: ordinal}
	if err != nil {
		trace.Category = embeddedTransportCategory(err)
	} else if resp != nil {
		trace.Status = resp.StatusCode
	} else {
		trace.Category = "other"
	}
	d.observe(trace)
	if d.terminal != nil {
		d.terminal()
	}
	return resp, err
}

// runEmbeddedSession uses no stock Pi storage or RPC. Provider selection is
// host-pluggable; a missing model fails closed before prompting. Root and
// worker sessions get fixed router-owned read and worker management tools; other
// tools require explicit host registration.
func (m *Manager) runEmbeddedSession(ctx context.Context, saved store.ManagedAgentRecord) error {
	return m.runEmbeddedSessionPrompt(ctx, saved, "")
}

type embeddedOriginalRestartGenerationKey struct{}

// The caller must already own the runner and supply a listener-verified process
// generation. This seam does not bind a listener or dispatch the planned wake.
func (m *Manager) runEmbeddedSessionOriginalRestart(ctx context.Context, saved store.ManagedAgentRecord, generation int64) error {
	if generation <= 0 {
		return errors.New("missing verified restart generation")
	}
	return m.runEmbeddedSession(context.WithValue(ctx, embeddedOriginalRestartGenerationKey{}, generation), saved)
}

func (m *Manager) runEmbeddedMail(ctx context.Context, saved store.ManagedAgentRecord, attempt *store.EmbeddedMailAttempt) (int64, error) {
	return m.runEmbeddedSessionTurnScoped(ctx, saved, "", attempt, nil, nil, nil)
}

// Explicit prompts are only supplied by an already-claimed runner; recovery
// alone never resumes a user turn with an unknown external effect.
func (m *Manager) runEmbeddedSessionPrompt(ctx context.Context, saved store.ManagedAgentRecord, explicitPrompt string) error {
	return m.runEmbeddedSessionTurn(ctx, saved, explicitPrompt, nil)
}

func (m *Manager) runEmbeddedSessionTurn(ctx context.Context, saved store.ManagedAgentRecord, explicitPrompt string, mail *store.EmbeddedMailAttempt) error {
	return m.runEmbeddedSessionTurnWithContinuation(ctx, saved, explicitPrompt, mail, nil)
}

func (m *Manager) runEmbeddedSessionTurnWithContinuation(ctx context.Context, saved store.ManagedAgentRecord, explicitPrompt string, mail *store.EmbeddedMailAttempt, continuation *store.EmbeddedGoalEvaluationKey) error {
	_, err := m.runEmbeddedSessionTurnScoped(ctx, saved, explicitPrompt, mail, continuation, nil, nil)
	return err
}

// A caller supplies an already claimed wake and the exact prompt for one SDK
// invocation. This is not a scheduler: it never reserves, claims, or retries.
func (m *Manager) runEmbeddedClaimedWake(ctx context.Context, saved store.ManagedAgentRecord, scope embeddedClaimedWakeScope, prompts []agent.AgentMessage) error {
	_, err := m.runEmbeddedSessionTurnScoped(ctx, saved, "", nil, nil, &scope, prompts)
	return err
}

func buildEmbeddedWorkerSystemPrompt(role, agentID, cwd string) string {
	return buildEmbeddedEngineSystemPrompt(role, agentID, cwd, false)
}

func buildEmbeddedEngineSystemPrompt(role, agentID, cwd string, claude bool) string {
	base := `You are an expert coding assistant operating inside pi, a coding agent harness. You help users by reading files, executing commands, editing code, and writing new files.

### Responsiveness & Communication
When a user asks a question or steers your work and you need to gather information or run tools to formulate your answer, always acknowledge the user with a brief conversational response first before starting information gathering.`

	if agentID != "" {
		identityRole := "worker"
		if claude && role == "root" {
			identityRole = "root"
		}
		identity := fmt.Sprintf(`# Agent Identity
- You are an embedded %s running in the mecha router harness.
- Your agent name / mailbox address is: `+"`%s`"+`
- When sending or receiving mailbox messages, your identity is `+"`%s`"+`.
- Your working directory is: `+"`%s`", identityRole, agentID, agentID, cwd)
		return base + "\n\n" + identity
	}
	return base
}

func resolveEmbeddedWorkerSystemPrompt(role, agentID, cwd string) string {
	return buildEmbeddedWorkerSystemPrompt(role, agentID, cwd)
}

func formatGenesisPrompt(agentID, instruction string) string {
	namePrefix := fmt.Sprintf("You are the '%s' agent.", agentID)
	trimmed := strings.TrimSpace(instruction)
	if trimmed == "" {
		return namePrefix + " Proceed with your assigned task."
	}
	if strings.Contains(trimmed, fmt.Sprintf("You are the '%s' agent", agentID)) {
		return trimmed
	}
	return namePrefix + "\n\n" + trimmed
}

const (
	embeddedSynthesisPrompt    = "Please provide your direct conversational response and answer to the user based on the actions above."
	embeddedThinkingOnlyPrompt = "You generated internal thinking without taking action or responding. Please execute your next tool call or provide your response to proceed."
)

func (m *Manager) nudgeEmbeddedSessionIfNeeded(ctx context.Context, session embeddedRuntime, p *embeddedPersistence) {
	if p != nil && m.isWorkerSelfTerminationPending(p.binding.AgentID) {
		return
	}
	if p.toolExecuted && len(strings.TrimSpace(session.Snapshot().LastAssistantText)) == 0 {
		_, _ = session.Run(ctx, embeddedSynthesisPrompt)
	} else if !p.toolExecuted && !p.toolCallReceived && len(strings.TrimSpace(session.Snapshot().LastAssistantText)) == 0 && p.thinkingGenerated {
		_, _ = session.Run(ctx, embeddedThinkingOnlyPrompt)
		if p.toolExecuted && len(strings.TrimSpace(session.Snapshot().LastAssistantText)) == 0 {
			_, _ = session.Run(ctx, embeddedSynthesisPrompt)
		}
	}
}

var errEmbeddedExecutionUnsettled = errors.New("embedded execution did not settle")

func (m *Manager) runEmbeddedSessionTurnScoped(ctx context.Context, saved store.ManagedAgentRecord, explicitPrompt string, mail *store.EmbeddedMailAttempt, continuation *store.EmbeddedGoalEvaluationKey, wake *embeddedClaimedWakeScope, wakePrompts []agent.AgentMessage) (finalSeq int64, turnErr error) {
	owner, ok := ctx.Value(embeddedOwnerContextKey{}).(embeddedOwner)
	if !ok || owner.binding.AgentID != saved.AgentID || owner.binding.SessionID != saved.PiSessionID || owner.binding.Epoch != saved.SessionEpoch || owner.binding.Generation != saved.RuntimeGeneration {
		return 0, errors.New("embedded runner requires claimed owner")
	}
	binding := owner.binding
	turnRestartGeneration := m.laneFor(binding.AgentID).claudeRestartGeneration.Load()
	if err := m.store.RenewEmbeddedOwner(ctx, binding, owner.identity, embeddedOwnerTTL); err != nil {
		return 0, err
	}
	// Only deliberate fresh user admission may recover an ownership hold here.
	// Renewal above must succeed first; passive/system turns are not recovery.
	if explicitUser, _ := ctx.Value(embeddedExplicitUserPromptKey{}).(bool); explicitUser {
		m.laneFor(binding.AgentID).goalOwnerInvalid = false
	}
	if m.laneFor(binding.AgentID).embeddedResetPending() {
		return 0, &store.ManagedAgentNotReadyError{AgentID: binding.AgentID}
	}

	executionToken, executionErr := m.store.EnterEmbeddedSDKExecution(ctx, binding, continuation)
	if executionErr != nil {
		return 0, executionErr
	}
	var p *embeddedPersistence
	var runtime embeddedRuntime
	var claudeCloseOnce sync.Once
	var claudeCloseErr error
	settleExecution := func() error {
		if claude, ok := runtime.(*claudeCodeRuntime); ok {
			claudeCloseOnce.Do(func() { claudeCloseErr = claude.closeScoped() })
			if claudeCloseErr != nil {
				return claudeCloseErr
			}
		}
		if p != nil {
			return p.clearExecution()
		}
		if executionToken == 0 {
			return nil
		}
		err := m.store.ClearEmbeddedExecution(context.Background(), binding, executionToken)
		if err != nil {
			log.Printf("[embedded:%s] clear SDK execution: %v", binding.AgentID, err)
			return err
		}
		executionToken = 0
		return nil
	}
	defer func() {
		if err := settleExecution(); err != nil {
			turnErr = errors.Join(turnErr, errEmbeddedExecutionUnsettled, err)
		}
	}()

	source := "prompt"
	msgID := ""
	if mail != nil {
		source = "mail"
		msgID = mail.MessageID
	} else if wake != nil {
		source = "wake"
		msgID = wake.id
	} else if continuation != nil {
		source = "continuation"
		msgID = fmt.Sprintf("%d:%s", continuation.UserSeq, continuation.UserDigest)
	} else if explicitPrompt != "" {
		source = "prompt"
		msgID = ""
	} else if saved.PendingInitialInstruction != "" {
		source = "initial_instruction"
		msgID = ""
	}
	if mail == nil {
		log.Printf("[embedded:%s] Turn started (source: %s, message: %s)", binding.AgentID, source, msgID)
	}
	var session *coding.Session
	var model *ai.Model
	_ = m.store.SetEmbeddedActivityState(ctx, binding, "active")
	m.publishProjection()
	defer func() {
		// SDK Run has joined its callbacks. Settle before wake offers.
		settleErr := settleExecution()
		if settleErr != nil && turnErr == nil {
			turnErr = settleErr
		}
		if runtime != nil {
			isStaleEpoch := false
			if m.store != nil {
				if currentAgent, err := m.store.ManagedAgent(context.Background(), binding.AgentID); err == nil && currentAgent != nil && currentAgent.SessionEpoch > binding.Epoch {
					isStaleEpoch = true
				}
			}
			if !isStaleEpoch {
				snapshot := runtime.Snapshot()
				tokens := snapshot.ContextTokens
				window := 0
				if model != nil && model.Provider == claudeCodeProvider {
					window = snapshot.ContextWindow
					m.laneFor(binding.AgentID).setClaudeWindow(binding, saved.SelectedModel.ID, window)
				} else if model != nil && model.ContextWindow > 0 {
					window = model.ContextWindow
				} else {
					window = snapshot.ContextWindow
				}
				m.updateAgentContextUsage(binding.AgentID, &tokens, window)
			}
		}
		if turnErr == nil && p != nil && p.lastStopReason == ai.StopError {
			turnErr = errors.New("embedded provider returned StopError")
		}
		if turnErr != nil {
			if mail == nil {
				log.Printf("[embedded:%s] ERROR turn failed (source: %s, message: %s): %v", binding.AgentID, source, msgID, turnErr)
			}
		} else if mail == nil && p != nil {
			log.Printf("[embedded:%s] Turn finished successfully (seq: %d)", binding.AgentID, p.seq)
		}
		automatic, _ := ctx.Value(embeddedAutomaticGoalKey{}).(bool)
		if !automatic && p != nil && p.toolExecuted && turnErr == nil {
			lane := m.laneFor(binding.AgentID)
			// Serialize the generation check and cleanup with restart reservation.
			lane.selection.Lock()
			if lane.claudeRestartGeneration.Load() == turnRestartGeneration {
				lane.consecutiveContinuations.Store(0)
			}
			lane.selection.Unlock()
		}
		if errors.Is(turnErr, context.Canceled) || (turnErr != nil && strings.Contains(turnErr.Error(), "context canceled")) {
			_ = m.store.ResetEmbeddedAgentInterrupt(context.Background(), binding.AgentID)
		} else {
			_ = m.store.SetEmbeddedActivityState(context.Background(), binding, "idle")
		}
		m.publishProjection()
		if explicitPrompt != ContextResetWakePrompt && settleErr == nil && (mail == nil || turnErr == nil) && !m.isWorkerSelfTerminationPending(binding.AgentID) {
			m.OfferDirect(binding.AgentID)
		}
		m.checkWorkerSelfTerminationAfterTurn(context.Background(), binding.AgentID)
	}()
	// Restored selections are untrusted admission input too. Check the real
	// SDK identity and private credential before any test substitution, tools,
	// conversation append, or provider work.
	if _, err := m.admitEmbeddedModel(ctx, Model{Provider: saved.SelectedModel.Provider, ID: saved.SelectedModel.ID}); err != nil {
		return 0, err
	}
	if err := m.validateEmbeddedWorkingDirectory(saved.Cwd); err != nil {
		return 0, err
	}
	var err error
	if saved.SelectedModel.Provider == claudeCodeProvider {
		model = &ai.Model{Provider: claudeCodeProvider, Api: claudeCodeProvider, ID: saved.SelectedModel.ID, ContextWindow: 1000000}
	} else {
		model, err = ResolveModel(saved.SelectedModel)
	}
	if err != nil && m.embeddedModelResolver == nil {
		return 0, err
	}
	var injectedStreamModel *ai.Model
	if m.embeddedModelResolver != nil && saved.SelectedModel.Provider != claudeCodeProvider {
		resolved := m.embeddedModelResolver(saved)
		if resolved == nil {
			return 0, errors.New("embedded model is unavailable")
		}
		if resolved.Provider == "openai-codex" {
			model = resolved
		} else {
			// Existing explicitly injected SDK doubles are a transport boundary,
			// not selection authority. Never substitute a built-in provider (which
			// could resolve ambient auth). The SDK session keeps its Codex identity.
			if len(ai.GetModels(resolved.Provider)) != 0 {
				return 0, errors.New("embedded provider substitution denied")
			}
			for _, provider := range ai.GetProviders() {
				for _, builtIn := range ai.GetModels(provider) {
					if resolved.Api == builtIn.Api {
						return 0, errors.New("embedded provider substitution denied")
					}
				}
			}
			injectedStreamModel = resolved
			if model != nil {
				copy := *model
				copy.ContextWindow, copy.MaxTokens = resolved.ContextWindow, resolved.MaxTokens
				model = &copy
			} else {
				model = resolved
			}
		}
	}
	if model == nil {
		return 0, errors.New("embedded model is unavailable")
	}
	if model.Provider == "openai-codex" {
		if err := validateEmbeddedCodexModel(model); err != nil {
			return 0, err
		}
	}
	p = &embeddedPersistence{db: m.store, binding: binding, executionToken: executionToken, claudeRestartGeneration: turnRestartGeneration, digest: "genesis", source: source, msgID: msgID, afterUserAppend: m.embeddedAfterUserAppend, publishComplete: m.publishCompleteHistory, flushReplies: func(agentID string) { m.flushEmbeddedReplies(ctx, agentID) }, toolInputs: make(map[string]any), toolCompanionTexts: make(map[string]string)}
	if wake != nil {
		if err := p.checkClaimedWake(ctx, *wake); err != nil {
			return 0, err
		}
	}
	authority, err := m.newEmbeddedTurnToolAuthority(ctx, owner, p, saved, explicitPrompt == "" && mail == nil && continuation == nil && wake == nil)
	if err != nil {
		return 0, err
	}
	tools, role := authority.Tools(), authority.role
	lane := m.laneFor(binding.AgentID)
	var appliedReceipt *store.EmbeddedGoalAppliedReceipt
	// The SDK invokes StreamFn only when it has selected a provider request.
	// MessageEnd persistence has already checked the initiating user and any
	// goal tool result. Never mark a desired revision applied at goal set time.
	stream := func(requestCtx context.Context, requestModel *ai.Model, transcript ai.TranscriptContext, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		fail := func(err error) *ai.AssistantMessageEventStream { return ai.ErrorStream(requestModel, err) }
		if lane.embeddedResetPending() {
			return fail(&store.ManagedAgentNotReadyError{AgentID: binding.AgentID})
		}
		if p.noticeStart {
			if err := m.store.StartEmbeddedInterruptedNotice(requestCtx, binding, *p.notice); err != nil {
				return fail(err)
			}
			p.noticeStart = false
		}
		if err := m.store.RenewEmbeddedOwner(requestCtx, binding, owner.identity, embeddedOwnerTTL); err != nil {
			return fail(err)
		}
		auxiliary, _ := requestCtx.Value(embeddedAuxiliaryRequestKey{}).(bool)
		if !auxiliary && p.turnSeq > 0 {
			if err := p.bindExecutionUser(requestCtx); err != nil {
				return fail(err)
			}
		}
		if !auxiliary && (requestModel == nil || requestModel.ContextWindow <= embeddedContextHeadroom || embeddedProjectedTokens(transcript.Messages) > requestModel.ContextWindow-embeddedContextHeadroom) {
			return fail(errors.New("provider request cannot fit fixed headroom"))
		}
		if !auxiliary && p.notice == nil && p.mailAttempt == "" && p.turnSeq != 0 {
			if err := m.store.EnterEmbeddedNonMailProviderTurn(requestCtx, binding, p.turnSeq, p.turnDigest); err != nil {
				return fail(err)
			}
		}
		aliasTrace("provider-entry", binding.AgentID, binding.SessionID, binding.Generation, "")
		policy, err := m.store.ManagedGoalPolicyFor(requestCtx, binding.AgentID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fail(err)
		}
		// Recovery awareness is not an ordinary goal turn. In particular an
		// interrupted Continue has no newly appended ordinary user and must not
		// apply a goal whose SDK result was never checked before the crash.
		if !auxiliary {
			appliedReceipt = nil // A newer pending/off revision cannot reuse prior evidence.
		}
		if !auxiliary && wake == nil && p.notice == nil && err == nil && policy.DesiredRevision > 0 &&
			(policy.DesiredRevision > policy.AppliedRevision || (policy.Config.Enabled && policy.DesiredRevision == policy.AppliedRevision && policy.ProjectionState == "applied")) {
			if p.turnSeq == 0 || p.turnDigest == "" {
				return fail(errors.New("embedded goal request has no checked user entry"))
			}
			key := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d:%s:%d:%d", binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation, p.turnSeq, policy.DesiredRevision))))
			receipt, _, receiptErr := m.store.CommitEmbeddedGoalAppliedReceipt(requestCtx, binding, store.EmbeddedGoalAppliedReceiptRequest{
				TurnKey: key, TurnEntrySeq: p.turnSeq, TurnEntryDigest: p.turnDigest, GoalRevision: policy.DesiredRevision,
			})
			if receiptErr != nil && !errors.Is(receiptErr, store.ErrEmbeddedGoalSDKResultUnchecked) {
				return fail(receiptErr)
			}
			if receiptErr == nil {
				appliedReceipt = &receipt
			}
			// An unappended SDK goal result stays pending. Ordinary work may
			// proceed, but cannot evaluate or replay that unresolved goal.
		}
		if injectedStreamModel != nil {
			// Only the explicit in-memory test resolver can install this double.
			// Production resolution has no generic-provider fallback.
			return ai.StreamSimple(requestCtx, injectedStreamModel, ai.Context{Messages: transcript.Messages}, opts)
		}
		if requestModel.Provider == "google" {
			return m.streamEmbeddedGoogle(requestCtx, requestModel, transcript, opts)
		}
		if requestModel.Provider == "google-vertex" {
			return m.streamEmbeddedVertex(requestCtx, requestModel, transcript, opts)
		}
		if requestModel.Provider != "openai-codex" {
			return ai.StreamSimple(requestCtx, requestModel, ai.Context{Messages: transcript.Messages}, opts)
		}
		if err := validateEmbeddedCodexModel(requestModel); err != nil {
			return fail(err)
		}
		if m.codexCredentialStore == nil {
			return fail(errors.New("embedded Codex credential store unavailable"))
		}
		{
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
			// The first provider request records the exact bearer it used. The
			// wrapper may discard only a typed pre-output 401 before the SDK
			// sees an event or persists a terminal assistant.
			used := ""
			opts.CodexAuth = func(authCtx context.Context) (ai.ModelAuth, error) {
				auth, authErr := m.embeddedCodexAuth(authCtx)
				if authErr == nil {
					used = auth.APIKey
				}
				return auth, authErr
			}
			if opts.APIKey != "" || len(opts.Headers) != 0 {
				return fail(errors.New("embedded Codex auth override denied"))
			}
			opts.HTTPClient = embeddedOwnerSendDoer{store: m.store, binding: binding, identity: owner.identity, inner: embeddedCodexTerminalDoer{newEmbeddedCodexClient(m.codexHTTPTransport)}, attempts: &atomic.Int32{}, observe: m.embeddedStatusObserver, terminal: func() { aliasTrace("codex-terminal", binding.AgentID, binding.SessionID, binding.Generation, "") }}
			return m.streamEmbeddedCodexWith401Rotation(requestCtx, requestModel, transcript, opts, &used)
		}
	}
	maxTokens := embeddedOrdinaryMaxTokens
	if model.MaxTokens > 0 && model.MaxTokens < maxTokens {
		maxTokens = model.MaxTokens
	}
	activeWorkflowState := authority.workflowState
	workerPrompt := buildEmbeddedEngineSystemPrompt(role, binding.AgentID, saved.Cwd, saved.SelectedModel.Provider == claudeCodeProvider)
	if activeWorkflowState != nil {
		wfVars := saved.WorkflowVariables
		if guidance := formatWorkflowStateGuidance(ctx, m.store, activeWorkflowState, wfVars); guidance != "" {
			workerPrompt = workerPrompt + "\n\n" + guidance
		}
	}
	if saved.SelectedModel.Provider != claudeCodeProvider {
		session = coding.NewSession(coding.SessionOptions{SessionID: binding.SessionID, Model: model, Cwd: saved.Cwd, Tools: tools, SystemPrompt: workerPrompt, OrderedPersistence: p, StreamFn: stream, MaxTokens: &maxTokens,
			ReplayToolResult: func(callCtx context.Context, call agent.BeforeToolCallContext) (*ai.ToolResultMessage, error) {
				return nil, nil
			}, BeforeToolCall: func(callCtx context.Context, call agent.BeforeToolCallContext) *agent.BeforeToolCallResult {
				if err := admitWorkflowStateTool(callCtx, m.store, binding.AgentID, call.ToolCall.Name); err != nil {
					return &agent.BeforeToolCallResult{
						Block:  true,
						Reason: err.Error(),
					}
				}
				toolName := call.ToolCall.Name
				normalizedToolName, isInlineTool := normalizeInlineActionTool(toolName)
				if isInlineTool {
					if m.IsInlineToolsAllowed(callCtx, binding.AgentID) {
						return nil
					}
					// Check if mecha_request_inline_tool_allowance is available to the agent.
					// If it is NOT available, do not block inline tools.
					lookupTools := tools
					if currentLane := m.laneFor(binding.AgentID); currentLane != nil {
						if dynTools := currentLane.getActiveTools(); len(dynTools) > 0 {
							lookupTools = dynTools
						} else if dynBase := currentLane.getBaseTools(); len(dynBase) > 0 {
							lookupTools = dynBase
						}
					}
					hasAllowanceTool := false
					for _, t := range lookupTools {
						if t.Name == InlineToolAllowanceToolName {
							hasAllowanceTool = true
							break
						}
					}
					if !hasAllowanceTool {
						return nil
					}

					// Otherwise, check allowance credits
					lane := m.laneFor(binding.AgentID)
					if lane != nil && lane.consumeInlineAllowance(normalizedToolName, time.Now()) {
						return nil
					}
					return &agent.BeforeToolCallResult{
						Block:  true,
						Reason: InlineToolBlockReason,
					}
				}
				return nil
			}, BeforeToolExecute: authority.BeforeToolExecute})
		runtime = &piEmbeddedRuntime{Session: session}
	} else {
		claude, buildErr := m.buildClaudeCodeRuntime(ctx, saved, owner, p, authority, workerPrompt, func(receipt *store.EmbeddedGoalAppliedReceipt) { appliedReceipt = receipt })
		if buildErr != nil {
			return 0, buildErr
		}
		runtime = claude
	}

	if m.embeddedRuntimeTestWrapper != nil {
		runtime = m.embeddedRuntimeTestWrapper(runtime, authority)
	}
	lane = m.laneFor(saved.AgentID)
	if !lane.installEmbeddedRuntime(runtime, p) {
		runtime.Close()
		return 0, &store.ManagedAgentNotReadyError{AgentID: binding.AgentID}
	}
	defer lane.removeEmbeddedRuntime(runtime)
	defer func() {
		var closeErr error
		if claude, ok := runtime.(*claudeCodeRuntime); ok {
			claudeCloseOnce.Do(func() { claudeCloseErr = claude.closeScoped() })
			closeErr = claudeCloseErr
		} else {
			runtime.Close()
		}
		settleErr := settleExecution()
		turnErr = errors.Join(turnErr, closeErr, settleErr)
		m.finishToolContextReset(context.Background(), binding, closeErr == nil && settleErr == nil)
	}()
	m.activeSessionsMu.Lock()
	m.activeEmbeddedSessions[saved.AgentID] = runtime
	m.activeSessionsMu.Unlock()
	defer func() {
		m.activeSessionsMu.Lock()
		if m.activeEmbeddedSessions[saved.AgentID] == runtime {
			delete(m.activeEmbeddedSessions, saved.AgentID)
		}
		m.activeSessionsMu.Unlock()
	}()
	if session != nil {
		if err := p.restore(ctx, session); err != nil {
			return 0, err
		}
	}
	var recoveryNotice *store.EmbeddedInterruptedNotice
	var resumeUnentered bool
	if wake == nil && mail == nil && explicitPrompt == "" && continuation == nil {
		snap, err := m.store.ReopenEmbedded(ctx, binding)
		if err != nil {
			return 0, err
		}
		for i := len(snap.Entries) - 1; i >= 0; i-- {
			if snap.Entries[i].Kind != "user" {
				continue
			}
			recoveryNotice, err = m.store.PrepareEmbeddedInterruptedNotice(ctx, binding, snap.Entries[i].Sequence)
			if err != nil {
				return 0, err
			}
			if recoveryNotice != nil {
				break
			}
			// Only the latest checked user can be resumed. A provider-entry
			// marker makes its outcome unknown, never retryable.
			if i == len(snap.Entries)-1 {
				resumeUnentered, err = m.store.EmbeddedUnenteredUser(ctx, binding, snap.Entries[i].Sequence, snap.Entries[i].Digest)
				if err != nil {
					return 0, err
				}
			}
			break
		}
	}
	// The SDK transform runs on the serial provider lane after AppendMessage
	// persisted the checked tool result, including on tool continuations.
	if session != nil {
		session.Agent.TransformContext = func(requestCtx context.Context, messages []agent.AgentMessage) []agent.AgentMessage {
			view, err := m.fitEmbeddedContext(requestCtx, p, owner, model, stream, messages)
			if err != nil {
				// The SDK transform has no error return. Its ordered run boundary
				// converts this into a failed turn before ordinary provider entry.
				panic(err)
			}
			return view
		}
	}
	hasUser := runtime.Snapshot().HasUser
	handleTurnErr := func(err error) error {
		if err != nil {
			turnErr = err
			if errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "context canceled") {
				_ = m.store.ResetEmbeddedAgentInterrupt(context.Background(), binding.AgentID)
				m.publishProjection()
				m.OfferDirect(binding.AgentID)
			}
		}
		return err
	}
	if wake != nil && len(wakePrompts) == 0 {
		// Continue an exact claimed interrupted wake, not an inferred fresh turn.
		snapshot, err := m.store.ReopenEmbedded(ctx, binding)
		if err != nil {
			return 0, err
		}
		for i := len(snapshot.Entries) - 1; i >= 0; i-- {
			if snapshot.Entries[i].Kind == "user" {
				p.turnSeq, p.turnDigest = snapshot.Entries[i].Sequence, snapshot.Entries[i].Digest
				break
			}
		}
		if err := p.bindExecutionUser(ctx); err != nil {
			return 0, err
		}
	}
	if wake != nil {
		// ClaimEmbeddedRestartWake already starts an interrupted 076 notice.
		// Do not enter the ordinary recovery path, which starts it again.
		var beforeComplete func() error
		if claude, ok := runtime.(*claudeCodeRuntime); ok {
			beforeComplete = func() error {
				claudeCloseOnce.Do(func() { claudeCloseErr = claude.closeScoped() })
				return claudeCloseErr
			}
		}
		if err := handleTurnErr(p.runClaimedWake(ctx, runtime, *wake, wakePrompts, beforeComplete)); err != nil {
			return 0, err
		}
		return p.seq, nil
	}
	if recoveryNotice != nil && recoveryNotice.State != "started" {
		p.notice = recoveryNotice
		if recoveryNotice.State == "appended" {
			p.turnSeq, p.turnDigest = p.seq, p.digest
			if err := p.bindExecutionUser(ctx); err != nil {
				return 0, err
			}
			p.noticeStart = true
			if err := handleTurnErr(runtime.Continue(ctx)); err != nil {
				return 0, err
			}
			return p.seq, nil
		}
		message := ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: recoveryNotice.Text}}}
		var err error
		p.noticeMessage, err = json.Marshal(message)
		if err != nil {
			return 0, err
		}
		_, err = runtime.RunMessages(ctx, []agent.AgentMessage{message})
		if err := handleTurnErr(err); err != nil {
			return 0, err
		}
		return p.seq, nil
	}
	if resumeUnentered {
		p.turnSeq, p.turnDigest = p.seq, p.digest
		// Continue the restored checked user, never append a duplicate user.
		// StreamFn fences and commits provider entry before dispatch.
		if err := runtime.Continue(ctx); err != nil {
			return 0, handleTurnErr(err)
		}
		if generation, ok := ctx.Value(embeddedOriginalRestartGenerationKey{}).(int64); ok {
			_, err := m.store.ScheduleEmbeddedSettledOriginalWake(ctx, binding, generation, p.turnSeq, p.turnDigest)
			if err != nil {
				return 0, handleTurnErr(err)
			}
		}
		return p.seq, nil
	}
	// The caller holds lane.serial. Reset incoming work before execution so
	// its own empty final remains the first counted turn, not erased on return.
	automatic, _ := ctx.Value(embeddedAutomaticGoalKey{}).(bool)
	if !automatic && continuation == nil && (explicitPrompt != "" || mail != nil) {
		lane := m.laneFor(binding.AgentID)
		lane.consecutiveContinuations.Store(0)
		lane.consecutiveEmptyTurns.Store(0)
		lane.goalAttempt.Store(0)
		lane.goalNextAttempt = time.Time{}
	}
	if continuation != nil {
		promptText := m.formatGoalContinuationPrompt(ctx, binding, continuation)
		if promptText == "" {
			return 0, handleTurnErr(errors.New("goal continuation prompt unavailable"))
		}
		if explicitPrompt != "" {
			promptText = explicitPrompt
		}
		runRes, err := runtime.RunMessages(ctx, []agent.AgentMessage{ai.UserMessage{
			Content: ai.ContentList{ai.TextContent{Text: promptText}},
		}})
		if err != nil {
			if (runRes != nil && runRes.StopReason == ai.StopError) || p.lastStopReason == ai.StopError {
				m.nudgeEmbeddedSessionIfNeeded(ctx, runtime, p)
				if err := settleExecution(); err != nil {
					return 0, err
				}
				m.checkContextResetAfterTurn(ctx, binding, model, runtime)
				return p.seq, nil
			}
			return 0, handleTurnErr(err)
		}
		m.nudgeEmbeddedSessionIfNeeded(ctx, runtime, p)
		if err := m.evaluateEmbeddedTurn(ctx, binding, p, appliedReceipt, model, stream); err != nil {
			return 0, handleTurnErr(err)
		}
		if err := settleExecution(); err != nil {
			return 0, err
		}
		m.checkContextResetAfterTurn(ctx, binding, model, runtime)
		return p.seq, nil
	}
	// A failed system declaration can leave only a system entry; the first
	// user prompt is still due. Once a user is durable, never silently
	// reissue a provider request after an ambiguous interruption.
	if mail != nil {
		if mail.Binding.AgentID != binding.AgentID || mail.Binding.SessionID != binding.SessionID || mail.Binding.Epoch != binding.Epoch || mail.Binding.Generation != binding.Generation {
			return 0, errors.New("embedded mail owner mismatch")
		}
		message, err := store.EmbeddedMailUserMessage(mail, m.clock.Now().UnixMilli())
		if err != nil {
			return 0, err
		}
		p.mailMessage, err = json.Marshal(message)
		if err != nil {
			return 0, err
		}
		p.mail = mail
		runRes, err := runtime.RunMessages(ctx, []agent.AgentMessage{message})
		// The SDK may turn a rejected checked append into a local synthetic
		// error. Preserve the Store's transaction outcome, not its error text.
		if p.mailAppendErr != nil {
			return 0, handleTurnErr(p.mailAppendErr)
		}
		if err != nil {
			if (runRes != nil && runRes.StopReason == ai.StopError) || p.lastStopReason == ai.StopError {
				m.nudgeEmbeddedSessionIfNeeded(ctx, runtime, p)
				if err := settleExecution(); err != nil {
					return 0, err
				}
				m.checkContextResetAfterTurn(ctx, binding, model, runtime)
				return p.seq, nil
			}
			return 0, handleTurnErr(err)
		}
		m.nudgeEmbeddedSessionIfNeeded(ctx, runtime, p)
		if err := m.evaluateEmbeddedTurn(ctx, binding, p, appliedReceipt, model, stream); err != nil {
			return 0, handleTurnErr(err)
		}
		if err := settleExecution(); err != nil {
			return 0, err
		}
		m.checkContextResetAfterTurn(ctx, binding, model, runtime)
		return p.seq, nil
	}
	if explicitPrompt != "" {
		promptToRun := explicitPrompt
		if role != "root" && !hasUser && saved.PendingInitialInstruction != "" && explicitPrompt == saved.PendingInitialInstruction {
			promptToRun = formatGenesisPrompt(saved.AgentID, explicitPrompt)
		}
		runRes, err := runtime.Run(ctx, promptToRun)
		if err != nil {
			if (runRes != nil && runRes.StopReason == ai.StopError) || p.lastStopReason == ai.StopError {
				m.nudgeEmbeddedSessionIfNeeded(ctx, runtime, p)
				// Claude reports fresh usage on the wake itself. Settle before monitoring
				// while the reset coordinator still owns the fresh-binding guard.
				if explicitPrompt != ContextResetWakePrompt || model.Provider == claudeCodeProvider {
					if err := settleExecution(); err != nil {
						return 0, err
					}
					m.checkContextResetAfterTurn(ctx, binding, model, runtime)
				}
				return p.seq, nil
			}
			return 0, handleTurnErr(err)
		}
		m.nudgeEmbeddedSessionIfNeeded(ctx, runtime, p)
		if err := m.evaluateEmbeddedTurn(ctx, binding, p, appliedReceipt, model, stream); err != nil {
			return 0, handleTurnErr(err)
		}
		if explicitPrompt != ContextResetWakePrompt || model.Provider == claudeCodeProvider {
			if err := settleExecution(); err != nil {
				return 0, err
			}
			m.checkContextResetAfterTurn(ctx, binding, model, runtime)
		}
		return p.seq, nil
	}
	if !hasUser && saved.PendingInitialInstruction != "" {
		instruction := saved.PendingInitialInstruction
		if role != "root" {
			instruction = formatGenesisPrompt(saved.AgentID, saved.PendingInitialInstruction)
		}
		runRes, err := runtime.Run(ctx, instruction)
		if err != nil {
			if (runRes != nil && runRes.StopReason == ai.StopError) || p.lastStopReason == ai.StopError {
				m.nudgeEmbeddedSessionIfNeeded(ctx, runtime, p)
				if err := settleExecution(); err != nil {
					return 0, err
				}
				m.checkContextResetAfterTurn(ctx, binding, model, runtime)
				return p.seq, nil
			}
			return 0, handleTurnErr(err)
		}
		m.nudgeEmbeddedSessionIfNeeded(ctx, runtime, p)
		if err := m.evaluateEmbeddedTurn(ctx, binding, p, appliedReceipt, model, stream); err != nil {
			return 0, handleTurnErr(err)
		}
		if err := settleExecution(); err != nil {
			return 0, err
		}
		m.checkContextResetAfterTurn(ctx, binding, model, runtime)
		return p.seq, nil
	}
	if err := settleExecution(); err != nil {
		return 0, err
	}
	m.checkContextResetAfterTurn(ctx, binding, model, runtime)
	return p.seq, nil
}

// evaluateEmbeddedTurn runs only after a successful SDK turn. The receipt is
// captured at the provider-request boundary, never inferred from desired state.
func (m *Manager) evaluateEmbeddedTurn(ctx context.Context, binding store.EmbeddedBinding, p *embeddedPersistence, receipt *store.EmbeddedGoalAppliedReceipt, model *ai.Model, stream agent.StreamFn) error {
	if m.isWorkerSelfTerminationPending(binding.AgentID) {
		return nil
	}
	curAgent, err := m.store.ManagedAgent(ctx, binding.AgentID)
	if err != nil {
		return err
	}
	hasActiveWorkflow := curAgent != nil && (curAgent.ActiveWorkflowID != "" || curAgent.ActiveWorkflowStateID != "")
	if curAgent != nil && curAgent.Blocked {
		log.Printf("[embedded:%s] Suppressing goal evaluation because agent is blocked: %s", binding.AgentID, curAgent.BlockedReason)
		return nil
	}
	if receipt == nil || !receipt.GoalEnabled ||
		receipt.TurnEntrySeq != p.turnSeq || receipt.TurnEntryDigest != p.turnDigest {
		return nil
	}

	claudeEvaluation := model != nil && model.Provider == claudeCodeProvider
	claudeRestartGeneration := p.claudeRestartGeneration
	policy, err := m.store.ManagedGoalPolicyFor(ctx, binding.AgentID)
	if err != nil {
		return err
	}
	if !policy.Config.Enabled || policy.DesiredRevision != receipt.GoalRevision ||
		policy.AppliedRevision != receipt.GoalRevision || policy.ProjectionState != "applied" {
		return nil
	}
	m.subagentsMu.Lock()
	hasActiveSubagents := false
	for _, task := range m.subagentsActive {
		if task.parent.AgentID == binding.AgentID {
			task.mu.Lock()
			st := task.status
			task.mu.Unlock()
			if st == "queued" || st == "running" {
				hasActiveSubagents = true
				break
			}
		}
	}
	m.subagentsMu.Unlock()
	if !hasActiveSubagents {
		// Also check store for active subagents
		subagents, err := m.store.ManagedSubagents(ctx, binding)
		if err == nil {
			for _, s := range subagents {
				if s.Status == "queued" || s.Status == "running" {
					hasActiveSubagents = true
					break
				}
			}
		}
	}
	if hasActiveSubagents {
		// Do not evaluate goal while subagents are running. The parent is waiting on them.
		return nil
	}
	snapshot, err := m.store.ReopenEmbedded(ctx, binding)
	if err != nil {
		return err
	}
	if len(snapshot.Entries) == 0 {
		return nil
	}
	last := snapshot.Entries[len(snapshot.Entries)-1]
	if last.Sequence != p.seq || last.Digest != p.digest || last.Kind != "assistant" {
		return nil
	}
	message, err := ai.UnmarshalMessage(last.Body)
	if err != nil {
		return err
	}
	assistant, ok := message.(ai.AssistantMessage)
	if !ok || assistant.StopReason != ai.StopStop {
		return nil
	}
	toolCalls := 0
	hasText := false
	calledGoalSnooze := false
	for _, entry := range snapshot.Entries {
		if entry.Sequence < p.turnSeq {
			continue
		}
		msg, err := ai.UnmarshalMessage(entry.Body)
		if err != nil {
			if entry.Kind == "tool_result" || entry.Kind == "toolResult" || entry.Kind == string(ai.RoleToolResult) {
				toolCalls++
			}
			continue
		}
		switch m := msg.(type) {
		case ai.ToolResultMessage:
			if m.ToolName != "goal" && m.ToolName != "goal_snooze" && m.ToolName != "workflow_snooze" {
				toolCalls++
			}
			if !m.IsError && (m.ToolName == "goal_snooze" || m.ToolName == "workflow_snooze") {
				calledGoalSnooze = true
			}
		case *ai.ToolResultMessage:
			if m != nil && m.ToolName != "goal" && m.ToolName != "goal_snooze" && m.ToolName != "workflow_snooze" {
				toolCalls++
			}
			if m != nil && !m.IsError && (m.ToolName == "goal_snooze" || m.ToolName == "workflow_snooze") {
				calledGoalSnooze = true
			}
		case ai.AssistantMessage:
			for _, part := range m.Content {
				switch c := part.(type) {
				case ai.ToolCall:
					if c.Name == "goal_snooze" {
						calledGoalSnooze = true
					}
					if c.Name != "goal" && c.Name != "goal_snooze" && c.Name != "workflow_snooze" {
						toolCalls++
					}
				case *ai.ToolCall:
					if c != nil && c.Name == "goal_snooze" {
						calledGoalSnooze = true
					}
					if c != nil && c.Name != "goal" && c.Name != "goal_snooze" && c.Name != "workflow_snooze" {
						toolCalls++
					}
				case ai.TextContent:
					t := strings.TrimSpace(c.Text)
					if t != "" && t != "(empty response)" {
						hasText = true
					}
				case *ai.TextContent:
					if c != nil {
						t := strings.TrimSpace(c.Text)
						if t != "" && t != "(empty response)" {
							hasText = true
						}
					}
				case ai.ThinkingContent:
					if strings.TrimSpace(c.Thinking) != "" {
						hasText = true
					}
				case *ai.ThinkingContent:
					if c != nil && strings.TrimSpace(c.Thinking) != "" {
						hasText = true
					}
				}
			}
		case *ai.AssistantMessage:
			if m != nil {
				for _, part := range m.Content {
					switch c := part.(type) {
					case ai.ToolCall:
						if c.Name != "goal" && c.Name != "goal_snooze" && c.Name != "workflow_snooze" {
							toolCalls++
						}
					case *ai.ToolCall:
						if c != nil && c.Name != "goal" && c.Name != "goal_snooze" && c.Name != "workflow_snooze" {
							toolCalls++
						}
					case ai.TextContent:
						t := strings.TrimSpace(c.Text)
						if t != "" && t != "(empty response)" {
							hasText = true
						}
					case *ai.TextContent:
						if c != nil {
							t := strings.TrimSpace(c.Text)
							if t != "" && t != "(empty response)" {
								hasText = true
							}
						}
					case ai.ThinkingContent:
						if strings.TrimSpace(c.Thinking) != "" {
							hasText = true
						}
					case *ai.ThinkingContent:
						if c != nil && strings.TrimSpace(c.Thinking) != "" {
							hasText = true
						}
					}
				}
			}
		}
	}
	lane := m.laneFor(binding.AgentID)
	// Serialize foreground accounting and claims with accepted Claude restarts.
	// The caller already owns serial; restart never waits on serial under selection.
	if claudeEvaluation {
		lane.selection.Lock()
		defer lane.selection.Unlock()
		if lane.claudeRestartGeneration.Load() != claudeRestartGeneration {
			return nil
		}
	}
	if !hasActiveWorkflow && toolCalls == 0 && !hasText && !calledGoalSnooze {
		lane.consecutiveEmptyTurns.Add(1)
		if lane.consecutiveEmptyTurns.Load() < 3 {
			log.Printf("[embedded:%s] Empty response on turn %d (count %d/3); re-nudging with actionable prompt", binding.AgentID, p.turnSeq, lane.consecutiveEmptyTurns.Load())
			incrementGoalAttempt(&lane.goalAttempt)
			goal := policy.Config.Goal
			nudge := goalContinuationPrompt(goal, int(lane.goalAttempt.Load()))
			continuationPrompt := "You emitted an empty response without taking action. You must take concrete action now on this goal: " + goal + ". Execute a bash command, read a file, or edit code to make progress.\n" + nudge

			key := store.EmbeddedGoalEvaluationKey{UserSeq: p.turnSeq, UserDigest: p.turnDigest,
				GoalRevision: receipt.GoalRevision, FinalSeq: last.Sequence, FinalDigest: last.Digest}
			_, claimed, err := m.store.ClaimEmbeddedGoalEvaluation(ctx, binding, key)
			if err != nil {
				return err
			}
			if !claimed {
				return nil
			}
			m.startManagedTask(func() {
				lane := m.laneFor(binding.AgentID)
				lane.serial.Lock()
				if claudeEvaluation {
					lane.selection.Lock()
					if lane.claudeRestartGeneration.Load() != claudeRestartGeneration {
						lane.selection.Unlock()
						lane.serial.Unlock()
						return
					}
				}
				token, enterErr := m.store.EnterEmbeddedGoalEvaluationExecution(m.runCtx, binding, key)
				if enterErr != nil {
					if claudeEvaluation {
						lane.selection.Unlock()
					}
					lane.serial.Unlock()
					return
				}
				outcome := store.EmbeddedGoalEvaluationNotDone
				reason := "Empty response without action"
				_, changed, evalErr := m.store.CompleteEmbeddedGoalEvaluation(m.runCtx, binding, key, outcome, reason)
				if evalErr == nil {
					m.recordGoalEvaluationActivity(binding, key, outcome, reason, reason)
				}
				clearErr := m.store.ClearEmbeddedExecution(context.Background(), binding, token)
				if clearErr != nil {
					log.Printf("[embedded:%s] clear evaluator execution: %v", binding.AgentID, clearErr)
				}
				if claudeEvaluation {
					lane.selection.Unlock()
				}
				lane.serial.Unlock()
				if evalErr != nil || clearErr != nil || !changed {
					m.publishProjection()
					return
				}
				m.startEmbeddedGoalContinuationWithPrompt(binding, key, continuationPrompt)
			})
			return nil
		}
		log.Printf("GOAL CIRCUIT BREAKER: agent %s emitted 3 consecutive empty responses; pausing continuation loop", binding.AgentID)
		return nil
	}
	lane.consecutiveEmptyTurns.Store(0)
	key := store.EmbeddedGoalEvaluationKey{UserSeq: p.turnSeq, UserDigest: p.turnDigest,
		GoalRevision: receipt.GoalRevision, FinalSeq: last.Sequence, FinalDigest: last.Digest}
	_, claimed, err := m.store.ClaimEmbeddedGoalEvaluation(ctx, binding, key)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	} // UNKNOWN is not replay permission.
	// The initiating turn (including mail reply publication) must not wait for
	// evaluation or its provider continuation. The durable claim is already
	// committed; a failed or interrupted task stays UNKNOWN.
	m.startManagedTask(func() {
		// Wait for the initiating lane (including launch readiness) to settle.
		// Evaluation cannot overlap a newer ordinary SDK turn on this agent.
		lane := m.laneFor(binding.AgentID)
		lane.serial.Lock()
		if claudeEvaluation {
			lane.selection.Lock()
			if lane.claudeRestartGeneration.Load() != claudeRestartGeneration {
				lane.selection.Unlock()
				lane.serial.Unlock()
				return
			}
		}
		token, enterErr := m.store.EnterEmbeddedGoalEvaluationExecution(m.runCtx, binding, key)
		if enterErr != nil {
			if claudeEvaluation {
				lane.selection.Unlock()
			}
			lane.serial.Unlock()
			m.publishProjection()
			return
		}
		if claudeEvaluation {
			lane.selection.Unlock()
			// Re-check the revision before recording the settled verdict.
			currentPolicy, policyErr := m.store.ManagedGoalPolicyFor(m.runCtx, binding.AgentID)
			if policyErr != nil || currentPolicy == nil || currentPolicy.DesiredRevision != key.GoalRevision || !currentPolicy.Config.Enabled {
				_ = m.store.ClearEmbeddedExecution(context.Background(), binding, token)
				lane.serial.Unlock()
				m.publishProjection()
				return
			}
		}
		var outcome, reason, rawDetails string
		var evalErr error
		curAgent, curAgentErr := m.store.ManagedAgent(m.runCtx, binding.AgentID)
		if curAgentErr != nil {
			log.Printf("[embedded:%s] read managed agent after goal evaluation claim (user_seq=%d, goal_revision=%d, final_seq=%d): %v; leaving UNKNOWN without replay", binding.AgentID, key.UserSeq, key.GoalRevision, key.FinalSeq, curAgentErr)
			if clearErr := m.store.ClearEmbeddedExecution(context.Background(), binding, token); clearErr != nil {
				log.Printf("[embedded:%s] clear evaluator execution: %v", binding.AgentID, clearErr)
			}
			lane.serial.Unlock()
			m.publishProjection()
			return
		}
		hasActiveWorkflow := curAgent != nil && (curAgent.ActiveWorkflowID != "" || curAgent.ActiveWorkflowStateID != "")

		if hasActiveWorkflow {
			outcome = store.EmbeddedGoalEvaluationNotDone
			reason = m.workflowContinuationGuidance(m.runCtx, curAgent)
			// The exact guidance is external content, not the bounded store reason.
			rawDetails = reason
			reason = "Active workflow requires progress or workflow_snooze."
		} else {
			// A settled turn with an active goal always continues. The agent
			// itself decides completion (goal off) or blocking (mecha_blocked).
			outcome = store.EmbeddedGoalEvaluationNotDone
			reason = embeddedAutomaticContinuationReason
		}
		if m.embeddedBeforeGoalCommit != nil {
			m.embeddedBeforeGoalCommit()
		}
		// A successful restart reservation and verdict settlement share this
		// boundary. Never wait for serial while holding selection here.
		if claudeEvaluation {
			lane.selection.Lock()
		}
		if evalErr == nil {
			piReviewBarrier(binding.AgentID, key.GoalRevision, outcome)
			defer piReviewDecisionComplete(binding.AgentID, key.GoalRevision)
		}
		if m.runCtx.Err() != nil || (claudeEvaluation && lane.claudeRestartGeneration.Load() != claudeRestartGeneration) {
			if claudeEvaluation {
				lane.selection.Unlock()
			}
			_ = m.store.ClearEmbeddedExecution(context.Background(), binding, token)
			lane.serial.Unlock()
			m.publishProjection()
			return
		}
		changed := false
		if m.runCtx.Err() == nil {
			_, changed, evalErr = m.store.CompleteEmbeddedGoalEvaluation(m.runCtx, binding, key, outcome, reason)
		}
		if evalErr == nil && (!claudeEvaluation || changed) && (outcome == store.EmbeddedGoalEvaluationDone || outcome == store.EmbeddedGoalEvaluationCompleted || outcome == store.EmbeddedGoalEvaluationNotDone) {
			m.recordGoalEvaluationActivity(binding, key, outcome, reason, rawDetails)
		}
		if !claudeEvaluation || (evalErr == nil && changed) {
			if outcome == store.EmbeddedGoalEvaluationDone || outcome == store.EmbeddedGoalEvaluationCompleted {
				lane.consecutiveContinuations.Store(0)
				lane.goalAttempt.Store(0)
			} else if outcome == store.EmbeddedGoalEvaluationNotDone {
				incrementGoalAttempt(&lane.goalAttempt)
			}
		}
		clearErr := m.store.ClearEmbeddedExecution(context.Background(), binding, token)
		if clearErr != nil {
			log.Printf("[embedded:%s] clear evaluator execution: %v", binding.AgentID, clearErr)
		}
		if claudeEvaluation {
			lane.selection.Unlock()
		}
		lane.serial.Unlock()
		if evalErr != nil || clearErr != nil || !changed || outcome != store.EmbeddedGoalEvaluationNotDone {
			m.publishProjection()
			return
		}
		m.dispatchEmbeddedGoalContinuation(binding, key)
	})
	return nil
}

// Notices inside an execution advance the append head, not its initiating user.
func (p *embeddedPersistence) appendInExecutionNotice(ctx context.Context, message ai.UserMessage) error {
	body, err := json.Marshal(message)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("root:%s:%s:%d:%s:%d", p.binding.AgentID, p.binding.SessionID, p.binding.Epoch, p.binding.Generation, p.seq+1)
	entry, err := p.db.AppendEmbeddedEntryAt(ctx, p.binding, p.seq, p.digest, key, "user", body)
	if err != nil {
		return err
	}
	p.seq, p.digest = entry.Sequence, entry.Digest
	p.projectMessage(ctx, message)
	return p.projectNotification(ctx, entry, claudeCodeMessageText(message))
}

// Notification uses the existing generic timeline activity card, not a user
// speech bubble or a mailbox envelope. No frontend component change is needed.
func (p *embeddedPersistence) projectNotification(ctx context.Context, entry store.EmbeddedEntry, text string) error {
	key := fmt.Sprintf("embedded-notification:%s:%d:%s", p.binding.AgentID, entry.Sequence, entry.Digest)
	history, err := p.db.RecordManagedCompleteHistoryWithDetails(ctx, p.binding.AgentID, "managed_notification", "info", "Background task finished", key, &store.ManagedHistoryPresentation{Kind: "lifecycle", Outcome: "success", Summary: "Background task finished"}, nil, &text)
	if err != nil {
		return err
	}
	if p.publishComplete != nil && history.ID != "" {
		p.publishComplete(history)
	}
	return nil
}
