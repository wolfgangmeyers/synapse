package managed

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/codexdevice"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

var (
	codexSearchAuthMu sync.Mutex
	// codexOAuthIssuerBase allows unit tests to point the token refresh request to a test server.
	codexOAuthIssuerBase = "https://auth.openai.com"
)

type codexCandidateAuth struct {
	path      string
	allJSON   map[string]any
	isCodex   bool   // true if ~/.codex/auth.json format
	keyName   string // "openai-codex" or "openai" when isCodex == false
	tokenMap  map[string]any
	access    string
	refresh   string
	accountID string
	expiresAt time.Time
}

// hasCodexSearchAuth reports true if valid or refreshable OpenAI/Codex credentials are available.
func (m *Manager) hasCodexSearchAuth(ctx context.Context) bool {
	if m == nil {
		return false
	}
	token, _, err := m.resolveCodexSearchToken(ctx)
	return err == nil && token != ""
}

// resolveCodexSearchToken extracts an access token and ChatGPT account ID from:
// 1. m.config.PiAgentDir/auth.json
// 2. ~/.pi/agent/auth.json
// 3. ~/.codex/auth.json
// If the token is expired or within 60 seconds of expiration, it refreshes the token via
// codexdevice.RefreshWithBaseURL and safely updates the auth file.
func (m *Manager) resolveCodexSearchToken(ctx context.Context) (string, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	codexSearchAuthMu.Lock()
	defer codexSearchAuthMu.Unlock()

	candidate, err := m.findCodexAuthCandidate()
	if err != nil {
		return "", "", err
	}

	// Check if token is nearing expiration (within 60 seconds) or already expired.
	now := time.Now()
	needsRefresh := candidate.expiresAt.IsZero() || !now.Add(60*time.Second).Before(candidate.expiresAt)

	if needsRefresh && candidate.refresh != "" {
		refreshed, rErr := m.refreshCodexCandidate(ctx, candidate)
		if rErr != nil {
			// If refresh fails but current token is not actually expired yet, we can still use it.
			if !candidate.expiresAt.IsZero() && now.Before(candidate.expiresAt) && candidate.access != "" {
				return candidate.access, candidate.accountID, nil
			}
			return "", "", fmt.Errorf("failed to refresh codex token: %w", rErr)
		}
		candidate = refreshed
	}

	if candidate.access == "" {
		return "", "", errors.New("no codex access token found")
	}

	return candidate.access, candidate.accountID, nil
}

func (m *Manager) codexCandidatePaths() []string {
	var paths []string
	if m != nil && m.config.PiAgentDir != "" {
		paths = append(paths, filepath.Join(m.config.PiAgentDir, "auth.json"))
	}

	userHome, _ := os.UserHomeDir()
	envHome := os.Getenv("HOME")

	addHome := func(h string) {
		if h == "" {
			return
		}
		paths = append(paths,
			filepath.Join(h, ".pi", "agent", "auth.json"),
			filepath.Join(h, ".codex", "auth.json"),
		)
	}

	addHome(userHome)
	if envHome != "" && envHome != userHome {
		addHome(envHome)
	}

	// Deduplicate while preserving priority order
	seen := make(map[string]bool)
	var unique []string
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		unique = append(unique, p)
	}
	return unique
}

func (m *Manager) findCodexAuthCandidate() (*codexCandidateAuth, error) {
	paths := m.codexCandidatePaths()

	for _, path := range paths {
		cand, err := parseCodexAuthFile(path)
		if err == nil && cand != nil {
			return cand, nil
		}
	}

	return nil, errors.New("no codex credentials found in auth files")
}

func parseCodexAuthFile(path string) (*codexCandidateAuth, error) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil, os.ErrNotExist
	}

	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}

	// 1. Check if ~/.codex/auth.json format: has "tokens" object
	if tokensRaw, ok := root["tokens"].(map[string]any); ok && tokensRaw != nil {
		access, _ := tokensRaw["access_token"].(string)
		refresh, _ := tokensRaw["refresh_token"].(string)
		accountID, _ := tokensRaw["account_id"].(string)

		if access != "" || refresh != "" {
			cand := &codexCandidateAuth{
				path:      path,
				allJSON:   root,
				isCodex:   true,
				tokenMap:  tokensRaw,
				access:    access,
				refresh:   refresh,
				accountID: accountID,
			}
			cand.expiresAt, _ = extractExpiry(tokensRaw, access)
			if cand.accountID == "" {
				cand.accountID = extractAccountIDFromJWT(access)
			}
			return cand, nil
		}
	}

	// 2. Check ~/.pi/agent/auth.json format: keys "openai-codex" or "openai"
	keys := []string{"openai-codex", "openai"}
	for _, k := range keys {
		subRaw, ok := root[k].(map[string]any)
		if !ok || subRaw == nil {
			continue
		}

		access, _ := subRaw["access"].(string)
		if access == "" {
			access, _ = subRaw["access_token"].(string)
		}
		refresh, _ := subRaw["refresh"].(string)
		if refresh == "" {
			refresh, _ = subRaw["refresh_token"].(string)
		}
		accountID, _ := subRaw["accountId"].(string)
		if accountID == "" {
			accountID, _ = subRaw["account_id"].(string)
		}

		if access != "" || refresh != "" {
			cand := &codexCandidateAuth{
				path:      path,
				allJSON:   root,
				isCodex:   false,
				keyName:   k,
				tokenMap:  subRaw,
				access:    access,
				refresh:   refresh,
				accountID: accountID,
			}
			cand.expiresAt, _ = extractExpiry(subRaw, access)
			if cand.accountID == "" {
				cand.accountID = extractAccountIDFromJWT(access)
			}
			return cand, nil
		}
	}

	return nil, errors.New("no codex or openai tokens in auth file")
}

