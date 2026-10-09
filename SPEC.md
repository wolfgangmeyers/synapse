# Specification: Workflow State Tool Restrictions

## 1. Problem Statement
Currently, embedded workers running in router-proxy have access to all registered tools for their role across their entire lifecycle. When operators configure workflow states (e.g., "Reviewing", "Specifying", "Triage", "Auditing"), they cannot constrain which tools an embedded agent can invoke while in that state. For example, a "Reviewing" state may require read-only inspection (`read`, `bash` or `read` only) while strictly forbidding file mutations (`write`, `edit`) or external delegation (`subagent`, `mecha_worker`). Without enforcement at both the prompt/session declaration boundary and the runtime execution admission boundary, workers can hallucinate or attempt destructive actions that violate the intended workflow phase contract.

## 2. Requirements & Boundaries

### 2.1 Backward Compatibility & Default Behavior
- Tool restrictions are opt-in per workflow state.
- If a workflow state has `allowed_tools` set to `null`, `[]`, or omitted, all tools remain available to the embedded worker (existing behavior).
- When `allowed_tools` contains one or more tool names, only those specified tools are provided to the agent and admitted for execution while that state is active.

### 2.2 Canonical Tool Identifier Whitelist
Tool names configured in `allowed_tools` must be validated against the set of known embedded worker tools:
- `mecha_mailbox_send`
- `goal`
- `goal_snooze`
- `mecha_context`
- `mecha_reminder`
- `context_reset` (alias: `reset`)
- `scratchpad`
- `bash`
- `read`
- `edit`
- `write`
- `subagent`
- `get_subagent_result`
- `steer_subagent`
- `mecha_worker`

Validation rules:
- Tool names must be non-empty, trimmed, and belong to the whitelist.
- `context_reset` and `reset` are aliased: allowing either allows both `context_reset` and `reset` in the runtime session.
- Duplicates are de-duplicated on save.
- Unknown tool names produce a 400 Bad Request error on state creation or update.

### 2.3 Dynamic Tool Filtering on State Entry & Turn Execution
When an embedded session prepares a turn in `router-proxy/internal/managed/embedded_session.go`:
1. If the agent has an `active_workflow_state_id`, the manager inspects the active `WorkflowState`.
2. If `AllowedTools` is non-empty:
   - Tool list construction filters out any tool whose name is not in the allowed list (accounting for `context_reset`/`reset` aliasing).
   - Only allowed tools are passed to `coding.NewSession(coding.SessionOptions{Tools: filteredTools, ...})`.
3. If no state is active, or `AllowedTools` is empty/null, the full standard tool set is passed to `coding.Session`.

### 2.4 Tool Execution Admission Fence
In `embedded_session.go` `BeforeToolCall`:
- If an agent is in an active workflow state with `AllowedTools` configured, and attempts to invoke a tool that is not allowed (e.g. through prompt hallucination or direct tool injection):
  - The router denies execution cleanly before durable intent or effect execution.
  - Return `&agent.PolicyDenial{Reason: fmt.Sprintf("tool '%s' is not allowed in active workflow state '%s'", toolName, state.Name)}`.
  - The denial is recorded in tool history as a cleanly rejected tool outcome without crashing the runner or terminating the session.

### 2.5 Dynamic State Transitions
- State transitions take effect immediately on the next prompt/turn.
- When an agent transitions from state A (`allowed_tools: ["read"]`) to state B (`allowed_tools: null` or `["read", "write"]`), the next turn instantiates `coding.Session` with the newly active state's tool set.
- Deactivating the active state restores full tool availability.

## 3. Data Storage & Schema Migration

### 3.1 Migration 093
File: `router-proxy/internal/store/migrations/093_workflow_state_allowed_tools.sql`
```sql
ALTER TABLE workflow_states ADD COLUMN allowed_tools_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(allowed_tools_json));
```

