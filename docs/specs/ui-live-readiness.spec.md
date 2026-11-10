# Spec: UI-live readiness

- **ID**: `SPEC-AGENT-UI-LIVE-READINESS-001`
- **Status**: `Draft`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/ui-live-readiness-deadline.spec.ts`

## Intent & User Outcome

Live managed-agent updates recover automatically if a connection opens but never becomes ready.

## Core Invariants

1. A connection is ready only after its first valid managed-agent snapshot.
2. The readiness deadline begins when connecting and remains active after the WebSocket opens. Other frames do not satisfy it.
3. A missed deadline reports a connection failure, resets the projection, closes the socket, and schedules a retry. A timely snapshot cancels the deadline and resets retry backoff.
4. Closing the client cancels pending deadlines and retries; stale socket events cannot affect a replacement connection.
5. Readiness is announced only while the snapshot's socket remains active after snapshot delivery and the connection callback; synchronous closure or replacement suppresses any remaining readiness notification.
6. Each newly allocated socket starts with no accepted snapshots or projection versions, including when it replaces a closing socket during snapshot delivery. Late or synchronous cleanup of the old socket cannot cancel the replacement deadline or reset its state.
7. If a frame callback closes or replaces its socket, remaining subscribers must not receive that socket's frame after the change.

## User Scenarios (Behavioral Contract)

### Scenario: [SCN-UI-LIVE-READY-001] Snapshot arrives in time
- **User Goal**: Receive live agent updates without unnecessary reconnection.
- **Given**: A live connection is opening.
- **When**: A valid agent snapshot arrives before the readiness deadline.
- **Then**: The snapshot is applied, the connection is announced ready once, and the deadline does not disconnect it.

### Scenario: [SCN-UI-LIVE-READY-002] Open connection stalls
- **User Goal**: Recover from a connection that never starts delivering agent state.
- **Given**: A live connection opens but no valid agent snapshot arrives.
- **When**: The readiness deadline expires.
- **Then**: The connection failure is reported, recovery runs, and a replacement connection is attempted. A valid snapshot on that replacement restores readiness.

### Scenario: [SCN-UI-LIVE-READY-003] Replacement during snapshot delivery
- **User Goal**: Recover and receive live state when a closing connection is replaced during snapshot delivery.
- **Given**: A snapshot callback starts closing its socket and requests a replacement before the old close event.
- **When**: The replacement stalls or receives a valid snapshot with a lower projection version.
- **Then**: A stalled replacement times out and retries; a valid snapshot is accepted and announces readiness. Old socket cleanup does not interfere, and subscribers not yet called do not receive the old snapshot after replacement.

## Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `SCN-UI-LIVE-READY-001` | Timely snapshot | `tests/e2e/ui-live-readiness-deadline.spec.ts` | `timely snapshot stays connected past readiness deadline` (passed); deterministic transport coverage in `web/router-ui-react/tests/transport/ui-live.test.ts` |
| `SCN-UI-LIVE-READY-002` | Open connection stalls and recovers | `tests/e2e/ui-live-readiness-deadline.spec.ts` | `open without snapshot reaches readiness deadline and reconnects` (passed): replacement snapshot and a real urgency delta translated on the wire; REST managed-agent reads and urgency notifications are suppressed during the delta assertion. The replacement remains connected beyond its deadline. Deterministic transport coverage in `web/router-ui-react/tests/transport/ui-live.test.ts` |
| `SCN-UI-LIVE-READY-003` | Replacement during snapshot delivery | `tests/e2e/ui-live-synchronous-replacement.spec.ts` | Independent transport-boundary run: 4 passes covering `onFrame` and subscriber replacement, each with lower-version and stalled replacement. Playwright runs the transport client in a browser against the real router through the proxy; intercepted WebSocket frames rewrite snapshot versions or suppress delivery. This is not a full UI test. Deterministic transport coverage also lives in `web/router-ui-react/tests/transport/ui-live.test.ts`. |
