-- Legacy evidence has no trustworthy workflow identity. Preserve an agent-level
-- warning without granting replay authority or attributing it to a workflow.
CREATE TABLE legacy_continuation_ambiguity (
 agent_id TEXT PRIMARY KEY REFERENCES managed_agents(agent_id) ON DELETE CASCADE,
 recorded_at_ms INTEGER NOT NULL
);
INSERT OR IGNORE INTO legacy_continuation_ambiguity
 SELECT agent_id, CAST(strftime('%s','now') AS INTEGER)*1000 FROM workflow_interruption_markers;
INSERT OR IGNORE INTO legacy_continuation_ambiguity
 SELECT agent_id, CAST(strftime('%s','now') AS INTEGER)*1000 FROM embedded_goal_continuations
 WHERE state='started' AND disposition='unknown';
-- Pre-137 markers cannot distinguish goal executions from workflow executions.
-- Discard them rather than assert an interruption without exact evidence.
DELETE FROM workflow_interruption_markers;
ALTER TABLE workflow_interruption_markers ADD COLUMN workflow_id TEXT NOT NULL DEFAULT '';
ALTER TABLE workflow_interruption_markers ADD COLUMN workflow_state_id TEXT NOT NULL DEFAULT '';
ALTER TABLE workflow_interruption_markers ADD COLUMN workflow_binding_generation INTEGER NOT NULL DEFAULT 0;

-- This witness lives as long as its continuation and cannot authorize replay.
-- Existing started continuations intentionally receive no witness.
CREATE TABLE workflow_continuation_start_identity (
 agent_id TEXT NOT NULL, session_id TEXT NOT NULL, epoch INTEGER NOT NULL,
 generation TEXT NOT NULL, user_seq INTEGER NOT NULL, user_digest TEXT NOT NULL,
 goal_revision INTEGER NOT NULL, final_seq INTEGER NOT NULL, final_digest TEXT NOT NULL,
 workflow_id TEXT NOT NULL, workflow_state_id TEXT NOT NULL,
 workflow_binding_generation INTEGER NOT NULL,
 owner_token INTEGER NOT NULL, execution_token INTEGER NOT NULL,
 PRIMARY KEY(agent_id,session_id,epoch,user_seq,user_digest,goal_revision,final_seq,final_digest),
 FOREIGN KEY(agent_id,session_id,epoch,user_seq,user_digest,goal_revision,final_seq,final_digest)
 REFERENCES embedded_goal_continuations(agent_id,session_id,epoch,user_seq,user_digest,goal_revision,final_seq,final_digest) ON DELETE CASCADE
);
CREATE TRIGGER workflow_continuation_start_identity_capture AFTER UPDATE OF state ON embedded_goal_continuations
WHEN OLD.state='reserved' AND NEW.state='started'
BEGIN
 INSERT INTO workflow_continuation_start_identity
 SELECT NEW.agent_id,NEW.session_id,NEW.epoch,NEW.generation,NEW.user_seq,NEW.user_digest,NEW.goal_revision,NEW.final_seq,NEW.final_digest,
 a.active_workflow_id,a.active_workflow_state_id,a.workflow_binding_generation,s.owner_token,s.execution_token
 FROM managed_agents a JOIN workflows w ON w.id=a.active_workflow_id
 JOIN workflow_states st ON st.id=a.active_workflow_state_id AND st.workflow_id=w.id
 JOIN embedded_sessions s ON s.agent_id=NEW.agent_id AND s.session_id=NEW.session_id AND s.epoch=NEW.epoch AND s.generation=NEW.generation AND s.current=1
 WHERE a.agent_id=NEW.agent_id AND json_extract(s.execution_json,'$.kind')='sdk' AND json_extract(s.execution_json,'$.owner')=s.owner_token AND json_extract(s.execution_json,'$.token')=s.execution_token;
END;
CREATE TRIGGER workflow_continuation_start_identity_immutable BEFORE UPDATE ON workflow_continuation_start_identity
BEGIN SELECT RAISE(ABORT,'workflow start identity immutable'); END;
