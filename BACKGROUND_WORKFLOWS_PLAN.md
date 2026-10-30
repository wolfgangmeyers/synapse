# Background Workflows & Scheduled Execution Plan

## 1. Overview & Motivation
This plan defines the end-to-end design and implementation for **Background Workflows** and **Scheduled Execution** in `mecha-developer`.

Users and supervisor agents require the capability to execute state-machine workflows autonomously in the background—either launched dynamically on-demand or automatically on a schedule. Because workflows may depend on specific runtime variables (e.g. `COMPONENT`, `TARGET_ENVIRONMENT`, `PR_NUMBER`), any scheduled execution must strictly enforce that all required parameters are provided before triggering, rejecting incomplete configurations with deterministic validation errors.

In the Web UI, background workers must not clutter the primary agent list. Instead, users must be able to switch between standard agents and background workflow workers using dedicated tabs at the top of the sidebar agent list. Background workers must visually match standard worker cards (status dots, badges, action buttons), support active vs. completed filtering, and be automatically pruned 1 hour after completion in alignment with existing system cleanup policies. Navigation for managing background workflow definitions and recurring schedules is modeled after the Knowledge Base interface.

---

## 2. Core Requirements (Traceability Matrix)

- **REQ-1 (Durable Background Execution Model)**:
  - Add an explicit `is_background INTEGER NOT NULL DEFAULT 0 CHECK (is_background IN (0, 1))` column to `managed_agents` via SQLite migration.
  - A background workflow worker is a top-level managed agent launched with `is_background = 1`, initialized with the target workflow, its entry state, and validated input variables.
  - Background workers have full autonomous execution, goal locking per active state, variable expansion in instructions and goals, and access to workflow transition tools (`mecha_workflow_transition`).

- **REQ-2 (Dynamic Launch API & Tooling)**:
  - Provide a REST endpoint `POST /v1/workflows/:id/run` to launch a background workflow worker dynamically.
  - Provide an embedded agent tool (`mecha_workflow_run`) enabling supervisor and worker agents to kick off background workflows programmatically.
  - Validate required parameters on launch: return HTTP 400 with missing parameter details if required variables are omitted or empty.

- **REQ-3 (Scheduled Execution Engine & Strict Parameter Validation)**:
  - Add a persistent `workflow_schedules` table storing schedules:
    - `id TEXT PRIMARY KEY`
    - `workflow_id TEXT NOT NULL REFERENCES workflows(id) ON DELETE CASCADE`
    - `name TEXT NOT NULL`
    - `enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1))`
    - `schedule_type TEXT NOT NULL CHECK (schedule_type IN ('interval', 'cron'))`
    - `interval_ms INTEGER`
    - `cron_expression TEXT`
    - `variables_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(variables_json))`
    - `next_run_at TEXT NOT NULL`
    - `last_run_at TEXT`
    - `last_run_status TEXT`
    - `last_run_error TEXT`
    - `created_at TEXT NOT NULL`, `updated_at TEXT NOT NULL`
  - REST endpoints for schedule management:
    - `GET /v1/workflow-schedules` (list with optional `workflow_id` filter)
    - `POST /v1/workflow-schedules` (create with strict variable validation)
    - `GET /v1/workflow-schedules/:id` (fetch details)
    - `PATCH /v1/workflow-schedules/:id` (update schedule/parameters with strict validation)
    - `DELETE /v1/workflow-schedules/:id` (delete schedule)
  - Embedded agent tool (`mecha_workflow_schedule`) for programmatic schedule management.
  - Integrated sweep loop in `router-proxy/internal/scheduler/`:
    - Checks due enabled schedules (`next_run_at <= now`).
    - Validates that every variable marked `required: true` in the associated workflow is present and non-empty in `variables_json`.
    - If required variables are missing: updates schedule `last_run_status = 'failed'`, records `last_run_error = "missing required workflow parameter(s): ..."`, advances `next_run_at`, and refuses to launch the worker.
    - If valid: triggers `LaunchBackgroundWorkflow`, updates `last_run_status = 'success'`, and advances `next_run_at`.

- **REQ-4 (1-Hour Cleanup Retention)**:
  - Completed background workers (`desired_state = 'terminated'` or `lifecycle_state = 'terminated'`) must be automatically purged 1 hour after reaching terminal state.
  - Integrate background worker cleanup into router store sweep (`SweepExpiredBackgroundAgents(ctx, 1*time.Hour)`).

