---
name: workflow-management
description: Authoritative guide to router-managed workflows, state machine DAGs, agent integration, tool filtering, workflow admin tools, and the mecha-workflows v1 bundle schema.
---

# Workflow Management

## Purpose & Architecture

Managed workflows are router-owned state-machine DAGs backed by SQLite tables:
- `workflows`: workflow definitions, entry state pointer, snooze duration, variable definitions JSON.
- `workflow_states`: individual states, descriptions, entry text instructions, tool restrictions, reminders, monitors, exit allowance, UI coordinates.
- `workflow_state_transitions`: directed edges between states with target IDs and descriptions.

These are not plain-text markdown templates. They are structural state machines enforced at the router runtime level.

```
+-----------------------------------------------------------+
|                        workflows                          |
|  id, name, description, entry_state_id, snooze_duration,  |
|  variables_json                                           |
+-----------------------------------------------------------+
                             | 1:N
                             v
+-----------------------------------------------------------+
|                     workflow_states                       |
|  id, workflow_id, name, entry_text, allow_exit,           |
|  allowed_tools, reminders, monitors, ui_pos_x, ui_pos_y   |
+-----------------------------------------------------------+
                             | 1:N
                             v
+-----------------------------------------------------------+
|               workflow_state_transitions                  |
|  workflow_state_id, target_state_id, description          |
+-----------------------------------------------------------+
```

---

## Agent Integration & Enforcement

When an agent binds to a workflow, the router directly mediates its residency and capabilities.

### Active State Binding

The `managed_agents` record tracks workflow execution:
- `active_workflow_id`: current workflow ID (or NULL).
- `active_workflow_state_id`: current workflow state ID (or NULL).
- `workflow_variables_json`: key-value map of bound runtime variables.

Subagents cannot hold independent workflow state; workflow transitions are rejected if invoked by a subagent.

### State Entry Behavior

On entering or transitioning to a state:
1. **Instruction prompt**: Router formats state context and admits it to the agent turn:
   - Header: `## Active Workflow State: <Name>`
   - State description (variable-interpolated)
   - Instructions from `entry_text` (variable-interpolated)
   - Available forward transitions with descriptions
   - Exit allowance indicator
2. **Implicit workflow completion goal**: When an agent enters an active workflow, the router automatically activates an implicit goal: `"Complete active workflow: <workflow_name>"` with workflow-configured snooze duration (default 10 minutes). Per-state goals are removed; internal state transitions keep the implicit goal active. Goal lock has been removed; stored `goal_lock_enabled` values are ignored. Manual goal set/off calls are rejected while any workflow is active. When the workflow exits or deactivates, the goal is cleared.
3. **Tool filtering**: If `allowed_tools` is defined and non-empty, the agent's exposed toolset is restricted to that list.
   - System escape tools always bypass restrictions:
     - `mecha_workflow_transition` (or alias `workflow_transition`)
     - `mecha_mailbox_send` (or alias `mecha_parent_message`)
     - `mecha_tool_timeout` (or alias `extend_tool_timeout`)
     - Note: `context_reset` and `reset` are interchangeable aliases if either is permitted.
   - If `allowed_tools` is empty, the agent's standard configured tools apply.
4. **Reminders & monitors**:
   - Stale reminders and monitors from previous states are cleaned up.
   - State-scoped one-shot and recurring reminders defined on the state are spawned.
   - State-scoped LLM monitors defined on the state are activated.

### State Progression

Agents advance through the graph using the `mecha_workflow_transition` tool:

```json
// Advance to next state
{
  "action": "transition",
  "target_state": "Planning",
  "reason": "Specification approved by supervisor"
}

// Exit workflow (only valid if current state has allow_exit: true)
{
  "action": "exit",
  "reason": "All phases complete and validated"
}
```

Transitions to targets not declared in `workflow_state_transitions` for the active state are rejected by the router.

---

## Workflow Management Tools (Worker Tools)

Administrative tools for inspecting, modifying, and assigning workflows.

> **Tool Availability**: Workflow admin tools are off by default (`DefaultEnabled: false`). Enable them per agent via agent tool configuration or supervisor assignment.

### 1. `workflow_list`

Lists all workflows with summary statistics.

Parameters: none.

