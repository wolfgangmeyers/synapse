-- Add context threshold configuration columns to managed_settings
ALTER TABLE managed_settings ADD COLUMN context_warning_percent INTEGER NOT NULL DEFAULT 10;
ALTER TABLE managed_settings ADD COLUMN context_reset_percent INTEGER NOT NULL DEFAULT 20;
ALTER TABLE managed_settings ADD COLUMN context_model_thresholds_json TEXT NOT NULL DEFAULT '{}';
