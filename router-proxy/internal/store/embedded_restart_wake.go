package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math"
)

// EmbeddedRestartWake is durable Store-only awareness, not permission to call a provider.
// A caller must cache its assigned generation; retained history is not authority.
type EmbeddedRestartWake struct {
	ProcessGeneration       int64
	ID, Kind, Digest, State string
}

// Keep the newest 32 mappings plus every wake/origin reference, with a hard
// total admission cap of 256. Mint, reference retirement and sweep own pruning.
// The singleton high-water survives forever; it is never pruned or reused.
const EmbeddedProcessGenerationHistoryLimit = 32
const EmbeddedProcessGenerationMappingLimit = 256

// EmbeddedProcessGenerationExpired denies absent historical identities once
// pruning is possible. Its fixed message never includes an incarnation key.
type EmbeddedProcessGenerationExpired struct{}

func (*EmbeddedProcessGenerationExpired) Error() string {
	return "embedded process incarnation mapping expired or unknown"
}

// EmbeddedProcessGenerationCapacity fails minting when protected mappings fill
// the admission cap. The immediate transaction restores pruning and high-water.
type EmbeddedProcessGenerationCapacity struct{}

func (*EmbeddedProcessGenerationCapacity) Error() string {
	return "embedded process generation mapping capacity reached"
}

// EmbeddedProcessGenerationUncertain means rollback did not prove the write
// undone. An owning Router must remain closed; a fresh mint is not a retry.
type EmbeddedProcessGenerationUncertain struct{ Cause error }

func (*EmbeddedProcessGenerationUncertain) Error() string {
	return "embedded process generation commit outcome uncertain"
}

func (e *EmbeddedProcessGenerationUncertain) Unwrap() error { return e.Cause }

// MintNewEmbeddedProcessGeneration witnesses a new instance by creating its
// identity here, not by accepting a historical token or reconstructing a wake.
// Call only after bind. A positive generation is a known committed assignment,
// even with a post-COMMIT error: cache it before propagating the error. Zero
// means either conclusive rollback or typed uncertainty, never an assignment.
func (s *Store) MintNewEmbeddedProcessGeneration(ctx context.Context) (int64, error) {
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return 0, fmt.Errorf("embedded process incarnation: %w", err)
	}
	return s.mintEmbeddedProcessGeneration(ctx, fmt.Sprintf("router-%x", nonce[:]), true)
}

// MintEmbeddedProcessGeneration preserves keyed lookup for retained identities.
// Before the first possible pruning it also supports legacy keyed minting.
// Thereafter an absent key fails closed: bounded history cannot distinguish a
// caller's new string from an expired old one. New instances use the method
// above, which accepts no historical identity. No tombstone ledger is needed.
// Like fresh mint, a positive return must be retained even when err is non-nil;
// committed disposition, not err == nil, decides whether an assignment exists.
func (s *Store) MintEmbeddedProcessGeneration(ctx context.Context, key string) (int64, error) {
	return s.mintEmbeddedProcessGeneration(ctx, key, false)
}

