CREATE TABLE conversation_messages (
  conversation_sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  message_id TEXT NOT NULL,
  message_incarnation_id TEXT NOT NULL,
  delivery_message_id TEXT NOT NULL,
  sender TEXT NOT NULL,
  recipient TEXT NOT NULL,
  participant_a TEXT NOT NULL,
  participant_b TEXT NOT NULL,
  subject TEXT,
  body TEXT NOT NULL,
  created_at TEXT NOT NULL,
  recorded_at TEXT NOT NULL,
  attachments_json TEXT NOT NULL DEFAULT '[]',
  UNIQUE (message_id, message_incarnation_id)
);

CREATE INDEX conversation_messages_pair_sequence_idx
  ON conversation_messages (participant_a, participant_b, conversation_sequence);

CREATE INDEX conversation_messages_recorded_at_idx
  ON conversation_messages (recorded_at);
