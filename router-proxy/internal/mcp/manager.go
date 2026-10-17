package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

const (
	defaultPendingAuthTTL = 10 * time.Minute
	proactiveRefreshWindow = 60 * time.Second
)

var (
	ErrServerNotFound = errors.New("mcp server not found")
	ErrInvalidAuth    = errors.New("invalid or expired oauth state")
	ErrAuthRequired   = errors.New("mcp server requires re-authentication")
)

// Manager manages remote MCP servers, OAuth flows, token refreshes, and tool execution.
type Manager struct {
	store      *store.Store
	secretKey  []byte
	httpClient *http.Client

	// serverLocks guards per-server mutations (e.g. serialized token refresh)
	serverLocks   sync.Map // map[string]*sync.Mutex
	serverLockMu  sync.Mutex

	// pendingAuth tracks in-flight PKCE authorizations by state nonce
	pendingAuthMu sync.Mutex
	pendingAuths  map[string]*PendingAuth
}

// NewManager initializes a new MCP manager.
func NewManager(st *store.Store, secretKey []byte, httpClient *http.Client) *Manager {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Manager{
		store:        st,
		secretKey:    secretKey,
		httpClient:   httpClient,
		pendingAuths: make(map[string]*PendingAuth),
	}
}

// SecretKey returns the encryption key used for token storage.
func (m *Manager) SecretKey() []byte {
	return m.secretKey
}

func (m *Manager) getServerMutex(serverID string) *sync.Mutex {
	m.serverLockMu.Lock()
	defer m.serverLockMu.Unlock()

	val, exists := m.serverLocks.Load(serverID)
	if exists {
		return val.(*sync.Mutex)
	}
	mu := &sync.Mutex{}
	m.serverLocks.Store(serverID, mu)
	return mu
}

// AddServer registers a new remote MCP server. If auth_type is oauth, it initiates discovery.
func (m *Manager) AddServer(ctx context.Context, s *store.MCPServer) (*store.MCPServer, error) {
	if s == nil {
		return nil, errors.New("server is nil")
	}
	if s.URL == "" {
		return nil, errors.New("server url is required")
	}
	if s.Name == "" {
		s.Name = s.URL
	}
	if s.AuthType == "" {
		s.AuthType = "none"
	}

	// If auth_type is oauth, discover endpoints if not already provided
	if s.AuthType == "oauth" && (s.TokenEndpoint == nil || *s.TokenEndpoint == "" || s.AuthEndpoint == nil || *s.AuthEndpoint == "") {
		disc, err := DiscoverProtectedResource(ctx, m.httpClient, s.URL)
		if err == nil {
			if s.AuthEndpoint == nil || *s.AuthEndpoint == "" {
				s.AuthEndpoint = &disc.AuthorizationEndpoint
			}
			if s.TokenEndpoint == nil || *s.TokenEndpoint == "" {
				s.TokenEndpoint = &disc.TokenEndpoint
			}
			if disc.RegistrationEndpoint != "" && (s.RegistrationEndpoint == nil || *s.RegistrationEndpoint == "") {
				s.RegistrationEndpoint = &disc.RegistrationEndpoint
			}
			if disc.RevocationEndpoint != "" && (s.RevocationEndpoint == nil || *s.RevocationEndpoint == "") {
				s.RevocationEndpoint = &disc.RevocationEndpoint
			}
		}
	}

	if s.Status == "" {
		if s.AuthType == "oauth" {
			s.Status = "needs_auth"
		} else {
			s.Status = "unconfigured"
		}
	}

	if err := m.store.CreateMCPServer(ctx, s); err != nil {
		return nil, fmt.Errorf("create mcp server: %w", err)
	}

	return s, nil
}

