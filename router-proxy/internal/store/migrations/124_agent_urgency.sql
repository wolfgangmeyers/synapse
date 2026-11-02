-- One current value, retained only for the existing shared identity lifetime.
-- Available final-deletion evidence initializes validity only at upgrade;
-- serialized admission/retirement controls it afterward, not audit retention.
ALTER TABLE agents ADD COLUMN identity_active INTEGER NOT NULL DEFAULT 1 CHECK (identity_active IN (0,1));
ALTER TABLE agents ADD COLUMN urgent INTEGER NOT NULL DEFAULT 0 CHECK (urgent IN (0,1));
INSERT INTO agents (agent_id, first_seen_at)
SELECT agent_id, created_at FROM managed_agents WHERE 1
ON CONFLICT(agent_id) DO NOTHING;

-- Preserve extant managed admissions and shared identities created or genuinely
-- connected after deletion. Pre-feature expired evidence cannot reconstruct a
-- retired lifetime; ambiguous rows keep the compatibility default above.
UPDATE agents SET identity_active=0
WHERE NOT EXISTS (SELECT 1 FROM managed_agents m WHERE m.agent_id=agents.agent_id)
AND (
    EXISTS (
        SELECT 1 FROM managed_lifecycle_actions a
        WHERE a.agent_id=agents.agent_id AND a.kind='terminate'
          AND a.state='succeeded' AND a.stage='complete'
          AND a.terminal_code='TERMINATED'
          AND julianday(agents.first_seen_at)<=julianday(a.completed_at)
          AND (agents.last_connected_at IS NULL
               OR julianday(agents.last_connected_at)<=julianday(a.completed_at))
    )
    OR EXISTS (
        SELECT 1 FROM managed_agent_archives a
        WHERE a.agent_id=agents.agent_id
          AND julianday(agents.first_seen_at)<=julianday(a.completed_at)
          AND (agents.last_connected_at IS NULL
               OR julianday(agents.last_connected_at)<=julianday(a.completed_at))
    )
);
