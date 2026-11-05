# Spec: Implicit Workflow Goals & Workflow-Level Snooze

- **ID**: `SPEC-WORKFLOWS-002`
- **Status**: `Active`
- **Domain**: `Workflows`
- **Target E2E Suite**: `tests/e2e/workflow-implicit-goals.spec.ts`

---

## 1. Intent & User Outcome

Previously, workflow goals were defined on individual workflow states (`workflow_states.goal`). This fractured architecture caused goals to be overwritten, dropped, or cleared on internal state transitions whenever a state omitted a goal, leading to loss of agent focus and loopholes in goal-lock enforcement.

This specification unifies workflow goal management by removing state-level goals entirely and introducing a persistent, router-enforced **implicit workflow completion goal**:
1. When an agent binds to or executes an active workflow, the router automatically activates an implicit goal: `"Complete active workflow: <workflow.name>"` with goal lock enabled.
2. Goal snooze is configured at the workflow level via `snooze_duration_ms` (defaulting to 10 minutes / 600,000 ms).
3. Internal state transitions maintain the implicit workflow goal continuously without re-initialization, interruption, or erasure.
4. The first legitimate worker idle goal wake may resume immediately when snooze is inactive or elapsed. Subsequent automatic attempts are paced (default 10 seconds after the preceding automatic attempt settles) and all automatic attempts, including failures before turn start, count toward a 20-attempt consecutive automatic-chain cap. Session-owner failure stops automatic continuation until deliberate recovery validates ownership. See [automatic wake safety](goal-wake-safety.spec.md).
5. Exiting or deactivating the workflow cleanly unlocks and deactivates the agent goal.
6. Legacy workflow bundles containing state-level goals are imported seamlessly with backward compatibility, discarding obsolete goal fields and applying default snooze durations.

---

## 2. Core Invariants

1. **State-Level Goal Elimination**: Workflow states do NOT possess individual goal fields in the database schema, REST API responses, Web UI configuration modals, or export bundles.
2. **Implicit Workflow Goal Derivation**: Any agent residing in an active workflow automatically receives the locked goal `"Complete active workflow: <workflow.name>"`.
3. **Goal Lock Enforcement**: While an agent is bound to an active workflow, manual goal modification (`goal set`, `goal off`, `PUT /v1/managed-agents/:id/goal`, WebSocket goal controls) is strictly prohibited and rejected with HTTP 409 Conflict.
4. **Transition Goal Continuity**: Internal workflow state transitions (`action="transition"`) MUST NEVER clear, reset, or rewrite the implicit workflow goal.
5. **Workflow-Level Snooze Default**: Workflows define `snooze_duration_ms` (integer > 0, default `600000`). Snooze durations during active workflows inherit this value unless an explicit override is supplied.
6. **Worker Settling Kickoff**: When a managed worker in an active workflow reaches the `idle` activity state:
   - If goal snooze is active (`snooze_until > now`), continuation remains on hold until snooze expiry, subject also to retry delay, budget and valid ownership.
   - With valid ownership, available budget and no snooze, the first legitimate automatic idle wake may run immediately without artificial delay.
   - Repeated automatic attempts wait the applicable delay (default 10 seconds after the preceding automatic attempt settles); at most 20 automatic attempts are allowed per consecutive automatic chain, including failed admission and failed turns. Automatic failures and repeated idle notifications never reset their own chain count. Session-owner failure stops automatic continuation until explicit recovery validates ownership. [SPEC-GOAL-001](goal-wake-safety.spec.md) owns these safety and user-recovery rules.
7. **Exit Goal Cleanup**: When an agent exits a workflow (`action="exit"`) or is deactivated from a workflow, the implicit workflow goal is deactivated, cleared, and unlocked.
8. **Backward Compatibility**: Importers of legacy `mecha-workflows` bundles ignore obsolete state-level `goal` fields and populate `snooze_duration_ms` with `600000` when missing or non-positive.

---

## 3. User Scenarios (Behavioral Contract)

### Happy Path Scenarios

#### Scenario: [SCN-WFIG-001] Workflow creation with custom snooze duration and absence of state goal inputs
- **User Goal**: Author a new workflow in the Web UI with custom snooze duration and configure states without per-state goal prompts.
- **Given**:
  - The Web UI is loaded and the user navigates to the Workflows page (`#/workflows`).
- **When**:
  - The user opens the "New Workflow" dialog.
  - The user observes the goal snooze input defaulted to `10` minutes.
  - The user enters a name (e.g. `"E2E Test Workflow"`), description, and sets snooze to `15` minutes (`900,000` ms).
  - The user submits creation and adds workflow states on the canvas.
  - The user opens the "Configure State" dialog for any state.
- **Then**:
  - The State Configuration dialog contains fields for Name, Description, Entry State toggle, Entry Text, and Exit Permission.
  - The State Configuration dialog contains NO goal input field or goal label.
  - The created workflow persists `snooze_duration_ms: 900000`.
  - State REST endpoints (`/v1/workflows/:id`, `/v1/workflow-states/:id`) omit `goal` properties.

#### Scenario: [SCN-WFIG-002] Implicit goal activation upon entering workflow and locked goal enforcement
- **User Goal**: Ensure an agent entering a workflow is automatically focused on completing the workflow with manual overrides disabled.
- **Given**:
  - A workflow `"Release Pipeline"` exists with `snooze_duration_ms = 900000`.
  - A managed agent enters the workflow via launch (`POST /v1/workflows/:id/run`) or assignment.
