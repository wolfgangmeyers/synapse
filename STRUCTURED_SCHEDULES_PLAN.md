# Implementation Plan: Structured Schedules & Router Test Clock

## 1. Overview & Context

Wolfgang requested ditching cron syntax in favor of an intuitive, flexible, structured JSON schedule object and clean scheduling UX.

Current limitations with cron:
- Standard 5-part cron syntax cannot represent multiple times with differing minutes (e.g., 9:15 AM and 5:45 PM).
- It creates high mental overhead and requires parsing logic with brittle edge cases.
- In the router backend, cron was never actually parsed; the backend merely queries SQLite for `WHERE enabled = 1 AND next_run_at <= ?`.

This plan outlines:
1. Replacing cron and legacy interval columns with a first-class **Structured JSON Schedule Object** supporting two independent, orthogonal toggles:
   - **Days Cadence**: "Every day" vs. "Specific days of the week" (with day pills `M T W T F S S` and presets).
   - **Time Cadence**: "At specific times" (arbitrary list of clock times e.g. `09:15`, `17:45`) vs. "Repeating interval" (every `X` minutes/hours/days).
2. Implementing authoritative server-side `NextRunAt(spec, now)` calculation in Go.
3. Wiring automated schedule execution into the router proxy live sweep loop (`sweepWorkflowSchedules`).
4. Introducing a clean **Test Clock Abstraction** (`--test-clock` and `POST /v1/test/clock/advance`) that synchronously executes `r.sweep()` to enable fast, frictionless, non-flaky E2E testing of time-based schedule executions.
5. Cleaning up all dead code, brittle cron regexes, and legacy fields.

---

## 2. Structured Schedule Specification

### 2.1 JSON Schema

```json
{
  "days_mode": "every_day" | "specific_days",
  "days": [1, 2, 3, 4, 5],
  "time_mode": "specific_times" | "repeating_interval",
  "times": ["09:15", "17:45"],
  "interval": {
    "value": 2,
    "unit": "minutes" | "hours" | "days"
  },
  "timezone": "America/Los_Angeles"
}
```

- `days`: Array of integers `0..6` where `0 = Sunday, 1 = Monday, ..., 6 = Saturday`. Required if `days_mode == "specific_days"`.
- `times`: Array of 24-hour `"HH:MM"` strings, sorted chronologically. Required if `time_mode == "specific_times"`.
- `interval`: Object with `value` (positive integer) and `unit` (`"minutes" | "hours" | "days"`). Required if `time_mode == "repeating_interval"`.
- `timezone`: IANA time zone identifier (e.g. `"America/Los_Angeles"`). Defaults to `"UTC"` if omitted or local browser time zone when authored in UI.

### 2.2 Canonical Examples

1. **Specific times on specific days**:
   ```json
   {
     "days_mode": "specific_days",
     "days": [1, 2, 3, 4, 5],
     "time_mode": "specific_times",
     "times": ["09:15", "17:45"],
     "timezone": "UTC"
   }
   ```
2. **Specific times every day**:
   ```json
   {
     "days_mode": "every_day",
     "time_mode": "specific_times",
     "times": ["08:30"],
     "timezone": "America/New_York"
   }
   ```
3. **Repeating interval on specific days**:
   ```json
   {
     "days_mode": "specific_days",
     "days": [1, 2, 3, 4, 5],
     "time_mode": "repeating_interval",
     "interval": { "value": 2, "unit": "hours" },
     "timezone": "UTC"
   }
   ```
4. **Repeating interval every day**:
   ```json
   {
     "days_mode": "every_day",
     "time_mode": "repeating_interval",
     "interval": { "value": 15, "unit": "minutes" },
     "timezone": "UTC"
   }
   ```

---

## 3. Architecture & Implementation Phases

### Phase 1: Behavioral Specification Backfill
- Author `docs/specs/SPEC-STRUCTURED-WORKFLOW-SCHEDULES.md` detailing:
  - User Scenarios for all 4 toggle permutations.
  - Required validation errors for missing days, times, or intervals.
  - Automatic calculation of `next_run_at`.
  - Schedule execution via live router sweeps.
  - Test clock manipulation in test environments.
- Register `SPEC-STRUCTURED-WORKFLOW-SCHEDULES.md` in `docs/specs/INDEX.md`.

### Phase 2: Database Migration & Go Store
- **Migration 119**: `119_structured_workflow_schedules.sql`:
  - Recreates `workflow_schedules` to clean out legacy columns (`interval_ms`, `cron_expression`) and the `schedule_type CHECK` constraint.
  - New schema stores `schedule_json TEXT NOT NULL`.
  - Migrates any existing schedules to valid JSON format.
