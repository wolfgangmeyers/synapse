-- Migration 113: Managed Agent Blocked State
ALTER TABLE managed_agents ADD COLUMN blocked INTEGER NOT NULL DEFAULT 0;
ALTER TABLE managed_agents ADD COLUMN blocked_reason TEXT;
ALTER TABLE managed_agents ADD COLUMN blocked_at TIMESTAMP;
