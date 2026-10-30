package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
)

// ConversationTimelineEvent represents a single chronological event in an agent's unified timeline.
type ConversationTimelineEvent struct {
	ID        string          `json:"id"`
	AgentID   string          `json:"agent_id"`
	Seq       int64           `json:"seq"`
	Kind      string          `json:"kind"`
	Role      *string         `json:"role,omitempty"`
	Content   *string         `json:"content,omitempty"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
	CreatedAt int64           `json:"created_at"`
}

type timelineCandidate struct {
	id        string
	agentID   string
	kind      string
	role      *string
	content   *string
	metadata  string
	createdAt int64
}

// parseTimelineTimestamp parses timestamps in varying formats (RFC3339Nano,
// ISO with milliseconds, standard RFC3339) and returns epoch milliseconds.
// Messages and managed_complete_history store timestamps with differing fractional-second
// precisions; normalising them to milliseconds enables unified chronological sorting.
func parseTimelineTimestamp(ts string) int64 {
	trimmed := strings.TrimSpace(ts)
	if trimmed == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, trimmed)
	if err == nil {
		return t.UnixMilli()
	}
	t, err = time.Parse("2006-01-02T15:04:05.000Z", trimmed)
	if err == nil {
		return t.UnixMilli()
	}
	t, err = time.Parse(time.RFC3339, trimmed)
	if err == nil {
		return t.UnixMilli()
	}
	return 0
}

// migrateToConversationTimelineTx migrates historical messages and managed_complete_history
// into conversation_timeline, assigning sequential seq 1..N per agent in chronological order.
// Unifying both message delivery and lifecycle/tool execution history into a single indexed
// timeline per agent allows the UI to render a single continuous stream without multi-source merging.
// Sequential seq 1..N ensures deterministic cursor-based pagination (before_seq).
func (s *Store) migrateToConversationTimelineTx(ctx context.Context) error {
	candidatesByAgent := make(map[string][]timelineCandidate)

	// Ingest attachments from conversation_messages if the table exists
	attachmentsByMsgID := make(map[string]json.RawMessage)
	var convExists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='conversation_messages'`).Scan(&convExists); err == nil && convExists > 0 {
		attRows, err := s.db.QueryContext(ctx, `SELECT message_id, attachments_json FROM conversation_messages WHERE attachments_json IS NOT NULL AND attachments_json != '' AND attachments_json != '[]'`)
		if err == nil {
			defer attRows.Close()
			for attRows.Next() {
				var msgID, attJSON string
				if err := attRows.Scan(&msgID, &attJSON); err == nil && msgID != "" {
					attachmentsByMsgID[msgID] = json.RawMessage(attJSON)
				}
			}
		}
	}

	// 1. Ingest messages
	var messagesExists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='messages'`).Scan(&messagesExists); err == nil && messagesExists > 0 {
		rows, err := s.db.QueryContext(ctx, `SELECT id, sender, recipient, body, created_at FROM messages`)
		if err != nil {
			return fmt.Errorf("read messages for timeline migration: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var id, sender, recipient, body, createdAtStr string
			if err := rows.Scan(&id, &sender, &recipient, &body, &createdAtStr); err != nil {
				return fmt.Errorf("scan message for timeline migration: %w", err)
			}
			createdAt := parseTimelineTimestamp(createdAtStr)
			metaMap := map[string]any{
				"from": sender,
				"to":   recipient,
			}
			if att, ok := attachmentsByMsgID[id]; ok && len(att) > 0 {
				var atts any
				if json.Unmarshal(att, &atts) == nil {
					metaMap["attachments"] = atts
				}
			}
			meta, _ := json.Marshal(metaMap)
			bodyCopy := body

			// For recipient: incoming message appears as role 'user' from their perspective
			roleUser := "user"
			candidatesByAgent[recipient] = append(candidatesByAgent[recipient], timelineCandidate{
				id:        fmt.Sprintf("msg:%s:%s", recipient, id),
				agentID:   recipient,
				kind:      "message",
				role:      &roleUser,
				content:   &bodyCopy,
				metadata:  string(meta),
				createdAt: createdAt,
			})

			// For sender (if not system): outgoing message appears as role 'assistant' from their perspective
			if sender != "" && sender != "system" {
				roleAssistant := "assistant"
				candidatesByAgent[sender] = append(candidatesByAgent[sender], timelineCandidate{
					id:        fmt.Sprintf("msg:%s:%s", sender, id),
					agentID:   sender,
					kind:      "message",
					role:      &roleAssistant,
					content:   &bodyCopy,
					metadata:  string(meta),
					createdAt: createdAt,
				})
			}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate messages for timeline migration: %w", err)
		}
	}

	// 2. Ingest managed_complete_history
	var historyExists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='managed_complete_history'`).Scan(&historyExists); err == nil && historyExists > 0 {
		rows, err := s.db.QueryContext(ctx, `SELECT sequence, agent_id, category, level, message, occurred_at, presentation_json, message_content, sender_identity, tool_input_json FROM managed_complete_history WHERE agent_id IS NOT NULL AND agent_id != ''`)
		if err != nil {
			return fmt.Errorf("read managed_complete_history for timeline migration: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var seq int64
			var agentID, category, level, message, occurredAtStr string
			var presJSON, msgContent, senderID, toolInput sql.NullString
			if err := rows.Scan(&seq, &agentID, &category, &level, &message, &occurredAtStr, &presJSON, &msgContent, &senderID, &toolInput); err != nil {
				return fmt.Errorf("scan managed_complete_history for timeline migration: %w", err)
			}
			createdAt := parseTimelineTimestamp(occurredAtStr)

			metaMap := make(map[string]any)
			if presJSON.Valid && strings.TrimSpace(presJSON.String) != "" {
				var p map[string]any
				if json.Unmarshal([]byte(presJSON.String), &p) == nil {
					for k, v := range p {
						metaMap[k] = v
					}
				}
			}
			if toolInput.Valid && strings.TrimSpace(toolInput.String) != "" {
				var ti any
				if json.Unmarshal([]byte(toolInput.String), &ti) == nil {
					metaMap["tool_input"] = ti
				}
			}
			if senderID.Valid && senderID.String != "" {
				metaMap["sender"] = senderID.String
			}
			metaMap["category"] = category
			metaMap["level"] = level
			metaBytes, _ := json.Marshal(metaMap)

			kind := "history"
			var role *string
			presTo, _ := metaMap["to"].(string)
			if presTo == "" {
				presTo, _ = metaMap["recipient"].(string)
			}
			presKind, _ := metaMap["kind"].(string)
			isAssistantTurn := category == "assistant" || (presKind == "message" && (strings.EqualFold(presTo, "user") || presTo == ""))
			if isAssistantTurn {
				kind = "message"
				roleAssistant := "assistant"
				role = &roleAssistant
			} else if category == "managed_tool" || category == "managed_builtin_tool" {
				kind = "tool"
			}

			var content *string
			if msgContent.Valid && msgContent.String != "" {
				c := msgContent.String
				content = &c
			} else if message != "" {
				c := message
				content = &c
			}

			candidatesByAgent[agentID] = append(candidatesByAgent[agentID], timelineCandidate{
				id:        fmt.Sprintf("hist:%d", seq),
				agentID:   agentID,
				kind:      kind,
				role:      role,
				content:   content,
				metadata:  string(metaBytes),
				createdAt: createdAt,
			})
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate managed_complete_history for timeline migration: %w", err)
		}
	}

	// 3. For each agent, sort chronologically, deduplicate, and insert with seq 1..N.
	// Stable ascending seq numbering guarantees predictable, gapless cursor pagination.
	for agentID, items := range candidatesByAgent {
		// Sort chronologically ascending
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].createdAt != items[j].createdAt {
				return items[i].createdAt < items[j].createdAt
			}
			return items[i].id < items[j].id
		})

		seenIDs := make(map[string]bool)
		var seq int64 = 1
		for _, item := range items {
			if seenIDs[item.id] {
				continue
			}
			seenIDs[item.id] = true

			var contentVal any
			if item.content != nil {
				contentVal = *item.content
			}
			var roleVal any
			if item.role != nil {
				roleVal = *item.role
			}
			var metaVal any
			if item.metadata != "" && item.metadata != "{}" {
				metaVal = item.metadata
			}

			_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO conversation_timeline (id, agent_id, seq, kind, role, content, metadata, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				item.id, agentID, seq, item.kind, roleVal, contentVal, metaVal, item.createdAt)
			if err != nil {
				return fmt.Errorf("insert migrated timeline event for agent %s seq %d: %w", agentID, seq, err)
			}
			seq++
		}
	}

	return nil
}

