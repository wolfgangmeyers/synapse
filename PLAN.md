# Implementation Plan: Workflow State Goals & Goal Locking

## 1. Architecture & Component Summary

Workflow State Goals bind persistent goals to specific workflow states, automatically synchronizing agent goals as agents enter, transition, or exit workflow states. While an agent is bound to a workflow state with an active non-empty goal, worker attempts to overwrite or turn off the goal via worker tool controls (`goal` / `goal.control`) are locked and rejected.

```
                          ┌──────────────────────────┐
                          │   Store / DB (SQLite)    │
                          │   Migration 099          │
                          │   workflow_states.goal   │
                          └────────────┬─────────────┘
                                       │
                    ┌──────────────────┴──────────────────┐
                    ▼                                     ▼
        ┌───────────────────────┐             ┌───────────────────────┐
        │  HTTP Transport Layer │             │ Runtime Manager Layer │
        │  /v1/workflow-states  │             │ embedded_workflow_    │
        │  /v1/workflows        │             │ transition.go &       │
        └───────────┬───────────┘             │ managed_http.go       │
                    │                         └───────────┬───────────┘
                    ▼                                     ▼
        ┌───────────────────────┐             ┌───────────────────────┐
        │    React Web UI       │             │ Goal Lock Enforcement │
        │ StateConfigModal      │             │ executeManagedGoal-   │
        │ WorkflowStateNode     │             │ Control: reject worker│
        │ WorkflowStatesPage    │             │ changes while locked  │
        └───────────────────────┘             └───────────────────────┘
```

### Components Impacted
1. **Database & Store (`router-proxy/internal/store/`)**:
   - Migration `099_workflow_state_goal.sql` adding `goal TEXT NOT NULL DEFAULT '' CHECK (length(goal) <= 4000)`.
   - Domain struct `WorkflowState` and `WorkflowStateRequest` field `Goal string`.
   - SQL queries and CRUD methods in `workflow_states.go` and `workflows.go`.
   - Request validation enforcing `<= 4000` bytes.
2. **HTTP Transport (`router-proxy/internal/transport/`)**:
   - `workflow_states_http.go`: Request parsing and response rendering for state `goal`.
   - `workflows_http.go`: Preserving state `goal` across workflow bundle CRUD.
   - `managed_http.go`: Goal synchronization when manually assigning or clearing workflow states on managed agents.
3. **Runtime & Lifecycle (`router-proxy/internal/managed/`)**:
   - `embedded_workflow_transition.go`: Applying target state's goal policy (`GoalLockEnabled = true`, `Enabled = true`) on `"transition"`, and deactivating goal (`Enabled = false`, `Goal = ""`, `GoalLockEnabled = false`) on `"exit"`.
   - Lifecycle orchestration (`SetManagedAgentActiveWorkflowState` integration) to coordinate goal projection with state persistence.
4. **Goal Locking (`router-proxy/internal/managed/`)**:
   - `managed_goal_projection.go` (`executeManagedGoalControl`): Denying worker `set` or `off` actions with `"goal is locked by active workflow state <Name>"` when active state has non-empty goal. `status` remains permitted.
5. **Web UI (`web/router-ui-react/`)**:
   - `src/domain/workflow-states.ts`: Adding `goal?: string` to `WorkflowState` and `WorkflowStateInput`.
   - `src/components/StateConfigModal.tsx`: Integrating `GoalEditorDialog`, preview box, "Configure Goal", "Edit Goal", and "Clear Goal" controls.
   - `src/components/WorkflowStateNode.tsx`: Rendering `<span className="node-badge badge-goal" title={state.goal}><i className="fa-solid fa-flag" aria-hidden="true" /> Goal</span>`.
   - `src/components/WorkflowStatesPage.tsx`: Ensuring goal is displayed in state lists/summaries and preserved in state forms.

---

## 2. Phased Implementation Breakdown

### Phase 1: DB Migration 099 & Store Layer
*Goal*: Add `goal` column to `workflow_states` with a 4000-character constraint, update domain models, validation, and CRUD queries in the Store.

