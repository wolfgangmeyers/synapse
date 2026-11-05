package store_test

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/crypto"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testsupport/storetest"
	"github.com/stretchr/testify/require"
)

func TestMCPServerCRUD(t *testing.T) {
	ctx := context.Background()
	s, _ := storetest.OpenTestStore(t)

	// 1. Create MCPServer
	clientSecret := "test-client-secret-123"
	tokenEndpoint := "https://auth.example.com/oauth/token"
	authEndpoint := "https://auth.example.com/oauth/authorize"
	scopes := "read write"

	server := &store.MCPServer{
		ID:            "mcp-server-1",
		Name:          "Test MCP Server",
		URL:           "https://mcp.example.com/sse",
		AuthType:      "oauth",
		ClientID:      strPtr("test-client-id"),
		ClientSecret:  &clientSecret,
		TokenEndpoint: &tokenEndpoint,
		AuthEndpoint:  &authEndpoint,
		Scopes:        &scopes,
		Status:        "connecting",
	}

	err := s.CreateMCPServer(ctx, server)
	require.NoError(t, err)
	require.NotEmpty(t, server.ID)
	require.False(t, server.CreatedAt.IsZero())
	require.False(t, server.UpdatedAt.IsZero())

	// 2. GetMCPServer by ID
	fetched, err := s.GetMCPServer(ctx, server.ID)
	require.NoError(t, err)
	require.Equal(t, server.ID, fetched.ID)
	require.Equal(t, "Test MCP Server", fetched.Name)
	require.Equal(t, "https://mcp.example.com/sse", fetched.URL)
	require.Equal(t, "oauth", fetched.AuthType)
	require.Equal(t, "test-client-id", *fetched.ClientID)
	require.Equal(t, clientSecret, *fetched.ClientSecret)
	require.Equal(t, tokenEndpoint, *fetched.TokenEndpoint)
	require.Equal(t, authEndpoint, *fetched.AuthEndpoint)
	require.Equal(t, scopes, *fetched.Scopes)
	require.Equal(t, "connecting", fetched.Status)
	require.Equal(t, 0, fetched.ToolsCount)

	// 3. GetMCPServerByURL
	byURL, err := s.GetMCPServerByURL(ctx, "https://mcp.example.com/sse")
	require.NoError(t, err)
	require.Equal(t, server.ID, byURL.ID)

	// GetMCPServer with non-existent ID
	_, err = s.GetMCPServer(ctx, "non-existent")
	require.ErrorIs(t, err, store.ErrMCPServerNotFound)

	// GetMCPServerByURL with non-existent URL
	_, err = s.GetMCPServerByURL(ctx, "https://non-existent.example.com")
	require.ErrorIs(t, err, store.ErrMCPServerNotFound)

	// 4. ListMCPServers
	server2 := &store.MCPServer{
		ID:       "mcp-server-2",
		Name:     "Second Server",
		URL:      "https://mcp2.example.com/sse",
		AuthType: "bearer",
		Status:   "connected",
	}
	err = s.CreateMCPServer(ctx, server2)
	require.NoError(t, err)

	list, err := s.ListMCPServers(ctx)
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, "mcp-server-1", list[0].ID)
	require.Equal(t, "mcp-server-2", list[1].ID)

	// 5. UpdateMCPServer
	fetched.Name = "Updated Server Name"
	fetched.Status = "connected"
	lastErr := "some non-fatal warning"
	fetched.LastError = &lastErr
	err = s.UpdateMCPServer(ctx, fetched)
	require.NoError(t, err)

	updated, err := s.GetMCPServer(ctx, server.ID)
	require.NoError(t, err)
	require.Equal(t, "Updated Server Name", updated.Name)
	require.Equal(t, "connected", updated.Status)
	require.Equal(t, &lastErr, updated.LastError)

	// Update non-existent server
	nonExistentServer := &store.MCPServer{ID: "non-existent", Name: "Ghost"}
	err = s.UpdateMCPServer(ctx, nonExistentServer)
	require.ErrorIs(t, err, store.ErrMCPServerNotFound)

	// 6. DeleteMCPServer
	err = s.DeleteMCPServer(ctx, server2.ID)
	require.NoError(t, err)

	_, err = s.GetMCPServer(ctx, server2.ID)
	require.ErrorIs(t, err, store.ErrMCPServerNotFound)

	// Delete non-existent server
	err = s.DeleteMCPServer(ctx, "non-existent")
	require.ErrorIs(t, err, store.ErrMCPServerNotFound)
}

