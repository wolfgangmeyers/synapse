-- Upgrade turn/revision identity to include the checked settled final.
-- Snapshot leaves before rebuilding parents; retain exact historical states and deadlines.

DROP TRIGGER IF EXISTS embedded_execution_validate;

DROP TRIGGER IF EXISTS embedded_goal_continuation_policy_invalidate;

DROP TRIGGER IF EXISTS embedded_goal_continuation_session_invalidate;

DROP TRIGGER IF EXISTS embedded_goal_continuation_epoch_invalidate;

DROP VIEW IF EXISTS embedded_retention_closures;

DROP VIEW IF EXISTS embedded_retention_legacy_holds;

CREATE TEMP TABLE goal_final_eval_134 AS SELECT * FROM embedded_goal_evaluations;

CREATE TEMP TABLE goal_final_cont_134 AS SELECT * FROM embedded_goal_continuations;

CREATE TEMP TABLE goal_final_pub_134 AS SELECT * FROM embedded_goal_activity_publications;

DROP TABLE embedded_goal_activity_publications;

DROP TABLE embedded_goal_continuations;

DROP TABLE embedded_goal_evaluations;

CREATE TABLE embedded_goal_evaluations (
 agent_id TEXT NOT NULL, session_id TEXT NOT NULL, epoch INTEGER NOT NULL,
 generation TEXT NOT NULL, user_seq INTEGER NOT NULL CHECK(user_seq>0), user_digest TEXT NOT NULL,
 goal_revision INTEGER NOT NULL CHECK(goal_revision>0),
 owner_token INTEGER NOT NULL CHECK(owner_token>0),
 final_seq INTEGER NOT NULL CHECK(final_seq>0), final_digest TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('UNKNOWN','DONE','NOT_DONE')),
 reason TEXT NOT NULL DEFAULT '', claimed_at TEXT NOT NULL, completed_at TEXT,
 PRIMARY KEY(agent_id,session_id,epoch,user_seq,user_digest,goal_revision,final_seq,final_digest),
 FOREIGN KEY(agent_id,session_id,epoch,generation) REFERENCES embedded_sessions(agent_id,session_id,epoch,generation) ON DELETE CASCADE,
 FOREIGN KEY(agent_id,session_id,epoch,generation,user_seq) REFERENCES embedded_entries(agent_id,session_id,epoch,generation,seq),
 FOREIGN KEY(agent_id,session_id,epoch,generation,final_seq) REFERENCES embedded_entries(agent_id,session_id,epoch,generation,seq),
 CHECK((state='UNKNOWN' AND completed_at IS NULL AND reason='') OR (state<>'UNKNOWN' AND completed_at IS NOT NULL))
);

CREATE TABLE embedded_goal_continuations (
 agent_id TEXT NOT NULL, session_id TEXT NOT NULL, epoch INTEGER NOT NULL,
 generation TEXT NOT NULL, user_seq INTEGER NOT NULL, user_digest TEXT NOT NULL,
 goal_revision INTEGER NOT NULL, final_seq INTEGER NOT NULL, final_digest TEXT NOT NULL, origin TEXT NOT NULL DEFAULT 'goal_reevaluation' CHECK(origin='goal_reevaluation'),
 state TEXT NOT NULL CHECK(state IN ('reserved','started','invalidated')),
 disposition TEXT NOT NULL CHECK(disposition IN ('pending','unknown','invalidated')),
 reserved_at TEXT NOT NULL, due_at TEXT, started_at TEXT,
 retention_expires_at_ms INTEGER NOT NULL DEFAULT 0 CHECK(retention_expires_at_ms>=0),
 PRIMARY KEY(agent_id,session_id,epoch,user_seq,user_digest,goal_revision,final_seq,final_digest),
 FOREIGN KEY(agent_id,session_id,epoch,user_seq,user_digest,goal_revision,final_seq,final_digest)
  REFERENCES embedded_goal_evaluations(agent_id,session_id,epoch,user_seq,user_digest,goal_revision,final_seq,final_digest) ON DELETE CASCADE,
 CHECK((state='reserved' AND disposition='pending' AND started_at IS NULL) OR
       (state='started' AND disposition='unknown' AND started_at IS NOT NULL) OR
       (state='invalidated' AND disposition='invalidated' AND started_at IS NULL))
);

