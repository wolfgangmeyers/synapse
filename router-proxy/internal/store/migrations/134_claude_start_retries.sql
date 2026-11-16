CREATE TABLE claude_start_retries (
 message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
 incarnation_id TEXT NOT NULL,
 recipient TEXT NOT NULL,
 deadline TEXT NOT NULL,
 next_eligible TEXT NOT NULL,
 executions INTEGER NOT NULL CHECK(executions BETWEEN 1 AND 5),
 category TEXT NOT NULL,
 in_flight INTEGER NOT NULL CHECK(in_flight IN (0,1)),
 prompt_settlement INTEGER NOT NULL DEFAULT 0 CHECK(prompt_settlement IN (0,1)),
 accepted_snapshot_id TEXT NOT NULL DEFAULT '',
 accepted_digest TEXT NOT NULL DEFAULT '',
 accepted_session_id TEXT NOT NULL DEFAULT '',
 accepted_epoch INTEGER NOT NULL DEFAULT 0,
 accepted_generation TEXT NOT NULL DEFAULT '',
 accepted_attempt_id TEXT NOT NULL DEFAULT '',
 rejected_release_safe INTEGER NOT NULL DEFAULT 0 CHECK(rejected_release_safe IN (0,1)),
 accepted_owner_token INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(message_id, incarnation_id, recipient)
);

CREATE TRIGGER claude_retry_message_lifecycle AFTER UPDATE OF message_incarnation_id, recipient, state ON messages
WHEN OLD.message_incarnation_id != NEW.message_incarnation_id OR OLD.recipient != NEW.recipient OR NEW.state != 'queued'
BEGIN DELETE FROM claude_start_retries WHERE message_id=OLD.id; END;

CREATE TABLE claude_retry_capacity (id INTEGER PRIMARY KEY CHECK(id=1), rows INTEGER NOT NULL, bytes INTEGER NOT NULL);
INSERT INTO claude_retry_capacity VALUES(1, 0, 0);

CREATE TRIGGER claude_retry_capacity_insert BEFORE INSERT ON claude_start_retries
BEGIN
 SELECT CASE WHEN (length(CAST(NEW.message_id AS BLOB)) + length(CAST(NEW.incarnation_id AS BLOB)) + length(CAST(NEW.recipient AS BLOB)) + length(CAST(NEW.deadline AS BLOB)) + length(CAST(NEW.next_eligible AS BLOB)) + length(CAST(NEW.accepted_snapshot_id AS BLOB)) + length(CAST(NEW.accepted_digest AS BLOB)) + length(CAST(NEW.accepted_session_id AS BLOB)) + length(CAST(NEW.accepted_generation AS BLOB)) + length(CAST(NEW.accepted_attempt_id AS BLOB)) + max(12, length(CAST(NEW.category AS BLOB))) + 48) > 4096
   OR (SELECT rows FROM claude_retry_capacity WHERE id=1) >= 10000
   OR (SELECT bytes FROM claude_retry_capacity WHERE id=1) + (length(CAST(NEW.message_id AS BLOB)) + length(CAST(NEW.incarnation_id AS BLOB)) + length(CAST(NEW.recipient AS BLOB)) + length(CAST(NEW.deadline AS BLOB)) + length(CAST(NEW.next_eligible AS BLOB)) + length(CAST(NEW.accepted_snapshot_id AS BLOB)) + length(CAST(NEW.accepted_digest AS BLOB)) + length(CAST(NEW.accepted_session_id AS BLOB)) + length(CAST(NEW.accepted_generation AS BLOB)) + length(CAST(NEW.accepted_attempt_id AS BLOB)) + max(12, length(CAST(NEW.category AS BLOB))) + 48) > 16777216
 THEN RAISE(ABORT, 'Claude retry retention capacity exceeded') END;
END;

