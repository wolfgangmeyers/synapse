-- Historical evaluations retain the original user admission, never the final time.
ALTER TABLE embedded_goal_evaluations ADD COLUMN retention_basis_ms INTEGER NOT NULL DEFAULT 0 CHECK(retention_basis_ms>=0);
DROP TRIGGER embedded_goal_evaluation_immutable_result;
DROP TRIGGER embedded_goal_evaluations_retention_update;
UPDATE embedded_goal_evaluations AS v SET retention_basis_ms=COALESCE((SELECT e.retention_admitted_at_ms FROM embedded_entries e WHERE e.agent_id=v.agent_id AND e.session_id=v.session_id AND e.epoch=v.epoch AND e.generation=v.generation AND e.seq=v.user_seq AND e.digest=v.user_digest AND e.kind='user'),0);
CREATE TRIGGER embedded_goal_evaluation_immutable_result BEFORE UPDATE ON embedded_goal_evaluations
WHEN OLD.state<>'UNKNOWN' OR NEW.state='UNKNOWN' OR NEW.agent_id<>OLD.agent_id OR NEW.session_id<>OLD.session_id
 OR NEW.epoch<>OLD.epoch OR NEW.generation<>OLD.generation OR NEW.user_seq<>OLD.user_seq
 OR NEW.user_digest<>OLD.user_digest OR NEW.goal_revision<>OLD.goal_revision OR NEW.owner_token<>OLD.owner_token
 OR NEW.retention_basis_ms<>OLD.retention_basis_ms OR NEW.final_seq<>OLD.final_seq OR NEW.final_digest<>OLD.final_digest OR NEW.claimed_at<>OLD.claimed_at
BEGIN SELECT RAISE(ABORT,'embedded goal evaluation is immutable'); END;


CREATE TRIGGER embedded_goal_evaluations_retention_update BEFORE UPDATE ON embedded_goal_evaluations
WHEN NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=NEW.agent_id AND t.session_id=NEW.session_id AND t.epoch=NEW.epoch AND t.generation=NEW.generation AND t.user_seq=NEW.user_seq AND t.user_digest=NEW.user_digest AND NEW.retention_basis_ms>t.evidence_expired_through_ms) AND NOT EXISTS(SELECT 1 FROM embedded_execution_closures x WHERE x.agent_id=NEW.agent_id AND x.session_id=NEW.session_id AND x.epoch=NEW.epoch AND x.generation=NEW.generation AND x.user_seq=NEW.user_seq AND x.user_digest=NEW.user_digest AND x.goal_revision=NEW.goal_revision AND x.final_seq=NEW.final_seq AND x.final_digest=NEW.final_digest)
BEGIN SELECT RAISE(ABORT,'retention-expired embedded evidence'); END;


DROP TRIGGER embedded_goal_evaluations_retention_insert;
CREATE TRIGGER embedded_goal_evaluations_retention_insert BEFORE INSERT ON embedded_goal_evaluations
WHEN NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=NEW.agent_id AND t.session_id=NEW.session_id AND t.epoch=NEW.epoch AND t.generation=NEW.generation AND t.user_seq=NEW.user_seq AND t.user_digest=NEW.user_digest AND NEW.retention_basis_ms>t.evidence_expired_through_ms AND NEW.retention_basis_ms=(SELECT f.retention_admitted_at_ms FROM embedded_entries f WHERE f.agent_id=NEW.agent_id AND f.session_id=NEW.session_id AND f.epoch=NEW.epoch AND f.generation=NEW.generation AND f.seq=NEW.final_seq AND f.digest=NEW.final_digest AND f.kind='assistant'))
BEGIN SELECT RAISE(ABORT,'retention-expired embedded evidence'); END;

DROP TRIGGER embedded_goal_evaluation_immutable_delete;
CREATE TRIGGER embedded_goal_evaluation_immutable_delete BEFORE DELETE ON embedded_goal_evaluations
WHEN EXISTS(SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id) AND
((NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=OLD.agent_id AND t.session_id=OLD.session_id AND t.epoch=OLD.epoch AND t.generation=OLD.generation AND t.user_seq=OLD.user_seq AND t.user_digest=OLD.user_digest AND OLD.retention_basis_ms<=t.evidence_expired_through_ms) AND NOT (EXISTS(SELECT 1 FROM embedded_retention_legacy_drained_sessions s WHERE s.agent_id=OLD.agent_id AND s.session_id=OLD.session_id AND s.epoch=OLD.epoch AND s.generation=OLD.generation) AND NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=OLD.agent_id AND t.session_id=OLD.session_id AND t.epoch=OLD.epoch AND t.generation=OLD.generation AND t.user_seq=OLD.user_seq AND t.user_digest=OLD.user_digest))) OR EXISTS(SELECT 1 FROM embedded_retention_closures x WHERE x.agent_id=OLD.agent_id AND x.session_id=OLD.session_id AND x.epoch=OLD.epoch AND x.generation=OLD.generation AND x.user_seq=OLD.user_seq AND x.user_digest=OLD.user_digest AND x.goal_revision=OLD.goal_revision AND x.final_seq=OLD.final_seq AND x.final_digest=OLD.final_digest))
BEGIN SELECT RAISE(ABORT,'retention evidence is immutable while protected'); END;

