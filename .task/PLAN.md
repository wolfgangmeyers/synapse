# Router pull batching plan

## Requirements source

This plan implements `.task/SPEC.md` approved by the supervisor. Required behavior:

- A recipient pull delivers one batch containing all deliverable non-sentinel messages available for that recipient since the previous pull.
- Timing, sender/source, and conversation-sync classification do not split normal mailbox messages.
- Each item keeps its own message ID, incarnation ID, attempt number, receipt state, retry/dead-letter state, reminder link, and conversation mapping.
- Replaying an unsettled delivery returns the same batch and same item metadata.
- ACK/NACK/timeout applies to every item in the batch.
- Sentinels remain unbatched boundaries.
- Configured body limits may split only at the largest ordered prefix that fits. Queued-byte limits apply at enqueue/admission time, not delivery claim time.
- Unchanged repeated sends while the original row is still live are suppressed; changed evidence and repeats after settlement still deliver.

## Proposed approach

Replace survivor-row coalescing with row-preserving batch leasing.

### Repository behavior

0. Add exact live-duplicate suppression before inserting a new message row. For rows in `queued`, `leased`, or `injection_requested`, match on sender, recipient, subject, body, and sentinel metadata. Return the existing live row as `created: false, deduped: true`; do not suppress changed content or repeats after the prior row is settled. Reminder firing accepts this deduped result and links to the existing live message.
1. Add repository helpers around message rows and delivery attempts. Add durable batch identity if needed to prove replay identity; the expected implementation is a small migration that stores a `delivery_batch_id` on active attempts and a retry group marker on queued messages that timed out from the same batch.
2. Change `claimNextMessage` to build an ordered claim set:
   - Select queued deliverable rows for the recipient ordered by `enqueue_sequence`.
   - Stop before a sentinel. If the oldest row is a sentinel, claim only that row.
   - For normal rows, include every compatible row until `maxBatchBodyChars` or `maxBodyBytes` would be exceeded.
   - Do not filter by conversation-sync state.
   - `maxQueuedBytes` remains an enqueue/admission limit and must not split an already accepted pull.
3. Add a replay-first claim path before selecting new queued rows:
   - Look for active `leased` or `injection_requested` attempts for the recipient, ordered by original `enqueue_sequence`.
   - Identify the whole active delivery by one `delivery_batch_id`, not by whichever rows happen to match a lease query.
   - If those attempts are owned by the recipient's current lease, reconstruct and return them unchanged.
   - If those attempts are owned by an older lease for the same recipient, adopt the whole active delivery to the current lease in the same transaction before returning it. Adoption updates `delivery_attempts.lease_id` and `owner_lease_id` for every active item in that `delivery_batch_id` together. It must not adopt attempts for another recipient, another active agent, or a partial item set.
   - Reconstruct the active delivery from those rows in original `enqueue_sequence` order.
   - Return that same item set and visible batch body without inserting new attempts.
   - If the active delivery is a single sentinel, replay that sentinel alone.
4. Lease every newly selected row in one SQLite transaction:
   - Ensure every row has a valid `message_incarnation_id`.
   - Set every selected row to `leased` with the same `claimed_at` behavior used today.
   - Insert one delivery_attempt row per selected message, each with its own next attempt number, same lease owner, and same delivery deadline.
5. Return a delivery object containing:
   - `message`: the envelope to inject. For single unbatched messages this remains the original envelope. For normal batches this is a synthetic delivery envelope whose visible body is the current mailbox batch text.
   - `items`: ordered item metadata: `message_id`, `message_incarnation_id`, `attempt_number`, and `delivery_expires_at`.
   - `attempt_number`: preserved as the first item's attempt for legacy callers.
6. Keep `selectOldestQueuedMessage` and existing single-row behavior where useful, but remove `coalesceQueuedDeliveryBatch` deletion/remapping from the claim path.

### Batch frame and receipt schema

Extend protocol v1 without removing existing fields:

- Server `message` frame keeps current top-level fields for compatibility: `message.id`, `message_incarnation_id`, `attempt_number`, and `delivery_expires_at` describe the first item.
- Add optional `delivery_items` to server `message` frames. It is a non-empty ordered array. Each item has:
  - `message_id`
  - `message_incarnation_id`
  - `attempt_number`
  - `delivery_expires_at`
- Single-message deliveries may omit `delivery_items`; the client treats the top-level delivery identity as a one-item delivery.
- Client receipts keep current top-level receipt fields for compatibility and may add `delivery_items` with the same item identity shape.
- For batch deliveries, updated clients send `delivery_items` on `injection_requested`, `ack`, and `nack`. The top-level `message_id` and `attempt_number` must match the first item.
- For an active multi-item delivery, a receipt without `delivery_items` is rejected with `delivery_not_active`. The router must not settle only the first item for an old client.
- Router validates that receipt `delivery_items` exactly match the currently active delivery for that lease and batch ID. Mismatches get `delivery_not_active` and do not partially settle.

### Router behavior

1. `offerNextMessage` calls the repository claim function once per pull and sends one `message` frame.
2. `deliveryEnvelope` must not decide batching from the body string alone. Use explicit item metadata to skip a misleading single sender footer for batched deliveries.
3. Receipt handling passes the full item list to repository methods.
4. Correlated ACK confirmation can still confirm the top-level first item, but the internal ACK must settle every item.
5. After ACK/NACK, offer the next batch once, as today.

### Client/extension behavior

