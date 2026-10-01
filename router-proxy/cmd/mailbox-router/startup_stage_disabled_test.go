//go:build !embedded_private_codex_observer

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestUntaggedStartupStagesDoNotWrite(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MECHA_EMBEDDED_PRIVATE_CODEX_OBSERVER_DIR", dir)
	secret := errors.New("private fixture")
	called := false
	if err := startupRun(stageCredential, func() error { called = true; return secret }); err != secret || !called {
		t.Fatal("untagged operation changed")
	}
	if err := startupSnapshotUnavailable(); err != nil {
		t.Fatal(err)
	}
	if err := startupEntered(stageListen); err != nil {
		t.Fatal(err)
	}
	if err := startupResult(stageListen, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "startup-listen-entered.json")); !os.IsNotExist(err) {
		t.Fatal("untagged observer wrote output")
	}
}
