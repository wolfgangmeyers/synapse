package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	DefaultCustomModelContextWindow = 131072
	DefaultCustomModelMaxTokens     = 8192
)

// CustomProviderRecord represents an OpenAI-compatible custom model provider.
type CustomProviderRecord struct {
	ID                string    `json:"id"`
	Label             string    `json:"label"`
	BaseURL           string    `json:"base_url"`
	Protocol          string    `json:"protocol"`
	APIKeyCiphertext  *string   `json:"api_key_ciphertext,omitempty"`
	Enabled           bool      `json:"enabled"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// CustomProviderModelRecord represents a model exposed by a custom provider.
type CustomProviderModelRecord struct {
	ID            string    `json:"id"`
	ProviderID    string    `json:"provider_id"`
	DisplayName   string    `json:"display_name"`
	ContextWindow int       `json:"context_window"`
	MaxTokens     int       `json:"max_tokens"`
	SupportsTools bool      `json:"supports_tools"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// CustomProviderWithModels holds a custom provider and its configured models.
type CustomProviderWithModels struct {
	Provider CustomProviderRecord        `json:"provider"`
	Models   []CustomProviderModelRecord `json:"models"`
}

var (
	ErrCustomProviderNotFound = errors.New("custom provider not found")
)

// CreateCustomProvider inserts a new custom provider record along with its models.
func (s *Store) CreateCustomProvider(ctx context.Context, provider CustomProviderRecord, models []CustomProviderModelRecord) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	provider.ID = strings.TrimSpace(provider.ID)
	provider.Label = strings.TrimSpace(provider.Label)
	provider.BaseURL = strings.TrimSpace(provider.BaseURL)
	if provider.ID == "" {
		return errors.New("custom provider id is required")
	}
	if len(provider.ID) > 64 {
		return errors.New("custom provider id must not exceed 64 characters")
	}
	if provider.Label == "" {
		return errors.New("custom provider label is required")
	}
	if len(provider.Label) > 128 {
		return errors.New("custom provider label must not exceed 128 characters")
	}
	if provider.BaseURL == "" {
		return errors.New("custom provider base_url is required")
	}
	if len(provider.BaseURL) > 1024 {
		return errors.New("custom provider base_url must not exceed 1024 characters")
	}
	if provider.Protocol == "" {
		provider.Protocol = "openai-compatible"
	}
	if len(provider.Protocol) > 64 {
		return errors.New("custom provider protocol must not exceed 64 characters")
	}

	now := s.clock.Now().UTC()
	if provider.CreatedAt.IsZero() {
		provider.CreatedAt = now
	}
	if provider.UpdatedAt.IsZero() {
		provider.UpdatedAt = now
	}

	enabledInt := 0
	if provider.Enabled {
		enabledInt = 1
	}

	createdStr := formatTimestamp(provider.CreatedAt)
	updatedStr := formatTimestamp(provider.UpdatedAt)

	return s.runImmediate(ctx, func() error {
		_, err := s.db.ExecContext(ctx, `
INSERT INTO custom_providers (
  id, label, base_url, protocol, api_key_ciphertext, enabled, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			provider.ID,
			provider.Label,
			provider.BaseURL,
			provider.Protocol,
			provider.APIKeyCiphertext,
			enabledInt,
			createdStr,
			updatedStr,
		)
		if err != nil {
			return fmt.Errorf("insert custom provider: %w", err)
		}

		for _, m := range models {
			m.ID = strings.TrimSpace(m.ID)
			if m.ID == "" {
				return errors.New("custom provider model id is required")
			}
			m.ProviderID = provider.ID
			m.DisplayName = strings.TrimSpace(m.DisplayName)
			if m.DisplayName == "" {
				m.DisplayName = m.ID
			}
			if m.ContextWindow <= 0 {
				m.ContextWindow = DefaultCustomModelContextWindow
			}
			if m.MaxTokens <= 0 {
				m.MaxTokens = DefaultCustomModelMaxTokens
			}
			supportsToolsInt := 0
			if m.SupportsTools {
				supportsToolsInt = 1
			}
			modelCreated := m.CreatedAt
			if modelCreated.IsZero() {
				modelCreated = now
			}
			modelUpdated := m.UpdatedAt
			if modelUpdated.IsZero() {
				modelUpdated = now
			}

			_, err := s.db.ExecContext(ctx, `
INSERT INTO custom_provider_models (
  id, provider_id, display_name, context_window, max_tokens, supports_tools, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				m.ID,
				m.ProviderID,
				m.DisplayName,
				m.ContextWindow,
				m.MaxTokens,
				supportsToolsInt,
				formatTimestamp(modelCreated),
				formatTimestamp(modelUpdated),
			)
			if err != nil {
				return fmt.Errorf("insert custom provider model %q: %w", m.ID, err)
			}
		}

		return nil
	})
}

