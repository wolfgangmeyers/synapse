# Specification: Workflow State Goal Integration and Goal Locking

## 1. Problem Statement
Workflow states currently configure agent instructions (`entry_text`), tool restrictions (`allowed_tools`), state transitions (`transitions`), exit flags (`allow_exit`), and scheduled reminders (`reminders`). However, workflow states do not support binding a persistent goal to a workflow state. When an agent transitions through structured workflow phases (e.g., from "Specification" to "Implementation" to "Review"), operators must either rely on transient entry prompts or manually reconfigure the agent's goal at each phase.

Furthermore, when an agent is bound to a workflow state with an assigned goal, the agent (or a worker calling the `goal` tool) must not be able to arbitrarily overwrite or disable that state-mandated goal. The goal must remain locked to the active workflow state until the workflow transitions or exits.

This feature introduces:
1. Schema migration adding `goal` to `workflow_states`.
2. Store CRUD and HTTP transport support for `goal` on workflow states.
3. Runtime goal synchronization upon entering, transitioning, or exiting workflow states with explicit goal locking.
4. Goal locking enforcement that denies worker attempts to mutate or clear the goal while in a workflow state that has a non-empty goal.
5. Web UI enhancements in `StateConfigModal.tsx`, `WorkflowStateNode.tsx`, and `WorkflowStatesPage.tsx` using `GoalEditorDialog`.

---

## 2. Database & Store Schema

### 2.1 Schema Migration (`099_workflow_state_goal.sql`)
- Add migration file `router-proxy/internal/store/migrations/099_workflow_state_goal.sql`:
  ```sql
  ALTER TABLE workflow_states ADD COLUMN goal TEXT NOT NULL DEFAULT '' CHECK (length(goal) <= 4000);
  ```
- Harmonized to 4000 characters to match `client_frames.go` and `GoalEditorDialog`.
- Registered in `router-proxy/internal/store/store.go` migrations list.

### 2.2 Store & Domain Model
- Validation constant in `router-proxy/internal/store/workflow_states.go`:
  ```go
  const maxWorkflowStateGoalBytes = 4000
  ```
- `WorkflowState` struct in `router-proxy/internal/store/workflow_states.go`:
  - Add field `Goal string `json:"goal"``
- `WorkflowStateRequest` struct in `router-proxy/internal/store/workflow_states.go`:
  - Add field `Goal string `json:"goal"``
- Validation in `validateWorkflowStateRequest`:
  - Check `len([]byte(req.Goal)) <= maxWorkflowStateGoalBytes` (return `WorkflowStateValidationError` on field `"goal"` if exceeded).
- Store CRUD in `router-proxy/internal/store/workflow_states.go` & `workflows.go`:
  - Query, insert, and update `goal` across all `workflow_states` SQL statements:
    - `CreateWorkflowState`
    - `UpdateWorkflowState`
    - `GetWorkflowState`
    - `ListWorkflowStates`
    - `ListWorkflowStatesByWorkflow`
    - `GetWorkflowWithStates`
- TypeScript domain model (`web/router-ui-react/src/domain/workflow-states.ts`):
  - Add `goal?: string;` to `WorkflowState` and `WorkflowStateInput`.

### 2.3 HTTP Transport Handlers
- In `router-proxy/internal/transport/workflow_states_http.go` and `workflows_http.go`:
  - Parse `goal` from incoming JSON request body.
  - Return `goal` in JSON responses for `GET`, `POST`, and `PUT` `/v1/workflow-states` and `/v1/workflows`.

---

## 3. Runtime Activation & Lifecycle

When a managed agent's active workflow state changes, the router automatically synchronizes the agent's goal configuration.

### 3.1 State Entry & Activation
When activating a workflow state on an agent (via `SetManagedAgentActiveWorkflowState` in `router-proxy/internal/managed/`, HTTP `/v1/managed-agents/{id}/workflow-state`, or state transition in `embedded_workflow_transition.go`):
1. Load the target `WorkflowState`.
2. If `state.Goal != ""`:
   - Set the agent's goal to `state.Goal`.
   - The applied `ManagedGoalConfig` must explicitly set:
     ```go
     Enabled: true,
     Goal:    state.Goal,
     Settings: store.ManagedGoalSettings{
         GoalLockEnabled: true,
         // ... default or existing snooze settings preserved
     },
     ```
   - Project the updated policy to the active embedded session or RPC worker.
3. If `state.Goal == ""`:
   - Deactivate the agent's goal:
     ```go
     Enabled: false,
     Goal:    "",
     Settings: store.ManagedGoalSettings{
         GoalLockEnabled: false,
     },
     ```
   - Project the deactivated policy to the active embedded session or RPC worker.

### 3.2 State Exit & Deactivation
When exiting or deactivating an active workflow state (via `action="exit"` in `embedded_workflow_transition.go` or workflow deactivation via HTTP):
1. Disable the goal:
   ```go
   Enabled: false,
   Goal:    "",
   Settings: store.ManagedGoalSettings{
       GoalLockEnabled: false,
   },
   ```
2. Project the deactivated goal policy to the active agent session.

