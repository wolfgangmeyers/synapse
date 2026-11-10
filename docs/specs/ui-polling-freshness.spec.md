# Spec: UI polling and freshness

- **ID**: `SPEC-AGENT-POLLING-001`
- **Status**: `Draft`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/ui-polling-freshness.spec.ts`

## Intent & User Outcome

Agent and child lists stay current without repeatedly fetching every worker while live updates are healthy. Goal status and history remain fresh even where live events do not guarantee updates.

## Core Invariants

1. Reconnection and projection recovery restore authoritative agent and child state; disconnected or unsupported live connections continue HTTP reconciliation.
2. Connected legacy agent discovery has one periodic owner and still refreshes on registration and invalidation.
3. Runtime goal state, which is not guaranteed by the managed projection, is periodically refreshed even while live.
4. Child activity previews and selected history remain available and refresh while open.

## User Scenarios (Behavioral Contract)

### Scenario: [POLL-001] Healthy live updates
- **User Goal**: See current agents and children without repeated list fanout.
- **Given**: The browser has accepted live agent and child snapshots.
- **When**: The view remains open and agent or child state changes.
- **Then**: Live changes appear, without a three-second managed list and child HTTP fanout; goal status still refreshes periodically.

### Scenario: [POLL-002] Live connection unavailable or recovers
- **User Goal**: Keep the list fresh during an outage and after reconnection.
- **Given**: Live updates are unsupported, disconnected, or invalid.
- **When**: The router remains reachable or the live connection recovers.
- **Then**: HTTP reconciliation continues while live is unavailable, and reconnection restores authoritative state.

### Scenario: [POLL-003] Child activity
- **User Goal**: Inspect current activity without continuous rapid fanout across children.
- **Given**: The child viewer is open.
- **When**: Activity changes or a child is selected.
- **Then**: List previews refresh periodically and the selected child's history refreshes promptly.

## Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `POLL-001` | Live updates and bounded polling | `tests/e2e/ui-polling-freshness.spec.ts` | `live projections avoid repeated fanout and recover after disconnect` |
| `POLL-002` | HTTP fallback and recovery | `tests/e2e/ui-polling-freshness.spec.ts` | `live projections avoid repeated fanout and recover after disconnect` |
| `POLL-003` | Child viewer freshness | `tests/e2e/ui-polling-freshness.spec.ts` | `child viewer refreshes selected history without rapid list fanout` |
