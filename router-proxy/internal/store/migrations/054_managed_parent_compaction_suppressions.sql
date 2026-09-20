-- A managed compact request can return Pi's exact, classified "session too
-- small" no-op while Pi also emits its generic failed compaction lifecycle.
-- Retain those native transitions for audit/state projection, but correlate the
-- pair so Complete history can use the managed result as its sole presentation.
CREATE TABLE managed_parent_compaction_suppressions (
  request_id TEXT PRIMARY KEY REFERENCES managed_control_requests(request_id) ON DELETE CASCADE,
  agent_id TEXT NOT NULL,
  pi_session_id TEXT NOT NULL,
  session_epoch INTEGER NOT NULL,
  process_incarnation TEXT NOT NULL,
  runtime_generation TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('pending','started','consumed')),
  start_event_key TEXT,
  end_event_key TEXT,
  created_at TEXT NOT NULL,
  consumed_at TEXT,
  CHECK ((state = 'pending' AND start_event_key IS NULL AND end_event_key IS NULL AND consumed_at IS NULL)
    OR (state = 'started' AND start_event_key IS NOT NULL AND end_event_key IS NULL AND consumed_at IS NULL)
    OR (state = 'consumed' AND start_event_key IS NOT NULL AND end_event_key IS NOT NULL AND consumed_at IS NOT NULL))
);
CREATE INDEX managed_parent_compaction_suppressions_fence_state_idx
  ON managed_parent_compaction_suppressions (agent_id,pi_session_id,session_epoch,process_incarnation,runtime_generation,state,created_at);
CREATE UNIQUE INDEX managed_parent_compaction_suppressions_start_idx
  ON managed_parent_compaction_suppressions (start_event_key) WHERE start_event_key IS NOT NULL;
CREATE UNIQUE INDEX managed_parent_compaction_suppressions_end_idx
  ON managed_parent_compaction_suppressions (end_event_key) WHERE end_event_key IS NOT NULL;

-- Repair already-recorded duplicates with the same closed classification used
-- by the manager. Consecutive native start/failed frames within five seconds
-- of that exact result are the generic lifecycle for the known no-op.
INSERT OR IGNORE INTO managed_parent_compaction_suppressions(request_id,agent_id,pi_session_id,session_epoch,process_incarnation,runtime_generation,state,start_event_key,end_event_key,created_at,consumed_at)
SELECT request.request_id,request.agent_id,request.pi_session_id,request.session_epoch,request.process_incarnation,request.runtime_generation,'consumed',start.event_key,terminal.event_key,request.handler_completed_at,terminal.occurred_at
FROM managed_control_requests request
JOIN managed_parent_compaction_transitions start
  ON start.agent_id=request.agent_id AND start.pi_session_id=request.pi_session_id AND start.session_epoch=request.session_epoch
  AND start.process_incarnation=request.process_incarnation AND start.runtime_generation=request.runtime_generation AND start.state='compacting'
JOIN managed_parent_compaction_transitions terminal
  ON terminal.agent_id=start.agent_id AND terminal.pi_session_id=start.pi_session_id AND terminal.session_epoch=start.session_epoch
  AND terminal.process_incarnation=start.process_incarnation AND terminal.runtime_generation=start.runtime_generation
  AND terminal.source_sequence=start.source_sequence+1 AND terminal.state='idle' AND terminal.outcome='failed'
WHERE request.operation_id='context.control' AND request.result_status='error' AND request.error_code='HANDLER_FAILED'
  AND request.error_detail='Pi RPC error: Nothing to compact (session too small)' AND request.handler_completed_at IS NOT NULL
  AND start.occurred_at>=request.handler_completed_at
  AND julianday(start.occurred_at)-julianday(request.handler_completed_at) BETWEEN 0 AND (5.0/86400.0)
  AND NOT EXISTS (
    SELECT 1 FROM managed_parent_compaction_transitions earlier
    WHERE earlier.agent_id=start.agent_id AND earlier.pi_session_id=start.pi_session_id AND earlier.session_epoch=start.session_epoch
      AND earlier.process_incarnation=start.process_incarnation AND earlier.runtime_generation=start.runtime_generation
      AND earlier.state='compacting' AND earlier.occurred_at>=request.handler_completed_at AND earlier.source_sequence<start.source_sequence
  );
