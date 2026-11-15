-- Also cover installations that have already applied migration 137. At this
-- point missing witnesses cannot distinguish legacy work from ordinary goals.
CREATE TABLE IF NOT EXISTS legacy_continuation_ambiguity (
 agent_id TEXT PRIMARY KEY REFERENCES managed_agents(agent_id) ON DELETE CASCADE,
 recorded_at_ms INTEGER NOT NULL
);
INSERT OR IGNORE INTO legacy_continuation_ambiguity
 SELECT c.agent_id, CAST(strftime('%s','now') AS INTEGER)*1000
 FROM embedded_goal_continuations c
 WHERE c.state='started' AND c.disposition='unknown'
 AND NOT EXISTS (SELECT 1 FROM workflow_continuation_start_identity i
 WHERE i.agent_id=c.agent_id AND i.session_id=c.session_id AND i.epoch=c.epoch
 AND i.user_seq=c.user_seq AND i.user_digest=c.user_digest AND i.goal_revision=c.goal_revision
 AND i.final_seq=c.final_seq AND i.final_digest=c.final_digest)
 AND NOT EXISTS (SELECT 1 FROM embedded_continuation_prompt_identity p
 WHERE p.agent_id=c.agent_id AND p.session_id=c.session_id AND p.epoch=c.epoch
 AND p.generation=c.generation AND p.user_seq=c.user_seq AND p.user_digest=c.user_digest
 AND p.goal_revision=c.goal_revision AND p.final_seq=c.final_seq AND p.final_digest=c.final_digest);
