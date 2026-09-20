-- Phase 0 managed-workflow authority. Definitions and revisions are durable
-- independently of transport; run tables establish the FK/uniqueness foundation
-- used by launch, delivery, transition processing, and recovery workstreams.
CREATE TABLE workflow_definitions (
  definition_id TEXT PRIMARY KEY CHECK (length(definition_id)=36),
  name TEXT NOT NULL UNIQUE CHECK (length(CAST(name AS BLOB)) BETWEEN 1 AND 256),
  description TEXT NOT NULL DEFAULT '' CHECK (length(CAST(description AS BLOB)) <= 4096),
  status TEXT NOT NULL CHECK (status IN ('enabled','disabled','archived')),
  version INTEGER NOT NULL CHECK (version >= 1),
  current_revision INTEGER NOT NULL CHECK (current_revision >= 1),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(definition_id,current_revision)
);
CREATE INDEX workflow_definitions_list_idx ON workflow_definitions(name,definition_id);

CREATE TABLE workflow_definition_revisions (
  definition_id TEXT NOT NULL REFERENCES workflow_definitions(definition_id) ON DELETE CASCADE,
  revision INTEGER NOT NULL CHECK (revision >= 1),
  snapshot_json TEXT NOT NULL CHECK (json_valid(snapshot_json) AND json_type(snapshot_json)='object' AND length(CAST(snapshot_json AS BLOB)) <= 65536),
  validation_valid INTEGER NOT NULL CHECK (validation_valid IN (0,1)),
  validation_json TEXT NOT NULL CHECK (json_valid(validation_json) AND json_type(validation_json)='object' AND length(CAST(validation_json AS BLOB)) <= 65536),
  created_at TEXT NOT NULL,
  PRIMARY KEY(definition_id,revision)
);
CREATE INDEX workflow_definition_revisions_list_idx ON workflow_definition_revisions(definition_id,revision DESC);
CREATE TRIGGER workflow_definition_revisions_immutable
BEFORE UPDATE ON workflow_definition_revisions
BEGIN
  SELECT RAISE(ABORT,'workflow definition revisions are immutable');
END;

CREATE TABLE workflow_runs (
  run_id TEXT PRIMARY KEY CHECK (length(run_id)=36),
  definition_id TEXT NOT NULL,
  revision INTEGER NOT NULL,
  definition_name TEXT NOT NULL CHECK (length(CAST(definition_name AS BLOB)) BETWEEN 1 AND 256),
  agent_id TEXT NOT NULL REFERENCES managed_agents(agent_id) ON DELETE RESTRICT,
  inputs_json TEXT NOT NULL CHECK (json_valid(inputs_json) AND json_type(inputs_json)='object' AND length(CAST(inputs_json AS BLOB)) <= 65536),
  status TEXT NOT NULL CHECK (status IN ('active','completed','cancelled','failed')),
  phase TEXT NOT NULL CHECK (phase IN ('active','transitioning','blocked','terminal')),
  version INTEGER NOT NULL CHECK (version >= 1),
  current_state_id TEXT,
  current_entry_id TEXT,
  final_exit TEXT,
  launch_pi_session_id TEXT NOT NULL,
  launch_session_epoch INTEGER NOT NULL CHECK (launch_session_epoch >= 1),
  launch_process_pid INTEGER NOT NULL CHECK (launch_process_pid > 0),
  launch_process_start_marker TEXT NOT NULL,
  launch_process_incarnation TEXT NOT NULL,
  launch_runtime_generation TEXT NOT NULL,
  launch_manifest_revision INTEGER NOT NULL CHECK (launch_manifest_revision >= 1),
  launch_manifest_hash TEXT NOT NULL CHECK (length(launch_manifest_hash)=64 AND launch_manifest_hash=lower(launch_manifest_hash) AND launch_manifest_hash NOT GLOB '*[^0-9a-f]*'),
  started_at TEXT NOT NULL,
  completed_at TEXT,
  updated_at TEXT NOT NULL,
  FOREIGN KEY(definition_id,revision) REFERENCES workflow_definition_revisions(definition_id,revision) ON DELETE RESTRICT,
  CHECK ((status='active') = (completed_at IS NULL)),
  CHECK ((status='active') = (phase<>'terminal')),
  CHECK ((status='active') = (current_state_id IS NOT NULL AND current_entry_id IS NOT NULL)),
  CHECK ((status='completed') = (final_exit IS NOT NULL))
);
CREATE UNIQUE INDEX workflow_runs_one_nonterminal_per_agent_idx ON workflow_runs(agent_id) WHERE status='active';
CREATE INDEX workflow_runs_definition_status_idx ON workflow_runs(definition_id,status,started_at,run_id);
CREATE INDEX workflow_runs_recovery_idx ON workflow_runs(status,phase,updated_at,run_id) WHERE status='active';

