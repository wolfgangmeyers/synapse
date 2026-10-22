# Plan: Configurable History Window for Context Reset

## Approach & Design Decisions
1. **Schema & Tool Execution**:
   - In `embedded_context_reset.go`:
     - Update `embeddedContextResetSchema()`:
       ```go
       ai.Opt("window", ai.Integer("Number of recent messages to preserve across the context reset (optional, default 20, max 100)."))
       ```
     - Define `const DefaultContextResetWindow = 20` and `const MaxContextResetWindow = 100`.
     - In `embeddedContextResetTool`: extract `window` from `args["window"]`. Support float64 (from JSON unmarshaling), int, int64, json.Number. Clamp: if <= 0, window = DefaultContextResetWindow; if > MaxContextResetWindow, window = MaxContextResetWindow.
     - Update callback signature: `requested func(reason string, window int)` or pass a request struct `contextResetRequest{Reason: string, Window: int}`.

2. **Manager State**:
   - In `supervisor.go`:
     - Define struct:
       ```go
       type contextResetRequest struct {
           Reason string
           Window int
       }
       ```
     - Change `contextResetRequested map[string]string` to `contextResetRequested map[string]contextResetRequest` in `Manager` struct and its initialization.
   - In `embedded_session.go`:
     - Update the `resetCB` closure to accept `(reason string, window int)` and store in `m.contextResetRequested[owner.binding.AgentID]`.

3. **Context Reset Logic**:
   - In `embedded_context_reset.go`:
     - Signature: `func (m *Manager) ExecuteContextReset(ctx context.Context, binding store.EmbeddedBinding, reason string, window int) error`
     - Also update package-level `ExecuteContextReset(ctx context.Context, binding store.EmbeddedBinding, reason string, window int) error`
     - Window clamping logic in `ExecuteContextReset`:
       ```go
       if window <= 0 {
           window = DefaultContextResetWindow
       } else if window > MaxContextResetWindow {
           window = MaxContextResetWindow
       }
       ```
     - Porting logic:
       ```go
       var portedMessages []agent.AgentMessage
       if len(sessionMessages) <= window {
           portedMessages = sessionMessages
       } else {
           startIdx := len(sessionMessages) - window
           for startIdx < len(sessionMessages) && sessionMessages[startIdx].MessageRole() == ai.RoleToolResult {
               startIdx++
           }
           if startIdx < len(sessionMessages) {
               portedMessages = sessionMessages[startIdx:]
           }
       }
       ```
   - In `checkContextResetAfterTurn`:
     - If `resetRequested`: call `m.ExecuteContextReset(ctx, binding, resetReq.Reason, resetReq.Window)`.
     - If `threshold_exceeded`: call `m.ExecuteContextReset(ctx, binding, "threshold_exceeded", DefaultContextResetWindow)`.
   - In `embedded_reset.go`:
     - Call `m.ExecuteContextReset(resetCtx, owner.binding, "gear_popup_requested", DefaultContextResetWindow)`.

4. **Integration & Regression Tests**:
   - In `embedded_context_reset_test.go`:
     - Update all existing calls to `mgr.ExecuteContextReset(...)` to pass window (0 or explicit).
     - Update `TestContextResetPortsLastFewMessages`: epoch 1 has 8 session messages (4 user + 4 assistant). Since 8 <= 20, all 8 messages will be ported if window is default 20. We can check that all 8 are ported, OR test with an agent that produces 25 messages and verify exactly 20 are ported by default!
     - Add explicit integration test: `TestContextResetWindowOptions` testing:
       - Default window of 20 when window is 0 or omitted.
       - Custom window (e.g. 5, 10).
       - Max window clamping (> 100 clamped to 100).
       - Invocation of `context_reset` tool with `window` argument during an embedded agent session, verifying the ported count across epochs.

## Regression Guarding
- Tests will ensure that when an agent requests a specific window (e.g., 5 messages), only 5 messages are carried over into the new epoch, avoiding context bloat.
- Tests will ensure default remains 20 (up from 6) across reset boundaries.
- Tests will guard against `RoleToolResult` orphaning when a custom window starts on a tool result.
