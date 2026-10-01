-- Provider-request boundary evidence for the exact saved user turn and desired goal revision.
-- A desired policy is never itself proof of application. Receipts are immutable.
CREATE TABLE embedded_goal_applied_receipts (
 turn_key TEXT PRIMARY KEY CHECK(length(turn_key)>0 AND length(turn_key)<=256),
 agent_id TEXT NOT NULL, session_id TEXT NOT NULL, epoch INTEGER NOT NULL,
 generation TEXT NOT NULL, role TEXT NOT NULL CHECK(role IN ('root','worker')),
 turn_entry_seq INTEGER NOT NULL CHECK(turn_entry_seq>0), turn_entry_digest TEXT NOT NULL,
 goal_revision INTEGER NOT NULL CHECK(goal_revision>0),
 goal_enabled INTEGER NOT NULL CHECK(goal_enabled IN (0,1)), goal_text TEXT NOT NULL,
 applied_at TEXT NOT NULL,
 UNIQUE(agent_id,session_id,epoch,generation,turn_entry_seq,goal_revision),
 FOREIGN KEY(agent_id,session_id,epoch,generation) REFERENCES embedded_sessions(agent_id,session_id,epoch,generation) ON DELETE CASCADE,
 FOREIGN KEY(agent_id,session_id,epoch,generation,turn_entry_seq) REFERENCES embedded_entries(agent_id,session_id,epoch,generation,seq) ON DELETE CASCADE
);
CREATE TRIGGER embedded_goal_receipt_insert_guard BEFORE INSERT ON embedded_goal_applied_receipts
BEGIN
 SELECT RAISE(ABORT,'receipt turn origin mismatch') WHERE NOT EXISTS (
  SELECT 1 FROM embedded_entries e WHERE e.agent_id=NEW.agent_id AND e.session_id=NEW.session_id
   AND e.epoch=NEW.epoch AND e.generation=NEW.generation AND e.seq=NEW.turn_entry_seq
   AND e.kind='user' AND e.digest=NEW.turn_entry_digest);
 SELECT RAISE(ABORT,'receipt role mismatch') WHERE NOT EXISTS (
  SELECT 1 FROM embedded_sessions s WHERE s.agent_id=NEW.agent_id AND s.session_id=NEW.session_id
   AND s.epoch=NEW.epoch AND s.generation=NEW.generation AND s.role=NEW.role AND s.current=1);
 SELECT RAISE(ABORT,'receipt desired policy mismatch') WHERE NOT EXISTS (
  SELECT 1 FROM managed_agent_policies p WHERE p.agent_id=NEW.agent_id
   AND p.desired_revision=NEW.goal_revision AND p.goal_enabled=NEW.goal_enabled AND p.goal_text=NEW.goal_text);
END;
CREATE TRIGGER embedded_goal_receipt_immutable_update BEFORE UPDATE ON embedded_goal_applied_receipts
BEGIN SELECT RAISE(ABORT,'embedded goal receipt is immutable'); END;
CREATE TRIGGER embedded_goal_receipt_immutable_delete BEFORE DELETE ON embedded_goal_applied_receipts
BEGIN SELECT RAISE(ABORT,'embedded goal receipt is immutable'); END;
