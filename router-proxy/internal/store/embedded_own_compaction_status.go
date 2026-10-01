package store

import (
	"context"
	"database/sql"
	"errors"
)

// EmbeddedOwnCompactionStatus is a read-only, owner-fenced view of the saved
// caller. Policy reserves 16384 tokens before every ordinary/tool-continuation
// request and rejects unknown windows. A claim without its checkpoint reports
// claimed, never committed; its unobserved effect remains UNKNOWN.
type EmbeddedOwnCompactionStatus struct {
	SessionID          string `json:"session_id"`
	Policy             string `json:"policy"`
	Outcome            string `json:"outcome"`
	CallID             string `json:"call_id,omitempty"`
	CheckpointSequence int64  `json:"checkpoint_sequence,omitempty"`
}

func (s *Store) EmbeddedOwnCompactionStatus(ctx context.Context, b EmbeddedBinding) (EmbeddedOwnCompactionStatus, error) {
	out := EmbeddedOwnCompactionStatus{SessionID: b.SessionID, Policy: "fixed pre-provider headroom", Outcome: "unknown"}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return out, errors.New("store is closed")
	}
	if err := s.embeddedFence(ctx, b); err != nil {
		return out, err
	}
	var role string
	if err := s.db.QueryRowContext(ctx, `SELECT role FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&role); err != nil {
		return out, err
	}
	if !validEmbeddedRole(role) {
		return out, errors.New("invalid saved embedded role")
	}
	err := s.db.QueryRowContext(ctx, `SELECT at_seq FROM embedded_checkpoints WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? ORDER BY at_seq DESC LIMIT 1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&out.CheckpointSequence)
	if err != nil && err != sql.ErrNoRows {
		return out, err
	}
	var callID, effectState, args string
	var headSeq, ownerToken int64
	var claimed int
	err = s.db.QueryRowContext(ctx, `SELECT r.call_id,r.head_seq,r.owner_token,r.claimed,e.state,r.args_json FROM embedded_own_compaction_requests r JOIN embedded_effects e ON e.effect_id=r.call_id AND e.agent_id=r.agent_id AND e.session_id=r.session_id AND e.epoch=r.epoch AND e.generation=r.generation WHERE r.agent_id=? AND r.session_id=? AND r.epoch=? AND r.generation=? AND r.role=? ORDER BY r.head_seq DESC,r.rowid DESC LIMIT 1`, b.AgentID, b.SessionID, b.Epoch, b.Generation, role).Scan(&callID, &headSeq, &ownerToken, &claimed, &effectState, &args)
	if err == sql.ErrNoRows {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.CallID = callID
	// An older owner may have left an unresolved attempt. Do not call it live.
	if ownerToken != b.OwnerToken {
		return out, nil
	}
	var committed int
	checkpointSeq := headSeq + 1
	if args == embeddedAutomaticCompactionArgs {
		checkpointSeq = headSeq
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_checkpoints WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND at_seq=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, checkpointSeq).Scan(&committed); err != nil {
		return out, err
	}
	switch {
	case claimed == 1 && committed == 1 && effectState == "observed":
		out.Outcome = "committed"
	case claimed == 1 && committed == 0 && effectState == "unknown":
		out.Outcome = "claimed"
	case claimed == 0 && effectState == "unknown":
		out.Outcome = "pending"
	}
	return out, nil
}