### 3.3 Implementation Locations
- `router-proxy/internal/store/managed.go` (`SetManagedAgentActiveWorkflowState` / manager orchestration layer):
  - Coordinate goal policy updates alongside workflow state ID persistence.
- `router-proxy/internal/managed/embedded_workflow_transition.go`:
  - When executing `"transition"` action: apply target state's goal policy (`GoalLockEnabled = true` if `state.Goal != ""`, or disabled if `""`).
  - When executing `"exit"` action: deactivate the goal policy (`Enabled: false, Goal: "", Settings.GoalLockEnabled = false`).
- `router-proxy/internal/transport/managed_http.go`:
  - Trigger goal synchronization on manual workflow state assignment or clearing.

---

## 4. Goal Locking Semantics

When an agent is executing within a workflow state that defines a non-empty `goal`:

### 4.1 Rejection of Worker Goal Changes
In `executeManagedGoalControl` (`router-proxy/internal/managed/managed_goal_projection.go`):
- Check whether the agent has an active workflow state with `state.Goal != ""`.
- Worker attempts to mutate or clear the goal (`action == "set"` or `action == "off"`) while in a workflow state that has a non-empty goal are denied with:
  ```go
  fmt.Errorf("goal is locked by active workflow state %q", state.Name)
  ```
- Action `"status"`: PERMITTED. Returns the current goal status and evaluation details without modification.

### 4.2 Administrative & Workflow Transitions
- Workflow transitions and administrative APIs apply state-mandated goal updates directly through the manager lifecycle path.
- Worker tool calls (`goal` / `goal.control`) from the LLM turn remain locked and cannot override or turn off the workflow state goal.

---

## 5. Web UI & Frontend

### 5.1 `WorkflowStateNode.tsx`
When `state.goal` is non-empty (`Boolean(state.goal)`), render a goal badge:
```tsx
{Boolean(state.goal) && (
  <span className="node-badge badge-goal" title={state.goal}>
    <i className="fa-solid fa-flag" aria-hidden="true" /> Goal
  </span>
)}
```

### 5.2 `StateConfigModal.tsx`
Directly reuse `GoalEditorDialog`:
- State holds local draft `goal: string` initialized from `state.goal ?? ""`.
- **Goal Section**:
  - When no goal is configured (`!goal`):
    - Display a section header "Workflow State Goal" and a button "Configure Goal".
    - Clicking "Configure Goal" opens `GoalEditorDialog`.
  - When a goal is configured (`Boolean(goal)`):
    - Display a clean preview box showing the goal text (matching how reminders are previewed and managed).
    - Provide an "Edit Goal" button (opens `GoalEditorDialog`) and a "Clear Goal" button (clears `goal` to `""`).
- **`GoalEditorDialog` Integration**:
  - Initializes with the state's current `goal` string:
    - Pass `goalState` (or `value`) constructed from the state: `{ ...DEFAULT_GOAL_STATE, enabled: Boolean(goal), goal: goal || "" }`.
    - Set `peer` to state name (e.g. `peer={`State: ${name}`}`).
  - On save (`onSave`):
    - Extract `payload.goal` (if payload action is `"set"` and enabled, or `""` if `"off"`).
    - Update the state's `goal` property and close `GoalEditorDialog`.
- Include `goal` in the `updated` state passed to `onSave(updated, makeEntry)` on submit.

### 5.3 `WorkflowStatesPage.tsx`
- Ensure table/list state summaries and state creation forms display and preserve `goal`.
- Display a goal indicator or badge when a state defines a goal.

---

## 6. Testing Strategy

1. **Store & Migration Tests (`workflow_states_test.go`, `migrations_test.go`)**:
   - Verify migration 099 applies cleanly with `CHECK (length(goal) <= 4000)`.
   - Verify CRUD operations persist and return `goal`.
   - Verify validation fails when `goal` exceeds 4000 bytes.
2. **Runtime Lifecycle Tests (`embedded_workflow_activation_test.go`, `embedded_workflow_transition_test.go`)**:
   - Verify entering a state with `goal` sets `Enabled: true`, `Goal: state.Goal`, and `Settings.GoalLockEnabled = true`.
   - Verify entering a state with `goal == ""` disables goal (`Enabled: false, Goal: "", Settings.GoalLockEnabled = false`).
   - Verify exiting a workflow deactivates goal (`Enabled: false, Goal: "", Settings.GoalLockEnabled = false`).
3. **Goal Lock Tests (`managed_goal_projection_test.go`)**:
   - Verify worker calling `goal(action="set")` or `goal(action="off")` while in a state with a goal is rejected with:
     `goal is locked by active workflow state "<StateName>"`
   - Verify worker calling `goal(action="status")` succeeds.
4. **Web UI Tests (`WorkflowStateNode.test.tsx`, `StateConfigModal.test.tsx`)**:
   - Verify node badge renders `<i className="fa-solid fa-flag" ... /> Goal` with `title={state.goal}` when `state.goal` is present.
   - Verify opening `GoalEditorDialog` from `StateConfigModal` initializes draft goal text, saves back to state, and preview box renders with Edit and Clear buttons.
