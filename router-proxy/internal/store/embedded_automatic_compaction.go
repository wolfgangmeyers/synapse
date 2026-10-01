package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/sky-valley/pi/ai"
)

const embeddedAutomaticCompactionArgs = `{"automatic":true}`

// ClaimEmbeddedAutomaticCompaction uses the existing bounded request/effect
// ledger. The checked head replaces a manual SDK tool-result prerequisite.
// Intent and one-shot claim commit together before the summary provider call.
// A saved UNKNOWN claim, including a prior owner's, never authorizes replay.
func (s *Store) ClaimEmbeddedAutomaticCompaction(ctx context.Context, b EmbeddedBinding, id string, seq int64, digest string) error {
	if ctx == nil || id == "" || len(id) > 256 || seq < 2 || digest == "" {
		return errors.New("invalid automatic compaction claim")
	}
	return s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		head, hash, err := s.embeddedHead(ctx, b)
		if err != nil {
			return err
		}
		if head != seq || hash != digest {
			return errors.New("automatic compaction ordered head changed")
		}
		var role string
		if err := s.db.QueryRowContext(ctx, `SELECT role FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&role); err != nil {
			return err
		}
		if !validEmbeddedRole(role) {
			return errors.New("invalid automatic compaction role")
		}
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_own_compaction_requests WHERE agent_id=? AND session_id=? AND epoch=?`, b.AgentID, b.SessionID, b.Epoch).Scan(&count); err != nil {
			return err
		}
		if count >= EmbeddedOwnCompactionMaxCalls {
			return errors.New("compaction request ledger cap reached")
		}
		intent, err := json.Marshal(ai.ToolCall{ID: id, Name: "mecha_context", Arguments: map[string]any{"action": "compact"}})
		if err != nil {
			return err
		}
		result, err := json.Marshal(ai.ToolResultMessage{ToolCallID: id, ToolName: "mecha_context", Content: ai.ContentList{ai.TextContent{Text: "pending automatic compaction"}}})
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO embedded_effects(effect_id,agent_id,session_id,epoch,generation,intent,state,reminder_owner_token) VALUES(?,?,?,?,?,?,'unknown',?)`, id, b.AgentID, b.SessionID, b.Epoch, b.Generation, intent, b.OwnerToken); err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO embedded_own_compaction_requests(call_id,agent_id,session_id,epoch,generation,role,owner_token,head_seq,head_digest,args_json,result,claimed) VALUES(?,?,?,?,?,?,?,?,?,?,?,1)`, id, b.AgentID, b.SessionID, b.Epoch, b.Generation, role, b.OwnerToken, seq, digest, embeddedAutomaticCompactionArgs, result)
		return err
	})
}
