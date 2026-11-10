package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
)

// InstallEmbeddedResetSeed installs or verifies the complete seed under the
// current owner fence. A matching prefix from the former incremental installer
// is repairable; any other target contents are rejected.
func (s *Store) InstallEmbeddedResetSeed(ctx context.Context, target, source EmbeddedBinding, identity string, bodies [][]byte, imports []EmbeddedEntry) error {
	if target.OwnerToken <= 0 || identity == "" || source.AgentID != target.AgentID {
		return errors.New("invalid reset seed authority")
	}
	return s.runImmediate(ctx, func() error { return s.installEmbeddedResetSeedTx(ctx, target, source, identity, bodies, imports) })
}

func (s *Store) installEmbeddedResetSeedTx(ctx context.Context, target, source EmbeddedBinding, identity string, bodies [][]byte, imports []EmbeddedEntry) error {
	if err := s.embeddedFence(ctx, target); err != nil {
		return err
	}
	var owner string
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(owner_id,'') FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, target.AgentID, target.SessionID, target.Epoch, target.Generation).Scan(&owner); err != nil {
		return err
	}
	if owner != identity {
		return fmt.Errorf("%w: reset seed owner changed", ErrStaleEmbeddedFence)
	}
	type seed struct {
		kind     string
		body     []byte
		admitted int64
	}
	entries := make([]seed, 0, len(bodies)+len(imports))
	for _, body := range bodies {
		if len(body) > 0 {
			if err := embeddedBody(body); err != nil {
				return err
			}
			entries = append(entries, seed{"user", body, s.clock.Now().UnixMilli()})
		}
	}
	for _, ref := range imports {
		saved := EmbeddedEntry{Sequence: ref.Sequence}
		var admitted int64
		if err := s.db.QueryRowContext(ctx, `SELECT kind,body,parent_digest,digest,retention_admitted_at_ms FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND seq=?`, source.AgentID, source.SessionID, source.Epoch, source.Generation, ref.Sequence).Scan(&saved.Kind, &saved.Body, &saved.ParentDigest, &saved.Digest, &admitted); err != nil {
			return err
		}
		if admitted <= 0 || saved.Digest != ref.Digest || embeddedEntryDigest(source, saved) != saved.Digest || !bytes.Equal(saved.Body, ref.Body) || embeddedBody(saved.Body) != nil {
			return errors.New("invalid embedded import authority")
		}
		entries = append(entries, seed{saved.Kind, saved.Body, admitted})
	}
	rows, err := s.db.QueryContext(ctx, `SELECT generation,seq,parent_digest,kind,body,digest,retention_admitted_at_ms FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? ORDER BY seq`, target.AgentID, target.SessionID, target.Epoch)
	if err != nil {
		return err
	}
	existing := []EmbeddedEntry{}
	admissions := []int64{}
	for rows.Next() {
		var e EmbeddedEntry
		var admitted int64
		var generation string
		if err = rows.Scan(&generation, &e.Sequence, &e.ParentDigest, &e.Kind, &e.Body, &e.Digest, &admitted); err != nil {
			rows.Close()
			return err
		}
		if generation != target.Generation {
			rows.Close()
			return errors.New("conflicting reset seed")
		}
		existing = append(existing, e)
		admissions = append(admissions, admitted)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(existing) > len(entries) {
		return errors.New("conflicting reset seed")
	}
	digest := embeddedGenesis
	for i, e := range existing {
		want := entries[i]
		if e.Sequence != int64(i+1) || e.ParentDigest != digest || e.Kind != want.kind || !bytes.Equal(e.Body, want.body) || e.Digest != embeddedEntryDigest(target, e) || (i >= len(entries)-len(imports) && admissions[i] != want.admitted) {
			return errors.New("conflicting reset seed")
		}
		digest = e.Digest
	}
	if len(imports) > 0 {
		var cutoff int64
		if err := s.db.QueryRowContext(ctx, `SELECT evidence_expired_through_ms FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, source.AgentID, source.SessionID, source.Epoch, source.Generation).Scan(&cutoff); err != nil {
			return err
		}
		var guards int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE id='123_workflow_evidence_retention_guards'`).Scan(&guards); err != nil {
			return err
		}
		if guards != 0 {
			if err := s.refreshEmbeddedExecutionCallsTx(ctx); err != nil {
				return err
			}
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE embedded_sessions SET evidence_expired_through_ms=MAX(evidence_expired_through_ms,?) WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, cutoff, target.AgentID, target.SessionID, target.Epoch, target.Generation); err != nil {
			return err
		}
	}
	for i := len(existing); i < len(entries); i++ {
		e, err := s.appendEmbeddedAdmittedTx(ctx, target, entries[i].kind, entries[i].body, int64(i), digest, entries[i].admitted)
		if err != nil {
			return err
		}
		digest = e.Digest
	}
	return nil
}
