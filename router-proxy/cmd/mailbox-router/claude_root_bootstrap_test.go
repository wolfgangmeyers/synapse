package main

import (
	"path/filepath"
	"testing"
)

func TestClaudeRootBootstrapDoesNotRequireCodexCredentials(t *testing.T) {
	dir := t.TempDir()
	base := []string{"--database", filepath.Join(dir, "router.db"), "--embedded-root-bootstrap", "--embedded-root-request-id", "a0666666-6666-4666-8666-666666666666", "--embedded-root-name", "root", "--embedded-root-cwd", dir, "--embedded-root-initial-instruction", "hello"}
	for _, model := range [][]string{nil, {"--embedded-root-model-provider", "claude-code", "--embedded-root-model-id", "sonnet"}} {
		if _, err := parseConfig(append(append([]string{}, base...), model...)); err != nil {
			t.Fatalf("root %v: %v", model, err)
		}
	}
	if _, err := parseConfig(append(append([]string{}, base...), "--embedded-root-model-provider", "claude-code")); err == nil {
		t.Fatal("partial model accepted")
	}
}
