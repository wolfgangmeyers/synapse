package managed

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
)

// dispatchEmbeddedMail is a saved-runtime branch of OfferDirect. The lane
// excludes launch, public prompts and other mail turns. A committed inbound
// append with an uncertain provider outcome is never replayed on recovery.
func (m *Manager) dispatchEmbeddedMail(agentID string) {
	m.dispatchEmbeddedMailTagged(agentID, "", "")
}

func (m *Manager) dispatchEmbeddedMailTagged(agentID, messageID, offerSite string) {
	lane := m.laneFor(agentID)

	lane.activeMu.RLock()
	active := lane.activeSession
	lane.activeMu.RUnlock()

	if active != nil {
		// Steer the message mid-turn!
		// Try to fetch next mail without locking lane.serial
		steeredAny := false
		for m.trySteerActiveSession(agentID, active) {
			steeredAny = true
		}
		if steeredAny {
			return
		}
	}

	gated := false
	defer func() {
		if gated && messageID != "" {
			if err := restartMailGatedReceipt(m.runCtx, agentID, messageID, offerSite); err != nil {
				log.Printf("mail test receipt: %v", err)
			}
		}
	}()
	lane.serial.Lock()
	defer lane.serial.Unlock()
	for {
		if pending := lane.claudePendingDiagnostic; pending != nil {
			entry, err := m.store.RecordClaudeUnclassifiedMail(context.Background(), pending.Binding, pending, claudeUnclassifiedDiagnostic)
			if errors.Is(err, store.ErrStaleEmbeddedFence) {
				lane.claudePendingDiagnostic = nil
				continue
			}
			if err != nil && entry.ID == "" {
				log.Printf("[embedded:%s] Claude diagnostic retry failed: %v", agentID, err)
				m.retryClaudeSettlement(agentID)
				return
			}
			lane.claudePendingDiagnostic = nil
			m.publishCompleteHistory(entry)
			continue
		}
		if pending := lane.claudePendingSettlement; pending != nil {
			var entry store.ManagedHistoryEntry
			var settleErr error
			if lane.claudePendingHold {
				entry, settleErr = m.store.HoldClaudeUncertainMail(context.Background(), pending.Binding, pending)
			} else {
				entry, settleErr = m.store.SettleClaudeUnsubmittedMail(context.Background(), pending.Binding, pending)
			}
			if errors.Is(settleErr, store.ErrStaleEmbeddedFence) {
				lane.claudePendingSettlement = nil
				lane.claudePendingHold = false
				lane.claudeUnclassifiedBlocked = pending
				delete(lane.mailAttempts, pending.MessageID)
				continue
			}
			if settleErr != nil && entry.ID == "" {
				log.Printf("[embedded:%s] Claude settlement retry failed: %v", agentID, settleErr)
				m.retryClaudeSettlement(agentID)
				return
			}
			lane.claudePendingSettlement = nil
			lane.claudePendingHold = false
			delete(lane.mailAttempts, pending.MessageID)
			m.publishCompleteHistory(entry)
			continue
		}
		if lane.embeddedResetPending() || m.runCtx.Err() != nil {
			gated = true
			return
		}
		m.flushEmbeddedReplies(m.runCtx, agentID)
		// A picker can commit between turns, never between the model snapshot
		// and this mail turn's durable admission.
		var saved *store.ManagedAgentRecord
		var owner embeddedOwner
		var attempt *store.EmbeddedMailAttempt
		admitted := func() bool {
			lane.selection.Lock()
			defer lane.selection.Unlock()
			lane.admission.Lock()
			defer lane.admission.Unlock()
			if (lane.resetPending || len(lane.resetToolGates) > 0) || lane.terminationPending > 0 {
				return false
			}
			var err error
			saved, err = m.store.ManagedAgent(m.runCtx, agentID)
			if err != nil || saved.Runtime != "embedded" || saved.DesiredState != "running" || saved.LifecycleState != "running" || !saved.AcceptingPrompts || !saved.SlotHeld {
				return false
			}
			owner, err = m.ensureEmbeddedOwner(m.runCtx, agentID, *saved)
			if err != nil {
				log.Printf("[embedded:%s] ERROR ensuring owner lease for mail: %v", agentID, err)
				return false
			}
			id, err := m.store.NextEmbeddedMail(m.runCtx, agentID)
			if err != nil || id == "" {
				return false
			}
			attempt, err = m.store.ReserveEmbeddedMail(m.runCtx, owner.binding, id)
			if err != nil {
				log.Printf("[embedded:%s] ERROR reserving mail %s: %v", agentID, id, err)
			}
			return err == nil
		}()
		if !admitted {
			return
		}
		binding := owner.binding
		if saved.SelectedModel.Provider == claudeCodeProvider && sameClaudeBlockedAttempt(lane.claudeUnclassifiedBlocked, attempt) {
			return
		}
		log.Printf("[embedded:%s] Turn started (source: %s, message: %s)", binding.AgentID, "mail", attempt.MessageID)
		ctx := context.WithValue(m.runCtx, embeddedOwnerContextKey{}, owner)
		finalSeq, err := m.runEmbeddedMail(ctx, *saved, attempt)
		if err != nil {
			log.Printf("[embedded:%s] ERROR turn failed (source: %s, message: %s): %v", binding.AgentID, "mail", attempt.MessageID, err)
			if errors.Is(err, errClaudeInputBounds) {
				// runEmbeddedMail joins callbacks and closes the scoped process before
				// returning. Only a definite pre-submission rejection is released.
				if errors.Is(err, errEmbeddedExecutionUnsettled) {
					log.Printf("[embedded:%s] Rejected mail %s remains reserved: execution cleanup failed", agentID, attempt.MessageID)
					return
				}
				if releaseErr := m.store.ReleaseEmbeddedUnconsumedAttempt(context.Background(), binding, attempt.AttemptID); releaseErr != nil {
					log.Printf("[embedded:%s] ERROR releasing rejected mail %s: %v", agentID, attempt.MessageID, releaseErr)
				}
				delete(lane.mailAttempts, attempt.MessageID)
				if m.store.RenewEmbeddedOwner(context.Background(), binding, owner.identity, embeddedOwnerTTL) == nil {
					_ = m.store.SetEmbeddedActivityState(context.Background(), binding, "idle")
					m.publishProjection()
				}
				return
			}
			if strings.Contains(err.Error(), "Codex auth unavailable") {
				m.failEmbeddedOwner(owner.binding, saved.LaunchOperationID, "EMBEDDED_MAIL_FAILED", err)
				return
			}
			// Do not fail the worker on turn errors. Transition cleanly back to idle:
			if fenceErr := m.store.RenewEmbeddedOwner(context.Background(), owner.binding, owner.identity, embeddedOwnerTTL); fenceErr == nil {
				_ = m.store.SetEmbeddedActivityState(context.Background(), owner.binding, "idle")
				_ = m.store.ResetEmbeddedAgentInterrupt(context.Background(), owner.binding.AgentID)
				m.publishProjection()
			}
			m.flushEmbeddedReplies(m.runCtx, agentID)

			if saved.SelectedModel.Provider == claudeCodeProvider {
				if errors.Is(err, errEmbeddedExecutionUnsettled) {
					m.blockClaudeUnclassified(lane, attempt)
					return
				}
				held, holdErr := m.store.ClaudeMailHeld(context.Background(), attempt.MessageID)
				if holdErr != nil {
					// Unknown marker state must not be treated as a definite start failure.
					m.blockClaudeUnclassified(lane, attempt)
					return
				}
				if held {
					lane.claudePendingSettlement = attempt
					lane.claudePendingHold = true
					continue
				}
				if !errors.Is(err, errClaudeDefiniteStart) {
					m.blockClaudeUnclassified(lane, attempt)
					return
				}
			}
			lane.mailAttempts[attempt.MessageID]++
			attempts := lane.mailAttempts[attempt.MessageID]
			if saved.SelectedModel.Provider == claudeCodeProvider && attempts >= 3 {
				lane.claudePendingSettlement = attempt
				continue
			}

			if attempts >= 3 {
				log.Printf("[embedded:%s] POISON PILL: message %s failed 3 times; dead-lettering to prevent spin loop: %v", agentID, attempt.MessageID, err)
				delete(lane.mailAttempts, attempt.MessageID)
				if dismissErr := m.store.DismissMailboxMessage(context.Background(), attempt.MessageID); dismissErr != nil {
					log.Printf("[embedded:%s] ERROR dismissing dead-lettered message %s: %v", agentID, attempt.MessageID, dismissErr)
				}
				return
			}

			baseBackoff := 5 * time.Second
			if m.embeddedMailBackoff > 0 {
				baseBackoff = m.embeddedMailBackoff
			}
			backoff := time.Duration(1<<(attempts-1)) * baseBackoff

			if saved.SelectedModel.Provider == claudeCodeProvider {
				m.scheduleClaudeMailRetry(agentID, backoff)
				return
			}

			m.startManagedTask(func() {
				timer := time.NewTimer(backoff)
				defer timer.Stop()
				select {
				case <-timer.C:
					m.OfferDirect(agentID)
				case <-m.runCtx.Done():
				}
			})
			return
		}
		delete(lane.mailAttempts, attempt.MessageID)
		log.Printf("[embedded:%s] Turn finished successfully (seq: %d)", binding.AgentID, finalSeq)
		m.flushEmbeddedReplies(ctx, agentID)
	}
}

