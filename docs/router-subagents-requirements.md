# Router Subagents Requirements

## Overview

We need native subagents for router-managed Go agents. Right now, embedded agents can launch independent top-level workers, but they cannot spawn background subagents.

We want embedded agents to run subagents using the router's existing embedded agent infrastructure. The child runs asynchronously in the background. The parent does not block. When the child finishes, the router wakes up the parent with a summary notification, and the parent pulls the full result.

This document defines the human decisions and product requirements. Every subsequent implementation task will trace directly back to one of these requirements.

---

## Resolved Human Decisions

| Area | Human Decision |
| --- | --- |
| **Execution model** | Non-blocking and asynchronous. The `subagent` tool returns immediately with an ID. The parent worker continues its turn without waiting. |
| **Tool interface** | Expose three tools to embedded agents: `subagent` (spawn/resume), `get_subagent_result` (retrieve output), and `steer_subagent` (redirect running child). Keep behavioral parity with `pi-subagents`. |
| **Turn limits & shutdown** | Two-stage limit: reaching the configured `max_turns` sends a steer prompt to wrap up immediately. The subagent gets a fixed grace period (5 grace turns). If it does not finish within grace turns, hard stop aborts it. |
| **Lifecycle & settling** | When the subagent model finishes responding without making further tool calls, its turn loop settles and its lifecycle ends cleanly (`completed`). |
| **Parent wake-up** | When a subagent finishes, the router delivers a structured notification to the parent worker. If the parent is idle, this notification automatically wakes it up to process the result. |
| **Result delivery** | Notifications contain only metadata and a short preview. The parent must explicitly call `get_subagent_result` to read the full output. This keeps parent context small. |
| **Anti-polling throttle** | Calling `get_subagent_result` on a running agent returns a strict snapshot warning. Polling repeatedly (e.g. 3 times within 60 seconds) returns an explicit error to prevent token burning. |
| **WebUI visibility** | Subagents are marked with parentage. They must NOT appear in the top-level WebUI agent list. They appear only under the parent worker's subagent badge. |
| **UI visual treatment** | Delete the entire orbiting subagent card widget completely from the UI. Do not render any separate subagent card widget; subagent activity appears solely through standard tool call cards in the conversation history. |
| **Crash recovery** | If the router process terminates abruptly (crash, SIGKILL), upon restart any subagents left in state "running" have their database status updated to "interrupted" (e.g. error: "router process terminated abruptly") so they do not linger as permanent ghost runners, and parents calling `get_subagent_result` immediately receive this interrupted status. |

---

## Requirements

### 1. Tool Surface & Execution Model

- **S1. Three-tool interface:** Router-managed embedded agents have access to three subagent tools:
  - `subagent`: spawn a new subagent or resume an existing one.
  - `get_subagent_result`: retrieve the status, stats, and output of a subagent.
  - `steer_subagent`: send a steering instruction to an active subagent.
- **S2. Non-blocking asynchronous spawn:** Calling `subagent` must never block the parent worker waiting for child completion. The tool creates the child session, enqueues execution in the background, and immediately returns the child's `agent_id` and initial status (`running` or `queued`).
- **S3. Behavioral parity with `pi-subagents`:** The tool parameters and behavior match `pi-subagents`:
  - `prompt`: initial task instructions.
  - `description`: short human-readable label for UI and notifications.
  - `subagent_type`: agent configuration or system prompt template.
  - `max_turns`: soft turn limit for the child (optional, defaults to runtime setting).
  - `resume`: agent ID of a previous child to resume with a follow-up prompt.
- **S4. Mid-run steering:** `steer_subagent` delivers a guidance message into a running subagent:
  - If the subagent is currently executing a tool, the message is injected into its conversation immediately after that tool finishes.
  - If the subagent is queued, the message is buffered and injected when execution starts.
  - If the subagent is already in a terminal state, the call is rejected with a clear error.
- **S5. Concurrency bounds:** Subagents share a configurable host concurrency ceiling. When all slots are busy, new subagents enter a `queued` state until a slot opens. Queued subagents can be inspected or stopped before starting.

---

### 2. Lifecycle, Turn Limits, and Settling

- **L1. Soft turn limit:** When a subagent reaches its configured `max_turns` limit, the runtime injects a steering nudge instructing the model to wrap up immediately and provide its final answer. Execution does not abort at the soft limit.
- **L2. Grace turns before hard stop:** After the soft limit is reached, the subagent is allowed a fixed grace window (default 5 grace turns) to complete its work and produce final output. If the subagent continues past the grace window, the runtime aborts the session (`aborted`) to stop runaway token consumption.
- **L3. Clean settling:** When the subagent's model finishes generating text without requesting further tool calls, the subagent settles. Settling completes the turn loop and transitions the child to terminal status (`completed`).
- **L4. Terminal states:** Every subagent ends in exactly one of these terminal states:
  - `completed`: task finished normally after model settled.
  - `steered`: task finished cleanly after wrapping up under the soft turn limit.
  - `aborted`: hard stop after exceeding `max_turns` plus grace turns.
  - `error`: unrecoverable runtime, model, or tool error.
  - `stopped`: stopped by parent or system while running or queued.

---

### 3. Parent Wake-Up and Notification

