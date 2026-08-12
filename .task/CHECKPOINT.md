# Router delivery batching checkpoint

## Current state

- Worktree: `/Users/wolfgang/code/mecha-developer/.worktrees/router-delivery-batching`
- Branch: `codex/router-delivery-batching-push` tracking `origin/codex/router-delivery-batching`
- Current HEAD: `326141d2a8a68d1bd92413dc7c1fe0e53394faaf`
- Merge base with `main`: `4b40ebd4a8e7838cd592b507c7c3c7b1258e225e`
- Ancestry status from `git rev-list --left-right --count HEAD...main`: `1 120`.
  - The branch ancestry is noisy, so final review scope must use the actual working-tree diff, not the whole branch-vs-main history.
- No commit/push/live restart performed for the current uncommitted duplicate-suppression follow-up.

## Intentional local scope

Tracked modified files for the local duplicate-suppression follow-up:

- `.task/PLAN.md`
- `.task/SPEC.md`
- `.task/CHECKPOINT.md`
- `harness/db.js`
- `harness/jsonl_logger.js`
- `harness/router.js`
- `tests/jsonl_logger.test.mjs`
- `tests/mailbox_v2_delivery.test.mjs`

The previously reviewed batching work is already pushed at `326141d2a8a68d1bd92413dc7c1fe0e53394faaf`. No PR is needed for this router work.

## Latest fixes after supervisor gates

- `README.md` now says queue cap rejects sends rather than trying enqueue-time batching, and says delivery-time batching combines deliverable rows that fit both delivery limits: `maxBatchBodyChars` and `maxBodyBytes`.
- Protocol tests directly cover valid and malformed `delivery_items` on delivery, injection-request, ACK, and NACK frames.
- Pi mailbox client now registers `delivery_receipts: "delivery_items_v1"` and constructs a one-item `delivery.items` representation for single-message deliveries.
- Router/repository preserve the core contract for legacy clients: they still receive full delivery-time batches. If a legacy client omits `delivery_items`, the top-level injection/ACK/NACK settles the active batch safely; negotiated clients still require exact `delivery_items` for multi-row batch receipts.
- Legacy reconnect/client-switch regression coverage proves a non-negotiating client can ACK an active multi-item batch after a delivery-item-aware client disconnects.
- Legacy/null active replay coverage proves pre-migration `delivery_batch_id IS NULL` active rows are not merged together during replay.
- Pi durable replay matching requires full delivery item metadata (`messageId`, `messageIncarnationId`, `attemptNumber`, `deliveryExpiresAt`) when delivery items are available.
- Pi durability tests prove a later-item metadata mismatch does not ACK prematurely.
- The max-batched-body test now verifies delivery-time splitting leaves the second row queued when the combined body would exceed `maxBatchBodyChars`.

## Duplicate-suppression follow-up

- `harness/db.js` suppresses exact live duplicates before queue-cap checks. A duplicate must match sender, recipient, subject, body, and sentinel metadata, and the existing row must still be `queued`, `leased`, or `injection_requested`.
- Suppressed enqueue returns `created: false, deduped: true` with the existing message. Changed evidence still enqueues as a new row, and the same content can enqueue again after the earlier row is acknowledged/deleted.
- Conversation insertion now records deduped enqueue attempts so conversation idempotency remains authoritative.
- One-shot and recurring reminder firing accept `deduped: true` and link the reminder to the existing live message.
- `harness/router.js` emits `deduped: true` on `message_enqueued` events when suppression happens.
- `harness/jsonl_logger.js` accepts the new `deduped` boolean on `message_enqueued`, so suppressed enqueue events are not dropped by logging validation.
- Tests added in `tests/mailbox_v2_delivery.test.mjs` cover queued/leased/injection-requested duplicate suppression, changed evidence, sentinel metadata, conversation mapping, recurring and one-shot reminder linking, router events, and post-ACK resend.
- `tests/jsonl_logger.test.mjs` covers logging deduped enqueue events while still rejecting body-bearing fields.

## Saved validation outputs

Duplicate-suppression validation:

- Focused duplicate/logger checks passed:
  - `node --test --test-name-pattern 'sentinel metadata participates|deduped conversation sends|duplicate recurring reminders|router emits deduped|duplicate one-shot reminders' tests/mailbox_v2_delivery.test.mjs`: 5 pass, 0 fail.
  - `node --test tests/jsonl_logger.test.mjs`: 4 pass, 0 fail.
- Broader focused suite output saved at `/tmp/router-duplicate-suppression-focused.txt`.
  - Command: `node --test tests/mailbox_v2_delivery.test.mjs tests/reminder_scheduler.test.mjs tests/shared_conversation_sync.test.mjs tests/android_router_contract.test.mjs tests/protocol.test.mjs tests/pi_extension_standalone.test.mjs tests/router_web_ui.test.mjs tests/jsonl_logger.test.mjs`
  - Result: 127 pass, 0 fail.