- **Go Store (`router-proxy/internal/store/workflow_schedules.go`)**:
  - Define Go structs: `StructuredSchedule`, `IntervalSpec`.
  - Implement validation: `ValidateStructuredSchedule(spec) error`.
  - Implement authoritative `NextRunAt(spec StructuredSchedule, now time.Time) (time.Time, error)` handling timezones, day filtering, time lists, and intervals.
  - Update `CreateWorkflowSchedule` and `UpdateWorkflowSchedule` to compute `next_run_at` automatically using `NextRunAt` when not overridden.

### Phase 3: Live Sweep Execution & Runner Integration
- **Router Live Runtime (`router-proxy/internal/transport/live.go`)**:
  - Implement `sweepWorkflowSchedules(ctx context.Context)`:
    1. Query due enabled schedules: `store.GetDueWorkflowSchedules(ctx, now)`.
    2. For each due schedule:
       - Spawn background workflow worker using `LaunchBackgroundWorkflow`.
       - Compute next run timestamp: `nextRunAt = NextRunAt(schedule.Spec, now)`.
       - Update record with `store.AdvanceWorkflowSchedule(ctx, schedule.ID, nextRunAt, status, err)`.
  - Add `sweepWorkflowSchedules` to `Router.sweep()` ticker alongside `sweepReminders` and `sweepLiveState`.

### Phase 4: Router Test Clock Abstraction for E2E
- **Clock Seam in Binary (`cmd/mailbox-router/main.go` & `internal/transport/`)**:
  - Support `--test-clock` flag (or `MECHA_TEST_CLOCK=true`).
  - When enabled:
    - Initialize router with `deterministic.NewFixedClock(startTime)`.
    - Register test endpoint: `POST /v1/test/clock/advance` accepting `{"delta_ms": 3600000}`.
    - Advancing the clock advances `fixedClock` AND synchronously calls `r.sweep(ctx)` so sweeps execute immediately without racing real OS tickers.
- **E2E Test Helper (`tests/e2e/harness.ts`)**:
  - Add `advanceClock(deltaMs: number)` on `IsolatedRouter` / `E2ETestHarness`.

### Phase 5: Frontend Structured Schedule Picker & View
- **`StructuredSchedulePicker.tsx` (`web/router-ui-react/src/components/`)**:
  - **Axis 1 (Days)**: Sleek segmented toggle:
    - `[ Every day ]`  `[ Specific days ]`
    - When "Specific days": 7 circular/pill day buttons (`M T W T F S S`) with neon accent states, plus 1-click presets (*Weekdays · Weekends · All*).
  - **Axis 2 (Time)**: Sleek segmented toggle:
    - `[ At specific times ]`  `[ Repeating interval ]`
    - When "At specific times": Dynamic list of time inputs (`HH:MM`) with `+ Add another time` and `.icon-btn-round` remove buttons.
    - When "Repeating interval": `Every [ X ] [ Minutes / Hours / Days ▾ ]`.
  - **Summary Chip**: Dark glassmorphic chip displaying natural English summary (e.g. *"Runs every 2 hours, Monday through Friday"*).
- **Update `WorkflowSchedulesView.tsx` & `WorkflowSchedulesModal.tsx`**:
  - Replace `ScheduleCronPicker` with `StructuredSchedulePicker`.
  - Display natural English summary in the schedule table/cards.
  - Remove all leftover cron parsing regexes and dead code.

### Phase 6: Verification & Fast E2E Testing
- Comprehensive unit tests in `router-proxy/internal/store/schedule_evaluation_test.go` covering all schedule combinations and timezone edge cases.
- Fast, non-flaky Playwright E2E tests in `tests/e2e/background-workflows.spec.ts` using the test clock to verify schedule firing and background worker creation without sleeping.

---

## 4. Acceptance Criteria
1. No cron syntax or cron fields exposed to the user in the UI.
2. Two independent orthogonal toggles (Days: Every day vs Specific days; Time: Specific times vs Repeating interval) supported seamlessly.
3. Arbitrary multiple times (e.g. 9:15 AM and 5:45 PM) work accurately without cron limitations.
4. Server-side sweep automatically executes due schedules and recalculates `next_run_at`.
5. Test clock HTTP endpoint allows deterministic time advancement and immediate sweep execution in E2E tests without real-time sleeps.
6. Clean, dead-code-free codebase adhering to `docs/web-ui-design-system.md` and behavioral testing policies.