// StartAuth generates PKCE credentials and constructs the OAuth authorization URL.
func (m *Manager) StartAuth(ctx context.Context, serverID, redirectURI string) (authURL string, err error) {
	s, err := m.store.GetMCPServer(ctx, serverID)
	if err != nil {
		return "", fmt.Errorf("get server %s: %w", serverID, err)
	}

	// Discover endpoints if missing
	if s.AuthEndpoint == nil || *s.AuthEndpoint == "" || s.TokenEndpoint == nil || *s.TokenEndpoint == "" {
		disc, err := DiscoverProtectedResource(ctx, m.httpClient, s.URL)
		if err != nil {
			return "", fmt.Errorf("discover endpoints: %w", err)
		}
		s.AuthEndpoint = &disc.AuthorizationEndpoint
		s.TokenEndpoint = &disc.TokenEndpoint
		if disc.RegistrationEndpoint != "" {
			s.RegistrationEndpoint = &disc.RegistrationEndpoint
		}
		if disc.RevocationEndpoint != "" {
			s.RevocationEndpoint = &disc.RevocationEndpoint
		}
		_ = m.store.UpdateMCPServer(ctx, s)
	}

	// If client_id is missing, perform RFC 7591 dynamic client registration if endpoint is available
	if (s.ClientID == nil || *s.ClientID == "") && s.RegistrationEndpoint != nil && *s.RegistrationEndpoint != "" {
		reg, err := RegisterClient(ctx, m.httpClient, *s.RegistrationEndpoint, "Mecha Router MCP Client", redirectURI)
		if err == nil {
			s.ClientID = &reg.ClientID
			if reg.ClientSecret != "" {
				s.ClientSecret = &reg.ClientSecret
			}
			_ = m.store.UpdateMCPServer(ctx, s)
		}
	}

	if s.ClientID == nil || *s.ClientID == "" {
		return "", errors.New("cannot initiate oauth: client_id is not configured and dynamic registration was not possible")
	}

	verifier, challenge, err := GeneratePKCE()
	if err != nil {
		return "", fmt.Errorf("generate pkce: %w", err)
	}

	state := uuid.New().String()
	pending := &PendingAuth{
		State:        state,
		ServerID:     serverID,
		CodeVerifier: verifier,
		RedirectURI:  redirectURI,
		ExpiresAt:    time.Now().Add(defaultPendingAuthTTL),
	}

	m.pendingAuthMu.Lock()
	// Clean up expired pending auths
	now := time.Now()
	for k, v := range m.pendingAuths {
		if now.After(v.ExpiresAt) {
			delete(m.pendingAuths, k)
		}
	}
	m.pendingAuths[state] = pending
	m.pendingAuthMu.Unlock()

	scopes := ""
	if s.Scopes != nil {
		scopes = *s.Scopes
	}

	resource := s.URL
	authURL, err = BuildAuthorizationURL(*s.AuthEndpoint, *s.ClientID, redirectURI, state, challenge, scopes, resource)
	if err != nil {
		return "", fmt.Errorf("build authorization url: %w", err)
	}

	return authURL, nil
}

// CompleteAuth exchanges authorization code for tokens and kicks off initial tool sync.
func (m *Manager) CompleteAuth(ctx context.Context, state, code string) (*store.MCPServer, error) {
	m.pendingAuthMu.Lock()
	pending, found := m.pendingAuths[state]
	if found {
		delete(m.pendingAuths, state)
	}
	m.pendingAuthMu.Unlock()

	if !found || time.Now().After(pending.ExpiresAt) {
		return nil, ErrInvalidAuth
	}

	server, err := m.store.GetMCPServer(ctx, pending.ServerID)
	if err != nil {
		return nil, fmt.Errorf("get server: %w", err)
	}

	if server.TokenEndpoint == nil || *server.TokenEndpoint == "" {
		return nil, errors.New("server token endpoint missing")
	}
	if server.ClientID == nil || *server.ClientID == "" {
		return nil, errors.New("server client_id missing")
	}

	var secret string
	if server.ClientSecret != nil {
		secret = *server.ClientSecret
	}

	tokenResp, err := ExchangeAuthorizationCode(
		ctx,
		m.httpClient,
		*server.TokenEndpoint,
		*server.ClientID,
		secret,
		pending.RedirectURI,
		code,
		pending.CodeVerifier,
		server.URL,
	)
	if err != nil {
		errMsg := err.Error()
		server.LastError = &errMsg
		server.Status = "error"
		_ = m.store.UpdateMCPServer(ctx, server)
		return nil, fmt.Errorf("token exchange: %w", err)
	}

	// Persist tokens
	var expTime *time.Time
	if tokenResp.ExpiresIn > 0 {
		t := time.Now().UTC().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
		expTime = &t
	}

	var refToken *string
	if tokenResp.RefreshToken != "" {
		refToken = &tokenResp.RefreshToken
	}

	mcpToken := &store.MCPToken{
		ServerID:     server.ID,
		AccessToken:  &tokenResp.AccessToken,
		RefreshToken: refToken,
		TokenType:    tokenResp.TokenType,
		ExpiresAt:    expTime,
	}
	if tokenResp.Scope != "" {
		mcpToken.Scopes = &tokenResp.Scope
	}

	if err := m.store.SetMCPToken(ctx, mcpToken, m.secretKey); err != nil {
		return nil, fmt.Errorf("save tokens: %w", err)
	}

	server.Status = "connected"
	server.LastError = nil
	if err := m.store.UpdateMCPServer(ctx, server); err != nil {
		return nil, fmt.Errorf("update server: %w", err)
	}

	// Kick off initial sync
	if err := m.SyncTools(ctx, server.ID); err != nil {
		return nil, fmt.Errorf("initial sync tools: %w", err)
	}

	return m.store.GetMCPServer(ctx, server.ID)
}

