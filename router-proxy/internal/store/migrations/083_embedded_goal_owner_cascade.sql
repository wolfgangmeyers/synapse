-- Forward repair for installed 065-082 databases. Store runs this whole file
-- in one immediate transaction with foreign_keys ON. Snapshot leaves first;
-- DROP TABLE's implicit deletion does not execute application DELETE triggers.
-- No owner/history rows are pre-deleted by supported termination.
CREATE TEMP TABLE embedded_goal_continuations_083 AS SELECT * FROM embedded_goal_continuations;
CREATE TEMP TABLE embedded_goal_snooze_calls_083 AS SELECT * FROM embedded_goal_snooze_calls;
CREATE TEMP TABLE embedded_goal_snooze_preferences_083 AS SELECT * FROM embedded_goal_snooze_preferences;
DROP TABLE embedded_goal_snooze_preferences;
DROP TABLE embedded_goal_snooze_calls;
DROP TABLE embedded_goal_continuations;

CREATE TABLE embedded_goal_continuations (
 agent_id TEXT NOT NULL, session_id TEXT NOT NULL, epoch INTEGER NOT NULL,
 generation TEXT NOT NULL, user_seq INTEGER NOT NULL, user_digest TEXT NOT NULL,
 goal_revision INTEGER NOT NULL, origin TEXT NOT NULL DEFAULT 'goal_reevaluation' CHECK(origin='goal_reevaluation'),
 state TEXT NOT NULL CHECK(state IN ('reserved','started','invalidated')),
 disposition TEXT NOT NULL CHECK(disposition IN ('pending','unknown','invalidated')),
 reserved_at TEXT NOT NULL, due_at TEXT, started_at TEXT,
 PRIMARY KEY(agent_id,session_id,epoch,user_seq,user_digest,goal_revision),
 FOREIGN KEY(agent_id,session_id,epoch,user_seq,user_digest,goal_revision)
  REFERENCES embedded_goal_evaluations(agent_id,session_id,epoch,user_seq,user_digest,goal_revision) ON DELETE CASCADE,
 CHECK((state='reserved' AND disposition='pending' AND started_at IS NULL) OR
       (state='started' AND disposition='unknown' AND started_at IS NOT NULL) OR
       (state='invalidated' AND disposition='invalidated' AND started_at IS NULL))
);
CREATE TABLE embedded_goal_snooze_calls (
 call_id TEXT PRIMARY KEY CHECK(length(call_id)>0 AND length(call_id)<=256),
 agent_id TEXT NOT NULL, session_id TEXT NOT NULL, epoch INTEGER NOT NULL,
 generation TEXT NOT NULL, role TEXT NOT NULL CHECK(role IN ('root','worker')),
 user_seq INTEGER NOT NULL, user_digest TEXT NOT NULL, goal_revision INTEGER NOT NULL,
 args_json TEXT NOT NULL CHECK(json_valid(args_json)),
 result_json TEXT NOT NULL CHECK(json_valid(result_json)),
 policy_version INTEGER NOT NULL CHECK(policy_version>0),
 FOREIGN KEY(agent_id,session_id,epoch,generation,user_seq) REFERENCES embedded_entries(agent_id,session_id,epoch,generation,seq) ON DELETE CASCADE
);
CREATE TABLE embedded_goal_snooze_preferences (
 agent_id TEXT NOT NULL, session_id TEXT NOT NULL, epoch INTEGER NOT NULL,
 generation TEXT NOT NULL, role TEXT NOT NULL CHECK(role IN ('root','worker')),
 user_seq INTEGER NOT NULL, user_digest TEXT NOT NULL, goal_revision INTEGER NOT NULL,
 policy_version INTEGER NOT NULL CHECK(policy_version>0),
 call_id TEXT NOT NULL UNIQUE REFERENCES embedded_goal_snooze_calls(call_id) ON DELETE CASCADE,
 due_at TEXT NOT NULL, active INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0,1)),
 PRIMARY KEY(agent_id,session_id,epoch,user_seq,user_digest,goal_revision),
 FOREIGN KEY(agent_id,session_id,epoch,generation,user_seq) REFERENCES embedded_entries(agent_id,session_id,epoch,generation,seq) ON DELETE CASCADE
);
-- Restore parents before their children. Install insertion/transition guards
-- after the exact historical rows are restored, including invalidated rows.
INSERT INTO embedded_goal_continuations SELECT * FROM embedded_goal_continuations_083;
INSERT INTO embedded_goal_snooze_calls SELECT * FROM embedded_goal_snooze_calls_083;
INSERT INTO embedded_goal_snooze_preferences SELECT * FROM embedded_goal_snooze_preferences_083;
DROP TABLE temp.embedded_goal_snooze_preferences_083;
DROP TABLE temp.embedded_goal_snooze_calls_083;
DROP TABLE temp.embedded_goal_continuations_083;