DROP TRIGGER embedded_goal_continuation_no_delete;
CREATE TRIGGER embedded_goal_continuation_no_delete BEFORE DELETE ON embedded_goal_continuations
WHEN EXISTS(SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id) AND
((NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=OLD.agent_id AND t.session_id=OLD.session_id AND t.epoch=OLD.epoch AND t.generation=OLD.generation AND t.user_seq=OLD.user_seq AND t.user_digest=OLD.user_digest AND (SELECT v.retention_basis_ms FROM embedded_goal_evaluations v WHERE v.agent_id=OLD.agent_id AND v.session_id=OLD.session_id AND v.epoch=OLD.epoch AND v.generation=OLD.generation AND v.user_seq=OLD.user_seq AND v.user_digest=OLD.user_digest AND v.goal_revision=OLD.goal_revision AND v.final_seq=OLD.final_seq AND v.final_digest=OLD.final_digest)<=t.evidence_expired_through_ms) AND NOT (EXISTS(SELECT 1 FROM embedded_retention_legacy_drained_sessions s WHERE s.agent_id=OLD.agent_id AND s.session_id=OLD.session_id AND s.epoch=OLD.epoch AND s.generation=OLD.generation) AND NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=OLD.agent_id AND t.session_id=OLD.session_id AND t.epoch=OLD.epoch AND t.generation=OLD.generation AND t.user_seq=OLD.user_seq AND t.user_digest=OLD.user_digest))) OR EXISTS(SELECT 1 FROM embedded_retention_closures x WHERE x.agent_id=OLD.agent_id AND x.session_id=OLD.session_id AND x.epoch=OLD.epoch AND x.generation=OLD.generation AND x.user_seq=OLD.user_seq AND x.user_digest=OLD.user_digest AND x.goal_revision=OLD.goal_revision AND x.final_seq=OLD.final_seq AND x.final_digest=OLD.final_digest))
BEGIN SELECT RAISE(ABORT,'retention evidence is immutable while protected'); END;

DROP TRIGGER embedded_goal_continuations_retention_insert;
CREATE TRIGGER embedded_goal_continuations_retention_insert BEFORE INSERT ON embedded_goal_continuations
WHEN NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=NEW.agent_id AND t.session_id=NEW.session_id AND t.epoch=NEW.epoch AND t.generation=NEW.generation AND t.user_seq=NEW.user_seq AND t.user_digest=NEW.user_digest AND EXISTS(SELECT 1 FROM embedded_goal_evaluations v WHERE v.agent_id=NEW.agent_id AND v.session_id=NEW.session_id AND v.epoch=NEW.epoch AND v.generation=NEW.generation AND v.user_seq=NEW.user_seq AND v.user_digest=NEW.user_digest AND v.goal_revision=NEW.goal_revision AND v.final_seq=NEW.final_seq AND v.final_digest=NEW.final_digest AND v.retention_basis_ms>t.evidence_expired_through_ms))
BEGIN SELECT RAISE(ABORT,'retention-expired embedded evidence'); END;

DROP TRIGGER embedded_retention_pending_insert;
CREATE TRIGGER embedded_retention_pending_insert BEFORE INSERT ON embedded_goal_continuations
WHEN NOT EXISTS(SELECT 1 FROM embedded_retention_turns t JOIN embedded_goal_evaluations v ON v.agent_id=t.agent_id AND v.session_id=t.session_id AND v.epoch=t.epoch AND v.generation=t.generation AND v.user_seq=t.user_seq AND v.user_digest=t.user_digest AND v.goal_revision=NEW.goal_revision AND v.final_seq=NEW.final_seq AND v.final_digest=NEW.final_digest WHERE t.agent_id=NEW.agent_id AND t.session_id=NEW.session_id AND t.epoch=NEW.epoch AND t.generation=NEW.generation AND t.user_seq=NEW.user_seq AND t.user_digest=NEW.user_digest
AND (NEW.due_at IS NULL OR (julianday(NEW.due_at) IS NOT NULL AND CAST(ROUND((julianday(NEW.due_at)-2440587.5)*86400000) AS INTEGER)<=v.retention_basis_ms+2678400000))
AND NEW.retention_expires_at_ms=MAX(v.retention_basis_ms+2592000000,COALESCE(CAST(ROUND((julianday(NEW.due_at)-2440587.5)*86400000) AS INTEGER),v.retention_basis_ms+2592000000))+2592000000)
BEGIN SELECT RAISE(ABORT,'invalid pending retention lifetime'); END;