- Static check output saved at `/tmp/router-duplicate-suppression-npm-check.txt`.
  - Command: `npm run check`
  - Result: passed.
- Full suite output saved at `/tmp/router-duplicate-suppression-npm-test.txt`.
  - Command: `npm test`
  - Result: 261 pass, 0 fail.
- Focused coverage output saved at `/tmp/router-duplicate-suppression-coverage.txt`.
  - Command: `node --test --experimental-test-coverage tests/mailbox_v2_delivery.test.mjs tests/reminder_scheduler.test.mjs tests/shared_conversation_sync.test.mjs tests/android_router_contract.test.mjs tests/protocol.test.mjs tests/pi_extension_standalone.test.mjs tests/router_web_ui.test.mjs tests/jsonl_logger.test.mjs`
  - Result: 127 pass, 0 fail.
  - Coverage summary from Node:
    - `harness/db.js`: line 84.67%, branch 78.18%, funcs 89.23%
    - `harness/jsonl_logger.js`: line 93.65%, branch 72.97%, funcs 80.00%
    - `harness/router.js`: line 75.62%, branch 68.51%, funcs 76.92%
    - `pi-extensions/mailbox-client.js`: line 76.55%, branch 59.78%, funcs 78.38%
    - `pi-extensions/mecha-messaging.js`: line 82.60%, branch 72.81%, funcs 78.33%
    - all files in that focused run: line 78.44%, branch 70.75%, funcs 72.24%
- Diff whitespace check output saved at `/tmp/router-duplicate-suppression-diff-check.txt`.
  - Command: `git diff --check`
  - Result: passed with no output.

Earlier batching validation:

- Static check output saved at `/tmp/router-delivery-batching-npm-check-final3.txt`.
  - Command: `npm run check`
  - Result: passed.
- Full suite output saved at `/tmp/router-delivery-batching-npm-test-final3.txt`.
  - Command: `npm test`
  - Result: 254 pass, 0 fail.
- Focused coverage output saved at `/tmp/router-delivery-batching-coverage-final3.txt`.
  - Command: `node --test --experimental-test-coverage tests/mailbox_v2_delivery.test.mjs tests/shared_conversation_sync.test.mjs tests/reminder_scheduler.test.mjs tests/android_router_contract.test.mjs tests/pi_extension_standalone.test.mjs tests/protocol.test.mjs tests/router_web_ui.test.mjs`
  - Result: 116 pass, 0 fail.
  - Coverage summary from Node:
    - `harness/db.js`: line 84.50%, branch 76.89%, funcs 89.18%
    - `harness/router.js`: line 75.72%, branch 68.68%, funcs 76.92%
    - `pi-extensions/mailbox-client.js`: line 76.55%, branch 60.64%, funcs 78.38%
    - `pi-extensions/mecha-messaging.js`: line 82.60%, branch 72.81%, funcs 78.33%
    - all files in that focused run: line 77.93%, branch 70.33%, funcs 72.03%
- Diff whitespace check output saved at `/tmp/router-delivery-batching-diff-check-final3.txt`.
  - Command: `git diff --check`
  - Result: passed with no output.

## Prior delegated checks

Duplicate-suppression gates:

- Initial Explore consistency/truthfulness subagents returned empty output and were treated as non-evidence.
- Robot gate `0a0c8876-c31b-4d0` found one logging blocker and several coverage should-address items; fixed by adding logger support and tests.
- Consistency gate `a85cc3b1-0313-4e3` found one should-address item for sentinel/conversation coverage; fixed.
- Truthfulness gate `a7a9e3da-251f-4a2` found one stale checkpoint wording blocker; fixed.
- Final consistency gate `5cd1a391-34a6-438` found Blocking 0, Should Address 0.
- Final truthfulness gate `e58e3911-3af7-4bb` found Blocking 0, Should Address 0.
- Final robot gate `64617b84-0640-409` found Blocking 0, Should Address 0.

Earlier batching gates:

- Initial Explore consistency/truthfulness/robot-review subagents returned empty output and were treated as non-evidence.
- Replacement bounded diff review subagent `4add0abf-7169-497` found no blockers before later supervisor gate failures.
- Fresh robot ledger subagent `3e931042-80aa-4be` found Blocking 1 and Should Address 1; fixed.
- Final robot ledger subagent `666255f3-0851-46b` found Blocking 0 and Should Address 0 before later legacy-reconnect supervisor gate; that later gate is fixed.
- Final2 consistency gate found one blocker in legacy/null active replay; fixed.
- Final2 truthfulness gate found two should-address items in README delivery-limit wording and a stale/under-tested body-limit test; both fixed.
- Final2 robot gate found Blocking 0 and Should Address 0 before the final2 consistency/truthfulness findings; rerun after final3 fixes was completed by final3 gates recorded before the batching push.

## Next required gates

- Supervisor review of the scoped diff and validation.
- Do not commit/push/live restart unless later directed.
