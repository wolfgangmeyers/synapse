package store

import (
	"context"
	"errors"
	"fmt"
)

// ScheduleEmbeddedSettledOriginalWake is the explicit post-Continue seam. It
// plans neutral awareness only after the original checked user has entered and
// its checked final is the projection head. It never dispatches the plan.
func (s *Store) ScheduleEmbeddedSettledOriginalWake(ctx context.Context, b EmbeddedBinding, processGeneration, sourceSeq int64, sourceDigest string) (EmbeddedRestartWake, error) {
	var wake EmbeddedRestartWake
	if sourceSeq <= 0 || sourceDigest == "" || processGeneration <= 0 {
		return wake, errors.New("invalid settled original source")
	}
	err := s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		var kind, digest string
		if err := s.db.QueryRowContext(ctx, `SELECT kind,digest FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND seq=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, sourceSeq).Scan(&kind, &digest); err != nil {
			return err
		}
		if kind != "user" || digest != sourceDigest {
			return errors.New("settled original source mismatch")
		}
		var entered int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_nonmail_provider_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND user_seq=? AND user_digest=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, sourceSeq, sourceDigest).Scan(&entered); err != nil {
			return err
		}
		if entered != 1 {
			return errors.New("original provider entry missing")
		}
		var latestUser, headSeq int64
		var body []byte
		if err := s.db.QueryRowContext(ctx, `SELECT max(seq) FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND kind='user'`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&latestUser); err != nil {
			return err
		}
		if latestUser != sourceSeq {
			return errors.New("original is no longer the latest user")
		}
		if err := s.db.QueryRowContext(ctx, `SELECT seq,digest,kind,body FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? ORDER BY seq DESC LIMIT 1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&headSeq, &digest, &kind, &body); err != nil {
			return err
		}
		if headSeq <= sourceSeq || !embeddedCheckedStop(kind, body) {
			return errors.New("original checked final missing")
		}
		role, err := s.EmbeddedSessionRole(ctx, b)
		if err != nil {
			return err
		}
		wakeKind := "root-idle"
		if role == "worker" {
			wakeKind = "worker-idle"
		}
		id := fmt.Sprintf("settled:%s:%d:%d", b.AgentID, sourceSeq, processGeneration)
		if len(id) > 256 {
			return errors.New("settled wake identity too long")
		}
		return s.reserveEmbeddedRestartWakeTx(ctx, b, processGeneration, id, wakeKind, digest, &wake)
	})
	return wake, err
}
