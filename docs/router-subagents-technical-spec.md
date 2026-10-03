# Router Subagents: Technical Specification

## Overview

This specification defines native subagent execution for router-managed Go agents.

Parent agents launch background subagents using the router's embedded Go runner. The launch is asynchronous and non-blocking. The child runs inside an in-process goroutine, executes its assigned prompt using standard file and terminal tools, and settles cleanly when finished. When the child finishes, the router delivers a short completion notification to wake up the parent, and the parent retrieves the full result.

The design is strictly minimal:
- No separate subprocesses or RPC pipes.
- No complex multi-layer state machines.
- Simple SQLite fields on the agent record.
- Straightforward goroutine runner reusing `embedded_runner.go` and `embedded_session.go`.
- Standard tool call cards in the WebUI with zero floating or orbiting widgets.

---

## Architectural Components

```
+--------------------------------------------------------------------------+
| Router Process                                                           |
|                                                                          |
|  +-----------------------+                    +-----------------------+  |
|  | Parent Agent          |                    | Child Subagent        |  |
|  | (embedded session)    |                    | (embedded session)    |  |
|  |                       |                    |                       |  |
|  | Calls:                |  Spawn goroutine   | Runs turn loop        |  |
|  | - subagent() --------->--------------------> Tools: read/write/    |  |
|  |   (returns agent_id)  |                    |        edit/bash      |  |
|  |                       |                    | Turn limit check      |  |
|  | - steer_subagent() ---> Guidance queue --->| Settles               |  |
|  |                       |                    |                       |  |
|  | - get_subagent_result()                    |                       |  |
|  |                       |  <task-notification|                       |  |
|  | Turn Lane <-----------<--------------------+ Writes final result   |  |
|  | (Wakes if idle)       |  delivery          | to SQLite             |  |
|  +-----------------------+                    +-----------------------+  |
|             |                                             |              |
|             v                                             v              |
|  +--------------------------------------------------------------------+  |
|  | SQLite Store (managed_agents table)                                 |  |
|  | parent_agent_id, subagent_status, turn_count, max_turns,           |  |
|  | result_text, last_error, tool_use_count, total_tokens              |  |
|  +--------------------------------------------------------------------+  |
+--------------------------------------------------------------------------+
```

---

## 1. Tool Implementation

Embedded sessions receive three host-level tools in their toolset: `subagent`, `get_subagent_result`, and `steer_subagent`.

### 1.1 `subagent`

Spawns a new child subagent or resumes an existing one.

- **Parameters:**
  - `prompt` (string, required): The task description and instructions for the child.
  - `description` (string, required): Short summary of the task (used in logs, UI badges, and completion notifications).
  - `subagent_type` (string, optional): Agent profile or system prompt template (defaults to standard general-purpose worker).
  - `max_turns` (integer, optional): Maximum soft turns before wrap-up steering (defaults to 15).
  - `resume` (string, optional): Existing child `agent_id` to continue with a follow-up prompt.
