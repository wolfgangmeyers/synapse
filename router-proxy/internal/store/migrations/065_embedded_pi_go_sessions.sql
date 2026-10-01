-- Router-owned runtime choice. Existing rows retain RPC; only new managed identities opt in.
ALTER TABLE managed_agents ADD COLUMN runtime TEXT NOT NULL DEFAULT 'rpc' CHECK(runtime IN ('rpc','embedded'));
CREATE TRIGGER managed_runtime_immutable BEFORE UPDATE OF runtime ON managed_agents
WHEN NEW.runtime <> OLD.runtime BEGIN SELECT RAISE(ABORT,'managed runtime is immutable'); END;
-- Router-owned ordered embedded sessions. Managed deletion cascades the embedded subtree; Store transactions enforce runtime identity.
CREATE TABLE embedded_sessions (
 agent_id TEXT NOT NULL, session_id TEXT NOT NULL, epoch INTEGER NOT NULL CHECK(epoch > 0),
 generation TEXT NOT NULL, role TEXT NOT NULL CHECK(role IN ('root','worker')),
 current INTEGER NOT NULL CHECK(current IN (0,1)),
 owner_id TEXT, owner_token INTEGER NOT NULL DEFAULT 0 CHECK(owner_token>=0),
 owner_expires_at TEXT,
 CHECK((owner_id IS NULL AND owner_expires_at IS NULL) OR (owner_id IS NOT NULL AND owner_expires_at IS NOT NULL)),
 PRIMARY KEY(agent_id,session_id,epoch,generation),
 FOREIGN KEY(agent_id) REFERENCES managed_agents(agent_id) ON DELETE CASCADE,
 CHECK(length(agent_id)>0 AND length(session_id)>0 AND length(generation)>0)
);
CREATE UNIQUE INDEX embedded_current_agent ON embedded_sessions(agent_id) WHERE current=1;
CREATE TABLE embedded_entries (
 agent_id TEXT NOT NULL, session_id TEXT NOT NULL, epoch INTEGER NOT NULL,
 generation TEXT NOT NULL, seq INTEGER NOT NULL CHECK(seq>0), parent_digest TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(length(kind)>0 AND length(kind)<=64), body BLOB NOT NULL CHECK(length(body)>0 AND length(body)<=1048576), digest TEXT NOT NULL,
 PRIMARY KEY(agent_id,session_id,epoch,seq),
 UNIQUE(agent_id,session_id,epoch,generation,seq),
 FOREIGN KEY(agent_id,session_id,epoch,generation) REFERENCES embedded_sessions(agent_id,session_id,epoch,generation) ON DELETE CASCADE
);
CREATE TABLE embedded_append_keys (
 append_key TEXT PRIMARY KEY, agent_id TEXT NOT NULL, session_id TEXT NOT NULL,
 epoch INTEGER NOT NULL, generation TEXT NOT NULL, entry_seq INTEGER NOT NULL,
 UNIQUE(agent_id,session_id,epoch,entry_seq),
 FOREIGN KEY(agent_id,session_id,epoch,generation,entry_seq) REFERENCES embedded_entries(agent_id,session_id,epoch,generation,seq) ON DELETE CASCADE
);
CREATE TABLE embedded_checkpoints (
 agent_id TEXT NOT NULL, session_id TEXT NOT NULL, epoch INTEGER NOT NULL,
 generation TEXT NOT NULL, at_seq INTEGER NOT NULL CHECK(at_seq>0), ancestor_digest TEXT NOT NULL,
 body BLOB NOT NULL CHECK(length(body)>0 AND length(body)<=1048576), digest TEXT NOT NULL,
 PRIMARY KEY(agent_id,session_id,epoch,at_seq),
 FOREIGN KEY(agent_id,session_id,epoch,generation) REFERENCES embedded_sessions(agent_id,session_id,epoch,generation) ON DELETE CASCADE,
 FOREIGN KEY(agent_id,session_id,epoch,at_seq) REFERENCES embedded_entries(agent_id,session_id,epoch,seq)
);
CREATE TABLE embedded_prompt_requests (
 request_id TEXT PRIMARY KEY, agent_id TEXT NOT NULL, session_id TEXT NOT NULL,
 epoch INTEGER NOT NULL, generation TEXT NOT NULL, prompt TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('pending','complete')),
 result_seq INTEGER,
 FOREIGN KEY(agent_id,session_id,epoch,generation) REFERENCES embedded_sessions(agent_id,session_id,epoch,generation) ON DELETE CASCADE,
 FOREIGN KEY(agent_id,session_id,epoch,generation,result_seq) REFERENCES embedded_entries(agent_id,session_id,epoch,generation,seq)
);
CREATE TABLE embedded_effects (
 effect_id TEXT PRIMARY KEY, agent_id TEXT NOT NULL, session_id TEXT NOT NULL,
 epoch INTEGER NOT NULL, generation TEXT NOT NULL, intent BLOB NOT NULL CHECK(length(intent)>0 AND length(intent)<=1048576),
 state TEXT NOT NULL CHECK(state IN ('unknown','observed')),
 result BLOB, CHECK((state='unknown' AND result IS NULL) OR (state='observed' AND result IS NOT NULL AND length(result)>0 AND length(result)<=1048576)),
 FOREIGN KEY(agent_id,session_id,epoch,generation) REFERENCES embedded_sessions(agent_id,session_id,epoch,generation) ON DELETE CASCADE
);
CREATE TABLE embedded_attempts (
 attempt_id TEXT PRIMARY KEY, message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
 message_incarnation_id TEXT NOT NULL, snapshot_id TEXT NOT NULL,
 sender TEXT NOT NULL, accepted_body TEXT NOT NULL, accepted_digest TEXT NOT NULL,
 agent_id TEXT NOT NULL, session_id TEXT NOT NULL, epoch INTEGER NOT NULL,
 generation TEXT NOT NULL, append_key TEXT NOT NULL UNIQUE,
 UNIQUE(message_id,message_incarnation_id),
 UNIQUE(attempt_id,message_id,agent_id,session_id,epoch,generation),
 FOREIGN KEY(agent_id,session_id,epoch,generation) REFERENCES embedded_sessions(agent_id,session_id,epoch,generation) ON DELETE CASCADE
);
CREATE TABLE embedded_acks (
 attempt_id TEXT PRIMARY KEY, message_id TEXT NOT NULL, agent_id TEXT NOT NULL,
 session_id TEXT NOT NULL, epoch INTEGER NOT NULL, generation TEXT NOT NULL, entry_seq INTEGER NOT NULL,
 UNIQUE(agent_id,session_id,epoch,entry_seq),
 FOREIGN KEY(attempt_id,message_id,agent_id,session_id,epoch,generation) REFERENCES embedded_attempts(attempt_id,message_id,agent_id,session_id,epoch,generation) ON DELETE CASCADE,
 FOREIGN KEY(agent_id,session_id,epoch,generation,entry_seq) REFERENCES embedded_entries(agent_id,session_id,epoch,generation,seq) ON DELETE CASCADE
);
-- Completed assistant and reply intent commit in the same transaction. Pending
-- intents keep their accepted attempt alive past the ordinary mail TTL.
CREATE TABLE embedded_reply_outbox (
 attempt_id TEXT PRIMARY KEY REFERENCES embedded_attempts(attempt_id) ON DELETE CASCADE,
 entry_seq INTEGER NOT NULL, entry_digest TEXT NOT NULL, reply_id TEXT NOT NULL UNIQUE,
 sender TEXT NOT NULL, recipient TEXT NOT NULL,
 body TEXT NOT NULL CHECK(length(body)>0), created_at TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('pending','delivered')),
 FOREIGN KEY(attempt_id) REFERENCES embedded_acks(attempt_id) ON DELETE CASCADE
);
-- Insert-time chain and checkpoint ancestry constraints. Reopen still verifies
-- digest hashes and detects later corruption (SQLite cannot hash these bodies).
CREATE TRIGGER embedded_entry_order BEFORE INSERT ON embedded_entries
BEGIN
 SELECT RAISE(ABORT,'embedded entry out of order') WHERE
  (NEW.seq=1 AND NEW.parent_digest<>'genesis') OR
  (NEW.seq>1 AND NOT EXISTS
   (SELECT 1 FROM embedded_entries WHERE agent_id=NEW.agent_id AND session_id=NEW.session_id
    AND epoch=NEW.epoch AND seq=NEW.seq-1 AND digest=NEW.parent_digest));