CREATE TABLE embedded_goal_activity_publications (
 agent_id TEXT NOT NULL, session_id TEXT NOT NULL, epoch INTEGER NOT NULL,
 user_seq INTEGER NOT NULL, user_digest TEXT NOT NULL, goal_revision INTEGER NOT NULL,
 final_seq INTEGER NOT NULL, final_digest TEXT NOT NULL,
 event_key TEXT NOT NULL, reason TEXT NOT NULL,
 PRIMARY KEY(agent_id,session_id,epoch,user_seq,user_digest,goal_revision,final_seq,final_digest),
 FOREIGN KEY(agent_id,session_id,epoch,user_seq,user_digest,goal_revision,final_seq,final_digest)
 REFERENCES embedded_goal_evaluations(agent_id,session_id,epoch,user_seq,user_digest,goal_revision,final_seq,final_digest) ON DELETE CASCADE
);

INSERT INTO embedded_goal_evaluations SELECT * FROM goal_final_eval_134;

INSERT INTO embedded_goal_continuations(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,final_seq,final_digest,origin,state,disposition,reserved_at,due_at,started_at,retention_expires_at_ms)
SELECT c.agent_id,c.session_id,c.epoch,c.generation,c.user_seq,c.user_digest,c.goal_revision,e.final_seq,e.final_digest,c.origin,c.state,c.disposition,c.reserved_at,c.due_at,c.started_at,c.retention_expires_at_ms FROM goal_final_cont_134 c JOIN goal_final_eval_134 e USING(agent_id,session_id,epoch,user_seq,user_digest,goal_revision);

INSERT INTO embedded_goal_activity_publications(agent_id,session_id,epoch,user_seq,user_digest,goal_revision,final_seq,final_digest,event_key,reason)
SELECT p.agent_id,p.session_id,p.epoch,p.user_seq,p.user_digest,p.goal_revision,e.final_seq,e.final_digest,p.event_key,p.reason FROM goal_final_pub_134 p JOIN goal_final_eval_134 e USING(agent_id,session_id,epoch,user_seq,user_digest,goal_revision);

-- Refuse an inconsistent historical database instead of silently dropping rows.
CREATE TEMP TABLE goal_final_copy_check_134 (ok INTEGER NOT NULL CHECK(ok=1));
INSERT INTO goal_final_copy_check_134 SELECT (SELECT count(*) FROM goal_final_cont_134)=(SELECT count(*) FROM embedded_goal_continuations) AND (SELECT count(*) FROM goal_final_pub_134)=(SELECT count(*) FROM embedded_goal_activity_publications);
DROP TABLE goal_final_copy_check_134;

DROP TABLE goal_final_pub_134;

DROP TABLE goal_final_cont_134;

DROP TABLE goal_final_eval_134;

CREATE VIEW embedded_retention_legacy_holds AS
SELECT c.agent_id,c.session_id,c.epoch,c.generation,c.user_seq,c.goal_revision
FROM embedded_goal_continuations c JOIN embedded_sessions s USING(agent_id,session_id,epoch,generation)
WHERE c.retention_expires_at_ms=s.legacy_evidence_expires_at_ms
 AND s.legacy_evidence_expires_at_ms>0
 AND s.evidence_expired_through_ms<s.legacy_evidence_expires_at_ms-2592000000
 AND ((c.due_at IS NOT NULL AND julianday(c.due_at) IS NULL)
 OR NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=c.agent_id AND t.session_id=c.session_id AND t.epoch=c.epoch AND t.generation=c.generation AND t.user_seq=c.user_seq AND t.user_digest=c.user_digest)
 OR NOT EXISTS(SELECT 1 FROM embedded_goal_evaluations v JOIN embedded_entries f ON f.agent_id=v.agent_id AND f.session_id=v.session_id AND f.epoch=v.epoch AND f.generation=v.generation AND f.seq=v.final_seq AND f.digest=v.final_digest AND f.kind='assistant' WHERE v.agent_id=c.agent_id AND v.session_id=c.session_id AND v.epoch=c.epoch AND v.generation=c.generation AND v.user_seq=c.user_seq AND v.user_digest=c.user_digest AND v.goal_revision=c.goal_revision AND v.final_seq=c.final_seq AND v.final_digest=c.final_digest));

