//go:build !embedded_private_status_probe

package managed

import "testing"

func setupPrivateStatusProbeRetryTest(_ *testing.T, _ *Manager, _ string, _ int, _ string) string {
	return ""
}
func verifyPrivateStatusProbeRetryTest(_ *testing.T, _ string) {}
