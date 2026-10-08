package managed

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/coding"
)

// dispatchEmbeddedMail is a saved-runtime branch of OfferDirect. The lane
// excludes launch, public prompts and other mail turns. A committed inbound
// append with an uncertain provider outcome is never replayed on recovery.
func (m *Manager) dispatchEmbeddedMail(agentID string) {
	lane := m.laneFor(agentID)

	lane.activeMu.RLock()
	active := lane.activeSession
	lane.activeMu.RUnlock()

	if active != nil {
		// Steer the message mid-turn!
		// Try to fetch next mail without locking lane.serial
		if handled := m.trySteerActiveSession(agentID, active); handled {
			return
		}
	}

	lane.serial.Lock()
	defer lane.serial.Unlock()
	for {
		if m.runCtx.Err() != nil {
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
			var err error
			saved, err = m.store.ManagedAgent(m.runCtx, agentID)
			if err != nil || saved.Runtime != "embedded" || saved.DesiredState != "running" || saved.LifecycleState != "running" || !saved.AcceptingPrompts || !saved.SlotHeld {
				return false
			}
			m.mu.Lock()
			var ok bool
			owner, ok = m.embeddedOwners[agentID]
			closed := m.closed
			m.mu.Unlock()
			if !ok || closed || owner.binding.AgentID != agentID || owner.binding.SessionID != saved.PiSessionID || owner.binding.Epoch != saved.SessionEpoch || owner.binding.Generation != saved.RuntimeGeneration {
				return false
			}
			if err := m.store.RenewEmbeddedOwner(m.runCtx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
				token, acqErr := m.store.AcquireEmbeddedOwner(m.runCtx, owner.binding, owner.identity, embeddedOwnerTTL)
				if acqErr != nil {
					log.Printf("[embedded:%s] ERROR renewing/acquiring owner lease for mail: renew=%v, acquire=%v", agentID, err, acqErr)
					return false
				}
				owner.binding.OwnerToken = token
				m.mu.Lock()
				m.embeddedOwners[agentID] = owner
				m.mu.Unlock()
			}
			id, err := m.store.NextEmbeddedMail(m.runCtx, agentID)
			if err != nil || id == "" {
				return false
			}
			attempt, err = m.store.ReserveEmbeddedMail(m.runCtx, owner.binding, id)
			return err == nil
		}()
		if !admitted {
			return
		}
		binding := owner.binding
		log.Printf("[embedded:%s] Turn started (source: %s, message: %s)", binding.AgentID, "mail", attempt.MessageID)
		ctx := context.WithValue(m.runCtx, embeddedOwnerContextKey{}, owner)
		finalSeq, err := m.runEmbeddedMail(ctx, *saved, attempt)
		if err != nil {
			log.Printf("[embedded:%s] ERROR turn failed (source: %s, message: %s): %v", binding.AgentID, "mail", attempt.MessageID, err)
			lane.mailAttempts[attempt.MessageID]++
			attempts := lane.mailAttempts[attempt.MessageID]

			// Do not fail the worker on turn errors. Transition cleanly back to idle:
			if fenceErr := m.store.RenewEmbeddedOwner(context.Background(), owner.binding, owner.identity, embeddedOwnerTTL); fenceErr == nil {
				_ = m.store.SetEmbeddedActivityState(context.Background(), owner.binding, "idle")
				_ = m.store.ResetEmbeddedAgentInterrupt(context.Background(), owner.binding.AgentID)
				m.publishProjection()
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

func (m *Manager) trySteerActiveSession(agentID string, active *coding.Session) bool {
	ctx := m.runCtx
	if ctx == nil {
		ctx = context.Background()
	}
	msgID, err := m.store.NextEmbeddedMail(ctx, agentID)
	if err != nil || msgID == "" {
		return false
	}
	msg, err := m.store.ClaimEmbeddedMailAttempt(ctx, msgID)
	if err != nil || msg == nil {
		return false
	}

	nowMilli := time.Now().UnixMilli()
	if m.clock != nil {
		nowMilli = m.clock.Now().UnixMilli()
	}

	userMsg := buildSteerUserMessage(msg, nowMilli)
	active.Steer(userMsg)
	log.Printf("[embedded:%s] Successfully steered message %s mid-turn into active session", agentID, msg.ID)
	return true
}

func buildSteerUserMessage(msg *store.ClaimedEmbeddedMailAttempt, nowMilli int64) ai.UserMessage {
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
