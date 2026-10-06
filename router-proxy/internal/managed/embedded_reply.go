package managed

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func (m *Manager) SetEmbeddedReplyPublisher(p func(context.Context, store.EmbeddedReply) error) {
	m.mu.Lock()
	m.embeddedReplyPublisher = p
	m.mu.Unlock()
}

// RecoverEmbeddedReplies is an explicit cold-recovery trigger. It never
// reopens a provider turn and needs no expired owner lease to resend an
// already committed final. Startup callers can invoke it after wiring the
// existing router publisher.
func (m *Manager) RecoverEmbeddedReplies(ctx context.Context) error {
	agents, err := m.store.PendingEmbeddedReplyAgents(ctx)
	if err != nil {
		return err
	}
	for _, agent := range agents {
		if err := m.deliverEmbeddedReplies(ctx, agent); err != nil {
			return err
		}
	}
	return nil
}

// DeliverEmbeddedReplies is the public entry point for delivering pending replies.
func (m *Manager) DeliverEmbeddedReplies(ctx context.Context, agentID string) error {
	return m.deliverEmbeddedReplies(ctx, agentID)
}

func (m *Manager) deliverEmbeddedReplies(ctx context.Context, agentID string) error {
	replies, err := m.store.PendingEmbeddedReplies(ctx, agentID)
	if err != nil {
		return err
	}
	m.mu.Lock()
	publish := m.embeddedReplyPublisher
	m.mu.Unlock()
	if publish == nil {
		return fmt.Errorf("embedded reply publisher unavailable")
	}
	for _, reply := range replies {
		if err := publish(ctx, reply); err != nil {
			var valErr *protocol.ValidationError
			if errors.As(err, &valErr) || strings.Contains(err.Error(), "payload_too_large") || strings.Contains(err.Error(), "invalid") {
				log.Printf("[embedded_reply] Dropping/truncating oversized reply row %d for agent %s: %v", reply.RowID, agentID, err)
				if settleErr := m.store.SettleEmbeddedReply(ctx, reply); settleErr != nil {
					_ = m.store.DismissEmbeddedReply(ctx, reply.AttemptID)
				}
				continue
			}
			return err
		}
		if err := m.store.SettleEmbeddedReply(ctx, reply); err != nil {
			return err
		}
	}
	return nil
}

// Explicit OfferDirect or owner recovery retries only the frozen envelope.
// A failed publish or settlement leaves the intent pending; no SDK rerun.
func (m *Manager) flushEmbeddedReplies(ctx context.Context, agentID string) {
	_ = m.deliverEmbeddedReplies(ctx, agentID)
}
