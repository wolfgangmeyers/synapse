# Router delivery batching main-integration checkpoint

## Current state

- Worktree: `/Users/wolfgang/code/mecha-developer/.worktrees/router-batching-main-integration`
- Branch: `codex/router-batching-main-integration` tracking `origin/main`
- Integration base: `origin/main` at `d85109dedb68403cced37a63d68807902fbdcc0c`
- Batching source: `origin/codex/router-delivery-batching` at `326141d2a8a68d1bd92413dc7c1fe0e53394faaf`
- The later duplicate-suppression branch commit is intentionally excluded because duplicate suppression is already on main.
- No PR, push, or runtime change performed.

## Integration scope

Delivery-time batching source-of-truth files from `326141d2`, resolved onto current main:

- `.task/PLAN.md`
- `.task/SPEC.md`
- `.task/CHECKPOINT.md`
- `README.md`
- `harness/db.js`
- `harness/migrations/016_delivery_batches.sql`
- `harness/router.js`
- `pi-extensions/mailbox-client.js`
- `pi-extensions/mecha-messaging.js`
- `protocol/v1/schema.json`
- `tests/android_router_contract.test.mjs`
- `tests/mailbox_v2_delivery.test.mjs`
- `tests/protocol.test.mjs`
- `tests/shared_conversation_sync.test.mjs`

Conflict-resolution notes:

- Renumbered the delivery-batches migration to `016_delivery_batches` because main already has `015_conversation_source`.
- Preserved main's duplicate-suppression behavior, source metadata, message-arrival behavior, compaction activity behavior, and JSONL logging changes.
- Replaced enqueue-time survivor-row coalescing with row-preserving delivery-time batching from `326141d2`.
- Kept existing main tests for duplicate suppression and newer Pi/session behavior while restoring the delivery-items batching tests from the source branch.

## Validation status

- `npm run check` passed. Output saved at `/tmp/router-batching-main-npm-check.txt`.
- Focused batching suite passed. Output saved at `/tmp/router-batching-main-batching-focused.txt`.
  - Command: `node --test --test-name-pattern 'batched Pi delivery|delivery item|delivery_items|batch|batching|legacy reconnect|legacy null-batch|reconnect replay|timed-out batch|retry group|merged prior attempts|recurring reminder linked|queued byte|sentinel|reminder links|rapid posts|synced sender preserves delivery batching' tests/mailbox_v2_delivery.test.mjs tests/shared_conversation_sync.test.mjs tests/android_router_contract.test.mjs tests/protocol.test.mjs`
  - Result: 28 pass, 0 fail.
- Broader affected suite output saved at `/tmp/router-batching-main-focused.txt`.
  - Command: `node --test tests/mailbox_v2_delivery.test.mjs tests/shared_conversation_sync.test.mjs tests/reminder_scheduler.test.mjs tests/android_router_contract.test.mjs tests/pi_extension_standalone.test.mjs tests/protocol.test.mjs tests/jsonl_logger.test.mjs`
  - Result: 163 pass, 1 fail.
  - The failing test is `compaction pauses activity without dropping mailbox resume`; this is the unrelated existing compaction timing assertion previously observed on main-integration checks, not a batching failure.
- `git diff --check` passed. Output saved at `/tmp/router-batching-main-diff-check.txt`.

## Next steps

1. Supervisor review of the scoped integration diff and validation evidence.
2. Do not commit, push, open a PR, or change runtime until authorized.
