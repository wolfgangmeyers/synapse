# Router-Level Remote MCP Server Configuration with OAuth & Worker Tool Distribution

**Specification ID:** SPEC-ROUTER-MCP-001  
**Status:** DRAFT (Dev-Loop Phase 1)  
**Authors:** Wolfgang Meyers, Mecha Core Team  
**Date:** October 2024  
**Target Delivery:** Router Go Backend (`router-proxy`), Web UI (`web/router-ui-react`), Embedded & Managed Worker Tool Runtimes  

---

## 1. Executive Summary & Objective

Modern agentic systems require access to external capabilities hosted across disparate, secure enterprise services (e.g. Jira, GitHub, Slack, Podclave, Linear). The Model Context Protocol (MCP) provides a standard JSON-RPC 2.0 interface for LLMs to query and execute tools. However, individual workers should never bear the overhead of maintaining distinct OAuth sessions, executing local Node.js proxies, or interacting with custom CLI wrappers.

This specification details the architecture for **router-level Remote MCP Server management** in `router-proxy`. The router serves as the centralized MCP client and authorization coordinator:
1. **Centralized Configuration & Remote Discovery**: Operators register remote MCP servers via base URL (e.g., `https://podclave.com/mcp`). The router performs automated OAuth 2.0 / 2.1 discovery (`.well-known/oauth-protected-resource/mcp` and `.well-known/oauth-authorization-server`), dynamic client registration (RFC 7591), and manages OAuth PKCE authorization code grants.
2. **Streamable HTTP JSON-RPC Transport**: Router establishes and maintains stateful Streamable HTTP (`POST /mcp`) connections to MCP servers, handles `initialize`, protocol negotiation, and queries `tools/list`.
3. **Transparent Worker Tool Distribution**: Tools discovered from remote MCP servers are injected directly into worker toolsets across both runtime modes:
   - **Embedded Go Mode** (`embedded_session.go` / `agent.AgentTool`).
   - **Managed Pi RPC Mode** (`mecha-rpc` / `mecha-install-tools-v1` manifest injection).
4. **Zero Worker Awareness**: **"The tools should look no different to the workers than any other tools."** Tool names, JSON parameter schemas, execution flows, and error contracts appear identical to native built-in tools (e.g., `read`, `bash`, `scratchpad`). Workers invoke them via standard structured tool calls without CLI syntax, manual token handling, or separate subprocess plumbing.

---

## 2. Dev-Loop Phase 1: Observable Behavior

### 2.1 Router-Level Configuration

MCP servers are configured globally at the router level. Configurations are durable and shared across all sessions and workers.

#### Attributes of an MCP Server:
- `id`: Unique identifier (UUIDv4 or slug, e.g., `podclave-prod`).
- `name`: Human-readable label (e.g., `Podclave Enterprise Tools`).
- `url`: Base endpoint URL of the remote MCP server (e.g., `https://podclave.com/mcp`).
- `auth_mode`: Authorization strategy:
  - `none`: Public server requiring no authorization.
  - `bearer`: Static API key / Bearer token configured by the operator.
  - `oauth`: Dynamic OAuth 2.0 / 2.1 with PKCE (`S256`).
- `tool_prefix`: Optional namespace prefix to prevent collisions (default: derived from slug or empty; e.g. `podclave_`).
- `enabled`: Boolean toggle indicating whether the server's tools are distributed to workers (default: `true`).
- `timeout_ms`: Request timeout for tool execution (default: `30000`, 30 seconds).

---

### 2.2 Automated OAuth 2.0/2.1 Discovery & PKCE Flow

When an MCP server is configured with `auth_mode: "oauth"`, the router acts as an OAuth confidential/public client utilizing RFC 7636 (PKCE with `S256` code challenge).

