# Implementation Plan: Router-Level Remote MCP Server Configuration with OAuth & Worker Tool Distribution

**Plan ID:** PLAN-ROUTER-MCP-001  
**Specification:** `docs/specs/router-mcp-oauth-tools-spec.md` (`SPEC.md`)  
**Status:** In Progress (Dev-Loop Implementation)  
**Authors:** Wolfgang Meyers, Mecha Core Team  
**Date:** October 2024  
**Primary Target:** Router Go Backend (`router-proxy`), Web UI (`web/router-ui-react`), Worker Tool Runtimes (Embedded Go & Managed Pi RPC)  

---

## 1. Overview & System Architecture

This plan executes the technical delivery of **Router-Level Remote MCP Server management** with OAuth 2.0/2.1 PKCE discovery, persistent SQLite storage, Streamable HTTP JSON-RPC 2.0 client pooling, and transparent tool distribution to agent workers.

Per the specification invariant:
> **"The tools should look no different to the workers than any other tools."**

Agent workers (both Embedded Go workers and Managed Pi RPC processes) receive remote MCP capabilities seamlessly as native tools with identical parameter schemas, execution flows, and error semantics.

### System Architecture Flow

```
                      +------------------------------------------+
                      |         Web UI (React Settings)          |
                      |  - MCPServersDialog.tsx                  |
                      |  - Sidebar: "MCP Servers"                |
                      +--------------------+---------------------+
                                           | REST & SSE/WS
                                           v
+-----------------------------------------------------------------------------------------------+
|                                      ROUTER-PROXY (Go)                                        |
|                                                                                               |
|  [ Transport Layer: internal/transport/mcp_http.go ]                                          |
|    - GET/POST/PUT/DELETE /v1/settings/mcp-servers                                             |
|    - POST /v1/settings/mcp-servers/{id}/sync & /refresh-tools                                 |
|    - POST /v1/settings/mcp-servers/{id}/auth/start (PKCE init)                                |
|    - GET  /v1/mcp/oauth/callback (Authorization code & token exchange)                         |
|    - POST /v1/settings/mcp-servers/{id}/auth/revoke (RFC 7009 revocation & needs_auth)         |
|                                                                                               |
|  [ MCP Manager & Core: internal/mcp/ ]                                                        |
|    - OAuth Coordinator: Discovery (.well-known), Dynamic Registration (RFC 7591), PKCE S256   |
|    - Token Lifecycle: Proactive auto-refresh (<60s remaining), serialized mutex, AES-GCM enc  |
|    - HTTP Client Pool: Streamable HTTP (POST /mcp), initialize, tools/list, tools/call        |
|    - Tool Catalog & Disambiguation: Cache, collision guard (<slug>_<name>), schema conversion  |
|                                                                                               |
|  [ Persistence Layer: internal/store/ ]                                                       |
|    - Migration 105: managed_mcp_servers, managed_mcp_tokens, managed_mcp_tools                |
|    - Store Repository: managed_mcp.go CRUD & token encryption helpers                         |
|                                                                                               |
|  [ Worker Tool Distribution ]                                                                 |
|    - Embedded Go: mcpManager.ActiveAgentTools() -> session.Tools (agent.AgentTool)            |
|    - Managed Pi RPC: Tool manifest injection & root control handler -> tools/call             |
+-----------------------------------------------------------------------------------------------+
                                 |                                 |
               Streamable HTTP   |                                 | OAuth 2.0 PKCE
               JSON-RPC 2.0      v                                 v
                     +-----------------------+           +-----------------------+
                     |   Remote MCP Server   |           |  OAuth Authorization  |
                     | (e.g. Podclave /mcp)  |           |     Server (AS)       |
                     +-----------------------+           +-----------------------+
```

---

## 2. Phase 1: Storage & Migrations