// RecordConversationTimelineTx writes an event to conversation_timeline within an open transaction,
// atomically calculating the sequence number in SQL:
// COALESCE((SELECT MAX(seq) FROM conversation_timeline WHERE agent_id = ?), 0) + 1
// Uses ON CONFLICT(id) DO UPDATE to remain idempotent across retried message deliveries or history sweeps.
func (s *Store) RecordConversationTimelineTx(ctx context.Context, id, agentID, kind string, role, content *string, metadata json.RawMessage, createdAt int64) (*ConversationTimelineEvent, error) {
	if agentID == "" {
		return nil, errors.New("agent_id is required for conversation timeline")
	}
	if kind == "" {
		kind = "event"
	}
	if createdAt == 0 {
		createdAt = s.clock.Now().UnixMilli()
	}

	var metaStr any
	if len(metadata) > 0 && string(metadata) != "null" && string(metadata) != "{}" {
		metaStr = string(metadata)
	}
	var roleVal any
	if role != nil {
		roleVal = *role
	}
	var contentVal any
	if content != nil {
		contentVal = *content
	}

	if id == "" {
		newID, err := s.ids.Next()
		if err != nil {
			return nil, fmt.Errorf("generate id for timeline event: %w", err)
		}
		id = newID
	}

	_, err := s.db.ExecContext(ctx, `INSERT INTO conversation_timeline (id, agent_id, seq, kind, role, content, metadata, created_at)
		VALUES (?, ?, COALESCE((SELECT MAX(seq) FROM conversation_timeline WHERE agent_id = ?), 0) + 1, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			role = excluded.role,
			kind = excluded.kind,
			content = excluded.content,
			metadata = excluded.metadata`,
		id, agentID, agentID, kind, roleVal, contentVal, metaStr, createdAt)
	if err != nil {
		return nil, fmt.Errorf("insert timeline event: %w", err)
	}

	var actualSeq int64
	err = s.db.QueryRowContext(ctx, `SELECT seq FROM conversation_timeline WHERE id = ?`, id).Scan(&actualSeq)
	if err != nil {
		return nil, fmt.Errorf("query seq for timeline event: %w", err)
	}

	return &ConversationTimelineEvent{
		ID:        id,
		AgentID:   agentID,
		Seq:       actualSeq,
		Kind:      kind,
		Role:      role,
		Content:   content,
		Metadata:  metadata,
		CreatedAt: createdAt,
	}, nil
}

