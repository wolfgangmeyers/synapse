package mcp

import (
	"encoding/json"
	"time"
)

// JSONRPCRequest models a JSON-RPC 2.0 request.
type JSONRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// JSONRPCResponse models a JSON-RPC 2.0 response.
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// JSONRPCError models a standard JSON-RPC 2.0 error.
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *JSONRPCError) Error() string {
	return e.Message
}

// ClientInfo identifies the client connecting to the MCP server.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeParams models params sent in the `initialize` method.
type InitializeParams struct {
	ProtocolVersion string                 `json:"protocolVersion"`
	Capabilities    map[string]any         `json:"capabilities"`
	ClientInfo      ClientInfo             `json:"clientInfo"`
}

// InitializeResult models the result returned from `initialize`.
type InitializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ServerInfo      ServerInfo     `json:"serverInfo"`
}

// ServerInfo identifies the remote MCP server.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// ListToolsParams models params sent in `tools/list`.
type ListToolsParams struct {
	Cursor string `json:"cursor,omitempty"`
}

// ListToolsResult models the result returned from `tools/list`.
type ListToolsResult struct {
	Tools      []MCPTool `json:"tools"`
	NextCursor string    `json:"nextCursor,omitempty"`
}

// MCPTool models a tool definition returned by an MCP server.
type MCPTool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
}

// CallToolParams models params sent in `tools/call`.
type CallToolParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// ToolCallContent represents a single content item returned by a tool call.
type ToolCallContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// ToolCallResult models the result returned by `tools/call`.
type ToolCallResult struct {
	Content []ToolCallContent `json:"content"`
	IsError bool              `json:"isError,omitempty"`
}

// DiscoveryResult models the discovered endpoints for OAuth.
type DiscoveryResult struct {
	Issuer                string   `json:"issuer,omitempty"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint,omitempty"`
	RevocationEndpoint    string   `json:"revocation_endpoint,omitempty"`
	ScopesSupported       []string `json:"scopes_supported,omitempty"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported,omitempty"`
	Resource              string   `json:"resource,omitempty"`
}

// ClientRegistrationRequest models the RFC 7591 dynamic client registration payload.
type ClientRegistrationRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types,omitempty"`
	ResponseTypes           []string `json:"response_types,omitempty"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method,omitempty"`
}

// ClientRegistrationResult models RFC 7591 registration response.
type ClientRegistrationResult struct {
	ClientID              string `json:"client_id"`
	ClientSecret          string `json:"client_secret,omitempty"`
	ClientIDIssuedAt      int64  `json:"client_id_issued_at,omitempty"`
	ClientSecretExpiresAt int64  `json:"client_secret_expires_at,omitempty"`
}

// TokenResponse models the OAuth 2.0 token response.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// PendingAuth tracks an in-flight OAuth authorization session.
type PendingAuth struct {
	State        string    `json:"state"`
	ServerID     string    `json:"server_id"`
	CodeVerifier string    `json:"code_verifier"`
	RedirectURI  string    `json:"redirect_uri"`
	ExpiresAt    time.Time `json:"expires_at"`
}