- **REQ-5 (Web UI Sidebar Tabs & Isolation)**:
  - Background workers must not appear in the primary "Agents" list.
  - At the top of the sidebar agent list, render view tabs:
    - **Agents** (standard active/managed agents)
    - **Background** (background workflow workers) with an active badge counter.
  - In the Background tab:
    - Provide a segmented toggle control: **Active** vs **Completed**.
    - Render background workers using the exact same card layout, styling, and action controls as standard workers (`AgentRow` styling, status indicators, workflow badge, terminate button).
    - Selecting a background worker opens its continuous conversation timeline in the main view.

- **REQ-6 (Web UI Management Navigation Modeled Like KB Interface)**:
  - Add a dedicated management modal/interface modeled after the Knowledge Base dialog:
    - List view showing configured workflow schedules across workflows.
    - Clean separation between List view and Add/Edit Form view.
    - Variable parameter editor with clear `*` required indicators.
    - Prevention of submission if required parameters are missing or blank.
    - Dark glassmorphic styling, neon accents, and `.icon-btn-round` action buttons.

---

## 3. Data Architecture & Schema Changes

### Migration `116_background_workflows_and_schedules.sql`
1. Alter `managed_agents`:
   ```sql
   ALTER TABLE managed_agents ADD COLUMN is_background INTEGER NOT NULL DEFAULT 0 CHECK (is_background IN (0, 1));
   CREATE INDEX IF NOT EXISTS idx_managed_agents_background ON managed_agents(is_background, lifecycle_state);
   ```

2. Create `workflow_schedules`:
   ```sql
   CREATE TABLE IF NOT EXISTS workflow_schedules (
     id TEXT PRIMARY KEY,
     workflow_id TEXT NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
     name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 256),
     enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
     schedule_type TEXT NOT NULL CHECK (schedule_type IN ('interval', 'cron')),
     interval_ms INTEGER,
     cron_expression TEXT,
     variables_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(variables_json)),
     target_cwd TEXT NOT NULL DEFAULT '',
     model_provider TEXT NOT NULL DEFAULT '',
     model_id TEXT NOT NULL DEFAULT '',
     next_run_at TEXT NOT NULL,
     last_run_at TEXT,
     last_run_status TEXT,
     last_run_error TEXT,
     created_at TEXT NOT NULL,
     updated_at TEXT NOT NULL
   );

   CREATE INDEX IF NOT EXISTS idx_workflow_schedules_due ON workflow_schedules(enabled, next_run_at);
   CREATE INDEX IF NOT EXISTS idx_workflow_schedules_workflow ON workflow_schedules(workflow_id);
   ```

---

## 4. Backend Implementation (`router-proxy`)

1. **Store Layer (`router-proxy/internal/store/`)**:
   - `workflow_schedules.go`:
     - Model `WorkflowScheduleRecord`.
     - CRUD: `CreateWorkflowSchedule`, `GetWorkflowSchedule`, `ListWorkflowSchedules`, `UpdateWorkflowSchedule`, `DeleteWorkflowSchedule`.
     - `GetDueWorkflowSchedules(ctx, now)`: Fetches enabled schedules where `next_run_at <= now`.
     - `AdvanceWorkflowSchedule(ctx, id, nextRunAt, status, errStr)`: Updates run metrics and next run time.
     - `ValidateWorkflowVariables(workflowVars []WorkflowVariableDefinition, inputVars map[string]string) error`: Verifies that all required variables exist and are non-empty.
   - `managed.go`:
     - Update `ManagedAgentRecord` and queries to include `is_background`.
     - `ListManagedAgents`: support filtering by `is_background`.
     - `PruneExpiredBackgroundAgents(ctx, cutoff time.Time)`: Cleans up terminated background agents older than 1 hour.

2. **Runner & Workflow Engine (`router-proxy/internal/managed/`)**:
   - `embedded_workflow_runner.go`:
     - `LaunchBackgroundWorkflow(ctx, req LaunchBackgroundWorkflowRequest) (*ManagedAgentRecord, error)`:
       - Validates workflow existence and entry state.
       - Validates required variables against `workflow.variables_json`.
       - Generates deterministic background agent name (e.g. `bg-<workflow-name>-<random>`).
       - Creates managed agent with `is_background = true`.
       - Binds active workflow and entry state with expanded variables.
       - Injects entry state instruction turn and initiates goal loop.
       - Configures auto-termination when agent exits workflow.

