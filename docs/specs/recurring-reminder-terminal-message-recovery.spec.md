# Spec: Recurring Reminder Terminal Message Recovery

- **ID**: `SPEC-MAILBOX-REMINDER-002`
- **Status**: `Active`
- **Domain**: `Mailbox`
- **Target E2E Suite**: `router-proxy/internal/store/reminder_transition_test.go`

---

## 1. Intent & User Outcome

Recurring reminders ensure persistent agent workflows and periodic checks (such as status pings, deadline triggers, and automated sweeps) occur on a predictable schedule. Each recurrence cycle dispatches a linked mailbox message (`reminder-<id>-<occurrence>`) to the target agent and binds it via `linked_message_id`.

If a linked message fails terminal delivery (e.g. Claude Code worker failure settlement, delivery NACK, message expiry, or manual dismissal), or if an unexpected state leaves `linked_message_id` referencing a dead-lettered or missing message, the recurring reminder must not stall or skip cycles indefinitely.

This specification guarantees that:
1. Any transition of a linked reminder message into a terminal state (`dead_letter`, failed settlement, expiry, or manual dismissal) immediately re-arms the recurring reminder by clearing `linked_message_id = NULL` and advancing `next_due_at` to the next cycle.
2. During scheduled reminder evaluation (`fireRecurring` and `fireManagedRecurring`), if the store encounters a scheduled recurring reminder whose `linked_message_id` points to a message in `dead_letter` state or missing from the store, it self-heals by clearing the stale link and immediately firing a fresh reminder message rather than postponing indefinitely.
3. In-flight messages (`queued`, `leased`, `injection_requested`) retain their linkage and prevent duplicate message generation while advancing `next_due_at`.

---

## 2. Core Invariants

1. **Terminal Transition Re-arming**: Every code path that transitions a message to a terminal failure state (`dead_letter`, failed settlement, expiry, or manual dismissal) MUST re-arm any recurring reminder linked to that message ID by setting `linked_message_id = NULL`, incrementing `attempt_count`, and ensuring `next_due_at` reflects the next recurrence interval.
2. **Evaluator Self-Healing on Stale/Dead Link**: When `fireRecurring` or `fireManagedRecurring` evaluates a due recurring reminder, it MUST inspect the state of `linked_message_id`. If the linked message is in `dead_letter` state or does not exist in the messages table, the evaluator MUST clear the stale link and proceed to enqueue and dispatch a fresh reminder message for the current tick.
3. **In-Flight Deduplication**: When `linked_message_id` points to an active, in-flight message (in `queued`, `leased`, or `injection_requested` state), the evaluator MUST NOT dispatch a duplicate message. It advances `next_due_at` to the next slot without disturbing the active delivery attempt.
4. **Target Liveness Authority**: Liveness and readiness gating (such as Claude Code worker readiness or active agent lease) remain authoritative: self-healing unlinks dead-lettered messages, but message dispatch respects agent availability rules.

---

## 3. User Scenarios (Behavioral Contract)

### Scenario: [SCN-001] Claude Code worker failure settlement dead-letters reminder message and re-arms recurring reminder
- **User Goal**: Ensure recurring reminders remain active and schedule future occurrences even when a Claude Code worker process crashes or terminates abnormally.
- **Given**:
  - A managed Claude Code worker agent is running with an active recurring reminder (interval: 60,000ms).
  - The reminder has fired occurrence 1, generating message `reminder-rec-1` which is currently in `queued` state and linked via `linked_message_id = 'reminder-rec-1'`.
- **When**:
  - The Claude Code worker process crashes or fails settlement.
  - Failure settlement runs, transitioning unconsumed queued messages for the worker to `dead_letter`.
- **Then**:
  - Message `reminder-rec-1` transitions to `dead_letter` state.
  - The store re-arms the recurring reminder: `linked_message_id` is reset to `NULL`.
  - The reminder remains in `scheduled` state with `next_due_at` set to the next valid cycle.
  - Subsequent reminder scheduler ticks can fire the next occurrence without manual intervention.

