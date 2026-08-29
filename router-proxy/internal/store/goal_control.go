package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
)

const maxGoalControlDesiredConfigBytes = 16 * 1024

// GoalControlDesired is the durable desired control for one target instance.
// The config retains the source agent and control ID used to fence results
// after the target reconnects.
type GoalControlDesired struct {
	AgentID    string
	InstanceID string
	Revision   int64
	Config     map[string]any
	UpdatedAt  string
	ExpiresAt  string
}

func (s *Store) GoalControlDesiredFor(ctx context.Context, agentID, instanceID string) (*GoalControlDesired, error) {
	if agentID == "" || instanceID == "" {
		return nil, errors.New("agent and instance identity are required")
	}
	var result *GoalControlDesired
	err := s.runImmediate(ctx, func() error {
		var err error
		result, err = s.goalControlDesiredForTx(ctx, agentID, instanceID, formatTimestamp(s.clock.Now()))
		return err
	})
	return result, err
}

func (s *Store) goalControlDesiredForTx(ctx context.Context, agentID, instanceID, now string) (*GoalControlDesired, error) {
	var desired GoalControlDesired
	var configJSON string
	err := s.db.QueryRowContext(ctx, `SELECT agent_id,instance_id,revision,config_json,updated_at,expires_at
		FROM goal_control_desired_state WHERE agent_id = ? AND instance_id = ?`, agentID, instanceID).
		Scan(&desired.AgentID, &desired.InstanceID, &desired.Revision, &configJSON, &desired.UpdatedAt, &desired.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if desired.ExpiresAt <= now {
		_, err := s.db.ExecContext(ctx, `DELETE FROM goal_control_desired_state
			WHERE agent_id = ? AND instance_id = ? AND expires_at <= ?`, agentID, instanceID, now)
		return nil, err
	}
	if err := json.Unmarshal([]byte(configJSON), &desired.Config); err != nil || desired.Config == nil {
		return nil, nil
	}
	return &desired, nil
}

// ConsumeGoalControlDesired atomically verifies and removes the desired control.
// Removing it before a result is projected fences duplicate results while the
// revision history remains available for the next control.
func (s *Store) ConsumeGoalControlDesired(ctx context.Context, agentID, instanceID, sourceAgentID, controlID string, revision int64) (*GoalControlDesired, error) {
	if agentID == "" || instanceID == "" || sourceAgentID == "" || controlID == "" || revision < 1 {
		return nil, errors.New("goal control desired state identity is invalid")
	}
	var result *GoalControlDesired
	err := s.runImmediate(ctx, func() error {
		desired, err := s.goalControlDesiredForTx(ctx, agentID, instanceID, formatTimestamp(s.clock.Now()))
		if err != nil || desired == nil || desired.Revision != revision {
			return err
		}
		desiredSource, sourceOK := desired.Config["sourceAgentId"].(string)
		desiredControl, controlOK := desired.Config["controlId"].(string)
		if !sourceOK || !controlOK || desiredSource != sourceAgentID || desiredControl != controlID {
			return nil
		}
		deleted, err := s.db.ExecContext(ctx, `DELETE FROM goal_control_desired_state
			WHERE agent_id = ? AND instance_id = ? AND revision = ?`, agentID, instanceID, revision)
		if err != nil {
			return err
		}
		count, err := deleted.RowsAffected()
		if err != nil || count != 1 {
			return err
		}
		result = desired
		return nil
	})
	return result, err
}

func (s *Store) UpsertGoalControlDesired(ctx context.Context, agentID, instanceID string, config map[string]any, expiresAt string) (*GoalControlDesired, error) {
	if agentID == "" || instanceID == "" || config == nil {
		return nil, errors.New("goal control desired state is invalid")
	}
	serialized, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("serialize goal control desired state: %w", err)
	}
	if len(serialized) > maxGoalControlDesiredConfigBytes {
		return nil, errors.New("goal control desired state is too large")
	}
	if _, err := protocol.ParseTimestamp(expiresAt); err != nil {
		return nil, errors.New("goal control desired state expiry is invalid")
	}
	var result *GoalControlDesired
	err = s.runImmediate(ctx, func() error {
		var revision int64
		err := s.db.QueryRowContext(ctx, `SELECT revision FROM goal_control_revision_history
			WHERE agent_id = ? AND instance_id = ?`, agentID, instanceID).Scan(&revision)
		if errors.Is(err, sql.ErrNoRows) {
			revision = 1
			if _, err := s.db.ExecContext(ctx, `INSERT INTO goal_control_revision_history
				(agent_id,instance_id,revision) VALUES (?,?,?)`, agentID, instanceID, revision); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			revision++
			if _, err := s.db.ExecContext(ctx, `UPDATE goal_control_revision_history
				SET revision = ? WHERE agent_id = ? AND instance_id = ?`, revision, agentID, instanceID); err != nil {
				return err
			}
		}
		now := formatTimestamp(s.clock.Now())
		if _, err := s.db.ExecContext(ctx, `INSERT INTO goal_control_desired_state
			(agent_id,instance_id,revision,config_json,updated_at,expires_at) VALUES (?,?,?,?,?,?)
			ON CONFLICT (agent_id,instance_id) DO UPDATE SET revision=excluded.revision,
			config_json=excluded.config_json,updated_at=excluded.updated_at,expires_at=excluded.expires_at`,
			agentID, instanceID, revision, string(serialized), now, expiresAt); err != nil {
			return err
		}
		result, err = s.goalControlDesiredForTx(ctx, agentID, instanceID, now)
		return err
	})
	return result, err
}

func (s *Store) DeleteGoalControlDesiredExcept(ctx context.Context, agentID, instanceID string) (int, error) {
	if agentID == "" || instanceID == "" {
		return 0, errors.New("agent and instance identity are required")
	}
	var count int64
	err := s.runImmediate(ctx, func() error {
		result, err := s.db.ExecContext(ctx, `DELETE FROM goal_control_desired_state
			WHERE agent_id = ? AND instance_id != ?`, agentID, instanceID)
		if err != nil {
			return err
		}
		count, err = result.RowsAffected()
		return err
	})
	return int(count), err
}
