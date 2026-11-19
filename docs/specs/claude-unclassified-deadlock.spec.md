# Spec: Claude Mail Unclassified Deadlock and Diagnostic Error Visibility

- **ID**: `SPEC-CLAUDE-UNCLASSIFIED-DEADLOCK-001`
- **Status**: Draft
- **Domain**: AgentManagement
- **Target E2E Suite**: `tests/e2e/claude-unclassified-deadlock.spec.ts`

---

## 1. Intent & User Outcome

When a Claude worker encounters an error during a mail turn that cannot be classified as a definite start failure or uncertain hold, the message must not permanently stall the worker's processing queue. Subsequent queued messages (e.g. from the Web UI or other agents) must continue processing rather than waiting indefinitely behind the blocked message until TTL expiration or router restart. Furthermore, whenever a message is blocked or dead-lettered, an unambiguous diagnostic notification or error history entry must always be visibly surfaced to the user in the UI conversation history, even under concurrent fence transitions or stale lease conditions. In addition, internal database queries executed during system-generated mail or reminder turns (such as context-usage threshold warnings) must handle missing rows safely without surfacing unhandled `sql: no rows in result set` errors that abort worker turns.

---

## 2. Core Invariants

1. A blocked or poison-pill message MUST NOT indefinitely prevent subsequent queued messages addressed to the same worker from being processed.
2. If an unclassified turn failure occurs, the failed message MUST either be dead-lettered/dismissed after bounded attempts or bypassed/skipped by the queue consumer so subsequent messages proceed.
3. Every blocked message event MUST reliably record a visible error/diagnostic in `managed_complete_history` and project to the UI timeline, with guaranteed fallback even if the initial recording encounters an `ErrStaleEmbeddedFence`.
4. System-generated reminder turns and mail processing turns MUST NOT fail with `sql: no rows in result set` due to queries lacking fallback or checking invalid pre-conditions.

---

## 3. User Scenarios (Behavioral Contract)

### Scenario: [SCN-001] Subsequent queued messages process when an earlier message fails with unclassified error
- **User Goal**: Send new messages to a Claude worker and have them processed even if a prior message encountered an unclassified failure.
- **Given**:
  - A managed Claude worker is active and idle.
  - A message `msg-1` in the queue fails with an unclassified error during execution.
  - Later messages `msg-2` and `msg-3` are enqueued behind `msg-1`.
- **When**:
  - The router handles the failure of `msg-1`.
- **Then**:
  - `msg-1` is marked blocked or dead-lettered after exhausting retries without holding the head of the queue.
  - The worker picks up and processes `msg-2` and `msg-3` successfully.
  - The worker does not remain idle with `msg-2` and `msg-3` indefinitely blocked.

### Scenario: [SCN-002] Error history entry is surfaced even on stale fence condition
- **User Goal**: See a visible error indicator in the chat timeline explaining that a message failed/blocked, rather than the worker silently ignoring future inputs.
- **Given**:
  - A Claude worker encounters an unclassified failure on message `msg-1`.
  - The current embedded session fence is stale or in transition.
- **When**:
  - `blockClaudeUnclassified` attempts to record the failure in history.
- **Then**:
  - Even if the fenced recording returns `ErrStaleEmbeddedFence`, the system schedules a retry or records an unfenced history event for the agent.
  - A "Claude mail blocked" error note is visibly published to `managed_complete_history` and rendered in the conversation timeline.

### Scenario: [SCN-003] Context-usage warning reminder turns complete without SQL errors
- **User Goal**: Context usage warning reminders are delivered and processed cleanly by Claude workers without failing with `sql: no rows in result set`.
- **Given**:
  - A Claude worker approaches context window threshold and receives a `Context usage threshold warning` message.
- **When**:
  - The worker processes the warning turn.
- **Then**:
  - All database queries for message metadata, attempts, target snapshots, and policies resolve cleanly without `sql.ErrNoRows`.
  - The worker responds or updates scratchpad, and normal operations continue.

---

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E / Functional Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `SCN-001` | Subsequent messages process when previous message blocked | `tests/e2e/claude-unclassified-deadlock.spec.ts` | `subsequent queued messages continue processing after unclassified failure` |
| `SCN-002` | Reliable error reporting on stale fence | `router-proxy/internal/managed` (Go unit) & `tests/e2e` | `TestClaudeBlockUnclassifiedSurfacesHistoryOnStaleFence` |
| `SCN-003` | Context usage warning turn handles missing rows cleanly | `router-proxy/internal/managed` (Go unit) & `tests/e2e` | `TestClaudeContextWarningTurnNoRowsSafety` |
