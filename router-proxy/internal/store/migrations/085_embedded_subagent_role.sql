ALTER TABLE managed_subagents ADD COLUMN attempt INTEGER NOT NULL DEFAULT 1 CHECK(attempt > 0);
ALTER TABLE managed_subagents ADD COLUMN duration_seconds REAL NOT NULL DEFAULT 0 CHECK(duration_seconds >= 0);
ALTER TABLE managed_subagents ADD COLUMN resume_prompt TEXT NOT NULL DEFAULT '';
-- Rebuild the one constrained parent table with FK enforcement temporarily
-- disabled by applyEmbeddedSubagentRoleMigration085 (outside its transaction).
-- Never rename the original table: dependent FK definitions must keep pointing
-- at embedded_sessions. Preserve the current-bit invalidation/cleanup triggers.
DROP TRIGGER embedded_goal_continuation_session_invalidate;
DROP TRIGGER embedded_goal_snooze_preference_session_invalidate;
DROP TRIGGER embedded_own_reminder_session_cleanup;
CREATE TABLE embedded_sessions_085 (
 agent_id TEXT NOT NULL, session_id TEXT NOT NULL, epoch INTEGER NOT NULL CHECK(epoch > 0),
 generation TEXT NOT NULL, role TEXT NOT NULL CHECK(role IN ('root','worker','subagent')),
 current INTEGER NOT NULL CHECK(current IN (0,1)),
 owner_id TEXT, owner_token INTEGER NOT NULL DEFAULT 0 CHECK(owner_token>=0),
 owner_expires_at TEXT,
 CHECK((owner_id IS NULL AND owner_expires_at IS NULL) OR (owner_id IS NOT NULL AND owner_expires_at IS NOT NULL)),
 PRIMARY KEY(agent_id,session_id,epoch,generation),
 FOREIGN KEY(agent_id) REFERENCES managed_agents(agent_id) ON DELETE CASCADE,
 CHECK(length(agent_id)>0 AND length(session_id)>0 AND length(generation)>0)
);
INSERT INTO embedded_sessions_085(agent_id,session_id,epoch,generation,role,current,owner_id,owner_token,owner_expires_at)
 SELECT agent_id,session_id,epoch,generation,role,current,owner_id,owner_token,owner_expires_at FROM embedded_sessions;
DROP TABLE embedded_sessions;
ALTER TABLE embedded_sessions_085 RENAME TO embedded_sessions;
CREATE UNIQUE INDEX embedded_current_agent ON embedded_sessions(agent_id) WHERE current=1;
CREATE TRIGGER embedded_goal_continuation_session_invalidate AFTER UPDATE OF current ON embedded_sessions
WHEN OLD.current=1 AND NEW.current<>1
BEGIN
 UPDATE embedded_goal_continuations SET state='invalidated',disposition='invalidated'
 WHERE agent_id=OLD.agent_id AND session_id=OLD.session_id AND epoch=OLD.epoch AND state='reserved';
END;
CREATE TRIGGER embedded_goal_snooze_preference_session_invalidate AFTER UPDATE OF current ON embedded_sessions
WHEN OLD.current=1 AND NEW.current<>1
BEGIN
 UPDATE embedded_goal_snooze_preferences SET active=0 WHERE agent_id=OLD.agent_id AND session_id=OLD.session_id AND epoch=OLD.epoch AND active=1;
END;
CREATE TRIGGER embedded_own_reminder_session_cleanup AFTER DELETE ON embedded_sessions
BEGIN DELETE FROM embedded_own_reminder_calls WHERE agent_id=OLD.agent_id AND session_id=OLD.session_id AND epoch=OLD.epoch; END;