CREATE VIEW embedded_retention_closures AS
SELECT agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,final_seq,final_digest,call_id FROM embedded_execution_closures
UNION ALL
SELECT c.agent_id,c.session_id,c.epoch,c.generation,c.user_seq,c.user_digest,c.goal_revision,v.final_seq,v.final_digest,COALESCE(p.call_id,'')
FROM embedded_goal_continuations c JOIN embedded_goal_evaluations v USING(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,final_seq,final_digest)
JOIN embedded_retention_turns t USING(agent_id,session_id,epoch,generation,user_seq,user_digest)
LEFT JOIN embedded_goal_snooze_preferences p USING(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision)
WHERE c.state='reserved' AND v.state='NOT_DONE' AND c.retention_expires_at_ms>t.evidence_expired_through_ms+2592000000;

CREATE TRIGGER embedded_goal_evaluation_immutable_result BEFORE UPDATE ON embedded_goal_evaluations
WHEN OLD.state<>'UNKNOWN' OR NEW.state='UNKNOWN' OR NEW.agent_id<>OLD.agent_id OR NEW.session_id<>OLD.session_id
 OR NEW.epoch<>OLD.epoch OR NEW.generation<>OLD.generation OR NEW.user_seq<>OLD.user_seq
 OR NEW.user_digest<>OLD.user_digest OR NEW.goal_revision<>OLD.goal_revision OR NEW.owner_token<>OLD.owner_token
 OR NEW.final_seq<>OLD.final_seq OR NEW.final_digest<>OLD.final_digest OR NEW.claimed_at<>OLD.claimed_at
BEGIN SELECT RAISE(ABORT,'embedded goal evaluation is immutable'); END;

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

CREATE TRIGGER embedded_goal_evaluations_legacy_hold_delete BEFORE DELETE ON embedded_goal_evaluations
WHEN EXISTS(SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id) AND EXISTS(SELECT 1 FROM embedded_retention_legacy_holds h WHERE h.agent_id=OLD.agent_id AND h.session_id=OLD.session_id AND h.epoch=OLD.epoch AND h.generation=OLD.generation AND h.user_seq=OLD.user_seq AND h.goal_revision=OLD.goal_revision)
BEGIN SELECT RAISE(ABORT,'legacy evidence is immutable before fallback horizon'); END;

CREATE TRIGGER embedded_goal_continuation_verdict BEFORE INSERT ON embedded_goal_continuations
WHEN NOT EXISTS (SELECT 1 FROM embedded_goal_evaluations e WHERE e.agent_id=NEW.agent_id
 AND e.session_id=NEW.session_id AND e.epoch=NEW.epoch AND e.generation=NEW.generation
 AND e.user_seq=NEW.user_seq AND e.user_digest=NEW.user_digest
 AND e.goal_revision=NEW.goal_revision AND e.final_seq=NEW.final_seq AND e.final_digest=NEW.final_digest AND e.state='NOT_DONE')
BEGIN SELECT RAISE(ABORT,'continuation requires committed NOT_DONE verdict'); END;

CREATE TRIGGER embedded_goal_continuation_transition BEFORE UPDATE ON embedded_goal_continuations
WHEN OLD.state<>'reserved' OR NEW.state NOT IN ('started','invalidated')
 OR NEW.agent_id<>OLD.agent_id OR NEW.session_id<>OLD.session_id OR NEW.epoch<>OLD.epoch
 OR NEW.generation<>OLD.generation OR NEW.user_seq<>OLD.user_seq OR NEW.user_digest<>OLD.user_digest
 OR NEW.final_seq<>OLD.final_seq OR NEW.final_digest<>OLD.final_digest
 OR NEW.goal_revision<>OLD.goal_revision OR NEW.origin<>OLD.origin OR NEW.reserved_at<>OLD.reserved_at
 OR NEW.due_at IS NOT OLD.due_at
BEGIN SELECT RAISE(ABORT,'continuation transition invalid'); END;