Returns:
```json
{
  "status": "ok",
  "workflows": [
    {
      "id": "wf_01j7...",
      "name": "dev-loop",
      "description": "Default dev loop: spec -> plan -> review -> implement (TDD) -> coverage -> review",
      "entry_state_id": "st_01j7...",
      "snooze_duration_ms": 600000,
      "state_count": 4,
      "variable_count": 1,
      "created_at": "2026-03-01T00:00:00Z",
      "updated_at": "2026-03-01T00:00:00Z"
    }
  ]
}
```

### 2. `workflow_get`

Fetches full workflow details including states, transitions, variables, and portable export bundle.

Parameters:
- `workflow_id` (string, required): Workflow ID or exact unique name.

Returns:
```json
{
  "status": "ok",
  "workflow": { "id": "...", "name": "...", "description": "...", "snooze_duration_ms": 600000, "entry_state_id": "...", "variables": [...] },
  "states": [ ... ],
  "bundle": { ... }
}
```

### 3. `workflow_save`

Creates or updates a workflow. Supports direct field updates or importing a portable `mecha-workflows` v1 JSON bundle.

Parameters:
- `bundle` (object|string, optional): Complete `mecha-workflows` v1 JSON export bundle.
- `workflow_id` (string, optional): Target workflow ID or name when updating via fields.
- `name` (string, optional): Workflow name (required when creating via fields without bundle).
- `description` (string, optional): Workflow description.
- `snooze_duration_ms` (integer, optional): Workflow-level goal snooze duration in milliseconds (defaults to 600,000 ms / 10 minutes).
- `entry_state_id` (string, optional): Entry state ID.

When `bundle` is provided, the entire workflow graph (states, transitions, variables) is validated and imported atomically.

### 4. `workflow_delete`

Deletes a workflow and all associated states and transitions.

Parameters:
- `workflow_id` (string, required): Workflow ID or exact unique name.

Returns:
```json
{
  "status": "ok",
  "deleted": true,
  "workflow_id": "wf_01j7..."
}
```

### 5. `workflow_assign`

Activates or deactivates a workflow on a managed agent.

Parameters:
- `agent_id` (string, required): Target agent ID.
- `action` (string, required): `"activate"` or `"deactivate"`.
- `workflow_id` (string, optional): Workflow ID or name (required for `"activate"` unless `workflow_state_id` is supplied).
- `workflow_state_id` (string, optional): Specific initial state ID (defaults to workflow entry state).
- `variables` (object, optional): Key-value string map of runtime variable values.

When activated:
- Active workflow and state are assigned to the agent.
- State reminders and monitors are spawned.
- Implicit workflow completion goal is applied.
- Active toolset is filtered.
- Initial guidance prompt is admitted to the agent turn.

When deactivated:
- Active workflow pointers and variables are cleared.
- State-scoped reminders and monitors are cleaned up.
- Implicit workflow goal is cleared.
- Tool filtering resets to default agent toolset.

### 6. `workflow_state_list`

Lists states belonging to a specific workflow as summaries.

Parameters:
- `workflow_id` (string, required): Workflow ID or exact unique name.

Returns:
```json
{
  "status": "ok",
  "workflow_id": "01838059-86d4-48af-a0b2-0cb006582728",
  "workflow_name": "test workflow",
  "states": [
    {
      "id": "112d9b4b-5044-4db1-aa25-77b743b8f0ca",
      "workflow_id": "01838059-86d4-48af-a0b2-0cb006582728",
      "name": "initial",
      "description": "Initial setup state",
      "allow_exit": true,
      "allowed_tools": ["bash", "read"],
      "reminder_count": 0,
      "monitor_count": 0,
      "transition_count": 0,
      "created_at": "2026-10-27T19:18:16.308Z",
      "updated_at": "2026-10-27T19:18:16.308Z"
    }
  ]
}
```

### 7. `workflow_state_get`

Retrieves full details for an individual workflow state.

Parameters:
- `state_id` (string, optional): Workflow state ID.
- `workflow_id` (string, optional): Workflow ID or exact unique name (when looking up state by name or ID within a workflow).
- `state_name` (string, optional): Workflow state name (when `workflow_id` is provided).
- `state_id_or_name` (string, optional): Workflow state ID or name (when `workflow_id` is provided).

