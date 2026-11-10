# Spec: Claude late steering echo

- **ID**: `SPEC-AGENT-CLAUDE-LATE-STEER`
- **Status**: Draft
- **Domain**: AgentManagement
- **Target E2E Suite**: `tests/e2e/claude-late-echo.spec.ts`, `tests/e2e/claude-steering-mid-turn.spec.ts`

## Intent & User Outcome
Steering sent during Claude output is answered whether Claude consumes it at a tool boundary or queues it for its next turn.

## Core Invariants
1. Only a matching checked input identity and content consumes and acknowledges a steer; uncertain writes are never blindly replayed.
2. A result answers only steers echoed before that result. The first result must not answer a steer echoed later.
3. After a result with unresolved writes, a 30-second deadline bounds the wait for the next checked steer echo. That echo suspends the deadline while its response is generated, even if other writes remain unresolved. A subsequent result starts a fresh deadline if unresolved writes remain. Ordinary response generation has no late-echo deadline. Failure leaves unechoed input unacknowledged and stops the process.

## User Scenarios

### Late echo
- **Given** an active Claude turn and a steer written while text is being generated.
- **When** Claude returns the first result before echoing the steer, then echoes it and returns a subsequent result.
- **Then** both answers are retained, the steer is acknowledged on its checked echo, and its prompt completes only after its result, without resubmission.

### Slow response after late echo
- **Given** the first result precedes a steered input echo.
- **When** all pending steers are checked before the late-echo deadline, but their response finishes after it.
- **Then** the response completes normally and the process remains available.

### Multiple sequential late steers
- **Given** two steers are written before the initial result and Claude processes them in separate responses.
- **When** the first echo arrives in time, its response runs longer than 30 seconds, and the second echo arrives promptly after that response result.
- **Then** both inputs are acknowledged once and both responses complete; no deadline runs during either response. If the second echo never arrives after the next result, the process stops and the second input remains unacknowledged.

### Immediate echo
- **Given** Claude echoes steering at a tool boundary during the active turn.
- **When** the turn returns its result.
- **Then** steering is acknowledged on echo and answered by that same result.

### Failure or missing echo
- **Given** a steer was written but not echoed by the first result.
- **When** the process exits, produces an invalid echo, errors, or does not produce the next unresolved steer echo within 30 seconds after a result.
- **Then** the process stops, unconfirmed input remains unacknowledged, and no automatic replay is attempted.

## Coverage
The target suites exercise the real router, SQLite and browser with a deterministic Claude CLI fixture. The timeout bounds unresolved echoes, not response generation after they are checked.
