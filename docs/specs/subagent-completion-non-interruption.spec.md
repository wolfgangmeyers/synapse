# Spec: Subagent Completion Non-Interruption of Multi-Tool Batches

- **ID**: `SPEC-SUBAGENT-COMPLETION-NON-INTERRUPTION-001`
- **Status**: `Draft`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/subagent-completion-non-interruption.spec.ts`

---

## 1. Intent & User Outcome

When a managed agent is executing a multi-tool batch (such as sequential bash commands, file edits, or diagnostic queries planned in a single assistant turn), automated background subagent lifecycle notifications (specifically messages with IDs matching `subagent-%`, such as `subagent-completed-*` or `subagent-terminated-*`) and scheduled reminders must not abort, cancel, or interrupt pending unexecuted tool calls in that batch.

Previously, `NextEmbeddedSteerMail` selected any queued mail addressed to the running agent without filtering out subagent completion notifications. When an asynchronous subagent completed while the parent agent was midway through a multi-tool turn, the router immediately treated the subagent completion notice as mid-turn steering input. In runtimes supporting mid-turn steering (such as Pi or Claude Code), injecting this system notice caused the runtime to abort subsequent planned tool calls in the active turn, reporting them as interrupted. The agent would then prematurely synthesize a response to the subagent wake notice before finishing its own scheduled work, leading to abandoned tasks, incomplete workflows, and misleading diagnostic messages ("interrupted by user steering message" when no user was involved).

Importantly, this non-interruption protection is specific to routine subagent lifecycle notices (`m.id LIKE 'subagent-%'`) and reminders. In contrast:
- **Background Bash Job Completions (`m.id LIKE 'background-bash-completed-%'`)**: As specified in `tests/e2e/background-bash-steer-serialization.spec.ts`, background bash completion notices (even though originated with `From: "system"`) MUST continue to steer active batches mid-turn so that sequential launches absorb job completions without deadlock or dropped execution.
- **Human User Steering (`source_kind IN ('web_ui', 'user')`)**: Direct user messages sent during an active turn MUST continue to steer and interrupt pending tool calls, preserving immediate operator intervention authority.

With this specification:
1. Subagent notifications (`m.id LIKE 'subagent-%'`) and reminders (`source_kind = 'reminder'` / `sender = 'reminders'`) are excluded from mid-turn steering selection (`NextEmbeddedSteerMail`).
2. Multi-tool batches planned by the agent execute to completion without interference from concurrent subagent completions.
3. Subagent completion notifications remain safely queued in the router store and are cleanly admitted as prompt input for the agent's *subsequent* turn once the active multi-tool turn completes and settles.
4. Background bash job completion notices (`m.id LIKE 'background-bash-completed-%'`) remain steerable, ensuring zero regression for background bash job serialization.
5. Deliberate human user steering retains its mid-turn interruption capability, allowing operators to halt in-flight agent actions when urgent intervention is required.
6. All interruption diagnostics truthfully reflect the exact initiator and source of the abort.

---

## 2. Core Invariants

1. **Subagent Notification Non-Interruption**: Subagent lifecycle notifications (`m.id LIKE 'subagent-%'`, such as `subagent-completed-*` and `subagent-terminated-*`) and scheduled reminders (`source_kind = 'reminder'` or sender `'reminders'`) MUST NEVER trigger mid-turn steering or abort pending tool calls in an active turn.
2. **Background Bash Steering Preservation**: Background bash job completion notices (`m.id LIKE 'background-bash-completed-%'`) MUST continue to be eligible for mid-turn steering, ensuring active launches absorb finished job notices as verified by `background-bash-steer-serialization.spec.ts`.
3. **Turn Completion Preservation**: An agent turn with multiple planned tool calls MUST execute all eligible tool calls in its batch regardless of how many background subagents finish, fail, or terminate during that turn.
4. **Queue Preservation & Deferred Admission**: Subagent completion notifications that arrive while a turn is active MUST remain durably queued in `messages` and MUST be admitted at the start of the next turn immediately after the active turn settles into an idle/ready state.
5. **User Steering Authority**: Direct user steering messages (`source_kind IN ('web_ui', 'user')` or human participants) MUST continue to be eligible for mid-turn steering and MAY interrupt pending tool calls when immediate operator override is desired.
6. **Diagnostic Truthfulness**: Any diagnostic, log entry, history record, or tool result describing an interruption MUST truthfully attribute the cause to its actual origin (e.g. "interrupted by user steering" only when triggered by a human user, never for subagent or system events).

---

## 3. User Scenarios (Behavioral Contract)

### Scenario: [SCN-001] Subagent completion during multi-tool batch does not interrupt pending tools
- **User Goal**: A parent agent running several tool calls in a batch completes all planned tools even if a background subagent finishes while tools are running.
- **Given**:
  - A managed agent has spawned a background subagent `child-1`.
  - The parent agent issues a turn containing a batch of multiple tool calls: `tool_1` (e.g. bash step 1) and `tool_2` (e.g. bash step 2).
  - `tool_1` begins execution.
- **When**:
  - While `tool_1` is still executing, `child-1` finishes execution.
  - The router generates a `subagent-completed-child-1-1` system notification message (`m.id LIKE 'subagent-%'`) with `source_kind = 'system'` and sender `'system'` addressed to the parent agent.
  - The notification is enqueued in the router store.
- **Then**:
  - `NextEmbeddedSteerMail` does NOT select `subagent-completed-child-1-1` as a steer candidate (`m.id NOT LIKE 'subagent-%'`).
  - The active session does NOT receive the notification as mid-turn steering.
  - `tool_1` finishes successfully.
  - `tool_2` executes to completion and produces its result; it is NOT aborted or marked as interrupted.
  - The parent agent's turn finishes with a normal terminal stop reason.
  - Upon turn settlement, the router admits the queued `subagent-completed-child-1-1` notice as the initiating prompt for the parent's next turn.

### Scenario: [SCN-002] User steering during multi-tool batch preserves user interruption behavior
- **User Goal**: A human operator can urgently intervene and halt a running agent's pending tool calls by sending a message from the Web UI.
- **Given**:
  - A managed agent is executing a multi-tool batch: `tool_1` (long-running) followed by `tool_2`.
  - `tool_1` is currently active.
- **When**:
  - Wolfgang submits an urgent steering message ("Stop tools immediately and report status") from the Web UI (`source_kind = 'web_ui'`).
- **Then**:
  - `NextEmbeddedSteerMail` selects the user message for mid-turn steering (user messages are not excluded by the subagent or reminder filters).
  - The active session receives the steer input.
  - Subsequent tool calls (`tool_2`) are aborted and not executed.
  - The agent responds directly to the user steering directive.
  - The tool result or timeline record for `tool_2` correctly notes that execution was halted by user steering.

### Scenario: [SCN-003] Truthful interruption diagnostics
- **User Goal**: Timeline history and tool result messages accurately report the real cause of an abort rather than falsely claiming user intervention when system events occur.
- **Given**:
  - A tool in an active turn is cancelled or aborted due to an explicit event (e.g. user steering, process restart, or session timeout).
- **When**:
  - The system records the tool result or timeline history entry for the aborted tool call.
- **Then**:
  - If aborted due to user steering, the message indicates interruption by user steering.
  - If aborted due to router restart or process cancellation, the diagnostic specifies restart/cancellation.
  - No diagnostic or history record EVER attributes an interruption to a user when no user message was submitted (specifically, background subagent completions never cause an interruption diagnostic).

### Scenario: [SCN-004] Concurrent arrival of subagent completion and user steering
- **User Goal**: When both a background subagent finishes and a user submits a steering prompt while an agent is executing a tool batch, user steering takes precedence for mid-turn intervention while subagent completion remains queued for subsequent turn admission.
- **Given**:
  - A managed agent is executing a batch with `tool_1` in progress and `tool_2` pending.
- **When**:
  - Subagent completion notice `subagent-completed-sub-A` (`m.id LIKE 'subagent-%'`) is enqueued.
  - Immediately afterward (or concurrently), user message `usr-msg-B` (`source_kind = 'web_ui'`) is enqueued.
- **Then**:
  - `NextEmbeddedSteerMail` excludes `subagent-completed-sub-A` and selects `usr-msg-B`.
  - The active turn is steered by `usr-msg-B`, causing `tool_2` to abort and the agent to address the user.
  - The subagent completion notice `subagent-completed-sub-A` remains safely queued in `messages`.
  - Once the steered user turn finishes, `subagent-completed-sub-A` is admitted for the following turn without being lost or dropped.

### Scenario: [SCN-005] Background bash job completion retains mid-turn steering capability
- **User Goal**: Background bash job completion notices continue to steer active launches mid-turn to preserve batch serialization and completion absorption.
- **Given**:
  - A managed agent executes a turn launching multiple background bash jobs or holding at a batch barrier.
  - A background bash job finishes execution.
- **When**:
  - The router enqueues a completion notice with ID `background-bash-completed-<jobId>` and sender `'system'`.
- **Then**:
  - `NextEmbeddedSteerMail` selects `background-bash-completed-<jobId>` because it does NOT match `subagent-%` and is not a reminder.
  - The active session receives the steer echo as specified in `background-bash-steer-serialization.spec.ts`.
  - No regression occurs in background bash job completion handling or serialization.

---

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `SCN-001` | Subagent completion (`subagent-%`) during multi-tool batch does not interrupt pending tools | `tests/e2e/subagent-completion-non-interruption.spec.ts` | `subagent completion notification does not interrupt pending tools in multi-tool batch` |
| `SCN-002` | User steering during multi-tool batch preserves user interruption behavior | `tests/e2e/subagent-completion-non-interruption.spec.ts` | `user steering message interrupts pending tools in multi-tool batch` |
| `SCN-003` | Truthful interruption diagnostics | `tests/e2e/subagent-completion-non-interruption.spec.ts` | `truthful attribution of interruption cause in tool results and history` |
| `SCN-004` | Concurrent subagent completion and user steering handles steering while keeping notice queued | `tests/e2e/subagent-completion-non-interruption.spec.ts` | `concurrent system notification and user steering prioritizes user steer and defers notice` |
| `SCN-005` | Background bash job completion (`background-bash-completed-%`) retains mid-turn steering capability | `tests/e2e/background-bash-steer-serialization.spec.ts` | `short completions steer during active launches` |
