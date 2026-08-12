# Router pull batching spec

## Behavior

When the router has messages ready for one recipient and that recipient's delivery path asks for work, the router delivers one batch for that pull. The batch contains every deliverable non-sentinel message that became available after the previous pull and before the current pull is claimed, in enqueue order. If ten normal messages are available, the recipient receives one ten-item batch.

Batching is independent of whether messages came from workers, reminders, HTTP sends, or conversation-synced clients. Conversation classification must not force separate deliveries.

Unchanged repeated sends to a busy recipient should not create duplicate live rows. While an identical message is still queued, leased, or awaiting injection acknowledgement, a repeat with the same sender, recipient, subject, body, and sentinel metadata is suppressed and points at the existing live row. Changed evidence, blockers, state transitions, or repeats after the earlier row is settled still deliver as new messages.

Each item in the batch keeps its own authoritative message ID and incarnation ID. Receipts must settle the item IDs, not only the displayed batch body. ACK settles every item in the delivered batch. NACK/retry/dead-letter handling must preserve the prior attempt count and reminder/conversation links for every item. Reconnect replay must return the same unsettled batch, with the same item IDs, until the batch is acknowledged, nacked, or timed out. Timeout settles the expired attempt for each item; retryable timed-out items from that batch stay grouped for their next delivery and do not mix with unrelated queued rows.

Sentinel messages remain outside normal mailbox batching. A queued sentinel is delivered by itself and acts as a boundary for adjacent normal messages.

Existing admission limits still apply when messages enter the queue. Delivery-time batching may split an available set only when the combined visible body would exceed the configured batch body or message body size limits. `maxQueuedBytes` is not a delivery split condition because claiming a row-preserving batch does not add queued bytes.

## High-level design

The repository should keep batched messages as separate rows. Claiming a batch leases each item in one transaction and records an attempt for each item. The router should send a batch delivery frame that carries the visible combined body plus item-level delivery metadata. Receipt handling should resolve every item in the batch consistently so reminder completion, recurring rearm, conversation sync idempotency, retries, timeouts, and dead-letter transitions run per original message row.

The client/extension receipt path should treat a batch as one injected envelope for the model, but retain the batch's item delivery metadata for ACK/NACK.

## Alternatives considered

1. Keep the current survivor-row coalescing and add more remapping. This cannot satisfy per-message IDs and per-message receipts because merged rows are deleted before delivery.
2. Delay every connected delivery with a short timer so more messages can arrive before the first offer. This helps burst timing, but it still does not preserve item-level settlement unless batch state is modeled explicitly.
3. Lease a batch of existing rows as one logical delivery while keeping item rows. This best matches the requirement: one pull gives one injected batch, while each original message keeps its own ID, attempts, reminder link, and conversation mapping.

Chosen approach: option 3.
