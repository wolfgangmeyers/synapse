-- Claim before a summarizer request. A stranded claim is unknown, never replayable.
ALTER TABLE embedded_own_compaction_requests ADD COLUMN claimed INTEGER NOT NULL DEFAULT 0 CHECK(claimed IN (0,1));