// recordTimelineMessageTx hook is invoked during Store.enqueue to project durable
// message deliveries into the unified conversation_timeline. It generates timeline
// entries for both recipient (as incoming/user) and sender (as outgoing/assistant)
// so either agent's timeline independently reflects the conversation event.
func (s *Store) recordTimelineMessageTx(ctx context.Context, msg domain.Message, source protocol.MessageSource) error {
	// Verify conversation_timeline table exists
	var tableExists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='conversation_timeline'`).Scan(&tableExists); err != nil || tableExists == 0 {
		return nil
	}

	createdAt := parseTimelineTimestamp(msg.CreatedAt)
	if createdAt == 0 {
		createdAt = s.clock.Now().UnixMilli()
	}

	metaMap := map[string]any{
		"from":        msg.From,
		"to":          msg.To,
		"subject":     msg.Subject,
		"source_kind": source.Kind,
	}
	if len(msg.Attachments) > 0 {
		metaMap["attachments"] = msg.Attachments
	}
	meta, _ := json.Marshal(metaMap)

	body := msg.Body
	// Event for recipient
	recipID := fmt.Sprintf("msg:%s:%s", msg.To, msg.ID)
	roleUser := "user"
	if _, err := s.RecordConversationTimelineTx(ctx, recipID, msg.To, "message", &roleUser, &body, meta, createdAt); err != nil {
		return err
	}

	// Event for sender if sender is an agent
	if msg.From != "" && msg.From != "system" {
		senderID := fmt.Sprintf("msg:%s:%s", msg.From, msg.ID)
		roleAssistant := "assistant"
		if _, err := s.RecordConversationTimelineTx(ctx, senderID, msg.From, "message", &roleAssistant, &body, meta, createdAt); err != nil {
			return err
		}
	}

	return nil
}

// recordTimelineHistoryTx hook is called during managed_complete_history recording
// to project execution events (tool invocations, thinking, context resets) into the timeline.
// It detects and skips user messages that are already recorded via recordTimelineMessageTx
// to prevent duplicate message bubbles in the unified feed.
func (s *Store) recordTimelineHistoryTx(ctx context.Context, agentID, category, level, message string, entry ManagedHistoryEntry) error {
	if agentID == "" {
		return nil
	}
	// Verify conversation_timeline table exists
	var tableExists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='conversation_timeline'`).Scan(&tableExists); err != nil || tableExists == 0 {
		return nil
	}

	// Avoid double-inserting incoming user messages that are already recorded via recordTimelineMessageTx
	// (e.g. category "managed_message", "reminder" or presentation.kind "message"/"reminder" when From is user/external).
	isUserMsg := category == "managed_message" || (entry.Presentation != nil && entry.Presentation.Kind == "message")
	fromUser := false
	fromIdentity := ""
	if entry.SenderIdentity != nil && *entry.SenderIdentity != "" {
		fromIdentity = *entry.SenderIdentity
	} else if entry.Presentation != nil {
		if entry.Presentation.From != "" {
			fromIdentity = entry.Presentation.From
		}
	}
	if fromIdentity != "" {
		sId := strings.ToLower(strings.TrimSpace(fromIdentity))
		if sId == "user" || sId != strings.ToLower(strings.TrimSpace(agentID)) {
			fromUser = true
		}
	} else if message == "Message received" || message == "User message" || message == "Reminder received" {
		fromUser = true
	}

	if isUserMsg && fromUser {
		// Check if a message event with matching content or event key already exists for this agent
		var contentToCheck string
		if entry.MessageContent != nil {
			contentToCheck = *entry.MessageContent
		} else {
			contentToCheck = message
		}
		var exists int
		_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversation_timeline WHERE agent_id = ? AND kind = 'message' AND content = ?`, agentID, contentToCheck).Scan(&exists)
		if exists > 0 {
			return nil
		}
	}

	createdAt := parseTimelineTimestamp(entry.At)
	if createdAt == 0 {
		createdAt = s.clock.Now().UnixMilli()
	}

	metaMap := make(map[string]any)
	if entry.Presentation != nil {
		pBytes, _ := json.Marshal(entry.Presentation)
		_ = json.Unmarshal(pBytes, &metaMap)
	}
	if len(entry.ToolInput) > 0 && string(entry.ToolInput) != "null" {
		var ti any
		if json.Unmarshal(entry.ToolInput, &ti) == nil {
			metaMap["tool_input"] = ti
		}
	}
	if entry.SenderIdentity != nil && *entry.SenderIdentity != "" {
		metaMap["sender"] = *entry.SenderIdentity
	}
	metaMap["category"] = category
	metaMap["level"] = level
	metaBytes, _ := json.Marshal(metaMap)

	kind := "history"
	var role *string
	presTo := ""
	presKind := ""
	if entry.Presentation != nil {
		presTo = entry.Presentation.To
		if presTo == "" {
			presTo, _ = metaMap["recipient"].(string)
		}
		presKind = entry.Presentation.Kind
	} else {
		if rec, ok := metaMap["recipient"].(string); ok && rec != "" {
			presTo = rec
		} else if toVal, ok := metaMap["to"].(string); ok && toVal != "" {
			presTo = toVal
		}
	}
	isAssistantTurn := category == "assistant" || (presKind == "message" && (strings.EqualFold(presTo, "user") || presTo == ""))
	if isAssistantTurn {
		kind = "message"
		roleAssistant := "assistant"
		role = &roleAssistant
	} else if category == "managed_tool" || category == "managed_builtin_tool" {
		kind = "tool"
	}

	var content *string
	if entry.MessageContent != nil && *entry.MessageContent != "" {
		content = entry.MessageContent
	} else if message != "" {
		content = &message
	}

	// Avoid double-inserting assistant turns that are already recorded via recordTimelineMessageTx
	if isAssistantTurn && content != nil {
		var exists int
		_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversation_timeline WHERE agent_id = ? AND kind = 'message' AND role = 'assistant' AND content = ?`, agentID, *content).Scan(&exists)
		if exists > 0 {
			return nil
		}
	}

	id := fmt.Sprintf("hist:%s", entry.ID)
	if entry.ID == "" {
		id = ""
	}

	_, err := s.RecordConversationTimelineTx(ctx, id, agentID, kind, role, content, metaBytes, createdAt)
	return err
}

