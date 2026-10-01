-- Provisional Store-only restart awareness. No provider dispatch or router wiring.
CREATE TABLE embedded_process_generation_counter (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1), generation INTEGER NOT NULL CHECK(generation>0)
);
CREATE TABLE embedded_process_generations (
 incarnation_key TEXT PRIMARY KEY CHECK(length(incarnation_key)>0 AND length(incarnation_key)<=256),
 generation INTEGER NOT NULL UNIQUE CHECK(generation>0)
);
CREATE TABLE embedded_restart_wake_slots (
 agent_id TEXT NOT NULL REFERENCES managed_agents(agent_id) ON DELETE CASCADE,
 slot TEXT NOT NULL CHECK(slot IN ('current','last')),
 session_id TEXT NOT NULL, epoch INTEGER NOT NULL CHECK(epoch>0), runtime_generation TEXT NOT NULL,
 process_generation INTEGER NOT NULL REFERENCES embedded_process_generations(generation),
 wake_id TEXT NOT NULL CHECK(length(wake_id)>0 AND length(wake_id)<=256),
 kind TEXT NOT NULL CHECK(kind IN ('root-idle','worker-idle','interrupted')),
 notice_id TEXT REFERENCES embedded_interrupted_notices(notice_id),
 digest TEXT NOT NULL CHECK(length(digest)>0 AND length(digest)<=256),
 state TEXT NOT NULL CHECK(state IN ('planned','claimed','checked-complete','unknown')),
 CHECK((kind='interrupted' AND notice_id=wake_id) OR (kind<>'interrupted' AND notice_id IS NULL)),
 PRIMARY KEY(agent_id,slot), UNIQUE(agent_id,process_generation), UNIQUE(agent_id,wake_id),
 FOREIGN KEY(agent_id,session_id,epoch,runtime_generation) REFERENCES embedded_sessions(agent_id,session_id,epoch,generation) ON DELETE CASCADE
);
