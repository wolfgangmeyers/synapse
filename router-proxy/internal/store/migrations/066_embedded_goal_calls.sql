-- Own-goal SDK calls are bound to the saved embedded identity, never a target.
-- An unresolved intent remains unknown and must not be retried as a new call.
CREATE TABLE embedded_goal_calls (
 call_id TEXT PRIMARY KEY CHECK(length(call_id)>0 AND length(call_id)<=256),
 agent_id TEXT NOT NULL, session_id TEXT NOT NULL, epoch INTEGER NOT NULL,
 generation TEXT NOT NULL, role TEXT NOT NULL CHECK(role IN ('root','worker')),
 action TEXT NOT NULL CHECK(action IN ('set','status','off')),
 args_json TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('unknown','complete')),
 result_json TEXT,
 CHECK((state='unknown' AND result_json IS NULL) OR (state='complete' AND result_json IS NOT NULL AND json_valid(result_json))),
 FOREIGN KEY(agent_id,session_id,epoch,generation) REFERENCES embedded_sessions(agent_id,session_id,epoch,generation) ON DELETE CASCADE
);
CREATE TRIGGER embedded_goal_call_immutable BEFORE UPDATE ON embedded_goal_calls
WHEN OLD.state='complete' OR NEW.call_id<>OLD.call_id OR NEW.agent_id<>OLD.agent_id
 OR NEW.session_id<>OLD.session_id OR NEW.epoch<>OLD.epoch OR NEW.generation<>OLD.generation
 OR NEW.role<>OLD.role OR NEW.action<>OLD.action OR NEW.args_json<>OLD.args_json
 OR NEW.state<>'complete'
BEGIN SELECT RAISE(ABORT,'embedded goal intent/result is immutable'); END;
