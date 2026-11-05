package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

// ErrAgentNotFound means no current logical identity can accept this mutation.
var ErrAgentNotFound = errors.New("agent was not found")

// SetAgentUrgency commits an explicit value and the managed projection version
// under the same writer boundary as final deletion. It never creates identities.
// Callers publishing notifications must serialize mutation plus publication.
func (s *Store) SetAgentUrgency(ctx context.Context, agentID string, urgent bool) (bool, error) {
	result, _, err := s.SetAgentUrgencyCommitted(ctx, agentID, urgent)
	if err != nil {
		return false, err
	}
	return result.Urgent, nil
}

// Historical migration fixtures intentionally predate the shared column.
func agentUrgencyEnabledTx(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE id='124_agent_urgency'`).Scan(&count)
	return count == 1, err
}

// loadManagedUrgencyTx adds two fixed queries per public read, independent of
// projection size. Keep the historical managed scanner and SQL aliases intact.
func loadManagedUrgencyTx(ctx context.Context, db *sql.DB, agents []*ManagedAgentRecord) error {
	if len(agents) == 0 {
		return nil
	}
	enabled, err := agentUrgencyEnabledTx(ctx, db)
	if err != nil || !enabled {
		return err
	}
	ids := make([]string, len(agents))
	for i, agent := range agents {
		ids[i] = agent.AgentID
	}
	rows, err := db.QueryContext(ctx, `SELECT agent_id,urgent FROM agents WHERE agent_id IN (`+sqlitePlaceholders(len(ids))+`)`, stringArgs(ids)...)
	if err != nil {
		return err
	}
	values := make(map[string]bool, len(ids))
	for rows.Next() {
		var id string
		var urgent bool
		if err := rows.Scan(&id, &urgent); err != nil {
			_ = rows.Close()
			return err
		}
		values[id] = urgent
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, agent := range agents {
		value, ok := values[agent.AgentID]
		if !ok {
			return sql.ErrNoRows
		}
		agent.Urgent = value
	}
	return nil
}

// reactivateAgentIdentityTx establishes a fresh lifetime only for retired rows.
// Caller owns the store transaction. Existing active identities keep urgency.
func reactivateAgentIdentityTx(ctx context.Context, db *sql.DB, agentID string) error {
	enabled, err := agentUrgencyEnabledTx(ctx, db)
	if err != nil || !enabled {
		return err
	}
	_, err = db.ExecContext(ctx, `UPDATE agents SET identity_active=1, urgent=0 WHERE agent_id=? AND identity_active=0`, agentID)
	return err
}

// Caller owns the writer transaction, including identity validation.
func (s *Store) setAgentUrgencyTx(ctx context.Context, agentID string, urgent bool) error {
	var current bool
	err := s.db.QueryRowContext(ctx, `SELECT urgent FROM agents WHERE agent_id=? AND identity_active=1`, agentID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAgentNotFound
	}
	if err != nil {
		return err
	}
	if current == urgent {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE agents SET urgent=? WHERE agent_id=?`, boolInt(urgent), agentID); err != nil {
		return err
	}
	_, err = s.bumpManagedProjectionVersion(ctx)
	return err
}

type AgentUrgencyResult struct {
	AgentID string `json:"agent_id"`
	Urgent  bool   `json:"urgent"`
}

// Validate original values at both the SDK and durable admission boundaries.
func AgentUrgencyArguments(args map[string]any, self string) (AgentUrgencyResult, error) {
	invalid := errors.New("expected urgent boolean and optional exact agent_id (1-128 characters)")
	urgent, ok := args["urgent"].(bool)
	if !ok {
		return AgentUrgencyResult{}, invalid
	}
	id := self
	for key, value := range args {
		switch key {
		case "urgent":
		case "agent_id":
			target, ok := value.(string)
			if !ok || !utf8.ValidString(target) || strings.TrimSpace(target) == "" || utf8.RuneCountInString(target) > 128 {
				return AgentUrgencyResult{}, invalid
			}
			id = target
		default:
			return AgentUrgencyResult{}, invalid
		}
	}
	return AgentUrgencyResult{AgentID: id, Urgent: urgent}, nil
}

// SetEmbeddedAgentUrgency consumes live checked authority within the same
// transaction as the boolean and projection version. Receipts cannot replay it.
func (s *Store) SetEmbeddedAgentUrgency(ctx context.Context, w *EmbeddedToolInvocation) (AgentUrgencyResult, error) {
	if w == nil {
		return AgentUrgencyResult{}, errors.New("missing urgency producer")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.domainSettled {
		return AgentUrgencyResult{}, errors.New("urgency admission already consumed")
	}
	result, err := AgentUrgencyArguments(w.call.Arguments, w.origin.binding.AgentID)
	if err != nil {
		return AgentUrgencyResult{}, err
	}
	committed, conclusive, err := s.runImmediateWithCommitOutcome(ctx, func() error {
		if err := s.admitEmbeddedToolTx(ctx, w); err != nil {
			return err
		}
		if w.call.Name != "mecha_agent_urgency" {
			return errors.New("invalid urgency invocation")
		}
		return s.setAgentUrgencyTx(ctx, result.AgentID, result.Urgent)
	})
	w.recordDomainDisposition(committed, conclusive, err)
	if committed && conclusive {
		var encodeErr error
		w.domainContent, encodeErr = json.Marshal(result)
		err = errors.Join(err, encodeErr)
	}
	return result, err
}

// SetAgentUrgencyCommitted exposes the actual transaction disposition for the
// shared publication coordinator, even if post-COMMIT state-file checks fail.
func (s *Store) SetAgentUrgencyCommitted(ctx context.Context, agentID string, urgent bool) (AgentUrgencyResult, bool, error) {
	if ctx == nil {
		return AgentUrgencyResult{}, false, errors.New("context is required")
	}
	committed, conclusive, err := s.runImmediateWithCommitOutcome(ctx, func() error { return s.setAgentUrgencyTx(ctx, agentID, urgent) })
	return AgentUrgencyResult{AgentID: agentID, Urgent: urgent}, committed && conclusive, err
}