> Note: Either `state_id` or `workflow_id` + `state_name`/`state_id_or_name` must be provided.

Returns:
```json
{
  "status": "ok",
  "state": {
    "id": "112d9b4b-5044-4db1-aa25-77b743b8f0ca",
    "workflow_id": "01838059-86d4-48af-a0b2-0cb006582728",
    "name": "initial",
    "description": "Initial setup state",
    "entry_text": "You are setting things up",
    "reminders": [],
    "monitors": [],
    "allowed_tools": ["bash", "read"],
    "transitions": [],
    "allow_exit": true,
    "ui_pos_x": 0.0,
    "ui_pos_y": 0.0,
    "created_at": "2026-10-27T19:18:16.308Z",
    "updated_at": "2026-10-27T19:18:16.308Z"
  }
}
```

### 8. `workflow_state_save`

Creates or updates an individual workflow state.

Parameters:
- `workflow_id` (string, required): Workflow ID or exact unique name.
- `state_id` (string, optional): State ID to update (creates a new state if omitted).
- `name` (string, optional): State name (required when creating a new state).
- `description` (string, optional): State description.
- `entry_text` (string, optional): Instructions injected into agent turn on state entry.
- `allow_exit` (boolean, optional): Whether exiting the workflow is allowed from this state.
- `allowed_tools` (array of strings, optional): Tool whitelist for this state.
- `reminders` (array of objects, optional): State-scoped reminder configs.
- `monitors` (array of objects, optional): State-scoped monitor configs.
- `transitions` (array of objects, optional): Allowed forward transitions (`[{"target_state_id": "...", "description": "..."}]`).
- `ui_pos_x` (number, optional): UI canvas X coordinate.
- `ui_pos_y` (number, optional): UI canvas Y coordinate.

Returns:
```json
{
  "status": "ok",
  "state": { ... }
}
```

### 9. `workflow_state_delete`

Deletes an individual workflow state.

Parameters:
- `state_id` (string, required): Workflow state ID to delete.

Returns:
```json
{
  "status": "ok",
  "deleted": true,
  "state_id": "50698d6f-abd3-45d9-b87d-cfebd384afc4"
}
```

---

## The Workflow Format (`mecha-workflows` v1 Bundle Schema)

Portable JSON bundle format used for export, import, git-sync, and programmatic definition.

### Top-Level Bundle Structure

```json
{
  "schema_version": 1,
  "exported_at": "2026-03-30T00:00:00Z",
  "workflow": {
    "name": "string (1-256 chars, unique)",
    "description": "string (max 4096 chars)",
    "snooze_duration_ms": 600000,
    "variables": [
      {
        "name": "string (1-64 chars: alphanumeric, _, -)",
        "label": "string (max 128 bytes)",
        "description": "string (max 512 bytes)",
        "type": "text | textarea",
        "required": false,
        "default_value": "string (max 2048 bytes)"
      }
    ]
  },
  "entry_state_key": "string (references a state.key)",
  "states": [
    {
      "key": "string (local identifier within bundle, unique per state)",
      "name": "string (1-256 chars)",
      "description": "string (max 4096 chars)",
      "entry_text": "string (max 65536 chars, instructions injected on state entry)",
      "is_entry_state": false,
      "allow_exit": false,
      "allowed_tools": ["string"],
      "reminders": [
        {
          "id": "string",
          "type": "one_shot | recurring",
          "subject": "string (optional)",
          "body": "string (max 16384 bytes)",
          "delay_seconds": 300,
          "interval_seconds": 600
        }
      ],
      "monitors": [
        {
          "id": "string",
          "name": "string",
          "description": "string",
          "interval_seconds": 300,
          "cooldown_seconds": 600,
          "max_alerts": 3,
          "trailing_messages_count": 10,
          "question_type": "boolean | multiple_choice | numeric",
          "question_instructions": "string",
          "condition_operator": "equals | greater_than | less_than",
          "condition_threshold": "string",
          "nudge_template": "string",
          "fallback_mode": "string"
        }
      ],
      "ui_pos_x": 0.0,
      "ui_pos_y": 0.0,
      "transitions": [
        {
          "target_state_key": "string (must match a state.key in states)",
          "description": "string (max 1024 chars, transition criteria shown to agent)"
        }
      ]
    }
  ]
}
```