CREATE TRIGGER embedded_goal_continuation_no_delete BEFORE DELETE ON embedded_goal_continuations
WHEN EXISTS(SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id) AND
((NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=OLD.agent_id AND t.session_id=OLD.session_id AND t.epoch=OLD.epoch AND t.generation=OLD.generation AND t.user_seq=OLD.user_seq AND t.user_digest=OLD.user_digest AND t.retention_admitted_at_ms<=t.evidence_expired_through_ms) AND NOT (EXISTS(SELECT 1 FROM embedded_retention_legacy_drained_sessions s WHERE s.agent_id=OLD.agent_id AND s.session_id=OLD.session_id AND s.epoch=OLD.epoch AND s.generation=OLD.generation) AND NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=OLD.agent_id AND t.session_id=OLD.session_id AND t.epoch=OLD.epoch AND t.generation=OLD.generation AND t.user_seq=OLD.user_seq AND t.user_digest=OLD.user_digest))) OR EXISTS(SELECT 1 FROM embedded_retention_closures x WHERE x.agent_id=OLD.agent_id AND x.session_id=OLD.session_id AND x.epoch=OLD.epoch AND x.generation=OLD.generation AND x.user_seq=OLD.user_seq AND x.user_digest=OLD.user_digest AND x.goal_revision=OLD.goal_revision AND x.final_seq=OLD.final_seq AND x.final_digest=OLD.final_digest))
BEGIN SELECT RAISE(ABORT,'retention evidence is immutable while protected'); END;

CREATE TRIGGER embedded_goal_continuations_retention_insert BEFORE INSERT ON embedded_goal_continuations
WHEN NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=NEW.agent_id AND t.session_id=NEW.session_id AND t.epoch=NEW.epoch AND t.generation=NEW.generation AND t.user_seq=NEW.user_seq AND t.user_digest=NEW.user_digest AND t.retention_admitted_at_ms>t.evidence_expired_through_ms)
BEGIN SELECT RAISE(ABORT,'retention-expired embedded evidence'); END;

CREATE TRIGGER embedded_retention_pending_immutable BEFORE UPDATE OF retention_expires_at_ms ON embedded_goal_continuations
WHEN NEW.retention_expires_at_ms<>OLD.retention_expires_at_ms
BEGIN SELECT RAISE(ABORT,'pending retention lifetime is immutable'); END;

CREATE TRIGGER embedded_retention_pending_insert BEFORE INSERT ON embedded_goal_continuations
WHEN NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=NEW.agent_id AND t.session_id=NEW.session_id AND t.epoch=NEW.epoch AND t.generation=NEW.generation AND t.user_seq=NEW.user_seq AND t.user_digest=NEW.user_digest
AND (NEW.due_at IS NULL OR (julianday(NEW.due_at) IS NOT NULL AND CAST(ROUND((julianday(NEW.due_at)-2440587.5)*86400000) AS INTEGER)<=t.retention_admitted_at_ms+2678400000))
AND NEW.retention_expires_at_ms=MAX(t.retention_admitted_at_ms+2592000000,COALESCE(CAST(ROUND((julianday(NEW.due_at)-2440587.5)*86400000) AS INTEGER),t.retention_admitted_at_ms+2592000000))+2592000000)
BEGIN SELECT RAISE(ABORT,'invalid pending retention lifetime'); END;

CREATE TRIGGER embedded_retention_pending_start BEFORE UPDATE ON embedded_goal_continuations
WHEN NEW.state='started' AND ((NEW.due_at IS NOT NULL AND julianday(NEW.due_at) IS NULL) OR NOT EXISTS(SELECT 1 FROM embedded_goal_evaluations v JOIN embedded_entries f ON f.agent_id=v.agent_id AND f.session_id=v.session_id AND f.epoch=v.epoch AND f.generation=v.generation AND f.seq=v.final_seq AND f.digest=v.final_digest AND f.kind='assistant' WHERE v.agent_id=NEW.agent_id AND v.session_id=NEW.session_id AND v.epoch=NEW.epoch AND v.generation=NEW.generation AND v.user_seq=NEW.user_seq AND v.user_digest=NEW.user_digest AND v.goal_revision=NEW.goal_revision AND v.final_seq=NEW.final_seq AND v.final_digest=NEW.final_digest) OR NOT EXISTS(SELECT 1 FROM embedded_retention_turns t WHERE t.agent_id=NEW.agent_id AND t.session_id=NEW.session_id AND t.epoch=NEW.epoch AND t.generation=NEW.generation AND t.user_seq=NEW.user_seq AND t.user_digest=NEW.user_digest AND NEW.retention_expires_at_ms>t.evidence_expired_through_ms+2592000000)
)
BEGIN SELECT RAISE(ABORT,'retention-expired pending acceptance'); END;

