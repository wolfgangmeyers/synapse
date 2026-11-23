# Spec: Bounded Automatic Goal Wakeups

- **ID**: `SPEC-GOAL-001`
- **Status**: `Draft`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/goal-wake-safety.spec.ts`

## 1. Intent & User Outcome

An operational worker may continue its goal automatically without rapidly retrying failed turns. The first legitimate idle wake may be immediate; repeated automatic attempts are paced with a default 10-second delay and capped at 20 attempts in a consecutive automatic chain. Session-owner failure stops automatic wakeups until deliberate recovery establishes valid ownership. Failed automatic attempts cannot reset their own consecutive-chain count.

When a worker turn produces an empty response or thinking-only output without tool calls or visible text, the worker has not made actionable forward progress. Such turns are accounted as empty turns under the 3-strike circuit breaker. Strikes 1 and 2 trigger actionable re-nudge attempts that are exempted from the finite consecutive continuation cap (allowing recovery even when the cap is reached), while pacing preserves the re-nudge prompt across delays. On strike 3, continuation halts to prevent runaway empty loops.

This contract covers embedded standalone and implicit workflow goals. It refines the embedded idle rule in [implicit workflow goals](workflow-implicit-goals.spec.md). The selected repair applies the existing intended 10-second default delay and 20-attempt consecutive cap consistently to embedded automatic paths; it does not change stock Claude worker evaluator policy. Independent scheduled workflow launches remain governed by [structured schedules](SPEC-STRUCTURED-WORKFLOW-SCHEDULES.md).

## 2. Core Invariants

1. **First wake**: With valid ownership, active goal, no snooze and remaining consecutive-chain allowance, the first legitimate workflow idle wake may run immediately. Repeated idle notifications do not reset the chain.
2. **Repeated wake pacing**: A repeated automatic attempt waits until the preceding attempt settles plus the applicable delay (default `10,000 ms`), or snooze expiry, whichever is later. Successful incomplete turns and failures, including rejected admission, failed turn start, backend/model error and execution failure, obey this rule. Automatic attempts do not overlap.
3. **Finite consecutive chain**: At most `20` automatic attempts occur in a consecutive automatic chain, including the first immediate attempt and failed attempts before model execution. Failed automatic turns and their own idle/fallback/timer notifications cannot reset the count. At exhaustion, further automatic signals are suppressed. This is not a durable lifetime budget: ordinary deliberate user action retains existing reset semantics. No new cross-restart persistent counter or evaluator reinterpretation is required.
4. **Attempted versus suppressed**: An automatic attempt starts when an eligible wake proceeds into admission/turn-start work, even if that work rejects before a model request. A timer/idle notification checked and suppressed by delay, snooze, cap, cancellation or an existing ownership hold is not an attempt and consumes no allowance. The initial discovery of invalid ownership during admission counts as a failed attempt; subsequent checks of the resulting hold must not re-enter admission/provider dispatch. Count actual admission/turn-start entries separately from suppression checks and model calls in proof.
5. **Invalid ownership and recovery**: Invalid, obsolete, missing or conflicting ownership stops the automatic chain until deliberate valid recovery; it must not seize another live owner. Existing failure state may represent the hold. Deliberate user action retains existing reset/recovery semantics; valid ownership is required to resume, invalid recovery fails visibly without an automatic loop. No new fresh-budget recovery API or persistent hold schema is mandated.
6. **Goal continuity and cancellation**: Failure holds do not mark the goal done, clear it, unlock a workflow goal or advance workflow state. DONE, goal off and workflow exit/deactivation cancel continuation. Snooze, cap and ownership holds remain authoritative over stale wakes.
7. **Observable recovery state**: Existing conversation/status surfaces expose failure and delayed/stopped automation. No new UI layout or public schema is prescribed.
8. **Bounded bookkeeping**: Reuse bounded existing per-agent/goal state where semantics fit; no growing attempt history or retry backlog. Persistence is required only where existing semantics naturally support it, not as a new cross-restart guarantee. Clean up any new bounded state with its owning goal/agent; existing timeline retention is unchanged.
9. **Empty-turn re-nudge cap exemption**: When an agent emits an empty response or thinking-only turn without taking action (strikes 1 and 2 of the 3-strike circuit breaker, `0 < consecutiveEmptyTurns < 3`), the resulting re-nudge continuation attempt is exempted from the consecutive continuation cap (default 20). If the agent reaches strike 3 (`consecutiveEmptyTurns >= 3`), continuation is paused by the circuit breaker.
10. **Thinking-only turns accounted as empty turns**: Turns generating internal thinking content without tool calls and without visible assistant text do not constitute actionable progress. They do not set `hasText` during turn settlement across both value and pointer assistant messages, and are treated as empty turns subject to the 3-strike circuit breaker rather than silently consuming continuation quota.
11. **Pacing prompt preservation**: When an empty-turn re-nudge continuation is delayed due to pacing (`clock.Now().Before(lane.goalNextAttempt)`), the specific actionable re-nudge prompt (`overridePrompt`) is preserved across the scheduling delay and dispatched when the timer fires.

The 10-second value is the default pacing policy, not a claim about existing constants. Existing supported delay overrides remain positive and obey snooze and ownership safety; no new configuration API is required. The cap is 20 consecutive automatic attempts, not 20 retries plus an initial attempt. This incident repair does not mandate a new schema, lifetime budget, user-message reset prohibition or reinterpretation of existing evaluator policy.

## 3. User Scenarios (Behavioral Contract)

Every case has **Mode: real-capable**. External LLM/evaluator services use deterministic local fixtures, including failure responses and connection errors; router scheduling and ownership are real.

### Scenario: [SCN-GWS-001] First immediate wake and paced healthy continuation
- **User Goal**: Continue useful work automatically without a tight loop.
- **Given**: A valid ready owner has a newly active workflow goal, no snooze and a fresh consecutive automatic chain.
- **When**: The initial normal turn settles incomplete and automatic continuations also settle incomplete before a later DONE result.
- **Then**: The first automatic idle wake may be immediate; later automatic attempts wait at least the default 10 seconds after the preceding attempt settles. DONE stops automation, including pending stale wakes.

### Scenario: [SCN-GWS-002] Failure does not bypass pacing
- **User Goal**: Avoid rapid retries on failed turns.
- **Given**: Valid ownership and remaining consecutive-chain allowance; fixtures separately reject admission, fail turn start, fail model/backend and fail execution after start.
- **When**: An automatic attempt fails at time `T` and repeated idle/timer signals occur before `T + 10 seconds` under default policy.
- **Then**: No additional automatic attempt occurs before that deadline; at eligibility at most one starts, and a subsequent failure establishes another full delay. Failure is visible, not reported as completion.

### Scenario: [SCN-GWS-003] Failures exhaust the 20-attempt consecutive-chain cap
- **User Goal**: Stop unattended failure loops.
- **Given**: A fresh consecutive automatic chain and every automatic attempt fails, including attempts before model execution.
- **When**: Controlled time advances through 20 attempts and several additional retry intervals with repeated idle signals.
- **Then**: Exactly 20 automatic attempts occurred, including the first; no 21st attempt occurs. The goal remains incomplete and the user can see automation stopped at its cap.

### Scenario: [SCN-GWS-004] Session-owner failure stops automatic spin
- **User Goal**: Stop retrying an invalid session.
- **Given**: An automatic wake fails due to invalid, obsolete, missing or conflicting session ownership.
- **When**: Idle signals repeat and controlled time advances beyond delay and snooze deadlines.
- **Then**: No further automatic turn/admission attempt or model request targets that binding; the ownership failure is visible and live ownership elsewhere is unchanged.

### Scenario: [SCN-GWS-005] Repeated automatic signals cannot renew their own chain
- **User Goal**: Avoid an unattended failure storm.
- **Given**: A consecutive automatic chain is delayed, cap-exhausted or held for invalid ownership.
- **When**: Its repeated idle, fallback and timer signals arrive without deliberate user action.
- **Then**: Delay/cap suppress admission without consuming attempts; ownership-hold checks do not reacquire or dispatch. Failed automatic turns cannot reset the chain. This case does not require a durable counter or restart-persistence expansion; mailbox restart proof remains in SCN-MSR-003–004.

### Scenario: [SCN-GWS-006] Deliberate user recovery restores useful work
- **User Goal**: Resume after correcting a failure.
- **Given**: Automation stopped after cap exhaustion or ownership failure and the goal remains active.
- **When**: The user deliberately requests recovery/resume, ownership is validated and the local backend becomes healthy.
- **Then**: Work can resume under the finite consecutive-chain cap and existing deliberate-user reset semantics without repeating committed mail effects. Workflow goal/lock remain intact until legitimate exit. If ownership remains invalid, recovery fails visibly and starts no automatic loop. Ordinary user messages retain existing reset semantics; no message-reset prohibition or dedicated fresh-budget control is introduced.

### Scenario: [SCN-GWS-007] Snooze and cancellation override pending wakes
- **User Goal**: Retain control over execution.
- **Given**: An automatic retry is pending after failure.
- **When**: Separate cases snooze beyond retry eligibility, turn off an unlocked goal, or exit/deactivate the workflow through permitted controls.
- **Then**: Snooze blocks execution until expiry and still respects cap/delay/ownership holds; goal off and workflow exit cancel continuation entirely. Manual workflow-locked goal changes remain rejected.

### Scenario: [SCN-GWS-008] Empty-turn recovery re-nudges bypass consecutive continuation cap on strikes 1 and 2
- **User Goal**: Allow an agent that reached the consecutive continuation cap to receive an actionable re-nudge when it emits an empty response.
- **Given**: An agent has reached the consecutive continuation cap (`consecutiveContinuations >= 20`) with valid ownership and emits an empty response (or thinking-only turn without text or tool calls), recording strike 1 or strike 2 of the circuit breaker.
- **When**: The router initiates an empty-turn recovery re-nudge with an actionable prompt.
- **Then**: The re-nudge continuation attempt bypasses the consecutive continuation cap and dispatches the recovery turn. If the agent emits a third consecutive empty response (strike 3), the circuit breaker trips and pauses continuation.

### Scenario: [SCN-GWS-009] Thinking-only turns are treated as empty turns under circuit breaker accounting
- **User Goal**: Prevent agents emitting only silent thoughts from consuming continuation quota indefinitely without forward progress.
- **Given**: An active goal is running and the model emits an assistant message containing only thinking tokens (no tool calls and no visible assistant text).
- **When**: The router evaluates the settled turn.
- **Then**: The turn is evaluated as an empty response (`toolCalls == 0 && !hasText`), incrementing the consecutive empty turns counter and triggering an actionable re-nudge (or halting at strike 3).

### Scenario: [SCN-GWS-010] Paced empty-turn re-nudge retains actionable prompt across delay
- **User Goal**: Ensure the actionable prompt crafted for empty-turn recovery is preserved when the recovery attempt is delayed by pacing.
- **Given**: An empty turn triggers a re-nudge continuation with a specific actionable prompt (`overridePrompt`), but the agent lane is within its pacing delay interval.
- **When**: The continuation is scheduled for the pacing deadline and subsequently wakes when the deadline arrives.
- **Then**: The continuation dispatches with the original actionable override prompt rather than reverting to a generic wakeup prompt.

## 4. Automated E2E Verification Matrix

Actual executable: `tests/e2e/goal-wake-safety.spec.ts` contains one TC, `SCN-GWS-002/003: failed automatic turns are paced and exhaust twenty attempts` (**Mode: real-capable**; executed with local Codex mock). It exercises real browser/WS/REST/router-proxy/SQLite, default boundary pacing and twenty provider-error attempts. This is partial SCN-GWS-002/003 proof, not all pre-model failures. Supporting Go guards: `TestGoalWakeSafetyFailedIdleAttemptsArePaced`, `TestGoalWakeSafetyFailedRecoveryEvictsOriginOwner`, `TestGoalWakeSafetyGoalOffCancelsFailedRetry`. They do not replace actor-visible acceptance. The seven titles below remain planned; no exact-title executable mapping is claimed. GWS-001/004/005/006, full GWS-007 and remaining GWS-002/003 branches are not run at browser acceptance level.

Execution evidence and proof gaps: [delivery report](reports/report-20261105-1650-main.md). Planned acceptance remains unchanged. All dependencies must be deterministic/local in automated tests.

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `SCN-GWS-001` | Healthy wake/completion | `tests/e2e/goal-wake-safety.spec.ts` | `first idle wake is immediate and subsequent wakes are paced` |
| `SCN-GWS-002` | All failure phases paced | `tests/e2e/goal-wake-safety.spec.ts` | `failed automatic attempts respect ten second default delay` |
| `SCN-GWS-003` | Failure cap | `tests/e2e/goal-wake-safety.spec.ts` | `pre-model and turn failures exhaust twenty attempt cap` |
| `SCN-GWS-004` | Invalid-owner hold | `tests/e2e/goal-wake-safety.spec.ts` | `session owner failure stops automatic wakeups` |
| `SCN-GWS-005` | Automatic signals preserve chain safety | `tests/e2e/goal-wake-safety.spec.ts` | `suppressed idle and timer checks do not renew automatic chain` |
| `SCN-GWS-006` | User recovery | `tests/e2e/goal-wake-safety.spec.ts` | `user recovery resumes only with valid ownership` |
| `SCN-GWS-007` | Snooze/cancellation | `tests/e2e/goal-wake-safety.spec.ts` | `snooze goal off and workflow exit override retry` |
| `SCN-GWS-008` | Empty-turn recovery bypasses continuation cap | `router-proxy/internal/managed/embedded_goal_continuation_test.go` | `TestEmptyTurnRecoveryBypassesConsecutiveContinuationCap` |
| `SCN-GWS-009` | Thinking-only turn empty turn accounting | `router-proxy/internal/managed/embedded_session_test.go` | `TestThinkingOnlyTurnTreatedAsEmptyTurn` |
| `SCN-GWS-010` | Pacing preserves override prompt | `router-proxy/internal/managed/embedded_goal_continuation_test.go` | `TestEmptyTurnRecoveryPreservesPromptAcrossPacing` |

Existing `tests/e2e/workflow-implicit-goals.spec.ts` is the regression target for workflow goal/lock continuity; `tests/e2e/background-workflows.spec.ts` protects independent schedule cadence. Their passing executable results are recorded separately in the delivery report and do not discharge this matrix.