DROP TRIGGER embedded_execution_validate;
CREATE TRIGGER embedded_execution_validate BEFORE UPDATE OF execution_json,execution_token ON embedded_sessions
BEGIN
 SELECT CASE WHEN NEW.execution_token<OLD.execution_token OR NEW.execution_token>OLD.execution_token+1 THEN RAISE(ABORT,'execution token monotonic') END;
 SELECT CASE WHEN NEW.execution_json IS NOT NULL AND (
 json_type(NEW.execution_json)<>'object'
 OR (SELECT count(*) FROM json_each(NEW.execution_json))<>5
 OR EXISTS(SELECT 1 FROM json_each(NEW.execution_json) WHERE key NOT IN ('kind','owner','token','source','current'))
 OR json_extract(NEW.execution_json,'$.kind') NOT IN ('sdk','evaluator')
 OR json_type(NEW.execution_json,'$.kind') IS NOT 'text'
 OR json_type(NEW.execution_json,'$.owner') IS NOT 'integer'
 OR json_extract(NEW.execution_json,'$.owner')<>NEW.owner_token
 OR json_type(NEW.execution_json,'$.token') IS NOT 'integer'
 OR json_extract(NEW.execution_json,'$.token')<>NEW.execution_token
 OR (OLD.execution_json IS NULL AND NEW.execution_token<>OLD.execution_token+1)
 OR (OLD.execution_json IS NOT NULL AND json_extract(OLD.execution_json,'$.owner')=NEW.owner_token AND NEW.execution_token<>OLD.execution_token)
 OR (OLD.execution_json IS NOT NULL AND json_extract(OLD.execution_json,'$.owner')<>NEW.owner_token AND NEW.execution_token<>OLD.execution_token+1)
 OR (json_extract(NEW.execution_json,'$.kind')='evaluator' AND (json_type(NEW.execution_json,'$.source')<>'object' OR json_type(NEW.execution_json,'$.current')<>'null'))
 ) THEN RAISE(ABORT,'invalid execution record') END;
 SELECT CASE WHEN NEW.execution_json IS NOT NULL AND json_type(NEW.execution_json,'$.source') IS NOT 'null' AND (
 json_type(NEW.execution_json,'$.source') IS NOT 'object'
 OR (SELECT count(*) FROM json_each(json_extract(NEW.execution_json,'$.source')))<>6
 OR EXISTS(SELECT 1 FROM json_each(json_extract(NEW.execution_json,'$.source')) WHERE key NOT IN ('UserSeq','UserDigest','GoalRevision','FinalSeq','FinalDigest','call_id'))
 OR json_type(json_extract(NEW.execution_json,'$.source'),'$.UserSeq') IS NOT 'integer'
 OR json_type(json_extract(NEW.execution_json,'$.source'),'$.GoalRevision') IS NOT 'integer'
 OR json_type(json_extract(NEW.execution_json,'$.source'),'$.FinalSeq') IS NOT 'integer'
 OR json_type(json_extract(NEW.execution_json,'$.source'),'$.UserDigest') IS NOT 'text'
 OR json_type(json_extract(NEW.execution_json,'$.source'),'$.FinalDigest') IS NOT 'text'
 OR json_type(json_extract(NEW.execution_json,'$.source'),'$.call_id') IS NOT 'text'
 OR json_extract(json_extract(NEW.execution_json,'$.source'),'$.UserSeq')<=0 OR json_extract(json_extract(NEW.execution_json,'$.source'),'$.GoalRevision')<0
 OR length(json_extract(json_extract(NEW.execution_json,'$.source'),'$.UserDigest')) NOT BETWEEN 1 AND 256
 OR length(json_extract(json_extract(NEW.execution_json,'$.source'),'$.FinalDigest'))>256 OR length(json_extract(json_extract(NEW.execution_json,'$.source'),'$.call_id'))>256
 OR NOT EXISTS(SELECT 1 FROM embedded_entries e WHERE e.agent_id=NEW.agent_id AND e.session_id=NEW.session_id AND e.epoch=NEW.epoch AND e.generation=NEW.generation AND e.seq=json_extract(json_extract(NEW.execution_json,'$.source'),'$.UserSeq') AND e.digest=json_extract(json_extract(NEW.execution_json,'$.source'),'$.UserDigest') AND e.kind='user')
 OR (NEW.execution_token=OLD.execution_token AND json_extract(OLD.execution_json,'$.source') IS NOT NULL AND json_extract(OLD.execution_json,'$.source')<>'null' AND (
json_extract(json_extract(NEW.execution_json,'$.source'),'$.UserSeq') IS NOT json_extract(json_extract(OLD.execution_json,'$.source'),'$.UserSeq') OR json_extract(json_extract(NEW.execution_json,'$.source'),'$.UserDigest') IS NOT json_extract(json_extract(OLD.execution_json,'$.source'),'$.UserDigest') OR json_extract(json_extract(NEW.execution_json,'$.source'),'$.GoalRevision') IS NOT json_extract(json_extract(OLD.execution_json,'$.source'),'$.GoalRevision') OR json_extract(json_extract(NEW.execution_json,'$.source'),'$.FinalSeq') IS NOT json_extract(json_extract(OLD.execution_json,'$.source'),'$.FinalSeq') OR json_extract(json_extract(NEW.execution_json,'$.source'),'$.FinalDigest') IS NOT json_extract(json_extract(OLD.execution_json,'$.source'),'$.FinalDigest')))
 OR (NEW.execution_token=OLD.execution_token AND json_extract(OLD.execution_json,'$.source') IS NOT NULL AND json_extract(OLD.execution_json,'$.source')<>'null' AND json_extract(json_extract(NEW.execution_json,'$.source'),'$.call_id') IS NOT json_extract(json_extract(OLD.execution_json,'$.source'),'$.call_id') AND EXISTS(SELECT 1 FROM embedded_entries e WHERE e.agent_id=NEW.agent_id AND e.session_id=NEW.session_id AND e.epoch=NEW.epoch AND e.seq=json_extract(json_extract(NEW.execution_json,'$.source'),'$.UserSeq') AND NEW.evidence_expired_through_ms>=e.retention_admitted_at_ms))
 ) THEN RAISE(ABORT,'invalid execution closure') END;
 SELECT CASE WHEN NEW.execution_json IS NOT NULL AND NEW.execution_token=OLD.execution_token AND json_extract(OLD.execution_json,'$.source') IS NOT NULL AND json_extract(OLD.execution_json,'$.source')<>'null' AND json_type(NEW.execution_json,'$.source')='null' THEN RAISE(ABORT,'execution closure immutable') END;
 SELECT CASE WHEN NEW.execution_json IS NOT NULL AND json_type(NEW.execution_json,'$.current') IS NOT 'null' AND (
 json_type(NEW.execution_json,'$.current') IS NOT 'object'
 OR (SELECT count(*) FROM json_each(json_extract(NEW.execution_json,'$.current')))<>6
 OR EXISTS(SELECT 1 FROM json_each(json_extract(NEW.execution_json,'$.current')) WHERE key NOT IN ('UserSeq','UserDigest','GoalRevision','FinalSeq','FinalDigest','call_id'))
 OR json_type(json_extract(NEW.execution_json,'$.current'),'$.UserSeq') IS NOT 'integer'
 OR json_type(json_extract(NEW.execution_json,'$.current'),'$.GoalRevision') IS NOT 'integer'
 OR json_type(json_extract(NEW.execution_json,'$.current'),'$.FinalSeq') IS NOT 'integer'
 OR json_type(json_extract(NEW.execution_json,'$.current'),'$.UserDigest') IS NOT 'text'
 OR json_type(json_extract(NEW.execution_json,'$.current'),'$.FinalDigest') IS NOT 'text'
 OR json_type(json_extract(NEW.execution_json,'$.current'),'$.call_id') IS NOT 'text'
 OR json_extract(json_extract(NEW.execution_json,'$.current'),'$.UserSeq')<=0 OR json_extract(json_extract(NEW.execution_json,'$.current'),'$.GoalRevision')<0
 OR length(json_extract(json_extract(NEW.execution_json,'$.current'),'$.UserDigest')) NOT BETWEEN 1 AND 256
 OR length(json_extract(json_extract(NEW.execution_json,'$.current'),'$.FinalDigest'))>256 OR length(json_extract(json_extract(NEW.execution_json,'$.current'),'$.call_id'))>256
 OR NOT EXISTS(SELECT 1 FROM embedded_entries e WHERE e.agent_id=NEW.agent_id AND e.session_id=NEW.session_id AND e.epoch=NEW.epoch AND e.generation=NEW.generation AND e.seq=json_extract(json_extract(NEW.execution_json,'$.current'),'$.UserSeq') AND e.digest=json_extract(json_extract(NEW.execution_json,'$.current'),'$.UserDigest') AND e.kind='user')
 OR (NEW.execution_token=OLD.execution_token AND json_type(OLD.execution_json,'$.current')='object' AND json_extract(json_extract(NEW.execution_json,'$.current'),'$.UserSeq')<>json_extract(json_extract(OLD.execution_json,'$.current'),'$.UserSeq') AND (
 json_extract(json_extract(NEW.execution_json,'$.current'),'$.UserSeq')<=json_extract(json_extract(OLD.execution_json,'$.current'),'$.UserSeq')
 OR json_extract(json_extract(NEW.execution_json,'$.current'),'$.UserSeq')<>(SELECT MAX(seq) FROM embedded_entries e WHERE e.agent_id=NEW.agent_id AND e.session_id=NEW.session_id AND e.epoch=NEW.epoch AND e.generation=NEW.generation)
 OR NOT EXISTS(SELECT 1 FROM embedded_entries e WHERE e.agent_id=NEW.agent_id AND e.session_id=NEW.session_id AND e.epoch=NEW.epoch AND e.generation=NEW.generation AND e.seq=json_extract(json_extract(NEW.execution_json,'$.current'),'$.UserSeq') AND e.retention_admitted_at_ms>NEW.evidence_expired_through_ms)))
 OR (NEW.execution_token=OLD.execution_token AND json_extract(json_extract(NEW.execution_json,'$.current'),'$.UserSeq')=json_extract(json_extract(OLD.execution_json,'$.current'),'$.UserSeq') AND (json_extract(json_extract(NEW.execution_json,'$.current'),'$.UserDigest') IS NOT json_extract(json_extract(OLD.execution_json,'$.current'),'$.UserDigest') OR json_extract(json_extract(NEW.execution_json,'$.current'),'$.GoalRevision') IS NOT json_extract(json_extract(OLD.execution_json,'$.current'),'$.GoalRevision')))

 OR (NEW.execution_token=OLD.execution_token AND json_extract(OLD.execution_json,'$.current') IS NOT NULL AND json_extract(OLD.execution_json,'$.current')<>'null' AND json_extract(json_extract(NEW.execution_json,'$.current'),'$.call_id') IS NOT json_extract(json_extract(OLD.execution_json,'$.current'),'$.call_id') AND EXISTS(SELECT 1 FROM embedded_entries e WHERE e.agent_id=NEW.agent_id AND e.session_id=NEW.session_id AND e.epoch=NEW.epoch AND e.seq=json_extract(json_extract(NEW.execution_json,'$.current'),'$.UserSeq') AND NEW.evidence_expired_through_ms>=e.retention_admitted_at_ms))
 ) THEN RAISE(ABORT,'invalid execution closure') END;
 SELECT CASE WHEN NEW.execution_json IS NOT NULL AND NEW.execution_token=OLD.execution_token AND json_extract(OLD.execution_json,'$.current') IS NOT NULL AND json_extract(OLD.execution_json,'$.current')<>'null' AND json_type(NEW.execution_json,'$.current')='null' THEN RAISE(ABORT,'execution closure immutable') END;
 SELECT CASE WHEN NEW.execution_json IS NOT NULL AND NEW.execution_token=OLD.execution_token AND json_extract(NEW.execution_json,'$.kind') IS NOT json_extract(OLD.execution_json,'$.kind') THEN RAISE(ABORT,'execution kind immutable') END;
 SELECT CASE WHEN NEW.execution_json IS NOT NULL AND json_type(NEW.execution_json,'$.source')='object' AND (NEW.execution_token<>OLD.execution_token OR json_type(OLD.execution_json,'$.source') IS NOT 'object') AND NOT EXISTS(
 SELECT 1 FROM embedded_goal_evaluations v JOIN embedded_entries e ON e.agent_id=v.agent_id AND e.session_id=v.session_id AND e.epoch=v.epoch AND e.generation=v.generation AND e.seq=v.user_seq
 WHERE v.agent_id=NEW.agent_id AND v.session_id=NEW.session_id AND v.epoch=NEW.epoch AND v.generation=NEW.generation
 AND v.user_seq=json_extract(json_extract(NEW.execution_json,'$.source'),'$.UserSeq') AND v.user_digest=json_extract(json_extract(NEW.execution_json,'$.source'),'$.UserDigest') AND v.goal_revision=json_extract(json_extract(NEW.execution_json,'$.source'),'$.GoalRevision') AND v.final_seq=json_extract(json_extract(NEW.execution_json,'$.source'),'$.FinalSeq') AND v.final_digest=json_extract(json_extract(NEW.execution_json,'$.source'),'$.FinalDigest')
 AND ((json_extract(NEW.execution_json,'$.kind')='evaluator' AND v.state='UNKNOWN' AND v.owner_token=NEW.owner_token AND v.retention_basis_ms>NEW.evidence_expired_through_ms)
 OR (json_extract(NEW.execution_json,'$.kind')='sdk' AND v.state='NOT_DONE' AND EXISTS(SELECT 1 FROM embedded_goal_continuations c WHERE c.agent_id=v.agent_id AND c.session_id=v.session_id AND c.epoch=v.epoch AND c.generation=v.generation AND c.user_seq=v.user_seq AND c.user_digest=v.user_digest AND c.goal_revision=v.goal_revision AND c.final_seq=v.final_seq AND c.final_digest=v.final_digest AND c.state='reserved' AND c.disposition='pending' AND c.retention_expires_at_ms>NEW.evidence_expired_through_ms+2592000000)))
 ) THEN RAISE(ABORT,'invalid execution source authority') END;
 SELECT CASE WHEN NEW.execution_json IS NOT NULL AND json_type(NEW.execution_json,'$.source')='object' AND json_extract(json_extract(NEW.execution_json,'$.source'),'$.call_id')<>'' AND NOT EXISTS(SELECT 1 FROM embedded_goal_snooze_calls c WHERE c.call_id=json_extract(json_extract(NEW.execution_json,'$.source'),'$.call_id') AND c.agent_id=NEW.agent_id AND c.session_id=NEW.session_id AND c.epoch=NEW.epoch AND c.generation=NEW.generation AND c.user_seq=json_extract(json_extract(NEW.execution_json,'$.source'),'$.UserSeq') AND c.user_digest=json_extract(json_extract(NEW.execution_json,'$.source'),'$.UserDigest') AND c.goal_revision=json_extract(json_extract(NEW.execution_json,'$.source'),'$.GoalRevision')) THEN RAISE(ABORT,'invalid execution referenced call') END;
 SELECT CASE WHEN NEW.execution_json IS NOT NULL AND json_type(NEW.execution_json,'$.current')='object' AND (NEW.execution_token<>OLD.execution_token OR json_type(OLD.execution_json,'$.current') IS NOT 'object') AND (NOT EXISTS(SELECT 1 FROM embedded_entries e WHERE e.agent_id=NEW.agent_id AND e.session_id=NEW.session_id AND e.epoch=NEW.epoch AND e.generation=NEW.generation AND e.seq=json_extract(json_extract(NEW.execution_json,'$.current'),'$.UserSeq') AND e.retention_admitted_at_ms>NEW.evidence_expired_through_ms) OR json_extract(json_extract(NEW.execution_json,'$.current'),'$.FinalSeq')<>0 OR json_extract(json_extract(NEW.execution_json,'$.current'),'$.FinalDigest')<>'') THEN RAISE(ABORT,'execution admission expired or invalid') END;
 SELECT CASE WHEN NEW.execution_json IS NOT NULL AND json_type(NEW.execution_json,'$.current')='object' AND json_extract(json_extract(NEW.execution_json,'$.current'),'$.call_id')<>'' AND NOT EXISTS(SELECT 1 FROM embedded_goal_snooze_calls c WHERE c.call_id=json_extract(json_extract(NEW.execution_json,'$.current'),'$.call_id') AND c.agent_id=NEW.agent_id AND c.session_id=NEW.session_id AND c.epoch=NEW.epoch AND c.generation=NEW.generation AND c.user_seq=json_extract(json_extract(NEW.execution_json,'$.current'),'$.UserSeq') AND c.user_digest=json_extract(json_extract(NEW.execution_json,'$.current'),'$.UserDigest') AND c.goal_revision=json_extract(json_extract(NEW.execution_json,'$.current'),'$.GoalRevision')) THEN RAISE(ABORT,'invalid execution referenced call') END;