// RevokeAuth revokes tokens and sets the server status to needs_auth.
func (m *Manager) RevokeAuth(ctx context.Context, serverID string) error {
	server, err := m.store.GetMCPServer(ctx, serverID)
	if err != nil {
		return err
	}

	// Try reading current token to revoke remotely if endpoint exists
	if server.RevocationEndpoint != nil && *server.RevocationEndpoint != "" {
		tok, err := m.store.GetMCPToken(ctx, serverID, m.secretKey)
		if err == nil && tok != nil {
			var clientID, secret string
			if server.ClientID != nil {
				clientID = *server.ClientID
			}
			if server.ClientSecret != nil {
				secret = *server.ClientSecret
			}
			if tok.RefreshToken != nil && *tok.RefreshToken != "" {
				_ = RevokeToken(ctx, m.httpClient, *server.RevocationEndpoint, clientID, secret, *tok.RefreshToken)
			} else if tok.AccessToken != nil && *tok.AccessToken != "" {
				_ = RevokeToken(ctx, m.httpClient, *server.RevocationEndpoint, clientID, secret, *tok.AccessToken)
			}
		}
	}

	// Remove local tokens
	_ = m.store.DeleteMCPToken(ctx, serverID)

	server.Status = "needs_auth"
	return m.store.UpdateMCPServer(ctx, server)
}

// ensureValidToken performs proactive serialized token refresh if expiry is within 60s.
func (m *Manager) ensureValidToken(ctx context.Context, server *store.MCPServer) (string, error) {
	if server.AuthType != "oauth" {
		return "", nil
	}

	mu := m.getServerMutex(server.ID)
	mu.Lock()
	defer mu.Unlock()

	tok, err := m.store.GetMCPToken(ctx, server.ID, m.secretKey)
	if err != nil {
		return "", fmt.Errorf("get mcp token: %w", err)
	}
	if tok == nil || tok.AccessToken == nil || *tok.AccessToken == "" {
		return "", ErrAuthRequired
	}

	// Check if token expires within proactiveRefreshWindow
	needsRefresh := false
	if tok.ExpiresAt != nil {
		if time.Until(*tok.ExpiresAt) < proactiveRefreshWindow {
			needsRefresh = true
		}
	}

	if !needsRefresh {
		return *tok.AccessToken, nil
	}

	// We need to refresh
	if tok.RefreshToken == nil || *tok.RefreshToken == "" {
		// No refresh token available, return current if still strictly valid or fail
		if tok.ExpiresAt != nil && time.Now().After(*tok.ExpiresAt) {
			server.Status = "needs_auth"
			_ = m.store.UpdateMCPServer(ctx, server)
			return "", ErrAuthRequired
		}
		return *tok.AccessToken, nil
	}

	newTok, err := m.refreshTokenLocked(ctx, server, *tok.RefreshToken)
	if err != nil {
		// If refresh failed, mark server as needs_auth
		server.Status = "needs_auth"
		errStr := err.Error()
		server.LastError = &errStr
		_ = m.store.UpdateMCPServer(ctx, server)
		return "", fmt.Errorf("%w: %v", ErrAuthRequired, err)
	}

	return *newTok.AccessToken, nil
}

func (m *Manager) refreshTokenLocked(ctx context.Context, server *store.MCPServer, refreshToken string) (*store.MCPToken, error) {
	if server.TokenEndpoint == nil || *server.TokenEndpoint == "" {
		return nil, errors.New("missing token endpoint")
	}
	var clientID, secret string
	if server.ClientID != nil {
		clientID = *server.ClientID
	}
	if server.ClientSecret != nil {
		secret = *server.ClientSecret
	}

	resp, err := RefreshToken(ctx, m.httpClient, *server.TokenEndpoint, clientID, secret, refreshToken, server.URL)
	if err != nil {
		return nil, err
	}

	var expTime *time.Time
	if resp.ExpiresIn > 0 {
		t := time.Now().UTC().Add(time.Duration(resp.ExpiresIn) * time.Second)
		expTime = &t
	}

	refToken := resp.RefreshToken
	if refToken == "" {
		refToken = refreshToken
	}

	tok := &store.MCPToken{
		ServerID:     server.ID,
		AccessToken:  &resp.AccessToken,
		RefreshToken: &refToken,
		TokenType:    resp.TokenType,
		ExpiresAt:    expTime,
	}
	if resp.Scope != "" {
		tok.Scopes = &resp.Scope
	}

	if err := m.store.SetMCPToken(ctx, tok, m.secretKey); err != nil {
		return nil, fmt.Errorf("store refreshed token: %w", err)
	}

	return tok, nil
}