// GetCustomProvider retrieves a custom provider and its configured models by ID.
func (s *Store) GetCustomProvider(ctx context.Context, id string) (*CustomProviderWithModels, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("custom provider id is required")
	}

	var p CustomProviderRecord
	var enabledInt int
	var createdStr, updatedStr string
	err := s.db.QueryRowContext(ctx, `
SELECT id, label, base_url, protocol, api_key_ciphertext, enabled, created_at, updated_at
FROM custom_providers
WHERE id = ?`, id).Scan(
		&p.ID,
		&p.Label,
		&p.BaseURL,
		&p.Protocol,
		&p.APIKeyCiphertext,
		&enabledInt,
		&createdStr,
		&updatedStr,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCustomProviderNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query custom provider: %w", err)
	}

	p.Enabled = (enabledInt == 1)
	p.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdStr)
	p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedStr)

	models, err := s.listCustomProviderModels(ctx, p.ID)
	if err != nil {
		return nil, err
	}

	return &CustomProviderWithModels{
		Provider: p,
		Models:   models,
	}, nil
}

func (s *Store) listCustomProviderModels(ctx context.Context, providerID string) ([]CustomProviderModelRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, provider_id, display_name, context_window, max_tokens, supports_tools, created_at, updated_at
FROM custom_provider_models
WHERE provider_id = ?
ORDER BY id ASC`, providerID)
	if err != nil {
		return nil, fmt.Errorf("query custom provider models: %w", err)
	}
	defer rows.Close()

	var models []CustomProviderModelRecord
	for rows.Next() {
		var m CustomProviderModelRecord
		var supportsToolsInt int
		var createdStr, updatedStr string
		if err := rows.Scan(
			&m.ID,
			&m.ProviderID,
			&m.DisplayName,
			&m.ContextWindow,
			&m.MaxTokens,
			&supportsToolsInt,
			&createdStr,
			&updatedStr,
		); err != nil {
			return nil, fmt.Errorf("scan custom provider model: %w", err)
		}
		m.SupportsTools = (supportsToolsInt == 1)
		m.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdStr)
		m.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedStr)
		models = append(models, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate custom provider models: %w", err)
	}
	return models, nil
}

// ListCustomProviders retrieves all custom providers with their configured models.
func (s *Store) ListCustomProviders(ctx context.Context) ([]*CustomProviderWithModels, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}

	rows, err := s.db.QueryContext(ctx, `
SELECT id, label, base_url, protocol, api_key_ciphertext, enabled, created_at, updated_at
FROM custom_providers
ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query custom providers: %w", err)
	}
	defer rows.Close()

	var providers []*CustomProviderWithModels
	for rows.Next() {
		var p CustomProviderRecord
		var enabledInt int
		var createdStr, updatedStr string
		if err := rows.Scan(
			&p.ID,
			&p.Label,
			&p.BaseURL,
			&p.Protocol,
			&p.APIKeyCiphertext,
			&enabledInt,
			&createdStr,
			&updatedStr,
		); err != nil {
			return nil, fmt.Errorf("scan custom provider: %w", err)
		}
		p.Enabled = (enabledInt == 1)
		p.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdStr)
		p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedStr)

		providers = append(providers, &CustomProviderWithModels{
			Provider: p,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate custom providers: %w", err)
	}

	for _, item := range providers {
		models, err := s.listCustomProviderModels(ctx, item.Provider.ID)
		if err != nil {
			return nil, err
		}
		item.Models = models
	}

	return providers, nil
}

