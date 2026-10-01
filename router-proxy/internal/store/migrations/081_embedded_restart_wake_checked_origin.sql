-- The origin is immutable evidence for the two retained wake slots only.
-- Deleting/replacing a slot cascades its evidence, not a live UNKNOWN slot's evidence.
ALTER TABLE embedded_restart_wake_slots ADD COLUMN claim_owner_token INTEGER CHECK(claim_owner_token>0);
CREATE TABLE embedded_restart_wake_checked_origins (
 agent_id TEXT NOT NULL, session_id TEXT NOT NULL, epoch INTEGER NOT NULL,
 runtime_generation TEXT NOT NULL, seq INTEGER NOT NULL,
 slot TEXT NOT NULL CHECK(slot IN ('current','last')),
 wake_id TEXT NOT NULL, process_generation INTEGER NOT NULL,
 claim_owner_token INTEGER NOT NULL CHECK(claim_owner_token>0),
 PRIMARY KEY(agent_id,session_id,epoch,runtime_generation,seq),
 UNIQUE(agent_id,slot),
 FOREIGN KEY(agent_id,slot) REFERENCES embedded_restart_wake_slots(agent_id,slot) ON UPDATE CASCADE ON DELETE CASCADE,
 FOREIGN KEY(agent_id,session_id,epoch,runtime_generation,seq)
  REFERENCES embedded_entries(agent_id,session_id,epoch,generation,seq) ON DELETE CASCADE
);
