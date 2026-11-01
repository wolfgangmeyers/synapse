package managed

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func makeTestJWT(accountID string, exp time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payloadData := map[string]any{
		"https://api.openai.com/auth": map[string]string{
			"chatgpt_account_id": accountID,
		},
		"exp": exp.Unix(),
	}
	payloadBytes, _ := json.Marshal(payloadData)
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	return fmt.Sprintf("%s.%s.sig", header, payload)
}

func TestResolveCodexSearchToken_PiAgentDir_Valid(t *testing.T) {
	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "auth.json")

	validExp := time.Now().Add(1 * time.Hour)
	jwt := makeTestJWT("acc-12345", validExp)

	authContent := fmt.Sprintf(`{
		"openai-codex": {
			"access": %q,
			"refresh": "refresh-token-1",
			"accountId": "acc-12345",
			"expires": %d
		}
	}`, jwt, validExp.UnixMilli())

	require.NoError(t, os.WriteFile(authPath, []byte(authContent), 0600))

	m := &Manager{config: Config{PiAgentDir: tempDir}}
	token, accID, err := m.resolveCodexSearchToken(context.Background())
	require.NoError(t, err)
	require.Equal(t, jwt, token)
	require.Equal(t, "acc-12345", accID)
	require.True(t, m.hasCodexSearchAuth(context.Background()))
}

func TestResolveCodexSearchToken_CodexAuthFile(t *testing.T) {
	tempDir := t.TempDir()
	codexDir := filepath.Join(tempDir, ".codex")
	require.NoError(t, os.MkdirAll(codexDir, 0700))
	authPath := filepath.Join(codexDir, "auth.json")

	t.Setenv("HOME", tempDir)

	validExp := time.Now().Add(2 * time.Hour)
	jwt := makeTestJWT("codex-acc-999", validExp)

	authContent := fmt.Sprintf(`{
		"auth_mode": "chatgpt",
		"tokens": {
			"access_token": %q,
			"refresh_token": "codex-refresh",
			"account_id": "codex-acc-999"
		}
	}`, jwt)

	require.NoError(t, os.WriteFile(authPath, []byte(authContent), 0600))

	m := &Manager{config: Config{PiAgentDir: filepath.Join(tempDir, "nonexistent-pi-agent")}}
	token, accID, err := m.resolveCodexSearchToken(context.Background())
	require.NoError(t, err)
	require.Equal(t, jwt, token)
	require.Equal(t, "codex-acc-999", accID)
	require.True(t, m.hasCodexSearchAuth(context.Background()))
}

func TestResolveCodexSearchToken_OpenAIKeyFallback(t *testing.T) {
	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "auth.json")

	validExp := time.Now().Add(1 * time.Hour)
	jwt := makeTestJWT("openai-acc-777", validExp)

	// Auth file with "openai" instead of "openai-codex"
	authContent := fmt.Sprintf(`{
		"openai": {
			"access": %q,
			"refresh": "openai-refresh",
			"expires": %d
		}
	}`, jwt, validExp.UnixMilli())

	require.NoError(t, os.WriteFile(authPath, []byte(authContent), 0600))

	m := &Manager{config: Config{PiAgentDir: tempDir}}
	token, accID, err := m.resolveCodexSearchToken(context.Background())
	require.NoError(t, err)
	require.Equal(t, jwt, token)
	// Extracted from JWT claim because accountId wasn't in root map
	require.Equal(t, "openai-acc-777", accID)
	require.True(t, m.hasCodexSearchAuth(context.Background()))
}