### Text Interpolation Rules

Variable substitutions occur across `entry_text`, state descriptions, transition descriptions, and reminder bodies.

Supported syntax:
- `${var_name}`
- `{{ var_name }}` (with optional surrounding whitespace)

When a variable key matches, the runtime substitutes its string value. Undefined variables evaluate to empty strings.

---

## Concrete Example: Canonical dev-loop Bundle

Complete, valid `mecha-workflows` v1 JSON bundle for the canonical development loop.

```json
{
  "schema_version": 1,
  "exported_at": "2026-03-30T00:00:00Z",
  "workflow": {
    "name": "dev-loop",
    "description": "Canonical development loop: Specification -> Planning -> Implementation (TDD) -> Review & Validation.",
    "snooze_duration_ms": 600000,
    "variables": [
      {
        "name": "task_description",
        "label": "Task Description",
        "description": "Specific functional requirements and scope of the change.",
        "type": "textarea",
        "required": true,
        "default_value": ""
      },
      {
        "name": "base_branch",
        "label": "Base Branch",
        "description": "Target branch to diff against and merge into.",
        "type": "text",
        "required": false,
        "default_value": "origin/main"
      }
    ]
  },
  "entry_state_key": "state_spec",
  "states": [
    {
      "key": "state_spec",
      "name": "Specification",
      "description": "Define observable behavior, architecture, and analyze alternative approaches.",
      "entry_text": "Write SPEC.md in the worktree based on the task description:\n${task_description}\n\nRequirements:\n1. Specify observable inputs, outputs, state changes, and error conditions.\n2. Include high-level component design.\n3. Analyze several alternative approaches and justify the selected one.\n4. Send SPEC.md to supervisor via mailbox for review. Do not advance until approved.",
      "is_entry_state": true,
      "allow_exit": false,
      "allowed_tools": [
        "read",
        "write",
        "edit",
        "bash",
        "mecha_mailbox_send",
        "mecha_workflow_transition",
        "mecha_context",
        "scratchpad"
      ],
      "reminders": [],
      "monitors": [],
      "ui_pos_x": 100.0,
      "ui_pos_y": 150.0,
      "transitions": [
        {
          "target_state_key": "state_plan",
          "description": "Supervisor approved SPEC.md; proceed to implementation planning."
        }
      ]
    },
    {
      "key": "state_plan",
      "name": "Planning",
      "description": "Draft implementation plan mapped to SPEC.md and run robot-review passes.",
      "entry_text": "Draft PLAN.md covering all behaviors from SPEC.md.\n\nRequirements:\n1. Map every requirement to design items, file paths, and test strategies.\n2. Run robot-review on PLAN.md using a single reviewer covering all focus areas (unless complexity requires multiple).\n3. Validate reviewer output with pr-validate using a different persona.\n4. Address valid feedback within the 3-round robot-review cap.\n5. Send signed-off plan ledger to supervisor before implementing.",
      "is_entry_state": false,
      "allow_exit": false,
      "allowed_tools": [
        "read",
        "write",
        "edit",
        "bash",
        "mecha_mailbox_send",
        "mecha_workflow_transition",
        "subagent",
        "get_subagent_result",
        "list_subagents",
        "stop_subagent",
        "scratchpad"
      ],
      "reminders": [],
      "monitors": [],
      "ui_pos_x": 350.0,
      "ui_pos_y": 150.0,
      "transitions": [
        {
          "target_state_key": "state_spec",
          "description": "Fundamental requirement gaps discovered; return to specification."
        },
        {
          "target_state_key": "state_implement",
          "description": "PLAN.md reviewed and signed off; proceed to implementation."
        }
      ]
    },
    {
      "key": "state_implement",
      "name": "Implementation",
      "description": "Test-driven development, service execution, and behavior verification.",
      "entry_text": "Implement the planned changes following strict TDD:\n\n1. Write failing tests covering all behaviors in SPEC.md before writing production code.\n2. Implement code until tests pass.\n3. For multi-service or user-visible changes, run live services and verify end-to-end.\n4. Verify tests pass against ${base_branch}.\n5. Transition to review once implementation and direct proofs are complete.",
      "is_entry_state": false,
      "allow_exit": false,
      "allowed_tools": [
        "read",
        "write",
        "edit",
        "bash",
        "mecha_mailbox_send",
        "mecha_workflow_transition",
        "mecha_tool_timeout",
        "scratchpad"
      ],
      "reminders": [],
      "monitors": [],
      "ui_pos_x": 600.0,
      "ui_pos_y": 150.0,
      "transitions": [
        {
          "target_state_key": "state_plan",
          "description": "Architectural roadblock or scope mismatch encountered; return to planning."
        },
        {
          "target_state_key": "state_review",
          "description": "TDD complete, all tests passing, and behavior verified."
        }
      ]
    },
    {
      "key": "state_review",
      "name": "Review & Validation",
      "description": "Test coverage validation, implementation robot-review, and final readiness gates.",
      "entry_text": "Perform final validation and review on the working candidate:\n\n1. Run test-coverage skill on changed code; resolve blocking gaps.\n2. Run robot-review on the diff against ${base_branch} using one reviewer per focus area.\n3. Validate findings using pr-validate with independent personas.\n4. Fix valid findings within the 3-round cap across plan and implementation.\n5. For UI changes, verify live browser evidence.\n6. Mail supervisor the final review ledger and sign-off report before exiting.",
      "is_entry_state": false,
      "allow_exit": true,
      "allowed_tools": [
        "read",
        "write",
        "edit",
        "bash",
        "mecha_mailbox_send",
        "mecha_workflow_transition",
        "subagent",
        "get_subagent_result",
        "list_subagents",
        "stop_subagent",
        "scratchpad"
      ],
      "reminders": [],
      "monitors": [],
      "ui_pos_x": 850.0,
      "ui_pos_y": 150.0,
      "transitions": [
        {
          "target_state_key": "state_implement",
          "description": "Review uncovered bugs or regressions requiring implementation fixes."
        }
      ]
    }
  ]
}
```

