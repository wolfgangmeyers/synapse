# SPEC: Subagent Visibility and Enriched Completion Notifications

## 1. Problem Statement

Embedded worker agents in the Mecha system can spawn background subagents via the `subagent` tool. However, parent agents face two visibility deficiencies during multi-agent task execution:

1. **No runtime introspection of active subagents**: Embedded workers have tools to launch (`subagent`), inspect results of known IDs (`get_subagent_result`), and steer (`steer_subagent`), but no tool to list or inspect their running/queued subagents. If a worker launches multiple subagents or resumes work across turns, it cannot programmatically discover what subagents are currently executing, their identifiers, or their run states.
2. **Missing sibling context in completion notifications**: When a subagent terminates (succeeds, fails, times out, or stops), the wake notification delivered to the parent mailbox (`DeliverSubagentParentWake` / `FormatSubagentWakePrompt`) only describes the single subagent that just finished. The parent receives no indication of whether sibling subagents are still actively running concurrently, what their task descriptions are, or whether the parent should wait vs. synthesize intermediate results.

---

## 2. Requirements

### 2.1 Tool: `list_subagents`

Define and implement a new public embedded tool named `list_subagents`.

#### Tool Schema and Registration
- **Tool Name**: `list_subagents` (canonical name, snake_case adhering to standard tool conventions).
- **Registration**: Registered in `EmbeddedSubagentPublicTools` in `router-proxy/internal/managed/embedded_subagent_public_tools.go` alongside `subagent`, `get_subagent_result`, and `steer_subagent`. If applicable in TypeScript/Pi extensions (`pi-subagents`), mirror tool definitions.
- **Access Restrictions**:
  - Bound to the parent's `EmbeddedBinding` fence.
  - **Inheriting child tool restriction**: Must **never** be exposed to subagents themselves. Subagents continue to have subagent tool surfaces stripped or omitted, preventing recursive subagent listing or spawning.
- **Parameters**:
  - `status` (optional string): Filter subagents by status (`"active"`, `"all"`, `"running"`, `"queued"`, `"completed"`, `"failed"`, `"stopped"`, `"timed_out"`). Defaults to `"active"` (`running` + `queued`).
  - `limit` (optional integer): Maximum number of subagents to return (default: 50, minimum: 1, maximum: 100).

#### Output Format and Information
The tool must return a structured response (JSON string or formatted report compatible with tool call responses) containing:
- `subagents`: Array of subagent summaries, each including:
  - `agent_id`: The unique child subagent identifier.
  - `subagent_type`: Type/specialization (e.g. `general-purpose`, `Plan`).
  - `description`: 3-5 word task description provided when spawned.
  - `status`: Current lifecycle state (`queued`, `running`, `completed`, `failed`, `stopped`, `timed_out`).
  - `started_at`: RFC3339 timestamp of subagent creation/start.
  - `elapsed_seconds` / `duration`: Elapsed execution time if running, or total duration if terminal.
  - `turn_count`: Number of agentic turns completed.
  - `tool_uses`: Number of tool calls executed by the child.
- Summary counts: `total_active`, `running_count`, `queued_count`.

---

### 2.2 Enriched Completion / Terminal Wake Notification

When a child subagent transitions to a terminal state (`completed`, `failed`, `stopped`, `timed_out`):

#### Trigger and Scope
- In `DeliverSubagentParentWake` (and `AsyncDeliverSubagentParentWake`) in `router-proxy/internal/managed/embedded_subagent_wake.go`, before creating the mailbox envelope, the manager must query the parent's remaining active subagents within the parent's epoch/binding fence.
- Active subagents are defined as those in state `"running"` or `"queued"`, **strictly excluding** the subagent that just completed.

#### Wake Prompt Formatting
- Update `FormatSubagentWakePrompt(record store.ManagedSubagentRecord)` or introduce an enriched formatter / options variant:
  `FormatSubagentWakePromptWithActiveSiblings(record store.ManagedSubagentRecord, activeSiblings []store.ManagedSubagentRecord) string`
- **When remaining active subagents exist (N > 0)**:
  Append an explicit section to the completion notification body:
  ```
  Active subagents still running (N):
  - <agent_id> (<description>): <status> (<duration/elapsed>)
  ```
  Example:
  ```
  [Subagent Completed] Task subagent-123 (implement auth handler) finished with status completed: Token verification succeeded.

  Active subagents still running (2):
  - subagent-124 (run unit tests): running (42s)
  - subagent-125 (update docs): queued (0s)

  Call get_subagent_result if you need the full output, and proceed immediately with the next steps to complete your task.

  ---
  *Automated notification from system. Do not reply to this message — messages to 'system' are not accepted.*
  ```
- **When no active subagents remain (N == 0)**:
  Omit the active subagents block entirely (or state clearly that no other subagents are running), preserving clean output without clutter.

---

## 3. Constraints & Invariants

1. **Backward Compatibility**:
   - Existing parsers of `[Subagent Completed]` header and get_subagent_result references must continue working without regression.
   - Mailbox message envelope subject and routing remain intact.
2. **Deterministic & Concurrency Safe**:
   - Querying sibling subagents must read from consistent store or in-memory tracker state with appropriate locks (`subagentsMu` or store transaction) without deadlocks or race conditions.
   - Active subagents must filter by the parent's active session epoch/binding to prevent cross-session leakage.
3. **Child Subagent Isolation**:
   - Subagent executions must not have access to `list_subagents`. Subagent tool exclusion logic in `embedded_session.go` and `embedded_subagent_public_tools_test.go` must verify `list_subagents` is excluded from child environments.
4. **Integration Testing**:
   - Mandatory end-to-end integration tests in the Go `router-proxy` suite verifying:
     - Tool execution of `list_subagents` by parent.
     - Exclusion of `list_subagents` in child sessions.
     - Single child completion notification without siblings.
     - Concurrent child completion notifications with active siblings correctly formatted and listed.

---

## 4. Acceptance Criteria

- **AC1**: Embedded parent agents can invoke `list_subagents` and receive accurate list of running/queued subagents (and optionally past subagents when filtered), including ID, description, status, elapsed duration, turn count, and tool uses.
- **AC2**: When a subagent completes and sibling subagents are still queued or running, the parent wake notification (`DeliverSubagentParentWake`) includes the `Active subagents still running (N):` block with each sibling's ID, description, status, and duration.
- **AC3**: When a subagent completes and NO siblings are running or queued, the parent wake notification does not list spurious siblings or format empty lists.
- **AC4**: Subagent sessions do not receive or have access to `list_subagents`.
- **AC5**: All regression integration tests pass in `router-proxy`.