func TestResolveCodexSearchToken_ExpiredRefreshesSuccessfully(t *testing.T) {
	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "auth.json")

	expiredExp := time.Now().Add(-10 * time.Minute)
	oldJWT := makeTestJWT("acc-old", expiredExp)
	newExp := time.Now().Add(1 * time.Hour)
	newJWT := makeTestJWT("acc-refreshed", newExp)

	// Setup fake OAuth issuer
	mockOAuth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/oauth/token", r.URL.Path)
		require.Equal(t, http.MethodPost, r.Method)
		require.NoError(t, r.ParseForm())
		require.Equal(t, "refresh_token", r.FormValue("grant_type"))
		require.Equal(t, "old-refresh-123", r.FormValue("refresh_token"))

		resp := map[string]any{
			"access_token":  newJWT,
			"refresh_token": "new-refresh-456",
			"expires_in":    3600,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockOAuth.Close()

	origIssuer := codexOAuthIssuerBase
	codexOAuthIssuerBase = mockOAuth.URL
	defer func() { codexOAuthIssuerBase = origIssuer }()

	authContent := fmt.Sprintf(`{
		"openai-codex": {
			"access": %q,
			"refresh": "old-refresh-123",
			"accountId": "acc-old",
			"expires": %d
		}
	}`, oldJWT, expiredExp.UnixMilli())

	require.NoError(t, os.WriteFile(authPath, []byte(authContent), 0600))

	m := &Manager{config: Config{PiAgentDir: tempDir}}
	token, accID, err := m.resolveCodexSearchToken(context.Background())
	require.NoError(t, err)
	require.Equal(t, newJWT, token)
	require.Equal(t, "acc-refreshed", accID)

	// Verify file was updated on disk
	updatedData, err := os.ReadFile(authPath)
	require.NoError(t, err)
	var updatedMap map[string]any
	require.NoError(t, json.Unmarshal(updatedData, &updatedMap))
	subMap := updatedMap["openai-codex"].(map[string]any)
	require.Equal(t, newJWT, subMap["access"])
	require.Equal(t, "new-refresh-456", subMap["refresh"])
	require.Equal(t, "acc-refreshed", subMap["accountId"])
}

func TestResolveCodexSearchToken_NearingExpirationRefreshes(t *testing.T) {
	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "auth.json")

	// Expiring in 30 seconds (less than 60s threshold)
	nearExp := time.Now().Add(30 * time.Second)
	oldJWT := makeTestJWT("acc-near", nearExp)
	newExp := time.Now().Add(1 * time.Hour)
	newJWT := makeTestJWT("acc-refreshed", newExp)

	mockOAuth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"access_token":  newJWT,
			"refresh_token": "new-refresh-999",
			"expires_in":    3600,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockOAuth.Close()

	origIssuer := codexOAuthIssuerBase
	codexOAuthIssuerBase = mockOAuth.URL
	defer func() { codexOAuthIssuerBase = origIssuer }()

	authContent := fmt.Sprintf(`{
		"openai-codex": {
			"access": %q,
			"refresh": "refresh-near",
			"accountId": "acc-near",
			"expires": %d
		}
	}`, oldJWT, nearExp.UnixMilli())

	require.NoError(t, os.WriteFile(authPath, []byte(authContent), 0600))

	m := &Manager{config: Config{PiAgentDir: tempDir}}
	token, accID, err := m.resolveCodexSearchToken(context.Background())
	require.NoError(t, err)
	require.Equal(t, newJWT, token)
	require.Equal(t, "acc-refreshed", accID)
}

