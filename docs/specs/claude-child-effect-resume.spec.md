# Spec: Claude child single attempt

- **ID**: `SPEC-CLAUDE-CHILD-RESUME-001`
- **Status**: `Draft`
- **Domain**: `AgentManagement`

## Intent
A Claude child gets one attempt. A parent may start a new child after inspecting an interruption, but cannot resume the old child.

## Scenarios

### CER-001: Normal execution
A parent starts a Claude child. The existing scheduler runs it with scoped tools, checked completion and cancellation. The parent receives its result or failure.

### CER-002: Interrupted or uncertain execution
If execution is interrupted, cleanup or an effect outcome is uncertain, the child must not automatically resume or replay tools, including after a router restart. Failure does not imply that effects were rolled back. Existing authority and process cleanup checks remain in force.

### CER-003: Resume request
A parent requesting resume of a terminal Claude child receives an error without changing its attempt or state, whether its earlier outcome was checked or uncertain.

### CER-004: Previously started session
If a Claude child execution reaches launch with an already-started session, it fails closed rather than continuing that session. No new child process or tool effect is started.

## Verification
Focused store and runtime tests must cover rejection before state change and the started-session launch guard; browser tests for stopped children with checked and interrupted effects, including restart, require validation.

## Remaining boundaries
Capacity accounting is manager-local and does not coordinate replacement managers; manager replacement cases remain unaccepted. The bridge still matches each scoped MCP request to its live tool call and persists the result in the ordinary transcript. There are no witness migrations in this copy and child execution does not use witness tables. The one-attempt restriction also applies to pre-release startup failures even when no execution occurred; the parent must spawn a new child.
