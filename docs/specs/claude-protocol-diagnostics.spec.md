# Spec: Claude protocol diagnostics

- **ID**: `SPEC-CLAUDE-DIAGNOSTICS-001`
- **Status**: `Draft` (acceptance pending)
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/claude-protocol-diagnostics.spec.ts`

## Intent & User Outcome

Operators can distinguish an early result from a replay identity or content mismatch without exposing conversation content or changing delivery.

## Core Invariants

1. Diagnostic records contain only fixed-vocabulary event categories, bounded counts and booleans; no payload, identifiers, arbitrary protocol strings, paths, or secrets.
2. Each process retains at most eight recent event categories and offers at most one record per diagnostic category. A single router-wide worker and a 64-record queue isolate the protocol from a blocked sink; new records are dropped when full. No goroutine is created per event.
3. The router writes diagnostics only to a separate sibling file named `claude-protocol-<router-log-basename>` with its own writer and lock; its basename does not start with `<router-log-basename>.`, so neither it nor its rotations are treated as router-log generations. Initialization and writes occur only on the diagnostic worker and failures drop diagnostics. Its active generation is at most 16 MiB, retained rotations at most 16 MiB total; it rotates after 24 hours or at the size limit and cleans rotations older than 48 hours on writes. It never writes diagnostics to standard error or uses the delivery event writer.
4. Logging does not acknowledge inputs, reorder events, alter failure handling, or change settlement. A pending correlation may be reserved before its stdin write; the diagnostic never claims otherwise.

## User Scenarios

### Scenario: [CPD-001] Result with pending correlation
- **Given** a Claude input correlation is reserved, whether its stdin write has started or not.
- **When** a result arrives before its matching replay is processed.
- **Then** a `result_with_pending_input` diagnostic records recent protocol categories and pending count without claiming the input was written; after settlement a write-started unechoed mail remains queued and held, with no ACK or output.

### Scenario: [CPD-002] Replay mismatch
- **Given** a Claude input awaits its replay echo.
- **When** a replay has an unknown identity or different content.
- **Then** a bounded sanitized diagnostic distinguishes identity from content mismatch and the protocol fails closed without acknowledging that input.

### Scenario: [CPD-004] Diagnostic sink blocked
- **Given** the diagnostic sink is blocked and the bounded queue fills.
- **When** a protocol mismatch occurs.
- **Then** the diagnostic may be dropped, but failure handling and normal event-log writes proceed without waiting for the sink, and the input is not acknowledged.

### Scenario: [CPD-003] Normal replay
- **Given** a Claude input awaits its replay echo.
- **When** the matching echo precedes its result.
- **Then** no early-result or mismatch diagnostic is emitted and normal delivery continues.

## Automated E2E Verification Matrix

| Scenario | E2E test | Test case |
| --- | --- | --- |
| CPD-001 | `tests/e2e/claude-protocol-diagnostics.spec.ts` | `result before echo is sanitized and remains unacknowledged` |
| CPD-002 | `tests/e2e/claude-protocol-diagnostics.spec.ts` | `mismatched echo is sanitized and remains unacknowledged`; `content mismatch fails closed without exposing input` |
| CPD-003 | `tests/e2e/claude-protocol-diagnostics.spec.ts` | `matching echo has no failure diagnostic` |
| CPD-004 | `router-proxy/internal/managed/claude_protocol_diagnostics_test.go` | `TestClaudeDiagnosticBlockedSinkDoesNotBlockFailure` (fault injection; browser E2E sink blocking unavailable) |