CREATE TRIGGER embedded_goal_continuations_legacy_hold_delete BEFORE DELETE ON embedded_goal_continuations
WHEN EXISTS(SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id) AND EXISTS(SELECT 1 FROM embedded_retention_legacy_holds h WHERE h.agent_id=OLD.agent_id AND h.session_id=OLD.session_id AND h.epoch=OLD.epoch AND h.generation=OLD.generation AND h.user_seq=OLD.user_seq AND h.goal_revision=OLD.goal_revision)
BEGIN SELECT RAISE(ABORT,'legacy evidence is immutable before fallback horizon'); END;

CREATE TRIGGER embedded_execution_continuation_start BEFORE UPDATE OF state ON embedded_goal_continuations
WHEN OLD.state='reserved' AND NEW.state='started'
BEGIN
 SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM embedded_sessions s WHERE s.agent_id=NEW.agent_id AND s.session_id=NEW.session_id AND s.epoch=NEW.epoch AND s.generation=NEW.generation AND s.current=1 AND (s.execution_json IS NULL OR json_extract(s.execution_json,'$.owner')<>s.owner_token)) THEN RAISE(ABORT,'embedded execution already active') END;
 UPDATE embedded_sessions SET execution_token=execution_token+1,execution_json=json_object(
 'kind','sdk','owner',owner_token,'token',execution_token+1,
 'source',json_object('UserSeq',NEW.user_seq,'UserDigest',NEW.user_digest,'GoalRevision',NEW.goal_revision,
 'FinalSeq',NEW.final_seq,
 'FinalDigest',NEW.final_digest,
 'call_id',COALESCE((SELECT call_id FROM embedded_goal_snooze_preferences p WHERE p.agent_id=NEW.agent_id AND p.session_id=NEW.session_id AND p.epoch=NEW.epoch AND p.generation=NEW.generation AND p.user_seq=NEW.user_seq AND p.user_digest=NEW.user_digest AND p.goal_revision=NEW.goal_revision),'')),
 'current',NULL)
 WHERE agent_id=NEW.agent_id AND session_id=NEW.session_id AND epoch=NEW.epoch AND generation=NEW.generation AND current=1;
END;

CREATE TRIGGER embedded_execution_continuation_started AFTER UPDATE OF state ON embedded_goal_continuations
WHEN OLD.state='reserved' AND NEW.state='started'
BEGIN
 SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM embedded_execution_closures x WHERE x.agent_id=NEW.agent_id AND x.session_id=NEW.session_id AND x.epoch=NEW.epoch AND x.generation=NEW.generation AND x.user_seq=NEW.user_seq AND x.user_digest=NEW.user_digest AND x.goal_revision=NEW.goal_revision AND x.final_seq=NEW.final_seq AND x.final_digest=NEW.final_digest) THEN RAISE(ABORT,'missing atomic continuation execution') END;
