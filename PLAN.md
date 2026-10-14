# Implementation Plan: Subagent Visibility and Enriched Completion Notifications

## 1. Overview & Context

This plan implements the features specified in `SPEC.md`:
1. **`list_subagents` embedded tool**: A new embedded tool registered on parent worker sessions in `router-proxy` allowing workers to inspect running, queued, and historical subagents under their binding fence.
2. **Enriched completion wake notification**: When any subagent terminates, `DeliverSubagentParentWake` inspects whether active sibling subagents (queued or running) remain under the parent binding. If siblings remain, it appends an explicit summary block `Active subagents still running (N):` to the parent wake prompt, giving the parent full awareness of concurrent tasks without guessing or polling.
3. **Child subagent containment & isolation**: Subagents themselves must not receive `list_subagents` in their toolset (alongside existing exclusion of `subagent`, `get_subagent_result`, `steer_subagent`, etc.).
4. **Integration regression tests**: Full within-service integration tests proving parent execution, child tool exclusion, wake formatting with siblings and without siblings, and durability across sessions/epochs.

---

## 2. Delegation & Delegate Seam Analysis (`pi-subagents` vs `router-proxy`)

We checked the repository and subagent integration seams across `router-proxy`, `pi-extensions/mecha-rpc`, and the `pi-packages` fork:
- In the router-managed architecture, the embedded session engine in Go (`router-proxy/internal/managed/`) is the authoritative host that runs embedded agents and manages background subagents via `embedded_subagent_runner.go`, `embedded_subagent_public_tools.go`, and `embedded_subagent_wake.go`.
- The public tools for embedded agents (`subagent`, `get_subagent_result`, `steer_subagent`) are defined and registered directly in Go via `Manager.EmbeddedSubagentPublicTools(parent store.EmbeddedBinding) []agent.AgentTool` in `router-proxy/internal/managed/embedded_subagent_public_tools.go`.
- Subagent completion wake notifications are dispatched and formatted directly in Go via `DeliverSubagentParentWake` in `embedded_subagent_wake.go` and `FormatSubagentWakePrompt` in `embedded_subagent_results.go`.
- For `pi-subagents` / `mecha-rpc`: In router-managed RPC mode, `fixedSubagentsTools` in `supervisor.go` specifies the list of fixed subagent tools. We will add `"list_subagents"` to `fixedSubagentsTools` in `supervisor.go` (and `store/workflow_tools.go` in `CanonicalWorkflowTools`) so manifest validation, tool parity, and workflow state tool filtering include it seamlessly. The core logic executes entirely within `router-proxy`. No changes to `pi-packages` are required because `pi-subagents` delegates subagent lifecycle management in embedded mode to the router-proxy host tools.

---

## 3. Detailed Component Implementation

### 3.1 Component 1: `list_subagents` Tool (`router-proxy/internal/managed/`)

#### Schema & Parameters
In `router-proxy/internal/managed/embedded_subagent_public_tools.go`:
- Define `embeddedListSubagentsSchema() *ai.Schema`:
  - `status` (optional string): `"active"` (default, filters for `running` and `queued`), `"all"`, `"running"`, `"queued"`, `"completed"`, `"failed"`, `"stopped"`, `"timed_out"`.
  - `limit` (optional integer): Default 50, minimum 1, maximum 100.
  - Closed schema: `AdditionalAllowed = false`.
- Tool name: `"list_subagents"`.
- Description: `"List background subagents launched by this agent, including their IDs, descriptions, statuses, and runtimes."`

#### Querying Store & In-Memory State
In `m.embeddedListSubagentsTool(parent store.EmbeddedBinding) agent.AgentTool`:
- Check context cancellation: `if err := ctx.Err(); err != nil { return agent.AgentToolResult{}, err }`.
- Parse arguments:
  - `statusRaw, _ := args["status"].(string)` -> default `"active"`. Normalized to lower case.
  - `limitRaw` -> default 50. Validate integer bounds 1 to 100 (clamp or return error if invalid type/range).
- Retrieve all subagent records for the parent binding using `m.store.ManagedSubagents(ctx, parent)`.
  - The read fence in `ManagedSubagents` verifies the durable parent identity and epoch boundaries.
  - In addition, combine/refresh with live in-memory metrics from `m.subagentsActive`:
    - Acquire `m.subagentsMu.Lock()`.
    - For subagents currently in `m.subagentsActive`, read live `startTime`, `turnCount`, `toolUseCount`, `totalTokens`, and current status to present up-to-the-millisecond duration and turn counts for active agents.
    - Release `m.subagentsMu.Unlock()`.