---

## Operating Guidance

### Graph Topology: DAGs vs Cycles

- Workflows are directed graphs. While progression is predominantly forward (DAG-like), backward transitions (cycles) are supported and expected for rework loops (e.g. `Review -> Implementation`, `Planning -> Specification`).
- Every non-trivial workflow must define an entry state (`entry_state_key` or `is_entry_state: true`).
- At least one state in every terminal path must specify `allow_exit: true`. If no state permits exit, an agent cannot exit the workflow without manual administrative deactivation (`workflow_assign` with action `deactivate`).

### Reachability & Validation Rules

The router validates bundle integrity on import/save:
1. `entry_state_key` must resolve to an existing state key in the bundle.
2. All transition `target_state_key` references must resolve to valid states within the same workflow.
3. Duplicate state keys or duplicate workflow names are rejected.
4. Variable names must match `^[A-Za-z0-9_-]{1,64}$`.
5. Tool names in `allowed_tools` must exist in the canonical workflow tools catalog.

### Error Contracts

- **Invalid Transition**: If an agent requests `action="transition"` to a state not declared in the active state's transitions, the router rejects the call with an error listing the allowed targets.
- **Unauthorized Exit**: If an agent requests `action="exit"` from a state where `allow_exit == false`, the router rejects the call with an explicit error.
- **Tool Restriction Violation**: If an agent attempts to invoke a tool omitted from `allowed_tools`, tool admission rejects execution before invocation (system escape tools excepted).

### Supervisor vs Worker Responsibilities

- **Supervisor**:
  - Assigns workflows to workers via `workflow_assign` during launch or task routing.
  - Supplies initial variable bindings (`task_description`, `base_branch`, etc.).
  - Participates as an external review authority at gated transition boundaries (such as approving Phase 1 SPEC.md).
  - Can force state realignment or deactivation if a worker stalls or enters an unrecoverable loop.
- **Worker**:
  - Operates inside the boundaries set by the active state.
  - Follows injected instructions from `entry_text`.
  - Executes work towards completing the workflow under the implicit workflow goal.
  - Initiates state progression explicitly via `mecha_workflow_transition` when phase exit conditions are met.
  - Exits the workflow cleanly once final sign-off is confirmed in an exit-allowed state.
