# SPEC-SUBAGENT-RESTRICTION-REMOVAL-001: Subagent Usage Restriction Removal

## 1. Overview
Removes the mandatory subagent usage restriction across embedded and Pi workers. Workers are free to choose when to execute tools inline (such as simple bash commands, file edits, or quick tasks) versus delegating to subagents (for parallel execution, large refactors, or isolated exploration).

Inline tools (`bash`, `edit`, `write`) are unrestricted by default across both embedded and Pi runtimes upon agent launch. Prompts, templates, and tool descriptions no longer mandate subagents or threaten tool allowance revocation.

## 2. Background & Problem
Previously, managed workers operated under a strict default-deny policy for inline action tools (`bash`, `edit`, `write`). Newly launched workers defaulted to `allow_inline_tools: false`, subjecting tool execution to a token-bucket allowance mechanism (`mecha_request_inline_tool_allowance`) with a maximum pool of 3 credits. Once exhausted, tool calls were blocked with explicit errors stating:
"Inline tool budget exhausted: you have used all available inline tool credits. Inline tools are intended only for quick, occasional actions; repeated inline tool use is disallowed. You must delegate this work to subagents instead."

Furthermore, system prompts and prompt templates explicitly instructed:
"Workers: Use subagents for actual work" and warned "Do not abuse the inline tool allowance, or it will be disabled."

This created unnecessary overhead, latency, and token bloat for simple edits and quick commands.

## 3. Behavioral Scenarios

### Scenario 1: Embedded Worker Executes Inline Tools Without Restriction (Happy Path)
- **GIVEN** a newly launched embedded worker agent created without an explicit `allow_inline_tools` parameter
- **WHEN** the agent issues multiple successive inline tool calls (`bash`, `edit`, or `write`)
- **THEN** each tool call executes successfully
- **AND** the tool calls are not blocked by credit exhaustion or the inline allowance gate
- **AND** no error or warning demanding subagent delegation is emitted.

### Scenario 2: Pi Worker Executes Inline Tools by Default (Happy Path)
- **GIVEN** a newly launched Pi worker agent created via `POST /v1/managed-agents` without specifying `allow_inline_tools`
- **WHEN** the agent record is stored and projected
- **THEN** `allow_inline_tools` is set to `true` by default
- **AND** inline action tools are admitted freely.

### Scenario 3: Workers Can Still Freely Choose Subagents (Happy Path)
- **GIVEN** an active embedded or Pi worker agent
- **WHEN** the worker chooses to launch a subagent via the `subagent` tool for parallel execution or large tasks
- **THEN** the subagent is launched, runs in the background, and reports completion normally.

### Scenario 4: Request Inline Tool Allowance Tool Invocations Return Unrestricted Status
- **GIVEN** an agent session where `mecha_request_inline_tool_allowance` is called
- **WHEN** the tool is executed
- **THEN** it returns that inline tools are unrestricted and calls are permitted freely without requiring an allowance
- **AND** no error message warning that "subagents remain mandatory by default" is returned.

### Scenario 5: Explicit Disabling of Inline Tools Remains Supported (Edge Case)
- **GIVEN** an agent where `allow_inline_tools` is explicitly set to `false` via `POST /v1/managed-agents/{agent_id}/allow-inline-tools` or `PUT /v1/managed-agents/{agent_id}/inline-tools`
- **WHEN** the agent attempts repeated inline tool calls without an allowance
- **THEN** the specific agent can still be restricted to subagents if explicitly configured by an operator.

## 4. Verification & E2E Mapping
- `router-proxy/internal/store/managed_allow_inline_tools_test.go`: Verifies store launch defaults `AllowInlineTools` to `true`.
- `router-proxy/internal/transport/managed_allow_inline_tools_http_test.go`: Verifies HTTP `POST /v1/managed-agents` defaults `allow_inline_tools` to `true`.
- `router-proxy/internal/managed/embedded_inline_allowance_test.go`: Verifies unrestricted inline tool execution without credit gating.
- `router-proxy/internal/managed/embedded_unjailed_tools_test.go`: Verifies unjailed tool execution for embedded workers.
