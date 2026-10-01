-- Preserve the lease incarnation that registered an SDK intent. Preexisting
-- unresolved intents have no owner evidence and cannot authorize a first effect.
ALTER TABLE embedded_effects ADD COLUMN reminder_owner_token INTEGER CHECK(reminder_owner_token > 0);
CREATE TRIGGER embedded_effect_reminder_owner_immutable BEFORE UPDATE OF reminder_owner_token ON embedded_effects
BEGIN SELECT RAISE(ABORT,'reminder effect owner immutable'); END;
