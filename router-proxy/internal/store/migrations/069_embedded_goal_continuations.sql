-- One non-mail continuation per proven NOT_DONE evaluation key. A started
-- attempt is unknown after interruption and is never eligible for a second start.
CREATE TABLE embedded_goal_continuations (
 agent_id TEXT NOT NULL, session_id TEXT NOT NULL, epoch INTEGER NOT NULL,
 generation TEXT NOT NULL, user_seq INTEGER NOT NULL, user_digest TEXT NOT NULL,
 goal_revision INTEGER NOT NULL, origin TEXT NOT NULL DEFAULT 'goal_reevaluation' CHECK(origin='goal_reevaluation'),
 state TEXT NOT NULL CHECK(state IN ('reserved','started','invalidated')),
 disposition TEXT NOT NULL CHECK(disposition IN ('pending','unknown','invalidated')),
 reserved_at TEXT NOT NULL, due_at TEXT, started_at TEXT,
 PRIMARY KEY(agent_id,session_id,epoch,user_seq,user_digest,goal_revision),
 FOREIGN KEY(agent_id,session_id,epoch,user_seq,user_digest,goal_revision)
  REFERENCES embedded_goal_evaluations(agent_id,session_id,epoch,user_seq,user_digest,goal_revision),
 CHECK((state='reserved' AND disposition='pending' AND started_at IS NULL) OR
       (state='started' AND disposition='unknown' AND started_at IS NOT NULL) OR
       (state='invalidated' AND disposition='invalidated' AND started_at IS NULL))
);
CREATE TRIGGER embedded_goal_continuation_verdict BEFORE INSERT ON embedded_goal_continuations
WHEN NOT EXISTS (SELECT 1 FROM embedded_goal_evaluations e WHERE e.agent_id=NEW.agent_id
 AND e.session_id=NEW.session_id AND e.epoch=NEW.epoch AND e.generation=NEW.generation
 AND e.user_seq=NEW.user_seq AND e.user_digest=NEW.user_digest
 AND e.goal_revision=NEW.goal_revision AND e.state='NOT_DONE')
BEGIN SELECT RAISE(ABORT,'continuation requires committed NOT_DONE verdict'); END;
CREATE TRIGGER embedded_goal_continuation_no_delete BEFORE DELETE ON embedded_goal_continuations
BEGIN SELECT RAISE(ABORT,'continuation is immutable'); END;
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
CREATE TRIGGER embedded_goal_continuation_transition BEFORE UPDATE ON embedded_goal_continuations
WHEN OLD.state<>'reserved' OR NEW.state NOT IN ('started','invalidated')
 OR NEW.agent_id<>OLD.agent_id OR NEW.session_id<>OLD.session_id OR NEW.epoch<>OLD.epoch
 OR NEW.generation<>OLD.generation OR NEW.user_seq<>OLD.user_seq OR NEW.user_digest<>OLD.user_digest
 OR NEW.goal_revision<>OLD.goal_revision OR NEW.origin<>OLD.origin OR NEW.reserved_at<>OLD.reserved_at
 OR NEW.due_at IS NOT OLD.due_at
BEGIN SELECT RAISE(ABORT,'continuation transition invalid'); END;
