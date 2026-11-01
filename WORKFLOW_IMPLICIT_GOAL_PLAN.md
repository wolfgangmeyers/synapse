# WORKFLOW_IMPLICIT_GOAL_PLAN.md

## Overview

Currently, workflow goals are attached to individual workflow states (`workflow_states.goal`). This design is fractured and fragile:
- Goals get cleared or rewritten whenever an agent transitions between states (if a target state has an empty goal, the agent's goal is wiped out).
- Goal lock enforcement has loopholes (if the active state has an empty goal string, agents can manually set/clear goals).
- There is no workflow-level goal continuity; the overarching goal is simply to complete the workflow, regardless of internal state progression.
- Goal snooze is currently ad-hoc and not tied to workflow execution cadence.

This plan removes per-state goals entirely and introduces a clean, unified **implicit workflow completion goal**:
1. When an agent is in an active workflow, an implicit goal to complete the active workflow is continuously active.
2. Manual goals (`goal set`, `goal off`, `PUT /v1/managed-agents/:id/goal`, WebSocket `goal_control`, etc.) cannot be set or unset while a workflow is active.
3. Goal snooze is configurable at the workflow level (`snooze_duration_ms`), defaulting to 10 minutes (`600,000` ms). Calling `goal_snooze` without parameters uses this workflow-level default.
4. If snooze has not kicked in (or has elapsed), any time a worker settles (`idle`), the settling mechanism resumes the goal loop immediately.
5. Internal workflow state transitions do not interrupt, re-initialize, or clear the implicit workflow goal. The goal only deactivates when the workflow exits or is deactivated.

---

## 1. Architecture & Design

### 1.1 Implicit Workflow Goal Model
- **Definition**: The goal is derived directly from the active workflow:
  `Complete active workflow: <workflow.name>` (compact, concise, avoiding prompt bloat).
- **Lifecycle & Evaluator Rules**:
  - **Activation**: When an agent enters an active workflow (via `PUT /v1/managed-agents/{agent_id}/workflow-state`, admin tool `worker_set_workflow_state`, or background workflow runner), the router activates this implicit goal on the agent's `managed_agent_policies` with `GoalLockEnabled = true`.
  - **State Transitions**: When `mecha_workflow_transition` executes with `action="transition"`, the state changes, but the workflow goal is NOT cleared or rewritten. It remains continuously active.
  - **Completion & Exit Contract**: As long as the agent remains in an active workflow (`curAgent.ActiveWorkflowID != nil`), the implicit workflow goal cannot be prematurely declared `COMPLETED` by a loose turn evaluator while the agent is still mid-workflow. The goal only deactivates when `mecha_workflow_transition` executes with `action="exit"` or when the workflow is explicitly cleared on the agent (`Enabled = false, Goal = "", GoalLockEnabled = false`).

### 1.2 Unified Goal Lock During Active Workflows
Instead of scattering ad-hoc checks across individual endpoints, goal lock validation is enforced centrally:
- In `store.UpdateManagedGoalConfig` / `manager.UpdateManagedGoalConfig`:
  If the agent has an active workflow attached (`agent.ActiveWorkflowID != nil && *agent.ActiveWorkflowID != ""`) and the request is not an internal workflow lifecycle sync, reject the update with:
  `"manual goal control is disabled while workflow %q is active; the active goal is to complete the workflow"`
- In `embedded_goal_tool.go` (`goal` tool inside the agent runtime):
  Inspect `curAgent.ActiveWorkflowID`. If non-nil, immediately reject `action="set"` or `action="off"`.

### 1.3 Workflow-Level Snooze Configuration & Backward Compatibility
- In `workflows` table: add `snooze_duration_ms INTEGER NOT NULL DEFAULT 600000 CHECK (snooze_duration_ms > 0)`.
- When an agent activates a workflow, `goalSettings.SnoozeDurationMS` is populated from the workflow's configured `snooze_duration_ms`.
- Default value: 10 minutes (`600,000` ms).
- If the agent calls `goal_snooze` without an explicit duration (or with 0), it defaults to the workflow's `snooze_duration_ms`.
- **Bundle Import/Export Backward Compatibility**:
  - **Importing legacy bundles**: If `snooze_duration_ms` is missing or `<= 0`, default to `600000`.
  - **Legacy `goal` in state JSON**: Unmarshal silently ignores or drops `goal` without failing bundle validation (`ValidateExportBundle`).
  - **Exporting**: Always emits `snooze_duration_ms` (defaulting to 600,000 if not set).

### 1.4 Worker Settling & Idle Kickoff
- In `MaybeResumeEmbeddedGoal` and `OfferDirect`:
  - When the worker settles (`curAgent.ActivityState == "idle"`):
    1. If `pref.SnoozeUntil > now` (snooze is active), do not resume until the snooze timer fires.
    2. If snooze has NOT kicked on or has already expired, immediately trigger `kickoffIdleGoalContinuation`.
- This ensures worker settling instantly drives workflow progression without artificial delay unless explicitly snoozed.

### 1.5 Defragmentation & Clean Removal of Per-State Goals
- **Database**:
  - Add migration `117_workflow_implicit_goals.sql`:
    ```sql
    ALTER TABLE workflows ADD COLUMN snooze_duration_ms INTEGER NOT NULL DEFAULT 600000 CHECK (snooze_duration_ms > 0);
    ALTER TABLE workflow_states DROP COLUMN goal;
    ```
- **Go Router (`router-proxy/internal/store/`)**:
  - Remove `Goal` from `WorkflowState` and `WorkflowStateRequest`.
  - Add `SnoozeDurationMS int` to `Workflow` and `WorkflowRequest`.
  - Update `workflow_import_export.go` to import/export `snooze_duration_ms` on the workflow and drop `goal` from states.
- **Go Managed Execution (`router-proxy/internal/managed/`)**:
  - Remove state goal synchronization in `embedded_workflow_transition.go`.
  - Maintain the implicit workflow goal across transitions.
  - Simplify `embedded_goal_tool.go` and `embedded_goal_snooze_tool.go`.
- **Web UI & Domain**:
  - Remove state goal input from `web/router-ui-react/src/components/StateConfigModal.tsx` and `domain/workflow-states.ts`.
  - Add workflow snooze duration setting in the workflow configuration editor / modal.

---

## 2. Step-by-Step Implementation Phases

### Phase 1: Database Schema & Store Layer
1. Create migration `117_workflow_implicit_goals.sql`:
   - `ALTER TABLE workflows ADD COLUMN snooze_duration_ms INTEGER NOT NULL DEFAULT 600000 CHECK (snooze_duration_ms > 0);`
   - `ALTER TABLE workflow_states DROP COLUMN goal;`
2. Update `router-proxy/internal/store/workflows.go`:
   - Add `SnoozeDurationMS int` to `Workflow` and `WorkflowRequest`.
   - Update SQL queries (`CreateWorkflow`, `UpdateWorkflow`, `GetWorkflow`, `ListWorkflows`) to include `snooze_duration_ms`.
   - Enforce default `600000` when `SnoozeDurationMS <= 0`.
3. Update `router-proxy/internal/store/workflow_states.go`:
   - Remove `Goal` field from `WorkflowState`, `WorkflowStateRequest`, and SQL queries.
4. Update `router-proxy/internal/store/workflow_import_export.go`:
   - Add `SnoozeDurationMS` to `WorkflowInfoExport`.
   - Remove `Goal` from `WorkflowStateExport` (accept legacy field gracefully if present).

### Phase 2: Router Execution & Goal Lifecycle
1. Update `router-proxy/internal/managed/embedded_workflow_transition.go`:
   - On workflow entry: compute the implicit goal from the workflow (`Complete active workflow: <name>`), configure `SnoozeDurationMS` from workflow, set `GoalLockEnabled = true`, and apply policy.
   - On state transition: do NOT touch the goal. Keep the implicit workflow goal intact.
   - On workflow exit: clear goal policy (`Enabled = false, Goal = "", GoalLockEnabled = false`).
2. Update `router-proxy/internal/managed/embedded_goal_tool.go`:
   - Check if agent is currently in an active workflow (`curAgent.ActiveWorkflowID != nil`).
   - If in an active workflow, reject `set` and `off` actions with a descriptive error.
3. Update `router-proxy/internal/managed/embedded_goal_snooze_tool.go`:
   - If duration is omitted or 0, check if in an active workflow and fallback to the workflow's configured `snooze_duration_ms` (or 600,000 ms default).
4. Update `router-proxy/internal/transport/managed_http.go`:
   - In `PUT /v1/managed-agents/:agent_id/goal`: reject changes if agent is in an active workflow.
   - In `PUT /v1/managed-agents/:agent_id/workflow-state`: when activating, activate implicit workflow goal; when clearing, clear goal.

### Phase 3: Web UI & Workflow Definitions
1. Update `web/router-ui-react/src/domain/workflow-states.ts`:
   - Remove `goal` from `WorkflowState`.
   - Add `snooze_duration_ms` to `Workflow` domain interface.
2. Update `web/router-ui-react/src/components/StateConfigModal.tsx`:
   - Remove goal textarea and goal preview card.
3. Update `.claude/skills/workflow-management/SKILL.md`:
   - Remove per-state `goal` fields from canonical bundle templates and document the implicit workflow completion goal.

### Phase 4: Test Updates & Verification
1. Update Go unit tests in `router-proxy/internal/store/` and `router-proxy/internal/managed/`:
   - `workflow_states_test.go`, `workflows_test.go`
   - `embedded_workflow_transition_test.go`
   - `embedded_goal_tool_test.go`
   - `managed_workflow_state_test.go`
2. Update React UI unit/component tests in `web/router-ui-react/`:
   - `StateConfigModal.test.tsx`
   - `workflow-states.test.tsx`
3. Run test suites and verify background workflows, transitions, settling wakeup, and snooze handling.

---

## 3. Risks & Non-Goals

- **Non-Goal**: We are not changing how subagents or mailbox notifications function.
- **Simplicity**: No complex state machines for goals; the workflow is either active (implicit goal on, locked) or inactive (goals manually controllable).
