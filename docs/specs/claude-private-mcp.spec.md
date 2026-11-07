# Spec: Claude private MCP transport

- **ID**: `SPEC-CLAUDE-PRIVATE-MCP-001`
- **Status**: `Draft`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/claude-code-tools.spec.ts` (integration pending)

## Intent & User Outcome

Claude workers use router tools without exposing their bearer grants through the shared router or its proxy.

## Core Invariants

1. The shared router does not serve the Claude MCP endpoint; the proxy remains transparent.
2. Claude receives the actual URL of a separate IPv4 loopback-only ephemeral listener. Existing grant and ownership checks still apply.
3. Listener startup or availability failure denies Claude tool access; it never falls back to the shared router. Listener failure and manager shutdown close active connections and revoke grants; no new grant or listener can be issued after closure. Shutdown does not wait for application handlers to finish.

## User Scenarios

### Scenario: Private tool access
- **Given** a managed Claude worker with a current grant,
- **When** it calls its configured private MCP URL,
- **Then** the checked tool boundary serves it; the shared router and proxy do not serve that endpoint.

### Scenario: Missing authority or stopped listener
- **Given** an invalid or revoked grant, a failed listener, or manager shutdown,
- **When** a worker attempts tool access,
- **Then** access fails closed, without fallback to the shared router.

## Automated E2E Verification Matrix

| Scenario | E2E suite | Test case |
| :--- | :--- | :--- |
| Private tool access | `tests/e2e/claude-code-tools.spec.ts` | private transport integration pending |
| Missing authority or stopped listener | `tests/e2e/claude-code-tools.spec.ts` | failure and shutdown integration pending |
| Live worker tool policy | `tests/e2e/claude-code-tools.spec.ts` | MCP discovery follows live worker tool policy |
| Cached catalog across turns | `tests/e2e/claude-code-tools.spec.ts` | cached catalog refreshes between turns without losing session |
| Stateless Claude handshake | `tests/e2e/claude-code-tools.spec.ts` | fake CLI handshake before checked calls; written, not yet run |

Supporting Go tests (not browser E2E): `claude_private_mcp_listener_test.go` covers listener lifecycle, idle connection closure and post-close grant denial; `claude_private_mcp_blocked_shutdown_test.go` adds a blocked checked callback with an already-performed simulated effect, grant cancellation and client connection closure. The new test is pending execution. It does not prove rollback of effects or real tool persistence. Concurrent issue/close has a probabilistic race test, not a deterministic lock-order test; no non-intrusive pre-lock barrier was identified. All coverage claims remain pending independent execution against the final source.

### Scenario: Stateless Claude handshake
- **Given** a current process grant,
- **When** Claude initializes using MCP 2025-03-26, sends initialized, lists tools and pings,
- **Then** it receives that protocol version, an empty 202 notification response and JSON results; no session is required.
- **When** it sends a valid JSON-RPC batch, including notifications or responses,
- **Then** only requests receive responses, and notification/response-only batches receive HTTP 202.
- **When** it sends an invalid batch entry or unsupported method,
- **Then** it receives a JSON-RPC error without executing an unauthorized tool.

This is a Claude-specific stateless JSON-response subset, not a claim of full MCP conformance: no SSE, server-initiated requests, resumability or sessions. Origin is neither parsed nor used for authorization; loopback and bearer grants are the authority boundary. The fake CLI handshake test is written but has not yet been run.

### Scenario: Tool policy changes during a Claude process
- **Given** a Claude worker with a private MCP grant and a worker or inherited workflow tool policy,
- **When** Claude requests `tools/list`, including after a policy change,
- **Then** the catalog reflects the current effective policy, using the same precedence as checked tool execution. A policy lookup failure returns an error instead of an unfiltered catalog.
- **When** a disabled tool is called from a cached catalog,
- **Then** checked execution still enforces current policy.
- A policy change does not interrupt an in-flight turn. Normal foreground execution closes its process and revokes its grant. The next execution starts a fresh process that discovers the current catalog and resumes the saved provider session, probing before submitting input. A missing saved session follows the existing safe fresh-session fallback. Policy lookup errors fail discovery closed.
- Adding, removing and re-adding tools across turns becomes visible even to a client that caches `tools/list` for its process lifetime. A cached disabled-tool call remains denied by current admission policy.

Discovery evaluates policy per tool; a concurrent policy edit can therefore produce a mixed catalog. Each call is independently checked against current policy, so discovery is not an authorization snapshot. An atomic catalog snapshot is not claimed by this change.

### Scenario: Concurrent tool queue admission
- **Given** a tool call occupies a process grant's serial lane,
- **When** another tool call waits for that lane,
- **Then** it is rejected after 30 seconds if not admitted, or sooner on request or grant cancellation; once admitted its execution retains its own timeout policy.

- Each grant serializes its own calls. The 30-second limit measures queue admission, not execution. An expired waiter returns JSON-RPC error `-32000`, `tool queue wait timed out`, and never invokes the checked tool, even if the lane becomes available at the same instant. It cannot leave the lane occupied.
- A request or grant cancelled while queued does not invoke the checked tool or retain the lane. When a response can still be delivered, cancellation returns JSON-RPC error `-32000`, `tool request cancelled`; a disconnected client need not receive it. Revocation can also prevent subsequent calls entirely.
- An admitted call may run longer than the queue limit under its existing execution context and timeout policy. Its result is subject to the usual post-execution grant and ownership checks. Timeout or cancellation is not a rollback guarantee for an already admitted effect.
- After a waiter times out, an otherwise valid grant can admit a later call once the lane is free.

Queue verification: `claude-mcp-queue-admission.spec.ts` runs a browser-launched router with a held external MCP call and a timed-out scratchpad contender. After release it confirms no write and retries the exact producer ID and arguments through the same grant, proving that checked authority can execute and persist once. This covers that controlled timeout ordering, not all cancellation or admission races. `TestInvestigationWitnessedQueueTimeout` independently checks the same authority property at the HTTP/checked-bridge boundary. `TestQueueHTTPTimeoutAndLongExecution` exercises HTTP and JSON-RPC with a controlled queue deadline and a simulated checked callback. `TestQueueRequestAndGrantCanceled` exercises both cancellation paths at the handler boundary. The latter handler tests do not themselves establish durable effects or browser-to-router E2E coverage.