END;

DROP VIEW embedded_retention_closures;
CREATE VIEW embedded_retention_closures AS
SELECT agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,final_seq,final_digest,call_id FROM embedded_execution_closures
UNION ALL
SELECT v.agent_id,v.session_id,v.epoch,v.generation,v.user_seq,v.user_digest,v.goal_revision,v.final_seq,v.final_digest,COALESCE(p.call_id,'')
FROM embedded_goal_evaluations v JOIN embedded_sessions s USING(agent_id,session_id,epoch,generation)
JOIN embedded_goal_applied_receipts r ON r.agent_id=v.agent_id AND r.session_id=v.session_id AND r.epoch=v.epoch AND r.generation=v.generation AND r.turn_entry_seq=v.user_seq AND r.turn_entry_digest=v.user_digest AND r.goal_revision=v.goal_revision AND r.goal_enabled=1
JOIN embedded_entries f ON f.agent_id=v.agent_id AND f.session_id=v.session_id AND f.epoch=v.epoch AND f.generation=v.generation AND f.seq=v.final_seq AND f.digest=v.final_digest AND f.kind='assistant'
LEFT JOIN embedded_goal_snooze_preferences p USING(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision)
WHERE v.retention_basis_ms>0 AND v.retention_basis_ms>s.evidence_expired_through_ms
AND NOT EXISTS(SELECT 1 FROM embedded_retention_legacy_holds h WHERE h.agent_id=v.agent_id AND h.session_id=v.session_id AND h.epoch=v.epoch AND h.generation=v.generation AND h.user_seq=v.user_seq AND h.goal_revision=v.goal_revision)
UNION ALL
SELECT c.agent_id,c.session_id,c.epoch,c.generation,c.user_seq,c.user_digest,c.goal_revision,c.final_seq,c.final_digest,COALESCE(p.call_id,'')
FROM embedded_goal_continuations c JOIN embedded_goal_evaluations v USING(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,final_seq,final_digest)
JOIN embedded_sessions s USING(agent_id,session_id,epoch,generation)
LEFT JOIN embedded_goal_snooze_preferences p USING(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision)
WHERE c.state='reserved' AND v.state='NOT_DONE' AND c.retention_expires_at_ms>s.evidence_expired_through_ms+2592000000;

