package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestPKCEGeneration(t *testing.T) {
	verifier, challenge, err := GeneratePKCE()
	if err != nil {
		t.Fatalf("GeneratePKCE failed: %v", err)
	}
	if len(verifier) < 43 {
		t.Fatalf("verifier too short: %d", len(verifier))
	}
	if len(challenge) == 0 {
		t.Fatalf("challenge cannot be empty")
	}
}

func TestBuildAuthorizationURL(t *testing.T) {
	authURL, err := BuildAuthorizationURL(
		"https://auth.example.com/oauth/authorize",
		"client-123",
		"http://localhost:8765/callback",
		"state-xyz",
		"challenge-abc",
		"read write",
		"https://podclave.com/mcp",
	)
	if err != nil {
		t.Fatalf("BuildAuthorizationURL failed: %v", err)
	}

	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("invalid url: %v", err)
	}

	q := u.Query()
	if q.Get("response_type") != "code" {
		t.Errorf("expected response_type=code, got %s", q.Get("response_type"))
	}
	if q.Get("client_id") != "client-123" {
		t.Errorf("expected client_id=client-123, got %s", q.Get("client_id"))
	}
	if q.Get("redirect_uri") != "http://localhost:8765/callback" {
		t.Errorf("expected redirect_uri, got %s", q.Get("redirect_uri"))
	}
	if q.Get("state") != "state-xyz" {
		t.Errorf("expected state=state-xyz, got %s", q.Get("state"))
	}
	if q.Get("code_challenge") != "challenge-abc" {
		t.Errorf("expected code_challenge=challenge-abc, got %s", q.Get("code_challenge"))
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("expected code_challenge_method=S256, got %s", q.Get("code_challenge_method"))
	}
	if q.Get("scope") != "read write" {
		t.Errorf("expected scope='read write', got %s", q.Get("scope"))
	}
	if q.Get("resource") != "https://podclave.com/mcp" {
		t.Errorf("expected resource='https://podclave.com/mcp', got %s", q.Get("resource"))
	}
}

func TestDiscoverProtectedResource(t *testing.T) {
	var asServer *httptest.Server
	asServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/oauth-authorization-server" {
			meta := authServerMetadata{
				Issuer:                asServer.URL,
				AuthorizationEndpoint: asServer.URL + "/authorize",
				TokenEndpoint:         asServer.URL + "/token",
				RegistrationEndpoint:  asServer.URL + "/register",
				RevocationEndpoint:    asServer.URL + "/revoke",
				ScopesSupported:       []string{"read", "write"},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(meta)
			return
		}
		http.NotFound(w, r)
	}))
	defer asServer.Close()

	mcpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/oauth-protected-resource/mcp" || r.URL.Path == "/.well-known/oauth-protected-resource" {
			meta := protectedResourceMetadata{
				AuthorizationServers: []string{asServer.URL},
				Resource:             "test-resource",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(meta)
			return
		}
		http.NotFound(w, r)
	}))
	defer mcpServer.Close()

	ctx := context.Background()
	disc, err := DiscoverProtectedResource(ctx, http.DefaultClient, mcpServer.URL+"/mcp")
	if err != nil {
		t.Fatalf("DiscoverProtectedResource failed: %v", err)
	}

	if disc.AuthorizationEndpoint != asServer.URL+"/authorize" {
		t.Errorf("expected auth endpoint %s, got %s", asServer.URL+"/authorize", disc.AuthorizationEndpoint)
	}
	if disc.TokenEndpoint != asServer.URL+"/token" {
		t.Errorf("expected token endpoint %s, got %s", asServer.URL+"/token", disc.TokenEndpoint)
	}
	if disc.RegistrationEndpoint != asServer.URL+"/register" {
		t.Errorf("expected reg endpoint %s, got %s", asServer.URL+"/register", disc.RegistrationEndpoint)
	}
	if disc.RevocationEndpoint != asServer.URL+"/revoke" {
		t.Errorf("expected rev endpoint %s, got %s", asServer.URL+"/revoke", disc.RevocationEndpoint)
	}
}

func TestRegisterClient(t *testing.T) {
	asServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/register" && r.Method == http.MethodPost {
			var regReq ClientRegistrationRequest
			if err := json.NewDecoder(r.Body).Decode(&regReq); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			res := ClientRegistrationResult{
				ClientID:     "dynamically-registered-client-id",
				ClientSecret: "client-secret-xyz",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(res)
			return
		}
		http.NotFound(w, r)
	}))
	defer asServer.Close()

	ctx := context.Background()
	reg, err := RegisterClient(ctx, http.DefaultClient, asServer.URL+"/register", "Test Client", "http://localhost/callback")
	if err != nil {
		t.Fatalf("RegisterClient failed: %v", err)
	}
	if reg.ClientID != "dynamically-registered-client-id" {
		t.Errorf("unexpected client id: %s", reg.ClientID)
	}
	if reg.ClientSecret != "client-secret-xyz" {
		t.Errorf("unexpected client secret: %s", reg.ClientSecret)
	}
}

