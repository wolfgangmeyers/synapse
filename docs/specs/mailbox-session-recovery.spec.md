# Spec: Mailbox Progress Across Session Replacement

- **ID**: `SPEC-MAILBOX-001`
- **Status**: `Draft`
- **Domain**: `Mailbox`
- **Target E2E Suite**: `tests/e2e/mailbox-session-recovery.spec.ts`

## 1. Intent & User Outcome

Messages addressed to a managed agent remain actionable after context reset, worker restart, or router restart. An obsolete session's reservation cannot block the current inbox. Recovery preserves live ownership and does not repeat committed mail effects.

This backfills session-replacement delivery, not all mailbox transports or rendering. Sources of intent: the user-authorized recovery outcome, [Claude workers §§3.4–3.9](claude-code-workers-spec.md), and [conversation continuity](conversation-timeline.md). The reported obsolete-reservation mechanism is user evidence, not a verified implementation finding.

## 2. Core Invariants

1. Normal context reset retires/deletes only safely unconsumed reservations; subsequent dispatch may create a new attempt and append key. The accepted message identity, sender, body, target and once-only consumption are preserved, not the normal-reset attempt ID/key. For an already-retired reservation persisted before upgrade, narrow reconciliation retains its original attempt ID and append key. Once replacement ownership is established and the current worker is ready, only safely unstarted reservations become eligible for reconciliation on the next normal inbox dispatch opportunity: no ACK, append-key or reply evidence; the reservation matches the exact retired session tuple for the same agent and generation, with old epoch strictly less than the current epoch; and the accepted envelope/target is unchanged. No obsolete-owner cooperation or manual queue repair is required. Missing, mismatched, cross-agent, cross-generation, equal/future-epoch or non-retired identity is not takeover authority.
2. A slow worker or disconnected observer alone does not prove obsolescence. Recovery must not steal a valid live reservation, including another agent's reservation.
3. After ownership replacement, an obsolete owner cannot acknowledge, release, consume, or execute mail effects against successor-owned delivery. Stale actions fail without modifying current delivery state.
4. Started or uncertain-outcome reservations are not relocated by this repair, including ACKed-but-unappended mail or mail with append/reply evidence. Accepted unconsumed mail remains pending across recovery; consumed mail is not newly consumed again. Recovery does not replay started/uncertain outcomes; identical effect replay does not duplicate committed mail effects. This is not a promise of exactly-once arbitrary external commands whose results were never committed.
5. Repeated recovery is idempotent and preserves existing mailbox ordering and eligibility rules. No new growing history, archive, or retry backlog is required; existing mail retention is unchanged.

## 3. User Scenarios (Behavioral Contract)

Every case has **Mode: real-capable**. Local deterministic model fixtures replace external LLM services, not router ownership or mailbox functions.

### Scenario: [SCN-MSR-001] Ordinary live-owner delivery
- **User Goal**: Deliver mail without disrupting a healthy worker.
- **Given**: A ready current worker has pending mail and valid ownership.
- **When**: Normal inbox dispatch occurs and the worker consumes the mail.
- **Then**: Delivery records consumption once and the browser receives one resulting reply without reload.

Recovery fixtures in SCN-MSR-002–004 use only the safely unstarted, exact retired same-agent/same-generation lower-epoch reservations defined above. They do not promise automatic replay of started or uncertain outcomes.

### Scenario: [SCN-MSR-002] Context reset releases obsolete blockage
- **User Goal**: Continue receiving mail after resetting context.
- **Given**: Accepted unconsumed mail is reserved to the pre-reset session.
- **When**: Context reset completes and a replacement session becomes ready.
- **Then**: On the next normal dispatch opportunity, the replacement worker receives the same accepted message (identity, sender and body) and consumes it once; later eligible mail also progresses without manual repair or obsolete-session cooperation. Normal reset may retire the old reservation and create a new attempt/key; stale old-owner operations have no effect. A legacy already-retired unstarted reservation persisted before upgrade instead reconciles narrowly, retaining its original attempt ID/key under the same safety fences.

### Scenario: [SCN-MSR-003] Worker restart restores progress
- **User Goal**: Resume a worker without stranding its inbox.
- **Given**: Pending mail is reserved to a worker session that restart replaces.
- **When**: The user restarts the worker and valid current ownership is established.
- **Then**: On the next normal dispatch opportunity, pending mail and later eligible mail progress through the current worker with one consumption per message.

