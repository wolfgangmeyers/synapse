-- Forward-only repair: a newly accepted call may replace a retired preference
-- for the same immutable turn. Historical call replay cannot satisfy this guard.
DROP TRIGGER embedded_goal_snooze_preference_transition;
CREATE TRIGGER embedded_goal_snooze_preference_transition BEFORE UPDATE ON embedded_goal_snooze_preferences
WHEN NEW.agent_id<>OLD.agent_id OR NEW.session_id<>OLD.session_id OR NEW.epoch<>OLD.epoch
 OR NEW.generation<>OLD.generation OR NEW.role<>OLD.role OR NEW.user_seq<>OLD.user_seq
 OR NEW.user_digest<>OLD.user_digest OR NEW.goal_revision<>OLD.goal_revision
 OR (OLD.active=1 AND NEW.active=1 AND (NEW.policy_version<>OLD.policy_version+1 OR NEW.call_id=OLD.call_id))
 OR (OLD.active=1 AND NEW.active=0 AND (NEW.policy_version<>OLD.policy_version OR NEW.call_id<>OLD.call_id OR NEW.due_at<>OLD.due_at))
 OR (OLD.active=0 AND (NEW.active<>1 OR NEW.policy_version<>OLD.policy_version+1 OR NEW.call_id=OLD.call_id))
 OR (NEW.active=1 AND NOT EXISTS (SELECT 1 FROM embedded_goal_snooze_calls c WHERE c.call_id=NEW.call_id
   AND c.agent_id=NEW.agent_id AND c.session_id=NEW.session_id AND c.epoch=NEW.epoch
   AND c.generation=NEW.generation AND c.role=NEW.role AND c.user_seq=NEW.user_seq
   AND c.user_digest=NEW.user_digest AND c.goal_revision=NEW.goal_revision
   AND c.policy_version=NEW.policy_version AND json_extract(c.result_json,'$.DueAt')=NEW.due_at))
BEGIN SELECT RAISE(ABORT,'snooze preference transition invalid'); END;