CREATE TRIGGER claude_retry_capacity_update BEFORE UPDATE ON claude_start_retries
BEGIN
 SELECT CASE WHEN ((length(CAST(NEW.message_id AS BLOB)) + length(CAST(NEW.incarnation_id AS BLOB)) + length(CAST(NEW.recipient AS BLOB)) + length(CAST(NEW.deadline AS BLOB)) + length(CAST(NEW.next_eligible AS BLOB)) + length(CAST(NEW.accepted_snapshot_id AS BLOB)) + length(CAST(NEW.accepted_digest AS BLOB)) + length(CAST(NEW.accepted_session_id AS BLOB)) + length(CAST(NEW.accepted_generation AS BLOB)) + length(CAST(NEW.accepted_attempt_id AS BLOB)) + max(12, length(CAST(NEW.category AS BLOB))) + 48) > 4096 AND (length(CAST(NEW.message_id AS BLOB)) + length(CAST(NEW.incarnation_id AS BLOB)) + length(CAST(NEW.recipient AS BLOB)) + length(CAST(NEW.deadline AS BLOB)) + length(CAST(NEW.next_eligible AS BLOB)) + length(CAST(NEW.accepted_snapshot_id AS BLOB)) + length(CAST(NEW.accepted_digest AS BLOB)) + length(CAST(NEW.accepted_session_id AS BLOB)) + length(CAST(NEW.accepted_generation AS BLOB)) + length(CAST(NEW.accepted_attempt_id AS BLOB)) + max(12, length(CAST(NEW.category AS BLOB))) + 48) > (length(CAST(OLD.message_id AS BLOB)) + length(CAST(OLD.incarnation_id AS BLOB)) + length(CAST(OLD.recipient AS BLOB)) + length(CAST(OLD.deadline AS BLOB)) + length(CAST(OLD.next_eligible AS BLOB)) + length(CAST(OLD.accepted_snapshot_id AS BLOB)) + length(CAST(OLD.accepted_digest AS BLOB)) + length(CAST(OLD.accepted_session_id AS BLOB)) + length(CAST(OLD.accepted_generation AS BLOB)) + length(CAST(OLD.accepted_attempt_id AS BLOB)) + max(12, length(CAST(OLD.category AS BLOB))) + 48))
   OR ((length(CAST(NEW.message_id AS BLOB)) + length(CAST(NEW.incarnation_id AS BLOB)) + length(CAST(NEW.recipient AS BLOB)) + length(CAST(NEW.deadline AS BLOB)) + length(CAST(NEW.next_eligible AS BLOB)) + length(CAST(NEW.accepted_snapshot_id AS BLOB)) + length(CAST(NEW.accepted_digest AS BLOB)) + length(CAST(NEW.accepted_session_id AS BLOB)) + length(CAST(NEW.accepted_generation AS BLOB)) + length(CAST(NEW.accepted_attempt_id AS BLOB)) + max(12, length(CAST(NEW.category AS BLOB))) + 48) > (length(CAST(OLD.message_id AS BLOB)) + length(CAST(OLD.incarnation_id AS BLOB)) + length(CAST(OLD.recipient AS BLOB)) + length(CAST(OLD.deadline AS BLOB)) + length(CAST(OLD.next_eligible AS BLOB)) + length(CAST(OLD.accepted_snapshot_id AS BLOB)) + length(CAST(OLD.accepted_digest AS BLOB)) + length(CAST(OLD.accepted_session_id AS BLOB)) + length(CAST(OLD.accepted_generation AS BLOB)) + length(CAST(OLD.accepted_attempt_id AS BLOB)) + max(12, length(CAST(OLD.category AS BLOB))) + 48) AND (SELECT bytes FROM claude_retry_capacity WHERE id=1) - (length(CAST(OLD.message_id AS BLOB)) + length(CAST(OLD.incarnation_id AS BLOB)) + length(CAST(OLD.recipient AS BLOB)) + length(CAST(OLD.deadline AS BLOB)) + length(CAST(OLD.next_eligible AS BLOB)) + length(CAST(OLD.accepted_snapshot_id AS BLOB)) + length(CAST(OLD.accepted_digest AS BLOB)) + length(CAST(OLD.accepted_session_id AS BLOB)) + length(CAST(OLD.accepted_generation AS BLOB)) + length(CAST(OLD.accepted_attempt_id AS BLOB)) + max(12, length(CAST(OLD.category AS BLOB))) + 48) + (length(CAST(NEW.message_id AS BLOB)) + length(CAST(NEW.incarnation_id AS BLOB)) + length(CAST(NEW.recipient AS BLOB)) + length(CAST(NEW.deadline AS BLOB)) + length(CAST(NEW.next_eligible AS BLOB)) + length(CAST(NEW.accepted_snapshot_id AS BLOB)) + length(CAST(NEW.accepted_digest AS BLOB)) + length(CAST(NEW.accepted_session_id AS BLOB)) + length(CAST(NEW.accepted_generation AS BLOB)) + length(CAST(NEW.accepted_attempt_id AS BLOB)) + max(12, length(CAST(NEW.category AS BLOB))) + 48) > 16777216)
 THEN RAISE(ABORT, 'Claude retry retention capacity exceeded') END;
