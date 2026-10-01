-- One internal, epoch-bound wake per interrupted addressed turn. Started is
-- deliberately terminal: a provider may have received the request.
CREATE TABLE embedded_interrupted_notices (
 agent_id TEXT NOT NULL, session_id TEXT NOT NULL, epoch INTEGER NOT NULL,
 generation TEXT NOT NULL, source_seq INTEGER NOT NULL,
 notice_id TEXT NOT NULL UNIQUE, notice_text TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('pending','appended','started')),
 entry_seq INTEGER, start_owner_token INTEGER,
 PRIMARY KEY(agent_id,session_id,epoch,source_seq),
 FOREIGN KEY(agent_id,session_id,epoch,source_seq) REFERENCES embedded_entries(agent_id,session_id,epoch,seq),
 CHECK((state='pending' AND entry_seq IS NULL AND start_owner_token IS NULL) OR
       (state='appended' AND entry_seq IS NOT NULL AND start_owner_token IS NULL) OR
       (state='started' AND entry_seq IS NOT NULL AND start_owner_token IS NOT NULL))
);