```
+----------------------------------------------------------------------------------------------------+
|                                    Router OAuth PKCE Lifecycle                                     |
+----------------------------------------------------------------------------------------------------+

1. Discovery:
   Router -----------------> GET /mcp/.well-known/oauth-protected-resource -----------------> MCP Server
   Router <----------------- { "authorization_servers": ["https://auth.podclave.com"] } <--- MCP Server
   Router -----------------> GET https://auth.podclave.com/.well-known/oauth-authorization-server ---> AS
   Router <----------------- { authorization_endpoint, token_endpoint, registration_endpoint } <----- AS

2. Dynamic Client Registration (RFC 7591 - if required):
   Router -----------------> POST registration_endpoint { redirect_uris, client_name } -------------> AS
   Router <----------------- { client_id, client_secret (optional) } <------------------------------- AS

3. Authorization Grant:
   Operator Browser -------> GET /v1/settings/mcp-servers/{id}/auth/start --------------------> Router
   Router -----------------> 302 Redirect to AS /authorize?response_type=code&code_challenge=... ----> AS
   Operator logs in & consents at AS
   AS ---------------------> 302 Redirect to Router /v1/mcp/oauth/callback?code=XYZ&state=ABC --------> Router

4. Token Exchange:
   Router -----------------> POST token_endpoint (code + code_verifier + client_id) ---------> AS
   Router <----------------- { access_token, refresh_token, expires_in, scope } <------------ AS
   Router writes encrypted tokens to secure SQLite store.
```

#### Detailed OAuth Protocol Steps:
1. **Protected Resource Discovery**:
   - Router performs an HTTP `GET` to `<mcp_url>/.well-known/oauth-protected-resource/mcp` or inspects `WWW-Authenticate` response headers on an initial unauthorized `POST /mcp` handshake.
   - Discovers issuer URL and authorization servers (e.g., `https://auth.podclave.com`).
2. **Authorization Server Discovery**:
   - Router requests `<issuer>/.well-known/oauth-authorization-server` or `<issuer>/.well-known/openid-configuration`.
   - Extracts:
     - `authorization_endpoint`
     - `token_endpoint`
     - `registration_endpoint` (optional)
     - `revocation_endpoint` (optional)
     - Supported code challenge methods (verifies `S256` support).
3. **Dynamic Client Registration (RFC 7591)**:
   - If no pre-configured `client_id` exists for the domain, router checks for `registration_endpoint`.
   - Router registers itself using redirect URI: `http://127.0.0.1:<listen_port>/v1/mcp/oauth/callback` (or configured public router origin).
   - Stores assigned `client_id` and optional `client_secret_expires_at`.
4. **PKCE Flow Initiation (`/v1/settings/mcp-servers/{id}/auth/start`)**:
   - Router generates high-entropy cryptographic `code_verifier` (43-128 chars base64url).
   - Computes `code_challenge = BASE64URL-ENCODE(SHA256(ASCII(code_verifier)))` with `code_challenge_method = S256`.
   - Generates random `state` nonce and stores pending transaction in memory/SQLite with 10-minute expiration.
   - Responds with HTTP `302 Found` or JSON redirect payload directing operator's browser to `authorization_endpoint`.
5. **Callback & Token Exchange (`/v1/mcp/oauth/callback`)**:
   - Authorization server redirects back with `?code=<code>&state=<state>`.
   - Router validates `state`, matches pending transaction, retrieves `code_verifier`.
   - Router executes back-channel `POST` to `token_endpoint` passing:
     - `grant_type: "authorization_code"`
     - `code: <code>`
     - `redirect_uri: "<callback_url>"`
     - `client_id: "<client_id>"`
     - `code_verifier: "<code_verifier>"`
   - Receives tokens: `access_token`, `refresh_token` (optional), `expires_in` (seconds), `scope`.
   - Tokens are committed to SQLite with AES-256-GCM encryption at rest.
6. **Token Refresh & Revocation**:
   - Automated serialized proactive refresh: When token remaining lifetime $< 60$ seconds, router uses `refresh_token` to fetch a new `access_token` before executing tool requests.
   - If refresh returns `invalid_grant` or refresh token expired, server status transitions to `needs_auth`.
   - Explicit revocation: Calling `/v1/settings/mcp-servers/{id}/auth/revoke` sends RFC 7009 revocation request to AS and clears credentials.

---

### 2.3 Tool Discovery, Schema Mapping & Caching

The router communicates with the MCP server using Streamable HTTP transport (MCP Specification 2024-11-05).

