ALTER TABLE messages ADD COLUMN source_kind TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE conversation_messages ADD COLUMN source_kind TEXT NOT NULL DEFAULT 'unknown';