// GetConversationTimeline retrieves events from conversation_timeline for an agent with pagination.
// Pagination uses `seq < beforeSeq ORDER BY seq DESC LIMIT limit + 1`:
// - Limit is clamped between 1 and 500 (default 50).
// - Querying limit + 1 allows determining if older events exist (next_before_seq).
// - Events are reversed to return chronological ascending order (oldest to newest)
//   so callers can directly append/render the page.
func (s *Store) GetConversationTimeline(ctx context.Context, agentID string, limit int, beforeSeq *int64) ([]ConversationTimelineEvent, *int64, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	query := `SELECT id, agent_id, seq, kind, role, content, metadata, created_at FROM conversation_timeline WHERE agent_id = ?`
	args := []any{agentID}

	if beforeSeq != nil && *beforeSeq > 0 {
		query += ` AND seq < ?`
		args = append(args, *beforeSeq)
	}

	query += ` ORDER BY seq DESC LIMIT ?`
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("query conversation timeline: %w", err)
	}
	defer rows.Close()

	var events []ConversationTimelineEvent
	for rows.Next() {
		var ev ConversationTimelineEvent
		var role, content, metadata sql.NullString
		if err := rows.Scan(&ev.ID, &ev.AgentID, &ev.Seq, &ev.Kind, &role, &content, &metadata, &ev.CreatedAt); err != nil {
			return nil, nil, fmt.Errorf("scan conversation timeline event: %w", err)
		}
		if role.Valid {
			ev.Role = &role.String
		}
		if content.Valid {
			ev.Content = &content.String
		}
		if metadata.Valid && strings.TrimSpace(metadata.String) != "" {
			ev.Metadata = json.RawMessage(metadata.String)
		}
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate conversation timeline events: %w", err)
	}

	var nextBeforeSeq *int64
	if len(events) > limit {
		nextSeq := events[limit-1].Seq
		nextBeforeSeq = &nextSeq
		events = events[:limit]
	}

	// Reverse events to be in chronological order (ascending seq)
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}

	return events, nextBeforeSeq, nil
}
