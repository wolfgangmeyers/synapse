# Spec: Claude managed subagent lifecycle

- **ID**: `SPEC-CLAUDE-SUBAGENT-001`
- **Status**: Draft
- **Domain**: AgentManagement
- **Target E2E Suite**: `tests/e2e/claude-managed-subagent-lifecycle.spec.ts`

## Intent & User Outcome
A Claude worker can delegate to a router-managed subagent, retrieve its completed result, and obey the worker's router-tool settings.

## Core Invariants
1. A permitted checked spawn creates a child tied to the parent; the child executes on the configured router default model.
2. A completed child's result is available to its parent through the checked result tool. Its completion notice is delivered to the Claude process and acknowledged through the normal checked mailbox path.
3. Pi children use the configured provider transport and credentials. The planned Claude-default child uses the locally authenticated CLI without Pi provider credentials, subject to [CD-006–007](claude-router-default.spec.md); that behavior is not yet accepted.
4. Disabling the router subagent tool blocks new spawns without affecting Claude native tools.

## User Scenarios
### Scenario: [CS-001] Spawn and retrieve a child
- **Given** a Claude worker and a configured router default model.
- **When** the worker calls the router subagent tool and later retrieves the completed child's result.
- **Then** the child runs, its completion notice reaches and is acknowledged by Claude, its result is persisted and returned, and the calls appear in checked history.

### Scenario: [CS-002] Restrict child spawning
- **Given** a Claude worker with the router subagent tool disabled in worker tools.
- **When** the worker calls that tool.
- **Then** the call is denied and no new child is created.

### Scenario: [CS-003] Missing router default
- **Given** a Claude worker and no configured router default model.
- **When** the worker calls the router subagent tool.
- **Then** the call explains that a router default is required and creates no child.

### Scenario: [CS-004] View and stop a managed child
- **Given** a Claude worker has a running router-managed child.
- **When** the user opens the Subagents card and stops the child.
- **Then** the child is visible in the viewer and the stop request is sent for that child. Claude native subagents are not represented as router-managed children.

## Automated E2E Verification Matrix
| Scenario | Suite | Test |
| --- | --- | --- |
| CS-001 | `tests/e2e/claude-managed-subagent-lifecycle.spec.ts` | `Claude managed subagent lifecycle and restriction` |
| CS-003 | `tests/e2e/claude-managed-subagent-lifecycle.spec.ts` | `Claude spawn requires a router default` |
| CS-002 | `tests/e2e/claude-managed-subagent-lifecycle.spec.ts` | `Claude managed subagent lifecycle and restriction` |
| CS-004 | `tests/e2e/claude-subagent-viewer-regression.spec.ts` | `view and stop a Claude managed child` |
