# Spec: Deterministic Embedded Own Reminder Delays & Idempotency

- **ID**: `SPEC-LIFECYCLE-007`
- **Status**: `Active`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `router-proxy/internal/store/embedded_own_reminder_test.go`

---

## 1. Intent & User Outcome

Managed embedded agents frequently set self-reminders to wake up, recheck asynchronous external operations, or retry interrupted operations after a designated period (e.g. "remind me in 5 minutes to verify subagent completion").

Historically, agents could supply either an absolute ISO timestamp (`due_at`) or a relative delay duration (`delay_minutes`, `delay_seconds`, `delayMinutes`). However, converting relative delays to absolute timestamps inside argument parsing logic introduced non-deterministic behavior: calls evaluated at slightly different millisecond intervals generated mismatched timestamp strings, breaking pre-execution effect intent verification (`embedded_effects`) and call ledger idempotency (`args_digest`).

This specification establishes that:
1. `mecha_reminder` provides first-class support for creating and updating reminders using relative delays (`delay_minutes`, `delay_seconds`, `delayMinutes`) in addition to absolute timestamps (`due_at`).
2. Parsing reminder arguments (`ParseEmbeddedOwnReminderArguments`) is purely deterministic and mathematical: it never consults system or wall clocks to compute absolute timestamps.
3. Pre-execution effect intent registration and call ledger idempotency checks (`args_digest`) operate reliably across replays without timestamp divergence when relative delays are supplied.
4. Exact absolute due time resolution occurs authoritatively only at execution time via the store clock when writing to the authoritative schedule.

---

## 2. Core Invariants

1. **Pure Argument Parsing**: `ParseEmbeddedOwnReminderArguments` MUST be a pure, deterministic function of its raw input bytes. It MUST NEVER read wall clocks, system time, or non-deterministic time sources during parsing.
2. **Relative Delay Representation**: When an agent provides relative delay parameters (`delay_minutes`, `delay_seconds`, or `delayMinutes`), the parsed representation preserves the relative delay in milliseconds (`DelayMS`) rather than synthesizing a non-deterministic `DueAt` timestamp.
3. **Pre-Execution Intent Parity**: Effect intent verification against `embedded_effects` and ledger idempotency hashing (`args_digest`) MUST compute identical representations for identical input arguments, ensuring replays and verification passes never diverge due to elapsed time between turns or retries.
4. **Authoritative Execution Clock**: The resolution of relative delays into concrete future timestamps (`due_at`) MUST occur authoritatively inside the store scheduling transaction using the store's configured clock (`s.clock.Now().UTC()`).
5. **Bounded Delay Horizon**: Relative delays and absolute timestamps MUST fall strictly within the future horizon: greater than zero and capped at 7 days (`7 * 24 * time.Hour`). Invocations exceeding 7 days MUST fail validation with a clear error.
6. **Mandatory Scheduling Target**: Reminder creation MUST specify either a valid future `due_at` or a positive delay duration (`delay_minutes`, `delay_seconds`, `delayMinutes`), along with a non-empty `body` (or `subject`/`title`). Missing both time specifications or missing body text MUST be rejected with a descriptive validation error.

---

## 3. User Scenarios (Behavioral Contract)

### Scenario: [SCN-001] Agent creates reminder with delay_minutes and verifies effect intent
- **User Goal**: Schedule a future self-reminder using a relative minute delay with reliable effect verification.
- **Given**:
  - An active embedded agent session binding exists with role `worker`.
  - The agent has not reached the 50 active reminder cap.
  - The store clock is fixed at $T_0$.
- **When**:
  - Agent registers tool intent in `embedded_effects` with argument `{"action":"create","body":"check worker task","delay_minutes":15}`.
  - The tool executes `EmbeddedOwnReminderAdmittedCall` with matching arguments.
- **Then**:
  - Pre-execution intent validation against `embedded_effects` matches without argument conflict.
  - The reminder is created with state `scheduled`.
  - The reminder's `next_due_at` is authoritatively computed as $T_0 + 15\text{ minutes}$ using the store clock.
  - The call ledger records the call with state `observed` and returns the generated `reminder_id`.