3. **Scheduler Integration (`router-proxy/internal/transport/live.go` & `scheduler.go`)**:
   - Use standard `github.com/robfig/cron/v3` for robust, deterministic cron expression calculation.
   - Add `sweepWorkflowSchedules(ctx, now)` to live router sweep loop:
     - Iterates over due schedules.
     - Validates required parameters.
     - If invalid: records failure with descriptive error, advances `next_run_at`.
     - If valid: launches background worker via `LaunchBackgroundWorkflow`, advances `next_run_at`.
   - Add background agent pruning (`PruneExpiredBackgroundAgents(ctx, 1*time.Hour)`):
     - Calls `s.bumpManagedProjectionVersion(ctx)` so Web UI clients receive instant WebSocket timeline updates upon pruning.

4. **HTTP Transport Layer (`router-proxy/internal/transport/`)**:
   - `POST /v1/workflows/:id/run`: Dynamic background launch.
   - `/v1/workflow-schedules` REST endpoints (list, create, read, update, delete).
   - `/v1/managed-agents`: Include `is_background` in serialization, support `?is_background=true|false`.

5. **Agent Tools (`router-proxy/internal/managed/`)**:
   - `mecha_workflow_run`: Trigger dynamic run.
   - `mecha_workflow_schedule`: Create/manage schedules.

---

## 5. Frontend Implementation (`web/router-ui-react`)

1. **API Client & State Projections**:
   - Extend `RouterHttpClient` in `router-http-client.ts`:
     - `runBackgroundWorkflow(workflowId, variables, options)`
     - `listWorkflowSchedules(workflowId?)`
     - `createWorkflowSchedule(schedule)`
     - `updateWorkflowSchedule(id, updates)`
     - `deleteWorkflowSchedule(id)`
   - Update `ManagedAgent` type in `types.ts` to include `is_background?: boolean`.
   - Update `agent-projection.ts` to cleanly partition standard agents and background workers.

2. **Sidebar Tabs (`Sidebar.tsx`)**:
   - Place tabs at the top of the agent list:
     - `Agents` tab (standard root agents).
     - `Background` tab (shows active background worker count badge).
   - In `Background` tab mode:
     - Render segmented toggle: `Active` vs `Completed`.
     - Render background workers matching `AgentRow` styling and actions.
     - Hide background workers from the standard `Agents` tab view.

3. **Background Workflows & Schedules Management View**:
   - Modeled cleanly like the Knowledge Base interface (`WorkflowSchedulesDialog.tsx` or dedicated sub-view in `WorkflowsPage.tsx`).
   - Clean List View displaying configured schedules with status badges, next run timestamp, cron/interval details, and inline trigger action.
   - Dedicated Add/Edit Schedule Form adhering to list/form separation.
   - Dynamic parameter inputs with required asterisks (`*`) and validation blocking submission if required parameters are missing.

---

## 6. Behavioral Specification & Automated Test Plan

1. **Behavioral Spec File**:
   - Create `docs/specs/SPEC-BACKGROUND-WORKFLOWS.md` following `docs/behavioral-specification-standard.md`.
   - Register the spec in `docs/specs/INDEX.md`.
   - Include exhaustive scenarios:
     - *Scenario 1: Dynamic launch with valid variables succeeds.*
     - *Scenario 2: Dynamic launch with missing required variables fails with HTTP 400.*
     - *Scenario 3: Scheduled workflow fires on time with valid variables.*
     - *Scenario 4: Scheduled workflow fails to execute and reports error if required variables are missing.*
     - *Scenario 5: UI agent list separation and sidebar tab switching.*
     - *Scenario 6: Active vs Completed filtering of background workers.*
     - *Scenario 7: Completed background workers pruned after 1 hour.*

2. **Fast Frictionless E2E Tests**:
   - Create `tests/e2e/background-workflows.spec.ts`.
   - Exercise full system boundaries: SQLite store, Go router REST & WebSocket, Headless Browser UI, mock LLM execution.
   - Verify sidebar tab switching, active/completed filtering, parameter validation errors, and scheduled runs.
