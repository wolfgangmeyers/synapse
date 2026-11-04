//go:build embedded_restart_test_only

package managed

import "context"

var SetRestartMailTestBarrier func(context.Context, string, string) error

func restartMailTestBarrier(ctx context.Context, agentID, phase string) error {
	if SetRestartMailTestBarrier != nil {
		return SetRestartMailTestBarrier(ctx, agentID, phase)
	}
	return nil
}

// OfferDirectEnqueueTest tags only the actual afterEnqueue calls in test builds.
func (m *Manager) OfferDirectEnqueueTest(agentID, messageID, site string) {
	m.offerDirectTagged(agentID, messageID, site)
}

var SetRestartMailGatedReceipt func(context.Context, string, string, string) error

func restartMailGatedReceipt(ctx context.Context, agentID, messageID, site string) error {
	if SetRestartMailGatedReceipt != nil {
		return SetRestartMailGatedReceipt(ctx, agentID, messageID, site)
	}
	return nil
}

func (m *Manager) offerLaunchDirect(agentID string) {
	m.offerDirectTagged(agentID, "", "launch")
}
