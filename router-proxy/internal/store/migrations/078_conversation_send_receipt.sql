-- Conversation history is bounded and deliveries are deleted on ACK. A send
-- receipt is neither: it keeps an accepted ID immutable across both events.
CREATE TABLE conversation_send_receipts (
 message_id TEXT PRIMARY KEY,
 conversation_json TEXT NOT NULL,
 delivery_json TEXT NOT NULL
);
