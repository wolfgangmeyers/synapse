//go:build !embedded_private_status_probe

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
)

func TestOrdinaryBinaryCannotActivatePrivateProbe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "should-not-exist")
	t.Setenv("MECHA_EMBEDDED_PRIVATE_STATUS_PROBE_FILE", path)
	if err := configurePrivateStatusProbe(&managed.Manager{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("ordinary build opened private probe path: %v", err)
	}
}
