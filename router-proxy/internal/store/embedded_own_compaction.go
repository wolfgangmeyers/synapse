package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/coding"
)

const EmbeddedOwnCompactionMaxCalls = 4096

// RequestEmbeddedOwnCompaction records only a deferred request. It neither
// summarizes nor commits a checkpoint. A previous unknown effect is never
// fresh permission, even after a lease takeover.
func (s *Store) RequestEmbeddedOwnCompaction(ctx context.Context, b EmbeddedBinding, callID string, headSeq int64, headDigest string, admitted bool) (ai.ToolResultMessage, error) {
	var out ai.ToolResultMessage
	if !admitted || ctx == nil || callID == "" || len(callID) > 256 || strings.ContainsAny(callID, "\x00\r\n") || headSeq < 1 || headDigest == "" {
		return out, errors.New("invalid compaction request")
	}
	err := s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		var role string
		if err := s.db.QueryRowContext(ctx, `SELECT role FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&role); err != nil {
			return err
		}
		if !validEmbeddedRole(role) {
			return errors.New("invalid compaction role")
		}
		var original []byte
		var state string
		var owner sql.NullInt64
		err := s.db.QueryRowContext(ctx, `SELECT intent,state,reminder_owner_token FROM embedded_effects WHERE effect_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, callID, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&original, &state, &owner)
		if err != nil {
			return err
		}
		var call struct {
			ID        string         `json:"id"`
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if json.Unmarshal(original, &call) != nil || call.ID != callID || call.Name != "mecha_context" || len(call.Arguments) != 1 || call.Arguments["action"] != "compact" || state != "unknown" || !owner.Valid || owner.Int64 != b.OwnerToken {
			return errors.New("compaction intent conflicts or is not fresh")
		}
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM embedded_own_compaction_requests WHERE agent_id=? AND session_id=? AND epoch=?`, b.AgentID, b.SessionID, b.Epoch).Scan(&count); err != nil {
			return err
		}
		if count >= EmbeddedOwnCompactionMaxCalls {
			return errors.New("compaction request ledger cap reached")
		}
		var existing int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM embedded_own_compaction_requests WHERE call_id=?`, callID).Scan(&existing); err != nil {
			return err
		}
		if existing != 0 {
			return errors.New("compaction request already recorded")
		}
		var actualSeq int64
		var actualDigest string
		if err := s.db.QueryRowContext(ctx, `SELECT seq,digest FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? ORDER BY seq DESC LIMIT 1`, b.AgentID, b.SessionID, b.Epoch).Scan(&actualSeq, &actualDigest); err != nil {
			return err
		}
		if headSeq != actualSeq || headDigest != actualDigest {
			return errors.New("compaction ordered head changed")
		}
		out = ai.ToolResultMessage{ToolCallID: callID, ToolName: "mecha_context", Content: ai.ContentList{ai.TextContent{Text: "pending"}}}
		result, err := json.Marshal(out)
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO embedded_own_compaction_requests(call_id,agent_id,session_id,epoch,generation,role,owner_token,head_seq,head_digest,args_json,result) VALUES(?,?,?,?,?,?,?,?,?,'{}',?)`, callID, b.AgentID, b.SessionID, b.Epoch, b.Generation, role, b.OwnerToken, headSeq, headDigest, result)
		return err
	})
	if err != nil {
		return ai.ToolResultMessage{}, err
	}
	return out, nil
}

// ClaimEmbeddedOwnCompaction crosses the one-shot pre-provider boundary. A
// claim survives an interrupted summarizer; no later runner may claim again.
// The request's saved head must be the assistant call immediately before the
// checked SDK result at the current ordered head.
func (s *Store) ClaimEmbeddedOwnCompaction(ctx context.Context, b EmbeddedBinding, callID string, resultSeq int64, resultDigest string) error {
	if callID == "" || resultSeq < 2 || resultDigest == "" {
		return errors.New("invalid compaction claim")
	}
	return s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		var savedSeq, owner int64
		var savedDigest string
		var claimed int
		var savedResult []byte
		err := s.db.QueryRowContext(ctx, `SELECT head_seq,head_digest,owner_token,claimed,result FROM embedded_own_compaction_requests WHERE call_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, callID, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&savedSeq, &savedDigest, &owner, &claimed, &savedResult)
		if err != nil {
			return err
		}
		if owner != b.OwnerToken || claimed != 0 || savedSeq+1 != resultSeq {
			return errors.New("compaction claim stale or already used")
		}
		seq, digest, err := s.embeddedHead(ctx, b)
		if err != nil {
			return err
		}
		if seq != resultSeq || digest != resultDigest {
			return errors.New("compaction claim ordered head changed")
		}
		var parent, kind string
		var body []byte
		if err := s.db.QueryRowContext(ctx, `SELECT parent_digest,kind,body FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND seq=?`, b.AgentID, b.SessionID, b.Epoch, resultSeq).Scan(&parent, &kind, &body); err != nil {
			return err
		}
		var result ai.ToolResultMessage
		if kind != string(ai.RoleToolResult) || parent != savedDigest || json.Unmarshal(body, &result) != nil || result.ToolCallID != callID || result.ToolName != "mecha_context" || result.IsError {
			return errors.New("compaction claim has no checked SDK result")
		}
		var expected ai.ToolResultMessage
		if json.Unmarshal(savedResult, &expected) != nil || expected.ToolCallID != result.ToolCallID || expected.ToolName != result.ToolName || !bytes.Equal(mustCompactionContent(expected.Content), mustCompactionContent(result.Content)) {
			return errors.New("compaction claim result differs from request")
		}
		var state string
		var intentOwner sql.NullInt64
		if err := s.db.QueryRowContext(ctx, `SELECT state,reminder_owner_token FROM embedded_effects WHERE effect_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, callID, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&state, &intentOwner); err != nil {
			return err
		}
		if state != "unknown" || !intentOwner.Valid || intentOwner.Int64 != b.OwnerToken {
			return errors.New("compaction claim intent no longer pending")
		}
		r, err := s.db.ExecContext(ctx, `UPDATE embedded_own_compaction_requests SET claimed=1 WHERE call_id=? AND claimed=0`, callID)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil || n != 1 {
			return errors.New("compaction claim lost")
		}
		return nil
	})
}

func mustCompactionContent(content ai.ContentList) []byte {
	body, _ := json.Marshal(content)
	return body
}

// CommitEmbeddedOwnCompactionCheckpoint atomically records the checked ordered
// summary and marks its pending intent observed. A failed commit leaves UNKNOWN.
func (s *Store) CommitEmbeddedOwnCompactionCheckpoint(ctx context.Context, b EmbeddedBinding, callID string, resultSeq int64, resultDigest string, checkpoint coding.OrderedCheckpoint) error {
	if checkpoint.ThroughSequence != resultSeq || checkpoint.FirstKeptSequence < 1 || checkpoint.FirstKeptSequence > resultSeq || checkpoint.Summary == "" {
		return errors.New("invalid own compaction checkpoint")
	}
	body, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	if err := embeddedBody(body); err != nil {
		return err
	}
	return s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		var savedSeq, owner int64
		var savedDigest, args string
		var claimed int
		var result []byte
		if err := s.db.QueryRowContext(ctx, `SELECT head_seq,head_digest,owner_token,claimed,result,args_json FROM embedded_own_compaction_requests WHERE call_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, callID, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&savedSeq, &savedDigest, &owner, &claimed, &result, &args); err != nil {
			return err
		}
		automatic := args == embeddedAutomaticCompactionArgs
		if owner != b.OwnerToken || claimed != 1 ||
			(!automatic && (savedSeq+1 != resultSeq || checkpoint.FirstKeptSequence != savedSeq)) ||
			(automatic && (savedSeq != resultSeq || savedDigest != resultDigest || checkpoint.FirstKeptSequence < 2)) {
			return errors.New("compaction checkpoint claim mismatch")
		}
		seq, digest, err := s.embeddedHead(ctx, b)
		if err != nil {
			return err
		}
		if seq != resultSeq || digest != resultDigest {
			return errors.New("compaction checkpoint ordered head changed")
		}
		if !automatic {
			var parent string
			if err := s.db.QueryRowContext(ctx, `SELECT parent_digest FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND seq=?`, b.AgentID, b.SessionID, b.Epoch, resultSeq).Scan(&parent); err != nil {
				return err
			}
			if parent != savedDigest {
				return errors.New("compaction checkpoint ancestor changed")
			}
		}
		c := EmbeddedCheckpoint{Sequence: resultSeq, AncestorDigest: digest, Body: body}
		c.Digest = embeddedCheckpointDigest(b, c)
		if _, err := s.db.ExecContext(ctx, `INSERT INTO embedded_checkpoints(agent_id,session_id,epoch,generation,at_seq,ancestor_digest,body,digest) VALUES(?,?,?,?,?,?,?,?)`, b.AgentID, b.SessionID, b.Epoch, b.Generation, c.Sequence, c.AncestorDigest, c.Body, c.Digest); err != nil {
			return err
		}
		r, err := s.db.ExecContext(ctx, `UPDATE embedded_effects SET state='observed',result=? WHERE effect_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=? AND state='unknown' AND reminder_owner_token=?`, result, callID, b.AgentID, b.SessionID, b.Epoch, b.Generation, b.OwnerToken)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil || n != 1 {
			return errors.New("compaction checkpoint intent no longer pending")
		}
		return nil
	})
}