// SyncTools queries tools/list and caches them in store.
func (m *Manager) SyncTools(ctx context.Context, serverID string) error {
	server, err := m.store.GetMCPServer(ctx, serverID)
	if err != nil {
		return err
	}

	tokenGetter := func(c context.Context) (string, error) {
		return m.ensureValidToken(c, server)
	}

	client := NewClient(server.URL, tokenGetter, m.httpClient)
	if err := client.Initialize(ctx); err != nil {
		errStr := err.Error()
		server.LastError = &errStr
		if errors.Is(err, ErrUnauthorized) {
			server.Status = "needs_auth"
		}
		_ = m.store.UpdateMCPServer(ctx, server)
		return fmt.Errorf("initialize client: %w", err)
	}

	tools, err := client.ListTools(ctx)
	if err != nil {
		errStr := err.Error()
		server.LastError = &errStr
		if errors.Is(err, ErrUnauthorized) {
			server.Status = "needs_auth"
		}
		_ = m.store.UpdateMCPServer(ctx, server)
		return fmt.Errorf("list tools: %w", err)
	}

	var records []store.MCPToolRecord
	for _, t := range tools {
		prefixed := t.Name
		// Disambiguate with server name or id prefix
		serverPrefix := server.Name
		if serverPrefix == "" {
			serverPrefix = server.ID
		}
		serverPrefix = strings.ToLower(strings.ReplaceAll(serverPrefix, " ", "_"))
		prefixed = fmt.Sprintf("%s_%s", serverPrefix, t.Name)

		inSchema := string(t.InputSchema)
		if inSchema == "" {
			inSchema = "{}"
		}

		var outSchema *string
		if len(t.OutputSchema) > 0 {
			s := string(t.OutputSchema)
			outSchema = &s
		}

		h := sha256.New()
		h.Write([]byte(t.Name))
		h.Write([]byte(inSchema))
		hashStr := hex.EncodeToString(h.Sum(nil))

		var desc *string
		if t.Description != "" {
			desc = &t.Description
		}

		records = append(records, store.MCPToolRecord{
			ID:               fmt.Sprintf("%s:%s", server.ID, t.Name),
			ServerID:         server.ID,
			Name:             t.Name,
			PrefixedName:     prefixed,
			Description:      desc,
			InputSchemaJSON:  inSchema,
			OutputSchemaJSON: outSchema,
			DefinitionSHA256: hashStr,
		})
	}

	if err := m.store.SaveMCPTools(ctx, server.ID, records); err != nil {
		return fmt.Errorf("save tools to store: %w", err)
	}

	server.ToolsCount = len(records)
	server.Status = "connected"
	server.LastError = nil
	_ = m.store.UpdateMCPServer(ctx, server)
	return nil
}

// CallTool executes a remote tool with proactive token refresh and reactive 401 retry.
func (m *Manager) CallTool(ctx context.Context, serverID, toolName string, args map[string]any) (*ToolCallResult, error) {
	server, err := m.store.GetMCPServer(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("get server: %w", err)
	}

	actualToolName := toolName
	if tools, err := m.store.ListMCPTools(ctx, serverID); err == nil {
		for _, t := range tools {
			if t.PrefixedName == toolName {
				actualToolName = t.Name
				break
			}
		}
	}

	tokenGetter := func(c context.Context) (string, error) {
		return m.ensureValidToken(c, server)
	}

	client := NewClient(server.URL, tokenGetter, m.httpClient)
	res, err := client.CallTool(ctx, actualToolName, args)
	if err == nil {
		return res, nil
	}

	// Reactive 401 retry: if request failed with ErrUnauthorized and server is oauth, attempt token refresh and replay once
	if errors.Is(err, ErrUnauthorized) && server.AuthType == "oauth" {
		mu := m.getServerMutex(server.ID)
		mu.Lock()
		tok, tokErr := m.store.GetMCPToken(ctx, server.ID, m.secretKey)
		var refErr error
		if tokErr == nil && tok != nil && tok.RefreshToken != nil && *tok.RefreshToken != "" {
			_, refErr = m.refreshTokenLocked(ctx, server, *tok.RefreshToken)
		} else {
			refErr = ErrAuthRequired
		}
		mu.Unlock()

		if refErr != nil {
			server.Status = "needs_auth"
			errStr := refErr.Error()
			server.LastError = &errStr
			_ = m.store.UpdateMCPServer(ctx, server)
			return nil, fmt.Errorf("%w: %v", ErrAuthRequired, refErr)
		}

		// Replay tool call once with newly refreshed token
		res, err = client.CallTool(ctx, actualToolName, args)
		if err != nil {
			if errors.Is(err, ErrUnauthorized) {
				server.Status = "needs_auth"
				_ = m.store.UpdateMCPServer(ctx, server)
				return nil, ErrAuthRequired
			}
			return nil, err
		}
		return res, nil
	}

	return nil, err
}

// ListActiveTools returns all tools belonging to connected MCP servers.
func (m *Manager) ListActiveTools(ctx context.Context) ([]store.MCPToolRecord, error) {
	return m.store.ListAllActiveMCPTools(ctx)
}