- [ ] **1.1 Test-First (TDD)**:
  - Add tests in `router-proxy/internal/store/workflow_states_test.go`:
    - Test validation: reject `req.Goal` exceeding 4000 bytes with `WorkflowStateValidationError` on field `"goal"`.
    - Test `CreateWorkflowState` with non-empty goal and verify persistence and retrieval.
    - Test `UpdateWorkflowState` updating goal from empty to non-empty and vice-versa.
    - Test `ListWorkflowStates` and `ListWorkflowStatesByWorkflow` returning `Goal`.
    - Test `GetWorkflowWithStates` returning `Goal` on constituent states.
  - Add test in `router-proxy/internal/store/migrations_test.go` verifying migration `099_workflow_state_goal.sql` runs cleanly and enforces the check constraint.
- [ ] **1.2 Migration Script**:
  - Create `router-proxy/internal/store/migrations/099_workflow_state_goal.sql`:
    ```sql
    ALTER TABLE workflow_states ADD COLUMN goal TEXT NOT NULL DEFAULT '' CHECK (length(goal) <= 4000);
    ```
  - Register migration `099_workflow_state_goal.sql` in `router-proxy/internal/store/store.go`.
- [ ] **1.3 Store Model & Validation**:
  - In `router-proxy/internal/store/workflow_states.go`:
    - Add constant `maxWorkflowStateGoalBytes = 4000`.
    - Add `Goal string `json:"goal"`` to `WorkflowState`.
    - Add `Goal string `json:"goal"`` to `WorkflowStateRequest`.
    - In `validateWorkflowStateRequest`, validate `len([]byte(req.Goal)) <= maxWorkflowStateGoalBytes`.
- [ ] **1.4 Store CRUD SQL**:
  - Update all SQL queries in `router-proxy/internal/store/workflow_states.go` and `router-proxy/internal/store/workflows.go`:
    - `CreateWorkflowState`: insert `goal`.
    - `UpdateWorkflowState`: update `goal`.
    - `GetWorkflowState`, `ListWorkflowStates`, `ListWorkflowStatesByWorkflow`, `GetWorkflowWithStates`: select `goal` column and scan into `WorkflowState.Goal`.
- [ ] **1.5 Validation**: Run `go test ./router-proxy/internal/store/...` to confirm all tests pass.

---

### Phase 2: HTTP Transport Layer
*Goal*: Expose `goal` through REST endpoints for workflow states and workflow bundles, with proper payload parsing and response serialization.

- [ ] **2.1 Test-First (TDD)**:
  - Add transport tests in `router-proxy/internal/transport/workflow_states_http_test.go`:
    - Test POST `/v1/workflow-states` with `{"goal": "Finish phase 1"}` creates state and returns `goal` in JSON.
    - Test PUT `/v1/workflow-states/{id}` updates `goal`.
    - Test GET `/v1/workflow-states/{id}` returns `goal`.
    - Test validation errors (e.g. goal > 4000 bytes) return HTTP 400.
  - In `router-proxy/internal/transport/workflows_http_test.go`:
    - Verify workflow bundle creation/update preserves state `goal`.
- [ ] **2.2 Transport Handlers**:
  - In `router-proxy/internal/transport/workflow_states_http.go`:
    - Ensure request payload unmarshals `goal` into `WorkflowStateRequest`.
    - Ensure JSON response representations include `goal`.
  - In `router-proxy/internal/transport/workflows_http.go`:
    - Ensure state objects within workflow endpoints serialize and deserialize `goal`.
- [ ] **2.3 Validation**: Run `go test ./router-proxy/internal/transport/...` to confirm HTTP endpoints adhere to the contract.

---

### Phase 3: Runtime Activation & Lifecycle
*Goal*: Automatically synchronize agent goal configuration upon entering, transitioning, or exiting workflow states.

- [ ] **3.1 Test-First (TDD)**:
  - Add lifecycle tests in `router-proxy/internal/managed/embedded_workflow_transition_test.go` and `router-proxy/internal/managed/embedded_workflow_activation_test.go`:
    - Test state transition to a state with non-empty goal: agent goal is updated with `Enabled: true`, `Goal: state.Goal`, `GoalLockEnabled: true`.
    - Test state transition to a state with empty goal: agent goal is disabled (`Enabled: false, Goal: "", GoalLockEnabled: false`).
    - Test workflow exit (`action="exit"`): agent goal is deactivated (`Enabled: false, Goal: "", GoalLockEnabled: false`).
  - Add transport lifecycle tests in `router-proxy/internal/transport/managed_http_test.go`:
    - Test assigning workflow state via HTTP `/v1/managed-agents/{id}/workflow-state` synchronizes goal policy to agent session.
    - Test clearing workflow state via HTTP deactivates goal.
