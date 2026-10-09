-- Add worktree support columns to managed_agents
ALTER TABLE managed_agents ADD COLUMN worktree INTEGER NOT NULL DEFAULT 0;
ALTER TABLE managed_agents ADD COLUMN worktree_base_path TEXT;
ALTER TABLE managed_agents ADD COLUMN worktree_branch TEXT;