func TestResolveCodexSearchToken_CodexFormatRefresh(t *testing.T) {
	tempDir := t.TempDir()
	codexDir := filepath.Join(tempDir, ".codex")
	require.NoError(t, os.MkdirAll(codexDir, 0700))
	authPath := filepath.Join(codexDir, "auth.json")

	t.Setenv("HOME", tempDir)

	expiredExp := time.Now().Add(-5 * time.Minute)
	oldJWT := makeTestJWT("acc-codex-old", expiredExp)
	newExp := time.Now().Add(1 * time.Hour)
	newJWT := makeTestJWT("acc-codex-new", newExp)

	mockOAuth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"access_token":  newJWT,
			"refresh_token": "codex-refreshed-rt",
			"expires_in":    3600,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockOAuth.Close()

	origIssuer := codexOAuthIssuerBase
	codexOAuthIssuerBase = mockOAuth.URL
	defer func() { codexOAuthIssuerBase = origIssuer }()

	authContent := fmt.Sprintf(`{
		"auth_mode": "chatgpt",
		"tokens": {
			"access_token": %q,
			"refresh_token": "codex-old-rt",
			"account_id": "acc-codex-old"
		}
	}`, oldJWT)

	require.NoError(t, os.WriteFile(authPath, []byte(authContent), 0600))

	m := &Manager{config: Config{PiAgentDir: filepath.Join(tempDir, "nonexistent-pi-agent")}}
	token, accID, err := m.resolveCodexSearchToken(context.Background())
	require.NoError(t, err)
	require.Equal(t, newJWT, token)
	require.Equal(t, "acc-codex-new", accID)

	// Check file was updated in .codex structure
	data, err := os.ReadFile(authPath)
	require.NoError(t, err)
	var root map[string]any
	require.NoError(t, json.Unmarshal(data, &root))
	tokens := root["tokens"].(map[string]any)
	require.Equal(t, newJWT, tokens["access_token"])
	require.Equal(t, "codex-refreshed-rt", tokens["refresh_token"])
	require.Equal(t, "acc-codex-new", tokens["account_id"])
}

func TestResolveCodexSearchToken_MissingCredentialsGraceful(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)

	m := &Manager{config: Config{PiAgentDir: filepath.Join(tempDir, "empty-agent")}}
	token, accID, err := m.resolveCodexSearchToken(context.Background())
	require.Error(t, err)
	require.Empty(t, token)
	require.Empty(t, accID)
	require.False(t, m.hasCodexSearchAuth(context.Background()))

	// Nil manager check
	var nilMgr *Manager
	require.False(t, nilMgr.hasCodexSearchAuth(context.Background()))
}

func TestResolveCodexSearchToken_RefreshFailureFallbackWhenValid(t *testing.T) {
	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "auth.json")

	// Token still valid for 45 seconds (less than 60s, triggers refresh attempt)
	stillValidExp := time.Now().Add(45 * time.Second)
	jwt := makeTestJWT("acc-fallback", stillValidExp)

	// Mock server that returns 500 error
	mockOAuth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer mockOAuth.Close()

	origIssuer := codexOAuthIssuerBase
	codexOAuthIssuerBase = mockOAuth.URL
	defer func() { codexOAuthIssuerBase = origIssuer }()

	authContent := fmt.Sprintf(`{
		"openai-codex": {
			"access": %q,
			"refresh": "refresh-fallback",
			"accountId": "acc-fallback",
			"expires": %d
		}
	}`, jwt, stillValidExp.UnixMilli())

	require.NoError(t, os.WriteFile(authPath, []byte(authContent), 0600))

	m := &Manager{config: Config{PiAgentDir: tempDir}}
	// Because token is still not strictly expired yet, fallback to existing token succeeds
	token, accID, err := m.resolveCodexSearchToken(context.Background())
	require.NoError(t, err)
	require.Equal(t, jwt, token)
	require.Equal(t, "acc-fallback", accID)
}

func TestResolveCodexSearchToken_RefreshFailureWhenExpired(t *testing.T) {
	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "auth.json")

	expiredExp := time.Now().Add(-1 * time.Minute)
	jwt := makeTestJWT("acc-fail", expiredExp)

	mockOAuth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer mockOAuth.Close()

	origIssuer := codexOAuthIssuerBase
	codexOAuthIssuerBase = mockOAuth.URL
	defer func() { codexOAuthIssuerBase = origIssuer }()

	authContent := fmt.Sprintf(`{
		"openai-codex": {
			"access": %q,
			"refresh": "refresh-bad",
			"accountId": "acc-fail",
			"expires": %d
		}
	}`, jwt, expiredExp.UnixMilli())

	require.NoError(t, os.WriteFile(authPath, []byte(authContent), 0600))

	m := &Manager{config: Config{PiAgentDir: tempDir}}
	token, accID, err := m.resolveCodexSearchToken(context.Background())
	require.Error(t, err)
	require.Empty(t, token)
	require.Empty(t, accID)
	require.False(t, m.hasCodexSearchAuth(context.Background()))
}
