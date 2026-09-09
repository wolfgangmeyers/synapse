-- Keep bounded, code-only evidence when Abort cannot complete. The terminate
-- action remains after final cleanup and retention owns eventual pruning.
CREATE TABLE managed_termination_diagnostics (
  action_id TEXT NOT NULL REFERENCES managed_lifecycle_actions(action_id) ON DELETE CASCADE,
  code TEXT NOT NULL CHECK (code IN ('ABORT_TIMEOUT','ABORT_EOF','ABORT_MALFORMED','ABORT_ERROR','ABORT_UNAVAILABLE','STATE_DISCOVERY_FAILED')),
  recorded_at TEXT NOT NULL,
  PRIMARY KEY (action_id, code)
);