### 2.1 Schema Definition (`router-proxy/internal/store/migrations/105_managed_mcp_servers.sql`)
- Create new migration file `105_managed_mcp_servers.sql` with strict foreign key constraints and indexes.
```sql
-- Migration 105: Managed Remote MCP Servers, OAuth Tokens, and Tool Registry

CREATE TABLE IF NOT EXISTS managed_mcp_servers (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    url TEXT NOT NULL UNIQUE,
    auth_mode TEXT NOT NULL CHECK (auth_mode IN ('none', 'bearer', 'oauth')),
    bearer_token TEXT, -- Optional encrypted static token
    client_id TEXT,    -- Discovered or configured OAuth Client ID
    client_secret TEXT,-- Encrypted OAuth Client Secret (if confidential)
    auth_server_url TEXT,
    token_endpoint TEXT,
    authorization_endpoint TEXT,
    tool_prefix TEXT,
    timeout_ms INTEGER NOT NULL DEFAULT 30000,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    status TEXT NOT NULL DEFAULT 'unconfigured' CHECK (status IN ('unconfigured', 'connecting', 'connected', 'needs_auth', 'error')),
    last_error TEXT,
    last_sync_at TEXT,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS managed_mcp_tokens (
    server_id TEXT PRIMARY KEY REFERENCES managed_mcp_servers(id) ON DELETE CASCADE,
    access_token_enc TEXT NOT NULL,
    refresh_token_enc TEXT,
    token_type TEXT NOT NULL DEFAULT 'Bearer',
    expires_at TEXT,
    scope TEXT,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS managed_mcp_tools (
    id TEXT PRIMARY KEY, -- server_id + ":" + original_name
    server_id TEXT NOT NULL REFERENCES managed_mcp_servers(id) ON DELETE CASCADE,
    original_name TEXT NOT NULL,
    exposed_name TEXT NOT NULL,
    description TEXT,
    input_schema_json TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(server_id, original_name),
    UNIQUE(exposed_name)
);

CREATE INDEX IF NOT EXISTS idx_managed_mcp_tools_server ON managed_mcp_tools(server_id);
CREATE INDEX IF NOT EXISTS idx_managed_mcp_servers_status ON managed_mcp_servers(status);
```

### 2.2 Go Store Repository (`router-proxy/internal/store/managed_mcp.go`)
- **Data Models**:
  - `ManagedMCPServer`: `ID`, `Name`, `URL`, `AuthMode`, `BearerToken`, `ClientID`, `ClientSecret`, `AuthServerURL`, `TokenEndpoint`, `AuthorizationEndpoint`, `ToolPrefix`, `TimeoutMS`, `Enabled`, `Status`, `LastError`, `LastSyncAt`, `CreatedAt`, `UpdatedAt`.
  - `ManagedMCPToken`: `ServerID`, `AccessToken`, `RefreshToken`, `TokenType`, `ExpiresAt`, `Scope`, `UpdatedAt`.
  - `ManagedMCPTool`: `ID`, `ServerID`, `OriginalName`, `ExposedName`, `Description`, `InputSchemaJSON`, `CreatedAt`.
- **Encryption Helpers (`router-proxy/internal/crypto/secret.go`)**:
  - Implement an explicit key derivation module in `router-proxy/internal/crypto/secret.go` providing a stable 32-byte key for AES-256-GCM symmetric encryption of `access_token`, `refresh_token`, and static `bearer_token`.
  - Derivation priority:
    1. Read `MECHA_SECRET_KEY` environment variable (hex- or base64-encoded 32 bytes) if set.
    2. Read or generate a machine/router-local secret file at `<router_state_dir>/secret.key` with strict `0600` file permissions (and non-world/non-group writable ancestor validation).
    3. Fallback: Derive a stable 32-byte key using HKDF-SHA256 from platform machine-id (`/etc/machine-id` or macOS `IOPlatformUUID`) and the canonical router state path.
  - Standard AES-256-GCM encrypt/decrypt functions: nonce prepended to ciphertext, authenticated encryption integrity validation.
