-- One durable current source record exists for every managed agent. It stores the
-- router-owned desired policy and the confirmations that fence a launch ready.
-- This is current state, not a history ledger; terminate owns its later deletion.
CREATE TABLE managed_readiness_sources (
  agent_id TEXT PRIMARY KEY REFERENCES managed_agents(agent_id),
  desired_policy_canonical BLOB NOT NULL
    CHECK (length(desired_policy_canonical) BETWEEN 2 AND 32768),
  desired_policy_sha256 TEXT NOT NULL
    CHECK (length(desired_policy_sha256) = 64
      AND desired_policy_sha256 = lower(desired_policy_sha256)
      AND desired_policy_sha256 NOT GLOB '*[^0-9a-f]*'),

  pi_session_id TEXT,
  session_epoch INTEGER,
  process_pid INTEGER,
  process_start_marker TEXT,
  process_incarnation TEXT,
  runtime_generation TEXT,
  manifest_revision INTEGER,
  manifest_hash TEXT,

  applied_policy_canonical BLOB,
  applied_policy_sha256 TEXT,
  policy_confirmed_at TEXT,

  snapshot_id TEXT,
  snapshot_owner_session_id TEXT,
  snapshot_sequence INTEGER,
  snapshot_canonical BLOB,
  snapshot_sha256 TEXT,
  snapshot_confirmed_at TEXT,

  CHECK (
    (pi_session_id IS NULL AND session_epoch IS NULL AND process_pid IS NULL
      AND process_start_marker IS NULL AND process_incarnation IS NULL
      AND runtime_generation IS NULL AND manifest_revision IS NULL AND manifest_hash IS NULL)
    OR
    (length(CAST(pi_session_id AS BLOB)) > 0 AND session_epoch >= 1 AND process_pid > 0
      AND length(CAST(process_start_marker AS BLOB)) > 0
      AND length(CAST(process_incarnation AS BLOB)) > 0
      AND length(CAST(runtime_generation AS BLOB)) > 0
      AND manifest_revision >= 1
      AND length(manifest_hash) = 64 AND manifest_hash = lower(manifest_hash)
      AND manifest_hash NOT GLOB '*[^0-9a-f]*')
  ),
  CHECK (
    (applied_policy_canonical IS NULL AND applied_policy_sha256 IS NULL AND policy_confirmed_at IS NULL)
    OR
    (applied_policy_canonical IS NOT NULL AND length(applied_policy_canonical) BETWEEN 2 AND 32768
      AND length(applied_policy_sha256) = 64 AND applied_policy_sha256 = lower(applied_policy_sha256)
      AND applied_policy_sha256 NOT GLOB '*[^0-9a-f]*' AND policy_confirmed_at IS NOT NULL)
  ),
  CHECK (
    (snapshot_id IS NULL AND snapshot_owner_session_id IS NULL AND snapshot_sequence IS NULL
      AND snapshot_canonical IS NULL AND snapshot_sha256 IS NULL AND snapshot_confirmed_at IS NULL)
    OR
    (length(CAST(snapshot_id AS BLOB)) BETWEEN 1 AND 8192
      AND length(CAST(snapshot_owner_session_id AS BLOB)) BETWEEN 1 AND 8192
      AND snapshot_sequence >= 0 AND length(snapshot_canonical) BETWEEN 2 AND 32768
      AND length(snapshot_sha256) = 64 AND snapshot_sha256 = lower(snapshot_sha256)
      AND snapshot_sha256 NOT GLOB '*[^0-9a-f]*' AND snapshot_confirmed_at IS NOT NULL
      AND applied_policy_canonical IS NOT NULL AND applied_policy_sha256 IS NOT NULL
      AND policy_confirmed_at IS NOT NULL)
  )
);

-- Existing managed rows predate source confirmation. They receive the same
-- unbound default record as a new reservation and remain not-ready until a
-- future binding rolls and confirms this row.
INSERT INTO managed_readiness_sources (
  agent_id, desired_policy_canonical, desired_policy_sha256
)
SELECT agent_id,
  CAST('{"compaction":{"deferred":false},"goal_lock":{"enabled":false},"inline":{"credits":{},"enabled":false},"recall":{"content":""},"settled":{"enabled":false}}' AS BLOB),
  'dcb17869020df00dd27a11e14cc0facada376f729a13002185a3d4b87f3c3648'
FROM managed_agents
WHERE NOT EXISTS (
  SELECT 1 FROM managed_readiness_sources source WHERE source.agent_id = managed_agents.agent_id
);