func (m *Manager) trySteerActiveSession(agentID string, active embeddedRuntime) bool {
	// Claude mail stays in the durable queue for the next scoped execution.
	if _, ok := active.(*claudeCodeRuntime); ok {
		return false
	}
	ctx := m.runCtx
	if ctx == nil {
		ctx = context.Background()
	}
	lane := m.laneFor(agentID)
	lane.admission.Lock()
	defer lane.admission.Unlock()
	if (lane.resetPending || len(lane.resetToolGates) > 0) || lane.terminationPending > 0 {
		return false
	}
	lane.activeMu.RLock()
	p := lane.activePersistence
	current := lane.activeSession
	lane.activeMu.RUnlock()
	if p == nil || current != active {
		return false
	}

	msgID, err := m.store.NextEmbeddedSteerMail(ctx, agentID)
	if err != nil || msgID == "" {
		return false
	}
	if p.hasSteerAttempt(msgID) {
		return false
	}
	attempt, err := m.store.ReserveEmbeddedMail(ctx, p.binding, msgID)
	if err != nil || attempt == nil {
		log.Printf("[embedded:%s] Failed to reserve steer mail %s: %v", agentID, msgID, err)
		return false
	}

	nowMilli := time.Now().UnixMilli()
	if m.clock != nil {
		nowMilli = m.clock.Now().UnixMilli()
	}

	userMsg := buildSteerUserMessage(attempt, nowMilli)
	userMsgBytes, err := json.Marshal(userMsg)
	if err != nil {
		log.Printf("[embedded:%s] Failed to marshal steer user message: %v", agentID, err)
		return false
	}

	if !p.queueSteerAttempt(attempt, userMsgBytes) {
		return false
	}
	active.Steer(userMsg)
	log.Printf("[embedded:%s] Successfully steered message %s mid-turn into active session", agentID, attempt.MessageID)
	return true
}

