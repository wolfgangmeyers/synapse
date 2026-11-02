-- Pending acceptance has a finite immutable lifetime, independent of call churn.
-- Older databases can be missing predecessor guards. Replace them whether or
-- not they survived earlier upgrades; every new guard is installed atomically.
-- One database-wide legacy horizon, copied once onto existing session metadata.
-- New sessions have no legacy grace. No history/ledger or refreshed deadline.
ALTER TABLE embedded_sessions ADD COLUMN legacy_evidence_expires_at_ms INTEGER NOT NULL DEFAULT 0 CHECK(legacy_evidence_expires_at_ms>=0);
UPDATE embedded_sessions SET legacy_evidence_expires_at_ms=(
 SELECT MAX(CAST(ROUND((julianday('now')-2440587.5)*86400000) AS INTEGER)+5270400000,
 COALESCE(MAX(CAST(ROUND((julianday(due_at)-2440587.5)*86400000) AS INTEGER)+2592000000),0))
 FROM embedded_goal_continuations);
CREATE TRIGGER embedded_retention_legacy_horizon_immutable BEFORE UPDATE OF legacy_evidence_expires_at_ms ON embedded_sessions
WHEN NEW.legacy_evidence_expires_at_ms<>OLD.legacy_evidence_expires_at_ms
BEGIN SELECT RAISE(ABORT,'legacy retention horizon is immutable'); END;
ALTER TABLE embedded_goal_continuations ADD COLUMN retention_expires_at_ms INTEGER NOT NULL DEFAULT 0 CHECK(retention_expires_at_ms>=0);
-- Migration-time legacy drain is frozen once; original saved D is not changed.
-- The legacy guard rejects even state-preserving UPDATEs. Suspend only that
-- guard for this backfill, then restore its exact definition in this transaction.
DROP TRIGGER IF EXISTS embedded_goal_continuation_transition;
UPDATE embedded_goal_continuations SET retention_expires_at_ms=COALESCE((
 SELECT MAX(e.retention_admitted_at_ms+2592000000,COALESCE(CAST(ROUND((julianday(embedded_goal_continuations.due_at)-2440587.5)*86400000) AS INTEGER),e.retention_admitted_at_ms+2592000000))+2592000000
 FROM embedded_entries e WHERE e.agent_id=embedded_goal_continuations.agent_id AND e.session_id=embedded_goal_continuations.session_id AND e.epoch=embedded_goal_continuations.epoch AND e.generation=embedded_goal_continuations.generation AND e.seq=embedded_goal_continuations.user_seq AND e.digest=embedded_goal_continuations.user_digest AND e.kind='user'
 AND (embedded_goal_continuations.due_at IS NULL OR julianday(embedded_goal_continuations.due_at) IS NOT NULL)
 AND EXISTS(SELECT 1 FROM embedded_goal_evaluations v JOIN embedded_entries f ON f.agent_id=v.agent_id AND f.session_id=v.session_id AND f.epoch=v.epoch AND f.generation=v.generation AND f.seq=v.final_seq AND f.digest=v.final_digest AND f.kind='assistant'
 WHERE v.agent_id=e.agent_id AND v.session_id=e.session_id AND v.epoch=e.epoch AND v.generation=e.generation AND v.user_seq=e.seq AND v.user_digest=e.digest AND v.goal_revision=embedded_goal_continuations.goal_revision)),
 (SELECT legacy_evidence_expires_at_ms FROM embedded_sessions s WHERE s.agent_id=embedded_goal_continuations.agent_id AND s.session_id=embedded_goal_continuations.session_id AND s.epoch=embedded_goal_continuations.epoch AND s.generation=embedded_goal_continuations.generation));
CREATE TRIGGER embedded_goal_continuation_transition BEFORE UPDATE ON embedded_goal_continuations
WHEN OLD.state<>'reserved' OR NEW.state NOT IN ('started','invalidated')
 OR NEW.agent_id<>OLD.agent_id OR NEW.session_id<>OLD.session_id OR NEW.epoch<>OLD.epoch
 OR NEW.generation<>OLD.generation OR NEW.user_seq<>OLD.user_seq OR NEW.user_digest<>OLD.user_digest
 OR NEW.goal_revision<>OLD.goal_revision OR NEW.origin<>OLD.origin OR NEW.reserved_at<>OLD.reserved_at
 OR NEW.due_at IS NOT OLD.due_at
