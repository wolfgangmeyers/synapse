-- Router-owned parent work keeps one direct turn active until its exact final
-- assistant response has been durably delivered. Rows are fence-scoped so an
-- old process can never change a replacement worker's activity.
CREATE TABLE managed_parent_work (
  work_id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL,
  message_id TEXT NOT NULL,
  message_incarnation_id TEXT NOT NULL,
  attempt_number INTEGER NOT NULL,
  pi_session_id TEXT NOT NULL,
  session_epoch INTEGER NOT NULL,
  process_pid INTEGER NOT NULL,
  process_start_marker TEXT NOT NULL,
  process_incarnation TEXT NOT NULL,
  runtime_generation TEXT NOT NULL,
  manifest_revision INTEGER NOT NULL,
  manifest_hash TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('pending_turn','settled','final_delivery_pending','completed','final_delivery_failed','interrupted')),
  created_at TEXT NOT NULL,
  settled_at TEXT,
  terminal_at TEXT,
  UNIQUE(message_id, message_incarnation_id, attempt_number)
);
CREATE INDEX managed_parent_work_active_fence_idx
  ON managed_parent_work(agent_id, pi_session_id, session_epoch, process_pid, process_start_marker, process_incarnation, runtime_generation, manifest_revision, manifest_hash, state);
CREATE INDEX managed_parent_work_terminal_retention_idx
  ON managed_parent_work(agent_id, terminal_at DESC, work_id DESC)
  WHERE state IN ('completed','final_delivery_failed','interrupted');

-- The source body is retained only while this final delivery is pending. The
-- deterministic delivery ID makes the existing conversation enqueue path safe
-- to retry after a router restart without manufacturing another reply.
CREATE TABLE managed_parent_final_deliveries (
  delivery_id TEXT PRIMARY KEY,
  work_id TEXT NOT NULL REFERENCES managed_parent_work(work_id) ON DELETE CASCADE,
  source_entry_identity TEXT NOT NULL,
  body TEXT NOT NULL CHECK (length(CAST(body AS BLOB)) BETWEEN 1 AND 65536),
  state TEXT NOT NULL CHECK (state IN ('pending','delivered')),
  created_at TEXT NOT NULL,
  delivered_at TEXT,
  UNIQUE(work_id, source_entry_identity)
);
CREATE INDEX managed_parent_final_deliveries_pending_idx
  ON managed_parent_final_deliveries(state, work_id)
  WHERE state='pending';
