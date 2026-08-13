# mecha-developer

Personal dev-tooling skills for AI coding agents — supervisor, worker, watchdog, mailbox, reminders, install, decompose, kb, voice, augment-persona, generate-persona, worktree, workflows, test-coverage.

Companion repos:
- `wolfgangmeyers/mecha-review` — self-contained PR review and review-adjacent skills (review-pr, robot-review, pr-summary, pr-validate, pr-context, explain)

## Structure

Skills live under `.claude/skills/<name>/SKILL.md` with optional `tools/` and supporting files. Each skill is a self-contained instruction set any agent (Claude Code, Codex, Cursor) can invoke by reading the file.

## Status

This repo is the home for non-review developer workflow skills.

## Managed worker runtime

`worker_launch.py` uses a machine-local default runtime from `~/.mecha-developer/worker-runtime.json` (or `MECHA_WORKER_RUNTIME_CONFIG`) with this shape:

```json
{"default_agent":"pi"}
```

Allowed values are `pi`, `claude`, `codex`, and `cursor`; missing config falls back to `pi`, and explicit `--agent` always overrides the default. Pi remains pinned to provider `openai-codex` and model `gpt-5.6-sol`. Claude workers are launched with a Mailbox V2 sidecar that registers as the normal worker agent ID and pastes sanitized mailbox messages into the Claude tmux pane; phase 1 does not scrape Claude replies, so Claude workers reply with `mailbox_send.py`.

## Pi context thresholds and self-compaction

The Mailbox V2 Pi extension monitors `ctx.getContextUsage()` and sends direct in-session notifications when percentage thresholds are crossed. The default policy uses a sensible 40/50/60/70/80/90 ladder for ordinary 272k-token workers, keeps stricter internal defaults for very large context windows, and has tighter built-in defaults for Sol models.

Agents can inspect or change the policy with the `mecha_context` tool (`status`, `set_policy`) and can request self-compaction with `mecha_context` action `compact`. Guidance intentionally says to compact only after a threshold notification or direct user instruction. `postCompactionNotification` is optional: include it while you are in the middle of an active task, review, or decision loop; omit it only when clearly idle/done and no details are needed. A default handoff note can be configured in the compaction policy, and a per-compact call can override it.

Managed Pi launchers accept `--compaction-policy` as simple JSON (`{"levels":[40,50,60,70,80,{"percent":90,"mandatory":true}],"models":{"openai-codex/gpt-5.6-sol":{"levels":[20,30,40,{"percent":45,"mandatory":true}]}}}`) or comma-separated default percentages (`40,50,60,70,80,90`). Optional per-model overrides match normalized keys such as `provider/model-id` or `model-id`; model-specific policy wins over the default policy, and command-level `postCompactionNotification` wins for that compact call. Existing tiered JSON remains accepted for compatibility, but the recommended config surface is default policy plus `models`. Pi does not currently expose a first-class CLI flag for this extension policy; the supported launcher path exports `MECHA_PI_COMPACTION_POLICY` and a launcher fallback `MECHA_PI_MODEL_KEY` for the extension.

## Pi reload

The broken `mecha_reload` tool and `/mecha-reload-runtime` command are retired. Reload is manual only: use Pi's built-in `/reload` or restart the managed worker when an extension/runtime refresh matters, then verify the reload through the UI/status or an observable runtime change.

## Mailbox V2 batching and backpressure

Mailbox V2 keeps each recipient's active mailbox bounded. By default, at most 15 live messages (`queued`, `leased`, or `injection_requested`) are allowed per recipient; when the cap is reached, the router rejects the send with `queue_full`, and `mailbox_send.py` exits non-zero with instructions to back off and wait for the mailbox to clear.

Small non-sentinel messages to the same recipient stay as separate queued rows, then the router combines the currently deliverable rows that fit the delivery limits into one batch when the recipient pulls. Batches can include different senders or subjects, and the combined body includes per-message sender, subject, and reply hints so the recipient can respond to the right agent. Delivery batching is capped by both `maxBatchBodyChars` (default `4096` characters) and the mailbox body byte limit (`maxBodyBytes`); `maxBatchBodyChars` is configurable in `openMessageRepository({ maxBatchBodyChars })` or with `tools/run_router.mjs --max-batch-chars <chars>` / `MECHA_MAILBOX_MAX_BATCH_CHARS`. Set `maxBatchBodyChars` to `0` to disable delivery batching.

## Shared conversation history

Updated web and Android clients opt in with `conversation_sync: "v1"`. The router then allows multiple client instances for one username, emits additive `conversation_message` frames, and exposes `GET /v1/conversations?owner=<id>&peer=<id>` for reconnect hydration. Clients that omit the capability keep the existing single-lease protocol.

Conversation history is stored in the router SQLite database for 30 days, capped at the newest 200 messages per peer pair. The lifecycle sweep enforces both bounds. Accepted image files live in `<database>.attachments` while a retained message references them and are removed with expired/pruned history; unreferenced files retain the existing temporary-file expiry cleanup.