BEGIN SELECT RAISE(ABORT,'continuation transition invalid'); END;
CREATE TRIGGER embedded_retention_pending_immutable BEFORE UPDATE OF retention_expires_at_ms ON embedded_goal_continuations
WHEN NEW.retention_expires_at_ms<>OLD.retention_expires_at_ms
BEGIN SELECT RAISE(ABORT,'pending retention lifetime is immutable'); END;
CREATE VIEW embedded_retention_turns AS
SELECT e.agent_id,e.session_id,e.epoch,e.generation,e.seq AS user_seq,e.digest AS user_digest,e.retention_admitted_at_ms,s.evidence_expired_through_ms
FROM embedded_entries e JOIN embedded_sessions s USING(agent_id,session_id,epoch,generation)
WHERE e.kind='user' AND e.retention_admitted_at_ms>0;
CREATE VIEW embedded_retention_legacy_drained_sessions AS
SELECT agent_id,session_id,epoch,generation FROM embedded_sessions
WHERE legacy_evidence_expires_at_ms>0 AND evidence_expired_through_ms>=legacy_evidence_expires_at_ms-2592000000;
-- Malformed legacy evidence has a deletion-only fallback lifetime. Policy
-- invalidation may revoke acceptance before H, but cannot shorten this hold.
-- This view is deliberately absent from replay/start authority.
CREATE VIEW embedded_retention_legacy_holds AS
SELECT c.agent_id,c.session_id,c.epoch,c.generation,c.user_seq,c.goal_revision
FROM embedded_goal_continuations c JOIN embedded_sessions s USING(agent_id,session_id,epoch,generation)
WHERE c.retention_expires_at_ms=s.legacy_evidence_expires_at_ms
 AND s.legacy_evidence_expires_at_ms>0
 AND s.evidence_expired_through_ms<s.legacy_evidence_expires_at_ms-2592000000
 AND ((c.due_at IS NOT NULL AND julianday(c.due_at) IS NULL)
 OR NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=c.agent_id AND t.session_id=c.session_id AND t.epoch=c.epoch AND t.generation=c.generation AND t.user_seq=c.user_seq AND t.user_digest=c.user_digest)
 OR NOT EXISTS(SELECT 1 FROM embedded_goal_evaluations v JOIN embedded_entries f ON f.agent_id=v.agent_id AND f.session_id=v.session_id AND f.epoch=v.epoch AND f.generation=v.generation AND f.seq=v.final_seq AND f.digest=v.final_digest AND f.kind='assistant' WHERE v.agent_id=c.agent_id AND v.session_id=c.session_id AND v.epoch=c.epoch AND v.generation=c.generation AND v.user_seq=c.user_seq AND v.user_digest=c.user_digest AND v.goal_revision=c.goal_revision));
