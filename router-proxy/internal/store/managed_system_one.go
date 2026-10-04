package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// SystemOneConfig holds router-wide configuration for the System 1 / Clef server connection.
type SystemOneConfig struct {
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Enabled bool   `json:"enabled"`
}

// GetSystemOneConfig reads the current System 1 configuration from managed_settings.
func (s *Store) GetSystemOneConfig(ctx context.Context) (SystemOneConfig, error) {
	var host sql.NullString
	var port sql.NullInt64
	var enabled int
	err := s.db.QueryRowContext(ctx, `SELECT system_one_host, system_one_port, system_one_enabled FROM managed_settings WHERE singleton_id = 1`).Scan(&host, &port, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return SystemOneConfig{}, nil
	}
	if err != nil {
		return SystemOneConfig{}, err
	}
	return SystemOneConfig{
		Host:    host.String,
		Port:    int(port.Int64),
		Enabled: enabled == 1,
	}, nil
}

// SetSystemOneConfig updates the System 1 configuration in managed_settings.
func (s *Store) SetSystemOneConfig(ctx context.Context, cfg SystemOneConfig) error {
	return s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		enabled := 0
		if cfg.Enabled {
			enabled = 1
		}
		var hostVal any = strings.TrimSpace(cfg.Host)
		if hostVal == "" {
			hostVal = nil
		}
		var portVal any = cfg.Port
		if cfg.Port <= 0 {
			portVal = nil
		}
		_, err := s.db.ExecContext(ctx, `UPDATE managed_settings SET system_one_host = ?, system_one_port = ?, system_one_enabled = ?, updated_at = ? WHERE singleton_id = 1`, hostVal, portVal, enabled, now)
		return err
	})
}
