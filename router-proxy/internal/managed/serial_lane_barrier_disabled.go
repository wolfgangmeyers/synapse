//go:build !embedded_restart_test_only

package managed

import "context"

func serialLaneTestBarrier(context.Context, string) error { return nil }
