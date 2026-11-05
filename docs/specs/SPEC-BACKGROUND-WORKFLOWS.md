# SPEC-BACKGROUND-WORKFLOWS-001: Background Workflows & Scheduled Execution

- **Specification ID**: `SPEC-BACKGROUND-WORKFLOWS-001`
- **Status**: `Active`
- **Authors**: Wolfgang & Embedded Agent (`Background-processes`)
- **Date**: 2026-10-30
- **Target Delivery**: `mecha-developer` main branch

---

## 1. Problem / Motivation

Autonomous workflow execution in `mecha-developer` currently requires an existing agent session to be explicitly assigned a workflow. When operations (such as recurring tests, CI verification, nightly code health sweeps, or scheduled triages) need to execute, there is no mechanism to:
1. Dynamically spawn a dedicated background worker to execute a workflow independently from beginning to completion.
2. Schedule workflows to run at predetermined intervals or cron times.
3. Strictly validate required runtime parameters for scheduled workflows, leading to potential silent execution failures.
4. Keep the main conversation/agent list clean and uncluttered by background workers while still providing full observability, a combined active/completed view with active workers first and no toggle, and automatic age-based termination.

This specification provides the behavioral contract for running background workflows dynamically and on schedule, parameter validation, UI sidebar tab isolation, and automatic age-based termination.

---

## 2. Requirements & System Invariants

- **REQ-1 (Durable Background Execution Model)**:
  - The SQLite table `managed_agents` must include column `is_background INTEGER NOT NULL DEFAULT 0 CHECK (is_background IN (0, 1))`.
  - A background workflow worker is a managed agent with `is_background = 1`, bound to a workflow ID and entry state.
  - Background workers execute autonomously, honoring workflow state entry instructions, goal locking, and transitions.

- **REQ-2 (Dynamic Launch API & Validation)**:
  - `POST /v1/workflows/:id/run` accepts `{ variables?: Record<string, string>, cwd?: string, model?: { provider: string, id: string } }`.
  - If the target workflow defines variables marked `required: true` and any required variable is omitted or whitespace-only, the router must reject the request with HTTP 400 Bad Request and detail the missing variable names.
  - On valid request, returns HTTP 201 Created with `{ agent_id: string, name: string, status: string, workflow_id: string }`.

- **REQ-3 (Scheduled Execution Engine & Strict Parameter Validation)**:
  - The database must persist `workflow_schedules` records with interval or cron configuration and required input variables.
  - At schedule creation/update time (`POST`/`PATCH /v1/workflow-schedules`), if required variables are missing or blank, the request must fail with HTTP 400.
  - At scheduled run sweep time, the scheduler must re-validate required parameters. If any required variable is missing (e.g. if the workflow definition was updated after schedule creation), the scheduler must:
    - NOT spawn a worker.
    - Set schedule `last_run_status = 'failed'`.
    - Record `last_run_error = "missing required workflow parameter(s): <names>"`.
    - Advance `next_run_at` to the subsequent scheduled time.

- **Historical expectation, not implemented**: The original specification expected completed background workers to remain visible for one hour and then be pruned. `PruneExpiredBackgroundAgents` exists in the store but has no production caller; it does not establish automatic one-hour retention or cleanup.

- **REQ-4 (Automatic Background Termination)**:
  - On startup and every minute, the router checks all background workers. On a pass at or after 24 hours since creation, it requests the existing managed worker termination, regardless of activity, progress, sleep, or lifecycle state.
  - The normal termination path owns cancellation, cleanup, errors and UI updates. Admission and recorded cleanup failures are logged; eligible failures are retried on later sweeps. A stalled or failed termination has no completion deadline. Ordinary agents and schedule definitions are unaffected. No separate post-termination retention is promised.

- **REQ-5 (UI Tab Isolation & Worker Consistency)**:
  - Background workers must not appear in the standard "Agents" list.
  - At the top of the sidebar agent list, tabs allow switching between "Agents" and "Background" (with an active counter badge).
  - The Background view shows active and completed workers together, with active workers first and no segmented toggle.
  - Background worker items must render using the standard worker card format (`AgentRow` styling, status indicators, badges, and action buttons).
  - Clicking a background worker selects it and displays its full timeline in the conversation view.

- **REQ-6 (Management Interface Modeled After Knowledge Base)**:
  - A dedicated management dialog/view for Workflow Schedules modeled after `KnowledgeBaseDialog`.
  - Clean separation between List View (configured schedules, last run status, next run timestamp) and Form View (Add/Edit schedule).
  - Visual indicators (`*`) for required parameters, disabling form submission until all required parameters are provided.

---

## 3. Behavioral Scenarios & Contracts