1. **Protocol Handshake (`initialize`)**:
   - Router sends `POST <mcp_url>` with JSON-RPC payload:
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
         "clientInfo": {
           "name": "mecha-router",
           "version": "1.0.0"
         }
       }
     }
     ```
   - Server returns protocol version, server info, and server capabilities.
   - Router completes handshake with `notifications/initialized`.

2. **Tool Listing (`tools/list`)**:
   - Router issues JSON-RPC call `tools/list`:
     ```json
     {
       "jsonrpc": "2.0",
       "id": 2,
       "method": "tools/list",
       "params": {}
     }
     ```
   - Server returns list of tools:
     ```json
     {
       "tools": [
         {
           "name": "create_issue",
           "description": "Create a new issue in Podclave tracker",
           "inputSchema": {
             "type": "object",
             "properties": {
               "title": { "type": "string" },
               "priority": { "type": "string", "enum": ["low", "high", "urgent"] }
             },
             "required": ["title"]
           }
         }
       ]
     }
     ```
   - If paginated (`nextCursor`), router iterates until full list is retrieved.

3. **Tool Definition Caching & Refresh**:
   - Discovered tools are cached in SQLite (`managed_mcp_tools` table) and kept in memory.
   - **Periodic Sync**: Background task re-syncs tool definitions every 15 minutes.
   - **Manual Sync**: Operator or worker can trigger immediate discovery via `POST /v1/settings/mcp-servers/{id}/sync`.
   - **Notification Sync**: If server supports SSE notifications on Streamable HTTP, router listens for `notifications/tools/list_changed` and updates cache immediately.

---

### 2.4 Tool Visibility to Workers ("No Different Than Any Other Tools")

A core architectural invariant:
> **"The tools should look no different to the workers than any other tools."**

Workers must not be required to know that a tool is serviced via MCP, nor should they execute shell commands or manage tokens.

#### 2.4.1 Naming and Collision Resolution
- Remote tool names are exposed using standard snake_case identifiers.
- If `tool_prefix` is configured (or if a collision with a native router tool occurs), the tool is prefixed: e.g., `podclave_create_issue`.
- Collision Guard: If a remote MCP tool name matches an existing built-in router tool (such as `read`, `bash`, `scratchpad`, `goal`, `mecha_worker`), the router automatically prepends `<server_slug>_` to the remote tool name and logs an info notice.

#### 2.4.2 Embedded Go Session Mode (`embedded_session.go`)
- In `embedded_session.go`, the session initializes its toolset for root and worker lanes:
  ```go
  // Native tools: mecha_mailbox_send, scratchpad, bash, read, etc.
  tools = append(tools, m.embeddedScratchpadTool(owner))
  ...
  // Dynamic injection of active Remote MCP tools:
  mcpTools := m.mcpManager.ActiveAgentTools(owner)
  tools = append(tools, mcpTools...)
  ```
- Each remote MCP tool is represented as an `agent.AgentTool`:
  - `Name`: e.g., `podclave_create_issue`
  - `Description`: String description from remote server
  - `Parameters`: Translated JSON Schema (`ai.Schema`) passed directly to LLM provider
  - `Execute`: Go function executing the RPC call through router's centralized transport.

#### 2.4.3 Managed Pi RPC Mode (`process.go` & `mecha-rpc`)
- For stock Pi processes running with `pi-extensions/mecha-rpc`:
  - Router compiles the active MCP tools into the desired manifest sent via `mecha-bootstrap-v1` and `mecha-install-tools-v1`.
  - Manifest tool entries map capability IDs to remote tool handlers.
  - When Pi's LLM calls the tool, `tool-registry.ts` dispatches `mecha-tool-call` back across the RPC bridge to Go router.
  - The router routes the execution through the MCP client and returns standard tool outputs.

#### 2.4.4 Execution Flow, Formatting & Timeouts
1. **Model Generates Tool Call**: LLM emits structured tool call:
   ```json
   {
     "name": "create_issue",
     "arguments": {
       "title": "Fix router memory leak",
       "priority": "high"
     }
   }
   ```
2. **Router Pre-Execution Validation**:
   - Validates arguments against cached `inputSchema`.
   - Verifies MCP server auth status is `connected`. If auth is expired, initiates proactive refresh.
3. **Streamable HTTP Dispatch**:
   - Router sends `POST <mcp_url>` with method `tools/call`:
     ```json
     {
       "jsonrpc": "2.0",
       "id": "call-12345",
       "method": "tools/call",
       "params": {
         "name": "create_issue",
         "arguments": {
           "title": "Fix router memory leak",
           "priority": "high"
         }
       }
     }
     ```
4. **Response Formatting**:
   - Remote MCP returns standard MCP content items (`text`, `image`, `resource`):
     ```json
     {
       "content": [
         { "type": "text", "text": "Issue POD-402 created successfully." }
       ],
       "isError": false
     }
     ```
   - Router converts content directly into `agent.AgentToolResult`:
     `AgentToolResult{ Content: ai.ContentList{ ai.TextContent{ Text: "..." } } }`
5. **Execution Timeouts**:
   - Router wraps remote HTTP execution in a context with `timeout_ms` (default 30s).
   - If timeout expires, HTTP request is cancelled via context cancellation, and a clean error message is returned to LLM:
     `Tool "create_issue" timed out after 30000ms. No changes were confirmed.`

---

### 2.5 REST API Endpoints & WebSocket Contracts

The router exposes management endpoints adhering to the existing `/v1/settings/*` convention established by System 1 and Default Model settings.

| Method | Path | Description |
|---|---|---|
| `GET` | `/v1/settings/mcp-servers` | List all configured remote MCP servers, their connection status, and tool counts. |
| `POST` | `/v1/settings/mcp-servers` | Register a new remote MCP server (URL, name, auth mode, tool prefix). |
| `GET` | `/v1/settings/mcp-servers/{id}` | Get detailed configuration, auth state, and cached tool definitions for a server. |
| `PUT` | `/v1/settings/mcp-servers/{id}` | Update server configuration (name, enabled, tool prefix, timeout, bearer token). |
| `DELETE`| `/v1/settings/mcp-servers/{id}` | Unregister an MCP server and remove its tools from worker toolsets. |
| `POST` | `/v1/settings/mcp-servers/{id}/sync` | Force an immediate discovery and tool listing refresh. |
| `POST` | `/v1/settings/mcp-servers/{id}/auth/start` | Initiate OAuth PKCE authorization flow; returns redirect authorization URL. |
| `GET` | `/v1/mcp/oauth/callback` | OAuth redirect callback receiving `code` and `state`, performing token exchange. |
| `POST` | `/v1/settings/mcp-servers/{id}/auth/revoke` | Revoke stored tokens and reset server auth state to `needs_auth`. |

#### Live WebSocket Event Contract
The router broadcasts live deltas on `/v1/agents` WebSocket for UI synchronization:
- `mcp_server_updated`: Emitted when server connectivity, status, or tool listing changes.
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

### 2.6 Edge Cases & Failure Containment

1. **Server Unreachable (502 / 503 / Network Timeout)**:
   - When a worker invokes a tool on an unreachable remote server, the router intercepts the failure.
   - **Crucial Invariant**: The router and worker session **never crash**.
   - Router returns a structured tool error result to the LLM:
     ```
     Error executing remote tool "podclave_create_issue": MCP server unreachable (HTTP 503 Service Unavailable). Please retry later.
     ```
   - LLM receives this in its conversation turn as normal tool error feedback and can reason over alternatives.
2. **Expired OAuth Token & Refresh Failure**:
   - If an API call fails with HTTP 401 Unauthorized, the router marks cached token dirty and performs an immediate serialized refresh.
   - If refresh succeeds, the tool call is replayed once transparently without the worker knowing.
   - If refresh fails (e.g. refresh token revoked or expired):
     - Server status moves to `needs_auth`.
     - Router returns clean error to LLM:
       ```
       Remote tool "podclave_create_issue" requires re-authentication. Administrator must re-authorize the server in Router Settings.
       ```
     - Operator is notified via UI indicator and optional Mailbox alert.
3. **Duplicate Server URLs & Name Collisions**:
   - Registering a server with an existing URL updates the existing server or returns HTTP 409 Conflict.
   - When multiple MCP servers offer identical tool names (e.g. `query_database` from Server A and Server B), the router enforces namespace disambiguation:
     `serverA_query_database` and `serverB_query_database`.
4. **Malformed Tool Output from Remote Server**:
   - If the remote server returns non-JSON-RPC compliant data or invalid UTF-8, the router sanitizes the response, truncates to safe memory limits (max 512 KiB per result), and returns the sanitized text or an informative parsing error.

---

## 3. High-Level Design & Component Architecture

### 3.1 System Boundaries

```
+-------------------------------------------------------------------------------------------------------------------------+
|                                                      ROUTER PROCESS                                                     |
|                                                                                                                         |
|  +--------------------+        +-----------------------------+        +----------------------------------------------+  |
|  |     Transport      |        |          Storage            |        |                  MCP Manager                 |  |
|  |  (managed_http.go, |        |     (SQLite Store / DB)     |        |              (internal/mcp/)                 |  |
|  |   managed_ws.go)   |        |                             |        |                                              |  |
|  +---------+----------+        |  - managed_mcp_servers      |        |  - OAuth Discovery & PKCE Coordinator        |  |
|            |                   |  - managed_mcp_tokens (enc) |        |  - Streamable HTTP Client Pool               |  |
|            | REST / WS         |  - managed_mcp_tools        |        |  - Dynamic Schema Validator & Registry       |  |
|            v                   +--------------+--------------+        +----------------------+-----------------------+  |
|  +--------------------+                       ^                                              |                          |
|  |     Web UI         |                       | Read/Write                                   | Inject Tools             |
|  | (Settings Dialog)  |                       +----------------------------------------------+                          |
|  +--------------------+                                                                      |                          |
|                                                                                              |                          |
|                        +---------------------------------------------------------------------+                          |
|                        |                                             |                                                  |
|                        v                                             v                                                  |
|         +-----------------------------+               +-------------------------------+                                 |
|         |     Embedded Worker Lane    |               |       Managed Pi Worker       |                                 |
|         |    (embedded_session.go)    |               |     (mecha-rpc / manifest)    |                                 |
|         |                             |               |                               |                                 |
|         | Directly invokes Go wrapper |               | Tool manifest synced via RPC; |                                 |
|         | `agent.AgentTool.Execute`   |               | Dispatches `mecha-tool-call`  |                                 |
|         +-----------------------------+               +-------------------------------+                                 |
+-------------------------------------------------------------------------------------------------------------------------+
                                         |
                                         | Streamable HTTP POST /mcp (Bearer Token / OAuth)
                                         v
                      +-------------------------------------+
                      |       Remote MCP Servers            |
                      | (e.g. https://podclave.com/mcp)     |
                      +-------------------------------------+
```

### 3.2 Database Schema (`router-proxy/internal/store/migrations/105_mcp_servers.sql`)

```sql
-- Migration 105: Remote MCP Servers, OAuth Tokens, and Tool Registry

CREATE TABLE managed_mcp_servers (
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

CREATE TABLE managed_mcp_tokens (
    server_id TEXT PRIMARY KEY REFERENCES managed_mcp_servers(id) ON DELETE CASCADE,
    access_token_enc TEXT NOT NULL,
    refresh_token_enc TEXT,
    token_type TEXT NOT NULL DEFAULT 'Bearer',
    expires_at TEXT,
    scope TEXT,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE managed_mcp_tools (
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

CREATE INDEX idx_managed_mcp_tools_server ON managed_mcp_tools(server_id);
```

### 3.3 Component Responsibilities

1. **`internal/mcp/manager.go`**:
   - Central coordinator. Manages lifecycle of remote MCP connections, periodic sync timers, and tool catalog.
   - Provides thread-safe lookup of active tools for worker session initialization.
2. **`internal/mcp/oauth.go`**:
   - Handles RFC 7636 PKCE code challenge generation and validation.
   - Performs `.well-known` discovery and dynamic client registration (RFC 7591).
   - Executes token exchange and serialized automatic refresh with mutex locks per server.
   - Interfaces with token encryption helper (AES-256-GCM using router master key).
3. **`internal/mcp/client.go`**:
   - Implements JSON-RPC 2.0 over Streamable HTTP (`POST`).
   - Reuses persistent HTTP connection pool (`http.Client` with Keep-Alive).
   - Marshals `initialize`, `tools/list`, and `tools/call`.
4. **`internal/transport/mcp_http.go`**:
   - Exposes REST routes (`/v1/settings/mcp-servers/*`).
   - Implements OAuth redirect callback receiver (`/v1/mcp/oauth/callback`).
5. **Worker Integration Bridges**:
   - `internal/managed/embedded_session.go`: Dynamically pulls `m.mcpManager.ActiveAgentTools()` and appends them to session tool allowlist.
   - `internal/managed/supervisor.go`: Dynamically merges MCP tool definitions into the active Pi manifest during bootstrap.
6. **Web UI (`MCPServersDialog.tsx`)**:
   - Provides interface in the Router UI sidebar menu (alongside "Provider settings" and "System 1 Config").
   - Displays registered servers, real-time connectivity status, tool counts, and action buttons ("Authorize", "Sync Now", "Edit", "Remove").

---

## 4. Analysis of Alternative Approaches

### 4.1 Alternative A: Per-Worker MCP CLI Wrapper Subprocesses
- **Concept**: A CLI wrapper (e.g. `podclave-mcp-cli`) is compiled and placed in the PATH. When the worker needs to use external tools, it runs shell commands via `bash` (e.g. `podclave-mcp-cli create-issue --title "..."`).
- **Why Rejected**:
  1. **Violates Core Requirement**: Fails the foundational requirement that *"tools should look no different to the workers than any other tools"*.
  2. **Poor LLM Ergonomics**: Forces the LLM to write bash commands, escape arguments, handle terminal output, and guess CLI flags rather than utilizing native structured JSON function calling.
  3. **Fragmented Security & Auth**: Each worker instance would need local file-based token storage or environment variables, breaking centralized token revocation and rotation.
  4. **High Resource Overhead**: Spawns an external CLI process for every tool call.

### 4.2 Alternative B: Local Node.js Stdio MCP Proxy Subprocess Per Session
- **Concept**: The router or launcher spawns a local Node.js stdio MCP client process for each agent session, communicating via standard in/out.
- **Why Rejected**:
  1. **Heavy Resource Footprint**: Running Node.js runtimes per agent session consumes hundreds of megabytes of RAM and introduces process supervisory churn.
  2. **Redundant Auth & State Fragmentation**: Multiple workers interacting with the same backend (e.g., Jira) would negotiate redundant OAuth sessions or duplicate access tokens across disparate disk locations.
  3. **Architectural Inconsistency**: Mecha's core architecture centralizes infrastructure-level capabilities (Model Providers, System 1 Clef, Git Worktrees, SQLite Store) inside the Go router. Dispersing MCP into per-session Node.js processes breaks this clean model.

### 4.3 Alternative C (Chosen): Router-Level Centralized MCP Manager in Go
- **Concept**: Centralized Go-native MCP client residing within `router-proxy`. Implements Streamable HTTP JSON-RPC 2.0, manages persistent SQLite token encryption and OAuth PKCE flows, maintains a consolidated tool cache, and injects tools directly into Go and Pi RPC worker runtimes.
- **Why Chosen**:
  1. **Perfect Worker Parity**: Remote tools look 100% indistinguishable from built-in Go tools (`agent.AgentTool`) and manifest tools. The LLM receives standard JSON schemas and returns structured arguments.
  2. **Single Secure Auth Boundary**: All OAuth grants, refresh tokens, and encryption happen centrally inside the router. Revocation or token expiration affects the router once, not $N$ separate agents.
  3. **Performance & Lightweight Operations**: Go's native HTTP connection pooling communicates with remote servers with sub-millisecond overhead. No subprocesses or Node runtimes required.
  4. **Seamless UI Integration**: Sits naturally alongside existing provider settings, System 1 config, and default model settings in the Router Web UI.

---

## 5. Verification & Test Plan (Dev-Loop Implementation Gates)

The implementation will be verified through comprehensive dev-loop test gates:

### Gate 1: OAuth PKCE & Discovery Validation
- Mock OAuth 2.0 Authorization Server (`httptest.Server`) providing `.well-known` discovery documents.
- Verify PKCE `code_challenge` calculation and `code_verifier` validation.
- Test RFC 7591 dynamic client registration.
- Verify token exchange, AES-256-GCM encryption in SQLite, proactive token refresh upon expiration, and clean revocation.

### Gate 2: Streamable HTTP & Tool Schema Mapping
- Mock Remote MCP Server providing `initialize`, `tools/list`, and `tools/call`.
- Verify protocol handshake, JSON Schema conversion to `ai.Schema`, and handling of complex object/array parameters.
- Verify periodic and manual tool synchronization.

### Gate 3: Worker Tool Execution & Parity Proof
- **Embedded Go Session Test**: Spin up an embedded worker session; assert that remote MCP tools appear in `session.Tools`, invoke the tool via mock LLM tool call, and verify clean result return.
- **Pi RPC Bridge Test**: Verify tools are included in `mecha.bootstrap/v1` manifest; simulate `mecha-tool-call` and verify execution.
- **Failure Resilience**: Simulate 502/503/timeout on remote MCP server; prove that neither the router nor the worker crashes, and that a structured error is returned to the model.

### Gate 4: Web UI & REST Endpoints
- Automated HTTP tests covering `/v1/settings/mcp-servers/*` routes.
- Vitest React component tests for `MCPServersDialog.tsx` verifying add server, authorize, sync, and status display.