- **Behavior:**
  1. Validates inputs. Enforces parent directory bounds (`cwd` matches or is within parent's workspace).
  2. If `resume` is specified:
     - Verifies `resume` agent exists, has `parent_agent_id == caller.agent_id`, and is in terminal state.
     - Re-opens child session, appends new user turn with `prompt`, updates status to `running`.
  3. If creating a new child:
     - Mints child `agent_id` format: `subagent-<parent_short_id>-<random_hex>`.
     - Inserts record into `managed_agents` with `parent_agent_id = caller.agent_id`, `subagent_status = 'running'`, `max_turns`, and `description`.
  4. Dispatches the child runner asynchronously in a background goroutine.
  5. Returns immediately to the parent with:
     ```json
     {
       "agent_id": "subagent-w1-a8f3b2",
       "status": "running",
       "description": "Run unit test suite"
     }
     ```
  6. Total tool execution time is under 50ms. Never blocks waiting for child progress.

### 1.2 `get_subagent_result`

Retrieves status, execution metrics, and output from a subagent.

- **Parameters:**
  - `agent_id` (string, required): The ID of the child subagent.
- **Behavior:**
  1. Verifies that `agent_id` belongs to the calling parent (or supervisor).
  2. Queries SQLite for current subagent record.
  3. **Terminal state** (`completed`, `steered`, `aborted`, `stopped`, `error`, `interrupted`):
     - Returns immediately without throttling.
     - Payload format:
       ```json
       {
         "agent_id": "subagent-w1-a8f3b2",
         "status": "completed",
         "description": "Run unit test suite",
         "duration_seconds": 14.2,
         "turn_count": 6,
         "tool_use_count": 8,
         "total_tokens": 4210,
         "result": "All 42 tests passed successfully in 3.1s.",
         "error": null
       }
       ```
  4. **Active state** (`queued`, `running`):
     - Checks the in-memory anti-polling throttle for `(parent_id, child_id)`.
     - If throttle limit exceeded (>3 calls within 60s): returns an error:
       `Rate limit exceeded: get_subagent_result called too frequently on a running subagent. Rest and wait for the completion notification.`
     - If within limit: records timestamp and returns snapshot with mandatory warning banner:
       ```json
       {
         "warning": "**MANDATORY**!!! This is a snapshot only. Do NOT repeatedly pull a running result!!! Rest and wait for completion; a completion notification will arrive automatically.",
         "agent_id": "subagent-w1-a8f3b2",
         "status": "running",
         "turn_count": 3,
         "duration_seconds": 8.0
       }
       ```

### 1.3 `steer_subagent`

Delivers mid-flight guidance to a running child.

- **Parameters:**
  - `agent_id` (string, required): The target child subagent ID.
  - `message` (string, required): Guidance text to steer the child.
- **Behavior:**
  1. Checks child status in SQLite. If terminal, returns error: `Cannot steer subagent: agent is in terminal state <status>`.
  2. Pushes `message` into the child's in-memory steering queue channel.
  3. The child runner checks this channel at tool completion and turn boundaries:
     - If child is executing a tool, steering message is injected into conversation immediately upon tool return.
     - If child is waiting for model inference, steering is queued for next turn.
  4. Returns `{ "status": "steered", "agent_id": agent_id }`.

---

## 2. Runner Model & Goroutine Lifecycle

Subagents run inside the router process as embedded Go sessions, using the existing machinery in `internal/managed/embedded_runner.go` and `internal/managed/embedded_session.go`.

### 2.1 Execution Flow

1. **Launch:**
   - Parent tool handler calls `manager.startEmbeddedSubagent(parentRecord, childSpec)`.
   - Manager allocates a child lane and spins up `go childRunner.run()`.
2. **Session Configuration:**
   - Working directory: identical to parent's working directory (`saved.Cwd`).
   - Model: inherits parent's active model unless overridden by `subagent_type`.
   - Toolset: subagents receive standard tools: `read`, `bash`, `edit`, `write`. Subagents do NOT receive `subagent`, `mecha_worker`, or supervisor tools. This prevents uncontrolled recursive tree-spawning.
3. **Turn Loop:**
   - The child runner loops through model generation and tool execution turns.
   - On each turn, increments `turn_count` in memory and SQLite.
   - Evaluates turn limits before issuing the next model request.
4. **Settling:**
   - When the model returns a final text message without requesting further tool calls, the loop exits.
   - The runner extracts the final text as `result_text`.
   - Sets terminal status to `completed` (or `steered` if wrap-up prompt was delivered).
   - Commits stats (`tool_use_count`, `total_tokens`, `turn_count`, `duration_seconds`) to SQLite.
   - Calls `postParentNotification()` to wake up the parent.

### 2.2 Concurrency Bounds

- Configurable host concurrency limit (default: 8 active subagents router-wide).
- Implemented with a simple buffered semaphore channel (`chan struct{}`).
- If capacity is exhausted, new subagent transitions to `queued`.
- As active subagents settle, queued subagents acquire slots and transition to `running`.

---

## 3. State Storage & SQLite Schema

Subagent metadata and state live directly in the `managed_agents` table via a lightweight migration. No secondary shadow database or external state file is needed.

### 3.1 Schema Migration

```sql
-- Migration: Add subagent tracking columns to managed_agents
ALTER TABLE managed_agents ADD COLUMN parent_agent_id TEXT REFERENCES managed_agents(agent_id) ON DELETE CASCADE;
ALTER TABLE managed_agents ADD COLUMN subagent_status TEXT CHECK(subagent_status IN ('queued','running','completed','steered','aborted','stopped','error','interrupted'));
ALTER TABLE managed_agents ADD COLUMN subagent_description TEXT;
ALTER TABLE managed_agents ADD COLUMN turn_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE managed_agents ADD COLUMN max_turns INTEGER NOT NULL DEFAULT 15;
ALTER TABLE managed_agents ADD COLUMN result_text TEXT;
ALTER TABLE managed_agents ADD COLUMN tool_use_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE managed_agents ADD COLUMN total_tokens INTEGER NOT NULL DEFAULT 0;

CREATE INDEX idx_managed_agents_parent ON managed_agents(parent_agent_id) WHERE parent_agent_id IS NOT NULL;
CREATE INDEX idx_managed_agents_subagent_status ON managed_agents(subagent_status) WHERE subagent_status IS NOT NULL;
```

### 3.2 State Transitions

The subagent status follows simple transitions:

- `queued` -> `running` (slot acquired)
- `running` -> `completed` (normal model settlement)
- `running` -> `steered` (settled after soft turn limit)
- `running` -> `aborted` (exceeded soft limit + grace turns)
- `running` -> `error` (unhandled model/runtime exception)
- `running` -> `stopped` (explicitly canceled by parent or supervisor)
- `running` / `queued` -> `interrupted` (router crash recovery on startup)

All transitions are single `UPDATE` queries on `managed_agents`.

---

## 4. Turn Limits & Soft Wrap-Up Steering

Runaway subagents waste tokens and block concurrency slots. A two-stage limit enforces safe termination.

### 4.1 Soft Turn Limit

- Every subagent has a `max_turns` value (default: 15).
- When `turn_count == max_turns`:
  - The runtime injects a high-priority system turn message into the subagent:
    ```
    You have reached your turn limit. Wrap up your work immediately. Do not make any new tool calls; provide your final answer to the parent now.
    ```
  - Execution continues into the grace window.

### 4.2 Grace Turns & Hard Stop

- Grace turns count: fixed at 5 turns.
- Hard limit: `max_turns + 5`.
- If the model continues calling tools past the hard limit:
  - The runner immediately halts the turn loop.
  - Sets `subagent_status = 'aborted'`.
  - Sets `result_text = 'Subagent aborted: exceeded maximum turn limit and grace period without settling.'`.
  - Saves current accumulated outputs and metrics to SQLite.
  - Sends `<task-notification>` to parent with status `aborted`.

---

## 5. Parent Notification & Wake-Up

When a subagent reaches a terminal state, the router notifies the parent deterministically.

### 5.1 `<task-notification>` Format

The notification is formatted as clean XML-tagged text:

```xml
<task-notification>
Task: Run unit test suite (ID: subagent-w1-a8f3b2)
Status: completed
Duration: 14.2s | Tool Uses: 8 | Tokens: 4210
Preview: All 42 tests passed successfully in 3.1s. Coverage report generated in build/coverage.out...
Full output available via get_subagent_result(agent_id="subagent-w1-a8f3b2")
</task-notification>
```

- **Preview length:** Bounded to 500 characters. If `result_text` exceeds 500 characters, it is truncated with `...`.
- **Context preservation:** Full result body is never injected into the notification. This ensures parent context stays compact.

### 5.2 Turn Lane Injection & Wake-Up

- The notification is injected directly into the parent agent's serial turn lane (`laneFor(parent_id)`).
- If the parent agent is currently `idle`:
  - Router schedules an immediate model turn for the parent.
  - The parent wakes up and processes the `<task-notification>`.
- If the parent agent is currently `active`:
  - The notification is enqueued in the parent's turn lane.
  - It will be processed at the start of the parent's next turn.

---

## 6. Anti-Polling Throttle

Repeatedly calling `get_subagent_result` while a child is running burns tokens rapidly. A simple rate-limiter prevents parent polling loops.

### 6.1 Mechanism

- Maintained in-memory inside `Manager`:
  ```go
  type pollTracker struct {
      mu    sync.Mutex
      calls map[string][]time.Time // key: parentID + ":" + childID
  }
  ```
- Window: 60 seconds rolling window.
- Max allowed running reads: 3 calls per 60 seconds.
- Algorithm on `get_subagent_result(child_id)`:
  1. If child status is terminal: bypass throttle completely. Return full result.
  2. If child status is `running` or `queued`:
     - Lock tracker mutex.
     - Prune entries older than `now - 60s`.
     - Count entries in window.
     - If count >= 3: reject with rate limit error.
     - If count < 3: append `now`, return snapshot result with `**MANDATORY**` warning header.

---

## 7. WebUI Visual Treatment & Visibility

Subagents must integrate cleanly into the existing WebUI without visual clutter or heavy animations.

### 7.1 Agent List Filtering

- In `router-proxy/internal/transport/managed_http.go` and `router.go`:
  - `GET /v1/agents` and live WebSocket snapshots filter agents:
    `WHERE parent_agent_id IS NULL`
  - Subagents never appear as top-level agent cards or rows in the WebUI sidebar.

### 7.2 Parent Agent Row Badge

- Parent rows display a clean, non-animated badge: e.g., `2 subagents`.
- Expanding the badge shows subagent descriptions and current statuses (`running`, `completed`, `error`).

### 7.3 Removal of Orbiting Card Widget

- **Complete Deletion:**
  - Remove `<aside className="active-subagents">` and its container completely from `router-ui-react/src/components/Conversation.tsx`.
  - Remove `.activity-entry--active::after` spinning conic-gradient animation (`agent-border-sweep`) from `app.css`.
  - Do not render any floating, orbiting, or separate subagent card widget anywhere in the UI.
- **Conversation History Presentation:**
  - Subagent lifecycle events appear solely as standard tool call cards (`subagent`, `steer_subagent`, `get_subagent_result`) inline in the parent's conversation timeline.
  - Tool cards display standard tool status icons and timing, matching all other tools (`read`, `bash`, `edit`).

---

## 8. Abrupt Crash Recovery

If the router process crashes or receives `SIGKILL`, in-flight subagents must not linger as permanent ghost runners.

### 8.1 Startup Reconciliation

During router startup (in `Store.Open()` or startup lifecycle hook), run a single SQL query before admitting any agent turns:

```sql
UPDATE managed_agents
SET lifecycle_state = 'stopped',
    subagent_status = 'interrupted',
    last_error = 'router process terminated abruptly',
    updated_at = ?
WHERE parent_agent_id IS NOT NULL
  AND (lifecycle_state = 'running' OR subagent_status IN ('running', 'queued'));
```

### 8.2 Guarantees

- **No ghost processes:** Since subagents run as in-process goroutines, killing the router process instantly terminates execution. No external OS processes or orphan containers remain.
- **Immediate parent awareness:** If a parent resumes and calls `get_subagent_result`, it receives:
  ```json
  {
    "agent_id": "subagent-w1-a8f3b2",
    "status": "interrupted",
    "error": "router process terminated abruptly"
  }
  ```
  The parent can immediately decide whether to re-run or abort the task.
- **Completed work preserved:** Subagents that settled before the crash retain their `completed` status and stored `result_text`. Their results remain fully queryable.

---

## 9. Verification & Acceptance Plan

| Gate | Test Case | Target Outcome |
| --- | --- | --- |
| **G1: Async Spawn** | Parent calls `subagent`. Measure return latency. | Returns `agent_id` and `running` in < 50ms. Parent turn continues immediately. |
| **G2: Normal Settlement** | Child executes prompt with tools and settles without further calls. | Status transitions to `completed`. Final text stored in `result_text`. |
| **G3: Turn Limits** | Subagent with `max_turns: 2` makes repeated tool calls. | Turn 2 receives wrap-up steer prompt. Turn 7 (2+5) aborts with status `aborted`. |
| **G4: Parent Wake-Up** | Parent launches subagent and goes idle. Subagent settles. | `<task-notification>` delivered to parent turn lane. Parent wakes and evaluates result. |
| **G5: Result Retrieval** | Parent issues `get_subagent_result` after completion. | Full result returned with exact tokens, duration, tool counts. Preview matches notification. |
| **G6: Polling Rate Limit** | Parent calls `get_subagent_result` 4 times within 10 seconds while child runs. | Calls 1-3 return snapshot + warning. Call 4 returns rate limit error. |
| **G7: Mid-Run Steer** | Parent calls `steer_subagent` during active tool execution. | Steer prompt injected into child conversation immediately after tool finishes. |
| **G8: Crash Recovery** | Kill router (`SIGKILL`) while subagents run. Restart router. | Subagent status in DB is `interrupted`. `get_subagent_result` returns error message. |
| **G9: WebUI Verification** | Inspect React conversation and agent list with active subagents. | No top-level rows for subagents. No orbiting card widget rendered. Standard tool call cards present in timeline. |
