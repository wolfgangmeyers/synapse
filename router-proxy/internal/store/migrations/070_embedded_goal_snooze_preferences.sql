-- Provisional G5/T6: one ordered preference per initiating SDK turn and goal revision.
-- This is not a wake reservation. Receipt, settled final and NOT_DONE are checked later.
CREATE TABLE embedded_goal_snooze_calls (
 call_id TEXT PRIMARY KEY CHECK(length(call_id)>0 AND length(call_id)<=256),
 agent_id TEXT NOT NULL, session_id TEXT NOT NULL, epoch INTEGER NOT NULL,
 generation TEXT NOT NULL, role TEXT NOT NULL CHECK(role IN ('root','worker')),
 user_seq INTEGER NOT NULL, user_digest TEXT NOT NULL, goal_revision INTEGER NOT NULL,
 args_json TEXT NOT NULL CHECK(json_valid(args_json)),
 result_json TEXT NOT NULL CHECK(json_valid(result_json)),
 policy_version INTEGER NOT NULL CHECK(policy_version>0),
 FOREIGN KEY(agent_id,session_id,epoch,generation,user_seq) REFERENCES embedded_entries(agent_id,session_id,epoch,generation,seq)
);
CREATE TRIGGER embedded_goal_snooze_call_no_update BEFORE UPDATE ON embedded_goal_snooze_calls
BEGIN SELECT RAISE(ABORT,'snooze call is immutable'); END;
CREATE TRIGGER embedded_goal_snooze_call_no_delete BEFORE DELETE ON embedded_goal_snooze_calls
BEGIN SELECT RAISE(ABORT,'snooze call is immutable'); END;
CREATE TABLE embedded_goal_snooze_preferences (
 agent_id TEXT NOT NULL, session_id TEXT NOT NULL, epoch INTEGER NOT NULL,
 generation TEXT NOT NULL, role TEXT NOT NULL CHECK(role IN ('root','worker')),
 user_seq INTEGER NOT NULL, user_digest TEXT NOT NULL, goal_revision INTEGER NOT NULL,
 policy_version INTEGER NOT NULL CHECK(policy_version>0),
 call_id TEXT NOT NULL UNIQUE REFERENCES embedded_goal_snooze_calls(call_id),
 due_at TEXT NOT NULL, active INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0,1)),
 PRIMARY KEY(agent_id,session_id,epoch,user_seq,user_digest,goal_revision),
 FOREIGN KEY(agent_id,session_id,epoch,generation,user_seq) REFERENCES embedded_entries(agent_id,session_id,epoch,generation,seq)
);
CREATE TRIGGER embedded_goal_snooze_preference_transition BEFORE UPDATE ON embedded_goal_snooze_preferences
WHEN NEW.agent_id<>OLD.agent_id OR NEW.session_id<>OLD.session_id OR NEW.epoch<>OLD.epoch
 OR NEW.generation<>OLD.generation OR NEW.role<>OLD.role OR NEW.user_seq<>OLD.user_seq
 OR NEW.user_digest<>OLD.user_digest OR NEW.goal_revision<>OLD.goal_revision
 OR (OLD.active=1 AND NEW.active=1 AND (NEW.policy_version<>OLD.policy_version+1 OR NEW.call_id=OLD.call_id))
 OR (OLD.active=1 AND NEW.active=0 AND (NEW.policy_version<>OLD.policy_version OR NEW.call_id<>OLD.call_id OR NEW.due_at<>OLD.due_at))
 OR OLD.active=0
BEGIN SELECT RAISE(ABORT,'snooze preference transition invalid'); END;
CREATE TRIGGER embedded_goal_snooze_preference_no_delete BEFORE DELETE ON embedded_goal_snooze_preferences
BEGIN SELECT RAISE(ABORT,'snooze preference is immutable history'); END;
CREATE TRIGGER embedded_goal_snooze_preference_policy_invalidate AFTER UPDATE OF goal_enabled,desired_revision ON managed_agent_policies
BEGIN
 UPDATE embedded_goal_snooze_preferences SET active=0 WHERE agent_id=NEW.agent_id AND active=1
 AND (NEW.goal_enabled<>1 OR NEW.desired_revision<>goal_revision);
END;
CREATE TRIGGER embedded_goal_snooze_preference_session_invalidate AFTER UPDATE OF current ON embedded_sessions
WHEN OLD.current=1 AND NEW.current<>1
BEGIN
 UPDATE embedded_goal_snooze_preferences SET active=0 WHERE agent_id=OLD.agent_id AND session_id=OLD.session_id AND epoch=OLD.epoch AND active=1;
END;
CREATE TRIGGER embedded_goal_snooze_preference_epoch_invalidate AFTER UPDATE OF session_epoch,runtime_generation,pi_session_id ON managed_agents
WHEN OLD.session_epoch<>NEW.session_epoch OR OLD.runtime_generation<>NEW.runtime_generation OR OLD.pi_session_id<>NEW.pi_session_id
BEGIN
 UPDATE embedded_goal_snooze_preferences SET active=0 WHERE agent_id=OLD.agent_id AND active=1;
END;