func TestMCPTokenRoundtrip(t *testing.T) {
	ctx := context.Background()
	s, _ := storetest.OpenTestStore(t)

	// Create parent server
	server := &store.MCPServer{
		ID:       "server-for-tokens",
		Name:     "Token Server",
		URL:      "https://tokens.example.com/sse",
		AuthType: "oauth",
		Status:   "connected",
	}
	err := s.CreateMCPServer(ctx, server)
	require.NoError(t, err)

	// Derive or generate a 32-byte key
	key := make([]byte, crypto.KeySize)
	_, err = rand.Read(key)
	require.NoError(t, err)

	// Try getting non-existent token
	_, err = s.GetMCPToken(ctx, server.ID, key)
	require.ErrorIs(t, err, store.ErrMCPTokenNotFound)

	// SetMCPToken
	accessToken := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.access-token-secret"
	refreshToken := "d98234-refresh-token-secret"
	expiresAt := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Millisecond)
	scopes := "repo read:org"

	token := &store.MCPToken{
		ServerID:     server.ID,
		AccessToken:  &accessToken,
		RefreshToken: &refreshToken,
		TokenType:    "Bearer",
		ExpiresAt:    &expiresAt,
		Scopes:       &scopes,
	}

	err = s.SetMCPToken(ctx, token, key)
	require.NoError(t, err)

	// GetMCPToken and verify decrypted content
	fetched, err := s.GetMCPToken(ctx, server.ID, key)
	require.NoError(t, err)
	require.Equal(t, server.ID, fetched.ServerID)
	require.NotNil(t, fetched.AccessToken)
	require.Equal(t, accessToken, *fetched.AccessToken)
	require.NotNil(t, fetched.RefreshToken)
	require.Equal(t, refreshToken, *fetched.RefreshToken)
	require.Equal(t, "Bearer", fetched.TokenType)
	require.NotNil(t, fetched.Scopes)
	require.Equal(t, scopes, *fetched.Scopes)
	require.NotNil(t, fetched.ExpiresAt)
	require.WithinDuration(t, expiresAt, *fetched.ExpiresAt, time.Second)

	// Decrypting with wrong key must fail
	wrongKey := make([]byte, crypto.KeySize)
	copy(wrongKey, key)
	wrongKey[0] ^= 0xff
	_, err = s.GetMCPToken(ctx, server.ID, wrongKey)
	require.Error(t, err)

	// Update token via SetMCPToken (upsert)
	newAccess := "new-access-token"
	token.AccessToken = &newAccess
	token.RefreshToken = nil
	err = s.SetMCPToken(ctx, token, key)
	require.NoError(t, err)

	updatedToken, err := s.GetMCPToken(ctx, server.ID, key)
	require.NoError(t, err)
	require.Equal(t, newAccess, *updatedToken.AccessToken)
	require.Nil(t, updatedToken.RefreshToken)

	// DeleteMCPToken
	err = s.DeleteMCPToken(ctx, server.ID)
	require.NoError(t, err)

	_, err = s.GetMCPToken(ctx, server.ID, key)
	require.ErrorIs(t, err, store.ErrMCPTokenNotFound)

	// Delete non-existent token
	err = s.DeleteMCPToken(ctx, "non-existent")
	require.ErrorIs(t, err, store.ErrMCPTokenNotFound)
}

func TestCascadeDeletion(t *testing.T) {
	ctx := context.Background()
	s, _ := storetest.OpenTestStore(t)

	server := &store.MCPServer{
		ID:       "server-cascade-1",
		Name:     "Cascade Server",
		URL:      "https://cascade.example.com/sse",
		AuthType: "oauth",
		Status:   "connected",
	}
	err := s.CreateMCPServer(ctx, server)
	require.NoError(t, err)

	key := make([]byte, crypto.KeySize)
	_, err = rand.Read(key)
	require.NoError(t, err)

	// Set Token
	accessToken := "cascade-token"
	err = s.SetMCPToken(ctx, &store.MCPToken{
		ServerID:    server.ID,
		AccessToken: &accessToken,
	}, key)
	require.NoError(t, err)

	// Save Tools
	tools := []store.MCPToolRecord{
		{
			Name:             "tool_a",
			PrefixedName:     "cascade_tool_a",
			InputSchemaJSON:  `{"type":"object"}`,
			DefinitionSHA256: "hash-a",
		},
		{
			Name:             "tool_b",
			PrefixedName:     "cascade_tool_b",
			InputSchemaJSON:  `{"type":"object"}`,
			DefinitionSHA256: "hash-b",
		},
	}
	err = s.SaveMCPTools(ctx, server.ID, tools)
	require.NoError(t, err)

	// Verify token and tools exist
	tok, err := s.GetMCPToken(ctx, server.ID, key)
	require.NoError(t, err)
	require.NotNil(t, tok)

	toolList, err := s.ListMCPTools(ctx, server.ID)
	require.NoError(t, err)
	require.Len(t, toolList, 2)

	// Delete Server
	err = s.DeleteMCPServer(ctx, server.ID)
	require.NoError(t, err)

	// Verify token is cascade deleted
	_, err = s.GetMCPToken(ctx, server.ID, key)
	require.ErrorIs(t, err, store.ErrMCPTokenNotFound)

	// Verify tools are cascade deleted
	remainingTools, err := s.ListMCPTools(ctx, server.ID)
	require.NoError(t, err)
	require.Empty(t, remainingTools)
}

