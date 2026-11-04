-- Shares message expiry and deletion; this bit is not a consumption receipt.
ALTER TABLE messages ADD COLUMN claude_stdin_started INTEGER NOT NULL DEFAULT 0 CHECK(claude_stdin_started IN (0,1));
