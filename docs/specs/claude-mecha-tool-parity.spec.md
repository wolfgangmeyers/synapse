# Spec: Claude Mecha tools and policy

- **ID**: `SPEC-CLAUDE-TOOLS-001`
- **Status**: Draft
- **Domain**: AgentManagement
- **Target E2E Suite**: `tests/e2e/claude-code-tool-parity.spec.ts`

## Intent & User Outcome
Claude workers can use the same applicable router-owned tools and configuration as embedded workers, without restarting a running Claude process when policy changes.

## Core Invariants
1. Router tools use the shared checked execution, role, schema, workflow and worker policy paths; a catalog entry is not execution authority.
2. Claude native tools are not governed by router inline or worker/workflow tool restrictions. Inline controls must not suggest otherwise.
3. Policy changes take effect on subsequent router tool calls, including after a previously disabled tool is enabled, without a policy-triggered restart of an active process. Existing normal between-turn process lifecycle is unchanged.
4. Router tool calls remain tied to the current process and admitted turn; stale and malformed calls cannot execute.

## User Scenarios
### Scenario: [CT-001] Discover and use shared router tools
- **Given** a running Claude worker with applicable router tools.
- **When** Claude discovers and calls a tool beyond the original limited catalog.
- **Then** it receives the shared tool result and the checked call and result appear in its history.

### Scenario: [CT-002] Configure worker or workflow tools
- **Given** a running Claude worker and its tool configuration controls.
- **When** the user disables a router tool, or selects a workflow that disallows it.
- **Then** subsequent calls are denied by shared policy; when the user re-enables it or changes the workflow, subsequent permitted calls succeed without a policy-triggered restart.

### Scenario: [CT-003] Native tools are not restricted by router policy
- **Given** a Claude worker.
- **When** the user inspects tool controls.
- **Then** inline tool restriction controls are hidden and worker/workflow configuration states that native Claude tools are not restricted.

### Scenario: [CT-004] Invalid or stale request
- **Given** a malformed, unlisted, or stale tool request.
- **When** it reaches the router.
- **Then** it is rejected without executing the tool.

### Scenario: [CT-005] Policy storage is unavailable
- **Given** a Claude worker invokes a router tool whose policy cannot be read.
- **When** workflow, parent workflow, or worker-tool settings lookup fails.
- **Then** the call is denied without executing the tool; a missing active workflow is not an error.

## Automated E2E Verification Matrix
| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name / coverage |
| --- | --- | --- | --- |
| CT-001 | Shared tool discovery and use | `tests/e2e/claude-code-tool-parity.spec.ts` | `Claude exposes shared tools and applies worker policy without restarting` (catalog, checked call and result) |
| CT-002 | Worker and workflow policy | `tests/e2e/claude-code-tool-parity.spec.ts` | `Claude exposes shared tools and applies worker policy without restarting` (worker re-enable covered; workflow change back to permitted remains untested) |
| CT-003 | Native tool controls | `tests/e2e/claude-code-tool-parity.spec.ts` | `Claude exposes shared tools and applies worker policy without restarting` (worker disclosure and inline controls; workflow disclosure remains untested) |
| CT-004 | Invalid and stale requests | `tests/e2e/claude-code-workers.spec.ts` | `CC-006 missing tool ID or checked producer cannot mutate scratchpad`; `CC-008 idle grant cannot reuse foreground authority for effects` (malformed and unlisted requests remain untested) |
| CT-005 | Policy storage unavailable | `tests/e2e/claude-code-tool-parity.spec.ts` | `Claude denies a checked tool when worker policy storage fails` (worker lookup; workflow and parent lookup E2E remain untested) |

Supporting Go test `TestAdmissionFailsClosedOnPolicyReadErrors` covers workflow, parent workflow and worker lookup failures. Missing active workflow is exercised by the ordinary permitted call in CT-001/002. These are coverage mappings, not claims that tests have been run.