- **Repository Methods on `*store.Store`**:
  - `ListMCPServers(ctx context.Context) ([]ManagedMCPServer, error)`
  - `GetMCPServer(ctx context.Context, id string) (*ManagedMCPServer, error)`
  - `GetMCPServerByURL(ctx context.Context, url string) (*ManagedMCPServer, error)`
  - `UpsertMCPServer(ctx context.Context, s ManagedMCPServer) error`
  - `UpdateMCPServerStatus(ctx context.Context, id string, status string, lastError string) error`
  - `DeleteMCPServer(ctx context.Context, id string) error`
  - `GetMCPToken(ctx context.Context, serverID string) (*ManagedMCPToken, error)`
  - `SaveMCPToken(ctx context.Context, token ManagedMCPToken) error`
  - `DeleteMCPToken(ctx context.Context, serverID string) error`
  - `ReplaceMCPTools(ctx context.Context, serverID string, tools []ManagedMCPTool) error`
  - `ListMCPTools(ctx context.Context, serverID string) ([]ManagedMCPTool, error)`
  - `ListAllActiveMCPTools(ctx context.Context) ([]ManagedMCPTool, error)`
- **Unit Tests (`managed_mcp_test.go`, `secret_test.go`)**:
  - Migration idempotency and schema verification.
  - Key derivation and AES-256-GCM encryption/decryption roundtrips, corrupt ciphertext detection, and permission checks.
  - CRUD operations on servers and tool records.
  - Cascading deletion of tokens and tools upon server removal.
  - Token encryption and decryption roundtrips.

---

## 3. Phase 2: MCP OAuth & Discovery Client (`router-proxy/internal/mcp/`)

### 3.1 OAuth Discovery (`oauth_discovery.go`)
- **Protected Resource Metadata (RFC 9207 / RFC 8414 draft)**:
  - Step 1: Probe `GET <mcp_url>/.well-known/oauth-protected-resource/mcp` and `<mcp_url>/.well-known/oauth-protected-resource`.
  - Step 2: Handle `WWW-Authenticate` header challenges from unauthorized `POST /mcp` requests. Parse `resource_metadata` or `as_uri` parameters.
  - Step 3: Extract authorization server issuer URLs (e.g., `https://auth.podclave.com`).
- **Authorization Server Metadata**:
  - Fetch `<issuer>/.well-known/oauth-authorization-server` or fallback to `<issuer>/.well-known/openid-configuration`.
  - Parse metadata:
    - `authorization_endpoint`
    - `token_endpoint`
    - `registration_endpoint`
    - `code_challenge_methods_supported` (confirm `S256` presence).

### 3.2 Dynamic Client Registration & PKCE Generator (`oauth_pkce.go`)
- **RFC 7591 Dynamic Client Registration**:
  - If `client_id` is missing and `registration_endpoint` is provided:
    - Send `POST <registration_endpoint>` with body:
      ```json
      {
        "client_name": "Mecha Router MCP Client",
        "redirect_uris": ["http://127.0.0.1:<listen_port>/v1/mcp/oauth/callback"],
        "grant_types": ["authorization_code", "refresh_token"],
        "response_types": ["code"],
        "token_endpoint_auth_method": "none"
      }
      ```
    - Parse assigned `client_id` and optional `client_secret`.
    - Persist to `managed_mcp_servers` table.
- **PKCE Generation (RFC 7636)**:
  - Generate cryptographically secure `code_verifier` (minimum 64 characters, unreserved URL charset).
  - Compute `code_challenge = Base64URLEncode(SHA256(code_verifier))`.
  - Maintain short-lived pending authorization transactions (`PendingAuth` in-memory map protected by mutex with 10-minute TTL):
    - `State`: High-entropy 32-byte hex string.
    - `ServerID`: Associated MCP server identifier.
    - `CodeVerifier`: Plaintext verifier to exchange.
    - `RedirectURI`: Bound callback URI.
    - `ExpiresAt`: Nonce expiration timestamp.