DROP TRIGGER embedded_goal_snooze_call_no_delete;
CREATE TRIGGER embedded_goal_snooze_call_no_delete BEFORE DELETE ON embedded_goal_snooze_calls
WHEN EXISTS(SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id) AND
((NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=OLD.agent_id AND t.session_id=OLD.session_id AND t.epoch=OLD.epoch AND t.generation=OLD.generation AND t.user_seq=OLD.user_seq AND t.user_digest=OLD.user_digest AND t.retention_admitted_at_ms<=t.evidence_expired_through_ms) AND NOT (EXISTS(SELECT 1 FROM embedded_retention_legacy_drained_sessions s WHERE s.agent_id=OLD.agent_id AND s.session_id=OLD.session_id AND s.epoch=OLD.epoch AND s.generation=OLD.generation) AND NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=OLD.agent_id AND t.session_id=OLD.session_id AND t.epoch=OLD.epoch AND t.generation=OLD.generation AND t.user_seq=OLD.user_seq AND t.user_digest=OLD.user_digest))) OR EXISTS(SELECT 1 FROM embedded_retention_closures x WHERE x.agent_id=OLD.agent_id AND x.session_id=OLD.session_id AND x.epoch=OLD.epoch AND x.generation=OLD.generation AND x.user_seq=OLD.user_seq AND x.user_digest=OLD.user_digest AND x.goal_revision=OLD.goal_revision))
BEGIN SELECT RAISE(ABORT,'retention evidence is immutable while protected'); END;

