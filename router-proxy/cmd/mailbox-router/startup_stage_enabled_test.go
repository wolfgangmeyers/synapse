//go:build embedded_private_codex_observer

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
)

func TestStartupStageFixedBoundariesAndPrivacy(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MECHA_EMBEDDED_PRIVATE_CODEX_OBSERVER_DIR", dir)
	if err := managed.PreparePrivateCodexObserver(); err != nil {
		t.Fatal(err)
	}
	secret := "token=email@example.invalid /secret/path request-id prompt header"
	stages := []startupStage{stageCLI, stageCredential, stageConfig, stageStoreManifest, stageHelper, stageListen, stageRootAdmission}
	for _, stage := range stages {
		stage := stage
		called := false
		err := startupRun(stage, func() error {
			called = true
			if _, err := os.Stat(filepath.Join(dir, "startup-"+string(stage)+"-entered.json")); err != nil {
				t.Fatal("not entered before operation", stage, err)
			}
			if stage == stageConfig || stage == stageRootAdmission {
				return errors.New(secret)
			}
			return nil
		})
		if !called {
			t.Fatal("operation not called", stage)
		}
		if stage == stageConfig || stage == stageRootAdmission {
			if err == nil || err.Error() != secret {
				t.Fatal("original error changed", stage)
			}
		} else if err != nil {
			t.Fatal(err)
		}
		for _, outcome := range []string{"entered", "result"} {
			name := "startup-" + string(stage) + "-" + outcome + ".json"
			data, e := os.ReadFile(filepath.Join(dir, name))
			if e != nil {
				t.Fatal(name, e)
			}
			var record map[string]string
			if json.Unmarshal(data, &record) != nil || len(record) != 3 || record["stage"] != string(stage) || record["outcome"] != outcome || strings.Contains(string(data), secret) {
				t.Fatal("invalid fixed record", name)
			}
			info, e := os.Stat(filepath.Join(dir, name))
			if e != nil || info.Mode().Perm() != 0600 {
				t.Fatal("unsafe file mode", name)
			}
		}
	}
	// Nonfatal Snapshot failure has a distinct fixed result, never success.
	helperResult := filepath.Join(dir, "startup-helper-result.json")
	if err := os.Remove(helperResult); err != nil {
		t.Fatal(err)
	}
	if err := startupSnapshotUnavailable(); err != nil {
		t.Fatal(err)
	}
	projection, err := os.ReadFile(helperResult)
	if err != nil || !strings.Contains(string(projection), `"category":"snapshot_unavailable_continued"`) {
		t.Fatal("snapshot category missing")
	}
	// Existing result cannot be overwritten; operation must not run on collision.
	called := false
	if err := startupRun(stageCLI, func() error { called = true; return nil }); err != errStartupObserver || called {
		t.Fatal("collision not fail-closed")
	}
	// A partial exclusive file must remain UNKNOWN, not be silently replaced.
	partial := filepath.Join(dir, "startup-helper-result.json")
	if err := os.Remove(partial); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partial, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := startupSnapshotUnavailable(); err != errStartupObserver {
		t.Fatal("partial record accepted")
	}
	data, _ := os.ReadFile(partial)
	if string(data) != "{" {
		t.Fatal("partial record overwritten")
	}
	if err := managed.WritePrivateStartupStage(secret, "entered", "entered"); err == nil {
		t.Fatal("unbounded stage accepted")
	}
}
