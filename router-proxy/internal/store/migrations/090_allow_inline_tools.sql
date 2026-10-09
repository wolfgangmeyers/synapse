-- Add allow_inline_tools column to managed_agents
ALTER TABLE managed_agents ADD COLUMN allow_inline_tools INTEGER NOT NULL DEFAULT 0;
