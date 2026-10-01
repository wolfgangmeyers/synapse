package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sky-valley/pi/ai"
)

// EmbeddedRestartDecision describes durable awareness only. It is not a
// dispatch instruction: the Router must separately prove listener readiness,
// acquire ownership, and cross the appropriate provider-entry boundary.
type EmbeddedRestartDecision struct {
	Kind   string // neutral, root-idle, worker-idle, interrupted, unentered-original, pending-original
	Wake   EmbeddedRestartWake
	Notice EmbeddedInterruptedNotice
	// Source is the checked unentered original, or a settled nonmail user
	// with its exact provider-entry marker and checked final at the saved head.
	// A neutral ACKed mail turn never supplies scheduling authority here.
	Source EmbeddedEntry
	// Suppressed means an unknown or already-dispatched wake cannot be chained.
	Suppressed bool
}

// ClassifyEmbeddedRestart inspects a checked saved projection under the current
// owner and process generation. It never invokes a provider or acknowledges mail.
// Call MintEmbeddedProcessGeneration only after a listener has bound; this Store
// method does not mint and cannot prove the caller's OS incarnation.
func (s *Store) ClassifyEmbeddedRestart(ctx context.Context, b EmbeddedBinding, processGeneration int64) (EmbeddedRestartDecision, error) {
	out := EmbeddedRestartDecision{Kind: "neutral"}
	if processGeneration <= 0 {
		return out, errors.New("invalid process generation")
	}
	snap, err := s.ReopenEmbedded(ctx, b)
	if err != nil {
		return out, err
	}
	role, err := s.EmbeddedSessionRole(ctx, b)
	if err != nil {
		return out, err
	}
	// Check the process counter and the exact projection head again before
	// mutating anything; a competing append cannot change the classification.
	err = s.runImmediate(ctx, func() error {
		if e := s.embeddedFence(ctx, b); e != nil {
			return e
		}
		var newest int64
		if e := s.db.QueryRowContext(ctx, `SELECT generation FROM embedded_process_generation_counter WHERE singleton=1`).Scan(&newest); e != nil {
			return e
		}
		if newest != processGeneration {
			return errors.New("stale process generation")
		}
		var savedRole string
		if e := s.db.QueryRowContext(ctx, `SELECT role FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&savedRole); e != nil {
			return e
		}
		if savedRole != role {
			return errors.New("saved role changed")
		}
		seq, digest, e := s.embeddedHead(ctx, b)
		if e != nil {
			return e
		}
		expectedSeq, expectedDigest := int64(0), embeddedGenesis
		if len(snap.Entries) > 0 {
			last := snap.Entries[len(snap.Entries)-1]
			expectedSeq, expectedDigest = last.Sequence, last.Digest
		}
		if seq != expectedSeq || digest != expectedDigest {
			return errors.New("embedded projection changed during restart classification")
		}
		// A claimed wake from a dead owner must be reconciled before rotation.
		return nil
	})
	if err != nil {
		return out, err
	}
	// An untouched launch must precede idle awareness. Classification is not
	// a claim: dispatch repeats this proof and durably reserves the original.
	prompt, _, suppressed, err := s.embeddedPendingOriginal(ctx, b, processGeneration, false)
	if err != nil {
		return out, err
	}
	if suppressed {
		out.Suppressed = true
		return out, nil
	}
	if prompt != "" {
		out.Kind = "pending-original"
		return out, nil
	}

	var previous EmbeddedRestartWake
	err = s.runImmediate(ctx, func() error {
		if e := s.embeddedFence(ctx, b); e != nil {
			return e
		}
		var e error
		previous, e = wakeRow(ctx, s.db, b, "current")
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		return e
	})
	if err != nil {
		return out, err
	}
	if previous.State == "claimed" && previous.ProcessGeneration != processGeneration {
		if err = s.ReconcileEmbeddedRestartWake(ctx, b, previous.ProcessGeneration, previous.ID); err != nil {
			return out, err
		}
		previous, err = wakeRow(ctx, s.db, b, "current")
		if err != nil {
			return out, err
		}
		// The checked completion is terminal but can allow a new generation's
		// separate awareness. UNKNOWN cannot authorize a chained call.
	}
	if previous.ProcessGeneration == processGeneration {
		if previous.State != "planned" {
			out.Suppressed = true
		}
		if previous.State == "planned" {
			out.Kind = previous.Kind
			out.Wake = previous
			if previous.Kind == "interrupted" {
				err = s.db.QueryRowContext(ctx, `SELECT source_seq,state,entry_seq,notice_text FROM embedded_interrupted_notices WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND notice_id=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, previous.ID).Scan(&out.Notice.SourceSequence, &out.Notice.State, &out.Notice.EntrySequence, &out.Notice.Text)
				if err != nil {
					return EmbeddedRestartDecision{Kind: "neutral"}, err
				}
				out.Notice.ID = previous.ID
			}
		}
		return out, nil
	}
	if previous.State == "unknown" || (previous.Kind == "interrupted" && previous.State != "planned") {
		out.Suppressed = true
		return out, nil
	}

	var source EmbeddedEntry
	for i := len(snap.Entries) - 1; i >= 0; i-- {
		if snap.Entries[i].Kind == "user" {
			source = snap.Entries[i]
			break
		}
	}
	if source.Sequence != 0 {
		// The appended 076 user is internal awareness, never a fresh original.
		var noticeSource int64
		e := s.db.QueryRowContext(ctx, `SELECT source_seq FROM embedded_interrupted_notices WHERE agent_id=? AND session_id=? AND epoch=? AND entry_seq=?`, b.AgentID, b.SessionID, b.Epoch, source.Sequence).Scan(&noticeSource)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return out, e
		}
		if e == nil {
			source = EmbeddedEntry{}
			for _, entry := range snap.Entries {
				if entry.Sequence == noticeSource {
					source = entry
					break
				}
			}
			if source.Sequence == 0 || source.Kind != "user" {
				return out, errors.New("interrupted notice source missing")
			}
		}
		// A checked final after the original user proves settlement. Fragments,
		// tool calls and malformed finals cannot grant replay permission.
		for _, entry := range snap.Entries {
			if entry.Sequence > source.Sequence && embeddedCheckedStop(entry.Kind, entry.Body) {
				// Keep settlement neutral. Only an exact entered nonmail source
				// with a checked final at the unchanged head can plan awareness.
				head := snap.Entries[len(snap.Entries)-1]
				if !embeddedCheckedStop(head.Kind, head.Body) {
					return out, nil
				}
				err = s.runImmediate(ctx, func() error {
					if e := s.embeddedFence(ctx, b); e != nil {
						return e
					}
					var newest int64
					if e := s.db.QueryRowContext(ctx, `SELECT generation FROM embedded_process_generation_counter WHERE singleton=1`).Scan(&newest); e != nil {
						return e
					}
					if newest != processGeneration {
						return errors.New("stale process generation")
					}
					seq, digest, e := s.embeddedHead(ctx, b)
					if e != nil {
						return e
					}
					if seq != head.Sequence || digest != head.Digest {
						return errors.New("embedded projection changed during restart classification")
					}
					var entered int
					e = s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries e JOIN embedded_nonmail_provider_entries p ON p.agent_id=e.agent_id AND p.session_id=e.session_id AND p.epoch=e.epoch AND p.generation=e.generation AND p.user_seq=e.seq AND p.user_digest=e.digest WHERE e.agent_id=? AND e.session_id=? AND e.epoch=? AND e.generation=? AND e.seq=? AND e.digest=? AND e.kind='user' AND NOT EXISTS (SELECT 1 FROM embedded_acks k WHERE k.agent_id=e.agent_id AND k.session_id=e.session_id AND k.epoch=e.epoch AND k.entry_seq=e.seq) AND NOT EXISTS (SELECT 1 FROM embedded_entries u WHERE u.agent_id=e.agent_id AND u.session_id=e.session_id AND u.epoch=e.epoch AND u.generation=e.generation AND u.kind='user' AND u.seq>e.seq)`, b.AgentID, b.SessionID, b.Epoch, b.Generation, source.Sequence, source.Digest).Scan(&entered)
					if e == nil && entered == 1 {
						out.Source = source
					}
					return e
				})
				return out, err
			}
		}
		eligible, e := s.EmbeddedUnenteredUser(ctx, b, source.Sequence, source.Digest)
		if e != nil {
			return out, e
		}
		if eligible {
			out.Kind = "unentered-original"
			out.Source = source
			return out, nil
		}
		var entered int
		// A durable mail ACK identifies its exact checked SDK user as entered.
		// Mail has no non-mail provider marker; its provider outcome remains unknown.
		e = s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries e WHERE e.agent_id=? AND e.session_id=? AND e.epoch=? AND e.generation=? AND e.seq=? AND e.digest=? AND e.kind='user' AND (
			EXISTS (SELECT 1 FROM embedded_acks k WHERE k.agent_id=e.agent_id AND k.session_id=e.session_id AND k.epoch=e.epoch AND k.generation=e.generation AND k.entry_seq=e.seq)
			OR EXISTS (SELECT 1 FROM embedded_nonmail_provider_entries p WHERE p.agent_id=e.agent_id AND p.session_id=e.session_id AND p.epoch=e.epoch AND p.generation=e.generation AND p.user_seq=e.seq AND p.user_digest=e.digest AND NOT EXISTS (SELECT 1 FROM embedded_acks k WHERE k.agent_id=p.agent_id AND k.session_id=p.session_id AND k.epoch=p.epoch AND k.entry_seq=p.user_seq))
		)`, b.AgentID, b.SessionID, b.Epoch, b.Generation, source.Sequence, source.Digest).Scan(&entered)
		if e != nil || entered != 1 {
			return out, e
		}
		notice, e := s.PrepareEmbeddedInterruptedNotice(ctx, b, source.Sequence)
		if e != nil {
			return out, e
		}
		if notice == nil {
			return out, nil
		}
		if notice.State == "started" {
			return out, nil
		}
		if notice.State == "pending" {
			payload, e := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: notice.Text}}})
			if e != nil {
				return out, e
			}
			head, digest, e := s.embeddedHead(ctx, b)
			if e != nil {
				return out, e
			}
			_, e = s.AppendEmbeddedInterruptedNotice(ctx, b, *notice, head, digest, payload)
			if e != nil {
				return out, e
			}
			notice.State = "appended"
			notice.EntrySequence = head + 1
		}
		if notice.State != "appended" {
			return out, fmt.Errorf("unrecognized interrupted notice state %q", notice.State)
		}
		head, digest, e := s.embeddedHead(ctx, b)
		if e != nil {
			return out, e
		}
		_ = head
		wake, e := s.ReserveEmbeddedRestartWake(ctx, b, processGeneration, notice.ID, "interrupted", digest)
		if e != nil {
			return out, e
		}
		out.Kind = "interrupted"
		out.Wake = wake
		out.Notice = *notice
		return out, nil
	}
	kind := "root-idle"
	if role == "worker" {
		kind = "worker-idle"
	}
	_, digest, err := s.embeddedHead(ctx, b)
	if err != nil {
		return out, err
	}
	id := fmt.Sprintf("idle:%s:%s:%d:%d", b.AgentID, b.SessionID, b.Epoch, processGeneration)
	if len(id) > 256 {
		return out, errors.New("idle restart identity too long")
	}
	wake, err := s.ReserveEmbeddedRestartWake(ctx, b, processGeneration, id, kind, digest)
	if err != nil {
		return out, err
	}
	out.Kind = kind
	out.Wake = wake
	return out, nil
}
