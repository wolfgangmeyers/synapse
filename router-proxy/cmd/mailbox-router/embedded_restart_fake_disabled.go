//go:build !embedded_restart_test_only

package main

import "github.com/reflexivity/mecha-developer/router-proxy/internal/managed"

// The production executable has no fake-provider configuration or network path.
func configureEmbeddedRestartFake(config, *managed.ManagerOptions) error { return nil }
