# Spec: Conversation Send Receipt Retention

- **ID**: `SPEC-MAILBOX-RECEIPTS-001`
- **Status**: `Active`
- **Domain**: `Mailbox`
- **Target E2E Suite**: `tests/e2e/conversation-send-receipt-retention.spec.ts`

## 1. Intent & User Outcome

A retried conversation send is safe to repeat for 30 minutes without allowing the receipt ledger to grow indefinitely or stop unrelated sends permanently. The receipt is a record of an accepted send, not the delivery itself. After the protection expires, a repeat can create another send; Wolfgang has accepted that tradeoff.

## 2. Core Invariants

1. Until its deadline, an accepted ID replays its original result for an identical request and rejects a different request. This works across acknowledgement, history pruning, restart, and full receipt capacity. Deduplicated aliases have their own receipts and preserve the original delivery result.
2. A receipt expires at the earlier of its existing deadline and 30 minutes after acceptance. At the deadline it no longer provides replay protection, even if cleanup has not yet run. Fresh receipts have a 30-minute deadline. Cleanup removes expired receipts for all delivery states, including queued and dead-letter, without modifying deliveries, messages, or conversation history. Existing delivery-ID uniqueness and normal live-message deduplication still apply; an expired alias may be accepted anew, and an expired ID whose original delivery no longer exists can produce a new send. Repeats can therefore duplicate a real-world effect.
3. The ledger is limited to 100,000 receipts and 200 MiB of serialized UTF-8 receipt content by default. Upgrades change only the old 64 MiB default to 200 MiB; explicitly different existing byte and count limits remain unchanged. Capacity never evicts an unexpired receipt. Exhaustion returns a specific capacity error and rolls back a new send completely. Expired receipts are reclaimed before admission; rejected sends cannot strand cleanup.
4. An independent periodic cleanup, separate from general lifecycle maintenance, promptly drains every due batch without requiring sends or successful unrelated maintenance. Startup drains overdue rows before serving traffic. Expired rows are semantically invalid at the deadline; physical removal follows promptly while the router and SQLite are available. A stopped process, database failure, lock contention, or scheduling delay can delay physical deletion; failures are visible and retried. Pruning releases the exact persisted receipt byte and count charge in the same transaction as deletion.
5. Upgrade shortens old deadlines without extending any already-shorter deadline. Where an old receipt predates deadline tracking, use its saved acceptance/recording time when available, not a fresh 30-day or 30-minute allowance. Overdue receipts are removed on startup; reopening does not extend deadlines. Upgrade, capacity changes, and deletion must preserve messages and deliveries and must not break older unrelated history.

## 3. User Scenarios (Behavioral Contract)

### Scenario: [RCP-001] Retry, conflict, and late reuse
- **User Goal**: Retry safely without keeping replay protection forever.
- **Given**: A conversation send has been accepted and acknowledged.
- **When**: The user repeats the request, changes it inside 30 minutes, and repeats it after the deadline.
- **Then**: The identical in-window request returns the original result, the changed request conflicts, and the late request can create a new delivery and history entry without removing the original history. Browser requests and WebSocket delivery traverse the router proxy.

### Scenario: [RCP-002] Unresolved, dead-letter, and alias receipts expire
- **User Goal**: Reclaim receipts without losing outstanding work.
- **Given**: Queued and dead-letter deliveries and a deduplicated alias have receipts.
- **When**: Their 30-minute deadlines pass without any new sends.
- **Then**: Independent cleanup removes all expired receipts, including the alias, and updates accounting accurately. Both actual deliveries and history remain intact. A live original ID retains normal message-ID conflict/replay behavior, while a late alias can be newly accepted under normal deduplication rules.

### Scenario: [RCP-003] Capacity and rollback
- **User Goal**: Understand and recover from a full receipt ledger without losing a send.
- **Given**: The receipt count or byte cap is full of unexpired receipts.
- **When**: A new send is attempted, an existing ID is retried, and then the receipts expire.
- **Then**: The new send receives the specific capacity error with no message or history written, an in-window retry still succeeds, cleanup releases the exact byte/count charges, and a subsequent new send succeeds.

### Scenario: [RCP-004] Upgrade old installations
- **User Goal**: Upgrade without lingering 30-day receipts or losing pending deliveries.
- **Given**: An older database has old-default or custom receipt caps, old, recent, and already-shorter receipt deadlines, and outstanding deliveries.
- **When**: The upgraded router starts and restarts.
- **Then**: The old default becomes 200 MiB but custom limits survive, overdue rows are pruned immediately, all other deadlines are no later than 30 minutes after the saved acceptance/recording time, shorter deadlines stay shorter, and deliveries and history remain. Reopening does not extend deadlines or reapply the migration.

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| RCP-001 | Retry, conflict, late reuse | `tests/e2e/conversation-send-receipt-retention.spec.ts` | `replays through the proxy and permits reuse after expiry` |
| RCP-002 | Unresolved, dead-letter, aliases, independent cleanup | `tests/e2e/conversation-send-receipt-retention.spec.ts` | `cleans unresolved and alias receipts without deleting deliveries` |
| RCP-003 | Count/byte capacity, rollback, recovery | `tests/e2e/conversation-send-receipt-retention.spec.ts` | `recovers count and byte capacity without partial sends` |
| RCP-004 | Default/custom upgrade and restart | `tests/e2e/conversation-send-receipt-retention.spec.ts` | `upgrades old deadlines and defaults without losing pending work` |

Supporting Go tests cover exact boundary, concurrent admission, multi-batch cleanup, accounting, failed transactions, migration rollback and historical schema upgrades.
