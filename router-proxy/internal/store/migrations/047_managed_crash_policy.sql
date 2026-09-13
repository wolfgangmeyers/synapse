-- Keep the complete crash policy in the existing one-row-per-agent record.
-- The bounded JSON history needs three timestamps at most; ready_since tracks
-- the continuous healthy interval across router restarts.
ALTER TABLE managed_crash_recovery ADD COLUMN crash_history TEXT NOT NULL DEFAULT '[]';
ALTER TABLE managed_crash_recovery ADD COLUMN ready_since TEXT;