- **When**:
  - The router activates the workflow on the agent.
- **Then**:
  - The agent's goal state reports `enabled: true`, `goal: "Complete active workflow: Release Pipeline"`, `goal_lock_enabled: true`, and `snooze_duration_ms: 900000`.
  - When an external caller or agent attempts `PUT /v1/managed-agents/:id/goal` with `{"action": "set", "goal": "Tampering"}`, the router rejects the request with HTTP 409 Conflict indicating manual goal control is disabled while a workflow is active.

#### Scenario: [SCN-WFIG-003] Continuous implicit goal preservation across internal state transitions
- **User Goal**: Verify that transitioning between workflow states preserves workflow goal focus across the entire lifecycle.
- **Given**:
  - An agent is executing in state A of `"Release Pipeline"` with active implicit goal `"Complete active workflow: Release Pipeline"`.
- **When**:
  - The agent or supervisor triggers a forward transition to state B (`action="transition"`).
- **Then**:
  - State B instructions and entry text are admitted into the turn.
  - The agent's goal state remains `enabled: true`, `goal: "Complete active workflow: Release Pipeline"`, and `goal_lock_enabled: true`.
  - The implicit workflow goal is not cleared, re-evaluated, or interrupted.

#### Scenario: [SCN-WFIG-004] Worker settling kickoff when snooze is not active vs hold when snoozed
- **User Goal**: Ensure workflow workers autonomously resume execution upon settling unless explicitly snoozed.
- **Given**:
  - An agent is running an active workflow.
- **When**:
  - Case 1: The initial normal agent turn finishes and the worker settles into `idle` with valid ownership, an available automatic chain and inactive goal snooze (`snooze_until <= now` or unset).
- **Then**:
  - The first legitimate idle goal continuation may start immediately. Subsequent automatic attempts obey the default 10-second pacing and 20-attempt consecutive automatic-chain cap, including on failure; ownership failures stop automation as specified by `SPEC-GOAL-001`.
- **When**:
  - Case 2: The agent invoked `goal_snooze` (defaulting to the workflow's configured snooze duration, e.g. 10 or 15 minutes) and settles into `idle`.
- **Then**:
  - The router holds execution until the snooze timestamp elapses; expiry cannot bypass retry delay, exhausted budget or invalid-owner holds.

#### Scenario: [SCN-WFIG-005] Workflow exit deactivates and clears the goal
- **User Goal**: Ensure leaving an active workflow restores normal agent goal state.
- **Given**:
  - An agent is in an exit-permitted state of an active workflow.
- **When**:
  - The agent executes `mecha_workflow_transition` with `action="exit"` (or supervisor deactivates workflow via `PUT /v1/managed-agents/:id/workflow-state` with `action="deactivate"`).
- **Then**:
  - The router removes active workflow and state references from the agent.
  - The agent goal state is reset to `enabled: false`, `goal: ""`, and `goal_lock_enabled: false`.
  - Manual goal control is re-enabled for subsequent agent operations.

### Edge & Error Scenarios

#### Scenario: [SCN-WFIG-006] Backward compatibility for legacy workflow bundles
- **User Goal**: Import existing or legacy workflow bundles containing deprecated state goals and missing snooze duration without errors.
- **Given**:
  - A legacy `mecha-workflows` v1 bundle with states defining `"goal": "Do state work"` and omitting `snooze_duration_ms`.
- **When**:
  - The bundle is imported via `workflow_save` or `POST /v1/workflows`.
- **Then**:
  - Bundle validation succeeds without schema errors.
  - The imported workflow assigns `snooze_duration_ms: 600000` (10 minutes default).
  - Obsolete state-level `goal` properties are discarded and not persisted in `workflow_states`.

---

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `SCN-WFIG-001` | Workflow creation with custom snooze duration and absence of state goal inputs | `tests/e2e/workflow-implicit-goals.spec.ts` | `SCN-WFIG-001: Workflow creation with custom snooze, state modal inspection (no goal field), and end-to-end transitions` |
| `SCN-WFIG-002` | Implicit goal activation upon entering workflow and locked goal enforcement | `tests/e2e/workflow-implicit-goals.spec.ts` | `SCN-WFIG-001: Workflow creation with custom snooze, state modal inspection (no goal field), and end-to-end transitions` |
| `SCN-WFIG-003` | Continuous implicit goal preservation across internal state transitions | `tests/e2e/workflow-implicit-goals.spec.ts` | `SCN-WFIG-001: Workflow creation with custom snooze, state modal inspection (no goal field), and end-to-end transitions` |
| `SCN-WFIG-004` | Worker settling kickoff when snooze is not active vs hold when snoozed | `tests/e2e/workflow-implicit-goals.spec.ts` | `SCN-WFIG-001: Workflow creation with custom snooze, state modal inspection (no goal field), and end-to-end transitions` |
| `SCN-WFIG-005` | Workflow exit deactivates and clears the goal | `tests/e2e/workflow-implicit-goals.spec.ts` | `SCN-WFIG-001: Workflow creation with custom snooze, state modal inspection (no goal field), and end-to-end transitions` |
| `SCN-WFIG-006` | Backward compatibility for legacy workflow bundles | `router-proxy/internal/store/workflow_import_export_test.go` | `TestWorkflowImportExport_LegacyStateGoals` |