### 3.3 Token Exchange & Serialized Refresh (`oauth_tokens.go`)
- **Authorization Code Exchange**:
  - Exchange code at `token_endpoint` with `grant_type=authorization_code`, `code_verifier`, and `redirect_uri`.
  - Store encrypted tokens and calculate `expires_at = time.Now().Add(expires_in * time.Second)`.
- **Serialized Proactive Refresh**:
  - Implement per-server sync mutex (`sync.Mutex` per `serverID`) to prevent thundering herd when multiple concurrent tool calls encounter an expiring token.
  - Check remaining lifetime: If $T_{\text{remaining}} < 60\text{s}$ and `refresh_token` exists, trigger proactive refresh.
  - Handle refresh failures:
    - If `invalid_grant` or refresh token expired: transition server status to `needs_auth`, invalidate cached token, return structured error for operator re-auth.

---

## 4. Phase 3: MCP Streamable HTTP Client & Tool Discovery (`router-proxy/internal/mcp/`)

### 4.1 Streamable HTTP Client (`client.go`)
- Implements MCP Specification 2024-11-05 Streamable HTTP transport:
  - `POST <mcp_url>` with `Content-Type: application/json` and `Accept: application/json, text/event-stream`.
  - Shared HTTP transport with keep-alive, connection pooling, and request timeout.
  - Automatic `Authorization: Bearer <token>` injection for `bearer` and `oauth` auth modes.

### 4.2 Handshake & Protocol Negotiation (`handshake.go`)
- **Initialize Handshake**:
  - Send JSON-RPC 2.0 `initialize`:
    ```json
    {
      "jsonrpc": "2.0",
      "id": 1,
      "method": "initialize",
      "params": {
        "protocolVersion": "2024-11-05",
        "capabilities": {
          "roots": { "listChanged": false },
          "sampling": {}
        },
        "clientInfo": { "name": "mecha-router", "version": "1.0.0" }
      }
    }
    ```
  - Verify server response, extract supported protocol version and server capabilities.
  - Send post-handshake notification `notifications/initialized`.

### 4.3 Tool Discovery, Schema Mapping & SSE Notifications (`discovery.go`, `sse.go`)
- **`tools/list` Retrieval**:
  - Call `tools/list` with cursor pagination support if `nextCursor` is returned.
  - Extract tool list: `name`, `description`, `inputSchema`.
- **SSE Real-Time Stream & Fallback Sync**:
  - For servers advertising SSE support over Streamable HTTP, establish and maintain an SSE stream listening for `notifications/tools/list_changed`.
  - When a `notifications/tools/list_changed` event is received:
    - Trigger immediate `tools/list` re-discovery.
    - Atomically update SQLite cached tools (`managed_mcp_tools`) and in-memory registry.
    - Broadcast `mcp_server_updated` over `/v1/agents` WebSocket to notify UI and update active worker tool manifests.
  - Periodic & manual fallbacks:
    - If SSE is unavailable, disconnected, or unsupported by the server, fall back to periodic polling every 15 minutes.
    - Support manual sync on-demand via `POST /v1/settings/mcp-servers/{id}/sync` (or `/refresh-tools`).
- **Tool Name Sanitization & Collision Guard**:
  - Ensure tool name adheres to `^[a-zA-Z0-9_-]{1,64}$`.
  - If server has configured `tool_prefix` (e.g. `podclave_`), format as `<tool_prefix><name>`.
  - If a collision occurs with native tools (`read`, `bash`, `edit`, `write`, `scratchpad`, `goal`, `mecha_worker`, etc.), automatically prefix with `<server_id>_<name>`.
- **JSON Schema Mapping**:
  - Map remote `inputSchema` to `ai.Schema` (or schema object compatible with embedded agent runtime and Pi RPC manifest).
  - Store raw and parsed schemas in `managed_mcp_tools`.