func (s *Store) mintEmbeddedProcessGeneration(ctx context.Context, key string, fresh bool) (int64, error) {
	if len(key) == 0 || len(key) > 256 {
		return 0, errors.New("invalid verified process incarnation key")
	}
	var generation int64
	committed, conclusive, err := s.runImmediateWithCommitOutcome(ctx, func() error {
		err := s.db.QueryRowContext(ctx, `SELECT generation FROM embedded_process_generations WHERE incarnation_key=?`, key).Scan(&generation)
		if err == nil {
			if fresh {
				return errors.New("embedded process incarnation collision")
			}
			if generation <= 0 {
				return errors.New("invalid embedded process generation")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var last int64
		err = s.db.QueryRowContext(ctx, `SELECT generation FROM embedded_process_generation_counter WHERE singleton=1`).Scan(&last)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if last < 0 || (last == 0 && err == nil) {
			return errors.New("invalid embedded process generation counter")
		}
		if last == math.MaxInt64 {
			return errors.New("embedded process generation exhausted")
		}
		if !fresh && last > EmbeddedProcessGenerationHistoryLimit {
			return &EmbeddedProcessGenerationExpired{}
		}
		generation = last + 1
		if _, err = s.db.ExecContext(ctx, `INSERT INTO embedded_process_generation_counter(singleton,generation) VALUES(1,?) ON CONFLICT(singleton) DO UPDATE SET generation=excluded.generation`, generation); err != nil {
			return err
		}
		if _, err = s.db.ExecContext(ctx, `INSERT INTO embedded_process_generations(incarnation_key,generation) VALUES(?,?)`, key, generation); err != nil {
			return err
		}
		if err = s.pruneEmbeddedProcessGenerationsTx(ctx); err != nil {
			return err
		}
		var count int
		if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_process_generations`).Scan(&count); err != nil {
			return err
		}
		if count > EmbeddedProcessGenerationMappingLimit {
			return &EmbeddedProcessGenerationCapacity{}
		}
		return nil
	})
	if committed {
		return generation, err
	}
	if !conclusive {
		return 0, &EmbeddedProcessGenerationUncertain{Cause: err}
	}
	return 0, err
}

// Caller holds an immediate transaction. Older migration fixtures lack 080.
func (s *Store) pruneEmbeddedProcessGenerationsTx(ctx context.Context) error {
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='embedded_process_generations'`).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return nil
	}
	// 080 fixtures may omit 081. Slot references already protect their origins.
	var origins int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='embedded_restart_wake_checked_origins'`).Scan(&origins); err != nil {
		return err
	}
	query := `DELETE FROM embedded_process_generations
	 WHERE generation NOT IN (SELECT generation FROM embedded_process_generations ORDER BY generation DESC LIMIT ?)
	 AND NOT EXISTS (SELECT 1 FROM embedded_restart_wake_slots w WHERE w.process_generation=embedded_process_generations.generation)`
	if origins == 1 {
		query += ` AND NOT EXISTS (SELECT 1 FROM embedded_restart_wake_checked_origins o WHERE o.process_generation=embedded_process_generations.generation)`
	}
	_, err := s.db.ExecContext(ctx, query, EmbeddedProcessGenerationHistoryLimit)
	return err
}

func wakeRow(ctx context.Context, db *sql.DB, b EmbeddedBinding, slot string) (EmbeddedRestartWake, error) {
	var w EmbeddedRestartWake
	err := db.QueryRowContext(ctx, `SELECT process_generation,wake_id,kind,digest,state FROM embedded_restart_wake_slots WHERE agent_id=? AND slot=? AND session_id=? AND epoch=? AND runtime_generation=?`, b.AgentID, slot, b.SessionID, b.Epoch, b.Generation).Scan(&w.ProcessGeneration, &w.ID, &w.Kind, &w.Digest, &w.State)
	return w, err
}

// ReserveEmbeddedRestartWake uses two slots per agent. Only the current process
// can create a plan; the last slot exists solely for dedup and inspection.
func (s *Store) ReserveEmbeddedRestartWake(ctx context.Context, b EmbeddedBinding, g int64, id, kind, digest string) (EmbeddedRestartWake, error) {
	var result EmbeddedRestartWake
	if g <= 0 || len(id) == 0 || len(id) > 256 || len(digest) == 0 || len(digest) > 256 || (kind != "root-idle" && kind != "worker-idle" && kind != "interrupted") {
		return result, errors.New("invalid restart wake")
	}
	err := s.runImmediate(ctx, func() error {
		return s.reserveEmbeddedRestartWakeTx(ctx, b, g, id, kind, digest, &result)
	})
	return result, err
}

func (s *Store) reserveEmbeddedRestartWakeTx(ctx context.Context, b EmbeddedBinding, g int64, id, kind, digest string, result *EmbeddedRestartWake) error {
	if err := s.embeddedFence(ctx, b); err != nil {
		return err
	}
	var newest int64
	if err := s.db.QueryRowContext(ctx, `SELECT generation FROM embedded_process_generation_counter WHERE singleton=1`).Scan(&newest); err != nil {
		return err
	}
	if g != newest {
		return errors.New("stale process generation")
	}
	if kind == "interrupted" {
		var found int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_interrupted_notices WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND notice_id=? AND state='appended' AND entry_seq IS NOT NULL`, b.AgentID, b.SessionID, b.Epoch, b.Generation, id).Scan(&found); err != nil {
			return err
		}
		if found != 1 {
			return errors.New("interrupted restart wake requires unstarted appended 076 notice")
		}
	} else {
		role := "root"
		if kind == "worker-idle" {
			role = "worker"
		}
		var found int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1 AND role=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, role).Scan(&found); err != nil {
			return err
		}
		if found != 1 {
			return errors.New("restart wake role mismatch")
		}
	}
	current, err := wakeRow(ctx, s.db, b, "current")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		if current.ProcessGeneration == g {
			if current.ID != id || current.Kind != kind || current.Digest != digest {
				return errors.New("conflicting restart wake")
			}
			*result = current
			return nil
		}
		if current.ProcessGeneration > g {
			return errors.New("stale restart wake generation")
		}
	}
	// A still-unclaimed plan for the same stable wake can move to this
	// incarnation. Rekey the current row, rather than inserting a second
	// row with the same wake_id (or losing the one-shot 076 notice).
	if err == nil && current.ID == id {
		if current.State != "planned" || current.Kind != kind || current.Digest != digest {
			return errors.New("conflicting or already claimed restart wake")
		}
		r, err := s.db.ExecContext(ctx, `UPDATE embedded_restart_wake_slots SET process_generation=? WHERE agent_id=? AND slot='current' AND session_id=? AND epoch=? AND runtime_generation=? AND process_generation=? AND wake_id=? AND kind=? AND digest=? AND state='planned'`, g, b.AgentID, b.SessionID, b.Epoch, b.Generation, current.ProcessGeneration, id, kind, digest)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("stale restart wake binding")
		}
		*result = EmbeddedRestartWake{ProcessGeneration: g, ID: id, Kind: kind, Digest: digest, State: "planned"}
		return s.pruneEmbeddedProcessGenerationsTx(ctx)
	}
	// Do not archive a claimed wake before reconciling its durable checked
	// origin. Implicitly marking it unknown would discard proven completion.
	var claimed int
	if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_restart_wake_slots WHERE agent_id=? AND slot='current' AND state='claimed'`, b.AgentID).Scan(&claimed); err != nil {
		return err
	}
	if claimed != 0 {
		return errors.New("claimed restart wake requires reconciliation before rotation")
	}
	// A replaced session/epoch must not carry a previous wake forward.
	if _, err = s.db.ExecContext(ctx, `DELETE FROM embedded_restart_wake_slots WHERE agent_id=? AND slot='last'`, b.AgentID); err != nil {
		return err
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE embedded_restart_wake_slots SET slot='last',state=CASE WHEN state='claimed' THEN 'unknown' ELSE state END WHERE agent_id=? AND slot='current'`, b.AgentID); err != nil {
		return err
	}
	notice := sql.NullString{}
	if kind == "interrupted" {
		notice = sql.NullString{String: id, Valid: true}
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO embedded_restart_wake_slots(agent_id,slot,session_id,epoch,runtime_generation,process_generation,wake_id,kind,notice_id,digest,state) VALUES(?,'current',?,?,?,?,?,?,?,?,'planned')`, b.AgentID, b.SessionID, b.Epoch, b.Generation, g, id, kind, notice, digest)
	if err != nil {
		return fmt.Errorf("reserve restart wake: %w", err)
	}
	*result = EmbeddedRestartWake{ProcessGeneration: g, ID: id, Kind: kind, Digest: digest, State: "planned"}
	return s.pruneEmbeddedProcessGenerationsTx(ctx)
}

func (s *Store) EmbeddedRestartWake(ctx context.Context, b EmbeddedBinding, g int64, id string) (EmbeddedRestartWake, error) {
	var w EmbeddedRestartWake
	err := s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		var err error
		w, err = wakeRow(ctx, s.db, b, "current")
		if err != nil {
			return err
		}
		if w.ProcessGeneration != g || w.ID != id {
			return errors.New("stale restart wake")
		}
		return nil
	})
	return w, err
}

func (s *Store) transitionEmbeddedRestartWake(ctx context.Context, b EmbeddedBinding, g int64, id, from, to string) error {
	if g <= 0 || id == "" {
		return errors.New("invalid restart wake identity")
	}
	return s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		if from == "planned" && to == "claimed" {
			var kind string
			var noticeID sql.NullString
			err := s.db.QueryRowContext(ctx, `SELECT kind,notice_id FROM embedded_restart_wake_slots WHERE agent_id=? AND slot='current' AND session_id=? AND epoch=? AND runtime_generation=? AND process_generation=? AND wake_id=? AND state='planned'`, b.AgentID, b.SessionID, b.Epoch, b.Generation, g, id).Scan(&kind, &noticeID)
			if err != nil {
				return fmt.Errorf("restart wake transition denied: %w", err)
			}
			if kind == "interrupted" {
				if !noticeID.Valid || noticeID.String != id {
					return errors.New("restart wake notice mismatch")
				}
				// This and the wake claim share the immediate transaction. A started
				// notice is terminal even when the process generation is new.
				r, err := s.db.ExecContext(ctx, `UPDATE embedded_interrupted_notices SET state='started',start_owner_token=? WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND notice_id=? AND state='appended' AND entry_seq IS NOT NULL`, b.OwnerToken, b.AgentID, b.SessionID, b.Epoch, b.Generation, id)
				if err != nil {
					return err
				}
				changed, err := r.RowsAffected()
				if err != nil {
					return err
				}
				if changed != 1 {
					return errors.New("interrupted notice already started or stale")
				}
			}
		}
		var r sql.Result
		var err error
		if from == "planned" && to == "claimed" {
			r, err = s.db.ExecContext(ctx, `UPDATE embedded_restart_wake_slots SET state=?,claim_owner_token=? WHERE agent_id=? AND slot='current' AND session_id=? AND epoch=? AND runtime_generation=? AND process_generation=? AND wake_id=? AND state=?`, to, b.OwnerToken, b.AgentID, b.SessionID, b.Epoch, b.Generation, g, id, from)
		} else {
			r, err = s.db.ExecContext(ctx, `UPDATE embedded_restart_wake_slots SET state=? WHERE agent_id=? AND slot='current' AND session_id=? AND epoch=? AND runtime_generation=? AND process_generation=? AND wake_id=? AND state=? AND claim_owner_token=?`, to, b.AgentID, b.SessionID, b.Epoch, b.Generation, g, id, from, b.OwnerToken)
		}
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("restart wake transition denied")
		}
		return nil
	})
}

// Claim is the irreversible pre-provider boundary. For interrupted wakes it
// also starts the 076 notice in the same transaction: callers must not call
// StartEmbeddedInterruptedNotice separately. A claimed wake cannot dispatch
// twice; after a crash it reconciles to unknown.
func (s *Store) ClaimEmbeddedRestartWake(ctx context.Context, b EmbeddedBinding, g int64, id string) error {
	return s.transitionEmbeddedRestartWake(ctx, b, g, id, "planned", "claimed")
}

// Complete accepts only the exact checked entry committed with this wake's origin.
// Reconcile must be attempted after final append and before this call after a crash.
func (s *Store) CompleteEmbeddedRestartWake(ctx context.Context, b EmbeddedBinding, g int64, id string, seq int64, digest string) error {
	if g <= 0 || id == "" || seq <= 0 || digest == "" {
		return errors.New("invalid restart wake completion")
	}
	return s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		var body []byte
		var kind, savedDigest string
		err := s.db.QueryRowContext(ctx, `SELECT e.kind,e.body,e.digest FROM embedded_restart_wake_checked_origins o JOIN embedded_restart_wake_slots w ON w.agent_id=o.agent_id AND w.slot=o.slot AND w.session_id=o.session_id AND w.epoch=o.epoch AND w.runtime_generation=o.runtime_generation AND w.wake_id=o.wake_id AND w.process_generation=o.process_generation AND w.claim_owner_token=o.claim_owner_token JOIN embedded_entries e ON e.agent_id=o.agent_id AND e.session_id=o.session_id AND e.epoch=o.epoch AND e.generation=o.runtime_generation AND e.seq=o.seq WHERE o.agent_id=? AND o.session_id=? AND o.epoch=? AND o.runtime_generation=? AND o.seq=? AND o.wake_id=? AND o.process_generation=? AND o.claim_owner_token=? AND w.slot='current' AND w.state='claimed'`, b.AgentID, b.SessionID, b.Epoch, b.Generation, seq, id, g, b.OwnerToken).Scan(&kind, &body, &savedDigest)
		if err != nil {
			return fmt.Errorf("restart wake checked origin denied: %w", err)
		}
		if savedDigest != digest || !embeddedCheckedStop(kind, body) {
			return errors.New("restart wake final mismatch")
		}
		r, err := s.db.ExecContext(ctx, `UPDATE embedded_restart_wake_slots SET state='checked-complete' WHERE agent_id=? AND slot='current' AND session_id=? AND epoch=? AND runtime_generation=? AND process_generation=? AND wake_id=? AND claim_owner_token=? AND state='claimed'`, b.AgentID, b.SessionID, b.Epoch, b.Generation, g, id, b.OwnerToken)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("restart wake completion denied")
		}
		return nil
	})
}

// Reconcile uses the live owner's fence to settle a dead owner's claimed wake.
// The old claim token remains immutable evidence: it is never lent to the new
// owner, and only an exact checked-origin final can produce checked-complete.
func (s *Store) ReconcileEmbeddedRestartWake(ctx context.Context, b EmbeddedBinding, g int64, id string) error {
	if g <= 0 || id == "" {
		return errors.New("invalid restart wake identity")
	}
	return s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		var claimToken int64
		err := s.db.QueryRowContext(ctx, `SELECT claim_owner_token FROM embedded_restart_wake_slots WHERE agent_id=? AND slot='current' AND session_id=? AND epoch=? AND runtime_generation=? AND process_generation=? AND wake_id=? AND state='claimed'`, b.AgentID, b.SessionID, b.Epoch, b.Generation, g, id).Scan(&claimToken)
		if err != nil {
			return fmt.Errorf("restart wake reconciliation denied: %w", err)
		}
		var kind string
		var body []byte
		var savedDigest string
		err = s.db.QueryRowContext(ctx, `SELECT e.kind,e.body,e.digest FROM embedded_restart_wake_checked_origins o JOIN embedded_entries e ON e.agent_id=o.agent_id AND e.session_id=o.session_id AND e.epoch=o.epoch AND e.generation=o.runtime_generation AND e.seq=o.seq WHERE o.agent_id=? AND o.slot='current' AND o.session_id=? AND o.epoch=? AND o.runtime_generation=? AND o.wake_id=? AND o.process_generation=? AND o.claim_owner_token=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, id, g, claimToken).Scan(&kind, &body, &savedDigest)
		state := "checked-complete"
		if errors.Is(err, sql.ErrNoRows) {
			// An origin for this slot with the wrong association is corruption,
			// not proof that the provider was never entered.
			var count int
			if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_restart_wake_checked_origins WHERE agent_id=? AND slot='current'`, b.AgentID).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				return errors.New("restart wake checked origin mismatch")
			}
			state = "unknown"
		} else if err != nil {
			return err
		} else if savedDigest == "" || !embeddedCheckedStop(kind, body) {
			return errors.New("restart wake checked final invalid")
		}
		r, err := s.db.ExecContext(ctx, `UPDATE embedded_restart_wake_slots SET state=? WHERE agent_id=? AND slot='current' AND session_id=? AND epoch=? AND runtime_generation=? AND process_generation=? AND wake_id=? AND claim_owner_token=? AND state='claimed'`, state, b.AgentID, b.SessionID, b.Epoch, b.Generation, g, id, claimToken)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("restart wake reconciliation denied")
		}
		return nil
	})
}
