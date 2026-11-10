package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
)

// EnqueueEmbeddedOwnerNotification admits an owner-directed notice only while
// the exact active managed binding, token and owner identity remain current.
// The fence and ordinary enqueue share one SQLite write transaction. Callers
// must still publish the resulting conversation and wake the owner as usual.
func (s *Store) EnqueueEmbeddedOwnerNotification(ctx context.Context, binding EmbeddedBinding, owner string, envelope protocol.Envelope, options ...EnqueueOptions) (EnqueueResult, error) {
	if owner == "" || len(owner) > 256 || binding.OwnerToken <= 0 {
		return EnqueueResult{}, errors.New("invalid embedded notification owner")
	}
	if envelope.To != binding.AgentID {
		return EnqueueResult{}, errors.New("embedded notification recipient must match owner")
	}
	option := EnqueueOptions{}
	if len(options) > 0 {
		option = options[0]
	}
	option.ownerNotificationDistinct = true
	return s.enqueue(ctx, envelope, &option, nil, func() error {
		if err := s.embeddedFence(ctx, binding); err != nil {
			return err
		}
		var count int
		err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1 AND owner_token=? AND owner_id=?`, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation, binding.OwnerToken, owner).Scan(&count)
		if err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("%w: stale embedded owner", ErrStaleEmbeddedFence)
		}
		return nil
	})
}
