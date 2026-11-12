# Spec: Managed agent views after a worker failure

- **ID**: `SPEC-MANAGED-PROJECTION-001`
- **Status**: `Active`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/managed-agent-projection-resilience.spec.ts`

## Intent & User Outcome

A failed agent must not prevent the user from seeing or using other agents. The agent list, individual agent views, and live updates continue to work while showing the failed agent's actual state.

## Core Invariants

1. An agent whose saved state is failed or stopped must not make the agent list, individual agent view, or live agent snapshots unavailable.
2. Background shell jobs are visible only under their current, verified owner. A failed or stale owner must not expose its old jobs or another owner's jobs. If an active owner's snapshot becomes stale during a read, the stale view is rejected instead of mixing ownership states.
3. An agent failure does not silently restart, delete, or change the state of that agent or other agents.
4. Unrelated store errors and request cancellation are not disguised as successful reads.

## User Scenarios (Behavioral Contract)

### Scenario: [SCN-MANAGED-PROJECTION-001] Healthy agents remain visible
- **User Goal**: See and select available agents while the router is running.
- **Given**: A healthy managed agent is running.
- **When**: The user opens the agent list and the live connection starts.
- **Then**: The agent appears in the list and live snapshot, and its individual view is available.

### Scenario: [SCN-MANAGED-PROJECTION-002] A background agent fails to start
- **User Goal**: Continue using healthy agents and inspect the failure without restarting the router.
- **Given**: A healthy agent is running and a background agent starts with a provider that returns an error.
- **When**: The background agent is recorded as failed and the user reads the agent list or opens an agent view.
- **Then**: The existing live connection receives the failed state. The list, both individual views, and a new live connection remain available. The failed agent is shown as failed without jobs from an unverified owner, and the healthy agent remains visible and usable.

### Scenario: [SCN-MANAGED-PROJECTION-003] A managed agent is stopped
- **User Goal**: Stop one agent without losing access to the others.
- **Given**: A healthy agent, a failed background agent, and another running agent are present.
- **When**: The user stops the other running agent.
- **Then**: Its live record is removed and its individual view is no longer found. The list and live snapshots still show the healthy and failed agents with their existing states.

## Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `SCN-MANAGED-PROJECTION-001` | Healthy list and live snapshot | `tests/e2e/managed-agent-projection-resilience.spec.ts` | `failed startup preserves agent projections and live updates` |
| `SCN-MANAGED-PROJECTION-002` | Failed background startup does not break agent views | `tests/e2e/managed-agent-projection-resilience.spec.ts` | `failed startup preserves agent projections and live updates` |
| `SCN-MANAGED-PROJECTION-003` | Stopping one agent preserves the other views | `tests/e2e/managed-agent-projection-resilience.spec.ts` | `failed startup preserves agent projections and live updates` |