func TestListAllActiveMCPTools(t *testing.T) {
	ctx := context.Background()
	s, _ := storetest.OpenTestStore(t)

	// Server 1: connected
	s1 := &store.MCPServer{
		ID:       "srv-connected",
		Name:     "Active Server",
		URL:      "https://active.example.com",
		AuthType: "none",
		Status:   "connected",
	}
	require.NoError(t, s.CreateMCPServer(ctx, s1))

	// Server 2: connecting
	s2 := &store.MCPServer{
		ID:       "srv-connecting",
		Name:     "Pending Server",
		URL:      "https://pending.example.com",
		AuthType: "none",
		Status:   "connecting",
	}
	require.NoError(t, s.CreateMCPServer(ctx, s2))

	// Server 3: needs_auth
	s3 := &store.MCPServer{
		ID:       "srv-needs-auth",
		Name:     "Unauth Server",
		URL:      "https://unauth.example.com",
		AuthType: "oauth",
		Status:   "needs_auth",
	}
	require.NoError(t, s.CreateMCPServer(ctx, s3))

	// Server 4: error
	s4 := &store.MCPServer{
		ID:       "srv-error",
		Name:     "Error Server",
		URL:      "https://error.example.com",
		AuthType: "none",
		Status:   "error",
	}
	require.NoError(t, s.CreateMCPServer(ctx, s4))

	// Populate tools for each server
	require.NoError(t, s.SaveMCPTools(ctx, s1.ID, []store.MCPToolRecord{
		{
			Name:             "tool_1",
			PrefixedName:     "active_tool_1",
			InputSchemaJSON:  `{}`,
			DefinitionSHA256: "hash1",
		},
		{
			Name:             "tool_2",
			PrefixedName:     "active_tool_2",
			InputSchemaJSON:  `{}`,
			DefinitionSHA256: "hash2",
		},
	}))

	require.NoError(t, s.SaveMCPTools(ctx, s2.ID, []store.MCPToolRecord{
		{
			Name:             "tool_p",
			PrefixedName:     "pending_tool_p",
			InputSchemaJSON:  `{}`,
			DefinitionSHA256: "hash-p",
		},
	}))

	require.NoError(t, s.SaveMCPTools(ctx, s3.ID, []store.MCPToolRecord{
		{
			Name:             "tool_u",
			PrefixedName:     "unauth_tool_u",
			InputSchemaJSON:  `{}`,
			DefinitionSHA256: "hash-u",
		},
	}))

	require.NoError(t, s.SaveMCPTools(ctx, s4.ID, []store.MCPToolRecord{
		{
			Name:             "tool_e",
			PrefixedName:     "error_tool_e",
			InputSchemaJSON:  `{}`,
			DefinitionSHA256: "hash-e",
		},
	}))

	// Verify server 1 tools_count was updated
	srv1After, err := s.GetMCPServer(ctx, s1.ID)
	require.NoError(t, err)
	require.Equal(t, 2, srv1After.ToolsCount)
	require.NotNil(t, srv1After.LastSyncAt)

	// ListAllActiveMCPTools should ONLY return tools from srv-connected
	activeTools, err := s.ListAllActiveMCPTools(ctx)
	require.NoError(t, err)
	require.Len(t, activeTools, 2)
	require.Equal(t, "active_tool_1", activeTools[0].PrefixedName)
	require.Equal(t, "active_tool_2", activeTools[1].PrefixedName)
	require.Equal(t, s1.ID, activeTools[0].ServerID)
	require.Equal(t, s1.ID, activeTools[1].ServerID)

	// Transition server 2 to 'connected'
	s2.Status = "connected"
	require.NoError(t, s.UpdateMCPServer(ctx, s2))

	activeTools, err = s.ListAllActiveMCPTools(ctx)
	require.NoError(t, err)
	require.Len(t, activeTools, 3)

	// Transition server 1 to 'error'
	s1.Status = "error"
	require.NoError(t, s.UpdateMCPServer(ctx, s1))

	activeTools, err = s.ListAllActiveMCPTools(ctx)
	require.NoError(t, err)
	require.Len(t, activeTools, 1)
	require.Equal(t, "pending_tool_p", activeTools[0].PrefixedName)
}

func strPtr(s string) *string {
	return &s
}
