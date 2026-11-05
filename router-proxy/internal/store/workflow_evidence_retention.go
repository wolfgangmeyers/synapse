package store

import (
	"context"
	"errors"
)

// AppendImportedEmbeddedEntry preserves admission authority during context reset.
// Only a validated saved entry of the same agent is importable. The source may
// be a former binding; only the target needs the current execution-owner fence.
func (s *Store) AppendImportedEmbeddedEntry(ctx context.Context, target, source EmbeddedBinding, sourceSeq int64, sourceDigest string) (EmbeddedEntry, error) {
	var entry EmbeddedEntry
	if source.AgentID != target.AgentID || sourceSeq <= 0 || sourceDigest == "" {
		return entry, errors.New("invalid embedded import source")
	}
	err := s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, target); err != nil {
			return err
		}
		saved := EmbeddedEntry{Sequence: sourceSeq}
		var admitted int64
		if err := s.db.QueryRowContext(ctx, `SELECT kind,body,parent_digest,digest,retention_admitted_at_ms FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND seq=?`, source.AgentID, source.SessionID, source.Epoch, source.Generation, sourceSeq).Scan(&saved.Kind, &saved.Body, &saved.ParentDigest, &saved.Digest, &admitted); err != nil {
			return err
		}
		if admitted <= 0 || saved.Digest != sourceDigest || embeddedBody(saved.Body) != nil || embeddedEntryDigest(source, saved) != saved.Digest {
			return errors.New("invalid embedded import authority")
		}
		var sourceCutoff int64
		if err := s.db.QueryRowContext(ctx, `SELECT evidence_expired_through_ms FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, source.AgentID, source.SessionID, source.Epoch, source.Generation).Scan(&sourceCutoff); err != nil {
			return err
		}
		// Phase-121-only stores support imports but have no execution schema.
		// Once retention guards exist, freeze live dependencies before moving
		// the target cutoff past their admission authority.
		var guards int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE id='123_workflow_evidence_retention_guards'`).Scan(&guards); err != nil {
			return err
		}
		if guards != 0 {
			if err := s.refreshEmbeddedExecutionCallsTx(ctx); err != nil {
				return err
			}
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE embedded_sessions SET evidence_expired_through_ms=MAX(evidence_expired_through_ms,?) WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, sourceCutoff, target.AgentID, target.SessionID, target.Epoch, target.Generation); err != nil {
			return err
		}
		seq, digest, err := s.embeddedHead(ctx, target)
		if err != nil {
			return err
		}
		entry, err = s.appendEmbeddedAdmittedTx(ctx, target, saved.Kind, saved.Body, seq, digest, admitted)
		return err
	})
	return entry, err
}

const embeddedEvidenceAgeMS int64 = 30 * 24 * 60 * 60 * 1000

var ErrEmbeddedEvidenceExpired = errors.New("retention-expired embedded evidence")

// Called inside an immediate transaction, before replay as well as insertion.
func (s *Store) embeddedEvidenceAdmissionTx(ctx context.Context, b EmbeddedBinding, seq int64, digest string) error {
	if err := s.refreshEmbeddedExecutionCallsTx(ctx); err != nil {
		return err
	}
	cutoff := s.clock.Now().UnixMilli() - embeddedEvidenceAgeMS
	if cutoff < 0 {
		cutoff = 0
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE embedded_sessions SET evidence_expired_through_ms=MAX(evidence_expired_through_ms,?) WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, cutoff, b.AgentID, b.SessionID, b.Epoch, b.Generation); err != nil {
		return err
	}
	var admitted, savedCutoff int64
	var kind, savedDigest string
	if err := s.db.QueryRowContext(ctx, `SELECT e.retention_admitted_at_ms,s.evidence_expired_through_ms,e.kind,e.digest FROM embedded_entries e JOIN embedded_sessions s ON s.agent_id=e.agent_id AND s.session_id=e.session_id AND s.epoch=e.epoch AND s.generation=e.generation WHERE e.agent_id=? AND e.session_id=? AND e.epoch=? AND e.generation=? AND e.seq=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, seq).Scan(&admitted, &savedCutoff, &kind, &savedDigest); err != nil {
		return err
	}
	if admitted <= 0 || kind != "user" || savedDigest != digest {
		return errors.New("invalid retention authority")
	}
	if admitted <= savedCutoff {
		return ErrEmbeddedEvidenceExpired
	}
	return nil
}

// embeddedEvidenceReplayTx permits only the exact retained closure after expiry.
func (s *Store) embeddedEvidenceReplayTx(ctx context.Context, b EmbeddedBinding, k EmbeddedGoalEvaluationKey, callID string) error {
	err := s.embeddedEvidenceAdmissionTx(ctx, b, k.UserSeq, k.UserDigest)
	if !errors.Is(err, ErrEmbeddedEvidenceExpired) {
		return err
	}
	var n int
	err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_retention_closures WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND user_seq=? AND user_digest=? AND goal_revision=? AND (?='' OR call_id=?) AND (?=0 OR (final_seq=? AND final_digest=?))`, b.AgentID, b.SessionID, b.Epoch, b.Generation, k.UserSeq, k.UserDigest, k.GoalRevision, callID, callID, k.FinalSeq, k.FinalSeq, k.FinalDigest).Scan(&n)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrEmbeddedEvidenceExpired
	}
	return nil
}

