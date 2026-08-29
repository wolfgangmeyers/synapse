DROP INDEX IF EXISTS reminders_linked_message_idx;

CREATE INDEX reminders_linked_message_idx
  ON reminders (linked_message_id)
  WHERE linked_message_id IS NOT NULL;
