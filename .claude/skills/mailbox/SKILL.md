---
name: mailbox
description: Mecha-wolfgang async mailbox — delivery format, mandatory pre-read blocks, and tools. Load whenever you act as supervisor or handle worker mail; companion to the supervisor skill.
---

# Mailbox Skill

Async messages between agents live under `~/.mecha-wolfgang/mailboxes/<window-name>/` (one JSON file per message). Delivery to Claude Code is implemented by a **PreToolUse hook** (`mailbox-deliver.py`); idle agents may also receive mail via **`mailbox_cron.py`** into tmux.

## Load ordering

**If you are the supervisor**, load **`supervisor/SKILL.md` first, then this skill** (or read both before taking supervisor actions). The supervisor skill orchestrates workers and state; this skill defines **what counts as mail** and **how to process injected deliveries**.

## When `[MAILBOX]` appears in chat (system-delivered)

The same `[MAILBOX]` text the hook prints to stdout can appear **in this chat as injected context** when your environment bridges mailbox delivery (not only when a human pastes it). Treat it as a **real delivery**:

1. If the block includes **`*** MANDATORY — DO THIS BEFORE READING THE MESSAGE BELOW ***`**, run **`state_update_status.py <alias> "<one-line summary>"`** (or the exact command shown) **before** treating the message body as processed.
2. Then read the message body.
3. Then act (reply, `worker_send`, park, etc.).

Skipping Step 1 when the mandatory block is present is a **protocol violation** — stale `last_checked` / wrong ordering vs. state.

## Canonical specification

Full requirements, JSON schema, hook behavior, and cron behavior:

**`MAILBOX_REQUIREMENTS.md`** at the **mecha-wolfgang repository root** (same repo that contains `.claude/skills/`).

## Tools (supervisor tools path)

| Script | Purpose |
|--------|---------|
| `mailbox_send.py` | Post a message to another window’s mailbox |
| `hooks/mailbox-deliver.py` | PreToolUse delivery (stdout → model; messages marked read) |
| `mailbox_cron.py` | Idle tmux delivery (cron) |

Installed layout: `~/.claude/skills/supervisor/tools/…` (or your mecha-wolfgang checkout).

## Supervisor window identity

For the hook to deliver to the supervisor, **`MECHA_AGENT_WINDOW`** must match the supervisor’s tmux window name (e.g. `supervisor`). Workers use **`MECHA_WORKER_WINDOW`** (set by `worker_launch.py`).

## See also

- **`supervisor`** skill — state, workers, `state_update_status.py`
- **`worker`** skill — worker mailbox reply patterns