END;

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
 OR (json_extract(NEW.execution_json,'$.kind')='sdk' AND v.state='NOT_DONE' AND EXISTS(SELECT 1 FROM embedded_goal_continuations c WHERE c.agent_id=v.agent_id AND c.session_id=v.session_id AND c.epoch=v.epoch AND c.generation=v.generation AND c.user_seq=v.user_seq AND c.user_digest=v.user_digest AND c.goal_revision=v.goal_revision AND c.final_seq=v.final_seq AND c.final_digest=v.final_digest AND c.state='reserved' AND c.disposition='pending' AND MAX(e.retention_admitted_at_ms+2592000000,COALESCE(CAST((julianday(c.due_at)-2440587.5)*86400000 AS INTEGER),e.retention_admitted_at_ms+2592000000))>NEW.evidence_expired_through_ms)))
 ) THEN RAISE(ABORT,'invalid execution source authority') END;
 SELECT CASE WHEN NEW.execution_json IS NOT NULL AND json_type(NEW.execution_json,'$.source')='object' AND json_extract(json_extract(NEW.execution_json,'$.source'),'$.call_id')<>'' AND NOT EXISTS(SELECT 1 FROM embedded_goal_snooze_calls c WHERE c.call_id=json_extract(json_extract(NEW.execution_json,'$.source'),'$.call_id') AND c.agent_id=NEW.agent_id AND c.session_id=NEW.session_id AND c.epoch=NEW.epoch AND c.generation=NEW.generation AND c.user_seq=json_extract(json_extract(NEW.execution_json,'$.source'),'$.UserSeq') AND c.user_digest=json_extract(json_extract(NEW.execution_json,'$.source'),'$.UserDigest') AND c.goal_revision=json_extract(json_extract(NEW.execution_json,'$.source'),'$.GoalRevision')) THEN RAISE(ABORT,'invalid execution referenced call') END;
 SELECT CASE WHEN NEW.execution_json IS NOT NULL AND json_type(NEW.execution_json,'$.current')='object' AND (NEW.execution_token<>OLD.execution_token OR json_type(OLD.execution_json,'$.current') IS NOT 'object') AND (NOT EXISTS(SELECT 1 FROM embedded_entries e WHERE e.agent_id=NEW.agent_id AND e.session_id=NEW.session_id AND e.epoch=NEW.epoch AND e.generation=NEW.generation AND e.seq=json_extract(json_extract(NEW.execution_json,'$.current'),'$.UserSeq') AND e.retention_admitted_at_ms>NEW.evidence_expired_through_ms) OR json_extract(json_extract(NEW.execution_json,'$.current'),'$.FinalSeq')<>0 OR json_extract(json_extract(NEW.execution_json,'$.current'),'$.FinalDigest')<>'') THEN RAISE(ABORT,'execution admission expired or invalid') END;
 SELECT CASE WHEN NEW.execution_json IS NOT NULL AND json_type(NEW.execution_json,'$.current')='object' AND json_extract(json_extract(NEW.execution_json,'$.current'),'$.call_id')<>'' AND NOT EXISTS(SELECT 1 FROM embedded_goal_snooze_calls c WHERE c.call_id=json_extract(json_extract(NEW.execution_json,'$.current'),'$.call_id') AND c.agent_id=NEW.agent_id AND c.session_id=NEW.session_id AND c.epoch=NEW.epoch AND c.generation=NEW.generation AND c.user_seq=json_extract(json_extract(NEW.execution_json,'$.current'),'$.UserSeq') AND c.user_digest=json_extract(json_extract(NEW.execution_json,'$.current'),'$.UserDigest') AND c.goal_revision=json_extract(json_extract(NEW.execution_json,'$.current'),'$.GoalRevision')) THEN RAISE(ABORT,'invalid execution referenced call') END;

END;

CREATE TRIGGER embedded_goal_continuation_policy_invalidate AFTER UPDATE OF goal_enabled,desired_revision,applied_revision,projection_state ON managed_agent_policies
BEGIN
 UPDATE embedded_goal_continuations SET state='invalidated',disposition='invalidated'
 WHERE agent_id=NEW.agent_id AND state='reserved' AND
 (NEW.goal_enabled<>1 OR NEW.desired_revision<>goal_revision OR NEW.applied_revision<>goal_revision OR NEW.projection_state<>'applied');
END;

CREATE TRIGGER embedded_goal_continuation_session_invalidate AFTER UPDATE OF current ON embedded_sessions
WHEN OLD.current=1 AND NEW.current<>1
BEGIN
 UPDATE embedded_goal_continuations SET state='invalidated',disposition='invalidated'
 WHERE agent_id=OLD.agent_id AND session_id=OLD.session_id AND epoch=OLD.epoch AND state='reserved';
END;

CREATE TRIGGER embedded_goal_continuation_epoch_invalidate AFTER UPDATE OF session_epoch,runtime_generation,pi_session_id ON managed_agents
WHEN OLD.session_epoch<>NEW.session_epoch OR OLD.runtime_generation<>NEW.runtime_generation OR OLD.pi_session_id<>NEW.pi_session_id
BEGIN
 UPDATE embedded_goal_continuations SET state='invalidated',disposition='invalidated'
 WHERE agent_id=OLD.agent_id AND state='reserved';
END;