func buildSteerUserMessage(msg *store.EmbeddedMailAttempt, nowMilli int64) ai.UserMessage {
	text := unwrapEmbeddedMailText(msg.Body)
	var content ai.ContentList
	var attachmentRefs []string
	var imageBlocks []ai.ImageContent

	for _, att := range msg.Attachments {
		if att.Data != "" {
			mime := att.MIMEType
			if mime == "" {
				mime = "image/png"
			}
			imageBlocks = append(imageBlocks, ai.ImageContent{
				Data:     att.Data,
				MimeType: mime,
			})
		} else if att.Path != "" {
			if data, err := os.ReadFile(att.Path); err == nil && len(data) > 0 {
				mime := att.MIMEType
				if mime == "" {
					ext := strings.ToLower(filepath.Ext(att.Path))
					switch ext {
					case ".png":
						mime = "image/png"
					case ".jpg", ".jpeg":
						mime = "image/jpeg"
					case ".webp":
						mime = "image/webp"
					case ".gif":
						mime = "image/gif"
					}
				}
				if strings.HasPrefix(mime, "image/") {
					imageBlocks = append(imageBlocks, ai.ImageContent{
						Data:     base64.StdEncoding.EncodeToString(data),
						MimeType: mime,
					})
				}
			}
		}

		ref := att.Path
		if ref == "" {
			ref = att.URL
		}
		if ref == "" {
			ref = att.Name
		}
		if ref == "" {
			ref = att.ID
		}
		if ref != "" {
			attachmentRefs = append(attachmentRefs, ref)
		}
	}

	if len(attachmentRefs) > 0 && !strings.Contains(text, "Attached image") && !strings.Contains(text, "Attachment") {
		text += "\n\nAttached:\n"
		for i, ref := range attachmentRefs {
			text += fmt.Sprintf("%d. %s\n", i+1, ref)
		}
		text = strings.TrimRight(text, "\n")
	}

	content = append(content, ai.TextContent{Text: text})
	for _, img := range imageBlocks {
		content = append(content, img)
	}

	return ai.UserMessage{
		Content:   content,
		Timestamp: nowMilli,
	}
}