1. `createMailboxClient.receive` builds a delivery object with `items` from `frame.delivery_items` when present, otherwise one item from top-level fields.
2. `sendReceipt` includes `delivery_items` whenever `delivery.items` has more than one item.
3. Existing callers that only inspect `messageId` and `attemptNumber` keep working for single deliveries.
4. Update Pi durable delivery state in `pi-extensions/mecha-messaging.js`:
   - `mailboxCustomMessage` stores a primary `message_id` for display compatibility and a `delivery_items` array for batch durability.
   - `mailboxSessionDeliveryState` can verify every item in a batch from the persisted custom message details.
   - Pending checkpoint maps use a stable batch key built from ordered item IDs, not only the first message ID.
   - `onMessageEnd` matches a persisted custom message when every item ID matches, then ACKs with full `delivery_items`.

### ACK/NACK/timeout mapping

- `injection_requested`: every active leased item in the batch becomes `injection_requested`. If any item is inactive or does not match the lease/attempt, no item changes.
- `ack`: every item must be active and injection-requested. Every item becomes acknowledged, linked one-shot reminders complete, linked recurring reminders complete according to existing success behavior, and all item rows are deleted through the existing successful-delivery cleanup path.
- `nack`: every active item is failed with the same reason. Each item independently becomes `queued` or `dead_letter` using its own attempt number and `maxDeliveryAttempts`. Recurring reminders are rearmed for every dead-lettered linked item; one-shot reminders fail for every dead-lettered linked item. A mixed batch can therefore leave some items queued for retry while others are dead-lettered.
- `timeout sweep`: each expired leased/injection-requested item goes through the same per-message timeout/dead-letter transition. Timeout does not flatten attempt counts across the batch. Items that remain retryable return to `queued` with the timed-out batch's retry group marker; items at the attempt limit dead-letter and run linked reminder failure/rearm logic. Retryable rows from the same timed-out batch must be claimed together on their next delivery and must not mix with unrelated queued rows.
- `redelivery/replay after reconnect`: the replay-first claim path returns the same active ordered item set while the attempts are still leased or injection-requested. If reconnect gave the same recipient a new lease, the repository atomically adopts the active batch to that lease before replay so the client's later ACK/NACK is valid. After timeout recovery, retryable queued items from the timed-out batch replay as their own ordered group. Terminal items are no longer unsettled and are not replayed.

### Tests to write first

Add failing tests before implementation in `tests/mailbox_v2_delivery.test.mjs` and protocol/client tests as needed:

1. Ten queued messages to a connected recipient are offered as one frame with ten `delivery_items` and one injected batch body in order.
2. Conversation-synced recipient still gets normal queued mailbox messages in one batch; conversation classification does not disable claim batching.
3. ACK with `delivery_items` deletes/settles every item and completes every linked reminder.
4. NACK with `delivery_items` requeues or dead-letters every item using each item's own attempt count, including a mixed batch where one item retries and another dead-letters.
5. Reconnect before timeout replays the same unsettled batch metadata and adopts it to the new lease. A mixed timeout batch requeues the retryable item with the same retry group, dead-letters the exhausted item, and runs linked reminder failure/rearm effects per item. The retryable timeout group then claims alone and does not mix with unrelated queued rows.
6. Sentinel between normal messages creates a boundary: first pull gets the normal prefix, second pull gets the sentinel alone, third pull gets following normal messages.
7. Configured body limit splits only at the largest ordered prefix that fits; `maxQueuedBytes` does not split accepted queued rows during delivery claim.
8. Protocol validation accepts valid batch frames/receipts and rejects mismatched or malformed `delivery_items`.
9. Client receipt code sends `delivery_items` for batch ACK/NACK and omits it for singles.
10. Pi durable checkpoint tests persist and replay a batch custom message with every item ID, and prove ACK waits until the batch details are durable.
11. Conversation-sync/idempotency tests verify every original conversation row keeps its own ID/incarnation/delivery mapping through batch claim, ACK/NACK, and duplicate enqueue.
12. Duplicate-suppression tests verify unchanged live repeats are suppressed for queued/leased/injection-requested rows, changed evidence still enqueues, repeated reminders link to the existing live message, and a repeat after ACK/deletion can enqueue again.

### Validation

Run, in order:

1. Focused failing tests before implementation.
2. `node --test tests/mailbox_v2_delivery.test.mjs tests/shared_conversation_sync.test.mjs tests/reminder_scheduler.test.mjs tests/android_router_contract.test.mjs tests/pi_extension_standalone.test.mjs tests/protocol.test.mjs`
3. `npm run check`
4. Coverage per test-coverage skill. Node has no existing coverage script, so use Node/V8 coverage or `c8` through `npx` against the focused tests and inspect `harness/db.js`, `harness/router.js`, `pi-extensions/mailbox-client.js`, and protocol/test files.
5. `npm test`
6. `git diff --check`
7. Full robot-review on current diff, one focus ledger covering requirements, protocol/API design, data integrity, concurrency, production risks, error handling, test coverage, compatibility, simplicity/scope, and diff-reading. Validate each finding with pr-validate using a different persona, fix validated findings, then rerun review until signed off.

## Files expected to change

- `harness/db.js`
- `harness/router.js`
- `pi-extensions/mailbox-client.js`
- `pi-extensions/mecha-messaging.js`
- `protocol/v1/schema.json`
- a new migration under `harness/migrations/` if `delivery_batch_id` or retry group metadata needs schema support
- `tests/mailbox_v2_delivery.test.mjs`
- Likely focused protocol/client/conversation tests if existing coverage needs updates.

## Non-goals

- Do not restart router/supervisor or install live changes.
- Do not open PRs, force push, merge, or mutate supervisor state.
- Do not commit or push in this lane without separate authorization.
- Do not redesign non-mailbox protocols or change sentinel delivery semantics.
