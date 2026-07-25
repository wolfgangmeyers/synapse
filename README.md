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
