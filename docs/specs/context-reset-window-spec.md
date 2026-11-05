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

## Browser reset overlap admission

- When a reset coordinator is still running (including its wake), a new browser reset with a distinct request ID returns not-ready and creates no replayable operation. Retrying that ID after the coordinator finishes can start a reset.
- An already accepted request replays its original response even while a coordinator is busy; reusing its ID with a different path or body returns an idempotency conflict.
- If durable reservation fails, the failed request releases its own coordinator without releasing another reset or tool gate. A replay discovered during reservation likewise releases only the coordinator it acquired.
- A newly reserved operation hands coordinator ownership to its reset task. Admission is never held while waiting for the serial lane.

### Scenario: Committed tool reset overlaps browser reservation

- **Given** a tool reset result has been successfully checked and committed on the current binding, and its turn is draining.
- **When** a browser reset coordinator begins before its durable reservation resolves, and the tool turn finishes.
- **Then** the committed tool request and its exact admission gate remain owned until the reservation outcome is known. No path waits for that outcome while holding the serial, admission, or selection lock.
- **And when** the browser reservation fails or replays an existing operation, the tool request performs one rotation and one fresh wake, subject to the current-binding fence.
- **And when** the browser reservation succeeds, the accepted browser coordinator performs one rotation and one fresh wake; it must not cause a second rotation for the same old binding.
- **And** old-binding callbacks cannot append or run after rotation; a failed checked tool result does not initiate a tool reset.

### Scenario: Mail arrives during a provisional reset gate

- **Given** an embedded worker is runnable and a reset reservation has closed admission.
- **When** a mail offer encounters that gate and the reservation fails or replays an existing operation,
- **Then** the queued mail is offered again after admission reopens, without another external message.
- **And** overlapping reset, tool-reset, or termination gates remain authoritative until their own release; uncertain Claude input remains held and is not replayed.

### Scenario: Reset and termination gates release queued mail safely

- **Given** a mail dispatch encounters a reset or termination admission gate,
- **When** that gate releases without unconfirmed process cleanup,
- **Then** eligible queued mail is offered once through the mail-only path; retry backoff is not bypassed and an unrelated idle goal is not awakened.
- **When** Claude process cleanup cannot be confirmed during an external or tool-initiated reset, including when recording that failure also fails,
- **Then** releasing the reset coordinator does not re-offer mail. Uncertain input remains held; a failure to persist is not evidence that cleanup succeeded.
- **And** each overlapping gate remains authoritative until its own release.