- [ ] **3.2 Manager Lifecycle Sync**:
  - In `router-proxy/internal/store/managed.go` / `router-proxy/internal/managed/`:
    - Coordinate goal policy updates alongside `SetManagedAgentActiveWorkflowState`.
    - If target state has `state.Goal != ""`:
      ```go
      ManagedGoalConfig{
          Enabled: true,
          Goal:    state.Goal,
          Settings: store.ManagedGoalSettings{
              GoalLockEnabled: true,
          },
      }
      ```
    - If target state has `state.Goal == ""` or is cleared:
      ```go
      ManagedGoalConfig{
          Enabled: false,
          Goal:    "",
          Settings: store.ManagedGoalSettings{
              GoalLockEnabled: false,
          },
      }
      ```
- [ ] **3.3 State Transition Execution**:
  - In `router-proxy/internal/managed/embedded_workflow_transition.go`:
    - When handling `"transition"`: look up target `WorkflowState`. If `targetState.Goal != ""`, apply locked goal configuration; otherwise deactivate goal.
    - When handling `"exit"`: deactivate goal policy and unlock.
  - In `router-proxy/internal/transport/managed_http.go`:
    - Ensure manual workflow state assignment or clearing triggers goal synchronization.
- [ ] **3.4 Validation**: Run `go test -run 'TestWorkflow.*' ./router-proxy/internal/managed/...` and `./router-proxy/internal/transport/...`.

---

### Phase 4: Goal Lock Enforcement
*Goal*: Prevent worker agents from modifying or disabling state-mandated goals while active in a workflow state with a goal.

- [ ] **4.1 Test-First (TDD)**:
  - Add tests in `router-proxy/internal/managed/managed_goal_projection_test.go`:
    - Test worker calling `goal(action="set", goal="worker goal")` when agent is in a workflow state with a goal returns error:
      `goal is locked by active workflow state "<StateName>"`
    - Test worker calling `goal(action="off")` when agent is in a workflow state with a goal returns error:
      `goal is locked by active workflow state "<StateName>"`
    - Test worker calling `goal(action="status")` when locked is PERMITTED and returns active status.
    - Test worker calling `goal(action="set")` when in a workflow state without a goal (`state.Goal == ""`) is permitted.
    - Test worker calling `goal(action="set")` when not in any workflow state is permitted.
- [ ] **4.2 Enforcement Implementation**:
  - In `router-proxy/internal/managed/managed_goal_projection.go` (`executeManagedGoalControl`):
    - Check active workflow state of the agent.
    - If agent has active workflow state and `state.Goal != ""`:
      - If `invocation.Arguments["action"] == "set"` or `action == "off"`:
        ```go
        return RootControlHandlerResult{}, fmt.Errorf("goal is locked by active workflow state %q", state.Name)
        ```
      - If `action == "status"`: allow execution.
- [ ] **4.3 Validation**: Run `go test ./router-proxy/internal/managed/...` to verify goal lock and edge cases.

---

### Phase 5: Web UI
*Goal*: Support workflow state goals in the frontend, enabling operators to configure, edit, preview, and visualize goals on states.

- [ ] **5.1 Domain Types**:
  - In `web/router-ui-react/src/domain/workflow-states.ts`:
    - Add `goal?: string;` to `WorkflowState`.
    - Add `goal?: string;` to `WorkflowStateInput`.
- [ ] **5.2 Test-First (TDD)**:
  - Add tests in `web/router-ui-react/src/components/WorkflowStateNode.test.tsx`:
    - Renders `<span className="node-badge badge-goal" title={state.goal}><i className="fa-solid fa-flag" aria-hidden="true" /> Goal</span>` when `state.goal` is present.
    - Does not render badge when `state.goal` is empty or undefined.
  - Add tests in `web/router-ui-react/src/components/StateConfigModal.test.tsx`:
    - Shows "Configure Goal" button when `goal` is empty.
    - Clicking "Configure Goal" opens `GoalEditorDialog`.
    - When `goal` is set, displays preview box with goal text, "Edit Goal" button, and "Clear Goal" button.
    - Saving `GoalEditorDialog` updates draft state and emits updated `goal` in `onSave`.
    - Clicking "Clear Goal" sets `goal` to `""`.