- Filter records according to `status`:
  - `"active"`: status is `"running"` or `"queued"`.
  - `"all"`: all records.
  - Specific status: case-insensitive match on `record.Status`.
- Sort by `created_at DESC` (or maintain creation order).
- Enforce `limit`.

#### Formatting Output
Format structured JSON response:
```json
{
  "summary": {
    "total_matching": 2,
    "total_active": 2,
    "running_count": 1,
    "queued_count": 1
  },
  "subagents": [
    {
      "agent_id": "subagent-parent-123",
      "subagent_type": "general-purpose",
      "description": "run unit tests",
      "status": "running",
      "started_at": "2026-06-02T10:00:00Z",
      "elapsed_seconds": 42.5,
      "turn_count": 3,
      "tool_uses": 5
    }
  ]
}
```
Return as `ai.TextContent` in `agent.AgentToolResult`.

#### Registration in Public Tools & Child Exclusion
- In `router-proxy/internal/managed/embedded_subagent_public_tools.go`:
  Update `EmbeddedSubagentPublicTools(parent store.EmbeddedBinding) []agent.AgentTool`:
  ```go
  func (m *Manager) EmbeddedSubagentPublicTools(parent store.EmbeddedBinding) []agent.AgentTool {
      return []agent.AgentTool{
          m.embeddedSubagentTool(parent),
          m.embeddedGetSubagentResultTool(parent),
          m.embeddedSteerSubagentTool(parent),
          m.embeddedListSubagentsTool(parent),
      }
  }
  ```
- Child sessions construct their toolset in `NewEmbeddedSubagentTools(cwd)` (`embedded_subagent_tools.go`). It only registers `read`, `edit`, `write`, `bash`. Neither `subagent`, `get_subagent_result`, `steer_subagent`, nor `list_subagents` are registered in child sessions.
- In `router-proxy/internal/managed/supervisor.go`:
  Add `"list_subagents"` to `fixedSubagentsTools`:
  ```go
  var fixedSubagentsTools = []string{"subagent", "get_subagent_result", "steer_subagent", "list_subagents"}
  ```
- In `router-proxy/internal/store/workflow_tools.go`:
  Add `list_subagents` definition to `CanonicalWorkflowTools`.

---

### 3.2 Component 2: Enriched Completion Notification (`embedded_subagent_wake.go` & `embedded_subagent_results.go`)

#### Querying Active Siblings in `DeliverSubagentParentWake`
In `router-proxy/internal/managed/embedded_subagent_wake.go`:
- Before building the mailbox envelope in `deliverSubagentMailboxNotificationWithContext(ctx context.Context, parent store.EmbeddedBinding, record store.ManagedSubagentRecord)`:
  - Query all subagents for the parent: `allChildren, err := m.store.ManagedSubagents(ctx, parent)`.
  - Filter for active siblings:
    ```go
    var activeSiblings []store.ManagedSubagentRecord
    if err == nil {
        for _, child := range allChildren {
            if child == nil {
                continue
            }
            // Strictly exclude the subagent that just completed
            if child.AgentID == record.AgentID {
                continue
            }
            if child.Status == "running" || child.Status == "queued" {
                activeSiblings = append(activeSiblings, *child)
            }
        }
    }
    ```
  - Pass `activeSiblings` into the formatter:
    `body := FormatSubagentWakePromptWithActiveSiblings(record, activeSiblings)`

#### Prompt Formatter Enhancement
In `router-proxy/internal/managed/embedded_subagent_results.go`:
- Maintain `FormatSubagentWakePrompt(record store.ManagedSubagentRecord)` for backwards compatibility:
  ```go
  func FormatSubagentWakePrompt(record store.ManagedSubagentRecord) string {
      return FormatSubagentWakePromptWithActiveSiblings(record, nil)
  }
  ```
- Introduce `FormatSubagentWakePromptWithActiveSiblings(record store.ManagedSubagentRecord, activeSiblings []store.ManagedSubagentRecord) string`:
  - Format base preview:
    ```
    [Subagent Completed] Task <agent_id> (<desc>) finished with status <status>: <preview>
    ```
  - If `len(activeSiblings) > 0`:
    Append sibling block:
    ```
    Active subagents still running (<N>):
    - <sibling.AgentID> (<desc>): <status> (<duration>)
    ```
    Where duration is formatted using `FormatSubagentDurationSeconds(sibling.DurationSeconds)`.
  - Append call-to-action & footer:
    ```
    Call get_subagent_result if you need the full output, and proceed immediately with the next steps to complete your task.

    ---
    *Automated notification from system. Do not reply to this message — messages to 'system' are not accepted.*
    ```
  - If `len(activeSiblings) == 0`:
    Omit the active subagents block entirely, keeping the prompt clean and identical to prior formatting.