func TestExchangeAndRefreshTokenAndRevoke(t *testing.T) {
	var revokedTokens []string

	asServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.URL.Path == "/token" && r.Method == http.MethodPost {
			grantType := r.Form.Get("grant_type")
			if grantType == "authorization_code" {
				if r.Form.Get("code") != "valid-code" || r.Form.Get("code_verifier") == "" {
					http.Error(w, "invalid code", http.StatusBadRequest)
					return
				}
				resp := TokenResponse{
					AccessToken:  "initial-access-token",
					TokenType:    "Bearer",
					ExpiresIn:    3600,
					RefreshToken: "initial-refresh-token",
					Scope:        "tools:all",
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(resp)
				return
			} else if grantType == "refresh_token" {
				if r.Form.Get("refresh_token") != "initial-refresh-token" {
					http.Error(w, "invalid refresh token", http.StatusBadRequest)
					return
				}
				resp := TokenResponse{
					AccessToken:  "refreshed-access-token",
					TokenType:    "Bearer",
					ExpiresIn:    3600,
					RefreshToken: "new-refresh-token",
					Scope:        "tools:all",
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(resp)
				return
			}
		}

		if r.URL.Path == "/revoke" && r.Method == http.MethodPost {
			tok := r.Form.Get("token")
			revokedTokens = append(revokedTokens, tok)
			w.WriteHeader(http.StatusOK)
			return
		}

		http.NotFound(w, r)
	}))
	defer asServer.Close()

	ctx := context.Background()

	// 1. Exchange
	tokenResp, err := ExchangeAuthorizationCode(
		ctx,
		http.DefaultClient,
		asServer.URL+"/token",
		"client-1",
		"",
		"http://localhost/callback",
		"valid-code",
		"verifier-123",
		"https://podclave.com/mcp",
	)
	if err != nil {
		t.Fatalf("ExchangeAuthorizationCode failed: %v", err)
	}
	if tokenResp.AccessToken != "initial-access-token" {
		t.Errorf("unexpected access token: %s", tokenResp.AccessToken)
	}

	// 2. Refresh
	refreshResp, err := RefreshToken(
		ctx,
		http.DefaultClient,
		asServer.URL+"/token",
		"client-1",
		"",
		tokenResp.RefreshToken,
		"https://podclave.com/mcp",
	)
	if err != nil {
		t.Fatalf("RefreshToken failed: %v", err)
	}
	if refreshResp.AccessToken != "refreshed-access-token" {
		t.Errorf("unexpected refreshed token: %s", refreshResp.AccessToken)
	}
	if refreshResp.RefreshToken != "new-refresh-token" {
		t.Errorf("unexpected refresh token: %s", refreshResp.RefreshToken)
	}

	// 3. Revoke
	err = RevokeToken(ctx, http.DefaultClient, asServer.URL+"/revoke", "client-1", "", refreshResp.RefreshToken)
	if err != nil {
		t.Fatalf("RevokeToken failed: %v", err)
	}
	if len(revokedTokens) != 1 || revokedTokens[0] != "new-refresh-token" {
		t.Errorf("token revocation failed, revoked: %v", revokedTokens)
	}
}

func TestWWWAuthenticateDiscoveryFallback(t *testing.T) {
	var asServer *httptest.Server
	asServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/.well-known/oauth-authorization-server") {
			meta := authServerMetadata{
				AuthorizationEndpoint: asServer.URL + "/auth",
				TokenEndpoint:         asServer.URL + "/token",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(meta)
			return
		}
		http.NotFound(w, r)
	}))
	defer asServer.Close()

	mcpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp" {
			w.Header().Set("WWW-Authenticate", `Bearer as_uri="`+asServer.URL+`"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	}))
	defer mcpServer.Close()

	ctx := context.Background()
	disc, err := DiscoverProtectedResource(ctx, http.DefaultClient, mcpServer.URL+"/mcp")
	if err != nil {
		t.Fatalf("DiscoverProtectedResource via WWW-Authenticate failed: %v", err)
	}
	if disc.AuthorizationEndpoint != asServer.URL+"/auth" {
		t.Errorf("unexpected auth endpoint: %s", disc.AuthorizationEndpoint)
	}
}