### 3.2 Store Layer (`router-proxy/internal/store/workflow_states.go`)
- Update `WorkflowState` struct:
  ```go
  type WorkflowState struct {
      ID           string          `json:"id"`
      Name         string          `json:"name"`
      Description  string          `json:"description"`
      EntryText    string          `json:"entry_text"`
      Reminders    []StateReminder `json:"reminders"`
      AllowedTools []string        `json:"allowed_tools"`
      CreatedAt    string          `json:"created_at"`
      UpdatedAt    string          `json:"updated_at"`
  }
  ```
- Update `CreateWorkflowState`, `UpdateWorkflowState`, `GetWorkflowState`, and `ListWorkflowStates`:
  - Serialize/deserialize `allowed_tools_json`.
  - Validate tool names against the whitelist on insert/update.
  - Guarantee `AllowedTools` defaults to `nil` or `[]string{}` when stored as `[]`.

## 4. HTTP API Endpoints

### 4.1 Request & Response Contracts
- `GET /v1/workflow-states`: Each item includes `"allowed_tools": ["read", "write"]` or `[]`.
- `POST /v1/workflow-states`:
  ```json
  {
    "name": "Reviewing",
    "description": "Read-only inspection phase",
    "entry_text": "Please review the proposed diff.",
    "allowed_tools": ["read", "bash"],
    "reminders": []
  }
  ```
- `PUT /v1/workflow-states/{id}`:
  Accepts updated `allowed_tools: string[] | null`.
- Invalid tool names return HTTP 400 with `{ "error": "INVALID_TOOL_NAME", "message": "unknown tool: ..." }`.

## 5. Web UI

### 5.1 Domain Models (`web/router-ui-react/src/domain/workflow-states.ts`)
- Add `allowed_tools?: string[] | null` to `WorkflowState`, `CreateWorkflowStateInput`, and `UpdateWorkflowStateInput`.
- Export constant `KNOWN_WORKFLOW_TOOLS` containing the 14 valid tool identifiers with human-friendly descriptions.

### 5.2 WorkflowStatesPage Component (`web/router-ui-react/src/components/WorkflowStatesPage.tsx`)
- Display allowed tools badge/list on each state card:
  - "All tools allowed" tag when empty or null.
  - Comma-separated or tag list of allowed tools when restricted (e.g., "Allowed tools: read, bash").
- Create / Edit Workflow State Modal:
  - Add "Tool Restrictions" section.
  - Checkbox / Toggle: "All tools allowed (unrestricted)" enabled by default.
  - When unchecked, render multi-select or checkbox list for each known tool.
  - Helper actions: "Select All", "Clear All", or common presets ("Read-only", "Code authoring").

## 6. Verification & Automated Tests
1. **Store Migration & CRUD Unit Tests**:
   - `router-proxy/internal/store/workflow_states_test.go`:
     - Test storing, retrieving, updating `allowed_tools`.
     - Test validation rejects invalid tool names.
     - Test round-trip serialization of empty vs non-empty tool lists.
2. **Managed Embedded Session Integration Test**:
   - `router-proxy/internal/managed/embedded_workflow_tool_restrictions_test.go`:
     - Launch embedded agent in default state -> verify all tools passed.
     - Activate workflow state with restricted tools (`read` only) -> verify model receives only `read`, and attempting to call `bash` or `write` returns `PolicyDenial`.
     - Deactivate or switch to unrestricted state -> verify full tool set restored.
3. **HTTP Transport Tests**:
   - Verify GET/POST/PUT `/v1/workflow-states` with `allowed_tools` and invalid tool payload validation.
4. **Web UI Component Tests**:
   - `web/router-ui-react/tests/component/workflow-states.test.tsx`:
     - Verify card displays "All tools allowed" vs restricted badges.
     - Verify modal allows selecting specific tools and submitting `allowed_tools`.
     - Verify editing preserves or updates `allowed_tools`.
