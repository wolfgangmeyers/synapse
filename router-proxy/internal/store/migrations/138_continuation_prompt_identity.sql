-- Legacy starts have no provable prompt snapshot and cannot be dispatched.
CREATE TABLE embedded_continuation_prompt_identity (
 agent_id TEXT NOT NULL, session_id TEXT NOT NULL, epoch INTEGER NOT NULL, generation TEXT NOT NULL,
 user_seq INTEGER NOT NULL, user_digest TEXT NOT NULL, goal_revision INTEGER NOT NULL, final_seq INTEGER NOT NULL, final_digest TEXT NOT NULL,
 owner_token INTEGER NOT NULL, execution_token INTEGER NOT NULL,
 workflow_id TEXT NOT NULL, workflow_state_id TEXT NOT NULL, workflow_name TEXT NOT NULL, workflow_state_name TEXT NOT NULL, goal_text TEXT NOT NULL,
 PRIMARY KEY(agent_id,session_id,epoch,user_seq,user_digest,goal_revision,final_seq,final_digest),
 FOREIGN KEY(agent_id,session_id,epoch,user_seq,user_digest,goal_revision,final_seq,final_digest)
 REFERENCES embedded_goal_continuations(agent_id,session_id,epoch,user_seq,user_digest,goal_revision,final_seq,final_digest) ON DELETE CASCADE
);
CREATE TRIGGER embedded_continuation_prompt_identity_capture AFTER UPDATE OF state ON embedded_goal_continuations
WHEN OLD.state='reserved' AND NEW.state='started'
BEGIN
 INSERT INTO embedded_continuation_prompt_identity
 SELECT NEW.agent_id,NEW.session_id,NEW.epoch,NEW.generation,NEW.user_seq,NEW.user_digest,NEW.goal_revision,NEW.final_seq,NEW.final_digest,
 s.owner_token,s.execution_token,COALESCE(w.id,''),COALESCE(st.id,''),COALESCE(w.name,''),COALESCE(st.name,''),COALESCE(r.goal_text,'')
 FROM managed_agents a JOIN embedded_sessions s ON s.agent_id=NEW.agent_id AND s.session_id=NEW.session_id AND s.epoch=NEW.epoch AND s.generation=NEW.generation AND s.current=1
 JOIN embedded_goal_applied_receipts r ON r.agent_id=NEW.agent_id AND r.session_id=NEW.session_id AND r.epoch=NEW.epoch AND r.generation=NEW.generation AND r.turn_entry_seq=NEW.user_seq AND r.turn_entry_digest=NEW.user_digest AND r.goal_revision=NEW.goal_revision
 LEFT JOIN workflows w ON w.id=a.active_workflow_id
 LEFT JOIN workflow_states st ON st.id=a.active_workflow_state_id AND st.workflow_id=w.id
 WHERE a.agent_id=NEW.agent_id AND ((a.active_workflow_id IS NULL AND a.active_workflow_state_id IS NULL) OR (w.id IS NOT NULL AND st.id IS NOT NULL));
 SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM embedded_continuation_prompt_identity i WHERE i.agent_id=NEW.agent_id AND i.session_id=NEW.session_id AND i.epoch=NEW.epoch AND i.generation=NEW.generation AND i.user_seq=NEW.user_seq AND i.user_digest=NEW.user_digest AND i.goal_revision=NEW.goal_revision AND i.final_seq=NEW.final_seq AND i.final_digest=NEW.final_digest) THEN RAISE(ABORT,'continuation prompt authority unavailable') END;
END;
CREATE TRIGGER embedded_continuation_prompt_identity_immutable BEFORE UPDATE ON embedded_continuation_prompt_identity BEGIN SELECT RAISE(ABORT,'continuation prompt identity immutable'); END;
