package testfixture

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sky-valley/pi/ai"
)

func TestCodexAdmissionPrivatePathAndRegisteredIdentities(t *testing.T) {
	credentials := CodexAdmission(t)
	for _, id := range []string{"gpt-5.6-sol", "gpt-5.6-terra"} {
		model := ai.GetModel("openai-codex", id)
		if model == nil || model.Provider != "openai-codex" || model.ID != id || model.Api != ai.APIOpenAICodexResponses {
			t.Fatalf("incorrect SDK Codex identity: %s", id)
		}
	}
	if ai.GetModel("openai-codex", "not-a-codex-model") != nil {
		t.Fatal("unsupported model registered")
	}
	credential, err := credentials.Load(context.Background())
	if err != nil || credential.AccountID != "offline-fixture-account" {
		t.Fatalf("offline credential unavailable: %v", err)
	}

	// The credential store enforces mode on every component. Check the test
	// fixture's own directory explicitly, including with a permissive umask.
	dir := PrivateDir(t)
	info, err := os.Stat(filepath.Clean(dir))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("test directory is not private: %v, %v", info, err)
	}
}
