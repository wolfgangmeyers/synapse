# mecha-developer

Personal dev-tooling skills for AI coding agents — supervisor, worker, watchdog, mailbox, reminders, install, decompose, kb, voice, augment-persona, generate-persona, worktree, workflows, test-coverage.

Companion repos:
- `wolfgangmeyers/mecha-review` — self-contained PR review and review-adjacent skills (review-pr, robot-review, pr-summary, pr-validate, pr-context, explain)

## Structure

Skills live under `.claude/skills/<name>/SKILL.md` with optional `tools/` and supporting files. Each skill is a self-contained instruction set any agent (Claude Code, Codex, Cursor) can invoke by reading the file.

## Status

This repo is the home for non-review developer workflow skills.

## Mailbox V2 batching and backpressure

Mailbox V2 keeps each recipient's active mailbox bounded. By default, at most 15 live messages (`queued`, `leased`, or `injection_requested`) are allowed per recipient; when the cap is reached and a new send cannot be batched, the router rejects the send with `queue_full`, and `mailbox_send.py` exits non-zero with instructions to back off and wait for the mailbox to clear.

Small non-sentinel messages to the same recipient are batched while they are still queued, even when senders or subjects differ. The combined body includes per-message sender, subject, and reply hints so the recipient can respond to the right agent. The combined body is capped by `maxBatchBodyChars` (default `4096` characters), configurable in `openMessageRepository({ maxBatchBodyChars })` or with `tools/run_router.mjs --max-batch-chars <chars>` / `MECHA_MAILBOX_MAX_BATCH_CHARS`. Set the value to `0` to disable batching.

## Pi context thresholds and self-compaction

The Mailbox V2 Pi extension monitors `ctx.getContextUsage()` and sends direct in-session notifications when percentage thresholds are crossed. The default policy uses a sensible 40/50/60/70/80/90 ladder for ordinary 272k-token workers, keeps stricter internal defaults for very large context windows, and has tighter built-in defaults for Sol models.

Agents can inspect or change the policy with the `mecha_context` tool (`status`, `set_policy`) and can request self-compaction with `mecha_context` action `compact`. Guidance intentionally says to compact only after a threshold notification or direct user instruction. `postCompactionNotification` is optional: include it while you are in the middle of an active task, review, or decision loop; omit it only when clearly idle/done and no details are needed. A default handoff note can be configured in the compaction policy, and a per-compact call can override it.