### 4.4 Tool Execution & Reactive 401 Retry (`execution.go`)
- **`tools/call` Invocation**:
  - Send JSON-RPC 2.0 `tools/call` with `name` and `arguments`.
  - Apply configured request timeout (`timeout_ms`, default 30s) using Go context.
- **Reactive 401 Retry Handling**:
  - If a `tools/call` request returns HTTP 401 Unauthorized (or equivalent JSON-RPC auth expired error):
    1. Mark the server's cached token as dirty.
    2. Acquire the per-server serialized refresh mutex (`serverID` lock).
    3. Execute token refresh using `refresh_token`.
    4. Upon successful refresh, update stored credentials and replay the failed `tools/call` once transparently.
    5. If refresh fails (e.g., refresh token invalid or revoked) or the replayed call fails with 401 again:
       - Transition server status to `needs_auth`.
       - Broadcast `mcp_server_updated` event over `/v1/agents`.
       - Return a structured tool error to the worker turn:
         `Remote tool "<tool_name>" requires re-authentication. Administrator must re-authorize the server in Router Settings.`
- **Output Unpacking & Error Sanitization**:
  - Remote MCP returns `{ "content": [...], "isError": bool }`.
  - Convert content blocks (`text`, `image`, `resource`) into standard tool result strings and agent content lists.
  - Bound result memory to maximum 512 KiB per call to prevent worker memory exhaustion.
  - Convert `isError: true` or HTTP 5xx failures into structured tool error returns rather than panics.

---

## 5. Phase 4: Router REST Handlers & Transport (`router-proxy/internal/transport/`)

Implement handlers in `router-proxy/internal/transport/mcp_http.go` and register them in `router.go`.

### 5.1 Endpoints Specification
1. **`GET /v1/settings/mcp-servers`**:
   - Returns list of all registered MCP servers, active status, tool counts, and metadata.
2. **`POST /v1/settings/mcp-servers`**:
   - Request body: `{ "name": string, "url": string, "auth_mode": "none"|"bearer"|"oauth", "bearer_token"?: string, "tool_prefix"?: string, "timeout_ms"?: number }`.
   - Validates URL syntax and uniqueness.
   - If `auth_mode == "none"` or `"bearer"`, triggers immediate handshake and tool discovery.
   - If `auth_mode == "oauth"`, initiates discovery of authorization server and returns `{ "server": ..., "auth_url": string }` if immediate auth is required.
3. **`GET /v1/settings/mcp-servers/{id}`**:
   - Returns detailed server configuration, token status (expires_at, scope, without exposing plaintext secret), and list of discovered tools.
4. **`PUT /v1/settings/mcp-servers/{id}`**:
   - Updates editable fields (`name`, `enabled`, `tool_prefix`, `timeout_ms`, `bearer_token`).
   - If `enabled` changes, updates active worker tool catalog.
5. **`DELETE /v1/settings/mcp-servers/{id}`**:
   - Removes server, deletes tokens and tool cache, and broadcasts tool removal to workers.
6. **`POST /v1/settings/mcp-servers/{id}/refresh-tools` (or `/sync`)**:
   - Forces immediate `tools/list` query and updates `managed_mcp_tools`.
7. **`POST /v1/settings/mcp-servers/{id}/auth/start`**:
   - Generates PKCE verifier/challenge and pending state.
   - Returns `{ "authorization_url": "https://auth.podclave.com/authorize?..." }`.
8. **`GET /v1/mcp/oauth/callback`**:
   - Handles redirect from Authorization Server with `?code=...&state=...`.
   - Validates state, performs token exchange, saves encrypted tokens, updates server status to `connected`.
   - Automatically kicks off initial `initialize` and `tools/list` discovery.
   - Renders a clean HTML success/failure page that posts message to opener or closes the popup window.
