-- One bit per existing binding, retained/deleted with embedded session history.
-- No transcript format change or independent accumulating lifecycle ledger.
ALTER TABLE embedded_sessions ADD COLUMN claude_started INTEGER NOT NULL DEFAULT 0 CHECK(claude_started IN (0,1));
