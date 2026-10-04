// Package testfixture holds offline fixtures imported only by tests.
package testfixture

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
)

// PrivateDir fixes the mode of this test's own directory, not TMPDIR or its
// shared ancestors. Go's t.TempDir can inherit a permissive process umask.
func PrivateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// CodexAdmission gives mocked embedded launches a complete router-owned credential.
// Only SDK-registered Codex identities are supported; no live account is used.
func CodexAdmission(t *testing.T) *store.LocalCodexCredentialStore {
	t.Helper()
	for _, id := range []string{"gpt-5.6-sol", "gpt-5.6-terra"} {
		model := ai.GetModel("openai-codex", id)
		if model == nil || model.Api != ai.APIOpenAICodexResponses {
			t.Fatalf("Codex SDK model %s unavailable", id)
		}
	}
	path := filepath.Join(PrivateDir(t), "codex-credential.json")
	credentials, err := store.OpenLocalCodexCredentialStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := credentials.Save(context.Background(), store.CodexCredential{
		AccessToken: "offline-fixture-access", RefreshToken: "offline-fixture-refresh",
		AccountID: "offline-fixture-account", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	return credentials
}
