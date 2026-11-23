# Implementation Plan - Empty-Turn Recovery Re-Nudge & Continuation Cap Exemption

## 1. Problem Summary & Motivation
In GitHub issue #135, an agent running Gemini 4 Hydrogen generated consecutive thinking-only turns, incrementing `lane.consecutiveContinuations` until reaching the cap of 20 (`defaultMaxConsecutiveGoalContinuations = 20`). On turn 300, it emitted an empty response (`content: []`, strike 1/3). The router attempted to re-nudge the agent with an actionable prompt (`startEmbeddedGoalContinuationWithPrompt`), but `goalAttemptAllowed` rejected the re-nudge because `consecutiveContinuations >= 20`. The agent then settled into `idle`, where both idle goal continuations and workflow wakeups were permanently blocked (`consecutiveEmptyTurns > 0` prevents idle continuation, while `consecutiveContinuations >= 20` prevents normal goal attempts).

Furthermore, thinking-only turns in `embedded_session.go` set `hasText = true`, which prevented them from being recognized as empty/non-actionable turns. Consequently, thinking-only turns consumed the 20-turn continuation budget without making visible forward progress or triggering the 3-strike circuit breaker.

## 2. Requirements & Invariants
1. **Empty-Turn Re-Nudge Dispatch Guarantee**: Empty-turn circuit-breaker re-nudges (strikes 1 and 2 of the 3-strike circuit breaker, i.e., `0 < consecutiveEmptyTurns < 3`) must not be suppressed by the general consecutive continuation cap (`consecutiveContinuations >= 20`).
2. **Circuit Breaker Integrity**: On strike 3 (`consecutiveEmptyTurns >= 3`), continuation must pause as designed by the circuit breaker.
3. **Pacing & Ownership Enforcement**: Empty-turn re-nudges must still respect valid session ownership (`!lane.goalOwnerInvalid`) and rate pacing (`!m.clock.Now().Before(lane.goalNextAttempt)`).
4. **Prompt Preservation Across Pacing**: When an empty-turn re-nudge is scheduled due to rate pacing (`m.clock.Now().Before(lane.goalNextAttempt)`), the specific actionable re-nudge prompt (`overridePrompt`) must be preserved when the wake fires.
5. **Thinking-Only Turn Accounting**: Turns that produce only thinking tokens without tool calls and without visible assistant text do not constitute actionable progress. They must not set `hasText = true` in turn settlement across both value (`ai.AssistantMessage`) and pointer (`*ai.AssistantMessage`) types. They are subject to empty-turn evaluation and the 3-strike circuit breaker rather than consuming continuation quota in silent loops.
6. **Workflow Nudge Consistency**: `workflow_nudge_accounting.go` must not treat thinking deltas as actionable responses to workflow nudges, and must support pointer variants (`*ai.TextContent`, `*ai.ToolCall`) consistently.

## 3. Technical Changes

### File 1: `router-proxy/internal/managed/embedded_goal_continuation.go`
- Add helper `goalAttemptAllowedForEmptyTurn(lane *managedAgentLane) bool`:
  ```go
  func (m *Manager) goalAttemptAllowedForEmptyTurn(lane *managedAgentLane) bool {
      return !lane.goalOwnerInvalid && !m.clock.Now().Before(lane.goalNextAttempt)
  }
  ```
- Support retaining `overridePrompt` across scheduled wakes:
  ```go
  func (m *Manager) scheduleEmbeddedGoalContinuation(binding store.EmbeddedBinding, key store.EmbeddedGoalEvaluationKey, due time.Time) {
      m.scheduleEmbeddedGoalContinuationWithPrompt(binding, key, due, "")
  }

  func (m *Manager) scheduleEmbeddedGoalContinuationWithPrompt(binding store.EmbeddedBinding, key store.EmbeddedGoalEvaluationKey, due time.Time, prompt string) {
      m.scheduleEmbeddedGoalWake(binding, key, due, func() { m.startEmbeddedGoalContinuationWithPrompt(binding, key, prompt) })
  }
  ```
