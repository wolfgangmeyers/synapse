# Spec: Structured Workflow Schedules & Test Clock

- **ID**: `SPEC-WORKFLOWS-003`
- **Status**: `Active`
- **Domain**: `Workflows`
- **Target E2E Suite**: `tests/e2e/background-workflows.spec.ts`

---

## 1. Intent & User Outcome

Wolfgang and developers need an intuitive, reliable, and expressive way to schedule background workflows without wrestling with arcane cron syntax. Standard 5-part cron syntax cannot express common business schedules—such as triggering a workflow at 9:15 AM and 5:45 PM—without brittle workarounds, multiple separate cron jobs, or error-prone mental translation.

The Structured Workflow Schedules feature replaces cron expressions and raw interval milliseconds with a first-class structured JSON schedule model governed by two independent, orthogonal toggles:
1. **Days Cadence**: "Every day" vs. "Specific days of the week" (e.g., Monday through Friday).
2. **Time Cadence**: "At specific times" (an arbitrary chronological list of 24-hour clock times like `09:15` and `17:45`) vs. "Repeating interval" (every *N* minutes, hours, or days).

The router proxy authoritative backend calculates the exact upcoming execution time (`next_run_at`) using the user's configured IANA timezone, validates required workflow parameters, executes workflows via automated background workers upon schedule sweeps, and exposes a deterministic test clock for fast, non-flaky end-to-end testing without real-time sleeps.

---

## 2. Core Invariants

1. **No Cron Syntax in Public API or Storage**: Neither the router API nor the SQLite schema shall require or store 5-part cron strings. All schedule definitions are persisted in SQLite as validated `schedule_json` adhering to the `StructuredSchedule` schema.
2. **Orthogonal Cadence Dimensions**: The days cadence (`days_mode`: `every_day` | `specific_days`) and time cadence (`time_mode`: `specific_times` | `repeating_interval`) are strictly orthogonal. Any valid combination of days mode and time mode is supported.
3. **Authoritative Server-Side Calculation**: Clients never dictate future run times. The server computes `next_run_at` deterministically via `NextRunAt(spec, now)` based on the schedule's timezone, days of the week, and time specification. If `next_run_at` is omitted at creation/update, the server auto-populates it.
4. **Strict Workflow Variable Validation**: A schedule cannot be created or updated with missing or blank required workflow variables. Furthermore, at execution sweep time, variables are re-validated against the current workflow definition; if variables are missing, the run fails with clear diagnostic error recording without crashing or launching an incomplete worker.
5. **Deterministic Test Clock Advancement**: When running in test mode (`--test-clock`), the router time does not drift with real OS time. Calling `POST /v1/test/clock/advance` advances the simulated router clock and immediately triggers synchronous sweep execution, providing predictable, race-free E2E verification.

---

## 3. User Scenarios (Behavioral Contract)

### SCN-001: Schedule with specific times on specific days
- **User Goal**: Schedule a recurring workflow to run at 09:15 and 17:45 every weekday (Monday through Friday).
- **Given**:
  - Workflow `audit-wf` exists with no required parameters.
  - Current server time is `2026-11-02T08:00:00Z` (Monday morning).
- **When**:
  - The client creates a workflow schedule with:
    - `days_mode`: `"specific_days"`
    - `days`: `[1, 2, 3, 4, 5]` (Monday through Friday)
    - `time_mode`: `"specific_times"`
    - `times`: `["09:15", "17:45"]`
    - `timezone`: `"UTC"`
- **Then**:
  - The schedule is created with HTTP 201.
  - The server automatically calculates `next_run_at` as `2026-11-02T09:15:00Z`.
  - When the clock reaches `2026-11-02T09:15:00Z`, the workflow triggers and advances `next_run_at` to `2026-11-02T17:45:00Z`.
  - After the 17:45 run completes, `next_run_at` advances to `2026-11-03T09:15:00Z` (Tuesday).

### SCN-002: Schedule with specific times every day
- **User Goal**: Run a daily morning health check at 08:30 every day including weekends.
- **Given**:
  - Workflow `health-check` exists.
  - Current server time is `2026-11-02T09:00:00Z`.
- **When**:
  - The client creates a workflow schedule with:
    - `days_mode`: `"every_day"`
    - `time_mode`: `"specific_times"`
    - `times`: `["08:30"]`
    - `timezone`: `"UTC"`
- **Then**:
  - The schedule is accepted.
  - The server recognizes that 08:30 has already passed for today and calculates `next_run_at` as tomorrow, `2026-11-03T08:30:00Z`.

### SCN-003: Schedule with repeating interval on specific days
- **User Goal**: Execute a synchronization workflow every 2 hours, but only on Saturdays and Sundays.
- **Given**:
  - Workflow `sync-wf` exists.
  - Current server time is `2026-11-06T20:00:00Z` (Friday evening).
- **When**:
  - The client creates a workflow schedule with:
    - `days_mode`: `"specific_days"`
    - `days`: `[0, 6]` (0 = Sunday, 6 = Saturday)
    - `time_mode`: `"repeating_interval"`
    - `interval`: `{"value": 2, "unit": "hours"}`
    - `timezone`: `"UTC"`