- **N1. Completion notification delivery:** When a subagent reaches a terminal state, the router formats a structured task notification and posts it to the parent worker's conversation.
- **N2. Structured notification content:** The completion notification includes:
  - Subagent ID and description.
  - Terminal status (`completed`, `steered`, `aborted`, `error`, `stopped`).
  - Runtime duration and lifetime token consumption.
  - Total tool uses count.
  - Result preview (bounded to at most 500 characters).
  - Explicit pointer reminding the parent to call `get_subagent_result` for the full output.
- **N3. Idle parent wake-up:** If the parent worker is currently idle when the subagent finishes, the router must wake the parent worker up, triggering a turn so the parent evaluates the finished subagent's work.
- **N4. Full result pull:** To keep parent context compact and avoid prompt bloat, the full output is never injected directly into the notification. The parent retrieves the complete output by calling `get_subagent_result(agent_id)`.

---

### 4. Anti-Polling Throttle & Token Protection

- **P1. Running snapshot warning:** If `get_subagent_result` is called while a subagent is still `queued` or `running`, it returns a status snapshot accompanied by an explicit warning:
  > `**MANDATORY**!!! This is a snapshot only. Do NOT repeatedly pull a running result!!! Rest and wait for completion; a completion notification will arrive automatically.`
- **P2. Anti-polling rate limit:** If a parent worker calls `get_subagent_result` on a running subagent repeatedly (more than 3 times within a rolling 60-second window), the runtime rejects the call with an error instructing the agent to stop polling and wait for the wake-up notification.
- **P3. Terminal retrieval guarantee:** Once a subagent has settled into a terminal state, `get_subagent_result` is never throttled. It returns the complete report, including full result text, status, duration, token usage, tool counts, and optional full conversation transcript.

---

### 5. WebUI & Visibility Policy

- **U1. Top-level agent list exclusion:** Subagents carry parentage metadata (`parent_agent_id` and subagent role). The router and WebUI must filter out subagents from the top-level agent list. Only top-level supervisor roots and independent managed workers appear as main rows.
- **U2. Parent row subagent indicator:** Subagents are represented visually inside their parent worker's row:
  - An indicator badge showing active subagent count (e.g. `2 subagents`).
  - Expanding the badge reveals the active subagent descriptions and running statuses.
  - When all subagents settle, the badge transitions to inactive or hides cleanly according to existing child disclosure rules.
- **U3. Delete orbiting subagent card widget:** Delete the entire orbiting subagent card widget completely from the UI (including `<aside className="active-subagents">` and any sweeping conic-gradient / `agent-border-sweep` orbiting border card animations). Do not render any separate subagent card widget. Subagent activity appears solely through standard tool call cards in the conversation history.
- **U4. Tool call card presentation:** Subagent execution and outcomes appear exclusively as standard tool call cards (`subagent`, `steer_subagent`, `get_subagent_result`) inline within the conversation history, plus clean parent badges, with no floating or orbiting subagent widgets.

---

### 6. Durability, Crash Recovery, and Fencing

- **D1. Durable subagent records:** Subagent metadata, parentage, runtime configuration, status, turn counts, and final results must be persisted in SQLite.
- **D2. Crash reconciliation:** If the router process terminates abruptly (crash, SIGKILL), upon restart any subagents left in state `running` have their database status updated to `interrupted` (with error: "router process terminated abruptly") so they do not linger as permanent ghost runners. Parents calling `get_subagent_result` immediately receive this interrupted status. Completed results remain queryable after restart.
- **D3. Session epoch fencing:** Subagent results, wake-ups, and steering messages are fenced by session epoch and runtime generation. A subagent from an older parent epoch cannot wake up or steer a new conversation epoch.
- **D4. Workspace & security boundaries:** Subagents run strictly within the parent worker's approved working directory and inherit the parent's file access restrictions.

---

## Acceptance Criteria

1. **Non-blocking spawn:** An embedded agent calls `subagent`. The tool returns an ID in < 50ms. The parent continues executing without waiting for the child.
2. **Turn limit & grace verification:** A subagent with `max_turns: 2` receives the wrap-up steer message on turn 2. It is allowed up to 5 grace turns. If it settles on turn 4, status is `steered`. If it runs past turn 7, it is hard-aborted with status `aborted`.
3. **Parent wake-up on completion:** A parent worker launches a subagent and finishes its own turn, entering `idle`. When the subagent settles, the parent worker is automatically woken up with a completion notification in its prompt.
4. **Result pull:** The parent receives the completion notification (with 500-char preview) and issues `get_subagent_result`. The full result body is returned with exact stats (tokens, tool uses, duration).
5. **Polling rejection:** A parent worker calls `get_subagent_result` 4 times in 10 seconds while a subagent is running. The 4th call is rejected with the anti-polling error.
6. **Steering delivery:** A parent calls `steer_subagent` while a child is executing. The message is delivered into the child conversation after the active tool call finishes.
7. **WebUI agent list cleanliness:** Active subagents do not appear as top-level cards or rows in the WebUI. They appear exclusively within the parent's subagent badge disclosure.
8. **No separate subagent card widget:** The orbiting subagent card widget is completely removed; subagent activity appears solely through standard tool call cards in conversation history.
9. **Abrupt crash recovery:** Killing the router process while subagents are running (`SIGKILL`) leaves no ghost runners on restart; startup marks all previously running subagents as `interrupted`, and subsequent `get_subagent_result` calls immediately return the interrupted status.
