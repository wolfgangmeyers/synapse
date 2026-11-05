> **Archival status: historical implementation plan with acceptance gaps, not completed.** Current reference: [replacement specification](../../specs/codex-search.md). See the [plan-specific completion evidence, remaining gaps, and status mismatch](../../retirement-crosswalk.md#five-plan-dispositions), including [specific unmapped acceptance](../../retirement-crosswalk.md#specific-plan-acceptance-still-unmapped). Existing implementation/test files are not a passing acceptance run. No tests were run for this docs-only move. Original content follows, with relative Markdown links repaired. Implementation evidence: `router-proxy/internal/managed/embedded_codex_search.go`, `embedded_codex_search_test.go`, and `embedded_codex_search_auth_test.go` cover selected deterministic HTTP/tool/auth cases. Remaining gaps: credential precedence/refresh writeback, request/override/output and inventory parity, plus a qualifying model-tool E2E path. Replacement status is **Active / Proposed**, not accepted completion.

# Plan: Codex Search (`codex_search`) Implementation

## Goal
Implement a new OpenAI-powered search tool named `codex_search` in `mecha-developer` that leverages existing OpenAI/Codex login credentials (`~/.pi/agent/auth.json` or `~/.codex/auth.json`) and ChatGPT Codex backend Responses API to perform real-time web searches with source citations and query grounding.

## Requirements & Scope
1. **Credentials Management**:
   - Resolve OAuth access token and `chatgpt_account_id` from:
     - `~/.pi/agent/auth.json` (`openai-codex` or `openai` key)
     - Fallback to `~/.codex/auth.json`
   - Detect expired access tokens and perform OAuth refresh using `POST https://auth.openai.com/oauth/token` with client ID `app_EMoamEEZ73f0CkXaXp7hrann` and refresh token. Safely write back refreshed tokens.
2. **Backend API Execution**:
   - Call ChatGPT backend API at `https://chatgpt.com/backend-api/codex/responses` (or `OPENAI_CODEX_API_URL` override).
   - Use headers: `Authorization: Bearer <token>`, `chatgpt-account-id: <account_id>`, `OpenAI-Beta: responses=experimental`, `Content-Type: application/json`.
   - Send payload with:
     - `model`: `gpt-5.5` (or default fast codex model)
     - `tools`: `[{"type": "web_search"}]`
     - `store`: `false`
     - `include`: `["web_search_call.results"]`
     - `input`: User's search query along with optional instructions and URLs.
   - Stream or parse SSE events / JSON response to extract text synthesis, sources (`title`, `url`), and executed search queries.
   - Format into clean Markdown identical to `google_search` (`<synthesis>\n\n### Sources\n- [title](url)\n\n*Search queries: ...*`).
3. **Router Embedded Session Tool**:
   - Implement `router-proxy/internal/managed/embedded_codex_search.go` providing `embeddedCodexSearchTool(owner)` and `hasCodexSearchAuth(ctx)`.
   - Register `codex_search` in `embedded_session.go` alongside `google_search`.
   - Register tool in `router-proxy/internal/store/native_tools.go` and `workflow_tools.go`.
4. **Testing & Dev-Loop Verification**:
   - Unit and behavioral tests for token discovery, JWT extraction, OAuth refresh, SSE response parsing, error handling (401, 429, missing auth, empty query).
   - Mock HTTP server testing without external network calls.
   - Fast Playwright/E2E or Go integration test proving tool registration and invocation.

## Step-by-Step Implementation Tasks
1. **Behavioral Spec**:
   - `docs/specs/codex-search.md` created.
   - Update `docs/specs/INDEX.md` to reference `codex-search.md`.
2. **Auth & Token Refresh Module**:
   - `router-proxy/internal/managed/codex_auth.go`:
     - Reads Pi and Codex auth files.
     - Parses JWT payload to extract `chatgpt_account_id`.
     - Checks expiration and refreshes token if needed.
3. **Codex Search Execution Engine**:
   - `router-proxy/internal/managed/embedded_codex_search.go`:
     - Tool schema (`query` required, `instruction` optional, `urls` optional).
     - Wire request builder for ChatGPT Responses API.
     - SSE / response parser extracting message content, web search call actions, and citation URLs.
     - Tool registration in `embedded_session.go`.
4. **Store & Inventory Registration**:
   - Add `codex_search` to `native_tools.go` and `workflow_tools.go`.
   - Add SQLite migration if necessary for tool management catalog.
5. **Testing**:
   - Unit tests in `router-proxy/internal/managed/embedded_codex_search_test.go` with mock SSE stream responses.
   - Token refresh test with mock OAuth token endpoint.
   - Tool validation in embedded session.
6. **Plan Review & Dev-Loop Execution**:
   - Transition to `Plan Review` state.
