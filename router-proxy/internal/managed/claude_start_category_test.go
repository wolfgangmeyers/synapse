package managed

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeStartCategoryPreflight(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		category   claudeStartCategory
	}{
		{"auth", `echo '{"loggedIn":false}'`, claudeStartAuth},
		{"malformed", `echo nope`, claudeStartUnknown},
		{"missing field", `echo '{}'`, claudeStartUnknown},
		{"exit", `exit 73`, claudeStartUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), "claude")
			if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+tc.body+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			_, err := PreflightClaudeCode(context.Background(), ClaudeCodeProcessConfig{Binary: bin})
			if err == nil || claudeStartCategoryOf(err) != tc.category {
				t.Fatalf("category %q error %v", claudeStartCategoryOf(err), err)
			}
		})
	}
	_, err := PreflightClaudeCode(context.Background(), ClaudeCodeProcessConfig{Binary: filepath.Join(t.TempDir(), "missing")})
	if claudeStartCategoryOf(err) != claudeStartSpawn || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing binary: %v", err)
	}
}

func TestClaudeStartCategoryPreservesCauseAndSafetyMarker(t *testing.T) {
	cause := errors.New("secret /private/path")
	for _, category := range []claudeStartCategory{claudeStartAuth, claudeStartSpawn, claudeStartProbeDied, claudeStartPoolFenced, claudeStartGrantFailed} {
		err := errors.Join(errClaudeDefiniteStart, claudeStartCategorize(category, cause))
		if !errors.Is(err, cause) || !errors.Is(err, errClaudeDefiniteStart) || claudeStartCategoryOf(err) != category {
			t.Fatalf("category %s: %v", category, err)
		}
	}
	if got := claudeStartCategoryOf(claudeStartCategorize("untrusted", cause)); got != claudeStartUnknown {
		t.Fatalf("unknown category = %s", got)
	}
	if errors.Is(claudeStartCategorize(claudeStartAuth, cause), errClaudeDefiniteStart) {
		t.Fatal("category must not authorize replay")
	}
}
