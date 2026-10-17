package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/crypto"
)

// MCPServer models a remote MCP server configuration.
type MCPServer struct {
	ID                   string     `json:"id"`
	Name                 string     `json:"name"`
	URL                  string     `json:"url"`
	AuthType             string     `json:"auth_type"`
	ClientID             *string    `json:"client_id,omitempty"`
	ClientSecret         *string    `json:"client_secret,omitempty"`
	TokenEndpoint        *string    `json:"token_endpoint,omitempty"`
	AuthEndpoint         *string    `json:"auth_endpoint,omitempty"`
	RegistrationEndpoint *string    `json:"registration_endpoint,omitempty"`
	RevocationEndpoint   *string    `json:"revocation_endpoint,omitempty"`
	Scopes               *string    `json:"scopes,omitempty"`
	Status               string     `json:"status"`
	LastError            *string    `json:"last_error,omitempty"`
	ToolsCount           int        `json:"tools_count"`
	LastSyncAt           *time.Time `json:"last_sync_at,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

// MCPToken models plaintext tokens stored securely at rest using AES-256-GCM.
type MCPToken struct {
	ServerID     string     `json:"server_id"`
	AccessToken  *string    `json:"access_token,omitempty"`
	RefreshToken *string    `json:"refresh_token,omitempty"`
	TokenType    string     `json:"token_type"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	Scopes       *string    `json:"scopes,omitempty"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// MCPToolRecord models an individual tool discovered from an MCP server.
type MCPToolRecord struct {
	ID               string    `json:"id"`
	ServerID         string    `json:"server_id"`
	Name             string    `json:"name"`
	PrefixedName     string    `json:"prefixed_name"`
	Description      *string   `json:"description,omitempty"`
	InputSchemaJSON  string    `json:"input_schema_json"`
	OutputSchemaJSON *string   `json:"output_schema_json,omitempty"`
	DefinitionSHA256 string    `json:"definition_sha256"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

var (
	ErrMCPServerNotFound = errors.New("mcp server not found")
	ErrMCPTokenNotFound  = errors.New("mcp token not found")
)

// CreateMCPServer inserts a new MCP server.
func (s *Store) CreateMCPServer(ctx context.Context, server *MCPServer) error {
	if server == nil {
		return errors.New("server cannot be nil")
	}
	if server.ID == "" {
		if s.ids != nil {
			id, err := s.ids.Next()
			if err == nil && id != "" {
				server.ID = id
			}
		}
		if server.ID == "" {
			server.ID = uuid.New().String()
		}
	}

	now := s.clock.Now().UTC()
	if server.CreatedAt.IsZero() {
		server.CreatedAt = now
	}
	if server.UpdatedAt.IsZero() {
		server.UpdatedAt = now
	}
	if server.Status == "" {
		server.Status = "unconfigured"
	}
	if server.AuthType == "" {
		server.AuthType = "none"
	}

	var lastSyncVal any
	if server.LastSyncAt != nil && !server.LastSyncAt.IsZero() {
		lastSyncVal = server.LastSyncAt.UTC().Format(time.RFC3339)
	}

	return s.runImmediate(ctx, func() error {
		_, err := s.db.ExecContext(ctx, `
INSERT INTO managed_mcp_servers (
  id, name, url, auth_type, client_id, client_secret,
  token_endpoint, auth_endpoint, registration_endpoint, revocation_endpoint,
  scopes, status, last_error, tools_count, last_sync_at, created_at, updated_at
) VALUES (
  ?, ?, ?, ?, ?, ?,
  ?, ?, ?, ?,
  ?, ?, ?, ?, ?, ?, ?
)`,
			server.ID,
			server.Name,
			server.URL,
			server.AuthType,
			server.ClientID,
			server.ClientSecret,
			server.TokenEndpoint,
			server.AuthEndpoint,
			server.RegistrationEndpoint,
			server.RevocationEndpoint,
			server.Scopes,
			server.Status,
			server.LastError,
			server.ToolsCount,
			lastSyncVal,
			server.CreatedAt.UTC().Format(time.RFC3339),
			server.UpdatedAt.UTC().Format(time.RFC3339),
		)
		return err
	})
}

// GetMCPServer retrieves an MCP server by its ID.
func (s *Store) GetMCPServer(ctx context.Context, id string) (*MCPServer, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT
  id, name, url, auth_type, client_id, client_secret,
  token_endpoint, auth_endpoint, registration_endpoint, revocation_endpoint,
  scopes, status, last_error, tools_count, last_sync_at, created_at, updated_at
FROM managed_mcp_servers
WHERE id = ?`, id)

	server, err := scanMCPServer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrMCPServerNotFound
	}
	return server, err
}

// GetMCPServerByURL retrieves an MCP server by its unique endpoint URL.
func (s *Store) GetMCPServerByURL(ctx context.Context, url string) (*MCPServer, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT
  id, name, url, auth_type, client_id, client_secret,
  token_endpoint, auth_endpoint, registration_endpoint, revocation_endpoint,
  scopes, status, last_error, tools_count, last_sync_at, created_at, updated_at
FROM managed_mcp_servers
WHERE url = ?`, url)

	server, err := scanMCPServer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrMCPServerNotFound
	}
	return server, err
}

