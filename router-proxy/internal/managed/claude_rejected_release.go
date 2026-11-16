package managed

import (
	"context"
	"errors"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"log"
	"time"
)

type claudePendingRejectedRelease struct {
	attempt  *store.EmbeddedMailAttempt
	deadline time.Time
	unknown  bool
}
type rejectedReleaseDisposition int

const (
	rejectedReleaseDone rejectedReleaseDisposition = iota
	rejectedReleaseRetry
	rejectedReleaseStale
	rejectedReleaseStopped
)

func retryRejectedRelease(ctx context.Context, pending *claudePendingRejectedRelease, release func(context.Context, store.EmbeddedBinding, *store.EmbeddedMailAttempt) error) (rejectedReleaseDisposition, error) {
	if err := ctx.Err(); err != nil {
		return rejectedReleaseStopped, err
	}
	if pending.unknown {
		return rejectedReleaseStopped, nil
	}
	err := release(ctx, pending.attempt.Binding, pending.attempt)
	var committed interface{ Committed() bool }
	if errors.As(err, &committed) && committed.Committed() {
		log.Printf("[embedded:%s] rejected mail release committed with error (message: %s, attempt: %s): %v", pending.attempt.Binding.AgentID, pending.attempt.MessageID, pending.attempt.AttemptID, err)
		return rejectedReleaseDone, err
	}
	var unknown interface{ OutcomeUnknown() bool }
	if errors.As(err, &unknown) && unknown.OutcomeUnknown() {
		pending.unknown = true
		log.Printf("[embedded:%s] rejected mail release outcome unknown (message: %s, attempt: %s): %v", pending.attempt.Binding.AgentID, pending.attempt.MessageID, pending.attempt.AttemptID, err)
		return rejectedReleaseStopped, err
	}
	if errors.Is(err, store.ErrStaleEmbeddedFence) {
		return rejectedReleaseStale, err
	}
	if err != nil {
		return rejectedReleaseRetry, err
	}
	return rejectedReleaseDone, nil
}
