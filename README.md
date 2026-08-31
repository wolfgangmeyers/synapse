# mecha-developer

Personal dev-tooling skills for AI coding agents — supervisor, worker, watchdog, mailbox, reminders, install, decompose, subagent-orchestration, kb, voice, augment-persona, generate-persona, worktree, workflows, test-coverage.

Companion repos:
- `wolfgangmeyers/mecha-review` — self-contained PR review and review-adjacent skills (review-pr, robot-review, pr-summary, pr-validate, pr-context, explain)

## Structure

Skills live under `.claude/skills/<name>/SKILL.md` with optional `tools/` and supporting files. Each skill is a self-contained instruction set any agent (Claude Code, Codex, Cursor) can invoke by reading the file.

## Status

This repo is the home for non-review developer workflow skills.

## Router authority contract

The Mailbox router accepts arbitrary `Host` header authorities after it has bound to its configured listen address. Its HTTP API accepts requests with any browser `Origin`, including preflight requests, and returns the existing wildcard, no-credential CORS headers. Its `/v1/agents` WebSocket accepts any browser `Origin` and any `Host` authority. These are transport decisions, not authentication: the router remains a trusted local development service.

The separate browser UI keeps its existing boundary. It accepts loopback or its configured allowed host, serves the UI only for those hosts, and accepts `/ui/events` only for the matching same-origin HTTP(S) origin. The UI's REST relay continues to forward the request to the router without changing that boundary.

## Managed worker runtime

`worker_launch.py` uses a machine-local default runtime from `~/.mecha-developer/worker-runtime.json` (or `MECHA_WORKER_RUNTIME_CONFIG`) with this shape:

```json
{"default_agent":"pi"}
```

Allowed values are `pi`, `claude`, `codex`, and `cursor`; missing config falls back to `pi`, and explicit `--agent` always overrides the default. Ordinary managed Pi workers and subagents use provider `openai-codex` and model `gpt-5.6-terra`; PR-review workers and their subagents use `gpt-5.6-luna`. Another model requires an explicit task requirement. Claude workers are launched with a Mailbox V2 sidecar that registers as the normal worker agent ID and pastes sanitized mailbox messages into the Claude tmux pane; phase 1 does not scrape Claude replies, so Claude workers reply with `mailbox_send.py`.

## Shell fallbacks and service scripts

The Go Mailbox V2 router is the official router and the default local service. From the repository root, start it with `tools/run_router.sh`. The legacy Node implementation is available only through the explicit `tools/run_router.sh --node` rollback path.

Use the managed Python tools first. When a model-session tool is unavailable, these shell-invoked fallbacks have narrow roles:

| Script | Use | Required setup and boundary |
|---|---|---|
| `.claude/skills/supervisor/tools/worker_launch.py` | Start or resume a managed worker from a shell. | Run with `python3`, `--window`, `--dir`, and the recorded runtime/session. It owns tmux creation, runtime flags, workspace trust, and supported Mailbox setup. Check `--help`, stderr, and `worker_capture.py` after failure. Do not replace it with hand-typed tmux or agent commands. |
| `.claude/skills/supervisor/tools/mailbox_send.py` | Send Mailbox V2 messages when native `mecha_mailbox_send` is unavailable. | Requires the loopback router and an agent-ID recipient. Sender identity comes from the current tmux window; use `--from` only for a verified service or launcher identity. Queue acceptance is not consumption proof. |
| `.claude/skills/supervisor/tools/cron-bootstrap.sh` | Wrap cron jobs that need the user's login environment. | Sources shell profiles and, when present, reads the local Claude OAuth token file into the child process. Use only from cron; never print or copy the token. |
| `.claude/skills/watchdog/tools/watchdog-agent.sh` | Run the optional Codex watchdog wrapper. | Keeps state, logs, locks, and work files under `MECHA_STATE_DIR` (default `~/.mecha-developer`) and validates paths. It is not a worker launcher. |
| `.claude/skills/supervisor/tools/hooks/claude-activity.sh` | Claude Code activity hook used by the sidecar. | Requires `TMUX_PANE` and a live sidecar marker. It must remain fast and exit successfully; it is not a launcher or mailbox sender. |
| `tools/run_router.sh` | Build and start this checkout's Go Mailbox V2 router with its configured database/log paths. | Inspect the existing router and paths before running. Keep the router loopback-only and do not point it at an unknown or shared database. Use `tools/run_router.sh --node` only for an explicit Node rollback. |