9. **`POST /v1/settings/mcp-servers/{id}/auth/revoke`**:
   - Performs RFC 7009 token revocation against the authorization server's `revocation_endpoint` (if advertised).
   - Clears stored access and refresh tokens in `managed_mcp_tokens`.
   - Transitions server status to `needs_auth` and clears active tool catalog.
   - Broadcasts `mcp_server_updated` over `/v1/agents` WebSocket.

### 5.2 Live WebSocket Event Contract (`/v1/agents`)
- The router broadcasts real-time state change events over the `/v1/agents` WebSocket connection to ensure synchronized UI and worker manifests.
- Trigger points:
  - Server creation or deletion (`POST` or `DELETE /v1/settings/mcp-servers`).
  - Server status transition (`unconfigured`, `connecting`, `connected`, `needs_auth`, `error`).
  - Tool refresh completion (periodic, manual sync, or SSE list change).
  - Explicit revocation (`POST /v1/settings/mcp-servers/{id}/auth/revoke`).
- Event Payload:
  ```json
  {
    "type": "mcp_server_updated",
    "server": {
      "id": "podclave-prod",
      "name": "Podclave Enterprise Tools",
      "status": "connected",
      "tools_count": 8,
      "last_sync_at": "2024-10-16T17:30:00Z"
    }
  }
  ```

---

## 6. Phase 5: Worker Tool Bridging

Ensure tools are distributed seamlessly so that remote MCP tools are indistinguishable from native tools.

### 6.1 Embedded Go Mode (`router-proxy/internal/managed/embedded_session.go`)
- Inject active MCP tools dynamically when constructing `session.Tools`:
  ```go
  // In embedded_session.go runLoop() / tool setup:
  if m.mcpManager != nil {
      mcpTools := m.mcpManager.ActiveAgentTools(owner)
      tools = append(tools, mcpTools...)
  }
  ```
- Each MCP tool implements `agent.AgentTool`:
  - `Name`: Discovered tool name (e.g. `create_issue` or `podclave_create_issue`).
  - `Description`: Tool description from remote server.
  - `Parameters`: Translated `ai.Schema`.
  - `Execute(ctx, input)`: Dispatches to `mcpManager.CallTool(ctx, serverID, toolName, input)`.
  - Transparent error handling: If the server is unreachable or returns 503, returns `agent.AgentToolResult{ Content: "...", IsError: true }` without crashing the Go worker loop.

### 6.2 Managed Pi RPC Mode (`router-proxy/internal/managed/`)
- **Manifest Injection (`manifest.go`, `supervisor.go`)**:
  - Dynamically include enabled MCP tools in the active tool manifest generated for Pi sessions during bootstrap and supplemental updates (`mecha-install-tools-v1`).
  - Map capability IDs to remote tool handlers.
- **Root Control Dispatch (`root_control_handlers.go`, `root_control_manager.go`)**:
  - Route execution of remote tool operations across the RPC bridge to `mcpManager.CallTool`.
  - Package result and return via standard `mecha-control-result-v1` envelope.

---

## 7. Phase 6: Web UI Integration (`web/router-ui-react`)

### 7.1 MCP Servers Dialog (`src/components/MCPServersDialog.tsx`)
- Build modern React dialog following existing Mecha UI conventions:
  - Uses `DialogShell`.
  - Compact circular buttons (`.icon-btn-round`: 24px x 24px, `border-radius: 999px`, translucent hover states).
  - Server list showing:
    - Server Name & URL.
    - Connection Badge (`connected` green, `connecting` yellow, `needs_auth` amber, `error` red).
    - Tool count badge (e.g. "8 tools").
    - Action buttons: "Sync", "Authorize" (if OAuth needed), "Edit", "Delete".
  - Add / Edit Server Form:
    - Input fields: Name, URL, Auth Mode (`none`, `bearer`, `oauth`), Bearer Token, Tool Prefix, Timeout (ms).
    - Validation with responsive error banners.
  - OAuth Popup Handling:
    - Clicking "Authorize" opens authorization URL in a centered popup window.
    - Listens for OAuth completion callback via `window.addEventListener("message", ...)` to refresh server status automatically.

