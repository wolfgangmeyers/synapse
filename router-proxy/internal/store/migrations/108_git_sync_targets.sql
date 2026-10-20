-- Migration 108: git sync targets
CREATE TABLE IF NOT EXISTS git_sync_targets (
    target TEXT PRIMARY KEY,
    repo_url TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
