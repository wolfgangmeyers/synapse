-- Preserve transcript bytes/digests. Legacy SDK timestamps are not trusted
-- router admission authority; migration time is assigned exactly once.
ALTER TABLE embedded_entries ADD COLUMN retention_admitted_at_ms INTEGER NOT NULL DEFAULT 0 CHECK(retention_admitted_at_ms>=0);
UPDATE embedded_entries SET retention_admitted_at_ms=CAST((julianday('now')-2440587.5)*86400000 AS INTEGER);
ALTER TABLE embedded_sessions ADD COLUMN evidence_expired_through_ms INTEGER NOT NULL DEFAULT 0 CHECK(evidence_expired_through_ms>=0);

CREATE TRIGGER embedded_retention_entry_stamp AFTER INSERT ON embedded_entries
WHEN NEW.retention_admitted_at_ms=0
BEGIN
 UPDATE embedded_entries SET retention_admitted_at_ms=CAST((julianday('now')-2440587.5)*86400000 AS INTEGER)
 WHERE agent_id=NEW.agent_id AND session_id=NEW.session_id AND epoch=NEW.epoch AND seq=NEW.seq;
END;
CREATE TRIGGER embedded_retention_entry_time_immutable BEFORE UPDATE OF retention_admitted_at_ms ON embedded_entries
WHEN OLD.retention_admitted_at_ms<>0 AND NEW.retention_admitted_at_ms<>OLD.retention_admitted_at_ms
BEGIN SELECT RAISE(ABORT,'retention admission time is immutable'); END;
CREATE TRIGGER embedded_retention_entry_no_replace BEFORE INSERT ON embedded_entries
WHEN EXISTS(SELECT 1 FROM embedded_entries WHERE agent_id=NEW.agent_id AND session_id=NEW.session_id AND epoch=NEW.epoch AND seq=NEW.seq)
BEGIN SELECT RAISE(ABORT,'retention entry replacement is forbidden'); END;
CREATE TRIGGER embedded_retention_cutoff_monotonic BEFORE UPDATE OF evidence_expired_through_ms ON embedded_sessions
WHEN NEW.evidence_expired_through_ms<OLD.evidence_expired_through_ms
BEGIN SELECT RAISE(ABORT,'retention cutoff is monotonic'); END;
CREATE TRIGGER embedded_retention_session_no_replace BEFORE INSERT ON embedded_sessions
WHEN EXISTS(SELECT 1 FROM embedded_sessions WHERE agent_id=NEW.agent_id AND session_id=NEW.session_id AND epoch=NEW.epoch AND generation=NEW.generation)
BEGIN SELECT RAISE(ABORT,'retention session replacement is forbidden'); END;
