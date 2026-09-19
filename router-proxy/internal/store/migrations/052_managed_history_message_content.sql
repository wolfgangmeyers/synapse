-- Retain click-through details alongside the concise Complete-history event.
-- Incoming message bodies and sender identities are copied before the queue row
-- is deleted. Tool input is redacted before it crosses this persistence boundary.
ALTER TABLE managed_complete_history ADD COLUMN message_content TEXT;
ALTER TABLE managed_complete_history ADD COLUMN sender_identity TEXT;
ALTER TABLE managed_complete_history ADD COLUMN tool_input_json TEXT;