---

## 4. Tests & Regression Prevention Strategy

Per the mandatory dev-loop requirements, integration tests that exercise real within-service paths and prevent regressions are mandatory.

### 4.1 Unit & Formatting Tests
File: `router-proxy/internal/managed/embedded_subagent_results_test.go`
- `TestFormatSubagentWakePromptWithActiveSiblings_NoSiblings`:
  - Proves wake prompt matches standard single-completion format with no spurious sibling section.
- `TestFormatSubagentWakePromptWithActiveSiblings_MultipleActiveSiblings`:
  - Verifies `Active subagents still running (2):` block is generated with accurate agent IDs, descriptions, statuses, and elapsed durations.

### 4.2 Public Tool Schema & Construction Tests
File: `router-proxy/internal/managed/embedded_subagent_public_tools_test.go`
- Update `TestEmbeddedSubagentPublicToolsConstructionAndChildSurfaceExclusion`:
  - Assert `EmbeddedSubagentPublicTools` returns 4 tools: `subagent`, `get_subagent_result`, `steer_subagent`, `list_subagents`.
  - Assert `list_subagents` parameter schema: `status` (string), `limit` (integer).
  - Verify child tools (`NewEmbeddedSubagentTools`) strictly exclude `list_subagents`.

### 4.3 Mandatory Within-Service Integration Tests
File: `router-proxy/internal/managed/embedded_subagent_public_tools_test.go` and `embedded_subagent_wake_test.go`
1. **`TestListSubagentsTool_ExecutionAndFiltering`** (Integration):
   - Setup: Real SQLite store, Manager instance with faux provider.
   - Spawn multiple subagents: one running, one queued, one completed, one failed.
   - Execute `list_subagents` with:
     - Default status (`"active"`): returns running and queued subagents only; verifies counts.
     - Filter `"all"`: returns all subagents.
     - Filter `"completed"`: returns only the completed subagent.
     - Limit: verify limit clamping and truncation.
   - Verify caller parent boundary: Parent A cannot see Parent B's subagents (cross-parent fence isolation).
2. **`TestDeliverSubagentParentWake_WithActiveSiblings_Integration`** (Integration):
   - Setup: Real parent agent in SQLite store.
   - Create 3 subagent records under parent: Child 1 (completed), Child 2 (running), Child 3 (queued).
   - Trigger `DeliverSubagentParentWake` for Child 1.
   - Inspect mailbox envelope enqueued in the store for the parent agent.
   - Assert envelope body contains `[Subagent Completed] Task child-1 ...`.
   - Assert envelope body contains `Active subagents still running (2):` with Child 2 and Child 3 details.
   - Assert Child 1 is NOT included in the active siblings list.
3. **`TestDeliverSubagentParentWake_WithoutActiveSiblings_Integration`** (Integration):
   - Setup: Real parent agent in SQLite store with single subagent (Child 1).
   - Trigger `DeliverSubagentParentWake` for Child 1.
   - Assert envelope body does NOT contain `Active subagents still running`.

---

## 5. Implementation Phases & Step-by-Step Execution

1. **Step 1 (Red tests for prompt formatting)**:
   - Add unit tests in `embedded_subagent_results_test.go` for `FormatSubagentWakePromptWithActiveSiblings`.
2. **Step 2 (Enriched wake prompt formatting implementation)**:
   - Implement `FormatSubagentWakePromptWithActiveSiblings` in `embedded_subagent_results.go`.
   - Update `DeliverSubagentParentWake` in `embedded_subagent_wake.go` to query active siblings from store and pass to formatter.
3. **Step 3 (Integration tests for wake delivery)**:
   - Add integration tests in `embedded_subagent_wake_test.go` proving sibling formatting when siblings exist vs single subagent completion.
4. **Step 4 (Red tests for `list_subagents` tool schema and execution)**:
   - Update `embedded_subagent_public_tools_test.go` to assert 4 public tools and child exclusion.
   - Add `TestListSubagentsTool_ExecutionAndFiltering` test cases.
5. **Step 5 (Implement `list_subagents` tool)**:
   - Implement `embeddedListSubagentsSchema` and `embeddedListSubagentsTool` in `embedded_subagent_public_tools.go`.
   - Register in `EmbeddedSubagentPublicTools`.
   - Update `fixedSubagentsTools` in `supervisor.go` and `CanonicalWorkflowTools` in `workflow_tools.go`.
6. **Step 6 (Full validation, coverage & regression verification)**:
   - Run `go test -v ./internal/managed/...` and `go test -v ./...`.
   - Ensure clean build, zero regressions, and full coverage of new logic.