### Scenario: [SCN-002] Idempotency replay with delay_minutes returns identical reminder result
- **User Goal**: Safely retry or replay a reminder creation tool call with a relative delay without creating duplicates or raising conflicts.
- **Given**:
  - A reminder was previously created via call ID `remind-call-1` with arguments `{"action":"create","body":"retry subagent polling","delay_minutes":5}` at store time $T_0$.
  - Time has advanced to $T_0 + 2\text{ minutes}$.
- **When**:
  - The agent replays the exact tool call with call ID `remind-call-1` and identical arguments `{"action":"create","body":"retry subagent polling","delay_minutes":5}`.
- **Then**:
  - The store detects the existing call in `embedded_own_reminder_calls`.
  - The `args_digest` matches identically despite the 2-minute time progression because argument parsing did not synthesize a shifted timestamp.
  - The store returns `replay=true` and the identical original `EmbeddedOwnReminderResult` containing the original `reminder_id`.
  - No new reminder record is inserted, and no conflicting arguments error is produced.

### Scenario: [SCN-003] Agent updates existing reminder using delay_minutes
- **User Goal**: Reschedule an existing scheduled reminder using a relative delay.
- **Given**:
  - Agent owns an active scheduled reminder with ID `embedded-rem-42`.
  - The store clock is at $T_1$.
- **When**:
  - Agent invokes `mecha_reminder` with arguments `{"action":"update","id":"embedded-rem-42","delay_minutes":30,"body":"updated follow-up"}`.
- **Then**:
  - The reminder's `body` is updated to `"updated follow-up"`.
  - The reminder's `next_due_at` is updated to $T_1 + 30\text{ minutes}`.
  - The reminder remains in `scheduled` state with `attempt_count=0`.
  - The tool returns `updated=true` and `replay=false`.

### Scenario: [SCN-004] Reminder creation fails when missing body or timing parameters
- **User Goal**: Receive immediate, clear diagnostic errors when reminder creation arguments lack essential parameters.
- **Given**:
  - An active embedded agent session.
- **When**:
  - Agent invokes `mecha_reminder` with `action="create"` but omits `body` (e.g. `{"action":"create","delay_minutes":10}`).
  - OR agent invokes `mecha_reminder` with `action="create"` and valid `body`, but neither `due_at` nor any delay argument (`delay_minutes`, `delay_seconds`, `delayMinutes`) is specified.
- **Then**:
  - Argument parsing rejects the invocation with an `EmbeddedOwnReminderError`.
  - The error message clearly indicates that creation requires body and a due time/delay.
  - No reminder is persisted and no effect receipt is recorded in the call ledger.

### Scenario: [SCN-005] Delay exceeding 7-day cap fails validation
- **User Goal**: Prevent scheduling reminders unrealistically far into the future beyond system retention policy.
- **Given**:
  - An active embedded agent session.
- **When**:
  - Agent invokes `mecha_reminder` with `action="create"`, `body="long term reminder"`, and `delay_minutes=11520` (8 days, exceeding the 7-day maximum).
- **Then**:
  - The scheduling validation rejects the operation.
  - The returned error states `"due_at must be within the next 7 days"`.
  - No reminder record is created.

---

## 4. Automated Verification Matrix

| Scenario ID | Scenario Description | Test Suite | Test Function / Test Case Name |
| :--- | :--- | :--- | :--- |
| `SCN-001` | Create reminder with `delay_minutes` succeeds with effect intent verification | `router-proxy/internal/store/embedded_own_reminder_test.go` | `TestEmbeddedOwnReminderDelayMinutesCreateWithEffectIntent` |
| `SCN-002` | Idempotency replay with `delay_minutes` returns identical reminder without conflict | `router-proxy/internal/store/embedded_own_reminder_test.go` | `TestEmbeddedOwnReminderDelayMinutesIdempotentReplay` |
| `SCN-003` | Update reminder with `delay_minutes` | `router-proxy/internal/store/embedded_own_reminder_test.go` | `TestEmbeddedOwnReminderDelayMinutesUpdate` |
| `SCN-004` | Missing body or timing parameters fails with descriptive error | `router-proxy/internal/store/embedded_own_reminder_test.go` | `TestEmbeddedOwnReminderDelayValidationMissingFields` |
| `SCN-005` | Delay exceeding 7 days cap fails validation | `router-proxy/internal/store/embedded_own_reminder_test.go` | `TestEmbeddedOwnReminderDelayExceedsCap` |
