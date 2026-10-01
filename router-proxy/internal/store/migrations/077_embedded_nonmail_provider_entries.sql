-- A checked user entry alone does not prove a provider turn was entered.
CREATE TABLE embedded_nonmail_provider_entries (
 agent_id TEXT NOT NULL, session_id TEXT NOT NULL, epoch INTEGER NOT NULL,
 generation TEXT NOT NULL, user_seq INTEGER NOT NULL, user_digest TEXT NOT NULL,
 PRIMARY KEY(agent_id,session_id,epoch,user_seq),
 FOREIGN KEY(agent_id,session_id,epoch,generation,user_seq)
  REFERENCES embedded_entries(agent_id,session_id,epoch,generation,seq) ON DELETE CASCADE
);
