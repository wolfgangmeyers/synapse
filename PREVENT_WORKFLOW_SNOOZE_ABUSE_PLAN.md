# Prevent Workflow Snooze Abuse Implementation Plan

## Problem Summary
Agents have been observed repeatedly calling `workflow_snooze` over and over instead of making active progress or escalating when stuck. Snooze is frequently misused as a stalling tactic rather than for its intended purpose (waiting for external asynchronous processes like CI builds or remote deployments).

Issue #84 mandates four core improvements:
1. **Mandatory `justification` Parameter**:
   - Require a mandatory non-empty `justification` string parameter in `workflow_snooze`.
   - Reject invocations without a substantive justification explaining the external asynchronous process being awaited (reject empty, whitespace-only, missing, non-string, or trivial strings < 8 chars).
2. **Track & Surface Snooze Invocation Count**:
   - Track cumulative snooze invocations for the current active workflow run / visit.
   - Return the cumulative snooze count in the tool execution response (e.g. `workflow_snooze_count` / formatted note).
3. **Guidance Hints & Anti-Abuse Escalation**:
   - Include an explicit hint in tool responses reminding agents that:
     - Snooze is **only** for waiting on external asynchronous processes (principally CI, test runs, or remote builds).
     - If progress is stalled due to ambiguity, missing credentials, environmental issues, or lack of external input, the agent must mark itself as blocked (`mecha_blocked`) instead of repeatedly snoozing.
   - Escalate warning intensity as the snooze count increases (e.g., warning advisory after multiple snoozes suggesting `mecha_blocked`).
4. **Tool Description & Documentation Updates**:
   - Update the tool schema description and system guidance to explicitly delineate when to use `workflow_snooze` vs. `mecha_blocked`.
   - Explicitly instruct models that repetitive snoozing without waiting on an active external process is considered a violation of workflow execution discipline.

---

## Technical Architecture & Design Decisions

### 1. Mandatory `justification` Argument
- In `embeddedWorkflowSnoozeSchema()` (`router-proxy/internal/managed/embedded_workflow_snooze_tool.go`):
  - Add `ai.Prop("justification", ai.String("Substantive explanation of the external asynchronous process being awaited (e.g. CI build, test run, or remote deployment). Snooze is ONLY for active external processes."))`.
  - Mark `justification` as required in the schema (`ai.Prop`).
  - Keep `duration_minutes` and `duration_ms` optional. Retain optional `reason` as an optional alias / backwards-compatible metadata field, but require `justification` to be provided and non-empty.
- In `NormalizeEmbeddedWorkflowSnoozeArguments`:
  - Validate that `justification` is present and decodes to a valid string.
  - Require `len(strings.TrimSpace(justification)) >= 8` to prevent empty / trivial bypass (`""`, `" "`, `"."`, `"waiting"`).
  - Reject unknown arguments, duplicate arguments, null justification, and non-string types.

### 2. Tracking Cumulative Snooze Count on `workflow_journal_runs`
- Place `snooze_count` directly on `workflow_journal_runs` via migration `136_workflow_snooze_count.sql`:
  ```sql
  ALTER TABLE workflow_journal_runs ADD COLUMN snooze_count INTEGER NOT NULL DEFAULT 0;
  ```
  - An active workflow run already has an active record in `workflow_journal_runs` (`status='active'`, guaranteed unique per agent via `workflow_journal_one_active`).
  - When an agent exits or replaces a workflow run, `ExitWorkflowJournal` or `ReplaceWorkflowRun` transitions the previous run to `status='terminal'` and opens a new one, cleanly resetting the run's lifecycle counter without needing manual reset logic on `managed_agents`.
  - In `SaveCurrentEmbeddedWorkflowSnoozePreference` (which runs in a `runImmediate` SQLite transaction), incrementing and returning the count is atomic:
    ```sql
    UPDATE workflow_journal_runs
    SET snooze_count = snooze_count + 1
    WHERE agent_id = ? AND status = 'active'
    RETURNING snooze_count;
    ```
  - If no active workflow journal run exists (e.g. synthetic or legacy), default count is 1.

