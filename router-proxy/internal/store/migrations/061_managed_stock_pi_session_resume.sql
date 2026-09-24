CREATE TABLE managed_stock_pi_session_reservations (
  reservation_id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL REFERENCES managed_agents(agent_id),
  session_epoch INTEGER NOT NULL CHECK (session_epoch >= 1),
  launch_operation_id TEXT NOT NULL CHECK (length(CAST(launch_operation_id AS BLOB)) BETWEEN 1 AND 256),
  root_relative_path TEXT NOT NULL CHECK (length(CAST(root_relative_path AS BLOB)) BETWEEN 1 AND 4096),
  path_sha256 TEXT NOT NULL CHECK (length(path_sha256) = 64),
  pi_session_id TEXT CHECK (pi_session_id IS NULL OR length(CAST(pi_session_id AS BLOB)) BETWEEN 1 AND 256),
  state TEXT NOT NULL CHECK (state IN ('reserved','bound','materialized','retired')),
  session_artifact_id TEXT REFERENCES managed_session_artifacts(artifact_id),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE (launch_operation_id),
  UNIQUE (agent_id, session_epoch),
  UNIQUE (path_sha256)
);

CREATE UNIQUE INDEX managed_stock_pi_session_id_idx
  ON managed_stock_pi_session_reservations(pi_session_id)
  WHERE pi_session_id IS NOT NULL;

ALTER TABLE managed_orderly_stop_markers
  ADD COLUMN resume_artifact_id TEXT REFERENCES managed_session_artifacts(artifact_id);
ALTER TABLE managed_root_recovery_actions
  ADD COLUMN resume_artifact_id TEXT REFERENCES managed_session_artifacts(artifact_id);