CREATE TABLE workflow_state_occurrences (
  entry_id TEXT PRIMARY KEY CHECK (length(entry_id)=36),
  run_id TEXT NOT NULL REFERENCES workflow_runs(run_id) ON DELETE CASCADE,
  state_id TEXT NOT NULL CHECK (length(state_id)=36),
  ordinal INTEGER NOT NULL CHECK (ordinal >= 1),
  status TEXT NOT NULL CHECK (status IN ('active','transitioning','closed')),
  entered_at TEXT NOT NULL,
  closed_at TEXT,
  UNIQUE(run_id,ordinal),
  UNIQUE(run_id,entry_id),
  CHECK ((status='closed') = (closed_at IS NOT NULL))
);
CREATE UNIQUE INDEX workflow_occurrences_one_current_idx ON workflow_state_occurrences(run_id) WHERE status IN ('active','transitioning');
CREATE INDEX workflow_occurrences_run_idx ON workflow_state_occurrences(run_id,ordinal);

CREATE TABLE workflow_entry_notifications (
  notification_id TEXT PRIMARY KEY CHECK (length(notification_id)=36),
  run_id TEXT NOT NULL REFERENCES workflow_runs(run_id) ON DELETE CASCADE,
  entry_id TEXT NOT NULL UNIQUE REFERENCES workflow_state_occurrences(entry_id) ON DELETE CASCADE,
  message_id TEXT UNIQUE REFERENCES messages(id) ON DELETE SET NULL,
  message_body TEXT NOT NULL CHECK (length(CAST(message_body AS BLOB)) <= 65536),
  delivery_state TEXT NOT NULL CHECK (delivery_state IN ('queued','offered','accepted','failed','cancelled')),
  wake_state TEXT NOT NULL CHECK (wake_state IN ('pending','accepted','failed','cancelled')),
  offer_attempt INTEGER NOT NULL DEFAULT 0 CHECK (offer_attempt BETWEEN 0 AND 100),
  next_attempt_at TEXT,
  wake_accepted_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX workflow_entry_notifications_recovery_idx ON workflow_entry_notifications(delivery_state,wake_state,next_attempt_at,created_at) WHERE wake_state='pending';

CREATE TABLE workflow_pending_transitions (
  transition_id TEXT PRIMARY KEY CHECK (length(transition_id)=36),
  run_id TEXT NOT NULL REFERENCES workflow_runs(run_id) ON DELETE CASCADE,
  entry_id TEXT NOT NULL UNIQUE REFERENCES workflow_state_occurrences(entry_id) ON DELETE CASCADE,
  control_request_id TEXT UNIQUE REFERENCES managed_control_requests(request_id) ON DELETE RESTRICT,
  exit_name TEXT NOT NULL CHECK (length(CAST(exit_name AS BLOB)) BETWEEN 1 AND 256),
  target_kind TEXT NOT NULL CHECK (target_kind IN ('state','complete')),
  target_state_id TEXT,
  note TEXT CHECK (note IS NULL OR length(CAST(note AS BLOB)) <= 4096),
  phase TEXT NOT NULL CHECK (phase IN ('admitted','processing','completed','failed')),
  outcome TEXT,
  admitted_at TEXT NOT NULL,
  completed_at TEXT,
  updated_at TEXT NOT NULL,
  CHECK ((target_kind='state') = (target_state_id IS NOT NULL)),
  CHECK ((phase IN ('completed','failed')) = (completed_at IS NOT NULL))
);
CREATE UNIQUE INDEX workflow_transitions_one_nonterminal_per_run_idx ON workflow_pending_transitions(run_id) WHERE phase IN ('admitted','processing');
CREATE INDEX workflow_transitions_recovery_idx ON workflow_pending_transitions(phase,updated_at,transition_id) WHERE phase IN ('admitted','processing');

CREATE TABLE workflow_journal_events (
  run_id TEXT NOT NULL REFERENCES workflow_runs(run_id) ON DELETE CASCADE,
  sequence INTEGER NOT NULL CHECK (sequence >= 1),
  semantic_key TEXT NOT NULL,
  event_type TEXT NOT NULL CHECK (event_type IN ('run_launched','state_entered','transition_requested','transition_completed','run_completed','run_cancelled','run_failed','run_blocked','run_recovered')),
  occurred_at TEXT NOT NULL,
  state_id TEXT,
  entry_id TEXT,
  exit_name TEXT,
  note TEXT CHECK (note IS NULL OR length(CAST(note AS BLOB)) <= 4096),
  safe_outcome_json TEXT CHECK (safe_outcome_json IS NULL OR (json_valid(safe_outcome_json) AND json_type(safe_outcome_json)='object' AND length(CAST(safe_outcome_json AS BLOB)) <= 16384)),
  PRIMARY KEY(run_id,sequence),
  UNIQUE(run_id,semantic_key)
);
CREATE INDEX workflow_journal_events_order_idx ON workflow_journal_events(run_id,sequence);

CREATE TABLE workflow_http_mutations (
  method TEXT NOT NULL,
  request_id TEXT NOT NULL CHECK (length(request_id)=36),
  canonical_path TEXT NOT NULL,
  canonical_body_hash TEXT NOT NULL CHECK (length(canonical_body_hash)=64 AND canonical_body_hash=lower(canonical_body_hash) AND canonical_body_hash NOT GLOB '*[^0-9a-f]*'),
  response_status INTEGER NOT NULL CHECK (response_status BETWEEN 200 AND 599),
  response_body BLOB NOT NULL CHECK (length(response_body) <= 65536),
  created_at TEXT NOT NULL,
  PRIMARY KEY(method,request_id)
);
CREATE INDEX workflow_http_mutations_created_idx ON workflow_http_mutations(created_at,method,request_id);