### Scenario 1: Dynamic Background Workflow Launch (Happy Path)
- **Given** a workflow "Code Audit" with required variable `REPOSITORY` and optional variable `BRANCH`.
- **When** a user or agent posts to `POST /v1/workflows/:id/run` with `variables: { "REPOSITORY": "mecha-developer" }`.
- **Then** the router responds with HTTP 201 Created and an agent ID `bg-code-audit-...`.
- **And** a new managed agent is created with `is_background = 1`, `active_workflow_id = :id`, and the initial entry instructions expanded.

### Scenario 2: Dynamic Background Workflow Launch (Missing Required Parameter)
- **Given** the "Code Audit" workflow with required variable `REPOSITORY`.
- **When** a client posts to `POST /v1/workflows/:id/run` with `variables: {}`.
- **Then** the router responds with HTTP 400 Bad Request.
- **And** the response payload contains `error: "missing required workflow parameter: REPOSITORY"`.
- **And** no agent is launched.

### Scenario 3: Scheduled Background Workflow Execution (Happy Path)
- **Given** an enabled schedule for "Code Audit" configured with `interval_ms: 60000`, `variables: { "REPOSITORY": "mecha-developer" }`, and `next_run_at <= now`.
- **When** the scheduler sweep runs.
- **Then** the scheduler launches a background worker with `is_background = 1`.
- **And** advances `next_run_at` to `now + 60000ms`.
- **And** records `last_run_status = "success"`.

### Scenario 4: Scheduled Background Workflow Execution (Missing Required Parameter at Sweep)
- **Given** an enabled schedule for "Code Audit" where `REPOSITORY` is missing in `variables_json`, and `next_run_at <= now`.
- **When** the scheduler sweep runs.
- **Then** the scheduler does NOT launch an agent.
- **And** records `last_run_status = "failed"` and `last_run_error = "missing required workflow parameter(s): REPOSITORY"`.
- **And** advances `next_run_at` to the next interval.

### Scenario 5: Web UI Sidebar Tabs & View Isolation
- **Given** 2 standard agents, 1 active background worker, and 1 completed background worker.
- **When** viewing the "Agents" tab in the sidebar.
- **Then** only the 2 standard agents are visible; neither background worker is listed.
- **When** switching to the "Background" tab in the sidebar.
- **Then** both background workers are visible together, with the active worker first and no segmented toggle.
- **And** clicking the active background worker displays its conversation timeline in the main panel.

### Scenario 6: Automatic termination by age
- **Given** background workers created less than and at least 24 hours ago, including active and sleeping workers, and an ordinary agent of the same age.
- **When** router maintenance runs, including after restart.
- **Then** each old background worker is sent through the normal worker termination path; younger workers, ordinary agents and schedules are untouched. A failed attempt is retried on a later sweep.

---

## 4. Verification & Automated Test Mapping

- `tests/e2e/background-workflows.spec.ts` (partial coverage; test titles use their own scenario numbering):
  - `test("Scenario 1 & 2: Dynamic Background Workflow Launch and Missing Required Variables Validation")`: exercises valid launch, missing/blank inputs, and tab isolation for one standard agent and one active background worker.
  - `test("Scenario 3: Create, list, edit, and trigger a workflow schedule via /v1/workflow-schedules")`: exercises schedule API creation validation, list/edit, a successful scheduled run, and the schedule list UI.
  - `test("SCN-001 & SCN-002: Specific Days and Specific Times schedule fires and UI creation workflow via Workflows window")`: exercises structured schedule creation and a successful scheduled run.
  - `test("Scenario 4: Background tab displays active background workers followed by completed workers without a segmented toggle")`: checks both workers are visible and active precedes completed; its no-toggle assertion only checks the selector `.segmented-toggle-group[aria-label="Background worker state"]`.
  - These tests do not cover Scenario 5's full Agents-tab isolation with both active and completed background workers, or selecting a worker to display its timeline. The selector-specific assertion does not establish that all possible active/completed toggles are absent. This mapping does not claim full E2E acceptance; in particular, scheduled sweep-time missing-parameter handling (Scenario 4) is not established by these tests.

- `tests/e2e/background-expiry.spec.ts`: startup catch-up through real router, proxy, SQLite and worker termination; younger background worker and workflow definition remain.
- `router-proxy/internal/managed/background_expiry_test.go`: inclusive age cutoff, active/sleeping/failed states, ordinary workers and invalid timestamps.

- `tests/e2e/background-cleanup.spec.ts`: periodic and startup age sweeps; `background-scheduled-expiry.spec.ts`: scheduled expiry and later launch; `background-active-retry.spec.ts`: dispatch while active, failed admission and retry after release. Full cleanup acceptance remains Draft in `background-worker-cleanup.spec.md`.