-- A historical A-based row can expire while its later final is still fresh.
-- Retain only its exact identity until the final itself crosses the durable
-- cutoff; afterwards the cutoff alone prevents recreation, including rollback.
CREATE TABLE embedded_evaluation_expired_keys (
 agent_id TEXT NOT NULL, session_id TEXT NOT NULL, epoch INTEGER NOT NULL, generation TEXT NOT NULL,
 user_seq INTEGER NOT NULL, user_digest TEXT NOT NULL, goal_revision INTEGER NOT NULL,
 final_seq INTEGER NOT NULL, final_digest TEXT NOT NULL, final_admitted_ms INTEGER NOT NULL CHECK(final_admitted_ms>0),
 PRIMARY KEY(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,final_seq,final_digest),
 FOREIGN KEY(agent_id,session_id,epoch,generation) REFERENCES embedded_sessions(agent_id,session_id,epoch,generation) ON DELETE CASCADE
);
CREATE TRIGGER embedded_evaluation_expired_key_capture BEFORE DELETE ON embedded_goal_evaluations
WHEN EXISTS(SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id)
BEGIN
 INSERT OR IGNORE INTO embedded_evaluation_expired_keys(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,final_seq,final_digest,final_admitted_ms)
 SELECT OLD.agent_id,OLD.session_id,OLD.epoch,OLD.generation,OLD.user_seq,OLD.user_digest,OLD.goal_revision,OLD.final_seq,OLD.final_digest,f.retention_admitted_at_ms
 FROM embedded_entries f JOIN embedded_sessions s USING(agent_id,session_id,epoch,generation)
 WHERE f.agent_id=OLD.agent_id AND f.session_id=OLD.session_id AND f.epoch=OLD.epoch AND f.generation=OLD.generation AND f.seq=OLD.final_seq AND f.digest=OLD.final_digest AND f.kind='assistant' AND f.retention_admitted_at_ms>s.evidence_expired_through_ms AND NOT EXISTS(SELECT 1 FROM embedded_evaluation_expired_keys x WHERE x.agent_id=OLD.agent_id AND x.session_id=OLD.session_id AND x.epoch=OLD.epoch AND x.generation=OLD.generation AND x.user_seq=OLD.user_seq AND x.user_digest=OLD.user_digest AND x.goal_revision=OLD.goal_revision AND x.final_seq=OLD.final_seq AND x.final_digest=OLD.final_digest);
