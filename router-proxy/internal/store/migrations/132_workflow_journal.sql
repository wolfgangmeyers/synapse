CREATE TABLE workflow_journal_runs (
 id TEXT PRIMARY KEY, agent_id TEXT NOT NULL REFERENCES managed_agents(agent_id) ON DELETE CASCADE,
 agent_name TEXT NOT NULL, workflow_id TEXT NOT NULL DEFAULT '', workflow_name TEXT NOT NULL DEFAULT '',
 initial_state_id TEXT NOT NULL, initial_state_name TEXT NOT NULL,
 started_at TEXT NOT NULL, terminal_at TEXT, status TEXT NOT NULL CHECK(status IN ('active','terminal')),
 outcome TEXT NOT NULL DEFAULT '', synthetic INTEGER NOT NULL DEFAULT 0,
 incomplete INTEGER NOT NULL DEFAULT 0, cutoff_sequence INTEGER NOT NULL DEFAULT 0,
 current_visit_id TEXT NOT NULL DEFAULT '', current_visit_sequence INTEGER NOT NULL DEFAULT 0,
 current_visit_at TEXT NOT NULL DEFAULT '', current_workflow_id TEXT NOT NULL DEFAULT '', current_workflow_name TEXT NOT NULL DEFAULT '',
 current_state_id TEXT NOT NULL DEFAULT '', current_state_name TEXT NOT NULL DEFAULT '',
 next_sequence INTEGER NOT NULL DEFAULT 1
);
CREATE UNIQUE INDEX workflow_journal_one_active ON workflow_journal_runs(agent_id) WHERE status='active';
CREATE INDEX workflow_journal_agent_order ON workflow_journal_runs(agent_id, started_at DESC, id DESC);
CREATE INDEX workflow_journal_terminal ON workflow_journal_runs(status, terminal_at, id);
CREATE TABLE workflow_journal_events (
 id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES workflow_journal_runs(id) ON DELETE CASCADE,
 sequence INTEGER NOT NULL, kind TEXT NOT NULL, occurred_at TEXT NOT NULL,
 workflow_id TEXT NOT NULL DEFAULT '', workflow_name TEXT NOT NULL DEFAULT '',
 state_id TEXT NOT NULL DEFAULT '', state_name TEXT NOT NULL DEFAULT '',
 reason TEXT NOT NULL DEFAULT '', source TEXT NOT NULL DEFAULT '', text TEXT NOT NULL DEFAULT '',
 target_event_id TEXT NOT NULL DEFAULT '', UNIQUE(run_id, sequence)
);
CREATE TABLE workflow_journal_retention (id INTEGER PRIMARY KEY CHECK(id=1), cutoff TEXT NOT NULL);
CREATE TABLE workflow_journal_receipts (
 agent_id TEXT NOT NULL, kind TEXT NOT NULL, operation_id TEXT NOT NULL,
 fingerprint TEXT NOT NULL, result TEXT NOT NULL, expires_at TEXT NOT NULL,
 bytes INTEGER NOT NULL, PRIMARY KEY(agent_id,kind,operation_id)
);
CREATE INDEX workflow_journal_receipts_expiry ON workflow_journal_receipts(expires_at);
