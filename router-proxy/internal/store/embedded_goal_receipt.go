package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/sky-valley/pi/ai"
)

// ErrEmbeddedGoalSDKResultUnchecked leaves the desired policy pending. It is
// not a provider failure or permission to replay the original tool call.
var ErrEmbeddedGoalSDKResultUnchecked = errors.New("pending embedded SDK goal has no checked matching result")

// EmbeddedGoalAppliedReceiptRequest identifies the checked user entry that
// originated this SDK turn. Call only immediately before the next provider
// request, never when merely changing the desired policy.
type EmbeddedGoalAppliedReceiptRequest struct {
	TurnKey         string
	TurnEntrySeq    int64
	TurnEntryDigest string
	GoalRevision    int64
}

// EmbeddedGoalAppliedReceipt is durable, immutable provider-request evidence.
type EmbeddedGoalAppliedReceipt struct {
	EmbeddedGoalAppliedReceiptRequest
	AgentID, SessionID  string
	Epoch               int64
	Generation, Role    string
	GoalEnabled         bool
	GoalText, AppliedAt string
}

// CommitEmbeddedGoalAppliedReceipt atomically checks live owner, saved runtime,
// role, exact user turn and desired revision, writes one immutable receipt and
// advances a pending projection or observes an already applied enabled policy
// for a new provider turn. Observation and identical replay do not update policy
// or bump the projection version. A stale or superseded call fails closed.
func (s *Store) CommitEmbeddedGoalAppliedReceipt(ctx context.Context, b EmbeddedBinding, request EmbeddedGoalAppliedReceiptRequest) (EmbeddedGoalAppliedReceipt, bool, error) {
	var receipt EmbeddedGoalAppliedReceipt
	if ctx == nil || request.TurnKey == "" || len(request.TurnKey) > 256 || !utf8.ValidString(request.TurnKey) || strings.ContainsAny(request.TurnKey, "\x00\r\n") || request.TurnEntrySeq <= 0 || request.TurnEntryDigest == "" || request.GoalRevision <= 0 {
		return receipt, false, errors.New("invalid embedded goal receipt")
	}
	replay := false
	err := s.runImmediate(ctx, func() error {
		if err := s.embeddedEvidenceReplayTx(ctx, b, EmbeddedGoalEvaluationKey{UserSeq: request.TurnEntrySeq, UserDigest: request.TurnEntryDigest, GoalRevision: request.GoalRevision}, ""); err != nil {
			return err
		}
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		var role string
		if err := s.db.QueryRowContext(ctx, `SELECT role FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&role); err != nil {
			return err
		}
		if !validEmbeddedRole(role) {
			return errors.New("invalid saved embedded role")
		}
		var entry EmbeddedEntry
		entry.Sequence = request.TurnEntrySeq
		if err := s.db.QueryRowContext(ctx, `SELECT kind,body,parent_digest,digest FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND seq=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, request.TurnEntrySeq).Scan(&entry.Kind, &entry.Body, &entry.ParentDigest, &entry.Digest); err != nil {
			return err
		}
		if entry.Kind != "user" || entry.Digest != request.TurnEntryDigest || embeddedBody(entry.Body) != nil || embeddedEntryDigest(b, entry) != entry.Digest {
			return errors.New("receipt turn origin mismatch")
		}
		var desired, applied int64
		var enabled int
		var goal, projection string
		if err := s.db.QueryRowContext(ctx, `SELECT desired_revision,applied_revision,goal_enabled,goal_text,projection_state FROM managed_agent_policies WHERE agent_id=?`, b.AgentID).Scan(&desired, &applied, &enabled, &goal, &projection); err != nil {
			return err
		}
		if desired != request.GoalRevision || (enabled != 0 && enabled != 1) {
			return errors.New("stale embedded goal revision")
		}
		if err := s.embeddedGoalReceiptProviderTurn(ctx, b, entry); err != nil {
			return err
		}
		// A turn can contain more than one goal change. Each revision needs
		// its own immutable receipt at its next provider-request boundary.
		var existing EmbeddedGoalAppliedReceipt
		var storedEnabled int
		err := s.db.QueryRowContext(ctx, `SELECT turn_key,agent_id,session_id,epoch,generation,role,turn_entry_seq,turn_entry_digest,goal_revision,goal_enabled,goal_text,applied_at FROM embedded_goal_applied_receipts WHERE turn_key=? OR (agent_id=? AND session_id=? AND epoch=? AND generation=? AND turn_entry_seq=? AND goal_revision=?)`, request.TurnKey, b.AgentID, b.SessionID, b.Epoch, b.Generation, request.TurnEntrySeq, request.GoalRevision).Scan(&existing.TurnKey, &existing.AgentID, &existing.SessionID, &existing.Epoch, &existing.Generation, &existing.Role, &existing.TurnEntrySeq, &existing.TurnEntryDigest, &existing.GoalRevision, &storedEnabled, &existing.GoalText, &existing.AppliedAt)
		if err == nil {
			existing.GoalEnabled = storedEnabled == 1
			if existing.TurnKey != request.TurnKey || existing.AgentID != b.AgentID || existing.SessionID != b.SessionID || existing.Epoch != b.Epoch || existing.Generation != b.Generation || existing.Role != role || existing.TurnEntrySeq != request.TurnEntrySeq || existing.TurnEntryDigest != request.TurnEntryDigest || existing.GoalRevision != request.GoalRevision || storedEnabled != enabled || existing.GoalText != goal || applied != desired || projection != "applied" {
				return errors.New("conflicting embedded goal receipt")
			}
			receipt = existing
			replay = true
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		observation := applied == desired && projection == "applied" && enabled == 1
		if !observation && (applied >= desired || projection != "pending") {
			return errors.New("embedded goal revision is not pending or active")
		}
		if !observation {
			if err := s.embeddedGoalReceiptProducer(ctx, b, role, request.GoalRevision); err != nil {
				return err
			}
		}
		receipt = EmbeddedGoalAppliedReceipt{EmbeddedGoalAppliedReceiptRequest: request, AgentID: b.AgentID, SessionID: b.SessionID, Epoch: b.Epoch, Generation: b.Generation, Role: role, GoalEnabled: enabled == 1, GoalText: goal, AppliedAt: formatTimestamp(s.clock.Now())}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO embedded_goal_applied_receipts(turn_key,agent_id,session_id,epoch,generation,role,turn_entry_seq,turn_entry_digest,goal_revision,goal_enabled,goal_text,applied_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, request.TurnKey, b.AgentID, b.SessionID, b.Epoch, b.Generation, role, request.TurnEntrySeq, request.TurnEntryDigest, desired, enabled, goal, receipt.AppliedAt); err != nil {
			return err
		}
		if observation {
			return nil // Current-turn evidence only: no projection write or bump.
		}
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agent_policies SET applied_revision=desired_revision,projection_state='applied',projection_attempts=0,next_projection_at=NULL,last_error_code=NULL,last_error_message=NULL,last_error_at=NULL,updated_at=? WHERE agent_id=? AND desired_revision=? AND applied_revision<desired_revision AND projection_state='pending'`, receipt.AppliedAt, b.AgentID, desired)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return errors.New("stale embedded goal projection")
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
	if err != nil {
		return EmbeddedGoalAppliedReceipt{}, false, err
	}
	return receipt, replay, nil
}

// Call inside the receipt transaction after the owner/runtime fence. The user
// must still own the provider turn, with either its checked mail ACK or its
// non-mail provider-entry marker. Awareness and completed turns are not work.
func (s *Store) embeddedGoalReceiptProviderTurn(ctx context.Context, b EmbeddedBinding, user EmbeddedEntry) error {
	message, err := ai.UnmarshalMessage(user.Body)
	if err != nil || message.MessageRole() != ai.RoleUser {
		return errors.New("goal receipt requires checked SDK user")
	}
	var eligible int
	err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries e
 WHERE e.agent_id=? AND e.session_id=? AND e.epoch=? AND e.generation=? AND e.seq=? AND e.digest=? AND e.kind='user'
 AND NOT EXISTS (SELECT 1 FROM embedded_entries later WHERE later.agent_id=e.agent_id AND later.session_id=e.session_id AND later.epoch=e.epoch AND later.seq>e.seq AND (later.kind='user' OR (later.kind='assistant' AND json_extract(later.body,'$.stopReason')='stop')))
 AND NOT EXISTS (SELECT 1 FROM embedded_interrupted_notices n WHERE n.agent_id=e.agent_id AND n.session_id=e.session_id AND n.epoch=e.epoch AND n.generation=e.generation AND n.entry_seq=e.seq)
 AND (EXISTS (SELECT 1 FROM embedded_nonmail_provider_entries p WHERE p.agent_id=e.agent_id AND p.session_id=e.session_id AND p.epoch=e.epoch AND p.generation=e.generation AND p.user_seq=e.seq AND p.user_digest=e.digest)
 OR EXISTS (SELECT 1 FROM embedded_acks k JOIN embedded_attempts a ON a.attempt_id=k.attempt_id WHERE k.agent_id=e.agent_id AND k.session_id=e.session_id AND k.epoch=e.epoch AND k.generation=e.generation AND k.entry_seq=e.seq AND a.agent_id=k.agent_id AND a.session_id=k.session_id AND a.epoch=k.epoch AND a.generation=k.generation AND a.message_id=k.message_id))`, b.AgentID, b.SessionID, b.Epoch, b.Generation, user.Sequence, user.Digest).Scan(&eligible)
	if err != nil {
		return err
	}
	if eligible != 1 {
		return errors.New("goal receipt requires current checked provider user")
	}
	return nil
}

// The only desired-revision writers are UpdateManagedGoalPolicy (API/control)
// and applyEmbeddedGoalCall. The latter atomically saves a set/off call's exact
// revision snapshot in the same transaction as the policy change. Status does
// not produce revisions. Search ALL retained bindings, not just this session:
// absence of a mutation snapshot is the non-SDK writer, not a timestamp guess.
// Goal call rows are immutable and cascade only with their managed identity.
func (s *Store) embeddedGoalReceiptProducer(ctx context.Context, b EmbeddedBinding, role string, revision int64) error {
	rows, err := s.db.QueryContext(ctx, `SELECT call_id,session_id,epoch,generation,role,action,args_json,state,result_json FROM embedded_goal_calls WHERE agent_id=? AND action IN ('set','off')`, b.AgentID)
	if err != nil {
		return err
	}
	type producer struct {
		id, session, generation, role, action, args, result string
		epoch                                               int64
		policy                                              ManagedGoalPolicy
	}
	var origin *producer
	for rows.Next() {
		var p producer
		var state string
		var result sql.NullString
		if err := rows.Scan(&p.id, &p.session, &p.epoch, &p.generation, &p.role, &p.action, &p.args, &state, &result); err != nil {
			_ = rows.Close()
			return err
		}
		if state != "complete" {
			continue // No policy mutation survives an unknown call transaction.
		}
		if !result.Valid || json.Unmarshal([]byte(result.String), &p.policy) != nil || p.policy.AgentID != b.AgentID || p.policy.DesiredRevision <= 0 {
			_ = rows.Close()
			return ErrEmbeddedGoalSDKResultUnchecked // Cannot classify this producer.
		}
		if p.policy.DesiredRevision != revision {
			continue
		}
		if origin != nil {
			_ = rows.Close()
			return ErrEmbeddedGoalSDKResultUnchecked
		}
		p.result = result.String
		origin = &p
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	if origin == nil {
		return nil // Legitimate API/control revision; no SDK result is required.
	}
	p := origin
	if p.session != b.SessionID || p.epoch != b.Epoch || p.generation != b.Generation || p.role != role {
		return ErrEmbeddedGoalSDKResultUnchecked
	}
	current, err := scanManagedGoalPolicy(s.db.QueryRowContext(ctx, `SELECT `+managedGoalPolicyColumns+` FROM managed_agent_policies WHERE agent_id=?`, b.AgentID))
	if err != nil {
		return err
	}
	want, _ := json.Marshal(current.Config)
	got, _ := json.Marshal(p.policy.Config)
	if !bytes.Equal(want, got) || current.HistoryScopeID != p.policy.HistoryScopeID {
		return ErrEmbeddedGoalSDKResultUnchecked
	}
	var intent []byte
	if err := s.db.QueryRowContext(ctx, `SELECT intent FROM embedded_effects WHERE effect_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, p.id, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&intent); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrEmbeddedGoalSDKResultUnchecked
		}
		return err
	}
	var call ai.ToolCall
	var command EmbeddedGoalCommand
	if json.Unmarshal(intent, &call) != nil || json.Unmarshal([]byte(p.args), &command) != nil || call.ID != p.id || call.Name != "goal" || command.Action != p.action {
		return ErrEmbeddedGoalSDKResultUnchecked
	}
	if command.Action == "set" {
		args := map[string]any{"action": command.Action, "goal": command.Goal}
		want, _ := json.Marshal(args)
		got, _ := json.Marshal(call.Arguments)
		settings, _ := json.Marshal(command.Settings)
		policySettings, _ := json.Marshal(p.policy.Config.Settings)
		if !bytes.Equal(want, got) || command.Goal != p.policy.Config.Goal || !p.policy.Config.Enabled || !bytes.Equal(settings, policySettings) {
			return ErrEmbeddedGoalSDKResultUnchecked
		}
	} else if command.Action == "off" {
		if command.Goal != "" || command.Settings != (ManagedGoalSettings{}) || p.policy.Config.Enabled || p.policy.Config.Goal != "" {
			return ErrEmbeddedGoalSDKResultUnchecked
		}
		act, _ := call.Arguments["action"].(string)
		if act != "off" {
			return ErrEmbeddedGoalSDKResultUnchecked
		}
		for k, v := range call.Arguments {
			if k != "action" && k != "goal" {
				return ErrEmbeddedGoalSDKResultUnchecked
			}
			if k == "goal" && v != nil {
				if _, ok := v.(string); !ok {
					return ErrEmbeddedGoalSDKResultUnchecked
				}
			}
		}
	}
	// Validate that the session has the checked producer and successful SDK result.
	// Do not fail closed over historical digest mismatches on past messages.
	entries, err := s.db.QueryContext(ctx, `SELECT seq,kind,body FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? ORDER BY seq`, b.AgentID, b.SessionID, b.Epoch, b.Generation)
	if err != nil {
		return err
	}
	defer entries.Close()
	var userSeq, producerUser int64
	var producerFound, resultFound bool
	for entries.Next() {
		var e EmbeddedEntry
		if err := entries.Scan(&e.Sequence, &e.Kind, &e.Body); err != nil {
			return err
		}
		message, err := ai.UnmarshalMessage(e.Body)
		if err != nil || string(message.MessageRole()) != e.Kind {
			continue
		}
		if e.Kind == "user" {
			userSeq = e.Sequence
		}
		switch message := message.(type) {
		case ai.AssistantMessage:
			for _, content := range message.Content {
				if checked, ok := content.(ai.ToolCall); ok && checked.ID == p.id {
					body, _ := json.Marshal(checked)
					if producerFound || userSeq == 0 || message.StopReason != ai.StopToolUse || !bytes.Equal(body, intent) {
						return ErrEmbeddedGoalSDKResultUnchecked
					}
					producerFound, producerUser = true, userSeq
				}
			}
		case ai.ToolResultMessage:
			if message.ToolCallID != p.id {
				continue
			}
			if !producerFound || resultFound || userSeq != producerUser || message.ToolName != "goal" || message.IsError || message.Timestamp <= 0 || len(message.Content) != 1 {
				return ErrEmbeddedGoalSDKResultUnchecked
			}
			text, ok := message.Content[0].(ai.TextContent)
			if !ok || text.Text != p.result {
				return ErrEmbeddedGoalSDKResultUnchecked
			}
			resultFound = true
		}
	}
	if err := entries.Err(); err != nil {
		return err
	}
	if !resultFound {
		return ErrEmbeddedGoalSDKResultUnchecked
	}
	return nil
}
