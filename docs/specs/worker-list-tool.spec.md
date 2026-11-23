# Spec: Worker List Administrative Tool

- **ID**: `SPEC-WORKER-LIST-001`
- **Status**: `Active`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/worker-tool-actions.spec.ts`

## 1. Intent & User Outcome

Supervisor agents and administrative workers require a structured, native capability to discover, inspect, and monitor managed workers across the router. The `worker_list` administrative tool gives agents direct visibility into the fleet of managed workers without having to scrape raw database tables or execute external CLI tools.

Agents can filter the worker fleet by lifecycle and activity status (`all`, `active`, `running`, `stopped`). Each returned record provides complete operational context, including identity, runtime states, working directory, model assignment, active goal policy configuration and revision status, git worktree isolation details, timestamps, and live uptime calculation.

## 2. Core Invariants

1. **Administrative Access Control**: `worker_list` belongs to the `'Worker Administration'` tool category and is registered with `IsAdmin: true` and `DefaultEnabled: false` (`enabled = 0` in `managed_tool_defaults`). It is disabled by default for all agents and is only available when explicitly granted through tool defaults or agent-level tool overrides (`PUT /v1/managed-agents/:id/tools`).
2. **Closed Schema**: The tool input schema strictly accepts an optional `status` string property enum: `"all"`, `"active"`, `"running"`, `"stopped"`. Additional properties are disallowed (`AdditionalAllowed = false`). Any unrecognized status string or unexpected arguments are rejected with a descriptive error.
3. **Default Status**: When `status` is omitted or provided as an empty string, it defaults to `"all"`.
4. **Deterministic Status Filtering**:
   - `"all"`: Matches all managed agents in the store.
   - `"running"`: Matches agents where `lifecycle_state == "running"`.
   - `"active"`: Matches agents where `lifecycle_state` is `"running"`, `"starting"`, or `"restarting"`, or `activity_state == "active"`.
   - `"stopped"`: Matches agents where `lifecycle_state` is `"stopped"`, `"failed"`, or `"terminating"`, or `desired_state == "stopped"`.
5. **Record Hydration**:
   - **Identity & Lifecycle**: `agent_id`, `name`, `lifecycle_state`, `activity_state`, `desired_state`, `cwd`, and `model` (`provider`, `id`).
   - **Goal Policy**: Hydrated from `ManagedGoalPolicyFor`. If configured, includes `enabled`, `goal`, `projection_state`, `desired_revision`, and `applied_revision`. Serialized as `null` if no policy exists.
   - **Worktree Metadata**: `worktree` (boolean), `worktree_base_path` (string), and `worktree_branch` (string).
   - **Timestamps**: `created_at` (string) and `started_at` (string).
   - **Uptime Metric**: `uptime_seconds` (integer) computed from `started_at` relative to current router time when the worker is running/active. If the worker has not started, `started_at` is empty or unparseable, or the worker is stopped, `uptime_seconds` is `0`. Negative diffs due to clock skew are clamped to `0`.
6. **Response Envelope**: Output is structured JSON with `status`, `filter`, `total`, and `workers` array:
   ```json
   {
     "status": "ok",
     "filter": "all",
     "total": 2,
     "workers": [ ... ]
   }
   ```

## 3. User Scenarios (Behavioral Contract)

### Scenario: [WKL-001] List all managed workers by default
- **User Goal**: Inspect all managed workers across the router without specifying filter criteria.
- **Given**:
  - Multiple managed workers exist in various lifecycle states (e.g., running, stopped).
  - The caller agent has been granted the `worker_list` administrative tool.
- **When**:
  - The caller executes `worker_list` with `{}` or `{ "status": "all" }`.
- **Then**:
  - The tool returns `{ "status": "ok", "filter": "all", "total": N, "workers": [...] }`.
  - All managed workers are present in the `workers` list.
  - Each worker record contains identity, lifecycle, activity, cwd, model, goal, worktree, and timestamp fields.

### Scenario: [WKL-002] Filter workers by status
- **User Goal**: Query only workers matching a specific operational lifecycle or activity state.
- **Given**:
  - Worker A is `running` and `active`.
  - Worker B is `stopped` with `desired_state == "stopped"`.
  - The caller agent has `worker_list` enabled.
- **When**:
  - The caller executes `worker_list` with `{ "status": "running" }`.
- **Then**:
  - The response filter is `"running"`.
  - Only Worker A is included; Worker B is excluded.
- **When**:
  - The caller executes `worker_list` with `{ "status": "stopped" }`.
- **Then**:
  - The response filter is `"stopped"`.
  - Only Worker B is included; Worker A is excluded.

### Scenario: [WKL-003] Hydrate goal policy, worktree metadata, and uptime
- **User Goal**: View detailed goal state, git worktree details, and uptime for active workers.
- **Given**:
  - Worker A has an active goal configured (`"Implement feature X"`), isolated worktree enabled (`worktree: true`, `worktree_base_path: "/repo"`, `worktree_branch: "feat-x"`), and `started_at` set 120 seconds in the past.
  - Worker B has no goal policy configured and no worktree.
- **When**:
  - The caller executes `worker_list`.
- **Then**:
  - Worker A's entry contains `goal.enabled == true`, `goal.goal == "Implement feature X"`, `worktree == true`, `worktree_base_path == "/repo"`, `worktree_branch == "feat-x"`, and `uptime_seconds >= 120`.
  - Worker B's entry contains `goal == null`, `worktree == false`, `worktree_base_path == ""`, and `worktree_branch == ""`.

### Scenario: [WKL-004] Reject invalid status filter or unexpected arguments
- **User Goal**: Ensure invalid tool invocations fail fast with descriptive errors.
- **Given**:
  - The caller agent has `worker_list` enabled.
- **When**:
  - The caller executes `worker_list` with `{ "status": "invalid_status" }`.
- **Then**:
  - Execution fails with an error indicating `"invalid_status"` is not a valid status filter.

### Scenario: [WKL-005] Disabled by default access control
- **User Goal**: Prevent unprivileged agents from executing administrative worker discovery.
- **Given**:
  - A regular managed worker is spawned without administrative privileges.
- **When**:
  - The agent's available tools are resolved by the router.
- **Then**:
  - `worker_list` is filtered out and not present in the agent's tool catalog.

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `WKL-001` | List all managed workers by default | `tests/e2e/worker-tool-actions.spec.ts` | `lists all managed workers with complete hydration by default` |
| `WKL-002` | Filter workers by status | `tests/e2e/worker-tool-actions.spec.ts` | `filters workers by running and stopped status` |
| `WKL-003` | Hydrate goal policy, worktree, and uptime | `tests/e2e/worker-tool-actions.spec.ts` | `hydrates goal policy, worktree metadata, and uptime metrics` |
| `WKL-004` | Reject invalid status filter | `tests/e2e/worker-tool-actions.spec.ts` | `rejects invalid status filter with error` |
| `WKL-005` | Disabled by default access control | `tests/e2e/worker-tool-actions.spec.ts` | `omits worker_list from unprivileged agent tools` |