### 3. Escalating Guidance & Warning Intensity
- Compute escalation tier based on `count`:
  - `count == 1`: standard reminder:
    - `"Workflow snooze count for this run: 1. Snooze is ONLY for waiting on active external asynchronous processes (such as CI test runs or remote deployments). If progress is stalled due to ambiguity, missing credentials, test failures, or lack of external input, you must call mecha_blocked instead of repeatedly snoozing."`
  - `count >= 2` and `count < 4`: elevated reminder:
    - `"⚠️ Advisory: Workflow snooze count for this run: 2. Repeated snoozing without waiting on an active external process violates workflow execution discipline. If you are stuck or waiting on user/environment input, call mecha_blocked."`
  - `count >= 4`: strong warning:
    - `"🚨 Warning: Workflow snooze count for this run: 4. You have snoozed 4 times in this workflow run. Snooze is strictly prohibited as a stalling tactic. If an external build or CI process is not actively running, you MUST call mecha_blocked immediately or make active progress."`
- The response JSON structure from `workflow_snooze` tool execution will contain:
  - `ok`: `true`
  - `snoozed_until`: RFC3339 timestamp
  - `duration_ms`: effective duration in ms
  - `duration_minutes`: effective duration in minutes
  - `workflow`: workflow name
  - `snooze_count`: cumulative integer count for the active run
  - `guidance`: escalating guidance text
  - `warning`: optional warning string when count >= 2

### 4. Updating Descriptions & Catalogs
- Update tool description in `embeddedWorkflowSnoozeTool`:
  - Delineate `workflow_snooze` vs `mecha_blocked`.
  - Emphasize that repetitive snoozing without waiting on an active external process is a workflow execution discipline violation.
- Update `store.CanonicalWorkflowTools` and `store.NativeToolCatalog`:
  - Clarify tool description for `workflow_snooze`.
- Update `workflowContinuationGuidance`:
  - Ensure continuation prompt remains consistent and compliant with existing tests.
- Update `docs/specs/INDEX.md` and `docs/specs/workflow-implicit-goals.spec.md` (and add `docs/specs/workflow-snooze-abuse.spec.md`).

### 5. Automated Tests & Regressions
- **Go Unit & Behavioral Tests**:
  - Update `embedded_workflow_snooze_tool_test.go` and `workflow_snooze_arguments_test.go` to cover:
    - Rejection of missing `justification`.
    - Rejection of empty or whitespace-only or trivial `justification` (< 8 chars).
    - Rejection of null or non-string `justification`.
    - Successful execution with valid `justification`.
    - Incrementation of `snooze_count` across multiple calls in the same run.
    - Escalation of warnings across multiple calls.
    - Reset of `snooze_count` upon new workflow run.
  - Update existing Go test fixtures calling `workflow_snooze` to provide `justification: "Waiting for test execution"`.
- **Playwright E2E Tests**:
  - Update existing E2E tests in `tests/e2e/` calling `workflow_snooze` to include valid `justification: "Waiting for external build"`.
  - Add dedicated E2E test in `tests/e2e/` (or update `tests/e2e/workflow-implicit-goals.spec.ts`) asserting:
    - Missing justification rejection.
    - Returned `snooze_count` and escalating warning message.

---

## Phased Implementation Plan

### Phase 1: Behavioral Specification & Schema Design
- Draft/update behavioral specification (`docs/specs/workflow-snooze-abuse.spec.md` and amend `docs/specs/workflow-implicit-goals.spec.md`).
- Define exact JSON schemas, error messages, and escalation thresholds.
- Update `docs/specs/INDEX.md`.

### Phase 2: Store Migration & Atomic Snooze Count Tracking
- Write SQLite migration `136_workflow_snooze_count.sql` adding `snooze_count` to `workflow_journal_runs`.
- Update `Store` methods in `router-proxy/internal/store/` to track and increment snooze count on `SaveCurrentEmbeddedWorkflowSnoozePreference`.
- Update `NormalizeEmbeddedWorkflowSnoozeArguments` and `WorkflowResultJSON` with mandatory justification, count, and escalating guidance.

### Phase 3: Tool Implementation & Schema Parity
- Update `embeddedWorkflowSnoozeSchema()` and `embeddedWorkflowSnoozeArguments()` in `router-proxy/internal/managed/embedded_workflow_snooze_tool.go`.
- Update tool descriptions in `CanonicalWorkflowTools` and `NativeToolCatalog`.

### Phase 4: Verification & Test Suite Updates
- Run Go tests for `router-proxy/internal/managed/` and `router-proxy/internal/store/`.
- Update existing test callers across `*_test.go` and `tests/e2e/*.spec.ts`.
- Run Playwright E2E tests for workflow snooze and implicit goals.
- Perform robot code review and verify clean git status.