CREATE TRIGGER embedded_goal_continuation_verdict BEFORE INSERT ON embedded_goal_continuations
WHEN NOT EXISTS (SELECT 1 FROM embedded_goal_evaluations e WHERE e.agent_id=NEW.agent_id
 AND e.session_id=NEW.session_id AND e.epoch=NEW.epoch AND e.generation=NEW.generation
 AND e.user_seq=NEW.user_seq AND e.user_digest=NEW.user_digest
 AND e.goal_revision=NEW.goal_revision AND e.state='NOT_DONE')
BEGIN SELECT RAISE(ABORT,'continuation requires committed NOT_DONE verdict'); END;
CREATE TRIGGER embedded_goal_continuation_transition BEFORE UPDATE ON embedded_goal_continuations
WHEN OLD.state<>'reserved' OR NEW.state NOT IN ('started','invalidated')
 OR NEW.agent_id<>OLD.agent_id OR NEW.session_id<>OLD.session_id OR NEW.epoch<>OLD.epoch
 OR NEW.generation<>OLD.generation OR NEW.user_seq<>OLD.user_seq OR NEW.user_digest<>OLD.user_digest
 OR NEW.goal_revision<>OLD.goal_revision OR NEW.origin<>OLD.origin OR NEW.reserved_at<>OLD.reserved_at
 OR NEW.due_at IS NOT OLD.due_at
BEGIN SELECT RAISE(ABORT,'continuation transition invalid'); END;
CREATE TRIGGER embedded_goal_continuation_no_delete BEFORE DELETE ON embedded_goal_continuations
WHEN EXISTS (SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id)
BEGIN SELECT RAISE(ABORT,'continuation is immutable'); END;
CREATE TRIGGER embedded_goal_snooze_call_no_update BEFORE UPDATE ON embedded_goal_snooze_calls
BEGIN SELECT RAISE(ABORT,'snooze call is immutable'); END;
CREATE TRIGGER embedded_goal_snooze_call_no_delete BEFORE DELETE ON embedded_goal_snooze_calls
WHEN EXISTS (SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id)
BEGIN SELECT RAISE(ABORT,'snooze call is immutable'); END;
CREATE TRIGGER embedded_goal_snooze_preference_transition BEFORE UPDATE ON embedded_goal_snooze_preferences
WHEN NEW.agent_id<>OLD.agent_id OR NEW.session_id<>OLD.session_id OR NEW.epoch<>OLD.epoch
 OR NEW.generation<>OLD.generation OR NEW.role<>OLD.role OR NEW.user_seq<>OLD.user_seq
 OR NEW.user_digest<>OLD.user_digest OR NEW.goal_revision<>OLD.goal_revision
 OR (OLD.active=1 AND NEW.active=1 AND (NEW.policy_version<>OLD.policy_version+1 OR NEW.call_id=OLD.call_id))
 OR (OLD.active=1 AND NEW.active=0 AND (NEW.policy_version<>OLD.policy_version OR NEW.call_id<>OLD.call_id OR NEW.due_at<>OLD.due_at))
 OR OLD.active=0
BEGIN SELECT RAISE(ABORT,'snooze preference transition invalid'); END;
CREATE TRIGGER embedded_goal_snooze_preference_no_delete BEFORE DELETE ON embedded_goal_snooze_preferences
WHEN EXISTS (SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id)
BEGIN SELECT RAISE(ABORT,'snooze preference is immutable history'); END;

-- Parent removal precedes its FK actions. Only that actual removal exempts
-- child deletion; desired_state='terminated' still has an owner and is guarded.
DROP TRIGGER embedded_goal_receipt_immutable_delete;
CREATE TRIGGER embedded_goal_receipt_immutable_delete BEFORE DELETE ON embedded_goal_applied_receipts
WHEN EXISTS (SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id)
BEGIN SELECT RAISE(ABORT,'embedded goal receipt is immutable'); END;
DROP TRIGGER embedded_goal_evaluation_immutable_delete;
CREATE TRIGGER embedded_goal_evaluation_immutable_delete BEFORE DELETE ON embedded_goal_evaluations
WHEN EXISTS (SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id)
BEGIN SELECT RAISE(ABORT,'embedded goal evaluation is immutable'); END;
