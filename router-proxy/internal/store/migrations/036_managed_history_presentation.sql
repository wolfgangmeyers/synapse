-- Complete history needs a small, closed display projection. It keeps the UI
-- from reverse-engineering semantics from human text or receiving private
-- runtime detail. Older rows remain readable without this optional metadata.
ALTER TABLE managed_complete_history ADD COLUMN presentation_json TEXT;
