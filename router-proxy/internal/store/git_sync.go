package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// GitSyncTarget represents a configured git repository sync target.
type GitSyncTarget struct {
	Target    string `json:"target"`
	RepoURL   string `json:"repo_url"`
	UpdatedAt string `json:"updated_at"`
}

// GetGitSyncTarget returns the configured git sync target by name, or nil if not found.
func (s *Store) GetGitSyncTarget(ctx context.Context, target string) (*GitSyncTarget, error) {
	var row GitSyncTarget
	err := s.db.QueryRowContext(ctx, `SELECT target, repo_url, updated_at FROM git_sync_targets WHERE target = ?`, target).Scan(&row.Target, &row.RepoURL, &row.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// SetGitSyncTarget updates, inserts, or clears a git sync target.
// When repoURL is empty, it removes the sync target.
func (s *Store) SetGitSyncTarget(ctx context.Context, target string, repoURL string) (*GitSyncTarget, error) {
	target = strings.TrimSpace(target)
	repoURL = strings.TrimSpace(repoURL)
	if target == "" {
		return nil, errors.New("target is required")
	}
	if repoURL == "" {
		if err := s.DeleteGitSyncTarget(ctx, target); err != nil {
			return nil, err
		}
		return &GitSyncTarget{
			Target:    target,
			RepoURL:   "",
			UpdatedAt: formatTimestamp(s.clock.Now()),
		}, nil
	}

	updatedAt := formatTimestamp(s.clock.Now())
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO git_sync_targets (target, repo_url, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(target) DO UPDATE SET repo_url = excluded.repo_url, updated_at = excluded.updated_at
	`, target, repoURL, updatedAt)
	if err != nil {
		return nil, err
	}

	return &GitSyncTarget{
		Target:    target,
		RepoURL:   repoURL,
		UpdatedAt: updatedAt,
	}, nil
}

// DeleteGitSyncTarget deletes a configured git sync target by name.
func (s *Store) DeleteGitSyncTarget(ctx context.Context, target string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return errors.New("target is required")
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM git_sync_targets WHERE target = ?`, target)
	return err
}

// ReplaceAllPromptTemplates atomically replaces all existing prompt templates with the given slice.
func (s *Store) ReplaceAllPromptTemplates(ctx context.Context, templates []PromptTemplate) error {
	return s.runImmediate(ctx, func() error {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM prompt_templates`); err != nil {
			return fmt.Errorf("delete prompt_templates: %w", err)
		}

		for _, item := range templates {
			if strings.TrimSpace(item.ID) == "" {
				return errors.New("template id cannot be empty")
			}
			if strings.TrimSpace(item.Name) == "" {
				return errors.New("template name cannot be empty")
			}
			createdAt := item.CreatedAt
			if createdAt == "" {
				createdAt = formatTimestamp(s.clock.Now())
			}
			updatedAt := item.UpdatedAt
			if updatedAt == "" {
				updatedAt = createdAt
			}
			_, err := s.db.ExecContext(ctx, `
				INSERT INTO prompt_templates (id, name, body, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?)
			`, item.ID, item.Name, item.Body, createdAt, updatedAt)
			if err != nil {
				return fmt.Errorf("insert prompt_template %s: %w", item.ID, err)
			}
		}
		return nil
	})
}
