package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

var (
	ErrDiscoveryFailed     = errors.New("oauth discovery failed")
	ErrRegistrationFailed  = errors.New("client registration failed")
	ErrTokenExchangeFailed = errors.New("token exchange failed")
	ErrTokenRefreshFailed  = errors.New("token refresh failed")
	ErrTokenRevocationFail = errors.New("token revocation failed")
	ErrInvalidPKCE         = errors.New("invalid pkce parameters")
)

// GeneratePKCE generates a code_verifier (high entropy URL-safe string)
// and corresponding S256 code_challenge.
func GeneratePKCE() (verifier, challenge string, err error) {
	// RFC 7636 Section 4.1: code_verifier = high-entropy cryptographic random string
	// using unreserved characters [A-Z] / [a-z] / [0-9] / "-" / "." / "_" / "~"
	// with a minimum length of 43 characters and a maximum length of 128 characters.
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("read random bytes: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)

	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])
	return verifier, challenge, nil
}

// BuildAuthorizationURL constructs the authorization redirect URL.
// Includes resource indicator parameter (RFC 8707) when specified.
func BuildAuthorizationURL(authEndpoint, clientID, redirectURI, state, codeChallenge, scopes, resource string) (string, error) {
	if authEndpoint == "" {
		return "", errors.New("authorization endpoint cannot be empty")
	}
	u, err := url.Parse(authEndpoint)
	if err != nil {
		return "", fmt.Errorf("invalid authorization endpoint URL: %w", err)
	}

	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	if scopes != "" {
		q.Set("scope", scopes)
	}
	if resource != "" {
		q.Set("resource", resource)
	}
	u.RawQuery = q.Encode()

	return u.String(), nil
}

type authServerMetadata struct {
	Issuer                           string   `json:"issuer"`
	AuthorizationEndpoint            string   `json:"authorization_endpoint"`
	TokenEndpoint                    string   `json:"token_endpoint"`
	RegistrationEndpoint             string   `json:"registration_endpoint"`
	RevocationEndpoint               string   `json:"revocation_endpoint"`
	ScopesSupported                  []string `json:"scopes_supported"`
	CodeChallengeMethodsSupported    []string `json:"code_challenge_methods_supported"`
	Resource                         string   `json:"resource"`
}

type protectedResourceMetadata struct {
	AuthorizationServers []string `json:"authorization_servers"`
	Resource             string   `json:"resource"`
}

// DiscoverProtectedResource resolves the authorization server metadata for a given mcpURL.
// Step 1: Query protected resource metadata or inspect WWW-Authenticate.
// Step 2: Fetch authorization server metadata from discovered AS or base URL.
func DiscoverProtectedResource(ctx context.Context, httpClient *http.Client, mcpURL string) (*DiscoveryResult, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	parsedURL, err := url.Parse(mcpURL)
	if err != nil {
		return nil, fmt.Errorf("invalid mcp url: %w", err)
	}

	baseURL := fmt.Sprintf("%s://%s", parsedURL.Scheme, parsedURL.Host)
	pathTrimmed := strings.TrimRight(parsedURL.Path, "/")

	// Candidate protected resource metadata URLs
	candidates := []string{}
	if pathTrimmed != "" {
		candidates = append(candidates, fmt.Sprintf("%s/.well-known/oauth-protected-resource%s", baseURL, pathTrimmed))
	}
	candidates = append(candidates, fmt.Sprintf("%s/.well-known/oauth-protected-resource", baseURL))

	var authServers []string
	var discoveredResource string

	// Try probing protected resource metadata endpoints
	for _, cand := range candidates {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, cand, nil)
		if err != nil {
			continue
		}
		req.Header.Set("Accept", "application/json")
		resp, err := httpClient.Do(req)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				var prm protectedResourceMetadata
				if err := json.NewDecoder(resp.Body).Decode(&prm); err == nil {
					if len(prm.AuthorizationServers) > 0 {
						authServers = prm.AuthorizationServers
						if prm.Resource != "" {
							discoveredResource = prm.Resource
						}
						break
					}
				}
			}
		}
	}

	// If not found via protected resource metadata, probe mcpURL directly for 401 WWW-Authenticate or links
	if len(authServers) == 0 {
		probeReq, err := http.NewRequestWithContext(ctx, http.MethodPost, mcpURL, bytes.NewReader([]byte("{}")))
		if err == nil {
			probeReq.Header.Set("Content-Type", "application/json")
			resp, err := httpClient.Do(probeReq)
			if err == nil {
				defer resp.Body.Close()
				authHeader := resp.Header.Get("WWW-Authenticate")
				if authHeader != "" {
					if as := parseASFromWWWAuthenticate(authHeader); as != "" {
						authServers = append(authServers, as)
					}
				}
			}
		}
	}

	// If still empty, fallback to the base URL host as the authorization server issuer
	if len(authServers) == 0 {
		authServers = append(authServers, baseURL)
	}

	// Try each authorization server candidate
	for _, as := range authServers {
		as = strings.TrimRight(as, "/")
		metaURLs := []string{
			fmt.Sprintf("%s/.well-known/oauth-authorization-server", as),
			fmt.Sprintf("%s/.well-known/openid-configuration", as),
		}

		for _, metaURL := range metaURLs {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, metaURL, nil)
			if err != nil {
				continue
			}
			req.Header.Set("Accept", "application/json")
			resp, err := httpClient.Do(req)
			if err != nil {
				continue
			}
			defer resp.Body.Close()

			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				var meta authServerMetadata
				if err := json.NewDecoder(resp.Body).Decode(&meta); err == nil {
					if meta.AuthorizationEndpoint != "" && meta.TokenEndpoint != "" {
						resURL := discoveredResource
						if resURL == "" && meta.Resource != "" {
							resURL = meta.Resource
						}
						if resURL == "" {
							resURL = mcpURL
						}
						return &DiscoveryResult{
							Issuer:                meta.Issuer,
							AuthorizationEndpoint: meta.AuthorizationEndpoint,
							TokenEndpoint:         meta.TokenEndpoint,
							RegistrationEndpoint:  meta.RegistrationEndpoint,
							RevocationEndpoint:    meta.RevocationEndpoint,
							ScopesSupported:       meta.ScopesSupported,
							CodeChallengeMethods:  meta.CodeChallengeMethodsSupported,
							Resource:              resURL,
						}, nil
					}
				}
			}
		}
	}

	return nil, fmt.Errorf("%w: unable to discover authorization server endpoints from %s", ErrDiscoveryFailed, mcpURL)
}

