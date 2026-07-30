---
name: mailbox
description: Mecha-wolfgang async mailbox — delivery format, mandatory pre-read blocks, and tools. Load whenever you act as supervisor or handle worker mail; companion to the supervisor skill.
---

# Mailbox Skill

Mailbox V2 sends durable agent messages through the local router. Normal delivery needs no router environment variable: `python3 ~/.claude/skills/supervisor/tools/mailbox_send.py --to <agent> --body "<text>`. It defaults to the local HTTP router; an explicitly configured invalid router URL is rejected.

## Load ordering

**If you are the supervisor**, load **`supervisor/SKILL.md` first, then this skill** (or read both before taking supervisor actions). The supervisor skill orchestrates workers and state; this skill defines **what counts as mail** and **how to process injected deliveries**.

## When `[MAILBOX]` appears in chat (system-delivered)

The same `[MAILBOX]` text the hook prints to stdout can appear **in this chat as injected context** when your environment bridges mailbox delivery (not only when a human pastes it). Treat it as a **real delivery**:

1. If the block includes **`*** MANDATORY — DO THIS BEFORE READING THE MESSAGE BELOW ***`**, run **`state_update_status.py <alias> "<one-line summary>"`** (or the exact command shown) **before** treating the message body as processed.
2. Then read the message body.
3. Then act (reply, `worker_send`, park, etc.).

Skipping Step 1 when the mandatory block is present is a **protocol violation** — stale `last_checked` / wrong ordering vs. state.

## Reply discipline: preserve the channel

When Wolfgang sends a message by mailbox, assume he may be communicating from his phone or another remote UI. If a response is needed, reply by mailbox instead of printing the substantive response only in the terminal/console. Keep the answer in the same channel he used.

## Reply discipline: avoid acknowledgment loops

Do **not** reply to messages that are only acknowledgments, confirmations, or promises to stay parked, unless the message also includes a new instruction, new evidence, a blocker, a question, or a state transition that needs routing. Examples that normally need **no reply**: "Acknowledged", "Confirmed", "I'll stay parked", "I'll wait for review", or restating an instruction back to the supervisor.

When closing a loop, send at most one acknowledgment. If the next message is another pure acknowledgment, do not answer it; update state only if needed.

## Update discipline: avoid noisy small updates

Mailbox is for durable coordination, not streaming every tiny step. Avoid frequent small progress messages that only say you are continuing, starting a command, waiting, or making expected incremental progress.

Send mail when it changes what the recipient can do or needs to know: a blocker, a decision point, a meaningful status transition, a completed validation result, a push/head change, a scoped plan before risky work, or a final/parked handoff. Batch related facts into one useful message instead of several small ones.

If a supervisor or worker has already asked you to stay parked or continue, do not send periodic "still parked" or "still working" messages unless new evidence, a delay/blocker, or a requested checkpoint is due.

## Canonical specification

Full requirements, JSON schema, hook behavior, and cron behavior:

**`MAILBOX_REQUIREMENTS.md`** at the **mecha-developer repository root** (same repo that contains `.claude/skills/`).

## Tools (supervisor tools path)

| Script | Purpose |
|--------|---------|
| `mailbox_send.py` | Post a message to another window’s mailbox |
| `hooks/mailbox-deliver.py` | PreToolUse delivery (stdout → model; messages marked read) |
| `mailbox_cron.py` | Idle tmux delivery (cron) |

Installed layout: `~/.claude/skills/supervisor/tools/…` (or your mecha-developer checkout).

## Supervisor window identity

Agents derive their mailbox identity from the current tmux pane's window name. Name the supervisor window `supervisor` and each worker window with its intended agent ID; names must use letters, digits, `_`, or `-` and start with a letter or digit.

## See also

- **`supervisor`** skill — state, workers, `state_update_status.py`
- **`worker`** skill — worker mailbox reply patterns