- In `startEmbeddedGoalContinuationWithPrompt(binding, key, overridePrompt)`:
  - Check if this continuation is an empty-turn circuit-breaker recovery:
    ```go
    isEmptyTurnRecovery := (overridePrompt != "" || (lane.consecutiveEmptyTurns.Load() > 0 && lane.consecutiveEmptyTurns.Load() < 3)) &&
        lane.consecutiveEmptyTurns.Load() > 0 && lane.consecutiveEmptyTurns.Load() < 3
    ```
  - When `isEmptyTurnRecovery` is true:
    - Use `goalAttemptAllowedForEmptyTurn(lane)`.
    - If rate-limited (`m.clock.Now().Before(lane.goalNextAttempt)`), schedule with prompt:
      `m.scheduleEmbeddedGoalContinuationWithPrompt(binding, key, lane.goalNextAttempt, overridePrompt)`.
    - Bypass the line 250 continuation cap check `if lane.consecutiveContinuations.Load() >= int64(maxContinuations)`.
  - Normal goal continuations continue to use `goalAttemptAllowed(lane)` and the line 250 cap check.

### File 2: `router-proxy/internal/managed/embedded_session.go`
- In `evaluateEmbeddedTurn`:
  - In both `case ai.AssistantMessage:` (lines ~2002-2010) and `case *ai.AssistantMessage:` (lines ~2036-2044):
    Remove `ThinkingContent` and `*ThinkingContent` from setting `hasText = true`.
  - Only `TextContent` with non-empty, non-placeholder text sets `hasText = true`.
  - This ensures thinking-only turns without text or tools have `toolCalls == 0 && !hasText && !calledGoalSnooze == true`, incrementing `consecutiveEmptyTurns` and triggering strikes 1 and 2 re-nudges, and strike 3 circuit-breaker pause.

### File 3: `router-proxy/internal/managed/workflow_nudge_accounting.go`
- In `observeClaude`, add pointer variants `*ai.TextContent` and `*ai.ToolCall` alongside value types for consistency with `embedded_session.go`. Thinking content remains excluded from eligible workflow responses.

### File 4: Behavioral Specification (`docs/specs/goal-wake-safety.spec.md` & `INDEX.md`)
- Update `docs/specs/goal-wake-safety.spec.md` with:
  - Invariant on empty-turn re-nudge continuation-cap exemption.
  - Invariant on thinking-only turn accounting and circuit-breaker evaluation.
  - Scenarios covering empty-turn recovery when cap is reached.

## 4. Verification & Testing
1. **Unit Test**: `TestEmptyTurnRecoveryBypassesConsecutiveContinuationCap` in `router-proxy/internal/managed/embedded_goal_continuation_test.go`:
   - Setup: Lane with `consecutiveContinuations = 20` (at cap) and `consecutiveEmptyTurns = 1` (strike 1).
   - Assertion: `startEmbeddedGoalContinuationWithPrompt` allows dispatch.
   - Setup: Lane with `consecutiveContinuations = 20` and `consecutiveEmptyTurns = 2` (strike 2).
   - Assertion: `startEmbeddedGoalContinuationWithPrompt` allows dispatch.
   - Setup: Lane with `consecutiveEmptyTurns = 3` (strike 3 circuit breaker).
   - Assertion: Circuit breaker pauses continuation.
2. **Unit Test**: Thinking-only turn evaluation in `router-proxy/internal/managed/embedded_session_test.go`:
   - Value and pointer messages with only `ai.ThinkingContent` without text or tools are treated as empty turns (`consecutiveEmptyTurns` incremented).
   - Value and pointer messages with `ai.TextContent` are treated as non-empty.
3. **Unit Test**: Preserving `overridePrompt` across pacing timer in `embedded_goal_continuation_test.go`.
4. **Integration / Full Suite Run**:
   - Run `go test ./router-proxy/internal/managed/...` to verify all tests pass without regression.