// ListMCPServers returns all configured MCP servers ordered by created_at ASC.
func (s *Store) ListMCPServers(ctx context.Context) ([]*MCPServer, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT
  id, name, url, auth_type, client_id, client_secret,
  token_endpoint, auth_endpoint, registration_endpoint, revocation_endpoint,
  scopes, status, last_error, tools_count, last_sync_at, created_at, updated_at
FROM managed_mcp_servers
ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var servers []*MCPServer
	for rows.Next() {
		server, err := scanMCPServer(rows)
		if err != nil {
			return nil, err
		}
		servers = append(servers, server)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return servers, nil
}

// UpdateMCPServer updates an existing MCP server.
func (s *Store) UpdateMCPServer(ctx context.Context, server *MCPServer) error {
	if server == nil {
		return errors.New("server cannot be nil")
	}

	server.UpdatedAt = s.clock.Now().UTC()

	var lastSyncVal any
	if server.LastSyncAt != nil && !server.LastSyncAt.IsZero() {
		lastSyncVal = server.LastSyncAt.UTC().Format(time.RFC3339)
	}

	return s.runImmediate(ctx, func() error {
		res, err := s.db.ExecContext(ctx, `
UPDATE managed_mcp_servers SET
  name = ?,
  url = ?,
  auth_type = ?,
  client_id = ?,
  client_secret = ?,
  token_endpoint = ?,
  auth_endpoint = ?,
  registration_endpoint = ?,
  revocation_endpoint = ?,
  scopes = ?,
  status = ?,
  last_error = ?,
  tools_count = ?,
  last_sync_at = ?,
  updated_at = ?
WHERE id = ?`,
			server.Name,
			server.URL,
			server.AuthType,
			server.ClientID,
			server.ClientSecret,
			server.TokenEndpoint,
			server.AuthEndpoint,
			server.RegistrationEndpoint,
			server.RevocationEndpoint,
			server.Scopes,
			server.Status,
			server.LastError,
			server.ToolsCount,
			lastSyncVal,
			server.UpdatedAt.UTC().Format(time.RFC3339),
			server.ID,
		)
		if err != nil {
			return err
		}
		rowsAffected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if rowsAffected == 0 {
			return ErrMCPServerNotFound
		}
		return nil
	})
}

// DeleteMCPServer removes an MCP server and cascades deletion to tokens and tools.
func (s *Store) DeleteMCPServer(ctx context.Context, id string) error {
	return s.runImmediate(ctx, func() error {
		res, err := s.db.ExecContext(ctx, `DELETE FROM managed_mcp_servers WHERE id = ?`, id)
		if err != nil {
			return err
		}
		rowsAffected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if rowsAffected == 0 {
			return ErrMCPServerNotFound
		}
		return nil
	})
}

// SetMCPToken encrypts and saves OAuth/bearer tokens for an MCP server.
func (s *Store) SetMCPToken(ctx context.Context, token *MCPToken, key []byte) error {
	if token == nil {
		return errors.New("token cannot be nil")
	}
	if len(key) != crypto.KeySize {
		return fmt.Errorf("invalid key size: got %d, expected %d", len(key), crypto.KeySize)
	}

	var encAccess *string
	if token.AccessToken != nil {
		ciphertext, err := crypto.Encrypt([]byte(*token.AccessToken), key)
		if err != nil {
			return fmt.Errorf("encrypt access token: %w", err)
		}
		encAccess = &ciphertext
	}

	var encRefresh *string
	if token.RefreshToken != nil {
		ciphertext, err := crypto.Encrypt([]byte(*token.RefreshToken), key)
		if err != nil {
			return fmt.Errorf("encrypt refresh token: %w", err)
		}
		encRefresh = &ciphertext
	}

	tokenType := token.TokenType
	if tokenType == "" {
		tokenType = "Bearer"
	}

	token.UpdatedAt = s.clock.Now().UTC()

	var expiresVal any
	if token.ExpiresAt != nil && !token.ExpiresAt.IsZero() {
		expiresVal = token.ExpiresAt.UTC().Format(time.RFC3339)
	}

	return s.runImmediate(ctx, func() error {
		_, err := s.db.ExecContext(ctx, `
INSERT INTO managed_mcp_tokens (
  server_id, encrypted_access_token, encrypted_refresh_token,
  token_type, expires_at, scopes, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(server_id) DO UPDATE SET
  encrypted_access_token = excluded.encrypted_access_token,
  encrypted_refresh_token = excluded.encrypted_refresh_token,
  token_type = excluded.token_type,
  expires_at = excluded.expires_at,
  scopes = excluded.scopes,
  updated_at = excluded.updated_at
`,
			token.ServerID,
			encAccess,
			encRefresh,
			tokenType,
			expiresVal,
			token.Scopes,
			token.UpdatedAt.UTC().Format(time.RFC3339),
		)
		return err
	})
}

// GetMCPToken retrieves and decrypts the tokens for an MCP server.
func (s *Store) GetMCPToken(ctx context.Context, serverID string, key []byte) (*MCPToken, error) {
	if len(key) != crypto.KeySize {
		return nil, fmt.Errorf("invalid key size: got %d, expected %d", len(key), crypto.KeySize)
	}

	var (
		sID        string
		encAccess  sql.NullString
		encRefresh sql.NullString
		tokenType  string
		expiresRaw sql.NullString
		scopes     sql.NullString
		updatedRaw string
	)

	err := s.db.QueryRowContext(ctx, `
SELECT
  server_id, encrypted_access_token, encrypted_refresh_token,
  token_type, expires_at, scopes, updated_at
FROM managed_mcp_tokens
WHERE server_id = ?`, serverID).Scan(
		&sID,
		&encAccess,
		&encRefresh,
		&tokenType,
		&expiresRaw,
		&scopes,
		&updatedRaw,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrMCPTokenNotFound
	}
	if err != nil {
		return nil, err
	}

	var decAccess *string
	if encAccess.Valid && encAccess.String != "" {
		dec, err := crypto.Decrypt(encAccess.String, key)
		if err != nil {
			return nil, fmt.Errorf("decrypt access token: %w", err)
		}
		str := string(dec)
		decAccess = &str
	}

	var decRefresh *string
	if encRefresh.Valid && encRefresh.String != "" {
		dec, err := crypto.Decrypt(encRefresh.String, key)
		if err != nil {
			return nil, fmt.Errorf("decrypt refresh token: %w", err)
		}
		str := string(dec)
		decRefresh = &str
	}

	var expiresAt *time.Time
	if expiresRaw.Valid && expiresRaw.String != "" {
		if t, err := parseFlexTime(expiresRaw.String); err == nil {
			expiresAt = &t
		}
	}

	updatedAt, _ := parseFlexTime(updatedRaw)

	res := &MCPToken{
		ServerID:     sID,
		AccessToken:  decAccess,
		RefreshToken: decRefresh,
		TokenType:    tokenType,
		ExpiresAt:    expiresAt,
		UpdatedAt:    updatedAt,
	}
	if scopes.Valid {
		res.Scopes = &scopes.String
	}

	return res, nil
}

// DeleteMCPToken deletes token credentials for a given MCP server.
func (s *Store) DeleteMCPToken(ctx context.Context, serverID string) error {
	return s.runImmediate(ctx, func() error {
		res, err := s.db.ExecContext(ctx, `DELETE FROM managed_mcp_tokens WHERE server_id = ?`, serverID)
		if err != nil {
			return err
		}
		rowsAffected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if rowsAffected == 0 {
			return ErrMCPTokenNotFound
		}
		return nil
	})
}

// SaveMCPTools replaces the tools registered for a server with the given slice,
// updating tools_count on the server.
func (s *Store) SaveMCPTools(ctx context.Context, serverID string, tools []MCPToolRecord) error {
	now := s.clock.Now().UTC()
	nowStr := now.Format(time.RFC3339)

	return s.runImmediate(ctx, func() error {
		// First verify server exists
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_mcp_servers WHERE id = ?`, serverID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return ErrMCPServerNotFound
		}

		// Delete existing tools for this server
		if _, err := s.db.ExecContext(ctx, `DELETE FROM managed_mcp_tools WHERE server_id = ?`, serverID); err != nil {
			return err
		}

		// Insert new tools
		stmt, err := s.db.PrepareContext(ctx, `
INSERT INTO managed_mcp_tools (
  id, server_id, name, prefixed_name, description,
  input_schema_json, output_schema_json, definition_sha256,
  created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()

		for i := range tools {
			tool := &tools[i]
			if tool.ID == "" {
				tool.ID = fmt.Sprintf("%s:%s", serverID, tool.Name)
			}
			tool.ServerID = serverID
			if tool.CreatedAt.IsZero() {
				tool.CreatedAt = now
			}
			if tool.UpdatedAt.IsZero() {
				tool.UpdatedAt = now
			}

			_, err := stmt.ExecContext(ctx,
				tool.ID,
				serverID,
				tool.Name,
				tool.PrefixedName,
				tool.Description,
				tool.InputSchemaJSON,
				tool.OutputSchemaJSON,
				tool.DefinitionSHA256,
				tool.CreatedAt.UTC().Format(time.RFC3339),
				tool.UpdatedAt.UTC().Format(time.RFC3339),
			)
			if err != nil {
				return fmt.Errorf("insert tool %q: %w", tool.Name, err)
			}
		}

		// Update server tools_count and last_sync_at
		_, err = s.db.ExecContext(ctx, `
UPDATE managed_mcp_servers
SET tools_count = ?, last_sync_at = ?, updated_at = ?
WHERE id = ?`, len(tools), nowStr, nowStr, serverID)
		return err
	})
}

// ListMCPTools returns all tools registered for a given server.
func (s *Store) ListMCPTools(ctx context.Context, serverID string) ([]MCPToolRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT
  id, server_id, name, prefixed_name, description,
  input_schema_json, output_schema_json, definition_sha256,
  created_at, updated_at
FROM managed_mcp_tools
WHERE server_id = ?
ORDER BY name ASC`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanMCPTools(rows)
}

// ListAllActiveMCPTools returns all tools from servers whose status is 'connected'.
func (s *Store) ListAllActiveMCPTools(ctx context.Context) ([]MCPToolRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT
  t.id, t.server_id, t.name, t.prefixed_name, t.description,
  t.input_schema_json, t.output_schema_json, t.definition_sha256,
  t.created_at, t.updated_at
FROM managed_mcp_tools t
JOIN managed_mcp_servers s ON t.server_id = s.id
WHERE s.status = 'connected'
ORDER BY t.prefixed_name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanMCPTools(rows)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMCPServer(scanner rowScanner) (*MCPServer, error) {
	var (
		server       MCPServer
		clientID     sql.NullString
		clientSecret sql.NullString
		tokenEnd     sql.NullString
		authEnd      sql.NullString
		regEnd       sql.NullString
		revEnd       sql.NullString
		scopes       sql.NullString
		lastError    sql.NullString
		lastSyncRaw  sql.NullString
		createdRaw   string
		updatedRaw   string
	)

	err := scanner.Scan(
		&server.ID,
		&server.Name,
		&server.URL,
		&server.AuthType,
		&clientID,
		&clientSecret,
		&tokenEnd,
		&authEnd,
		&regEnd,
		&revEnd,
		&scopes,
		&server.Status,
		&lastError,
		&server.ToolsCount,
		&lastSyncRaw,
		&createdRaw,
		&updatedRaw,
	)
	if err != nil {
		return nil, err
	}

	if clientID.Valid {
		server.ClientID = &clientID.String
	}
	if clientSecret.Valid {
		server.ClientSecret = &clientSecret.String
	}
	if tokenEnd.Valid {
		server.TokenEndpoint = &tokenEnd.String
	}
	if authEnd.Valid {
		server.AuthEndpoint = &authEnd.String
	}
	if regEnd.Valid {
		server.RegistrationEndpoint = &regEnd.String
	}
	if revEnd.Valid {
		server.RevocationEndpoint = &revEnd.String
	}
	if scopes.Valid {
		server.Scopes = &scopes.String
	}
	if lastError.Valid {
		server.LastError = &lastError.String
	}
	if lastSyncRaw.Valid && lastSyncRaw.String != "" {
		if t, err := parseFlexTime(lastSyncRaw.String); err == nil {
			server.LastSyncAt = &t
		}
	}

	server.CreatedAt, _ = parseFlexTime(createdRaw)
	server.UpdatedAt, _ = parseFlexTime(updatedRaw)

	return &server, nil
}

func scanMCPTools(rows *sql.Rows) ([]MCPToolRecord, error) {
	var tools []MCPToolRecord
	for rows.Next() {
		var (
			tool       MCPToolRecord
			desc       sql.NullString
			outSchema  sql.NullString
			createdRaw string
			updatedRaw string
		)

		err := rows.Scan(
			&tool.ID,
			&tool.ServerID,
			&tool.Name,
			&tool.PrefixedName,
			&desc,
			&tool.InputSchemaJSON,
			&outSchema,
			&tool.DefinitionSHA256,
			&createdRaw,
			&updatedRaw,
		)
		if err != nil {
			return nil, err
		}

		if desc.Valid {
			tool.Description = &desc.String
		}
		if outSchema.Valid {
			tool.OutputSchemaJSON = &outSchema.String
		}

		tool.CreatedAt, _ = parseFlexTime(createdRaw)
		tool.UpdatedAt, _ = parseFlexTime(updatedRaw)

		tools = append(tools, tool)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tools, nil
}

func parseFlexTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05Z",
	}
	for _, layout := range formats {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unable to parse timestamp: %q", s)
}
