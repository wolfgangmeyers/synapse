-- Keep goal revisions after the expiring desired row is consumed or pruned.
-- A new control must never reuse a revision that a reconnecting client may have applied.
CREATE TABLE goal_control_revision_history (
  agent_id TEXT NOT NULL,
  instance_id TEXT NOT NULL,
  revision INTEGER NOT NULL CHECK (revision >= 1),
  PRIMARY KEY (agent_id, instance_id)
);

INSERT INTO goal_control_revision_history (agent_id, instance_id, revision)
SELECT agent_id, instance_id, revision
FROM goal_control_desired_state;