If the installed `~/.claude` link is stale, invoke the canonical checkout path and repair installation separately. Never put credentials, tokens, or auth state in commands, logs, task files, or documentation.

## Pi context thresholds and self-compaction

The Mailbox V2 Pi extension monitors `ctx.getContextUsage()` and sends direct in-session notifications when percentage thresholds are crossed. The default policy uses a 40/50/60/70/80/90 ladder for ordinary 272k-token workers and keeps stricter defaults for very large context windows.

A level marked `mandatory: true` is a hard guard. The ordinary default is 90%. At that level, the extension asks Pi to compact, aborts the current run, and blocks later tool calls until compaction finishes or fails. Lower levels keep their existing advisory messages. Pi cancellation is cooperative: it passes an abort signal to the provider and tools, but a custom provider or tool that ignores that signal can delay the idle boundary where compaction begins. The guard checks before provider work, so it cannot compact at the exact instant an in-flight operation crosses the threshold.

Agents can inspect or change the policy with the `mecha_context` tool (`status`, `set_policy`) and can request self-compaction with `mecha_context` action `compact`. Below a mandatory level, guidance says to compact after a threshold notification or direct user instruction. `postCompactionNotification` is optional: include it while you are in the middle of an active task, review, or decision loop; omit it only when clearly idle/done and no details are needed. A default handoff note can be configured in the compaction policy, and a per-compact call can override it.

Managed Pi launchers accept `--compaction-policy` as simple JSON (`{"levels":[40,50,60,70,80,{"percent":90,"mandatory":true}],"models":{"openai-codex/custom-model":{"levels":[20,30,40,{"percent":45,"mandatory":true}]}}}`) or comma-separated default percentages (`40,50,60,70,80,90`). Optional per-model overrides match normalized keys such as `provider/model-id` or `model-id`; model-specific policy wins over the default policy, and command-level `postCompactionNotification` wins for that compact call. Existing tiered JSON remains accepted for compatibility, but the recommended config surface is default policy plus `models`. Pi does not currently expose a first-class CLI flag for this extension policy; the supported launcher path exports `MECHA_PI_COMPACTION_POLICY` and a launcher fallback `MECHA_PI_MODEL_KEY` for the extension.

## Pi reload

The broken `mecha_reload` tool and `/mecha-reload-runtime` command are retired. Reload is manual only: use Pi's built-in `/reload` or restart the managed worker when an extension/runtime refresh matters, then verify the reload through the UI/status or an observable runtime change.

## Mailbox V2 batching and backpressure

Mailbox V2 keeps each recipient's active mailbox bounded. By default, at most 15 live messages (`queued`, `leased`, or `injection_requested`) are allowed per recipient; when the cap is reached, the router rejects the send with `queue_full`, and `mailbox_send.py` exits non-zero with instructions to back off and wait for the mailbox to clear.

Small non-sentinel messages to the same recipient stay as separate queued rows, then the router combines the currently deliverable rows that fit the delivery limits into one batch when the recipient pulls. Batches can include different senders or subjects, and the combined body includes per-message sender, subject, and reply hints so the recipient can respond to the right agent. Delivery batching is capped by both `maxBatchBodyChars` (default `4096` characters) and the mailbox body byte limit (`maxBodyBytes`). Configure the supported Go router with `MECHA_ROUTER_MAX_BATCH_CHARS` when launching `tools/run_router.sh`; set it to `0` to disable delivery batching. The legacy Node rollback reads the same environment variable through that launcher.

## Shared conversation history

Updated web and Android clients opt in with `conversation_sync: "v1"`. The router then allows multiple client instances for one username, emits additive `conversation_message` frames, and exposes `GET /v1/conversations?owner=<id>&peer=<id>` for reconnect hydration. Clients that omit the capability keep the existing single-lease protocol.

Conversation history is stored in the router SQLite database for 30 days, capped at the newest 200 messages per peer pair. The lifecycle sweep enforces both bounds. Accepted image files live in `<database>.attachments` while a retained message references them and are removed with expired/pruned history; unreferenced files retain the existing temporary-file expiry cleanup.
