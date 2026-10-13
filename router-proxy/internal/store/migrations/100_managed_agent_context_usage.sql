-- Add context usage tracking columns to managed_agents
ALTER TABLE managed_agents ADD COLUMN context_tokens INTEGER;
ALTER TABLE managed_agents ADD COLUMN context_window INTEGER;
ALTER TABLE managed_agents ADD COLUMN context_percent REAL;