- **Then**:
  - The schedule is accepted.
  - The server sets `next_run_at` to the start of the next eligible day: `2026-11-07T00:00:00Z` (Saturday midnight).
  - During Saturday and Sunday, each completed run advances `next_run_at` by 2 hours.
  - At the end of Sunday (`2026-11-08T22:00:00Z`), the subsequent run advances across the weekday gap to next Saturday `2026-11-14T00:00:00Z`.

### SCN-004: Schedule with repeating interval every day
- **User Goal**: Run a triage sweep every 15 minutes continuously across all days.
- **Given**:
  - Workflow `triage-wf` exists.
  - Current server time is `2026-11-02T10:00:00Z`.
- **When**:
  - The client creates a workflow schedule with:
    - `days_mode`: `"every_day"`
    - `time_mode`: `"repeating_interval"`
    - `interval`: `{"value": 15, "unit": "minutes"}`
    - `timezone`: `"UTC"`
- **Then**:
  - The server calculates `next_run_at` as `2026-11-02T10:15:00Z`.
  - Every 15 minutes, the schedule executes and recalculates the subsequent run time strictly 15 minutes later.

### SCN-005: Parameter and schedule validation
- **User Goal**: Prevent invalid or incomplete schedules from entering the system.
- **Given**:
  - Workflow `deploy-wf` requires variable `ENVIRONMENT`.
- **When**:
  - Case A: A client creates a schedule with `days_mode: "specific_days"` but an empty or missing `days` array.
  - Case B: A client creates a schedule with `time_mode: "specific_times"` but empty `times` or malformed time strings (e.g. `"25:99"`).
  - Case C: A client creates a schedule with `time_mode: "repeating_interval"` with `value: 0` or unsupported unit `"weeks"`.
  - Case D: A client creates a schedule with an invalid timezone string like `"Mars/Phobos"`.
  - Case E: A client creates a schedule with missing `variables: {}` omitting required `ENVIRONMENT`.
- **Then**:
  - For all cases A through E, the router rejects the request with HTTP 400 Bad Request.
  - A structured error response is returned indicating the exact validation error without persisting invalid records.

### SCN-006: Server-side automatic `next_run_at` calculation and update
- **User Goal**: Allow clients to submit schedules without having to compute local timestamps or handle timezone offsets.
- **Given**:
  - An existing schedule configured with timezone `"America/New_York"`.
- **When**:
  - The user updates the schedule times from `["09:00"]` to `["14:00"]` via `PATCH /v1/workflow-schedules/:id` without supplying a `next_run_at` field.
- **Then**:
  - The server evaluates the updated `StructuredSchedule` against the current clock and America/New_York timezone.
  - The server updates `next_run_at` automatically to the next upcoming 14:00 America/New_York occurrence formatted in ISO 8601 UTC.

### SCN-007: Scheduler sweep execution of due schedules
- **User Goal**: Execute scheduled workflows autonomously in the background when their due time arrives.
- **Given**:
  - An enabled schedule with `next_run_at <= now` pointing to workflow `report-wf`.
- **When**:
  - The router live sweep loop (`sweepWorkflowSchedules`) executes.
- **Then**:
  - A background workflow agent is launched with `is_background = 1`.
  - The schedule record is updated with:
    - `last_run_at` = current timestamp.
    - `last_run_status` = `"success"`.
    - `last_run_error` = `null`.
    - `next_run_at` = calculated subsequent execution timestamp via `NextRunAt`.
  - If the launch fails (e.g. invalid workflow), `last_run_status` is marked `"failed"` and `last_run_error` records the failure reason while still advancing `next_run_at`.

### SCN-008: Deterministic test clock advancement
- **User Goal**: Fast, reproducible E2E tests for scheduled workflows without `setTimeout` or real-time waiting.
- **Given**:
  - The router is running with `--test-clock`.
  - A schedule is configured to run at `2026-11-02T12:00:00Z` and current test clock is `2026-11-02T11:00:00Z`.
- **When**:
  - The test suite sends `POST /v1/test/clock/advance` with `{"delta_ms": 3600000}`.
- **Then**:
  - The router clock jumps forward by 1 hour to `12:00:00Z`.
  - The router synchronously invokes the sweep engine.
  - The due schedule executes immediately and the background worker is spawned within the same HTTP request cycle.

---

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `SCN-001` | Specific times on specific days | `tests/e2e/background-workflows.spec.ts` | `executes schedule on specific days at designated clock times` |
| `SCN-002` | Specific times every day | `tests/e2e/background-workflows.spec.ts` | `executes schedule daily at specified clock time` |
| `SCN-003` | Repeating interval on specific days | `tests/e2e/background-workflows.spec.ts` | `executes repeating interval restricted to specific days` |
| `SCN-004` | Repeating interval every day | `tests/e2e/background-workflows.spec.ts` | `executes repeating interval across consecutive days` |
| `SCN-005` | Parameter & schedule validation | `tests/e2e/background-workflows.spec.ts` | `rejects invalid structured schedules and missing workflow parameters` |
| `SCN-006` | Automatic next_run_at calculation | `tests/e2e/background-workflows.spec.ts` | `server auto-computes next_run_at upon create or update` |
| `SCN-007` | Scheduler sweep execution | `tests/e2e/background-workflows.spec.ts` | `sweep spawns background worker and advances next_run_at` |
| `SCN-008` | Test clock deterministic advance | `tests/e2e/background-workflows.spec.ts` | `test clock advancement synchronously triggers due schedule sweep` |
