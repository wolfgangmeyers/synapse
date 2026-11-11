# Spec: UI polling and freshness

- **ID**: `SPEC-AGENT-POLLING-001`
- **Status**: `Draft`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/ui-polling-freshness.spec.ts`

## Intent & User Outcome

The worker list and its goal and child indicators stay current without a separate network request for each worker. Switching between main and background workers shows fresh information for the selected view without repeatedly fetching details for the hidden view.

## Core Invariants

1. Exactly one shared state request on a healthy startup supplies the agent list and the goal and child state needed to render it; the number of list-state requests does not grow with the number of agents. Opening a goal editor or child history may request details for that selection.
2. Only the currently visible main or background worker view causes view-specific detail fetching. A view switch refreshes the newly visible view and never applies a stale response to the wrong view.
3. Live updates, HTTP fallback, reconnect, and explicit refresh restore authoritative goal and child state without per-agent list fanout. Failed requests do not silently erase previously displayed state, and a later successful recovery restores freshness.
4. Child activity previews and selected history remain available and refresh while open. Legacy discovery remains functional.

## User Scenarios (Behavioral Contract)

### Scenario: [POLL-001] Reload and healthy live updates
- **User Goal**: See current agents, goals, and children without repeated list fanout.
- **Given**: Multiple main and background workers have different goal and child states.
- **When**: The user reloads the UI and leaves the live connection healthy.
- **Then**: The visible list shows current state using a shared snapshot; no per-agent goal or children GETs are made for list rendering, including after live updates. Request counts do not scale with worker count.

### Scenario: [POLL-005] Embedded worker updates its own goal
- **User Goal**: See a worker's goal as soon as the worker sets it, even while it is still working.
- **Given**: An embedded worker is visible and the live connection is healthy.
- **When**: The worker sets its own goal using its goal tool and continues its turn.
- **Then**: The live projection announces the enabled goal and the worker's goal indicator appears without a per-worker goal request or a page refresh.

### Scenario: [POLL-002] Live connection unavailable or recovers
- **User Goal**: Keep the list fresh during an outage and after reconnection.
- **Given**: Live updates are unsupported, disconnected, or invalid.
- **When**: Goal or child state changes, and the router remains reachable or the live connection recovers.
- **Then**: Shared HTTP reconciliation refreshes the visible state without per-agent goal or children GETs. Recovery restores authoritative state and history. A failed reconciliation preserves prior state until a successful one.

### Scenario: [POLL-003] Child activity
- **User Goal**: Inspect current activity without continuous rapid fanout across children.
- **Given**: The child viewer is open.
- **When**: Activity changes or a child is selected.
- **Then**: List previews refresh periodically and the selected child's history refreshes promptly.

### Scenario: [POLL-004] Switch worker views
- **User Goal**: See current main or background workers without loading hidden worker details.
- **Given**: Both worker categories exist and their state changes while one category is hidden.
- **When**: The user switches between the main and background tabs, including while a previous request is in flight.
- **Then**: Only the active view requests its view-specific details; the newly visible view becomes fresh, and late responses cannot replace it with the other view's data.

## Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `POLL-001` | Live updates and bounded polling | `tests/e2e/ui-polling-freshness.spec.ts` | `live projections avoid repeated fanout and recover after disconnect` |
| `POLL-002` | HTTP fallback and recovery | `tests/e2e/ui-polling-freshness.spec.ts` | `live projections avoid repeated fanout and recover after disconnect` |
| `POLL-003` | Child viewer freshness | `tests/e2e/ui-polling-freshness.spec.ts` | `child viewer refreshes selected history without rapid list fanout` |
| `POLL-004` | Visible worker view and freshness | `tests/e2e/ui-polling-freshness.spec.ts` | `switching worker views fetches only visible details and stays fresh` |
| `POLL-005` | Embedded self-goal live projection | `tests/e2e/ui-polling-freshness.spec.ts` | `embedded self-goal is projected live while the turn continues` |
