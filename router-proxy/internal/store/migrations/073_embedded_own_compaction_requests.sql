-- An own-session request is durable but does not authorize a checkpoint or replay.
CREATE TABLE embedded_own_compaction_requests (
 call_id TEXT PRIMARY KEY,
 agent_id TEXT NOT NULL, session_id TEXT NOT NULL, epoch INTEGER NOT NULL,
 generation TEXT NOT NULL, role TEXT NOT NULL CHECK(role IN ('root','worker')),
 owner_token INTEGER NOT NULL CHECK(owner_token>0),
 head_seq INTEGER NOT NULL CHECK(head_seq>0), head_digest TEXT NOT NULL,
 args_json TEXT NOT NULL CHECK(args_json IN ('{}','{"automatic":true}')),
 result BLOB NOT NULL CHECK(length(result)>0 AND length(result)<=1048576),
 FOREIGN KEY(agent_id,session_id,epoch,generation) REFERENCES embedded_sessions(agent_id,session_id,epoch,generation) ON DELETE CASCADE,
 FOREIGN KEY(agent_id,session_id,epoch,head_seq) REFERENCES embedded_entries(agent_id,session_id,epoch,seq)
);
CREATE INDEX embedded_own_compaction_session ON embedded_own_compaction_requests(agent_id,session_id,epoch);