func extractExpiry(m map[string]any, accessToken string) (time.Time, bool) {
	// First check explicit fields: "expires" or "expires_at"
	for _, expField := range []string{"expires", "expires_at"} {
		if raw, ok := m[expField]; ok && raw != nil {
			var ms int64
			switch v := raw.(type) {
			case float64:
				ms = int64(v)
			case int64:
				ms = v
			case int:
				ms = int64(v)
			case json.Number:
				ms, _ = v.Int64()
			case string:
				if t, err := time.Parse(time.RFC3339, v); err == nil {
					return t, true
				}
			}
			if ms > 0 {
				// If seconds vs milliseconds: ms > 1e11 is milliseconds
				if ms < 1e11 {
					return time.Unix(ms, 0), true
				}
				return time.UnixMilli(ms), true
			}
		}
	}

	// Fallback to inspecting JWT exp claim
	if accessToken != "" {
		if exp := extractExpiryFromJWT(accessToken); !exp.IsZero() {
			return exp, true
		}
	}

	return time.Time{}, false
}

func extractExpiryFromJWT(jwtToken string) time.Time {
	parts := strings.Split(jwtToken, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	claimBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp json.Number `json:"exp"`
	}
	if err := json.Unmarshal(claimBytes, &claims); err != nil {
		return time.Time{}
	}
	sec, err := claims.Exp.Int64()
	if err != nil || sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}

func extractAccountIDFromJWT(jwtToken string) string {
	parts := strings.Split(jwtToken, ".")
	if len(parts) != 3 {
		return ""
	}
	claimBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var jwt map[string]json.RawMessage
	if err := json.Unmarshal(claimBytes, &jwt); err != nil {
		return ""
	}
	authRaw, ok := jwt["https://api.openai.com/auth"]
	if !ok {
		return ""
	}
	var auth struct {
		AccountID string `json:"chatgpt_account_id"`
	}
	if err := json.Unmarshal(authRaw, &auth); err != nil {
		return ""
	}
	return auth.AccountID
}

func (m *Manager) refreshCodexCandidate(ctx context.Context, cand *codexCandidateAuth) (*codexCandidateAuth, error) {
	if cand.refresh == "" {
		return nil, errors.New("cannot refresh without refresh token")
	}

	old := store.CodexCredential{
		AccessToken:  cand.access,
		RefreshToken: cand.refresh,
		AccountID:    cand.accountID,
		ExpiresAt:    cand.expiresAt,
	}

	client := &http.Client{Timeout: 30 * time.Second}
	refreshed, err := codexdevice.RefreshWithBaseURL(ctx, client, codexOAuthIssuerBase, old)
	if err != nil {
		return nil, err
	}

	// Update candidate struct
	cand.access = refreshed.AccessToken
	cand.refresh = refreshed.RefreshToken
	if refreshed.AccountID != "" {
		cand.accountID = refreshed.AccountID
	}
	cand.expiresAt = refreshed.ExpiresAt

	// Update in-memory JSON structure
	if cand.isCodex {
		cand.tokenMap["access_token"] = refreshed.AccessToken
		cand.tokenMap["refresh_token"] = refreshed.RefreshToken
		if refreshed.AccountID != "" {
			cand.tokenMap["account_id"] = refreshed.AccountID
		}
		cand.allJSON["tokens"] = cand.tokenMap
		cand.allJSON["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)
	} else {
		// Pi format
		if _, hasAccess := cand.tokenMap["access"]; hasAccess {
			cand.tokenMap["access"] = refreshed.AccessToken
		} else {
			cand.tokenMap["access_token"] = refreshed.AccessToken
		}
		if _, hasRefresh := cand.tokenMap["refresh"]; hasRefresh {
			cand.tokenMap["refresh"] = refreshed.RefreshToken
		} else {
			cand.tokenMap["refresh_token"] = refreshed.RefreshToken
		}
		if refreshed.AccountID != "" {
			if _, hasAcc := cand.tokenMap["accountId"]; hasAcc {
				cand.tokenMap["accountId"] = refreshed.AccountID
			} else {
				cand.tokenMap["account_id"] = refreshed.AccountID
			}
		}
		cand.tokenMap["expires"] = refreshed.ExpiresAt.UnixMilli()
		cand.allJSON[cand.keyName] = cand.tokenMap
	}

	// Safely write to file
	if err := safeWriteAuthJSON(cand.path, cand.allJSON); err != nil {
		return nil, fmt.Errorf("failed to persist refreshed codex tokens: %w", err)
	}

	return cand, nil
}

func safeWriteAuthJSON(authPath string, allAuth map[string]any) error {
	out, err := json.MarshalIndent(allAuth, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal updated auth.json: %w", err)
	}
	out = append(out, '\n')

	dir := filepath.Dir(authPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create directory for auth file: %w", err)
	}

	tmpFile, err := os.CreateTemp(dir, "auth.*.json.tmp")
	if err != nil {
		return os.WriteFile(authPath, out, 0600)
	}
	tmpName := tmpFile.Name()
	defer os.Remove(tmpName)

	if err := tmpFile.Chmod(0600); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to chmod temp auth file: %w", err)
	}
	if _, err := tmpFile.Write(out); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to write temp auth file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to sync temp auth file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp auth file: %w", err)
	}

	if err := os.Rename(tmpName, authPath); err != nil {
		return fmt.Errorf("failed to replace auth file: %w", err)
	}
	return nil
}
