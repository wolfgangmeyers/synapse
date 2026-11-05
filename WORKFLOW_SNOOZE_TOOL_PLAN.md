# WORKFLOW_SNOOZE_TOOL_PLAN.md

> **Historical, superseded plan.** The current contract and its scoped validation/acceptance limits are in [Implicit Workflow Goals & Workflow-Level Snooze](docs/specs/workflow-implicit-goals.spec.md). This plan is not evidence of implementation or acceptance.

### Supersession map

- Settling bypass and continuation prompt (§1.1): current spec invariant 5; its exact prompt and fallback rules supersede the quoted examples below.
- `workflow_snooze` and `goal_snooze` (§1.2): invariants 2–4 and 6–9 govern arguments, bounds, compatibility, durable deadlines and lifecycle; the simpler storage proposal below is not the contract.
- UI label (§1.3): scenario SCN-WFIG-001.
- Implementation steps and tests (§2): historical proposals only; use the current spec scenarios and its validation record, including any stated acceptance limits.

## Overview

Based on live testing feedback with Wolfgang:
1. **Clarify Settling Prompt**: Replace the confusing `"Evaluator inconclusive: goal evaluation did not produce a bounded verdict"` output with a direct, unambiguous instruction.
2. **Bypass LLM Goal Evaluation During Workflows**: When an agent is in an active workflow, the settlement evaluation should not execute an expensive, flaky auxiliary LLM stream call. Workflows are inherently in progress until explicitly exited; settling resolves deterministically to `NOT_DONE` with zero LLM overhead.
3. **Dedicated `workflow_snooze` Tool**: Provide an explicit `workflow_snooze` tool that lets agents snooze workflow continuation nudges, defaulting to the workflow's configured snooze duration.
4. **UI Label Simplification**: In the workflow creation and edit modals (`WorkflowsPage.tsx`), update the field label from `"Goal Snooze Duration (minutes)"` to simply `"Snooze (minutes)"`.

---

## 1. Architecture & Design

### 1.1 Direct Workflow Settling Nudge (No LLM Evaluation)
- In `router-proxy/internal/managed/embedded_session.go` (`evaluateEmbeddedTurn`):
  - Check if the agent is currently in an active workflow (`curAgent.ActiveWorkflowID != "" || curAgent.ActiveWorkflowStateID != ""`).
  - If in an active workflow:
    - **Do NOT** call `evaluateEmbeddedProductionGoalWithDetails` or any auxiliary LLM stream.
    - Set `outcome = store.EmbeddedGoalEvaluationNotDone`.
    - Retrieve active workflow name and active state name.
    - Format reason: `fmt.Sprintf("Workflow %q is currently active in state %q. Make progress towards completing the workflow or call workflow_snooze.", wfName, stateName)`
- In `router-proxy/internal/managed/embedded_goal_continuation.go` (`formatGoalContinuationPrompt`):
  - When in an active workflow, emit the clean prompt:
    `The workflow "<workflow_name>" is currently active in state "<state_name>". You must either make progress towards completing the workflow or call workflow_snooze.`
  - No `VERDICT: NOT_DONE` or `REASON: Evaluator inconclusive...` headers.

### 1.2 Dedicated `workflow_snooze` Tool
- Create `router-proxy/internal/managed/embedded_workflow_snooze_tool.go`:
  - Tool Name: `workflow_snooze`
  - Parameters:
    - `duration_minutes` (integer, optional)
    - `duration_ms` (integer, optional)
    - `reason` (string, optional)
  - Behavior:
    - If neither `duration_minutes` nor `duration_ms` is supplied (or <= 0), look up the active workflow's `snooze_duration_ms` (defaulting to 600,000 ms / 10 minutes if unspecified).
    - If `duration_minutes` is provided, convert to ms (`duration_minutes * 60,000`).
    - Record the snooze preference on the agent's goal preference store (`UpdateEmbeddedGoalSnoozePreference`), so `MaybeResumeEmbeddedGoal` and the continuation scheduler respect the snooze timer.
    - Return a clean structured JSON:
      `{"ok": true, "snoozed_until": "<rfc3339>", "duration_ms": <int>, "duration_minutes": <int>, "workflow": "<workflow_name>"}`
  - Register `workflow_snooze` in `embedded_session.go` and expose it to managed agents.
  - Keep `goal_snooze` working as an alias if an agent calls it while in a workflow.

### 1.3 UI Simplification in `web/router-ui-react`
- In `web/router-ui-react/src/components/WorkflowsPage.tsx`:
  - Rename input label from `"Goal Snooze Duration (minutes)"` to `"Snooze (minutes)"`.
  - Update description text to `"Default duration for workflow_snooze while executing this workflow (default: 10 minutes)."`.
  - Maintain the clean numeric input state and minute-to-millisecond conversion.

---

## 2. Implementation Steps

1. **Backend Tool Implementation**:
   - Add `embedded_workflow_snooze_tool.go` with schema, parsing, workflow snooze fallback, and preference storage.
   - Register tool in `embedded_session.go` tool registry.
   - Add native tool entry in `router-proxy/internal/store/native_tools.go` and `workflow_tools.go`.
2. **Settling Prompt & Evaluation Bypass**:
   - In `embedded_session.go`: Bypass auxiliary LLM goal evaluation when agent is in an active workflow.
   - In `embedded_goal_continuation.go`: Format the prompt as a direct workflow progression reminder.
3. **Web UI Updates**:
   - Update `WorkflowsPage.tsx` modal labels to `"Snooze (minutes)"`.
4. **Testing & Verification**:
   - Backend unit tests in `router-proxy/internal/managed/` for `workflow_snooze` tool execution and settling prompt formatting.
   - Update Playwright E2E test `tests/e2e/workflow-implicit-goals.spec.ts` to assert the updated label `"Snooze (minutes)"` and `workflow_snooze` availability.
   - Run tests and verify zero regressions.

---

## 3. Simplicity & Non-Goals

- **Non-Goal**: Do not alter how `mecha_workflow_transition` operates.
- **Simplicity**: No complex state machines; `workflow_snooze` directly sets the snooze timestamp on the agent, and the existing idle resumption check honors it.
