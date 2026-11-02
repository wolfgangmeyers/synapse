-- One bounded execution slot, not a ledger. Tokens survive clearing/ownership.
ALTER TABLE embedded_sessions ADD COLUMN execution_token INTEGER NOT NULL DEFAULT 0 CHECK(typeof(execution_token)='integer' AND execution_token>=0);
ALTER TABLE embedded_sessions ADD COLUMN execution_json TEXT CHECK(execution_json IS NULL OR (length(execution_json)<=8192 AND json_valid(execution_json)));
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
 AND ((json_extract(NEW.execution_json,'$.kind')='evaluator' AND v.state='UNKNOWN' AND v.owner_token=NEW.owner_token AND e.retention_admitted_at_ms>NEW.evidence_expired_through_ms)
 OR (json_extract(NEW.execution_json,'$.kind')='sdk' AND v.state='NOT_DONE' AND EXISTS(SELECT 1 FROM embedded_goal_continuations c WHERE c.agent_id=v.agent_id AND c.session_id=v.session_id AND c.epoch=v.epoch AND c.generation=v.generation AND c.user_seq=v.user_seq AND c.user_digest=v.user_digest AND c.goal_revision=v.goal_revision AND c.state='reserved' AND c.disposition='pending' AND MAX(e.retention_admitted_at_ms+2592000000,COALESCE(CAST((julianday(c.due_at)-2440587.5)*86400000 AS INTEGER),e.retention_admitted_at_ms+2592000000))>NEW.evidence_expired_through_ms)))
 ) THEN RAISE(ABORT,'invalid execution source authority') END;
 SELECT CASE WHEN NEW.execution_json IS NOT NULL AND json_type(NEW.execution_json,'$.source')='object' AND json_extract(json_extract(NEW.execution_json,'$.source'),'$.call_id')<>'' AND NOT EXISTS(SELECT 1 FROM embedded_goal_snooze_calls c WHERE c.call_id=json_extract(json_extract(NEW.execution_json,'$.source'),'$.call_id') AND c.agent_id=NEW.agent_id AND c.session_id=NEW.session_id AND c.epoch=NEW.epoch AND c.generation=NEW.generation AND c.user_seq=json_extract(json_extract(NEW.execution_json,'$.source'),'$.UserSeq') AND c.user_digest=json_extract(json_extract(NEW.execution_json,'$.source'),'$.UserDigest') AND c.goal_revision=json_extract(json_extract(NEW.execution_json,'$.source'),'$.GoalRevision')) THEN RAISE(ABORT,'invalid execution referenced call') END;
 SELECT CASE WHEN NEW.execution_json IS NOT NULL AND json_type(NEW.execution_json,'$.current')='object' AND (NEW.execution_token<>OLD.execution_token OR json_type(OLD.execution_json,'$.current') IS NOT 'object') AND (NOT EXISTS(SELECT 1 FROM embedded_entries e WHERE e.agent_id=NEW.agent_id AND e.session_id=NEW.session_id AND e.epoch=NEW.epoch AND e.generation=NEW.generation AND e.seq=json_extract(json_extract(NEW.execution_json,'$.current'),'$.UserSeq') AND e.retention_admitted_at_ms>NEW.evidence_expired_through_ms) OR json_extract(json_extract(NEW.execution_json,'$.current'),'$.FinalSeq')<>0 OR json_extract(json_extract(NEW.execution_json,'$.current'),'$.FinalDigest')<>'') THEN RAISE(ABORT,'execution admission expired or invalid') END;
 SELECT CASE WHEN NEW.execution_json IS NOT NULL AND json_type(NEW.execution_json,'$.current')='object' AND json_extract(json_extract(NEW.execution_json,'$.current'),'$.call_id')<>'' AND NOT EXISTS(SELECT 1 FROM embedded_goal_snooze_calls c WHERE c.call_id=json_extract(json_extract(NEW.execution_json,'$.current'),'$.call_id') AND c.agent_id=NEW.agent_id AND c.session_id=NEW.session_id AND c.epoch=NEW.epoch AND c.generation=NEW.generation AND c.user_seq=json_extract(json_extract(NEW.execution_json,'$.current'),'$.UserSeq') AND c.user_digest=json_extract(json_extract(NEW.execution_json,'$.current'),'$.UserDigest') AND c.goal_revision=json_extract(json_extract(NEW.execution_json,'$.current'),'$.GoalRevision')) THEN RAISE(ABORT,'invalid execution referenced call') END;