- [ ] **5.3 Component Implementation**:
  - In `web/router-ui-react/src/components/WorkflowStateNode.tsx`:
    - Add goal badge rendering condition when `Boolean(state.goal)`.
  - In `web/router-ui-react/src/components/StateConfigModal.tsx`:
    - Initialize local `goal: string` from `state.goal ?? ""`.
    - Add goal management UI section reusing `GoalEditorDialog`.
    - Support save from `GoalEditorDialog` and clear action.
    - Pass `goal` in `updated` state to `onSave`.
  - In `web/router-ui-react/src/components/WorkflowStatesPage.tsx`:
    - Include `goal` in state creation/edit forms and state table/summary list items.
- [ ] **5.4 Validation**:
  - Run frontend test suite: `npm --prefix web/router-ui-react test`.
  - Run frontend build: `npm --prefix web/router-ui-react run build`.

---

### Phase 6: Verification & Robot Review
*Goal*: End-to-end integration validation across all layers, followed by robot code review.

- [ ] **6.1 End-to-End Behavioral Test**:
  - Add behavioral functional test in `router-proxy/internal/managed/`:
    - Create workflow with two states: "State A" (goal: "Fix bug") and "State B" (no goal).
    - Activate "State A" on agent -> verify agent goal is locked to "Fix bug".
    - Worker attempts to change goal -> verify rejected with exact error format.
    - Worker queries status -> verify permitted.
    - Agent transitions to "State B" -> verify goal is cleared and unlocked.
    - Worker calls `goal(action="set")` -> verify now permitted.
- [ ] **6.2 Full Test Suite & Linting**:
  - Backend: `go test -v -race ./router-proxy/...`
  - Frontend: `npm --prefix web/router-ui-react run test` and `npm --prefix web/router-ui-react run build`
- [ ] **6.3 Robot Review**:
  - Run robot review on all modified files using Wolfgang's reviewer persona.
  - Address any findings or polish.

---

## 3. Parallel Execution Opportunities

The implementation can be accelerated by executing independent workstreams in parallel:

```
[Track A: Backend]                               [Track B: Frontend]
Phase 1: DB & Store (099, CRUD, models)          Phase 5.1: Domain model update (workflow-states.ts)
           │                                                │
Phase 2: HTTP Transport Layer                    Phase 5.2 & 5.3: UI Components
           │                                     (WorkflowStateNode, StateConfigModal)
Phase 3 & 4: Runtime Sync & Goal Lock                       │
           │                                     Phase 5.4: Frontend tests & Vite build
           └───────────────────────┬────────────────────────┘
                                   ▼
                    Phase 6: E2E Verification & Review
```

1. **Phase 1 (Store) & Phase 5.1 (UI Domain Types)**:
   - Defining `goal?: string` in `workflow-states.ts` can be done concurrently with DB migration and Store CRUD.
2. **Phase 2 (HTTP) & Phase 5.2/5.3 (Web UI Components)**:
   - Web UI development (`WorkflowStateNode.tsx`, `StateConfigModal.tsx`) using mocked `WorkflowState` objects can run in parallel with HTTP transport endpoints (`workflow_states_http.go`).
3. **Phase 3 (Lifecycle) & Phase 4 (Lock Enforcement)**:
   - Transition lifecycle logic (`embedded_workflow_transition.go`) and worker tool control locking (`executeManagedGoalControl`) touch distinct code paths within `router-proxy/internal/managed/` and can be authored or tested concurrently.

---

## 4. TDD & Verification Checklists

Every phase strictly enforces write-tests-first:
- [ ] Write failing unit test for store validation and CRUD before implementing `099_workflow_state_goal.sql` and `workflow_states.go`.
- [ ] Write failing HTTP transport test before adding JSON parsing in `workflow_states_http.go`.
- [ ] Write failing lifecycle transition test before synchronizing goal config in `embedded_workflow_transition.go`.
- [ ] Write failing goal control test expecting `goal is locked by active workflow state "<StateName>"` before adding enforcement in `executeManagedGoalControl`.
- [ ] Write failing Vitest UI component tests before updating `StateConfigModal.tsx` and `WorkflowStateNode.tsx`.
- [ ] All tests must run with deterministic mocks, no external network calls or LLM API calls.