### Scenario: [SCN-002] Recurring reminder tick encounters dead-lettered linked message and self-heals
- **User Goal**: Recover immediately when a recurring reminder tick runs while its linked message was dead-lettered without prior unlinking.
- **Given**:
  - A recurring reminder is due at time $T_{now}$ with interval 60,000ms.
  - Its `linked_message_id` points to message `reminder-rec-prev`.
  - Message `reminder-rec-prev` is in `dead_letter` state (or was purged from the messages table).
  - The target agent is live, connected, and accepting prompts.
- **When**:
  - `FireDueReminders` runs at time $T_{now}$ and executes `fireRecurring` (or `fireManagedRecurring`).
- **Then**:
  - The evaluator detects that `linked_message_id` is non-NULL but references a terminal `dead_letter` (or missing) message.
  - The store self-heals by clearing `linked_message_id = NULL`.
  - A fresh reminder message `reminder-rec-new` is enqueued and dispatched immediately for this tick.
  - `occurrence_number` increments and `linked_message_id` is updated to `reminder-rec-new`.
  - `next_due_at` is advanced to $T_{now} + 60,000\text{ms}$.

### Scenario: [SCN-003] Recurring reminder tick encounters in-flight linked message
- **User Goal**: Avoid duplicate message spam while an agent is still actively processing or awaiting delivery of the previous reminder message.
- **Given**:
  - A recurring reminder is due at time $T_{now}$ with interval 60,000ms.
  - Its `linked_message_id` points to message `reminder-rec-flight`.
  - Message `reminder-rec-flight` is in an active delivery state (`queued`, `leased`, or `injection_requested`).
  - Target agent is connected.
- **When**:
  - `FireDueReminders` runs at time $T_{now}$ and evaluates the reminder.
- **Then**:
  - The evaluator detects that the linked message is currently in-flight.
  - No new duplicate reminder message is enqueued or dispatched.
  - The reminder's `next_due_at` is advanced to the next cycle ($T_{now} + 60,000\text{ms}$).
  - `linked_message_id` remains set to `reminder-rec-flight`.

### Scenario: [SCN-004] Manual message dismissal dead-letters message and re-arms recurring reminder
- **User Goal**: Allow Wolfgang or an operator to dismiss a stuck or unwanted reminder message from the mailbox without permanently breaking the recurring reminder.
- **Given**:
  - A recurring reminder has fired, and its linked message `reminder-rec-stuck` is in the agent's mailbox in `queued` or `leased` state.
  - The reminder has `linked_message_id = 'reminder-rec-stuck'`.
- **When**:
  - The user or client invokes `DismissMailboxMessage` for `reminder-rec-stuck`.
- **Then**:
  - Message `reminder-rec-stuck` transitions to `dead_letter`.
  - The recurring reminder linked to `reminder-rec-stuck` is re-armed: `linked_message_id` is set to `NULL`.
  - The reminder remains in `scheduled` state and will fire on its next scheduled `next_due_at`.

---

## 4. Automated Verification Matrix

| Scenario ID | Scenario Description | Test Suite | Test Function / Test Case Name |
| :--- | :--- | :--- | :--- |
| `SCN-001` | Claude Code worker failure settlement dead-letters message and re-arms recurring reminder | `router-proxy/internal/store/reminder_transition_test.go` | `TestRecurringDeadLetterRearmsOnClaudeFailureSettlement` |
| `SCN-002` | Recurring tick self-heals dead-lettered linked message by unlinking and firing fresh message | `router-proxy/internal/store/reminder_transition_test.go` | `TestRecurringFireSelfHealsDeadLetterLinkedMessage` |
| `SCN-003` | Recurring tick with in-flight message advances next_due_at without duplicate dispatch | `router-proxy/internal/store/reminder_transition_test.go` | `TestRecurringFireInFlightMessageAdvancesWithoutDuplicate` |
| `SCN-004` | Manual message dismissal dead-letters message and re-arms recurring reminder | `router-proxy/internal/store/reminder_transition_test.go` | `TestRecurringDismissalRearmsLinkedReminder` |
