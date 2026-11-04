//go:build embedded_restart_test_only

package managed

import "context"

// SetSerialLaneTestBarrier is wired only by the disposable test router.
var SetSerialLaneTestBarrier func(context.Context, string) error

func serialLaneTestBarrier(ctx context.Context, agentID string) error {
	if SetSerialLaneTestBarrier != nil {
		return SetSerialLaneTestBarrier(ctx, agentID)
	}
	return nil
}