END;
-- Capture surviving historical identities during this upgrade as well.
INSERT OR IGNORE INTO embedded_evaluation_expired_keys(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,final_seq,final_digest,final_admitted_ms)
SELECT v.agent_id,v.session_id,v.epoch,v.generation,v.user_seq,v.user_digest,v.goal_revision,v.final_seq,v.final_digest,f.retention_admitted_at_ms
FROM embedded_goal_evaluations v JOIN embedded_entries f ON f.agent_id=v.agent_id AND f.session_id=v.session_id AND f.epoch=v.epoch AND f.generation=v.generation AND f.seq=v.final_seq AND f.digest=v.final_digest AND f.kind='assistant'
JOIN embedded_sessions s ON s.agent_id=v.agent_id AND s.session_id=v.session_id AND s.epoch=v.epoch AND s.generation=v.generation
WHERE v.retention_basis_ms<f.retention_admitted_at_ms AND f.retention_admitted_at_ms>s.evidence_expired_through_ms;
-- BEFORE INSERT runs for OR REPLACE even when recursive_triggers is off.
-- Require both the exact evaluation and its immutable final admission, and
-- reject duplicates before SQLite can replace a protected tombstone.
CREATE TRIGGER embedded_evaluation_expired_key_insert BEFORE INSERT ON embedded_evaluation_expired_keys
WHEN EXISTS(SELECT 1 FROM embedded_evaluation_expired_keys x WHERE x.agent_id=NEW.agent_id AND x.session_id=NEW.session_id AND x.epoch=NEW.epoch AND x.generation=NEW.generation AND x.user_seq=NEW.user_seq AND x.user_digest=NEW.user_digest AND x.goal_revision=NEW.goal_revision AND x.final_seq=NEW.final_seq AND x.final_digest=NEW.final_digest)
 OR NOT EXISTS(SELECT 1 FROM embedded_goal_evaluations v JOIN embedded_entries f ON f.agent_id=v.agent_id AND f.session_id=v.session_id AND f.epoch=v.epoch AND f.generation=v.generation AND f.seq=v.final_seq AND f.digest=v.final_digest AND f.kind='assistant' JOIN embedded_sessions s ON s.agent_id=v.agent_id AND s.session_id=v.session_id AND s.epoch=v.epoch AND s.generation=v.generation WHERE v.agent_id=NEW.agent_id AND v.session_id=NEW.session_id AND v.epoch=NEW.epoch AND v.generation=NEW.generation AND v.user_seq=NEW.user_seq AND v.user_digest=NEW.user_digest AND v.goal_revision=NEW.goal_revision AND v.final_seq=NEW.final_seq AND v.final_digest=NEW.final_digest AND f.retention_admitted_at_ms=NEW.final_admitted_ms AND NEW.final_admitted_ms>s.evidence_expired_through_ms)
