# Behavioral Specification: Codex Search (`codex_search`)

## Status
Active / Proposed

## Overview
The `codex_search` tool allows agents in `mecha-developer` embedded router sessions to execute real-time web research grounded by OpenAI Codex Responses API. It uses the user's existing OpenAI / Codex OAuth login tokens (resolved from `~/.pi/agent/auth.json`, `~/.codex/auth.json`, or the router credential store).

Instead of reinventing OAuth token refresh or JWT decoding, `codex_search` reuses the established implementation in `router-proxy/internal/codexdevice/refresh.go` (`codexdevice.Refresh`).

Like `google_search`, `codex_search` returns synthesized answers, cited source titles and URLs, and executed search queries.

---

## User Scenarios & Expected Behaviors

### Scenario 1: Standard Real-Time Web Query & Session Invariants (Happy Path)
- **Given** valid OpenAI Codex OAuth credentials and an active embedded session.
- **When** an agent calls `codex_search` with `query: "latest Go 1.25 release notes"`.
- **Then**:
  1. **Owner TTL Renewal Invariant**: The tool calls `m.store.RenewEmbeddedOwner(callCtx, owner.binding, owner.identity, embeddedOwnerTTL)` before execution; failure halts the call immediately.
  2. **Role Check Invariant**: The tool checks `m.store.EmbeddedSessionRole(callCtx, owner.binding)`. Execution is permitted only for roles `"root"` or `"worker"`; any other role returns an error (`"codex_search requires embedded root or worker"`), matching `google_search`.
  3. **Input Validation**: The tool ensures `query` is a non-empty string.
  4. **Backend Request**: Authenticates against `https://chatgpt.com/backend-api/codex/responses` (or configured base URL) using Bearer access token and `chatgpt-account-id` header.
  5. Submits a Responses API request with `tools: [{"type": "web_search"}]`, `store: false`, and `include: ["web_search_call.results"]`.
  6. The response SSE stream is parsed for synthesized text, web citations (`sources`), and search queries.
  7. Returns Markdown text containing the synthesized answer, a `### Sources` section with `[Title](URL)` links, and `*Search queries: ...*`.

### Scenario 2: Directives and Context URLs
- **Given** valid credentials and authorized embedded session.
- **When** the caller provides optional `instruction: "focus on compiler optimizations"` or `urls: ["https://go.dev/doc/go1.25"]`.
- **Then**:
  1. The prompt or instructions incorporate the user's specific directive.
  2. Specific target URLs are provided to guide web search extraction.
  3. Synthesized output emphasizes requested focus areas.

### Scenario 3: Token Expiration and Automatic Refresh via `codexdevice.Refresh`
- **Given** the access token has expired (`expires` timestamp in the past or API returns HTTP 401).
- **When** `codex_search` is called.
- **Then**:
  1. The auth helper delegates token refresh and JWT claim decoding directly to `codexdevice.Refresh` (`router-proxy/internal/codexdevice/refresh.go`).
  2. It does not reinvent OAuth refresh requests or JWT parsing.
  3. Upon receiving the refreshed `store.CodexCredential` (with renewed access token, refresh token, expiry, and parsed `chatgpt_account_id`), credentials are persisted safely.
  4. The search request proceeds without failing the user's call.

### Scenario 4: Missing Credentials (Graceful Failure)
- **Given** neither auth files nor the credential store contains valid OpenAI or OpenAI Codex tokens.
- **When** `codex_search` is executed.
- **Then**:
  1. The tool returns an actionable, clear error message: `"No OpenAI/Codex credentials found. Run /login openai-codex or authenticate with Codex CLI."`.
  2. It does not panic or crash the embedded session or router.

### Scenario 5: Tool Registration, Migration & Native Tools Inventory
- **Given** `mecha-developer` router proxy starts or embedded sessions initialize.
- **When** inspecting available tools.
- **Then**:
  1. **Database Migration**: `router-proxy/internal/store/migrations/118_codex_search_tool.sql` inserts `codex_search` into `managed_tool_defaults` with category `'Knowledge Base & Search'` and `enabled = 1`.
  2. `codex_search` is registered in `router-proxy/internal/store/native_tools.go` and `router-proxy/internal/store/workflow_tools.go` under `"Knowledge Base & Search"`.
  3. In `router-proxy/internal/managed/embedded_session.go`, `codex_search` is exposed conditionally when `m.hasCodexSearchAuth(ctx)` is true, matching `m.hasGoogleSearchAuth(ctx)`.
  4. Auth helper logic lives in `router-proxy/internal/managed/embedded_codex_search_auth.go` (or leverages `embedded_codex_auth.go`) to prevent naming collisions.
  5. Pi CLI extension registration is out of scope; only embedded router session integration is covered.

### Scenario 6: Tool Availability Testing (Credentialed vs. Uncredentialed)
- **Given** embedded session test scenarios in `router-proxy/internal/managed/embedded_session_test.go`.
- **When** testing session initialization across credential states.
- **Then**:
  1. **Credentialed**: When Codex credentials exist (`hasCodexSearchAuth` is true), `codex_search` is present in the session's active tool list alongside `google_search`.
  2. **Uncredentialed**: When Codex credentials are absent (`hasCodexSearchAuth` is false), `codex_search` is omitted from the toolset without errors or launch failures.