// UpdateCustomProvider updates provider metadata and synchronizes its model set.
func (s *Store) UpdateCustomProvider(ctx context.Context, provider CustomProviderRecord, models []CustomProviderModelRecord) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	provider.ID = strings.TrimSpace(provider.ID)
	provider.Label = strings.TrimSpace(provider.Label)
	provider.BaseURL = strings.TrimSpace(provider.BaseURL)
	if provider.ID == "" {
		return errors.New("custom provider id is required")
	}
	if provider.Label == "" {
		return errors.New("custom provider label is required")
	}
	if len(provider.Label) > 128 {
		return errors.New("custom provider label must not exceed 128 characters")
	}
	if provider.BaseURL == "" {
		return errors.New("custom provider base_url is required")
	}
	if len(provider.BaseURL) > 1024 {
		return errors.New("custom provider base_url must not exceed 1024 characters")
	}
	if provider.Protocol == "" {
		provider.Protocol = "openai-compatible"
	}
	if len(provider.Protocol) > 64 {
		return errors.New("custom provider protocol must not exceed 64 characters")
	}

	now := s.clock.Now().UTC()
	provider.UpdatedAt = now

	enabledInt := 0
	if provider.Enabled {
		enabledInt = 1
	}

	updatedStr := formatTimestamp(provider.UpdatedAt)

	return s.runImmediate(ctx, func() error {
		res, err := s.db.ExecContext(ctx, `
UPDATE custom_providers SET
  label = ?,
  base_url = ?,
  protocol = ?,
  api_key_ciphertext = ?,
  enabled = ?,
  updated_at = ?
WHERE id = ?`,
			provider.Label,
			provider.BaseURL,
			provider.Protocol,
			provider.APIKeyCiphertext,
			enabledInt,
			updatedStr,
			provider.ID,
		)
		if err != nil {
			return fmt.Errorf("update custom provider: %w", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrCustomProviderNotFound
		}

		// Upsert provided models
		var modelIDs []string
		for _, m := range models {
			m.ID = strings.TrimSpace(m.ID)
			if m.ID == "" {
				return errors.New("custom provider model id is required")
			}
			m.ProviderID = provider.ID
			m.DisplayName = strings.TrimSpace(m.DisplayName)
			if m.DisplayName == "" {
				m.DisplayName = m.ID
			}
			if m.ContextWindow <= 0 {
				m.ContextWindow = DefaultCustomModelContextWindow
			}
			if m.MaxTokens <= 0 {
				m.MaxTokens = DefaultCustomModelMaxTokens
			}
			supportsToolsInt := 0
			if m.SupportsTools {
				supportsToolsInt = 1
			}
			modelCreated := m.CreatedAt
			if modelCreated.IsZero() {
				modelCreated = now
			}
			modelUpdated := m.UpdatedAt
			if modelUpdated.IsZero() {
				modelUpdated = now
			}

			_, err := s.db.ExecContext(ctx, `
INSERT INTO custom_provider_models (provider_id, id, display_name, context_window, max_tokens, supports_tools, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(provider_id, id) DO UPDATE SET
    display_name = excluded.display_name,
    context_window = excluded.context_window,
    max_tokens = excluded.max_tokens,
    supports_tools = excluded.supports_tools,
    updated_at = excluded.updated_at`,
				m.ProviderID,
				m.ID,
				m.DisplayName,
				m.ContextWindow,
				m.MaxTokens,
				supportsToolsInt,
				formatTimestamp(modelCreated),
				formatTimestamp(modelUpdated),
			)
			if err != nil {
				return fmt.Errorf("upsert custom provider model %q: %w", m.ID, err)
			}
			modelIDs = append(modelIDs, m.ID)
		}

		// Prune any models that were removed
		if len(modelIDs) > 0 {
			placeholders := make([]string, len(modelIDs))
			args := make([]any, 0, 1+len(modelIDs))
			args = append(args, provider.ID)
			for i, id := range modelIDs {
				placeholders[i] = "?"
				args = append(args, id)
			}
			query := fmt.Sprintf("DELETE FROM custom_provider_models WHERE provider_id = ? AND id NOT IN (%s)", strings.Join(placeholders, ", "))
			if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
				return fmt.Errorf("prune custom provider models: %w", err)
			}
		} else {
			if _, err := s.db.ExecContext(ctx, `DELETE FROM custom_provider_models WHERE provider_id = ?`, provider.ID); err != nil {
				return fmt.Errorf("clear custom provider models: %w", err)
			}
		}

		return nil
	})
}

// DeleteCustomProvider deletes a custom provider and cascades deletion to its models.
func (s *Store) DeleteCustomProvider(ctx context.Context, id string) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("custom provider id is required")
	}

	return s.runImmediate(ctx, func() error {
		res, err := s.db.ExecContext(ctx, `DELETE FROM custom_providers WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("delete custom provider: %w", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrCustomProviderNotFound
		}
		return nil
	})
}

// ListActiveCustomModels returns all models belonging to enabled custom providers.
func (s *Store) ListActiveCustomModels(ctx context.Context) ([]CustomProviderModelRecord, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}

	rows, err := s.db.QueryContext(ctx, `
SELECT m.id, m.provider_id, m.display_name, m.context_window, m.max_tokens, m.supports_tools, m.created_at, m.updated_at
FROM custom_provider_models m
JOIN custom_providers p ON m.provider_id = p.id
WHERE p.enabled = 1
ORDER BY m.provider_id ASC, m.id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query active custom models: %w", err)
	}
	defer rows.Close()

	var models []CustomProviderModelRecord
	for rows.Next() {
		var m CustomProviderModelRecord
		var supportsToolsInt int
		var createdStr, updatedStr string
		if err := rows.Scan(
			&m.ID,
			&m.ProviderID,
			&m.DisplayName,
			&m.ContextWindow,
			&m.MaxTokens,
			&supportsToolsInt,
			&createdStr,
			&updatedStr,
		); err != nil {
			return nil, fmt.Errorf("scan active custom model: %w", err)
		}
		m.SupportsTools = (supportsToolsInt == 1)
		m.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdStr)
		m.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedStr)
		models = append(models, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active custom models: %w", err)
	}
	return models, nil
}
