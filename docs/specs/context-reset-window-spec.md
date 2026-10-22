# Specification: Configurable History Window for Context Reset

## Problem
Currently, when an agent or system triggers `context_reset`, the history porting logic in `ExecuteContextReset` defaults to porting at most 6 messages from the prior session context:
```go
if len(sessionMessages) <= 6 {
    portedMessages = sessionMessages
} else {
    startIdx := len(sessionMessages) - 6
    ...
```
A hardcoded window of 6 messages is often too narrow to maintain conversational continuity, especially when turns involve tool calls, tool results, and multi-step interactions. Additionally, agents cannot control the window size for their specific context-reset needs.

## Requirements
1. **Default Window of 20**: When `context_reset` is called without specifying a window (or with window <= 0), the default number of recent messages to port from the prior epoch shall be 20.
2. **Optional `window` Parameter in Tool Schema**: The `context_reset` tool schema must accept an optional `window` integer parameter:
   - Tool description/schema property: optional integer, default 20, max 100.
3. **Parameter Extraction and Clamping**:
   - When extracted in `embeddedContextResetTool`, if `window` is missing, 0, or negative, default to 20.
   - If `window` exceeds 100, clamp it down to 100.
   - If `window` is specified in range 1..100, use that exact window value.
4. **State Storage in Manager**:
   - `contextResetRequested` map in `Manager` shall store a struct `{ Reason string, Window int }` instead of a plain string.
5. **ExecuteContextReset Execution**:
   - `ExecuteContextReset` signature and callers shall accept `window int` (or options struct containing `window`).
   - If `window <= 0`, default to 20. If `window > 100`, clamp to 100.
   - Port the last `window` messages from `sessionMessages`, maintaining the existing orphan `RoleToolResult` check (i.e. skipping forward if the start index falls on a `RoleToolResult`).
6. **All Callers Updated**:
   - Tool invocation callback passes extracted window.
   - `checkContextResetAfterTurn` passes stored window for agent-requested resets, or default 20 for threshold auto-resets.
   - `embedded_reset.go` (gear popup requested reset) passes default 0/20.
   - All tests calling `ExecuteContextReset` updated accordingly.
7. **Integration & Regression Testing**:
   - Verify default window of 20 messages is ported.
   - Verify custom window (e.g. 5, 10) ports exactly that number (subject to orphan tool result rule).
   - Verify window clamping (> 100 clamped to 100).
   - Verify tool invocation with `window` argument triggers reset with specified window.
   - All tests in `router-proxy/internal/managed/...` pass.

## Constraints
- Preserve single-user workstation and single branch workflow (`main` in `mecha-developer`).
- Preserve unrelated dirty files in working tree (`embedded_required_tools_registry_test.go`, etc.).
- Maintain orphan `RoleToolResult` skipping logic so tool result messages are never orphaned at the start of ported history.

## Acceptance Criteria
- AC1: `embeddedContextResetSchema()` includes `window` integer property.
- AC2: `contextResetRequested` stores reason and window.
- AC3: `ExecuteContextReset` uses clamped window with default 20 and max 100.
- AC4: Default window tests pass and verify up to 20 messages ported.
- AC5: Custom window and clamping tests pass.
- AC6: Full package test suite `go test ./internal/managed/...` passes.