### 7.2 Sidebar & App Integration
- **`Sidebar.tsx`**: Add "MCP Servers" item to the ellipsis menu alongside "Provider settings" and "System 1 Config".
- **`App.tsx`**: Add dialog state `"mcp-servers"` to `Dialog` type and wire `MCPServersDialog`.
- **`http-client.ts`**: Add MCP API client methods:
  - `getMCPServers()`, `addMCPServer(data)`, `updateMCPServer(id, data)`, `deleteMCPServer(id)`, `syncMCPServer(id)`, `startMCPAuth(id)`.

---

## 8. Dev-Loop Verification & Testing Strategy

Following Mecha test policy: Prioritize behavioral functional tests with deterministic mocks (no real LLM or external service calls).

### 8.1 Test Matrix

| Test Suite | File | Focus |
|---|---|---|
| **Store & Migration Tests** | `internal/store/managed_mcp_test.go` | Schema migration 105, foreign keys, token encryption roundtrip, cascade deletes. |
| **OAuth Discovery & PKCE** | `internal/mcp/oauth_test.go` | Mock AS discovery, RFC 7591 registration, S256 PKCE challenge/verifier verification, code exchange, token refresh on expiry. |
| **Streamable HTTP & RPC** | `internal/mcp/client_test.go` | JSON-RPC `initialize`, `notifications/initialized`, `tools/list` pagination, `tools/call` error handling and timeouts. |
| **REST Handlers & Transport** | `internal/transport/mcp_http_test.go` | Full HTTP lifecycle for `/v1/settings/mcp-servers/*` and `/v1/mcp/oauth/callback`. |
| **Embedded Worker Parity** | `internal/managed/embedded_mcp_tools_test.go` | Verifies remote MCP tools appear in embedded worker session, execute cleanly, and recover from remote 502/503 errors. |
| **Managed Pi RPC Parity** | `internal/managed/managed_mcp_rpc_test.go` | Verifies tool injection into manifest and execution via `mecha-control-result-v1`. |
| **Web UI Component Tests** | `src/components/MCPServersDialog.test.tsx` | Vitest tests for rendering server list, triggering auth flow, and error states. |

### 8.2 Podclave Endpoint Compatibility
- Verify real-world discovery compatibility against Podclave metadata layout:
  - Mock endpoint representing `https://podclave.com/mcp` and `https://auth.podclave.com`.
  - Validate protected resource endpoint `.well-known/oauth-protected-resource/mcp` and OAuth authorization server discovery.

---

## 9. Traceability & Acceptance Criteria

- [ ] **AC1 (Storage)**: Migration 105 applies cleanly; servers, tokens, and tools are persisted with AES-256-GCM token encryption and cascade deletion.
- [ ] **AC2 (OAuth Discovery & PKCE)**: Automatic discovery of OAuth metadata, dynamic client registration (RFC 7591), and secure PKCE S256 code challenge exchange.
- [ ] **AC3 (Streamable HTTP JSON-RPC)**: Robust Streamable HTTP transport supporting `initialize`, `tools/list`, and `tools/call`.
- [ ] **AC4 (Token Auto-Refresh)**: Serialized proactive token refresh (<60s expiry) prevents request failures; unrecoverable auth transitions cleanly to `needs_auth`.
- [ ] **AC5 (Tool Parity for Workers)**: Workers receive MCP tools indistinguishable from native tools (`agent.AgentTool` in Go, RPC manifest in Pi).
- [ ] **AC6 (Failure Containment)**: Remote server 502/503, timeouts, or token expiry return structured tool errors to the LLM turn without crashing the worker or router.
- [ ] **AC7 (REST Endpoints & Web UI)**: Operator can manage servers, view connectivity, authorize via OAuth popup, and sync tools directly from Settings in the React Web UI.
