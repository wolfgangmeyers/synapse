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
