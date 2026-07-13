CREATE TABLE reminders (
  id TEXT PRIMARY KEY,
  target TEXT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
  creator TEXT NOT NULL,
  subject TEXT,
  body TEXT NOT NULL,
  created_at TEXT NOT NULL,
  next_due_at TEXT NOT NULL,
  interval_ms INTEGER CHECK (interval_ms IS NULL OR interval_ms > 0),
  state TEXT NOT NULL CHECK (state IN (
    'scheduled',
    'due',
    'message_queued',
    'acknowledged',
    'canceled',
    'failed'
  )),
  linked_message_id TEXT,
  occurrence_number INTEGER NOT NULL CHECK (occurrence_number >= 0),
  attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  last_attempt_at TEXT
);

CREATE INDEX reminders_due_state_idx
  ON reminders (state, next_due_at);

CREATE INDEX reminders_target_state_idx
  ON reminders (target, state);

CREATE UNIQUE INDEX reminders_linked_message_idx
  ON reminders (linked_message_id)
  WHERE linked_message_id IS NOT NULL;