func parseASFromWWWAuthenticate(header string) string {
	// Examples: Bearer as_uri="https://auth.example.com", resource_metadata="..."
	// Or: Bearer error="invalid_token", as_uri="https://auth.example.com"
	header = strings.TrimPrefix(header, "Bearer ")
	header = strings.TrimPrefix(header, "bearer ")
	parts := strings.Split(header, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		lower := strings.ToLower(part)
		if strings.HasPrefix(lower, "as_uri=") {
			val := strings.TrimPrefix(part, part[:7])
			return strings.Trim(val, "\" ")
		}
		if strings.HasPrefix(lower, "authorization_server=") {
			val := strings.TrimPrefix(part, part[:21])
			return strings.Trim(val, "\" ")
		}
	}
	return ""
}

// RegisterClient executes dynamic client registration (RFC 7591).
func RegisterClient(ctx context.Context, httpClient *http.Client, registrationEndpoint, clientName, redirectURI string) (*ClientRegistrationResult, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if registrationEndpoint == "" {
		return nil, errors.New("registration endpoint cannot be empty")
	}

	reqPayload := ClientRegistrationRequest{
		ClientName:              clientName,
		RedirectURIs:            []string{redirectURI},
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
	}

	bodyBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, fmt.Errorf("marshal registration request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, registrationEndpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create registration request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRegistrationFailed, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("%w: HTTP %d: %s", ErrRegistrationFailed, resp.StatusCode, string(respBody))
	}

	var result ClientRegistrationResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode registration response: %w", err)
	}

	if result.ClientID == "" {
		return nil, fmt.Errorf("%w: missing client_id in response", ErrRegistrationFailed)
	}

	return &result, nil
}

// ExchangeAuthorizationCode exchanges an auth code for access & refresh tokens.
// If resource is provided, it is sent as the resource indicator parameter (RFC 8707).
func ExchangeAuthorizationCode(ctx context.Context, httpClient *http.Client, tokenEndpoint, clientID, clientSecret, redirectURI, code, codeVerifier, resource string) (*TokenResponse, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if tokenEndpoint == "" {
		return nil, errors.New("token endpoint cannot be empty")
	}

	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("code", code)
	data.Set("redirect_uri", redirectURI)
	data.Set("client_id", clientID)
	data.Set("code_verifier", codeVerifier)
	if clientSecret != "" {
		data.Set("client_secret", clientSecret)
	}
	if resource != "" {
		data.Set("resource", resource)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create token exchange request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTokenExchangeFailed, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("%w: HTTP %d: %s", ErrTokenExchangeFailed, resp.StatusCode, string(respBody))
	}

	var tokenResp TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("decode token exchange response: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("%w: missing access_token in response", ErrTokenExchangeFailed)
	}

	return &tokenResp, nil
}

// RefreshToken refreshes an expired access token using the refresh_token grant.
// If resource is provided, it is sent as the resource indicator parameter (RFC 8707).
func RefreshToken(ctx context.Context, httpClient *http.Client, tokenEndpoint, clientID, clientSecret, refreshToken, resource string) (*TokenResponse, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if tokenEndpoint == "" {
		return nil, errors.New("token endpoint cannot be empty")
	}
	if refreshToken == "" {
		return nil, errors.New("refresh token cannot be empty")
	}

	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("refresh_token", refreshToken)
	data.Set("client_id", clientID)
	if clientSecret != "" {
		data.Set("client_secret", clientSecret)
	}
	if resource != "" {
		data.Set("resource", resource)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create refresh token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTokenRefreshFailed, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("%w: HTTP %d: %s", ErrTokenRefreshFailed, resp.StatusCode, string(respBody))
	}

	var tokenResp TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("decode refresh token response: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("%w: missing access_token in refresh response", ErrTokenRefreshFailed)
	}

	// Preserve refresh token if not rotated
	if tokenResp.RefreshToken == "" {
		tokenResp.RefreshToken = refreshToken
	}

	return &tokenResp, nil
}

// RevokeToken implements RFC 7009 token revocation.
func RevokeToken(ctx context.Context, httpClient *http.Client, revocationEndpoint, clientID, clientSecret, token string) error {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if revocationEndpoint == "" {
		return nil // Revocation endpoint not advertised, nothing to revoke remotely
	}
	if token == "" {
		return nil
	}

	data := url.Values{}
	data.Set("token", token)
	if clientID != "" {
		data.Set("client_id", clientID)
	}
	if clientSecret != "" {
		data.Set("client_secret", clientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, revocationEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return fmt.Errorf("create revocation request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTokenRevocationFail, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("%w: HTTP %d: %s", ErrTokenRevocationFail, resp.StatusCode, string(respBody))
	}

	return nil
}
