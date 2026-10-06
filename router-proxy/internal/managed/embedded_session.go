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
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/coding"
)

// embeddedPersistence serializes one root's checked SDK callbacks in its
// Manager lane. SQLite owns the binding, append head, and checkpoint fence.
type embeddedPersistence struct {
	db                *store.Store
	binding           store.EmbeddedBinding
	seq               int64
	digest            string
	turnSeq           int64
	turnDigest        string
	mail              *store.EmbeddedMailAttempt
	mailMessage       []byte
	mailAttempt       string
	mailAppendErr     error
	notice            *store.EmbeddedInterruptedNotice
	noticeMessage     []byte
	noticeStart       bool
	wake              *embeddedClaimedWakeScope
	wakeUsed          bool
	wakeFinal         *store.EmbeddedEntry
	reminderResult    func(context.Context, ai.ToolResultMessage, bool) error
	bashResult        func(context.Context, ai.ToolResultMessage, bool) error
	goalResult        func(context.Context, ai.ToolResultMessage, bool) error
	goalInvocation    func(string) *store.EmbeddedGoalInvocation
	toolInvocation    func(string) *store.EmbeddedToolInvocation
	toolChecked       func(string)
	goalEntry         store.EmbeddedEntry
	compactionResult  func(ai.ToolResultMessage) (bool, error)
	pendingCompaction string
	afterUserAppend   func(context.Context)
	publishComplete   func(store.ManagedHistoryEntry)
	toolInputs        map[string]any
	currentThinking   string
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

func (p *embeddedPersistence) AppendMessage(ctx context.Context, message agent.AgentMessage) error {
	if message.MessageRole() == ai.RoleUser {
		p.currentThinking = ""
	}
	var assistantContent ai.ContentList
	switch assistant := message.(type) {
	case ai.AssistantMessage:
		assistantContent = assistant.Content
	case *ai.AssistantMessage:
		if assistant != nil {
			assistantContent = assistant.Content
		}
	}
	for _, part := range assistantContent {
		if th, ok := part.(ai.ThinkingContent); ok && th.Thinking != "" {
			p.currentThinking += th.Thinking
		} else if th, ok := part.(*ai.ThinkingContent); ok && th != nil && th.Thinking != "" {
			p.currentThinking += th.Thinking
		}
		if call, ok := part.(ai.ToolCall); ok && call.ID != "" {
			if p.toolInputs == nil {
				p.toolInputs = make(map[string]any)
			}
			p.toolInputs[call.ID] = call.Arguments
		} else if call, ok := part.(*ai.ToolCall); ok && call != nil && call.ID != "" {
			if p.toolInputs == nil {
				p.toolInputs = make(map[string]any)
			}
			p.toolInputs[call.ID] = call.Arguments
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
			thinkingKey := fmt.Sprintf("thinking:%s:%x", p.binding.AgentID, sha256.Sum256([]byte(p.currentThinking)))
			pres := &store.ManagedHistoryPresentation{
				Kind:     "thinking",
				Outcome:  "success",
				Summary:  p.currentThinking,
				Thinking: p.currentThinking,
			}
			thinkingCopy := p.currentThinking
			entry, err := p.db.RecordManagedCompleteHistoryWithDetails(ctx, p.binding.AgentID, "managed_thinking", "info", "Thinking Process", thinkingKey, pres, nil, &thinkingCopy)
			if err == nil && p.publishComplete != nil {
				p.publishComplete(entry)
			}
			p.currentThinking = ""
		}
	}
	if result, ok := message.(ai.ToolResultMessage); ok {
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
			level := "info"
			messageText := "Ran " + toolName
			pres := &store.ManagedHistoryPresentation{
				Kind:     "tool",
				ToolName: toolName,
				Outcome:  "success",
				Summary:  "Completed successfully",
				Thinking: p.currentThinking,
			}
			if isFailed {
				level = "error"
				displayName := toolName
				if len(displayName) > 0 {
					displayName = strings.ToUpper(displayName[:1]) + displayName[1:]
				}
				messageText = displayName + " failed"
				pres = &store.ManagedHistoryPresentation{
					Kind:     "tool",
					ToolName: toolName,
					Outcome:  "failed",
					Summary:  "The operation could not be completed.",
					Error:    &store.ManagedHistoryError{Code: "TOOL_FAILED", Retryable: false, DetailAvailable: false},
					Thinking: p.currentThinking,
				}
			}
			if toolName == "mecha_mailbox_send" {
				if args, ok := toolInput.(map[string]any); ok {
					if recipient, ok := args["recipient"].(string); ok {
						pres.To = recipient
					}
					pres.From = p.binding.AgentID
				}
			} else if toolName == "mecha_worker" {
				if args, ok := toolInput.(map[string]any); ok && args["action"] == "send" {
					if recipient, ok := args["name"].(string); ok {
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
		if result.ToolName == "bash" && p.bashResult != nil {
			if err := p.bashResult(ctx, result, false); err != nil {
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
	if result, ok := message.(ai.ToolResultMessage); ok && (result.ToolName == "mecha_mailbox_send" || result.ToolName == "mecha_worker") && p.toolInvocation != nil && p.toolInvocation(result.ToolCallID) == nil {
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
				return errors.New("claimed wake final already appended")
			}
			key := fmt.Sprintf("root:%s:%s:%d:%s:%d", p.binding.AgentID, p.binding.SessionID, p.binding.Epoch, p.binding.Generation, p.seq+1)
			entry, err := p.db.AppendEmbeddedRestartWakeFinalAt(ctx, p.binding, p.wake.processGeneration, p.wake.id, p.seq, p.digest, key, body)
			if err == nil {
				p.seq, p.digest = entry.Sequence, entry.Digest
				p.wakeFinal = &entry
				p.traceChecked(message)
			}
			return err
		}
	}
	if p.mailAttempt != "" {
		final := false
		switch assistant := message.(type) {
		case ai.AssistantMessage:
			final = assistant.StopReason == ai.StopStop
		case *ai.AssistantMessage:
			final = assistant.StopReason == ai.StopStop
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
			}
			return err
		}
	}
	key := fmt.Sprintf("root:%s:%s:%d:%s:%d", p.binding.AgentID, p.binding.SessionID, p.binding.Epoch, p.binding.Generation, p.seq+1)
	var entry store.EmbeddedEntry
	if result, ok := message.(ai.ToolResultMessage); ok && p.toolInvocation != nil && p.toolInvocation(result.ToolCallID) != nil {
		entry, err = p.db.AppendEmbeddedToolInvocationResult(ctx, p.toolInvocation(result.ToolCallID), p.seq, p.digest, key, result, body)
	} else if result, ok := message.(ai.ToolResultMessage); ok && p.goalInvocation != nil && p.goalInvocation(result.ToolCallID) != nil {
		entry, err = p.db.AppendEmbeddedGoalInvocationResult(ctx, p.goalInvocation(result.ToolCallID), p.seq, p.digest, key, result, body)
	} else {
		entry, err = p.db.AppendEmbeddedEntryAt(ctx, p.binding, p.seq, p.digest, key, string(message.MessageRole()), body)
	}
	if err == nil {
		p.seq, p.digest = entry.Sequence, entry.Digest
		p.traceChecked(message)
		if result, ok := message.(ai.ToolResultMessage); ok && p.toolChecked != nil {
			p.toolChecked(result.ToolCallID)
		}

		if result, ok := message.(ai.ToolResultMessage); ok && result.ToolName == "mecha_reminder" && p.reminderResult != nil {
			if observeErr := p.reminderResult(ctx, result, true); observeErr != nil {
				return observeErr
			}
		}
		if result, ok := message.(ai.ToolResultMessage); ok && result.ToolName == "bash" && p.bashResult != nil {
			if observeErr := p.bashResult(ctx, result, true); observeErr != nil {
				return observeErr
			}
		}
		if result, ok := message.(ai.ToolResultMessage); ok && p.goalResult != nil {
			p.goalEntry = entry
			if observeErr := p.goalResult(ctx, result, true); observeErr != nil {
				return observeErr
			}
		}
		if result, ok := message.(ai.ToolResultMessage); ok && result.ToolName != "mecha_worker" && result.ToolName != "mecha_mailbox_send" {
			if result.ToolName != "mecha_reminder" && result.ToolName != "bash" && result.ToolName != "goal" && result.ToolName != "goal_snooze" {
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
// worker sessions get fixed router-owned read and worker launch tools; other
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

func (m *Manager) runEmbeddedMail(ctx context.Context, saved store.ManagedAgentRecord, attempt *store.EmbeddedMailAttempt) error {
	return m.runEmbeddedSessionTurn(ctx, saved, "", attempt)
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
	return m.runEmbeddedSessionTurnScoped(ctx, saved, explicitPrompt, mail, continuation, nil, nil)
}

// A caller supplies an already claimed wake and the exact prompt for one SDK
// invocation. This is not a scheduler: it never reserves, claims, or retries.
func (m *Manager) runEmbeddedClaimedWake(ctx context.Context, saved store.ManagedAgentRecord, scope embeddedClaimedWakeScope, prompts []agent.AgentMessage) error {
	return m.runEmbeddedSessionTurnScoped(ctx, saved, "", nil, nil, &scope, prompts)
}

func (m *Manager) runEmbeddedSessionTurnScoped(ctx context.Context, saved store.ManagedAgentRecord, explicitPrompt string, mail *store.EmbeddedMailAttempt, continuation *store.EmbeddedGoalEvaluationKey, wake *embeddedClaimedWakeScope, wakePrompts []agent.AgentMessage) error {
	owner, ok := ctx.Value(embeddedOwnerContextKey{}).(embeddedOwner)
	if !ok || owner.binding.AgentID != saved.AgentID || owner.binding.SessionID != saved.PiSessionID || owner.binding.Epoch != saved.SessionEpoch || owner.binding.Generation != saved.RuntimeGeneration {
		return errors.New("embedded runner requires claimed owner")
	}
	binding := owner.binding
	if err := m.store.RenewEmbeddedOwner(ctx, binding, owner.identity, embeddedOwnerTTL); err != nil {
		return err
	}
	var turnErr error
	_ = m.store.SetEmbeddedActivityState(ctx, binding, "active")
	m.publishProjection()
	defer func() {
		if errors.Is(turnErr, context.Canceled) || (turnErr != nil && strings.Contains(turnErr.Error(), "context canceled")) {
			_ = m.store.ResetEmbeddedAgentInterrupt(context.Background(), binding.AgentID)
		} else {
			_ = m.store.SetEmbeddedActivityState(context.Background(), binding, "idle")
		}
		m.publishProjection()
		m.OfferDirect(binding.AgentID)
	}()
	// Restored selections are untrusted admission input too. Check the real
	// SDK identity and private credential before any test substitution, tools,
	// conversation append, or provider work.
	if _, err := m.admitEmbeddedModel(ctx, Model{Provider: saved.SelectedModel.Provider, ID: saved.SelectedModel.ID}); err != nil {
		return err
	}
	if err := m.validateEmbeddedWorkingDirectory(saved.Cwd); err != nil {
		return err
	}
	model := ai.GetModel(saved.SelectedModel.Provider, saved.SelectedModel.ID)
	if model == nil && (saved.SelectedModel.Provider == "antigravity" || saved.SelectedModel.Provider == "google") {
		model = ai.GetModel("google", saved.SelectedModel.ID)
	}
	if model == nil && saved.SelectedModel.Provider == "antigravity" {
		model = &ai.Model{
			ID:            saved.SelectedModel.ID,
			Provider:      "antigravity",
			Api:           "google-generative-ai",
			ContextWindow: 1048576,
			MaxTokens:     8192,
		}
	}
	var injectedStreamModel *ai.Model
	if m.embeddedModelResolver != nil {
		resolved := m.embeddedModelResolver(saved)
		if resolved == nil {
			return errors.New("embedded model is unavailable")
		}
		if resolved.Provider == "openai-codex" {
			model = resolved
		} else {
			// Existing explicitly injected SDK doubles are a transport boundary,
			// not selection authority. Never substitute a built-in provider (which
			// could resolve ambient auth). The SDK session keeps its Codex identity.
			if len(ai.GetModels(resolved.Provider)) != 0 {
				return errors.New("embedded provider substitution denied")
			}
			for _, provider := range ai.GetProviders() {
				for _, builtIn := range ai.GetModels(provider) {
					if resolved.Api == builtIn.Api {
						return errors.New("embedded provider substitution denied")
					}
				}
			}
			injectedStreamModel = resolved
			copy := *model
			copy.ContextWindow, copy.MaxTokens = resolved.ContextWindow, resolved.MaxTokens
			model = &copy
		}
	}
	if model == nil {
		return errors.New("embedded model is unavailable")
	}
	if model.Provider == "openai-codex" {
		if err := validateEmbeddedCodexModel(model); err != nil {
			return err
		}
	}
	p := &embeddedPersistence{db: m.store, binding: binding, digest: "genesis", afterUserAppend: m.embeddedAfterUserAppend, publishComplete: m.publishCompleteHistory, toolInputs: make(map[string]any)}
	if wake != nil {
		if err := p.checkClaimedWake(ctx, *wake); err != nil {
			return err
		}
	}
	// Only an intent admitted by this SDK turn may cross the first reservation
	// window. A recovered intent has no such in-memory admission.
	freshWorkerIntents := make(map[string]bool)
	freshToolIntents := make(map[string]*store.EmbeddedToolInvocation)
	toolExecutions := make(map[string]*store.EmbeddedToolInvocation)
	freshBashIntents := make(map[string]bool)
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
	freshCompactionIntents := make(map[string]bool)
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
		if execution.expected == nil && result.ToolName != "goal" && result.ToolName != "goal_snooze" {
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
	type bashExecution struct {
		expected *ai.ToolResultMessage
		replay   bool
		failure  embeddedBashFailureClass
	}
	bashExecutions := make(map[string]bashExecution)
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
	p.bashResult = func(resultCtx context.Context, result ai.ToolResultMessage, commit bool) error {
		freshWorkerMu.Lock()
		execution := bashExecutions[result.ToolCallID]
		freshWorkerMu.Unlock()
		if !commit {
			if execution.expected == nil {
				// A pre-intent policy denial may be checked. After an intent,
				// a synthesized SDK error cannot certify the Bash outcome.
				_, err := m.store.RecoverEmbeddedEffect(resultCtx, binding, result.ToolCallID)
				if errors.Is(err, sql.ErrNoRows) {
					return nil
				}
				if err != nil {
					return err
				}
				if execution.failure == embeddedBashOutputTruncated {
					return errors.New("bash output truncated: outcome UNKNOWN after spawn")
				}
				return errors.New("bash outcome unknown: no checked SDK result")
			}
			if execution.expected.IsError {
				return errors.New("bash expected result is not successful")
			}
			actual, err := json.Marshal(result.Content)
			if err != nil {
				return err
			}
			expected, err := json.Marshal(execution.expected.Content)
			if err != nil {
				return err
			}
			if result.ToolCallID != execution.expected.ToolCallID || result.ToolName != execution.expected.ToolName || result.IsError != execution.expected.IsError || !bytes.Equal(actual, expected) {
				return errors.New("bash SDK result differs from checked effect")
			}
			if execution.replay {
				actual, err := json.Marshal(result)
				if err != nil {
					return err
				}
				expected, err := json.Marshal(execution.expected)
				if err != nil {
					return err
				}
				if !bytes.Equal(actual, expected) {
					return errors.New("bash SDK replay differs from saved effect")
				}
			}
			return nil
		}
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
		delete(bashExecutions, result.ToolCallID)
		freshWorkerMu.Unlock()
		return nil
	}
	// A non-nil list, even when empty, excludes every SDK built-in.
	tools := []agent.AgentTool{}
	role, err := m.store.EmbeddedSessionRole(ctx, binding)
	if err != nil {
		return err
	}
	if role == "root" || role == "worker" {
		tools = append(tools, m.embeddedAddressedTool(owner, "mecha_mailbox_send", toolAdmitted, toolCompleted, nil))
		tools = append(tools, m.embeddedGoalTool(owner, goalAdmitted, goalCompleted))
		tools = append(tools, m.embeddedGoalSnoozeTool(owner, func() store.EmbeddedGoalSnoozeTurn {
			return store.EmbeddedGoalSnoozeTurn{UserSeq: p.turnSeq, UserDigest: p.turnDigest}
		}, goalAdmitted, goalCompleted))
		tools = append(tools, m.embeddedCompactionTool(owner, func(id string) bool {
			freshWorkerMu.Lock()
			defer freshWorkerMu.Unlock()
			fresh := freshCompactionIntents[id]
			delete(freshCompactionIntents, id)
			return fresh
		}, func() (int64, string) { return p.seq, p.digest }, func(id string, result ai.ToolResultMessage) {
			freshWorkerMu.Lock()
			compactionResults[id] = &result
			freshWorkerMu.Unlock()
		}))
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
		tools = append(tools, m.embeddedBashTool(owner, saved.Cwd, func(id string) bool {
			freshWorkerMu.Lock()
			defer freshWorkerMu.Unlock()
			fresh := freshBashIntents[id]
			delete(freshBashIntents, id)
			return fresh
		}, func(id string, expected ai.ToolResultMessage, replay bool) {
			freshWorkerMu.Lock()
			bashExecutions[id] = bashExecution{expected: &expected, replay: replay}
			freshWorkerMu.Unlock()
		}, func(id string, class embeddedBashFailureClass) {
			freshWorkerMu.Lock()
			execution := bashExecutions[id]
			execution.failure = class
			bashExecutions[id] = execution
			freshWorkerMu.Unlock()
		}))
		readTool, err := coding.CreateTool("read", saved.Cwd)
		if err != nil {
			return fmt.Errorf("embedded read tool: %w", err)
		}
		editTool, err := coding.CreateTool("edit", saved.Cwd)
		if err != nil {
			return fmt.Errorf("embedded edit tool: %w", err)
		}
		writeTool, err := coding.CreateTool("write", saved.Cwd)
		if err != nil {
			return fmt.Errorf("embedded write tool: %w", err)
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
		tools = append(tools, m.EmbeddedSubagentPublicTools(binding)...)
	}
	if embeddedShouldAppendWorkerLaunch(role, binding.OwnerToken, saved.PendingInitialInstruction != "", explicitPrompt == "" && mail == nil && continuation == nil && wake == nil) {
		tools = append(tools, m.embeddedAddressedTool(owner, "mecha_worker", toolAdmitted, toolCompleted, func(id string) bool {
			freshWorkerMu.Lock()
			defer freshWorkerMu.Unlock()
			fresh := freshWorkerIntents[id]
			delete(freshWorkerIntents, id)
			return fresh
		}))
	}
	if m.embeddedExternalTool != nil {
		tool := *m.embeddedExternalTool
		if tool.Name == "" || tool.Parameters == nil || tool.Execute == nil {
			return errors.New("invalid registered embedded external tool")
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
	var appliedReceipt *store.EmbeddedGoalAppliedReceipt
	// The SDK invokes StreamFn only when it has selected a provider request.
	// MessageEnd persistence has already checked the initiating user and any
	// goal tool result. Never mark a desired revision applied at goal set time.
	stream := func(requestCtx context.Context, requestModel *ai.Model, transcript ai.TranscriptContext, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		fail := func(err error) *ai.AssistantMessageEventStream { return ai.ErrorStream(requestModel, err) }
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
		if requestModel.Provider == "antigravity" || requestModel.Provider == "google" {
			return m.streamEmbeddedGoogle(requestCtx, requestModel, transcript, opts)
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
	session := coding.NewSession(coding.SessionOptions{SessionID: binding.SessionID, Model: model, Cwd: saved.Cwd, Tools: tools, OrderedPersistence: p, StreamFn: stream, MaxTokens: &maxTokens,
		ReplayToolResult: func(callCtx context.Context, call agent.BeforeToolCallContext) (*ai.ToolResultMessage, error) {
			if call.ToolCall.Name != "bash" {
				return nil, nil
			}
			if role != "root" && role != "worker" {
				return nil, errors.New("bash replay requires embedded root or worker")
			}
			if err := m.store.RenewEmbeddedOwner(callCtx, binding, owner.identity, embeddedOwnerTTL); err != nil {
				return nil, err
			}
			effect, err := m.store.RecoverEmbeddedEffect(callCtx, binding, call.ToolCall.ID)
			if errors.Is(err, sql.ErrNoRows) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			intent, err := json.Marshal(call.ToolCall)
			if err != nil {
				return nil, err
			}
			if !bytes.Equal(effect.Intent, intent) {
				return nil, errors.New("bash replay intent mismatch")
			}
			if effect.State != "observed" {
				return nil, nil
			}
			message, err := ai.UnmarshalMessage(effect.Result)
			if err != nil {
				return nil, err
			}
			result, ok := message.(ai.ToolResultMessage)
			if !ok || result.MessageRole() != ai.RoleToolResult || result.ToolCallID != call.ToolCall.ID || result.ToolName != "bash" || result.IsError || len(result.Content) != 1 || result.Timestamp <= 0 {
				return nil, errors.New("invalid observed bash replay result")
			}
			if _, ok := result.Content[0].(ai.TextContent); !ok {
				return nil, errors.New("invalid observed bash replay content")
			}
			canonical, err := json.Marshal(result)
			if err != nil {
				return nil, err
			}
			if !bytes.Equal(canonical, effect.Result) {
				return nil, errors.New("observed bash replay bytes differ")
			}
			freshWorkerMu.Lock()
			bashExecutions[call.ToolCall.ID] = bashExecution{expected: &result, replay: true}
			freshWorkerMu.Unlock()
			return &result, nil
		}, BeforeToolExecute: func(callCtx context.Context, call agent.BeforeToolCallContext) error {
			toolName := call.ToolCall.Name
			toolCallID := call.ToolCall.ID
			if toolCallID != "" && toolName != "" {
				if p != nil {
					if p.toolInputs == nil {
						p.toolInputs = make(map[string]any)
					}
					p.toolInputs[toolCallID] = call.ToolCall.Arguments
				}
				invocationKey := fmt.Sprintf("tool:%s:%s", binding.AgentID, toolCallID)
				entry, err := m.store.RecordManagedCompleteHistoryWithPresentationAndInput(callCtx, binding.AgentID, "managed_tool", "info", "Running "+toolName, invocationKey, &store.ManagedHistoryPresentation{
					Kind:     "tool",
					ToolName: toolName,
					Outcome:  "active",
					Summary:  "Running",
				}, call.ToolCall.Arguments)
				if err == nil {
					m.publishCompleteHistory(entry)
				}
			}
			var registered *agent.AgentTool
			for i := range tools {
				if call.ToolCall.Name == tools[i].Name {
					registered = &tools[i]
					break
				}
			}
			if registered == nil || !registered.Parameters.Check(call.ToolCall.Arguments) {
				return &agent.PolicyDenial{Reason: "embedded external tool schema mismatch"}
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
				if (role != "root" && role != "worker") || len(call.ToolCall.Arguments) != 1 {
					return &agent.PolicyDenial{Reason: "context requires own root or worker and a closed action"}
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
			if call.ToolCall.Name == "goal_snooze" {
				if role != "root" && role != "worker" {
					return &agent.PolicyDenial{Reason: "goal snooze requires embedded root or worker"}
				}
				if _, err := embeddedGoalSnoozeArguments(call.ToolCall.Arguments); err != nil {
					return &agent.PolicyDenial{Reason: err.Error()}
				}
			}
			if call.ToolCall.Name == "bash" {
				if role != "root" && role != "worker" {
					return &agent.PolicyDenial{Reason: "bash requires embedded root or worker"}
				}
				if _, parseErr := parseEmbeddedBash(call.ToolCall.Arguments); parseErr != nil {
					return &agent.PolicyDenial{Reason: parseErr.Error()}
				}
			}
			if call.ToolCall.Name == "read" || call.ToolCall.Name == "edit" || call.ToolCall.Name == "write" {
				if role != "root" && role != "worker" {
					return &agent.PolicyDenial{Reason: call.ToolCall.Name + " requires embedded root or worker"}
				}
			}
			if call.ToolCall.Name == "mecha_worker" || call.ToolCall.Name == "mecha_mailbox_send" {
				if role != "root" && role != "worker" {
					return &agent.PolicyDenial{Reason: "addressed managed tools require a saved root or worker"}
				}
				if err := embeddedAddressedArguments(call.ToolCall.Name, call.ToolCall.Arguments); err != nil {
					return &agent.PolicyDenial{Reason: err.Error()}
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
			if call.ToolCall.Name == "goal" || call.ToolCall.Name == "goal_snooze" {
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
			if call.ToolCall.Name == "read" || call.ToolCall.Name == "write" || call.ToolCall.Name == "edit" || call.ToolCall.Name == "bash" {
				prior, err := m.store.RecoverEmbeddedEffect(callCtx, binding, call.ToolCall.ID)
				if err == nil {
					if !bytes.Equal(prior.Intent, intent) {
						return errors.New("file call ID conflicts with prior arguments")
					}
					if call.ToolCall.Name == "bash" && prior.State == "observed" {
						return nil
					}
					if prior.State == "unknown" {
						return errors.New("file outcome unknown: no committed SDK tool result")
					}
					return errors.New("file call ID already used")
				}
				if !errors.Is(err, sql.ErrNoRows) {
					return err
				}
			}
			if call.ToolCall.Name == "mecha_context" && call.ToolCall.Arguments["action"] == "compact" {
				freshWorkerMu.Lock()
				delete(freshCompactionIntents, call.ToolCall.ID)
				delete(compactionResults, call.ToolCall.ID)
				freshWorkerMu.Unlock()
				prior, err := m.store.RecoverEmbeddedEffect(callCtx, binding, call.ToolCall.ID)
				if err == nil {
					if !bytes.Equal(prior.Intent, intent) {
						return errors.New("compaction call ID conflicts with original arguments")
					}
					return errors.New("compaction outcome unknown or already recorded: no replay")
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
			if call.ToolCall.Name == "mecha_worker" || call.ToolCall.Name == "mecha_mailbox_send" {
				freshWorkerMu.Lock()
				delete(freshWorkerIntents, call.ToolCall.ID)
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
			if call.ToolCall.Name == "mecha_worker" || call.ToolCall.Name == "mecha_mailbox_send" {
				toolAdmission, err = m.store.RecordEmbeddedToolInvocation(callCtx, binding, goalProcess, p.turnSeq, p.turnDigest, p.mailAttempt, call.ToolCall)
			} else if call.ToolCall.Name == "goal" || call.ToolCall.Name == "goal_snooze" {
				goalAdmission, err = m.store.RecordEmbeddedGoalInvocation(callCtx, binding, goalProcess, p.turnSeq, p.turnDigest, p.mailAttempt, call.ToolCall.ID, intent)
			} else {
				err = m.store.RecordEmbeddedEffectIntent(callCtx, binding, call.ToolCall.ID, intent)
			}
			if err != nil {
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
			if call.ToolCall.Name == "mecha_context" && call.ToolCall.Arguments["action"] == "compact" {
				freshWorkerMu.Lock()
				freshCompactionIntents[call.ToolCall.ID] = true
				freshWorkerMu.Unlock()
			}
			if call.ToolCall.Name == "mecha_reminder" {
				freshWorkerMu.Lock()
				freshReminderIntents[call.ToolCall.ID] = true
				freshWorkerMu.Unlock()
			}
			if call.ToolCall.Name == "goal" || call.ToolCall.Name == "goal_snooze" {
				freshWorkerMu.Lock()
				freshGoalIntents[call.ToolCall.ID] = goalAdmission
				freshWorkerMu.Unlock()
			}
			if call.ToolCall.Name == "bash" {
				freshWorkerMu.Lock()
				freshBashIntents[call.ToolCall.ID] = true
				freshWorkerMu.Unlock()
			}

			if toolAdmission != nil {
				freshWorkerMu.Lock()
				freshToolIntents[call.ToolCall.ID] = toolAdmission
				if call.ToolCall.Name == "mecha_worker" && call.ToolCall.Arguments["action"] == "launch" {
					freshWorkerIntents[call.ToolCall.ID] = true
				}
				freshWorkerMu.Unlock()
			}
			return nil
		}})
	m.activeSessionsMu.Lock()
	m.activeEmbeddedSessions[saved.AgentID] = session
	m.activeSessionsMu.Unlock()
	defer func() {
		m.activeSessionsMu.Lock()
		if m.activeEmbeddedSessions[saved.AgentID] == session {
			delete(m.activeEmbeddedSessions, saved.AgentID)
		}
		m.activeSessionsMu.Unlock()
	}()
	if err := p.restore(ctx, session); err != nil {
		return err
	}
	var recoveryNotice *store.EmbeddedInterruptedNotice
	var resumeUnentered bool
	if wake == nil && mail == nil && explicitPrompt == "" && continuation == nil && binding.OwnerToken > 1 {
		snap, err := m.store.ReopenEmbedded(ctx, binding)
		if err != nil {
			return err
		}
		for i := len(snap.Entries) - 1; i >= 0; i-- {
			if snap.Entries[i].Kind != "user" {
				continue
			}
			recoveryNotice, err = m.store.PrepareEmbeddedInterruptedNotice(ctx, binding, snap.Entries[i].Sequence)
			if err != nil {
				return err
			}
			if recoveryNotice != nil {
				break
			}
			// Only the latest checked user can be resumed. A provider-entry
			// marker makes its outcome unknown, never retryable.
			if i == len(snap.Entries)-1 {
				resumeUnentered, err = m.store.EmbeddedUnenteredUser(ctx, binding, snap.Entries[i].Sequence, snap.Entries[i].Digest)
				if err != nil {
					return err
				}
			}
			break
		}
	}
	// The SDK transform runs on the serial provider lane after AppendMessage
	// persisted the checked tool result, including on tool continuations.
	session.Agent.TransformContext = func(requestCtx context.Context, messages []agent.AgentMessage) []agent.AgentMessage {
		view, err := m.fitEmbeddedContext(requestCtx, p, owner, model, stream, messages)
		if err != nil {
			// The SDK transform has no error return. Its ordered run boundary
			// converts this into a failed turn before ordinary provider entry.
			panic(err)
		}
		return view
	}
	hasUser := false
	for _, message := range session.History() {
		if message.MessageRole() == "user" {
			hasUser = true
			break
		}
	}
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
	if wake != nil {
		// ClaimEmbeddedRestartWake already starts an interrupted 076 notice.
		// Do not enter the ordinary recovery path, which starts it again.
		return handleTurnErr(p.runClaimedWake(ctx, session, *wake, wakePrompts))
	}
	if recoveryNotice != nil && recoveryNotice.State != "started" {
		p.notice = recoveryNotice
		if recoveryNotice.State == "appended" {
			p.noticeStart = true
			return handleTurnErr(session.Continue(ctx))
		}
		message := ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: recoveryNotice.Text}}}
		var err error
		p.noticeMessage, err = json.Marshal(message)
		if err != nil {
			return err
		}
		_, err = session.RunMessages(ctx, []agent.AgentMessage{message})
		return handleTurnErr(err)
	}
	if resumeUnentered {
		p.turnSeq, p.turnDigest = p.seq, p.digest
		// Continue the restored checked user, never append a duplicate user.
		// StreamFn fences and commits provider entry before dispatch.
		if err := session.Continue(ctx); err != nil {
			return handleTurnErr(err)
		}
		if generation, ok := ctx.Value(embeddedOriginalRestartGenerationKey{}).(int64); ok {
			_, err := m.store.ScheduleEmbeddedSettledOriginalWake(ctx, binding, generation, p.turnSeq, p.turnDigest)
			return err
		}
		return nil
	}
	if continuation != nil {
		promptText := "Continue working on the current goal."
		if explicitPrompt != "" {
			promptText = explicitPrompt
		}
		_, err := session.RunMessages(ctx, []agent.AgentMessage{ai.UserMessage{
			Content: ai.ContentList{ai.TextContent{Text: promptText}},
		}})
		if err != nil {
			return handleTurnErr(err)
		}
		return m.evaluateEmbeddedTurn(ctx, binding, p, appliedReceipt, model, stream)
	}
	// A failed system declaration can leave only a system entry; the first
	// user prompt is still due. Once a user is durable, never silently
	// reissue a provider request after an ambiguous interruption.
	if mail != nil {
		if mail.Binding.AgentID != binding.AgentID || mail.Binding.SessionID != binding.SessionID || mail.Binding.Epoch != binding.Epoch || mail.Binding.Generation != binding.Generation || mail.Binding.OwnerToken != binding.OwnerToken {
			return errors.New("embedded mail owner mismatch")
		}
		message, err := store.EmbeddedMailUserMessage(mail, m.clock.Now().UnixMilli())
		if err != nil {
			return err
		}
		p.mailMessage, err = json.Marshal(message)
		if err != nil {
			return err
		}
		p.mail = mail
		_, err = session.RunMessages(ctx, []agent.AgentMessage{message})
		// The SDK may turn a rejected checked append into a local synthetic
		// error. Preserve the Store's transaction outcome, not its error text.
		if p.mailAppendErr != nil {
			return p.mailAppendErr
		}
		if err != nil {
			return handleTurnErr(err)
		}
		return m.evaluateEmbeddedTurn(ctx, binding, p, appliedReceipt, model, stream)
	}
	if explicitPrompt != "" {
		_, err := session.Run(ctx, explicitPrompt)
		if err != nil {
			return handleTurnErr(err)
		}
		return m.evaluateEmbeddedTurn(ctx, binding, p, appliedReceipt, model, stream)
	}
	if !hasUser && saved.PendingInitialInstruction != "" {
		_, err := session.Run(ctx, saved.PendingInitialInstruction)
		if err != nil {
			return handleTurnErr(err)
		}
		return m.evaluateEmbeddedTurn(ctx, binding, p, appliedReceipt, model, stream)
	}
	return nil
}

// evaluateEmbeddedTurn runs only after a successful SDK turn. The receipt is
// captured at the provider-request boundary, never inferred from desired state.
func (m *Manager) evaluateEmbeddedTurn(ctx context.Context, binding store.EmbeddedBinding, p *embeddedPersistence, receipt *store.EmbeddedGoalAppliedReceipt, model *ai.Model, stream agent.StreamFn) error {
	if receipt == nil || !receipt.GoalEnabled ||
		receipt.TurnEntrySeq != p.turnSeq || receipt.TurnEntryDigest != p.turnDigest {
		return nil
	}
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
		evaluator := m.embeddedGoalEvaluator
		if evaluator == nil {
			evaluator = func(evalCtx context.Context, evalKey store.EmbeddedGoalEvaluationKey) (string, string, error) {
				return m.evaluateEmbeddedProductionGoal(evalCtx, binding, evalKey, snapshot, policy.Config.Goal, model, stream)
			}
		}
		outcome, reason, evalErr := evaluator(m.runCtx, key)
		changed := false
		if evalErr == nil && m.runCtx.Err() == nil {
			_, changed, evalErr = m.store.CompleteEmbeddedGoalEvaluation(m.runCtx, binding, key, outcome, reason)
		}
		if evalErr == nil && (outcome == store.EmbeddedGoalEvaluationDone || outcome == store.EmbeddedGoalEvaluationNotDone) {
			m.recordGoalEvaluationActivity(binding, key, outcome, reason)
		}
		lane.serial.Unlock()
		if evalErr != nil || !changed || outcome != store.EmbeddedGoalEvaluationNotDone {
			return
		}
		m.dispatchEmbeddedGoalContinuation(binding, key)
	})
	return nil
}