END;
CREATE TRIGGER embedded_checkpoint_ancestor BEFORE INSERT ON embedded_checkpoints
BEGIN
 SELECT RAISE(ABORT,'embedded checkpoint ancestor mismatch') WHERE NOT EXISTS
 (SELECT 1 FROM embedded_entries WHERE agent_id=NEW.agent_id AND session_id=NEW.session_id
  AND epoch=NEW.epoch AND seq=NEW.at_seq AND digest=NEW.ancestor_digest);
END;
CREATE TRIGGER embedded_attempt_recipient BEFORE INSERT ON embedded_attempts
BEGIN
 SELECT RAISE(ABORT,'embedded attempt accepted envelope mismatch') WHERE NOT EXISTS
 (SELECT 1 FROM messages m JOIN message_target_snapshots t ON t.message_id=m.id
  WHERE m.id=NEW.message_id AND m.recipient=NEW.agent_id
  AND m.sender=NEW.sender AND m.body=NEW.accepted_body
  AND m.message_incarnation_id=NEW.message_incarnation_id
  AND t.snapshot_id=NEW.snapshot_id AND t.owner_identity=NEW.agent_id
  AND t.source_kind='managed' AND t.message_incarnation_id=NEW.message_incarnation_id);
END;
-- The Store validates canonical SDK user JSON and the frozen accepted envelope
-- in its immediate transaction. SQL also forbids independent evidence writes.
CREATE TRIGGER embedded_ack_user BEFORE INSERT ON embedded_acks
BEGIN
 SELECT RAISE(ABORT,'embedded ACK must reference user entry') WHERE NOT EXISTS
 (SELECT 1 FROM embedded_entries WHERE agent_id=NEW.agent_id AND session_id=NEW.session_id
  AND epoch=NEW.epoch AND generation=NEW.generation AND seq=NEW.entry_seq AND kind='user');
 SELECT RAISE(ABORT,'embedded ACK key mismatch') WHERE NOT EXISTS
 (SELECT 1 FROM embedded_attempts a JOIN embedded_append_keys k ON k.append_key=a.append_key
  WHERE a.attempt_id=NEW.attempt_id AND a.message_id=NEW.message_id
  AND k.agent_id=NEW.agent_id AND k.session_id=NEW.session_id
  AND k.epoch=NEW.epoch AND k.generation=NEW.generation AND k.entry_seq=NEW.entry_seq);
END;