END;
CREATE TRIGGER embedded_execution_insert BEFORE INSERT ON embedded_sessions
WHEN NEW.execution_token<>0 OR NEW.execution_json IS NOT NULL
BEGIN SELECT RAISE(ABORT,'execution insert forbidden'); END;
CREATE VIEW embedded_execution_closures AS
SELECT s.agent_id,s.session_id,s.epoch,s.generation,json_extract(json_extract(s.execution_json,'$.source'),'$.UserSeq') AS user_seq,json_extract(json_extract(s.execution_json,'$.source'),'$.UserDigest') AS user_digest,json_extract(json_extract(s.execution_json,'$.source'),'$.GoalRevision') AS goal_revision,json_extract(json_extract(s.execution_json,'$.source'),'$.FinalSeq') AS final_seq,json_extract(json_extract(s.execution_json,'$.source'),'$.FinalDigest') AS final_digest,json_extract(json_extract(s.execution_json,'$.source'),'$.call_id') AS call_id FROM embedded_sessions s WHERE s.current=1 AND s.execution_json IS NOT NULL AND s.owner_token=json_extract(s.execution_json,'$.owner') AND s.execution_token=json_extract(s.execution_json,'$.token') AND json_type(s.execution_json,'$.source')='object' UNION ALL
SELECT s.agent_id,s.session_id,s.epoch,s.generation,json_extract(json_extract(s.execution_json,'$.current'),'$.UserSeq') AS user_seq,json_extract(json_extract(s.execution_json,'$.current'),'$.UserDigest') AS user_digest,json_extract(json_extract(s.execution_json,'$.current'),'$.GoalRevision') AS goal_revision,json_extract(json_extract(s.execution_json,'$.current'),'$.FinalSeq') AS final_seq,json_extract(json_extract(s.execution_json,'$.current'),'$.FinalDigest') AS final_digest,json_extract(json_extract(s.execution_json,'$.current'),'$.call_id') AS call_id FROM embedded_sessions s WHERE s.current=1 AND s.execution_json IS NOT NULL AND s.owner_token=json_extract(s.execution_json,'$.owner') AND s.execution_token=json_extract(s.execution_json,'$.token') AND json_type(s.execution_json,'$.current')='object';

-- Install while the immutable source is still reserved. Historical started rows
-- cannot pass source admission, even with a fresh execution token after clear.
CREATE TRIGGER embedded_execution_continuation_start BEFORE UPDATE OF state ON embedded_goal_continuations
WHEN OLD.state='reserved' AND NEW.state='started'
BEGIN
 SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM embedded_sessions s WHERE s.agent_id=NEW.agent_id AND s.session_id=NEW.session_id AND s.epoch=NEW.epoch AND s.generation=NEW.generation AND s.current=1 AND (s.execution_json IS NULL OR json_extract(s.execution_json,'$.owner')<>s.owner_token)) THEN RAISE(ABORT,'embedded execution already active') END;
 UPDATE embedded_sessions SET execution_token=execution_token+1,execution_json=json_object(
 'kind','sdk','owner',owner_token,'token',execution_token+1,
 'source',json_object('UserSeq',NEW.user_seq,'UserDigest',NEW.user_digest,'GoalRevision',NEW.goal_revision,
 'FinalSeq',(SELECT final_seq FROM embedded_goal_evaluations v WHERE v.agent_id=NEW.agent_id AND v.session_id=NEW.session_id AND v.epoch=NEW.epoch AND v.user_seq=NEW.user_seq AND v.user_digest=NEW.user_digest AND v.goal_revision=NEW.goal_revision),
 'FinalDigest',(SELECT final_digest FROM embedded_goal_evaluations v WHERE v.agent_id=NEW.agent_id AND v.session_id=NEW.session_id AND v.epoch=NEW.epoch AND v.user_seq=NEW.user_seq AND v.user_digest=NEW.user_digest AND v.goal_revision=NEW.goal_revision),
 'call_id',COALESCE((SELECT call_id FROM embedded_goal_snooze_preferences p WHERE p.agent_id=NEW.agent_id AND p.session_id=NEW.session_id AND p.epoch=NEW.epoch AND p.generation=NEW.generation AND p.user_seq=NEW.user_seq AND p.user_digest=NEW.user_digest AND p.goal_revision=NEW.goal_revision),'')),
 'current',NULL)
 WHERE agent_id=NEW.agent_id AND session_id=NEW.session_id AND epoch=NEW.epoch AND generation=NEW.generation AND current=1;
END;
CREATE TRIGGER embedded_execution_continuation_started AFTER UPDATE OF state ON embedded_goal_continuations
WHEN OLD.state='reserved' AND NEW.state='started'
BEGIN
 SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM embedded_execution_closures x WHERE x.agent_id=NEW.agent_id AND x.session_id=NEW.session_id AND x.epoch=NEW.epoch AND x.generation=NEW.generation AND x.user_seq=NEW.user_seq AND x.user_digest=NEW.user_digest AND x.goal_revision=NEW.goal_revision) THEN RAISE(ABORT,'missing atomic continuation execution') END;
END;
