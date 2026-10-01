//go:build !embedded_private_status_probe

package main

import "github.com/reflexivity/mecha-developer/router-proxy/internal/managed"

// Ordinary binaries never read probe environment variables or configure observers.
func configurePrivateStatusProbe(*managed.Manager) error { return nil }
