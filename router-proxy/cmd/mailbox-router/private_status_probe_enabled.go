//go:build embedded_private_status_probe

package main

import (
	"os"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
)

// Explicit one-shot opt-in. No default destination and no runtime endpoint.
func configurePrivateStatusProbe(m *managed.Manager) error {
	path := os.Getenv("MECHA_EMBEDDED_PRIVATE_STATUS_PROBE_FILE")
	if path == "" {
		return nil
	}
	return managed.EnablePrivateStatusProbe(m, path)
}
