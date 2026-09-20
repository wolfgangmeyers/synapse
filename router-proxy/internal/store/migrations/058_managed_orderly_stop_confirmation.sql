-- Keep orderly-stop intent until the router proves Process.Stop succeeded.
-- Existing markers are intentionally unconfirmed when this column is added.
ALTER TABLE managed_orderly_stop_markers
  ADD COLUMN confirmed INTEGER NOT NULL DEFAULT 0 CHECK (confirmed IN (0,1));
