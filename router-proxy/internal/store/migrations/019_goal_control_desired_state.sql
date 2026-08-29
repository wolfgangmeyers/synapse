-- Goal controls can outlive a WebSocket reconnect, but only for the same
-- process incarnation. Expiry bounds abandoned controls without carrying one
-- into a replacement worker.
CREATE TABLE goal_control_desired_state (
  agent_id TEXT NOT NULL,
  instance_id TEXT NOT NULL,
  revision INTEGER NOT NULL CHECK (revision >= 1),
  config_json TEXT NOT NULL CHECK (length(config_json) <= 16384),
  updated_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  PRIMARY KEY (agent_id, instance_id)
);

CREATE INDEX idx_goal_control_desired_state_expires_at
  ON goal_control_desired_state (expires_at);
