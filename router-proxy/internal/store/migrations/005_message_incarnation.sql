ALTER TABLE messages ADD COLUMN message_incarnation_id TEXT;

CREATE UNIQUE INDEX messages_message_incarnation_id_idx
  ON messages (message_incarnation_id);
