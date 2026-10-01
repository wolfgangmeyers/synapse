package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/sky-valley/pi/ai"
)

// embeddedGoalContinuationHead runs only in the start's immediate transaction.
// It is deliberately not a general descendant walker. Lost retained evidence
// denies the exception; it never changes the original evaluation identity.
func (s *Store) embeddedGoalContinuationHead(ctx context.Context, b EmbeddedBinding, k EmbeddedGoalEvaluationKey) error {
	if err := s.embeddedGoalEvaluationGate(ctx, b, k, false); err != nil {
		return err
	}
	var running int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM managed_agents WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND runtime_generation=? AND runtime='embedded' AND desired_state='running' AND lifecycle_state='running'`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&running); err != nil {
		return err
	}
	if running != 1 {
		return errors.New("continuation requires live running selection")
	}
	e, err := s.embeddedEvaluationRow(ctx, b, k)
	if err != nil {
		return err
	}
	if e.Generation != b.Generation || e.EmbeddedGoalEvaluationKey != k || e.State != EmbeddedGoalEvaluationNotDone || e.CompletedAt == "" {
		return errors.New("continuation requires original completed NOT_DONE evaluation")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT seq,parent_digest,kind,body,digest FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? ORDER BY seq`, b.AgentID, b.SessionID, b.Epoch)
	if err != nil {
		return err
	}
	var entries []EmbeddedEntry
	previous := embeddedGenesis
	for rows.Next() {
		var entry EmbeddedEntry
		if err = rows.Scan(&entry.Sequence, &entry.ParentDigest, &entry.Kind, &entry.Body, &entry.Digest); err != nil {
			break
		}
		if entry.Sequence != int64(len(entries)+1) || entry.ParentDigest != previous || entry.Kind == "" || embeddedBody(entry.Body) != nil || embeddedEntryDigest(b, entry) != entry.Digest {
			err = errors.New("corrupt continuation entry chain")
			break
		}
		entries = append(entries, entry)
		previous = entry.Digest
	}
	if err == nil {
		err = rows.Err()
	}
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if int64(len(entries)) != k.FinalSeq && int64(len(entries)) != k.FinalSeq+2 {
		return errors.New("continuation head superseded")
	}
	neutral := int64(len(entries)) == k.FinalSeq+2
	checkpoints, err := s.db.QueryContext(ctx, `SELECT at_seq,ancestor_digest,body,digest FROM embedded_checkpoints WHERE agent_id=? AND session_id=? AND epoch=?`, b.AgentID, b.SessionID, b.Epoch)
	if err != nil {
		return err
	}
	for checkpoints.Next() {
		var c EmbeddedCheckpoint
		if err = checkpoints.Scan(&c.Sequence, &c.AncestorDigest, &c.Body, &c.Digest); err != nil {
			break
		}
		if c.Sequence <= 0 || c.Sequence > int64(len(entries)) || entries[c.Sequence-1].Digest != c.AncestorDigest || embeddedBody(c.Body) != nil || embeddedCheckpointDigest(b, c) != c.Digest || (neutral && c.Sequence > k.FinalSeq) {
			err = errors.New("continuation checkpoint ancestry denied")
			break
		}
	}
	if err == nil {
		err = checkpoints.Err()
	}
	closeErr = checkpoints.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if !embeddedCheckedStop(entries[k.FinalSeq-1].Kind, entries[k.FinalSeq-1].Body) {
		return errors.New("continuation original final is not checked stop")
	}
	if !neutral {
		// The exact original head is not authority to retry unresolved effects.
		// Keep its existing observed-effect behavior, but deny every UNKNOWN.
		var unresolved int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_effects WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND state='unknown'`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&unresolved); err != nil {
			return err
		}
		if unresolved != 0 {
			return errors.New("continuation unproven effect lineage")
		}
		return nil // Effect-free/observed original head retains the existing gate.
	}
	user, final := entries[k.FinalSeq], entries[k.FinalSeq+1]
	if user.Kind != "user" || user.ParentDigest != k.FinalDigest || !embeddedGoalNeutralUser(user.Body) || final.ParentDigest != user.Digest || !embeddedCheckedStop(final.Kind, final.Body) {
		return errors.New("continuation neutral pair denied")
	}
	var authority int
	err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_restart_wake_slots w
 JOIN embedded_sessions s ON s.agent_id=w.agent_id AND s.session_id=w.session_id AND s.epoch=w.epoch AND s.generation=w.runtime_generation AND s.current=1 AND s.owner_token=w.claim_owner_token
 JOIN embedded_restart_wake_checked_origins o ON o.agent_id=w.agent_id AND o.slot=w.slot AND o.session_id=w.session_id AND o.epoch=w.epoch AND o.runtime_generation=w.runtime_generation AND o.wake_id=w.wake_id AND o.process_generation=w.process_generation AND o.claim_owner_token=w.claim_owner_token
 JOIN embedded_process_generation_counter g ON g.singleton=1 AND g.generation=w.process_generation
 JOIN embedded_nonmail_provider_entries p ON p.agent_id=w.agent_id AND p.session_id=w.session_id AND p.epoch=w.epoch AND p.generation=w.runtime_generation AND p.user_seq=? AND p.user_digest=?
 WHERE w.agent_id=? AND w.session_id=? AND w.epoch=? AND w.runtime_generation=? AND w.slot='current' AND w.state='checked-complete' AND w.notice_id IS NULL AND w.digest=? AND w.claim_owner_token=? AND o.seq=?
 AND ((s.role='root' AND w.kind='root-idle') OR (s.role='worker' AND w.kind='worker-idle'))
 AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.recipient=w.agent_id AND m.state='queued')
 AND NOT EXISTS (SELECT 1 FROM embedded_acks a WHERE a.agent_id=w.agent_id AND a.session_id=w.session_id AND a.epoch=w.epoch AND a.entry_seq>?)
 AND NOT EXISTS (SELECT 1 FROM embedded_attempts a WHERE a.agent_id=w.agent_id AND a.session_id=w.session_id AND a.epoch=w.epoch AND NOT EXISTS (SELECT 1 FROM embedded_acks k WHERE k.attempt_id=a.attempt_id))
 AND NOT EXISTS (SELECT 1 FROM embedded_reply_outbox r JOIN embedded_attempts a ON a.attempt_id=r.attempt_id WHERE a.agent_id=w.agent_id AND a.session_id=w.session_id AND a.epoch=w.epoch AND r.state='pending')
 AND NOT EXISTS (SELECT 1 FROM embedded_prompt_requests r WHERE r.agent_id=w.agent_id AND r.session_id=w.session_id AND r.epoch=w.epoch AND r.state='pending')
 AND NOT EXISTS (SELECT 1 FROM embedded_own_compaction_requests c WHERE c.agent_id=w.agent_id AND c.session_id=w.session_id AND c.epoch=w.epoch AND c.head_seq>?)
 AND NOT EXISTS (SELECT 1 FROM embedded_interrupted_notices n WHERE n.agent_id=w.agent_id AND n.session_id=w.session_id AND n.epoch=w.epoch AND n.generation=w.runtime_generation)`, user.Sequence, user.Digest, b.AgentID, b.SessionID, b.Epoch, b.Generation, k.FinalDigest, b.OwnerToken, final.Sequence, k.FinalSeq, k.FinalSeq).Scan(&authority)
	if err != nil {
		return err
	}
	if authority != 1 {
		return errors.New("continuation retained neutral authority denied")
	}
	// Effects have no turn sequence foreign key. Prove every saved effect is
	// settled and belongs to a checked transcript call/result before
	// the original final. Never infer ownership from time or neutral prompt text.
	calls, results := map[string][]byte{}, map[string][]byte{}
	for _, entry := range entries[:k.FinalSeq] {
		message, parseErr := ai.UnmarshalMessage(entry.Body)
		if parseErr != nil {
			continue
		}
		switch m := message.(type) {
		case ai.AssistantMessage:
			if entry.Kind != "assistant" {
				return errors.New("continuation effect call kind mismatch")
			}
			for _, block := range m.Content {
				if call, ok := block.(ai.ToolCall); ok {
					if call.ID == "" || calls[call.ID] != nil {
						return errors.New("continuation ambiguous effect call")
					}
					encoded, marshalErr := json.Marshal(call)
					if marshalErr != nil {
						return marshalErr
					}
					calls[call.ID] = encoded
				}
			}
		case ai.ToolResultMessage:
			if entry.Kind != "toolResult" || calls[m.ToolCallID] == nil || results[m.ToolCallID] != nil {
				return errors.New("continuation ambiguous effect result")
			}
			results[m.ToolCallID] = entry.Body
		}
	}
	// Completed DOMAIN receipts do not retain the original owner/process/attempt
	// fence. Matching SDK bytes and the current wake cannot supply that missing
	// attribution. UNKNOWN effects therefore deny the neutral-head exception.
	effects, err := s.db.QueryContext(ctx, `SELECT effect_id,state,intent,result FROM embedded_effects WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation)
	if err != nil {
		return err
	}
	for effects.Next() {
		var id, state string
		var intent, result []byte
		if err = effects.Scan(&id, &state, &intent, &result); err != nil {
			break
		}
		settled := state == "observed" && bytes.Equal(result, results[id])
		if !settled || calls[id] == nil || results[id] == nil || !bytes.Equal(intent, calls[id]) {
			err = errors.New("continuation unproven effect lineage")
			break
		}
	}
	if err == nil {
		err = effects.Err()
	}
	closeErr = effects.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func embeddedGoalNeutralUser(body []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return false
	}
	for name := range fields {
		if name != "role" && name != "content" && name != "timestamp" {
			return false
		}
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(fields["content"], &blocks) != nil || len(blocks) != 1 || len(blocks[0]) != 2 {
		return false
	}
	var user struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(body, &user) != nil || user.Role != "user" || len(user.Content) != 1 {
		return false
	}
	return user.Content[0].Type == "text" && user.Content[0].Text == "Continue working from your saved context."
}
