//go:build embedded_private_status_probe

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
)

func TestTaggedProbeRequiresExplicitPrivateFile(t *testing.T) {
	private := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(private, "status.jsonl")
	t.Setenv("MECHA_EMBEDDED_PRIVATE_STATUS_PROBE_FILE", "")
	if err := configurePrivateStatusProbe(&managed.Manager{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("unset probe created file: %v", err)
	}
	t.Setenv("MECHA_EMBEDDED_PRIVATE_STATUS_PROBE_FILE", path)
	if err := configurePrivateStatusProbe(&managed.Manager{}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 || info.Size() != 0 {
		t.Fatalf("invalid probe file: %v %v", info, err)
	}
	if err := configurePrivateStatusProbe(&managed.Manager{}); err == nil {
		t.Fatal("existing probe file was accepted")
	}
}
