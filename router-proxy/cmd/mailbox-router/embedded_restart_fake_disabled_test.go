//go:build !embedded_restart_test_only

package main

import (
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"testing"
)

func TestEmbeddedRestartFakeAbsentFromNormalBuild(t *testing.T) {
	t.Setenv("EMBEDDED_RESTART_FAKE_ENABLE", "1")
	t.Setenv("EMBEDDED_RESTART_FAKE_ENDPOINT", "http://127.0.0.1:9999")
	opts := managed.ManagerOptions{}
	if err := configureEmbeddedRestartFake(config{}, &opts); err != nil || opts.EmbeddedModelResolver != nil || opts.CodexHTTPTransport != nil || opts.CodexRefresh != nil {
		t.Fatalf("normal binary activated fake: %v", err)
	}
	if _, err := parseConfig([]string{"--database", "/unused", "--embedded-restart-fake-endpoint", "http://127.0.0.1:9999"}); err == nil {
		t.Fatal("normal binary accepted fake flag")
	}
}
