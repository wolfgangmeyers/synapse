package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// ModelContextThreshold defines warning and reset percentages for a specific model.
type ModelContextThreshold struct {
	WarningPercent int `json:"warning_percent"`
	ResetPercent   int `json:"reset_percent"`
}

// ContextThresholdConfig contains router-wide default thresholds and per-model overrides.
type ContextThresholdConfig struct {
	WarningPercent  int                              `json:"warning_percent"`
	ResetPercent    int                              `json:"reset_percent"`
	ModelThresholds map[string]ModelContextThreshold `json:"model_thresholds"`
}

const (
	DefaultContextWarningPercent = 10
	DefaultContextResetPercent   = 20
)

// GetContextThresholdConfig reads the current context threshold configuration from managed_settings.
func (s *Store) GetContextThresholdConfig(ctx context.Context) (ContextThresholdConfig, error) {
	var warningPct sql.NullInt64
	var resetPct sql.NullInt64
	var modelJSON sql.NullString

	err := s.db.QueryRowContext(ctx, `SELECT context_warning_percent, context_reset_percent, context_model_thresholds_json FROM managed_settings WHERE singleton_id = 1`).Scan(&warningPct, &resetPct, &modelJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return ContextThresholdConfig{
			WarningPercent:  DefaultContextWarningPercent,
			ResetPercent:    DefaultContextResetPercent,
			ModelThresholds: make(map[string]ModelContextThreshold),
		}, nil
	}
	if err != nil {
		return ContextThresholdConfig{}, err
	}

	cfg := ContextThresholdConfig{
		WarningPercent:  int(warningPct.Int64),
		ResetPercent:    int(resetPct.Int64),
		ModelThresholds: make(map[string]ModelContextThreshold),
	}
	if cfg.WarningPercent <= 0 {
		cfg.WarningPercent = DefaultContextWarningPercent
	}
	if cfg.ResetPercent <= 0 {
		cfg.ResetPercent = DefaultContextResetPercent
	}

	rawJSON := modelJSON.String
	if rawJSON != "" && rawJSON != "{}" {
		var thresholds map[string]ModelContextThreshold
		if err := json.Unmarshal([]byte(rawJSON), &thresholds); err == nil && thresholds != nil {
			cfg.ModelThresholds = thresholds
		}
	}

	return cfg, nil
}

// SetContextThresholdConfig validates and updates the context threshold configuration in managed_settings.
func (s *Store) SetContextThresholdConfig(ctx context.Context, cfg ContextThresholdConfig) error {
	if cfg.WarningPercent < 1 || cfg.ResetPercent <= cfg.WarningPercent || cfg.ResetPercent > 100 {
		return fmt.Errorf("invalid context threshold bounds: warning %d, reset %d (must satisfy 1 <= warning < reset <= 100)", cfg.WarningPercent, cfg.ResetPercent)
	}

	for modelKey, override := range cfg.ModelThresholds {
		if override.WarningPercent < 1 || override.ResetPercent <= override.WarningPercent || override.ResetPercent > 100 {
			return fmt.Errorf("invalid override bounds for model %q: warning %d, reset %d (must satisfy 1 <= warning < reset <= 100)", modelKey, override.WarningPercent, override.ResetPercent)
		}
	}

	if cfg.ModelThresholds == nil {
		cfg.ModelThresholds = make(map[string]ModelContextThreshold)
	}
	marshaledJSON, err := json.Marshal(cfg.ModelThresholds)
	if err != nil {
		return fmt.Errorf("marshal model thresholds: %w", err)
	}

	return s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		_, err := s.db.ExecContext(ctx, `UPDATE managed_settings SET context_warning_percent = ?, context_reset_percent = ?, context_model_thresholds_json = ?, updated_at = ? WHERE singleton_id = 1`,
			cfg.WarningPercent, cfg.ResetPercent, string(marshaledJSON), now)
		return err
	})
}
