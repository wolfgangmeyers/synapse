-- Add System 1 (Clef) configuration columns to managed_settings
ALTER TABLE managed_settings ADD COLUMN system_one_host TEXT;
ALTER TABLE managed_settings ADD COLUMN system_one_port INTEGER;
ALTER TABLE managed_settings ADD COLUMN system_one_enabled INTEGER NOT NULL DEFAULT 0 CHECK (system_one_enabled IN (0, 1));
