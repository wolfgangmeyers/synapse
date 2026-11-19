# Spec: Workflow Crash Nudge Recovery & Stopped Worker Continuations

- **ID**: `SPEC-WORKFLOWS-CRASH-NUDGE-001`
- **Status**: `Active`
- **Domain**: `Workflows`
- **Target E2E Suite**: `tests/e2e/workflow-stopped-worker-nudge.spec.ts`

---

## 1. Intent & User Outcome

When an agent participates in an active workflow, the system guarantees that work continues autonomously toward completing the workflow or respecting an explicit snooze. If an execution turn halts prematurely—whether due to normal completion of a step, a process crash, a tool call timeout, context deadline cancellation, or an unhandled SDK error before final assistant settlement—the agent must not become permanently stalled.

For Claude Code and embedded agent runtimes, the router ensures that when a turn terminates without completing the workflow (and without active subagents or a pending snooze), an automatic follow-up nudge is scheduled so execution recovers cleanly. This autonomous recovery operates under strict bounding: recovery nudges share the existing 20-consecutive-nudge cap across distinct stops and reset upon qualifying worker progress or deliberate user steering.

---

## 2. Core Invariants

1. **Autonomous Progress Preservation**: A worker in an active workflow that stops or crashes during a turn MUST be nudged again to resume progress once the agent returns to an idle, running state, provided no subagents are active, no unexpired snooze is active, and the agent is not blocked.
2. **Provider-Agnostic Recovery**: Recovery nudges MUST function identically across supported agent runtimes (Claude Code CLI and embedded Pi/Gemini/Vertex). Claude Code workers MUST NOT be suppressed from idle recovery simply because `AppliedRevision == DesiredRevision` when the worker is in an active workflow state requiring progress.
3. **Strict Bounded Accounting**: Consecutive recovery nudges across distinct stops MUST obey the global maximum limit (default 20 consecutive unanswered nudges). A crashed or failed turn MUST NOT bypass or inflate the cap beyond configured limits.
4. **Streak Reset on Qualifying Response**: When the worker emits qualifying progress (non-empty text or an executed tool call) in response to a nudge, the unanswered nudge count MUST reset to 0.
5. **Exact Guidance Prompt Parity**: Nudge continuation prompts for active workflows MUST strictly follow the canonical workflow guidance format:
   `"The workflow <workflow name> is currently active in state <state name>. You must either make progress towards completing the workflow, call workflow_snooze, exit the workflow with mecha_workflow_transition (action exit) when an exit is allowed, or call mecha_blocked with the reason."`
6. **No Uncontrolled Spinning / Loop Prevention**: If a worker continuously crashes without producing any output or tool calls, the 20-nudge cap MUST halt automatic wakes and prevent infinite restart loops until user interaction occurs.

---

## 3. User Scenarios (Behavioral Contract)

### Scenario: [SCN-WFCN-001] Claude worker turn crashes mid-turn and is nudged again (Happy Recovery Path)
- **User Goal**: Recover workflow execution autonomously when a Claude worker crashes during turn execution.
- **Given**:
  - An agent with Claude Code runtime is executing an active workflow in state `A`.
  - The workflow goal has been applied (`AppliedRevision == DesiredRevision`).
  - An initial goal or workflow turn is dispatched to the worker.
- **When**:
  - The worker's turn crashes or terminates abruptly partway through execution (e.g. tool execution context deadline exceeded, command timeout, or unexpected process termination) before producing a final assistant reply or completing goal evaluation.
  - The turn error is handled and the worker returns to `activity_state: "idle"` with `desired_state: "running"`.
- **Then**:
  - The router detects that the agent is idle with an active workflow and no active subagents.
  - The router does NOT suppress the idle wake for Claude Code; it formats the exact canonical workflow continuation guidance for state `A`.
  - An automatic continuation turn is scheduled and delivered to the worker.
  - The worker receives the recovery prompt and can resume workflow execution.

---

### Scenario: [SCN-WFCN-002] Worker crashes repeatedly and respects the 20-nudge limit (Error/Capping Path)
- **User Goal**: Ensure crashing workers do not cause unbounded router CPU consumption, log spam, or billing runaway.
- **Given**:
  - An agent with an active workflow is in a state where every turn crashes immediately upon delivery.
  - The worker produces no text deltas and no tool calls before crashing.
- **When**:
  - The router schedules and attempts successive recovery nudges across distinct turn crashes.
- **Then**:
  - Each dispatched recovery attempt increments the consecutive continuation and unanswered nudge counters.
  - Upon reaching the 20th unanswered attempt without qualifying progress, the router suppresses further automatic wakes.
  - The worker remains idle; no additional provider turns are dispatched until a user message, mail delivery, or administrative reset occurs.

---

### Scenario: [SCN-WFCN-003] Successful response after crash resets nudge streak (Recovery & Progress Path)
- **User Goal**: Ensure that once a worker recovers from a transient crash and takes action, its full recovery budget is restored for any future issues.
- **Given**:
  - An agent in an active workflow crashed on turn 1, resulting in 1 unanswered recovery nudge recorded.
  - The router dispatched a recovery nudge.
- **When**:
  - The worker executes the recovery turn successfully, emitting an assistant text response or invoking a tool (e.g. `mecha_workflow_transition` or a shell command).
- **Then**:
  - The workflow response authority validates the worker's response.
  - The consecutive unanswered nudge counter resets to 0.
  - Subsequent distinct stops or transitions receive their full 20-nudge allowance.

---

### Scenario: [SCN-WFCN-004] Native workflow transition stop receives current-state guidance without external user message
- **User Goal**: When an agent transitions states within a workflow, it receives autonomous guidance for the new state even when the worker process stops between turns.
- **Given**:
  - An agent is active in state `A` of a workflow.
  - The agent invokes `mecha_workflow_transition` to advance to state `B`.
- **When**:
  - The transition turn settles and the agent process stops or goes idle in state `B`.
- **Then**:
  - The router evaluates the idle agent and recognizes it is in state `B`.
  - A continuation turn is started with guidance referencing state `B` without requiring a user message.
  - Distinct evaluations and continuation records are created for state `B` with distinct sequence and digest identities.

---

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `SCN-WFCN-001` | Claude worker turn crashes mid-turn and is nudged again | `tests/e2e/workflow-stopped-worker-nudge.spec.ts` | `Claude worker turn crash partway schedules recovery nudge` |
| `SCN-WFCN-002` | Worker crashes repeatedly and respects 20-nudge limit | `tests/e2e/workflow-nudge-accounting.spec.ts` | `attempts: capped scheduler suppresses a valid pre-send wake` |
| `SCN-WFCN-003` | Successful response after crash resets nudge streak | `tests/e2e/workflow-nudge-accounting.spec.ts` | `attempts: checked response resets the near-cap streak` |
| `SCN-WFCN-004` | Native transition receives current-state guidance on stop | `tests/e2e/workflow-stopped-worker-nudge.spec.ts` | `Claude native transition receives current-state nudge without another user message` |