func (s *Store) pruneEmbeddedEvidenceTx(ctx context.Context) error {
	if err := s.refreshEmbeddedExecutionCallsTx(ctx); err != nil {
		return err
	}
	cutoff := s.clock.Now().UnixMilli() - embeddedEvidenceAgeMS
	if cutoff < 0 {
		cutoff = 0
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE embedded_sessions SET evidence_expired_through_ms=MAX(evidence_expired_through_ms,?)`, cutoff); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE embedded_goal_continuations SET state='invalidated',disposition='invalidated' WHERE state='reserved' AND retention_expires_at_ms<=(SELECT evidence_expired_through_ms+2592000000 FROM embedded_sessions s WHERE s.agent_id=embedded_goal_continuations.agent_id AND s.session_id=embedded_goal_continuations.session_id AND s.epoch=embedded_goal_continuations.epoch AND s.generation=embedded_goal_continuations.generation)`); err != nil {
		return err
	}
	for _, table := range []string{"embedded_goal_snooze_preferences", "embedded_goal_snooze_calls", "embedded_goal_continuations", "embedded_goal_evaluations", "embedded_goal_applied_receipts"} {
		seq, digest := "user_seq", "user_digest"
		if table == "embedded_goal_applied_receipts" {
			seq, digest = "turn_entry_seq", "turn_entry_digest"
		}
		match := `x.agent_id=q.agent_id AND x.session_id=q.session_id AND x.epoch=q.epoch AND x.generation=q.generation AND x.user_seq=q.` + seq + ` AND x.user_digest=q.` + digest + ` AND x.goal_revision=q.goal_revision`
		if table == "embedded_goal_snooze_calls" {
			match += ` AND x.call_id=q.call_id`
		}
		if table == "embedded_goal_evaluations" {
			match += ` AND x.final_seq=q.final_seq AND x.final_digest=q.final_digest`
		}
		query := `DELETE FROM ` + table + ` AS q WHERE (EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=q.agent_id AND t.session_id=q.session_id AND t.epoch=q.epoch AND t.generation=q.generation AND t.user_seq=q.` + seq + ` AND t.user_digest=q.` + digest + ` AND t.retention_admitted_at_ms<=t.evidence_expired_through_ms) OR (EXISTS(SELECT 1 FROM embedded_retention_legacy_drained_sessions s WHERE s.agent_id=q.agent_id AND s.session_id=q.session_id AND s.epoch=q.epoch AND s.generation=q.generation) AND NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=q.agent_id AND t.session_id=q.session_id AND t.epoch=q.epoch AND t.generation=q.generation AND t.user_seq=q.` + seq + ` AND t.user_digest=q.` + digest + `))) AND NOT EXISTS(SELECT 1 FROM embedded_retention_closures x WHERE ` + match + `) AND NOT EXISTS(SELECT 1 FROM embedded_retention_legacy_holds h WHERE h.agent_id=q.agent_id AND h.session_id=q.session_id AND h.epoch=q.epoch AND h.generation=q.generation AND h.user_seq=q.` + seq + ` AND h.goal_revision=q.goal_revision)`
		if _, err := s.db.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) pruneEmbeddedEvidenceIfPresentTx(ctx context.Context) error {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE id='123_workflow_evidence_retention_guards'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	return s.pruneEmbeddedEvidenceTx(ctx)
}

// Read-only gate for lock-protected diagnostics/recovery. Uses both injected now
// and durable cutoff; it does not grant insertion permission.
func (s *Store) embeddedEvidenceReadGate(ctx context.Context, b EmbeddedBinding, k EmbeddedGoalEvaluationKey, callID string) error {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_retention_turns t WHERE t.agent_id=? AND t.session_id=? AND t.epoch=? AND t.generation=? AND t.user_seq=? AND t.user_digest=? AND (t.retention_admitted_at_ms>MAX(t.evidence_expired_through_ms,?) OR EXISTS(SELECT 1 FROM embedded_execution_closures x WHERE x.agent_id=t.agent_id AND x.session_id=t.session_id AND x.epoch=t.epoch AND x.generation=t.generation AND x.user_seq=t.user_seq AND x.user_digest=t.user_digest AND x.goal_revision=? AND (?='' OR x.call_id=?) AND (?=0 OR (x.final_seq=? AND x.final_digest=?))) OR EXISTS(SELECT 1 FROM embedded_goal_continuations c LEFT JOIN embedded_goal_snooze_preferences p USING(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision) WHERE c.agent_id=t.agent_id AND c.session_id=t.session_id AND c.epoch=t.epoch AND c.generation=t.generation AND c.user_seq=t.user_seq AND c.user_digest=t.user_digest AND c.goal_revision=? AND (?=0 OR EXISTS(SELECT 1 FROM embedded_goal_evaluations v WHERE v.agent_id=c.agent_id AND v.session_id=c.session_id AND v.epoch=c.epoch AND v.generation=c.generation AND v.user_seq=c.user_seq AND v.user_digest=c.user_digest AND v.goal_revision=c.goal_revision AND v.final_seq=? AND v.final_digest=?)) AND c.state='reserved' AND c.retention_expires_at_ms>MAX(t.evidence_expired_through_ms+2592000000,?) AND (?='' OR p.call_id=?)))`, b.AgentID, b.SessionID, b.Epoch, b.Generation, k.UserSeq, k.UserDigest, s.clock.Now().UnixMilli()-embeddedEvidenceAgeMS, k.GoalRevision, callID, callID, k.FinalSeq, k.FinalSeq, k.FinalDigest, k.GoalRevision, k.FinalSeq, k.FinalSeq, k.FinalDigest, s.clock.Now().UnixMilli(), callID, callID).Scan(&n)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrEmbeddedEvidenceExpired
	}
	return nil
}
