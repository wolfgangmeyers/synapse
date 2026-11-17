# Spec: Workflow Snooze Abuse Prevention & Mandatory Justification

- **ID**: `SPEC-WORKFLOWS-SNOOZE-ABUSE-001`
- **Status**: `Active`
- **Domain**: `Workflows`
- **Target E2E Suite**: `tests/e2e/workflow-implicit-goals.spec.ts`

---

## Section 1: Intent & User Outcome

Autonomous agents running active workflows sometimes get stuck and repeatedly invoke `workflow_snooze` as an evasive stalling mechanism rather than making forward progress or reporting blockers. `workflow_snooze` is strictly intended for awaiting active external asynchronous operations (e.g. CI build pipelines, long-running test suites, or remote deployments). When an agent is stalled due to ambiguity, missing credentials, environmental issues, or lack of external input, it must escalate using `mecha_blocked` rather than repetitively snoozing.

This feature prevents snooze abuse by:
1. Enforcing a mandatory `justification` parameter explaining the external asynchronous process being awaited.
2. Rejecting trivial, empty, whitespace-only, or non-substantive justifications (< 8 characters).
3. Tracking the cumulative snooze count across the active workflow journal run.
4. Escalating guidance and warning intensity in the tool's output as the snooze count rises, reminding the agent of workflow execution discipline and directing it to call `mecha_blocked` if stalled.

---

## Section 2: Core Invariants

1. **Mandatory Substantive Justification**: Every `workflow_snooze` invocation MUST provide a `justification` string property whose trimmed length is at least 8 characters. Any invocation with a missing, null, non-string, whitespace-only, or fewer than 8-character justification MUST be rejected before altering any snooze deadline or preference.
2. **Cumulative Run Counter**: Snooze counts MUST be tracked per active workflow journal run. Each accepted `workflow_snooze` execution within the same active workflow run MUST increment `snooze_count` atomically.
3. **Lifecycle Run Reset**: When a workflow run ends (transition to terminal, exit, or replacement with a new run), the new workflow run starts with `snooze_count = 0`.
4. **Structured Escalating Feedback**: Every successful `workflow_snooze` execution result MUST return `snooze_count` and contextual `guidance`. For `snooze_count >= 2`, an explicit `warning` property MUST be included with escalating severity directing the agent to `mecha_blocked` if not actively awaiting an external process.
5. **Duration Semantics Preserved**: Optional `duration_minutes`, optional `duration_ms`, and optional `reason` remain supported with identical clamping and precedence rules.

---

## Section 3: User Scenarios (Behavioral Contract)

### Scenario: [SCN-SNOOZE-001] Agent snoozes with valid external justification
- **User Goal**: Snooze workflow continuation while waiting for a remote CI build to finish.
- **Given**:
  - An agent has an active workflow run in state `Build & Test`.
- **When**:
  - The agent calls `workflow_snooze` with arguments:
    ```json
    {
      "justification": "Waiting for GitHub Actions CI run #1042 to complete",
      "duration_minutes": 5
    }
    ```
- **Then**:
  - The tool succeeds with `ok: true`.
  - The tool response includes `snooze_count: 1`.
  - The tool response includes standard guidance:
    `"Workflow snooze count for this run: 1. Snooze is ONLY for waiting on active external asynchronous processes (such as CI test runs or remote deployments). If progress is stalled due to ambiguity, missing credentials, test failures, or lack of external input, you must call mecha_blocked instead of repeatedly snoozing."`
  - No `warning` field is present on count 1.
  - The agent's goal policy is snoozed for 5 minutes.

### Scenario: [SCN-SNOOZE-002] Agent attempts to snooze without justification or with trivial string
- **User Goal**: Prevent agents from snoozing without explaining the external asynchronous process.
- **Given**:
  - An agent has an active workflow run.
- **When**:
  - The agent calls `workflow_snooze` with empty arguments `{}` or with trivial justification (e.g. `{"justification": "waiting"}` or `{"justification": "   "}`).
- **Then**:
  - The tool rejects the call with an error message:
    `"justification is required for workflow_snooze and must describe the external asynchronous process being awaited (at least 8 characters)"`.
  - The agent's goal policy and snooze preference are NOT updated.
  - The active workflow run's `snooze_count` is NOT incremented.

### Scenario: [SCN-SNOOZE-003] Elevated advisory on repeated snooze invocations (tier 2)
- **User Goal**: Alert the agent and surface an advisory when repeated snoozing occurs.
- **Given**:
  - An agent has an active workflow run that has already snoozed once (`snooze_count == 1`).
- **When**:
  - The agent calls `workflow_snooze` again with valid justification:
    `{"justification": "Still waiting for remote deployment to roll out"}`.
- **Then**:
  - The tool succeeds with `ok: true`.
  - The tool response includes `snooze_count: 2`.
  - The tool response includes an elevated advisory `warning`:
    `"⚠️ Advisory: Workflow snooze count for this run: 2. Repeated snoozing without waiting on an active external process violates workflow execution discipline. If you are stuck or waiting on user/environment input, call mecha_blocked."`

### Scenario: [SCN-SNOOZE-004] Strong warning on excessive snooze invocations (tier 3)
- **User Goal**: Firmly warn the agent against stalling when snooze count reaches 4 or higher.
- **Given**:
  - An agent has an active workflow run that has already snoozed 3 times (`snooze_count == 3`).
- **When**:
  - The agent calls `workflow_snooze` a 4th time with valid justification:
    `{"justification": "Awaiting staging environment health check readiness"}`.
- **Then**:
  - The tool succeeds with `ok: true`.
  - The tool response includes `snooze_count: 4`.
  - The tool response includes a strong `warning`:
    `"🚨 Warning: Workflow snooze count for this run: 4. You have snoozed 4 times in this workflow run. Snooze is strictly prohibited as a stalling tactic. If an external build or CI process is not actively running, you MUST call mecha_blocked immediately or make active progress."`

---

## Section 4: Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E / Unit Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `SCN-SNOOZE-001` | Valid justification and initial snooze count | `tests/e2e/workflow-implicit-goals.spec.ts` | `SCN-SNOOZE-001/003/004: workflow_snooze tracks cumulative snooze count and escalates warnings` |
| `SCN-SNOOZE-002` | Reject missing, trivial, or whitespace justification | `router-proxy/internal/store/workflow_snooze_arguments_test.go` | `TestNormalizeEmbeddedWorkflowSnoozeArgumentsJustificationValidation` |
| `SCN-SNOOZE-003` | Escalating advisory on repeated snoozes (count >= 2) | `tests/e2e/workflow-implicit-goals.spec.ts` | `SCN-SNOOZE-001/003/004: workflow_snooze tracks cumulative snooze count and escalates warnings` |
| `SCN-SNOOZE-004` | Strong warning on excessive snoozes (count >= 4) | `tests/e2e/workflow-implicit-goals.spec.ts` | `SCN-SNOOZE-001/003/004: workflow_snooze tracks cumulative snooze count and escalates warnings` |
| `SCN-SNOOZE-004` | Strong warning on excessive snoozes (count >= 4) | `router-proxy/internal/managed/embedded_workflow_snooze_tool_test.go` | `TestEmbeddedWorkflowSnoozeCountAndEscalatingWarnings` |