### Scenario: [SCN-MSR-004] Router restart recovers durable mail
- **User Goal**: Preserve inbox progress after service interruption.
- **Given**: Unconsumed reserved mail is durable before router shutdown.
- **When**: The router restarts on the same SQLite store and establishes a ready current worker owner.
- **Then**: Pending and later eligible mail progress without queue repair; consumed mail stays consumed and committed mail effects stay single.

### Scenario: [SCN-MSR-005] Live owners remain protected
- **User Goal**: Avoid competing consumers during a slow live turn.
- **Given**: A valid current owner holds a reservation while deliberately slow, and another agent holds its own valid reservation. Separate fixtures contain cross-generation, non-retired, equal/future-epoch or mismatched tuples, ACKed-but-unappended mail, and append-key or reply evidence.
- **When**: Recovery checks repeat and an observer disconnects and reconnects without replacing worker ownership.
- **Then**: No competing worker receives either reservation; each valid original owner can complete its own consumption once. None of the ineligible fixtures is relocated, including the ACKed-but-unappended uncertain outcome.

### Scenario: [SCN-MSR-006] Late stale owner cannot mutate successor delivery
- **User Goal**: Recover safely when an obsolete process returns late.
- **Given**: Replacement ownership is established and the successor has reserved previously pending mail.
- **When**: The obsolete owner attempts acknowledgement, release, consumption and mail effects, interleaved with successor consumption and repeated recovery.
- **Then**: Stale operations fail without changing current delivery; the successor completes once without loss or duplicate effects.

### Scenario: [SCN-MSR-007] Recovery and replay preserve completed effects
- **User Goal**: Avoid repeating completed coordination.
- **Given**: One message's consumption and mail effect are committed and a second message is pending before session replacement.
- **When**: Recovery and the first message's identical effect replay repeat across reset or restart.
- **Then**: The first message is not consumed anew and its committed effect remains single; the second message progresses once under current ownership.

## 4. Automated E2E Verification Matrix

Actual executable: `tests/e2e/mailbox-session-recovery.spec.ts` contains one combined TC, `SCN-MSR-001/002: context reset unblocks retired-epoch unstarted mail`. It uses canonical store Given for safely unstarted accepted reservation and actual context reset, not REST notification steering; real browser/WS/REST/proxy/store with deterministic local model fixture (**Mode: real-capable**). It provides partial ordinary/reset progress proof only. Supporting store guards `TestMailboxSessionRecoveryUnstartedReservation` and `TestMailboxSessionRecoveryFailsClosed` cover normal retirement/new-attempt progress and legacy retained-ID/key reconciliation, plus narrow transactional safety invariants, not full browser acceptance. The seven titles below remain planned; restart/live-owner/stale-owner/committed-replay browser acceptance is not run. No full SCN-MSR-001–007 completion is claimed.

Execution evidence and proof gaps: [delivery report](reports/report-20261105-1650-main.md). Planned acceptance remains unchanged. All dependencies must be deterministic/local in automated tests.

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `SCN-MSR-001` | Ordinary delivery | `tests/e2e/mailbox-session-recovery.spec.ts` | `live owner consumes mail once` |
| `SCN-MSR-002` | Reset recovery | `tests/e2e/mailbox-session-recovery.spec.ts` | `context reset unblocks obsolete reserved mail` |
| `SCN-MSR-003` | Worker restart | `tests/e2e/mailbox-session-recovery.spec.ts` | `worker restart restores inbox progress` |
| `SCN-MSR-004` | Router restart | `tests/e2e/mailbox-session-recovery.spec.ts` | `router restart recovers durable reserved mail` |
| `SCN-MSR-005` | Live owner protection | `tests/e2e/mailbox-session-recovery.spec.ts` | `recovery preserves slow live owners and other agents` |
| `SCN-MSR-006` | Stale owner race | `tests/e2e/mailbox-session-recovery.spec.ts` | `stale owner cannot mutate successor delivery` |
| `SCN-MSR-007` | Replay safety | `tests/e2e/mailbox-session-recovery.spec.ts` | `recovery does not repeat committed mail effects` |