END;

CREATE TRIGGER claude_retry_capacity_count_insert AFTER INSERT ON claude_start_retries
BEGIN
 UPDATE claude_retry_capacity SET rows=rows+1, bytes=bytes+(length(CAST(NEW.message_id AS BLOB)) + length(CAST(NEW.incarnation_id AS BLOB)) + length(CAST(NEW.recipient AS BLOB)) + length(CAST(NEW.deadline AS BLOB)) + length(CAST(NEW.next_eligible AS BLOB)) + length(CAST(NEW.accepted_snapshot_id AS BLOB)) + length(CAST(NEW.accepted_digest AS BLOB)) + length(CAST(NEW.accepted_session_id AS BLOB)) + length(CAST(NEW.accepted_generation AS BLOB)) + length(CAST(NEW.accepted_attempt_id AS BLOB)) + max(12, length(CAST(NEW.category AS BLOB))) + 48) WHERE id=1;
END;

CREATE TRIGGER claude_retry_capacity_count_update AFTER UPDATE ON claude_start_retries
BEGIN
 UPDATE claude_retry_capacity SET bytes=bytes-(length(CAST(OLD.message_id AS BLOB)) + length(CAST(OLD.incarnation_id AS BLOB)) + length(CAST(OLD.recipient AS BLOB)) + length(CAST(OLD.deadline AS BLOB)) + length(CAST(OLD.next_eligible AS BLOB)) + length(CAST(OLD.accepted_snapshot_id AS BLOB)) + length(CAST(OLD.accepted_digest AS BLOB)) + length(CAST(OLD.accepted_session_id AS BLOB)) + length(CAST(OLD.accepted_generation AS BLOB)) + length(CAST(OLD.accepted_attempt_id AS BLOB)) + max(12, length(CAST(OLD.category AS BLOB))) + 48)+(length(CAST(NEW.message_id AS BLOB)) + length(CAST(NEW.incarnation_id AS BLOB)) + length(CAST(NEW.recipient AS BLOB)) + length(CAST(NEW.deadline AS BLOB)) + length(CAST(NEW.next_eligible AS BLOB)) + length(CAST(NEW.accepted_snapshot_id AS BLOB)) + length(CAST(NEW.accepted_digest AS BLOB)) + length(CAST(NEW.accepted_session_id AS BLOB)) + length(CAST(NEW.accepted_generation AS BLOB)) + length(CAST(NEW.accepted_attempt_id AS BLOB)) + max(12, length(CAST(NEW.category AS BLOB))) + 48) WHERE id=1;
END;

CREATE TRIGGER claude_retry_capacity_count_delete AFTER DELETE ON claude_start_retries
BEGIN
 UPDATE claude_retry_capacity SET rows=rows-1, bytes=bytes-(length(CAST(OLD.message_id AS BLOB)) + length(CAST(OLD.incarnation_id AS BLOB)) + length(CAST(OLD.recipient AS BLOB)) + length(CAST(OLD.deadline AS BLOB)) + length(CAST(OLD.next_eligible AS BLOB)) + length(CAST(OLD.accepted_snapshot_id AS BLOB)) + length(CAST(OLD.accepted_digest AS BLOB)) + length(CAST(OLD.accepted_session_id AS BLOB)) + length(CAST(OLD.accepted_generation AS BLOB)) + length(CAST(OLD.accepted_attempt_id AS BLOB)) + max(12, length(CAST(OLD.category AS BLOB))) + 48) WHERE id=1;
END;

CREATE TABLE claude_startup_guards (
 agent_id TEXT PRIMARY KEY REFERENCES managed_agents(agent_id) ON DELETE CASCADE,
 session_id TEXT NOT NULL CHECK(length(session_id)>0),
 epoch INTEGER NOT NULL CHECK(epoch>0),
 generation TEXT NOT NULL CHECK(length(generation)>0),
 owner_token INTEGER NOT NULL CHECK(owner_token>0),
 operation_id TEXT NOT NULL CHECK(length(operation_id)>0)
);

CREATE TRIGGER claude_startup_guards_agent_delete AFTER DELETE ON managed_agents
BEGIN DELETE FROM claude_startup_guards WHERE agent_id=OLD.agent_id; END;
