# Spec: Embedded agent relaunch safety

- **ID**: `SPEC-AGENT-STALE-RELAUNCH`
- **Status**: `Draft`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/embedded-stale-relaunch.spec.ts`

## Intent & User Outcome

A user can launch an embedded agent after a completed termination. If leftover session authority makes a relaunch unsafe, the user receives an actionable error rather than a misleading validation error.

## Core Invariants

1. A running or incompletely terminated agent cannot be replaced by a same-name launch.
2. A session without a managed agent is not itself proof that its process is stopped; do not erase or adopt it automatically.
3. A refused launch leaves the existing session authority and evidence unchanged and reserves no agent or operation. Independent retention sweeps may advance the session’s monotonic evidence-expiry cutoff and prune expired evidence.
4. A completed, ordinary termination permits a new launch once its old session is removed by normal lifecycle cleanup.

## User Scenarios

### Scenario: [STALE-001] Relaunch after normal termination
- **Given** a Claude agent has completed termination and its managed agent and session are gone.
- **When** the user launches the same name.
- **Then** the new agent is accepted with a fresh session.

### Scenario: [STALE-002] Orphaned current session
- **Given** an embedded current session remains but no managed agent exists for the same ID.
- **When** the user tries to launch the same name.
- **Then** the launch is refused with an explanation that stale session state needs investigation and a suggestion to choose another name. The old session authority remains unchanged; independent retention maintenance still applies.

### Scenario: [STALE-003] Existing agent
- **Given** an active managed agent with a current session.
- **When** the user launches the same name.
- **Then** the name conflict is reported and the running agent remains unchanged.

## Automated E2E Verification Matrix

| Scenario | Test case |
| --- | --- |
| STALE-001 | `normal termination permits relaunch` |
| STALE-002 | `orphan session refuses relaunch without mutation` |
| STALE-003 | `active name remains reserved` |
