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
4. Keep the main conversation/agent list clean and uncluttered by background workers while still providing full observability, active/completed filtering, and a standard 1-hour cleanup lifecycle.

This specification provides the behavioral contract for running background workflows dynamically and on schedule, parameter validation, UI sidebar tab isolation, and 1-hour retention cleanup.

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

- **REQ-4 (1-Hour Cleanup Retention)**:
  - When a background worker reaches a terminal state (`terminated` or `failed`), it remains queryable and visible in the Completed tab for 1 hour.
  - Router sweeps must prune background worker records where `terminal_at < now - 1h` and bump the managed agent projection version so connected UIs update automatically.

- **REQ-5 (UI Tab Isolation & Worker Consistency)**:
  - Background workers must not appear in the standard "Agents" list.
  - At the top of the sidebar agent list, tabs allow switching between "Agents" and "Background" (with an active counter badge).
  - Inside the Background view, a segmented control allows filtering between "Active" and "Completed".
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
- **Given** 2 standard agents and 1 background worker running.
- **When** viewing the "Agents" tab in the sidebar.
- **Then** only the 2 standard agents are visible; the background worker is not listed.
- **When** switching to the "Background" tab in the sidebar.
- **Then** only the background worker is visible under "Active".
- **And** clicking the background worker displays its active conversation timeline in the main panel.

### Scenario 6: 1-Hour Automatic Cleanup of Completed Background Workers
- **Given** a background worker that terminated 65 minutes ago.
- **When** the router maintenance sweep executes.
- **Then** the terminated background worker is pruned from the database.
- **And** the UI removes it from the "Completed" background workers list.

---

## 4. Verification & Automated Test Mapping

- `tests/e2e/background-workflows.spec.ts`:
  - `test("dynamic launch validates required parameters and launches background agent")`
  - `test("scheduled workflow sweep validates required parameters and advances run time")`
  - `test("UI sidebar tabs switch between standard agents and background workers")`
  - `test("UI background tab filters active vs completed workers")`
  - `test("workflow schedules management dialog enforces required parameters and manages schedules")`