// Retry only the fenced transaction, not the provider execution.
func (m *Manager) retryClaudeSettlement(agentID string) {
	m.scheduleClaudeMailRetry(agentID, 5*time.Second)
}

const claudeUnclassifiedDiagnostic = "Claude mail could not be classified safely and is blocked from automatic retry. Inspect the router logs and conversation before retrying or restarting the router."

func sameClaudeBlockedAttempt(a, b *store.EmbeddedMailAttempt) bool {
	return a != nil && b != nil && a.MessageID == b.MessageID && a.MessageIncarnationID == b.MessageIncarnationID && a.Binding == b.Binding
}

func (m *Manager) blockClaudeUnclassified(lane *managedAgentLane, attempt *store.EmbeddedMailAttempt) {
	lane.claudeUnclassifiedBlocked = attempt
	entry, err := m.store.RecordClaudeUnclassifiedMail(context.Background(), attempt.Binding, attempt, claudeUnclassifiedDiagnostic)
	if errors.Is(err, store.ErrStaleEmbeddedFence) {
		return
	}
	if err != nil && entry.ID == "" {
		log.Printf("[embedded:%s] cannot record blocked Claude mail diagnostic: %v", attempt.Binding.AgentID, err)
		lane.claudePendingDiagnostic = attempt
		m.retryClaudeSettlement(attempt.Binding.AgentID)
		return
	}
	m.publishCompleteHistory(entry)
}

func (m *Manager) scheduleClaudeMailRetry(agentID string, delay time.Duration) {
	lane := m.laneFor(agentID) // caller holds lane.serial
	deadline := time.Now().Add(delay)
	if lane.claudeRetryScheduled {
		if deadline.Before(lane.claudeRetryDeadline) {
			lane.claudeRetryDeadline = deadline
			select {
			case lane.claudeRetryWake <- struct{}{}:
			default:
			}
		}
		return
	}
	lane.claudeRetryScheduled = true
	lane.claudeRetryDeadline = deadline
	lane.claudeRetryWake = make(chan struct{}, 1)
	wake := lane.claudeRetryWake
	if !m.startManagedTask(func() {
		for {
			lane.serial.Lock()
			remaining := time.Until(lane.claudeRetryDeadline)
			lane.serial.Unlock()
			timer := time.NewTimer(remaining)
			select {
			case <-timer.C:
				lane.serial.Lock()
				if time.Now().Before(lane.claudeRetryDeadline) {
					lane.serial.Unlock()
					continue
				}
				lane.claudeRetryScheduled = false
				lane.claudeRetryDeadline = time.Time{}
				lane.serial.Unlock()
				m.OfferDirect(agentID)
				return
			case <-wake:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			case <-m.runCtx.Done():
				timer.Stop()
				lane.serial.Lock()
				lane.claudeRetryScheduled = false
				lane.claudeRetryDeadline = time.Time{}
				lane.serial.Unlock()
				return
			}
		}
	}) {
		lane.claudeRetryScheduled = false
		lane.claudeRetryDeadline = time.Time{}
	}
}