BEGIN SELECT RAISE(ABORT,'invalid expired evaluation key'); END;
CREATE TRIGGER embedded_evaluation_expired_key_guard BEFORE INSERT ON embedded_goal_evaluations
WHEN EXISTS(SELECT 1 FROM embedded_evaluation_expired_keys x WHERE x.agent_id=NEW.agent_id AND x.session_id=NEW.session_id AND x.epoch=NEW.epoch AND x.generation=NEW.generation AND x.user_seq=NEW.user_seq AND x.user_digest=NEW.user_digest AND x.goal_revision=NEW.goal_revision AND x.final_seq=NEW.final_seq AND x.final_digest=NEW.final_digest)
BEGIN SELECT RAISE(ABORT,'retention-expired embedded evidence'); END;
CREATE TRIGGER embedded_evaluation_expired_key_immutable BEFORE UPDATE ON embedded_evaluation_expired_keys
BEGIN SELECT RAISE(ABORT,'expired evaluation key immutable'); END;
CREATE TRIGGER embedded_evaluation_expired_key_delete BEFORE DELETE ON embedded_evaluation_expired_keys
WHEN EXISTS(SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id) AND EXISTS(SELECT 1 FROM embedded_sessions s WHERE s.agent_id=OLD.agent_id AND s.session_id=OLD.session_id AND s.epoch=OLD.epoch AND s.generation=OLD.generation AND s.evidence_expired_through_ms<OLD.final_admitted_ms)
BEGIN SELECT RAISE(ABORT,'expired evaluation key still required'); END;
