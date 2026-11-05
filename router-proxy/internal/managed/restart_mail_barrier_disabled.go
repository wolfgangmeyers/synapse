//go:build !embedded_restart_test_only

package managed

import "context"

func restartMailTestBarrier(context.Context, string, string) error { return nil }

func restartMailGatedReceipt(context.Context, string, string, string) error { return nil }

func (m *Manager) offerLaunchDirect(agentID string) { m.OfferDirect(agentID) }