CREATE VIEW embedded_retention_closures AS
SELECT agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,final_seq,final_digest,call_id FROM embedded_execution_closures
UNION ALL
SELECT c.agent_id,c.session_id,c.epoch,c.generation,c.user_seq,c.user_digest,c.goal_revision,v.final_seq,v.final_digest,COALESCE(p.call_id,'')
FROM embedded_goal_continuations c JOIN embedded_goal_evaluations v USING(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision)
JOIN embedded_retention_turns t USING(agent_id,session_id,epoch,generation,user_seq,user_digest)
LEFT JOIN embedded_goal_snooze_preferences p USING(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision)
WHERE c.state='reserved' AND v.state='NOT_DONE' AND c.retention_expires_at_ms>t.evidence_expired_through_ms+2592000000;
DROP TRIGGER IF EXISTS embedded_goal_receipt_immutable_delete;
CREATE TRIGGER embedded_goal_receipt_immutable_delete BEFORE DELETE ON embedded_goal_applied_receipts
WHEN EXISTS(SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id) AND
((NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=OLD.agent_id AND t.session_id=OLD.session_id AND t.epoch=OLD.epoch AND t.generation=OLD.generation AND t.user_seq=OLD.turn_entry_seq AND t.user_digest=OLD.turn_entry_digest AND t.retention_admitted_at_ms<=t.evidence_expired_through_ms) AND NOT (EXISTS(SELECT 1 FROM embedded_retention_legacy_drained_sessions s WHERE s.agent_id=OLD.agent_id AND s.session_id=OLD.session_id AND s.epoch=OLD.epoch AND s.generation=OLD.generation) AND NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=OLD.agent_id AND t.session_id=OLD.session_id AND t.epoch=OLD.epoch AND t.generation=OLD.generation AND t.user_seq=OLD.turn_entry_seq AND t.user_digest=OLD.turn_entry_digest))) OR EXISTS(SELECT 1 FROM embedded_retention_closures x WHERE x.agent_id=OLD.agent_id AND x.session_id=OLD.session_id AND x.epoch=OLD.epoch AND x.generation=OLD.generation AND x.user_seq=OLD.turn_entry_seq AND x.user_digest=OLD.turn_entry_digest AND x.goal_revision=OLD.goal_revision))
BEGIN SELECT RAISE(ABORT,'retention evidence is immutable while protected'); END;
CREATE TRIGGER embedded_goal_applied_receipts_retention_insert BEFORE INSERT ON embedded_goal_applied_receipts
WHEN NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=NEW.agent_id AND t.session_id=NEW.session_id AND t.epoch=NEW.epoch AND t.generation=NEW.generation AND t.user_seq=NEW.turn_entry_seq AND t.user_digest=NEW.turn_entry_digest AND t.retention_admitted_at_ms>t.evidence_expired_through_ms)
BEGIN SELECT RAISE(ABORT,'retention-expired embedded evidence'); END;
DROP TRIGGER IF EXISTS embedded_goal_evaluation_immutable_delete;
CREATE TRIGGER embedded_goal_evaluation_immutable_delete BEFORE DELETE ON embedded_goal_evaluations
WHEN EXISTS(SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id) AND
((NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=OLD.agent_id AND t.session_id=OLD.session_id AND t.epoch=OLD.epoch AND t.generation=OLD.generation AND t.user_seq=OLD.user_seq AND t.user_digest=OLD.user_digest AND t.retention_admitted_at_ms<=t.evidence_expired_through_ms) AND NOT (EXISTS(SELECT 1 FROM embedded_retention_legacy_drained_sessions s WHERE s.agent_id=OLD.agent_id AND s.session_id=OLD.session_id AND s.epoch=OLD.epoch AND s.generation=OLD.generation) AND NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=OLD.agent_id AND t.session_id=OLD.session_id AND t.epoch=OLD.epoch AND t.generation=OLD.generation AND t.user_seq=OLD.user_seq AND t.user_digest=OLD.user_digest))) OR EXISTS(SELECT 1 FROM embedded_retention_closures x WHERE x.agent_id=OLD.agent_id AND x.session_id=OLD.session_id AND x.epoch=OLD.epoch AND x.generation=OLD.generation AND x.user_seq=OLD.user_seq AND x.user_digest=OLD.user_digest AND x.goal_revision=OLD.goal_revision AND x.final_seq=OLD.final_seq AND x.final_digest=OLD.final_digest))
BEGIN SELECT RAISE(ABORT,'retention evidence is immutable while protected'); END;
CREATE TRIGGER embedded_goal_evaluations_retention_insert BEFORE INSERT ON embedded_goal_evaluations
WHEN NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=NEW.agent_id AND t.session_id=NEW.session_id AND t.epoch=NEW.epoch AND t.generation=NEW.generation AND t.user_seq=NEW.user_seq AND t.user_digest=NEW.user_digest AND t.retention_admitted_at_ms>t.evidence_expired_through_ms)
BEGIN SELECT RAISE(ABORT,'retention-expired embedded evidence'); END;
CREATE TRIGGER embedded_goal_evaluations_retention_update BEFORE UPDATE ON embedded_goal_evaluations
WHEN NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=NEW.agent_id AND t.session_id=NEW.session_id AND t.epoch=NEW.epoch AND t.generation=NEW.generation AND t.user_seq=NEW.user_seq AND t.user_digest=NEW.user_digest AND t.retention_admitted_at_ms>t.evidence_expired_through_ms) AND NOT EXISTS(SELECT 1 FROM embedded_execution_closures x WHERE x.agent_id=NEW.agent_id AND x.session_id=NEW.session_id AND x.epoch=NEW.epoch AND x.generation=NEW.generation AND x.user_seq=NEW.user_seq AND x.user_digest=NEW.user_digest AND x.goal_revision=NEW.goal_revision AND x.final_seq=NEW.final_seq AND x.final_digest=NEW.final_digest)
BEGIN SELECT RAISE(ABORT,'retention-expired embedded evidence'); END;
DROP TRIGGER IF EXISTS embedded_goal_continuation_no_delete;
CREATE TRIGGER embedded_goal_continuation_no_delete BEFORE DELETE ON embedded_goal_continuations
WHEN EXISTS(SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id) AND
((NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=OLD.agent_id AND t.session_id=OLD.session_id AND t.epoch=OLD.epoch AND t.generation=OLD.generation AND t.user_seq=OLD.user_seq AND t.user_digest=OLD.user_digest AND t.retention_admitted_at_ms<=t.evidence_expired_through_ms) AND NOT (EXISTS(SELECT 1 FROM embedded_retention_legacy_drained_sessions s WHERE s.agent_id=OLD.agent_id AND s.session_id=OLD.session_id AND s.epoch=OLD.epoch AND s.generation=OLD.generation) AND NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=OLD.agent_id AND t.session_id=OLD.session_id AND t.epoch=OLD.epoch AND t.generation=OLD.generation AND t.user_seq=OLD.user_seq AND t.user_digest=OLD.user_digest))) OR EXISTS(SELECT 1 FROM embedded_retention_closures x WHERE x.agent_id=OLD.agent_id AND x.session_id=OLD.session_id AND x.epoch=OLD.epoch AND x.generation=OLD.generation AND x.user_seq=OLD.user_seq AND x.user_digest=OLD.user_digest AND x.goal_revision=OLD.goal_revision))
BEGIN SELECT RAISE(ABORT,'retention evidence is immutable while protected'); END;
CREATE TRIGGER embedded_goal_continuations_retention_insert BEFORE INSERT ON embedded_goal_continuations
WHEN NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=NEW.agent_id AND t.session_id=NEW.session_id AND t.epoch=NEW.epoch AND t.generation=NEW.generation AND t.user_seq=NEW.user_seq AND t.user_digest=NEW.user_digest AND t.retention_admitted_at_ms>t.evidence_expired_through_ms)
BEGIN SELECT RAISE(ABORT,'retention-expired embedded evidence'); END;
DROP TRIGGER IF EXISTS embedded_goal_snooze_call_no_delete;
CREATE TRIGGER embedded_goal_snooze_call_no_delete BEFORE DELETE ON embedded_goal_snooze_calls
WHEN EXISTS(SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id) AND
((NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=OLD.agent_id AND t.session_id=OLD.session_id AND t.epoch=OLD.epoch AND t.generation=OLD.generation AND t.user_seq=OLD.user_seq AND t.user_digest=OLD.user_digest AND t.retention_admitted_at_ms<=t.evidence_expired_through_ms) AND NOT (EXISTS(SELECT 1 FROM embedded_retention_legacy_drained_sessions s WHERE s.agent_id=OLD.agent_id AND s.session_id=OLD.session_id AND s.epoch=OLD.epoch AND s.generation=OLD.generation) AND NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=OLD.agent_id AND t.session_id=OLD.session_id AND t.epoch=OLD.epoch AND t.generation=OLD.generation AND t.user_seq=OLD.user_seq AND t.user_digest=OLD.user_digest))) OR EXISTS(SELECT 1 FROM embedded_retention_closures x WHERE x.agent_id=OLD.agent_id AND x.session_id=OLD.session_id AND x.epoch=OLD.epoch AND x.generation=OLD.generation AND x.user_seq=OLD.user_seq AND x.user_digest=OLD.user_digest AND x.goal_revision=OLD.goal_revision AND x.call_id=OLD.call_id))
BEGIN SELECT RAISE(ABORT,'retention evidence is immutable while protected'); END;
CREATE TRIGGER embedded_goal_snooze_calls_retention_insert BEFORE INSERT ON embedded_goal_snooze_calls
WHEN NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=NEW.agent_id AND t.session_id=NEW.session_id AND t.epoch=NEW.epoch AND t.generation=NEW.generation AND t.user_seq=NEW.user_seq AND t.user_digest=NEW.user_digest AND t.retention_admitted_at_ms>t.evidence_expired_through_ms)
BEGIN SELECT RAISE(ABORT,'retention-expired embedded evidence'); END;
DROP TRIGGER IF EXISTS embedded_goal_snooze_preference_no_delete;
CREATE TRIGGER embedded_goal_snooze_preference_no_delete BEFORE DELETE ON embedded_goal_snooze_preferences
WHEN EXISTS(SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id) AND
((NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=OLD.agent_id AND t.session_id=OLD.session_id AND t.epoch=OLD.epoch AND t.generation=OLD.generation AND t.user_seq=OLD.user_seq AND t.user_digest=OLD.user_digest AND t.retention_admitted_at_ms<=t.evidence_expired_through_ms) AND NOT (EXISTS(SELECT 1 FROM embedded_retention_legacy_drained_sessions s WHERE s.agent_id=OLD.agent_id AND s.session_id=OLD.session_id AND s.epoch=OLD.epoch AND s.generation=OLD.generation) AND NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=OLD.agent_id AND t.session_id=OLD.session_id AND t.epoch=OLD.epoch AND t.generation=OLD.generation AND t.user_seq=OLD.user_seq AND t.user_digest=OLD.user_digest))) OR EXISTS(SELECT 1 FROM embedded_retention_closures x WHERE x.agent_id=OLD.agent_id AND x.session_id=OLD.session_id AND x.epoch=OLD.epoch AND x.generation=OLD.generation AND x.user_seq=OLD.user_seq AND x.user_digest=OLD.user_digest AND x.goal_revision=OLD.goal_revision))
BEGIN SELECT RAISE(ABORT,'retention evidence is immutable while protected'); END;
CREATE TRIGGER embedded_goal_snooze_preferences_retention_insert BEFORE INSERT ON embedded_goal_snooze_preferences
WHEN NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=NEW.agent_id AND t.session_id=NEW.session_id AND t.epoch=NEW.epoch AND t.generation=NEW.generation AND t.user_seq=NEW.user_seq AND t.user_digest=NEW.user_digest AND t.retention_admitted_at_ms>t.evidence_expired_through_ms)
BEGIN SELECT RAISE(ABORT,'retention-expired embedded evidence'); END;
CREATE TRIGGER embedded_goal_snooze_preferences_retention_update BEFORE UPDATE ON embedded_goal_snooze_preferences
WHEN (NEW.call_id<>OLD.call_id OR NEW.policy_version<>OLD.policy_version OR NEW.due_at<>OLD.due_at OR NEW.active>OLD.active) AND NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=NEW.agent_id AND t.session_id=NEW.session_id AND t.epoch=NEW.epoch AND t.generation=NEW.generation AND t.user_seq=NEW.user_seq AND t.user_digest=NEW.user_digest AND t.retention_admitted_at_ms>t.evidence_expired_through_ms)
BEGIN SELECT RAISE(ABORT,'retention-expired embedded evidence'); END;
CREATE TRIGGER embedded_retention_pending_insert BEFORE INSERT ON embedded_goal_continuations
WHEN NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=NEW.agent_id AND t.session_id=NEW.session_id AND t.epoch=NEW.epoch AND t.generation=NEW.generation AND t.user_seq=NEW.user_seq AND t.user_digest=NEW.user_digest
AND (NEW.due_at IS NULL OR (julianday(NEW.due_at) IS NOT NULL AND CAST(ROUND((julianday(NEW.due_at)-2440587.5)*86400000) AS INTEGER)<=t.retention_admitted_at_ms+2678400000))
AND NEW.retention_expires_at_ms=MAX(t.retention_admitted_at_ms+2592000000,COALESCE(CAST(ROUND((julianday(NEW.due_at)-2440587.5)*86400000) AS INTEGER),t.retention_admitted_at_ms+2592000000))+2592000000)
BEGIN SELECT RAISE(ABORT,'invalid pending retention lifetime'); END;
CREATE TRIGGER embedded_retention_pending_start BEFORE UPDATE ON embedded_goal_continuations
WHEN NEW.state='started' AND ((NEW.due_at IS NOT NULL AND julianday(NEW.due_at) IS NULL) OR NOT EXISTS(SELECT 1 FROM embedded_goal_evaluations v JOIN embedded_entries f ON f.agent_id=v.agent_id AND f.session_id=v.session_id AND f.epoch=v.epoch AND f.generation=v.generation AND f.seq=v.final_seq AND f.digest=v.final_digest AND f.kind='assistant' WHERE v.agent_id=NEW.agent_id AND v.session_id=NEW.session_id AND v.epoch=NEW.epoch AND v.generation=NEW.generation AND v.user_seq=NEW.user_seq AND v.user_digest=NEW.user_digest AND v.goal_revision=NEW.goal_revision) OR NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=NEW.agent_id AND t.session_id=NEW.session_id AND t.epoch=NEW.epoch AND t.generation=NEW.generation AND t.user_seq=NEW.user_seq AND t.user_digest=NEW.user_digest AND NEW.retention_expires_at_ms>t.evidence_expired_through_ms+2592000000)
)
BEGIN SELECT RAISE(ABORT,'retention-expired pending acceptance'); END;
-- Surviving authority cannot be removed/reinserted to reset admission.
CREATE TRIGGER embedded_retention_entry_no_delete BEFORE DELETE ON embedded_entries
WHEN EXISTS(SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id)
BEGIN SELECT RAISE(ABORT,'retention transcript authority is immutable'); END;
CREATE TRIGGER embedded_retention_session_no_delete BEFORE DELETE ON embedded_sessions
WHEN EXISTS(SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id)
BEGIN SELECT RAISE(ABORT,'retention session authority is immutable'); END;
CREATE TRIGGER embedded_goal_applied_receipts_legacy_hold_delete BEFORE DELETE ON embedded_goal_applied_receipts
WHEN EXISTS(SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id) AND EXISTS(SELECT 1 FROM embedded_retention_legacy_holds h WHERE h.agent_id=OLD.agent_id AND h.session_id=OLD.session_id AND h.epoch=OLD.epoch AND h.generation=OLD.generation AND h.user_seq=OLD.turn_entry_seq AND h.goal_revision=OLD.goal_revision)
BEGIN SELECT RAISE(ABORT,'legacy evidence is immutable before fallback horizon'); END;
CREATE TRIGGER embedded_goal_evaluations_legacy_hold_delete BEFORE DELETE ON embedded_goal_evaluations
WHEN EXISTS(SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id) AND EXISTS(SELECT 1 FROM embedded_retention_legacy_holds h WHERE h.agent_id=OLD.agent_id AND h.session_id=OLD.session_id AND h.epoch=OLD.epoch AND h.generation=OLD.generation AND h.user_seq=OLD.user_seq AND h.goal_revision=OLD.goal_revision)
BEGIN SELECT RAISE(ABORT,'legacy evidence is immutable before fallback horizon'); END;
CREATE TRIGGER embedded_goal_continuations_legacy_hold_delete BEFORE DELETE ON embedded_goal_continuations
WHEN EXISTS(SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id) AND EXISTS(SELECT 1 FROM embedded_retention_legacy_holds h WHERE h.agent_id=OLD.agent_id AND h.session_id=OLD.session_id AND h.epoch=OLD.epoch AND h.generation=OLD.generation AND h.user_seq=OLD.user_seq AND h.goal_revision=OLD.goal_revision)
BEGIN SELECT RAISE(ABORT,'legacy evidence is immutable before fallback horizon'); END;
CREATE TRIGGER embedded_goal_snooze_calls_legacy_hold_delete BEFORE DELETE ON embedded_goal_snooze_calls
WHEN EXISTS(SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id) AND EXISTS(SELECT 1 FROM embedded_retention_legacy_holds h WHERE h.agent_id=OLD.agent_id AND h.session_id=OLD.session_id AND h.epoch=OLD.epoch AND h.generation=OLD.generation AND h.user_seq=OLD.user_seq AND h.goal_revision=OLD.goal_revision)
BEGIN SELECT RAISE(ABORT,'legacy evidence is immutable before fallback horizon'); END;
CREATE TRIGGER embedded_goal_snooze_preferences_legacy_hold_delete BEFORE DELETE ON embedded_goal_snooze_preferences
WHEN EXISTS(SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id) AND EXISTS(SELECT 1 FROM embedded_retention_legacy_holds h WHERE h.agent_id=OLD.agent_id AND h.session_id=OLD.session_id AND h.epoch=OLD.epoch AND h.generation=OLD.generation AND h.user_seq=OLD.user_seq AND h.goal_revision=OLD.goal_revision)
BEGIN SELECT RAISE(ABORT,'legacy evidence is immutable before fallback horizon'); END;
