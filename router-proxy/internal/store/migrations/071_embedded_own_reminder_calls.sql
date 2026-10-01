-- G5 Store-only SDK own-reminder call evidence. Never FK to reminders: one-shots
-- are removed by the existing sweep and cancel path. Session/agent deletion is
-- the only retention boundary; a 4096-call admission cap is enforced by Store.
-- No time-based pruning: an old call ID must never become executable again.
CREATE TABLE embedded_own_reminder_calls (
 call_id TEXT PRIMARY KEY CHECK(length(call_id)>0 AND length(call_id)<=256),
 agent_id TEXT NOT NULL, session_id TEXT NOT NULL, epoch INTEGER NOT NULL,
 role TEXT NOT NULL CHECK(role IN ('root','worker')),
 action TEXT NOT NULL DEFAULT 'list' CHECK(action IN ('create','list','cancel')),
 reminder_id TEXT,
 args_digest TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('unknown','observed')),
 result_json TEXT CHECK(result_json IS NULL OR (json_valid(result_json) AND length(CAST(result_json AS BLOB))<=32768)),
 CHECK((state='unknown' AND result_json IS NULL) OR (state='observed' AND result_json IS NOT NULL)),
 FOREIGN KEY(agent_id) REFERENCES managed_agents(agent_id) ON DELETE CASCADE
);
CREATE INDEX embedded_own_reminder_calls_scope ON embedded_own_reminder_calls(agent_id,session_id,epoch);
CREATE TRIGGER embedded_own_reminder_call_no_update BEFORE UPDATE ON embedded_own_reminder_calls
BEGIN SELECT RAISE(ABORT,'own reminder call immutable'); END;
CREATE TRIGGER embedded_own_reminder_session_cleanup AFTER DELETE ON embedded_sessions
BEGIN DELETE FROM embedded_own_reminder_calls WHERE agent_id=OLD.agent_id AND session_id=OLD.session_id AND epoch=OLD.epoch; END;
