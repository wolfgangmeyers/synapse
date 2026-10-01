package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
)

// ClaimEmbeddedPendingOriginal admits only the untouched launch reservation.
// The existing pending prompt row is a durable UNKNOWN dispatch claim: neither
// an unsent flag nor a new owner can authorize a second original invocation.
func (s *Store) ClaimEmbeddedPendingOriginal(ctx context.Context, b EmbeddedBinding, g int64) (string, string, error) {
	prompt, id, suppressed, err := s.embeddedPendingOriginal(ctx, b, g, true)
	if err == nil && (suppressed || prompt == "") {
		err = errors.New("pending original dispatch authority denied")
	}
	return prompt, id, err
}

func (s *Store) embeddedPendingOriginal(ctx context.Context, b EmbeddedBinding, g int64, claim bool) (prompt, id string, suppressed bool, err error) {
	id = fmt.Sprintf("launch-original:%x", sha256.Sum256([]byte(fmt.Sprintf("%q:%q:%d:%q", b.AgentID, b.SessionID, b.Epoch, b.Generation))))
	err = s.runImmediate(ctx, func() error {
		if e := s.embeddedFence(ctx, b); e != nil {
			return e
		}
		var newest int64
		if e := s.db.QueryRowContext(ctx, `SELECT generation FROM embedded_process_generation_counter WHERE singleton=1`).Scan(&newest); e != nil {
			return e
		}
		if g <= 0 || newest != g {
			return errors.New("stale process generation")
		}
		var state string
		var result sql.NullInt64
		e := s.db.QueryRowContext(ctx, `SELECT state,result_seq FROM embedded_prompt_requests WHERE request_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, id, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&state, &result)
		if e == nil {
			suppressed = true
			if state == "complete" && result.Valid && !claim {
				// Later ordinary turns retain their existing restart policy.
				var later int
				if e := s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND seq>?`, b.AgentID, b.SessionID, b.Epoch, result.Int64).Scan(&later); e != nil {
					return e
				}
				suppressed = later == 0
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var saved sql.NullString
		var sent int
		if e := s.db.QueryRowContext(ctx, `SELECT pending_initial_instruction,initial_instruction_sent FROM managed_agents WHERE agent_id=? AND runtime='embedded' AND pi_session_id=? AND session_epoch=? AND runtime_generation=? AND desired_state='running'`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&saved, &sent); e != nil {
			return e
		}
		if sent != 0 || !saved.Valid || saved.String == "" {
			return nil
		}
		var entries int
		if e := s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=?`, b.AgentID, b.SessionID, b.Epoch).Scan(&entries); e != nil {
			return e
		}
		if entries != 0 {
			return nil
		}
		// Zero entries alone are insufficient. Refuse other prompt, mail,
		// effect, notice, provider or wake claims, including archived UNKNOWN.
		for _, table := range []string{"embedded_prompt_requests", "embedded_attempts", "embedded_effects", "embedded_interrupted_notices", "embedded_nonmail_provider_entries", "embedded_restart_wake_slots"} {
			var n int
			if e := s.db.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE agent_id=? AND session_id=? AND epoch=?`, b.AgentID, b.SessionID, b.Epoch).Scan(&n); e != nil {
				return e
			}
			if n != 0 {
				suppressed = true
				return nil
			}
		}
		prompt = saved.String
		if claim {
			_, e := s.db.ExecContext(ctx, `INSERT INTO embedded_prompt_requests(request_id,agent_id,session_id,epoch,generation,prompt,state) VALUES(?,?,?,?,?,?,'pending')`, id, b.AgentID, b.SessionID, b.Epoch, b.Generation, prompt)
			return e
		}
		return nil
	})
	return
}

// ReserveEmbeddedPrompt fences the exact owner and records a request before the
// provider can run. A pending request is ambiguous and cannot be retried.
func (s *Store) ReserveEmbeddedPrompt(ctx context.Context, b EmbeddedBinding, id, prompt string) (bool, error) {
	if id == "" || len(id) > 256 || prompt == "" {
		return false, errors.New("invalid embedded prompt request")
	}
	replay := false
	err := s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		var agent, session, generation, saved, state string
		var epoch int64
		var seq sql.NullInt64
		err := s.db.QueryRowContext(ctx, `SELECT agent_id,session_id,epoch,generation,prompt,state,result_seq FROM embedded_prompt_requests WHERE request_id=?`, id).Scan(&agent, &session, &epoch, &generation, &saved, &state, &seq)
		if err == nil {
			if agent != b.AgentID || session != b.SessionID || epoch != b.Epoch || generation != b.Generation || saved != prompt {
				return errors.New("conflicting embedded prompt request")
			}
			if state != "complete" || !seq.Valid {
				return errors.New("embedded prompt outcome unknown")
			}
			var kind string
			if err := s.db.QueryRowContext(ctx, `SELECT kind FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND seq=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, seq.Int64).Scan(&kind); err != nil {
				return err
			}
			if kind != "assistant" {
				return errors.New("embedded prompt result is corrupt")
			}
			replay = true
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO embedded_prompt_requests(request_id,agent_id,session_id,epoch,generation,prompt,state) VALUES(?,?,?,?,?,?,'pending')`, id, b.AgentID, b.SessionID, b.Epoch, b.Generation, prompt)
		return err
	})
	return replay, err
}

// CompleteEmbeddedPrompt records only a checked, durable assistant head.
func (s *Store) CompleteEmbeddedPrompt(ctx context.Context, b EmbeddedBinding, id string) error {
	return s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		var seq int64
		var kind string
		if err := s.db.QueryRowContext(ctx, `SELECT seq,kind FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? ORDER BY seq DESC LIMIT 1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&seq, &kind); err != nil {
			return err
		}
		if kind != "assistant" {
			return errors.New("embedded prompt has no durable assistant result")
		}
		result, err := s.db.ExecContext(ctx, `UPDATE embedded_prompt_requests SET state='complete',result_seq=? WHERE request_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=? AND state='pending'`, seq, id, b.AgentID, b.SessionID, b.Epoch, b.Generation)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return errors.New("stale embedded prompt request")
		}
		return nil
	})
}
