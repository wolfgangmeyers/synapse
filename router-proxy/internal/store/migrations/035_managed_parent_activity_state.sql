-- Parent activity and parent compaction share Pi's one ordered stdout stream.
-- Retain the active bit beside compaction so a delayed ordinary turn event
-- cannot clear a later compaction (or the reverse) in the public projection.
ALTER TABLE managed_parent_compaction_state
  ADD COLUMN active INTEGER NOT NULL DEFAULT 0 CHECK (active IN (0,1));
